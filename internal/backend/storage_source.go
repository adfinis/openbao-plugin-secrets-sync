package backend

import (
	"context"
	"sort"
	"strconv"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
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
