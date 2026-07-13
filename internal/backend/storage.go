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
	terminalIDs, err := listOutboxIDsForState(ctx, storage, outboxStateFailedTerminal)
	if err != nil {
		return err
	}
	for _, id := range terminalIDs {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || record.Path != path {
			continue
		}
		if err := deleteOutbox(ctx, storage, *record); err != nil {
			return err
		}
	}
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
	normalizeDestinationDefaults(&record)
	return &record, nil
}

func normalizeDestinationDefaults(_ *destinationRecord) {
	// Destination records intentionally have no defaulted stored fields yet.
	// Keep this hook so future destination defaults are backfilled on read
	// instead of freezing zero-value behavior by accident.
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
	version string,
	record destinationSensitiveRecord,
) error {
	key := destinationSensitiveStorageKey(record.Type, record.Name)
	if version != "" {
		key = destinationSensitiveVersionStorageKey(record.Type, record.Name, version)
	}
	entry, err := logical.StorageEntryJSON(key, record)
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
	record, err := getDestination(ctx, storage, destinationType, name)
	if err != nil || record == nil {
		return nil, err
	}
	return getDestinationSensitiveConfigForRecord(ctx, storage, *record)
}

func getDestinationSensitiveConfigForRecord(
	ctx context.Context,
	storage logical.Storage,
	record destinationRecord,
) (*destinationSensitiveRecord, error) {
	if record.SensitiveConfigVersion == destinationSensitiveNone {
		return nil, nil
	}
	key := destinationSensitiveStorageKey(record.Type, record.Name)
	if record.SensitiveConfigVersion != "" {
		key = destinationSensitiveVersionStorageKey(record.Type, record.Name, record.SensitiveConfigVersion)
	}
	entry, err := storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var sensitiveRecord destinationSensitiveRecord
	if err := entry.DecodeJSON(&sensitiveRecord); err != nil {
		return nil, err
	}
	return &sensitiveRecord, nil
}

func deleteDestinationSensitiveConfig(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
) error {
	if err := storage.Delete(ctx, destinationSensitiveStorageKey(destinationType, name)); err != nil {
		return err
	}
	prefix := destinationSensitiveVersionStoragePrefix(destinationType, name)
	versions, err := storage.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if strings.HasSuffix(version, "/") {
			continue
		}
		if err := storage.Delete(ctx, prefix+version); err != nil {
			return err
		}
	}
	return nil
}

func deleteDestinationSensitiveConfigVersion(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
	version string,
) error {
	if version == destinationSensitiveNone {
		return nil
	}
	if version == "" {
		return storage.Delete(ctx, destinationSensitiveStorageKey(destinationType, name))
	}
	return storage.Delete(ctx, destinationSensitiveVersionStorageKey(destinationType, name, version))
}

