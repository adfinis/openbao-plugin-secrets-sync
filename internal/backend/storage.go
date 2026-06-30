package backend

import (
	"context"

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
	if metadata.Versions == nil {
		metadata.Versions = make(map[string]versionMetadata)
	}
	return &metadata, nil
}

func putMetadata(ctx context.Context, storage logical.Storage, path string, metadata metadataRecord) error {
	entry, err := logical.StorageEntryJSON(metadataStorageKey(path), metadata)
	if err != nil {
		return err
	}
	return storage.Put(ctx, entry)
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
