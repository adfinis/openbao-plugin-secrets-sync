package backend

import (
	"context"
	"strings"

	"github.com/openbao/openbao/sdk/v2/logical"
)

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
