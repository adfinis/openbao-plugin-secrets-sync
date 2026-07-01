package backend

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

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

func listMetadataKeys(ctx context.Context, storage logical.Storage, prefix string) ([]string, error) {
	storagePrefix := metadataStoragePrefix
	if prefix != "" {
		storagePrefix += prefix + "/"
	}
	return storage.List(ctx, storagePrefix)
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
	for version := range metadata.Versions {
		versionNumber, err := strconv.Atoi(version)
		if err != nil {
			return err
		}
		if versionNumber >= keepFrom {
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

func listDestinationNames(ctx context.Context, storage logical.Storage, destinationType string) ([]string, error) {
	return storage.List(ctx, destinationStoragePrefix+destinationType+"/")
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
	reservationEntry, err := logical.StorageEntryJSON(
		associationNameStorageKey(record.DestinationRef, record.ResolvedName, record.ID),
		record.Path,
	)
	if err != nil {
		return err
	}
	return storage.Put(ctx, reservationEntry)
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
	return storage.Delete(ctx, associationNameStorageKey(record.DestinationRef, record.ResolvedName, record.ID))
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
	entry, err := logical.StorageEntryJSON(outboxStorageKey(record.ID), record)
	if err != nil {
		return err
	}
	if err := storage.Put(ctx, entry); err != nil {
		return err
	}
	indexEntry, err := logical.StorageEntryJSON(outboxByPathStorageKey(record.Path, record.ID), record.ID)
	if err != nil {
		return err
	}
	return storage.Put(ctx, indexEntry)
}

func deleteOutbox(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	if err := storage.Delete(ctx, outboxStorageKey(record.ID)); err != nil {
		return err
	}
	return storage.Delete(ctx, outboxByPathStorageKey(record.Path, record.ID))
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
	return &record, nil
}

func listOutboxIDs(ctx context.Context, storage logical.Storage) ([]string, error) {
	return storage.List(ctx, outboxStoragePrefix)
}

func listQueuedOutboxIDs(ctx context.Context, storage logical.Storage) ([]string, error) {
	ids, err := listOutboxIDs(ctx, storage)
	if err != nil {
		return nil, err
	}
	return filterQueuedOutboxIDs(ctx, storage, ids)
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
	now string,
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
		record.State = outboxStateCanceled
		record.UpdatedTime = now
		clearOutboxClaim(record)
		if err := putOutbox(ctx, storage, *record); err != nil {
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

func cancelQueuedOutboxIDs(ctx context.Context, storage logical.Storage, ids []string, now string) error {
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil || !isQueuedOutboxState(record.State) {
			continue
		}
		record.State = outboxStateCanceled
		record.UpdatedTime = now
		clearOutboxClaim(record)
		if err := putOutbox(ctx, storage, *record); err != nil {
			return err
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
	entry, err := logical.StorageEntryJSON(
		statusStorageKey(record.Path, record.AssociationID, record.ObjectID),
		record,
	)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
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
