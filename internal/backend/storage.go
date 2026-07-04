package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

var (
	errQueuedOperationClaimed = errors.New("queued operation is currently claimed")
	errQueueCapacity          = errors.New("sync queue is full")
)

func isQueuedOperationClaimedError(err error) bool {
	return errors.Is(err, errQueuedOperationClaimed)
}

func getMetadata(ctx context.Context, storage logical.Storage, path string) (*metadataRecord, error) {
	entry, err := storage.Get(ctx, metadataStorageKey(path))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var metadata metadataRecord
	if err := entry.DecodeJSON(&metadata); err != nil {
		return nil, err
	}
	normalizeMetadataDefaults(&metadata)
	return &metadata, nil
}

func putMetadata(ctx context.Context, storage logical.Storage, path string, metadata metadataRecord) error {
	entry, err := logical.StorageEntryJSON(metadataStorageKey(path), metadata)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func deleteMetadata(ctx context.Context, storage logical.Storage, path string) error {
	return storage.Delete(ctx, metadataStorageKey(path))
}

func listMetadataKeysPage(
	ctx context.Context,
	storage logical.Storage,
	prefix string,
	pagination listPagination,
) ([]string, error) {
	storagePrefix := metadataStoragePrefix
	if prefix != "" {
		storagePrefix += prefix + "/"
	}
	return storage.ListPage(ctx, storagePrefix, pagination.after, pagination.limit)
}

func getVersion(ctx context.Context, storage logical.Storage, path string, version int) (*versionRecord, error) {
	entry, err := storage.Get(ctx, versionStorageKey(path, version))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record versionRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func putVersion(ctx context.Context, storage logical.Storage, path string, record versionRecord) error {
	entry, err := logical.StorageEntryJSON(versionStorageKey(path, record.Version), record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func deleteVersion(ctx context.Context, storage logical.Storage, path string, version int) error {
	return storage.Delete(ctx, versionStorageKey(path, version))
}

func listVersionKeys(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	return storage.List(ctx, versionStoragePrefix+path+"/versions/")
}

func deleteSourcePath(ctx context.Context, storage logical.Storage, path string) error {
	versionKeys, err := listVersionKeys(ctx, storage, path)
	if err != nil {
		return err
	}
	for _, versionKey := range versionKeys {
		version, err := strconv.Atoi(versionKey)
		if err != nil {
			return err
		}
		if err := deleteVersion(ctx, storage, path, version); err != nil {
			return err
		}
	}
	statusRecords, err := listStatusRecordsForPath(ctx, storage, path)
	if err != nil {
		return err
	}
	for _, record := range statusRecords {
		if err := deleteStatus(ctx, storage, record); err != nil {
			return err
		}
	}
	return deleteMetadata(ctx, storage, path)
}

func normalizeMetadataDefaults(metadata *metadataRecord) {
	if metadata.MaxVersions == 0 {
		metadata.MaxVersions = defaultMaxVersions
	}
	if metadata.DeleteVersionAfter == "" {
		metadata.DeleteVersionAfter = defaultDeleteVersionAfter
	}
	if metadata.CustomMetadata == nil {
		metadata.CustomMetadata = make(map[string]string)
	}
	if metadata.Versions == nil {
		metadata.Versions = make(map[string]versionMetadata)
	}
}

func pruneExcessVersions(ctx context.Context, storage logical.Storage, path string, metadata *metadataRecord) error {
	if metadata.MaxVersions <= 0 || metadata.CurrentVersion == 0 {
		return nil
	}
	keepFrom := metadata.CurrentVersion - metadata.MaxVersions + 1
	if keepFrom <= 1 {
		metadata.OldestVersion = oldestMetadataVersion(metadata)
		return nil
	}
	protectedVersions, err := queuedUpsertVersionsForPath(ctx, storage, path)
	if err != nil {
		return err
	}
	for version := range metadata.Versions {
		versionNumber, err := strconv.Atoi(version)
		if err != nil {
			return err
		}
		if versionNumber >= keepFrom {
			continue
		}
		if _, protected := protectedVersions[versionNumber]; protected {
			continue
		}
		if err := deleteVersion(ctx, storage, path, versionNumber); err != nil {
			return err
		}
		delete(metadata.Versions, version)
	}
	metadata.OldestVersion = oldestMetadataVersion(metadata)
	return nil
}

func oldestMetadataVersion(metadata *metadataRecord) int {
	versions := metadataVersionNumbers(metadata)
	if len(versions) == 0 {
		return 0
	}
	return versions[0]
}

func metadataVersionNumbers(metadata *metadataRecord) []int {
	versions := make([]int, 0, len(metadata.Versions))
	for version := range metadata.Versions {
		versionNumber, err := strconv.Atoi(version)
		if err != nil {
			continue
		}
		versions = append(versions, versionNumber)
	}
	sort.Ints(versions)
	return versions
}

func queuedUpsertVersionsForPath(ctx context.Context, storage logical.Storage, path string) (map[int]struct{}, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	versions := make(map[int]struct{})
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || record.Type != outbox.OperationTypeUpsert {
			continue
		}
		versions[record.Version] = struct{}{}
	}
	return versions, nil
}

func putEnqueueIntent(ctx context.Context, storage logical.Storage, record enqueueIntentRecord) error {
	entry, err := logical.StorageEntryJSON(enqueueIntentStorageKey(record.Path, record.Version), record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func getEnqueueIntent(
	ctx context.Context,
	storage logical.Storage,
	path string,
	version int,
) (*enqueueIntentRecord, error) {
	entry, err := storage.Get(ctx, enqueueIntentStorageKey(path, version))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record enqueueIntentRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func deleteEnqueueIntent(ctx context.Context, storage logical.Storage, path string, version int) error {
	return storage.Delete(ctx, enqueueIntentStorageKey(path, version))
}

func listEnqueueIntents(ctx context.Context, storage logical.Storage) ([]enqueueIntentRecord, error) {
	keys, err := logical.CollectKeysWithPrefix(ctx, storage, enqueueIntentStoragePrefix)
	if err != nil {
		return nil, err
	}
	records := make([]enqueueIntentRecord, 0, len(keys))
	for _, key := range keys {
		entry, err := storage.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			continue
		}
		var record enqueueIntentRecord
		if err := entry.DecodeJSON(&record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func putDestination(ctx context.Context, storage logical.Storage, record destinationRecord) error {
	entry, err := logical.StorageEntryJSON(destinationStorageKey(record.Type, record.Name), record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func getDestination(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) (*destinationRecord, error) {
	entry, err := storage.Get(ctx, destinationStorageKey(destinationType, name))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record destinationRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func deleteDestination(ctx context.Context, storage logical.Storage, destinationType string, name string) error {
	return storage.Delete(ctx, destinationStorageKey(destinationType, name))
}

func listDestinationNamesPage(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	pagination listPagination,
) ([]string, error) {
	return storage.ListPage(ctx, destinationStoragePrefix+destinationType+"/", pagination.after, pagination.limit)
}

func putDestinationSensitiveConfig(
	ctx context.Context,
	storage logical.Storage,
	record destinationSensitiveRecord,
) error {
	entry, err := logical.StorageEntryJSON(destinationSensitiveStorageKey(record.Type, record.Name), record)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func getDestinationSensitiveConfig(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) (*destinationSensitiveRecord, error) {
	entry, err := storage.Get(ctx, destinationSensitiveStorageKey(destinationType, name))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record destinationSensitiveRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func deleteDestinationSensitiveConfig(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) error {
	return storage.Delete(ctx, destinationSensitiveStorageKey(destinationType, name))
}

func putAssociation(ctx context.Context, storage logical.Storage, record associationRecord) error {
	existing, err := getAssociation(ctx, storage, record.Path, record.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := deleteStaleAssociationNameReservations(ctx, storage, *existing, record); err != nil {
			return err
		}
	}
	entry, err := logical.StorageEntryJSON(associationStorageKey(record.Path, record.ID), record)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, entry); err != nil {
		return err
	}
	byDestinationEntry, err := logical.StorageEntryJSON(
		associationByDestinationStorageKey(record.DestinationType, record.DestinationName, record.ID),
		record.Path,
	)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, byDestinationEntry); err != nil {
		return err
	}
	for _, reservationName := range record.reservationNames() {
		reservationEntry, err := logical.StorageEntryJSON(
			associationNameStorageKey(record.DestinationRef, reservationName, record.ID),
			record.Path,
		)
		if err != nil {
			return err
		}
		if err := storage.Put(ctx, reservationEntry); err != nil {
			return err
		}
	}
	return nil
}

func getAssociation(ctx context.Context, storage logical.Storage, path string, id string) (*associationRecord, error) {
	entry, err := storage.Get(ctx, associationStorageKey(path, id))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record associationRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	normalizeAssociationDefaults(&record)
	return &record, nil
}

func normalizeAssociationDefaults(record *associationRecord) {
	if record.DeleteMode == "" {
		record.DeleteMode = defaultDeleteMode
	}
}

func deleteAssociation(ctx context.Context, storage logical.Storage, record associationRecord) error {
	if err := storage.Delete(ctx, associationStorageKey(record.Path, record.ID)); err != nil {
		return err
	}
	if err := storage.Delete(
		ctx,
		associationByDestinationStorageKey(record.DestinationType, record.DestinationName, record.ID),
	); err != nil {
		return err
	}
	for _, reservationName := range record.reservationNames() {
		if err := storage.Delete(ctx, associationNameStorageKey(record.DestinationRef, reservationName, record.ID)); err != nil {
			return err
		}
	}
	return nil
}

func deleteStaleAssociationNameReservations(
	ctx context.Context,
	storage logical.Storage,
	existing associationRecord,
	updated associationRecord,
) error {
	updatedNames := map[string]struct{}{}
	for _, reservationName := range updated.reservationNames() {
		updatedNames[reservationName] = struct{}{}
	}
	for _, reservationName := range existing.reservationNames() {
		if _, ok := updatedNames[reservationName]; ok {
			continue
		}
		if err := storage.Delete(ctx, associationNameStorageKey(existing.DestinationRef, reservationName, existing.ID)); err != nil {
			return err
		}
	}
	return nil
}

func listAssociationIDsForPath(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	return storage.List(ctx, associationStoragePrefix+path+"/")
}

func listAssociationsForPath(ctx context.Context, storage logical.Storage, path string) ([]associationRecord, error) {
	ids, err := listAssociationIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	records := make([]associationRecord, 0, len(ids))
	for _, id := range ids {
		record, err := getAssociation(ctx, storage, path, id)
		if err != nil {
			return nil, err
		}
		if record != nil {
			records = append(records, *record)
		}
	}
	return records, nil
}

func listAssociationIDsForDestination(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) ([]string, error) {
	return storage.List(ctx, associationByDestPrefix+destinationRef(destinationType, name)+"/")
}

func listAssociationNameReservationIDs(
	ctx context.Context,
	storage logical.Storage,
	destinationReference string,
	resolvedName string,
) ([]string, error) {
	return storage.List(ctx, associationNamePrefix+destinationReference+"/"+nameReservationID(resolvedName)+"/")
}

func putOutbox(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	existing, err := getOutbox(ctx, storage, record.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := deleteOutboxIndexes(ctx, storage, *existing); err != nil {
			return err
		}
	}
	entry, err := logical.StorageEntryJSON(outboxStorageKey(record.ID), record)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, entry); err != nil {
		return err
	}
	return putOutboxIndexes(ctx, storage, record)
}

func putOutboxIndexes(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	indexEntry, err := logical.StorageEntryJSON(outboxByPathStorageKey(record.Path, record.ID), record.ID)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, indexEntry); err != nil {
		return err
	}
	if record.State == "" {
		return nil
	}
	stateEntry, err := logical.StorageEntryJSON(outboxByStateStorageKey(record.State, record.ID), record.ID)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, stateEntry); err != nil {
		return err
	}
	if dueTime := outboxDueIndexTime(record); dueTime != "" {
		dueEntry, err := logical.StorageEntryJSON(outboxByDueStorageKey(dueTime, record.ID), record.ID)
		if err != nil {
			return err
		}
		return storage.Put(ctx, dueEntry)
	}
	return nil
}

func deleteOutbox(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	existing, err := getOutbox(ctx, storage, record.ID)
	if err != nil {
		return err
	}
	if err := storage.Delete(ctx, outboxStorageKey(record.ID)); err != nil {
		return err
	}
	if err := deleteOutboxIndexes(ctx, storage, record); err != nil {
		return err
	}
	if existing != nil {
		return deleteOutboxIndexes(ctx, storage, *existing)
	}
	return nil
}

func deleteOutboxIndexes(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	if record.Path != "" {
		if err := storage.Delete(ctx, outboxByPathStorageKey(record.Path, record.ID)); err != nil {
			return err
		}
	}
	if record.State != "" {
		if err := storage.Delete(ctx, outboxByStateStorageKey(record.State, record.ID)); err != nil {
			return err
		}
	}
	if dueTime := outboxDueIndexTime(record); dueTime != "" {
		return storage.Delete(ctx, outboxByDueStorageKey(dueTime, record.ID))
	}
	return nil
}

func getOutbox(ctx context.Context, storage logical.Storage, id string) (*outboxRecord, error) {
	entry, err := storage.Get(ctx, outboxStorageKey(id))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record outboxRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	normalizeOutboxDefaults(&record)
	return &record, nil
}

func normalizeOutboxDefaults(record *outboxRecord) {
	if record.Trigger == "" {
		record.Trigger = outboxTriggerUser
	}
}

func outboxTrigger(record outboxRecord) string {
	if record.Trigger == "" {
		return outboxTriggerUser
	}
	return record.Trigger
}

func listQueuedOutboxIDs(ctx context.Context, storage logical.Storage) ([]string, error) {
	return listOutboxIDsForStates(ctx, storage, outboxStatePending, outboxStateRetryWait)
}

func listOutboxIDsForState(ctx context.Context, storage logical.Storage, state string) ([]string, error) {
	return storage.List(ctx, outboxByStateStoragePrefix+state+"/")
}

func listOutboxIDsForStates(ctx context.Context, storage logical.Storage, states ...string) ([]string, error) {
	ids := []string{}
	for _, state := range states {
		stateIDs, err := listOutboxIDsForState(ctx, storage, state)
		if err != nil {
			return nil, err
		}
		ids = append(ids, stateIDs...)
	}
	return ids, nil
}

func listDueOutboxIDs(ctx context.Context, storage logical.Storage, now time.Time) ([]string, error) {
	keys, err := logical.CollectKeysWithPrefix(ctx, storage, outboxByDueStoragePrefix)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	ids := []string{}
	nowString := now.Format(timeFormatRFC3339)
	for _, key := range keys {
		trimmed := strings.TrimPrefix(key, outboxByDueStoragePrefix)
		dueTime, id, ok := strings.Cut(trimmed, "/")
		if !ok {
			continue
		}
		if dueTime > nowString {
			break
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func nextFutureOutboxDueTime(ctx context.Context, storage logical.Storage, now time.Time) (*time.Time, error) {
	keys, err := logical.CollectKeysWithPrefix(ctx, storage, outboxByDueStoragePrefix)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	nowString := now.Format(timeFormatRFC3339)
	for _, key := range keys {
		trimmed := strings.TrimPrefix(key, outboxByDueStoragePrefix)
		dueTime, _, ok := strings.Cut(trimmed, "/")
		if !ok || dueTime <= nowString {
			continue
		}
		next, err := time.Parse(timeFormatRFC3339, dueTime)
		if err != nil {
			continue
		}
		return &next, nil
	}
	return nil, nil
}

func outboxDueIndexTime(record outboxRecord) string {
	if !isQueuedOutboxState(record.State) {
		return ""
	}
	if _, err := time.Parse(timeFormatRFC3339, record.NotBefore); err == nil {
		return record.NotBefore
	}
	return "0001-01-01T00:00:00Z"
}

func listOutboxIDsForPath(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	return storage.List(ctx, outboxByPathStoragePrefix+path+"/")
}

func deleteQueuedOutboxForAssociation(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
) error {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, association.Path)
	if err != nil {
		return err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || record.AssociationID != association.ID {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
		if err := deleteOutbox(ctx, storage, *record); err != nil {
			return err
		}
	}
	return nil
}

func cancelQueuedOutboxForAssociation(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
) ([]string, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, association.Path)
	if err != nil {
		return nil, err
	}
	canceledIDs := []string{}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || record.AssociationID != association.ID {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return nil, fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
		if err := deleteOutbox(ctx, storage, *record); err != nil {
			return nil, err
		}
		canceledIDs = append(canceledIDs, record.ID)
	}
	return canceledIDs, nil
}

func queuedUpsertIDsForPathVersion(
	ctx context.Context,
	storage logical.Storage,
	path string,
	version int,
) ([]string, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	matchingIDs := []string{}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || record.Version != version || record.Type != outbox.OperationTypeUpsert {
			continue
		}
		matchingIDs = append(matchingIDs, record.ID)
	}
	return matchingIDs, nil
}

func hasQueuedUpsertForAssociationObject(
	ctx context.Context,
	storage logical.Storage,
	path string,
	associationID string,
	objectID string,
	minVersion int,
) (bool, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return false, err
		}
		if record == nil ||
			record.Type != outbox.OperationTypeUpsert ||
			record.AssociationID != associationID ||
			record.ObjectID != objectID {
			continue
		}
		if record.Version >= minVersion {
			return true, nil
		}
	}
	return false, nil
}

func hasQueuedUpsertForAssociationVersion(
	ctx context.Context,
	storage logical.Storage,
	path string,
	associationID string,
	minVersion int,
) (bool, error) {
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return false, err
		}
		if record == nil ||
			record.Type != outbox.OperationTypeUpsert ||
			record.AssociationID != associationID {
			continue
		}
		if record.Version >= minVersion {
			return true, nil
		}
	}
	return false, nil
}

func cancelQueuedOutboxIDs(ctx context.Context, storage logical.Storage, ids []string) error {
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || !isQueuedOutboxState(record.State) {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
		if err := deleteOutbox(ctx, storage, *record); err != nil {
			return err
		}
	}
	return nil
}

func ensureQueuedOutboxIDsUnclaimed(ctx context.Context, storage logical.Storage, ids []string) error {
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || !isQueuedOutboxState(record.State) {
			continue
		}
		if isOutboxClaimActive(*record, nowUTC()) {
			return fmt.Errorf("%w: %s", errQueuedOperationClaimed, record.ID)
		}
	}
	return nil
}

func listQueuedOutboxIDsForPath(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	ids, err := listOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	return filterQueuedOutboxIDs(ctx, storage, ids)
}

func filterQueuedOutboxIDs(ctx context.Context, storage logical.Storage, ids []string) ([]string, error) {
	queued := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		if isQueuedOutboxState(record.State) {
			queued = append(queued, id)
		}
	}
	return queued, nil
}

func isQueuedOutboxState(state string) bool {
	return state == outboxStatePending || state == outboxStateRetryWait
}

func putStatus(ctx context.Context, storage logical.Storage, record statusRecord) error {
	existing, err := getStatus(ctx, storage, record.Path, record.AssociationID, record.ObjectID)
	if err != nil {
		return err
	}
	if existing != nil && record.Version < existing.Version {
		return nil
	}
	if existing != nil {
		preserveStatusDriftBookkeeping(&record, *existing)
	}
	entry, err := logical.StorageEntryJSON(
		statusStorageKey(record.Path, record.AssociationID, record.ObjectID),
		record,
	)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
}

func preserveStatusDriftBookkeeping(record *statusRecord, existing statusRecord) {
	if record.Verification == "" {
		record.Verification = existing.Verification
	}
	if record.LastReconcileTime == "" {
		record.LastReconcileTime = existing.LastReconcileTime
	}
	if record.LastDriftDetectedTime == "" {
		record.LastDriftDetectedTime = existing.LastDriftDetectedTime
	}
	if record.LastRepairTime == "" {
		record.LastRepairTime = existing.LastRepairTime
	}
	if record.RepairCount == 0 {
		record.RepairCount = existing.RepairCount
	}
}

func deleteStatus(ctx context.Context, storage logical.Storage, record statusRecord) error {
	return storage.Delete(ctx, statusStorageKey(record.Path, record.AssociationID, record.ObjectID))
}

func getStatus(
	ctx context.Context,
	storage logical.Storage,
	path string,
	associationID string,
	objectID string,
) (*statusRecord, error) {
	entry, err := storage.Get(ctx, statusStorageKey(path, associationID, objectID))
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var record statusRecord
	if err := entry.DecodeJSON(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func listStatusRecordsForPath(ctx context.Context, storage logical.Storage, path string) ([]statusRecord, error) {
	associationIDs, err := storage.List(ctx, statusStoragePrefix+path+"/")
	if err != nil {
		return nil, err
	}
	records := []statusRecord{}
	for _, associationID := range associationIDs {
		objectIDs, err := storage.List(ctx, statusStoragePrefix+path+"/"+associationID)
		if err != nil {
			return nil, err
		}
		for _, objectID := range objectIDs {
			record, err := getStatus(ctx, storage, path, strings.TrimSuffix(associationID, "/"), objectID)
			if err != nil {
				return nil, err
			}
			if record != nil {
				records = append(records, *record)
			}
		}
	}
	return records, nil
}