func putAssociation(ctx context.Context, storage logical.Storage, record associationRecord) error {
	existing, err := getAssociation(ctx, storage, record.Path, record.ID)
	if err != nil {
		return err
	}
	entry, err := logical.StorageEntryJSON(associationStorageKey(record.Path, record.ID), record)
	if err != nil {
		return err
	}
	byDestinationEntry, err := logical.StorageEntryJSON(
		associationByDestinationStorageKey(record.DestinationType, record.DestinationName, record.ID),
		record.Path,
	)
	if err != nil {
		return err
	}
	reservationNames := record.reservationNames()
	reservationEntries := make([]*logical.StorageEntry, 0, len(reservationNames))
	for _, reservationName := range reservationNames {
		reservationEntry, err := logical.StorageEntryJSON(
			associationNameStorageKey(record.DestinationRef, reservationName, record.ID),
			record.Path,
		)
		if err != nil {
			return err
		}
		reservationEntries = append(reservationEntries, reservationEntry)
	}
	if err := storage.Put(ctx, byDestinationEntry); err != nil {
		return err
	}
	for _, reservationEntry := range reservationEntries {
		if err := storage.Put(ctx, reservationEntry); err != nil {
			return err
		}
	}
	// Write secondary indexes before the canonical association record. Readers
	// tolerate stale indexes, but the record must not become visible before its
	// destination and name-reservation indexes exist.
	if err := storage.Put(ctx, entry); err != nil {
		return err
	}
	if existing != nil {
		// Stale indexes are removed only after the replacement association is
		// visible, so association updates do not create a lookup gap.
		return deleteStaleAssociationIndexes(ctx, storage, *existing, record)
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
		key := associationNameStorageKey(record.DestinationRef, reservationName, record.ID)
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func deleteStaleAssociationIndexes(
	ctx context.Context,
	storage logical.Storage,
	existing associationRecord,
	updated associationRecord,
) error {
	if existing.DestinationType != updated.DestinationType || existing.DestinationName != updated.DestinationName {
		if err := storage.Delete(
			ctx,
			associationByDestinationStorageKey(existing.DestinationType, existing.DestinationName, existing.ID),
		); err != nil {
			return err
		}
	}
	updatedReservationKeys := map[string]struct{}{}
	for _, reservationName := range updated.reservationNames() {
		updatedReservationKeys[associationNameStorageKey(updated.DestinationRef, reservationName, updated.ID)] = struct{}{}
	}
	for _, reservationName := range existing.reservationNames() {
		key := associationNameStorageKey(existing.DestinationRef, reservationName, existing.ID)
		if _, ok := updatedReservationKeys[key]; ok {
			continue
		}
		if err := storage.Delete(ctx, key); err != nil {
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
	ids, err := storage.List(ctx, associationByDestPrefix+destinationRef(destinationType, name)+"/")
	if err != nil {
		return nil, err
	}
	liveIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getAssociationFromIndex(
			ctx,
			storage,
			associationByDestinationStorageKey(destinationType, name, id),
			id,
		)
		if err != nil {
			return nil, err
		}
		if record == nil || record.DestinationType != destinationType || record.DestinationName != name {
			continue
		}
		liveIDs = append(liveIDs, id)
	}
	return liveIDs, nil
}

func listAssociationNameReservationIDs(
	ctx context.Context,
	storage logical.Storage,
	destinationReference string,
	resolvedName string,
) ([]string, error) {
	ids, err := storage.List(ctx, associationNamePrefix+destinationReference+"/"+nameReservationID(resolvedName)+"/")
	if err != nil {
		return nil, err
	}
	liveIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getAssociationFromIndex(
			ctx,
			storage,
			associationNameStorageKey(destinationReference, resolvedName, id),
			id,
		)
		if err != nil {
			return nil, err
		}
		if record == nil || record.DestinationRef != destinationReference {
			continue
		}
		if associationReservesName(*record, resolvedName) {
			liveIDs = append(liveIDs, id)
		}
	}
	return liveIDs, nil
}

func getAssociationFromIndex(
	ctx context.Context,
	storage logical.Storage,
	indexKey string,
	id string,
) (*associationRecord, error) {
	entry, err := storage.Get(ctx, indexKey)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var path string
	if err := entry.DecodeJSON(&path); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return getAssociation(ctx, storage, path, id)
}

func associationReservesName(record associationRecord, resolvedName string) bool {
	for _, reservationName := range record.reservationNames() {
		if reservationName == resolvedName {
			return true
		}
	}
	return false
}

func putOutbox(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	existing, err := getOutbox(ctx, storage, record.ID)
	if err != nil {
		return err
	}
	// Write replacement indexes before the canonical record. A failed index or
	// canonical write can then leave only stale indexes, which readers validate
	// against the authoritative canonical record.
	if err := putOutboxIndexes(ctx, storage, record); err != nil {
		return err
	}
	entry, err := logical.StorageEntryJSON(outboxStorageKey(record.ID), record)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, entry); err != nil {
		return err
	}
	if existing != nil {
		return deleteStaleOutboxIndexes(ctx, storage, *existing, record)
	}
	return nil
}

func putOutboxIndexes(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	for _, key := range outboxIndexKeys(record) {
		entry, err := logical.StorageEntryJSON(key, record.ID)
		if err != nil {
			return err
		}
		if err := storage.Put(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

func outboxIndexKeys(record outboxRecord) []string {
	keys := []string{}
	if record.Path != "" {
		keys = append(keys, outboxByPathStorageKey(record.Path, record.ID))
	}
	if record.State != "" {
		keys = append(keys, outboxByStateStorageKey(record.State, record.ID))
	}
	if dueTime := outboxDueIndexTime(record); dueTime != "" {
		keys = append(keys, outboxByDueStorageKey(dueTime, record.ID))
	}
	return keys
}

func deleteStaleOutboxIndexes(
	ctx context.Context,
	storage logical.Storage,
	existing outboxRecord,
	updated outboxRecord,
) error {
	updatedKeys := make(map[string]struct{}, len(outboxIndexKeys(updated)))
	for _, key := range outboxIndexKeys(updated) {
		updatedKeys[key] = struct{}{}
	}
	for _, key := range outboxIndexKeys(existing) {
		if _, retained := updatedKeys[key]; retained {
			continue
		}
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
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
		// The caller may hold an older copy whose state or due-time indexes differ
		// from storage. Delete both views so claimed/retried records do not leave a
		// stale queue index behind.
		return deleteOutboxIndexes(ctx, storage, *existing)
	}
	return nil
}

func deleteOutboxIndexes(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	for _, key := range outboxIndexKeys(record) {
		if err := storage.Delete(ctx, key); err != nil {
			return err
		}
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
	ids, err := storage.List(ctx, outboxByStateStoragePrefix+state+"/")
	if err != nil {
		return nil, err
	}
	return filterOutboxIDs(ctx, storage, ids, func(record outboxRecord) bool {
		return record.State == state
	})
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
		if !ok || id == "" {
			continue
		}
		if dueTime > nowString {
			break
		}
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || outboxDueIndexTime(*record) != dueTime {
			continue
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
		dueTime, id, ok := strings.Cut(trimmed, "/")
		if !ok || id == "" || dueTime <= nowString {
			continue
		}
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || outboxDueIndexTime(*record) != dueTime {
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
	return outboxDueZeroTime
}

func listOutboxIDsForPath(ctx context.Context, storage logical.Storage, path string) ([]string, error) {
	ids, err := storage.List(ctx, outboxByPathStoragePrefix+path+"/")
	if err != nil {
		return nil, err
	}
	return filterOutboxIDs(ctx, storage, ids, func(record outboxRecord) bool {
		return record.Path == path
	})
}

func filterOutboxIDs(
	ctx context.Context,
	storage logical.Storage,
	ids []string,
	matches func(outboxRecord) bool,
) ([]string, error) {
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || !matches(*record) {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered, nil
}

func deleteOutboxForAssociation(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
) error {
	ids, err := listOutboxIDsForPath(ctx, storage, association.Path)
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

func (b *secretSyncBackend) pruneTerminalOutboxRecords(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
) error {
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	_, err := pruneTerminalOutboxRecordsLimit(
		ctx,
		storage,
		now,
		maxRetainedTerminalOutboxRecords,
		defaultTerminalOutboxPruneLimit,
	)
	return err
}

func pruneTerminalOutboxRecordsLimit(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
	maxRecords int,
	expiredLimit int,
) (int, error) {
	records, err := terminalOutboxRecords(ctx, storage)
	if err != nil {
		return 0, err
	}
	sort.Slice(records, func(i int, j int) bool {
		left := terminalOutboxRetentionTime(records[i])
		right := terminalOutboxRetentionTime(records[j])
		if left.Equal(right) {
			return records[i].ID < records[j].ID
		}
		return left.Before(right)
	})
	excess := max(len(records)-maxRecords, 0)
	cutoff := now.Add(-terminalOutboxRetention)
	deleted := 0
	expiredDeleted := 0
	for index, record := range records {
		retentionTime := terminalOutboxRetentionTime(record)
		expired := !retentionTime.IsZero() && !retentionTime.After(cutoff)
		if !shouldPruneTerminalOutboxRecord(index, excess, expired, expiredDeleted, expiredLimit) {
			continue
		}
		if err := deleteOutbox(ctx, storage, record); err != nil {
			return deleted, err
		}
		deleted++
		if expired && index >= excess {
			expiredDeleted++
		}
	}
	return deleted, nil
}

func terminalOutboxRecords(ctx context.Context, storage logical.Storage) ([]outboxRecord, error) {
	ids, err := listOutboxIDsForState(ctx, storage, outboxStateFailedTerminal)
	if err != nil {
		return nil, err
	}
	records := make([]outboxRecord, 0, len(ids))
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record != nil && record.State == outboxStateFailedTerminal {
			records = append(records, *record)
		}
	}
	return records, nil
}

func shouldPruneTerminalOutboxRecord(
	index int,
	excess int,
	expired bool,
	expiredDeleted int,
	expiredLimit int,
) bool {
	if index < excess {
		return true
	}
	if !expired {
		return false
	}
	return expiredLimit <= 0 || expiredDeleted < expiredLimit
}

func terminalOutboxRetentionTime(record outboxRecord) time.Time {
	for _, value := range []string{record.UpdatedTime, record.CreatedTime} {
		parsed, err := time.Parse(timeFormatRFC3339, value)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
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
