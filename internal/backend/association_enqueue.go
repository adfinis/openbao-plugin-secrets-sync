package backend

import (
	"context"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func (b *secretSyncBackend) enqueueAssociationCurrentVersion(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	now string,
) ([]string, error) {
	return b.enqueueAssociationsCurrentVersion(ctx, storage, []associationRecord{record}, metadata, now)
}

func (b *secretSyncBackend) enqueueAssociationsCurrentVersion(
	ctx context.Context,
	storage logical.Storage,
	records []associationRecord,
	metadata metadataRecord,
	now string,
) ([]string, error) {
	if len(records) == 0 {
		return []string{}, nil
	}
	version, err := currentVersionRecord(ctx, storage, records[0].Path, metadata)
	if err != nil {
		return nil, err
	}
	operations, operationIDs, err := newAssociationOutboxRecords(
		records,
		metadata.Generation,
		metadata.CurrentVersion,
		version.Data,
		now,
		associationOutboxOptions{
			operationType: outbox.OperationTypeUpsert,
			trigger:       outboxTriggerUser,
		},
	)
	if err != nil {
		return nil, err
	}
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()

	additionalOperations, err := additionalQueuedOperationCount(ctx, storage, operations)
	if err != nil {
		return nil, err
	}
	if err := ensureQueueCapacityFor(ctx, storage, additionalOperations); err != nil {
		return nil, err
	}
	for index, operation := range operations {
		existing, err := getOutbox(ctx, storage, operation.ID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			operation.CreatedTime = existing.CreatedTime
			operations[index] = operation
		}
	}
	if err := putPendingEnqueueIntent(
		ctx,
		storage,
		records[0].Path,
		metadata.Generation,
		metadata.CurrentVersion,
		operations,
		nil,
		now,
	); err != nil {
		return nil, err
	}
	if err := putOutboxRecords(ctx, storage, operations); err != nil {
		return nil, err
	}
	if err := completeEnqueueIntent(
		ctx,
		storage,
		records[0].Path,
		metadata.CurrentVersion,
		operations,
		now,
	); err != nil {
		return nil, err
	}
	return operationIDs, nil
}

func (b *secretSyncBackend) enqueueAssociationCurrentVersionAsManualSync(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	now string,
) ([]string, error) {
	return b.enqueueAssociationCurrentVersionWithSalt(
		ctx,
		storage,
		record,
		metadata,
		now,
		"manual",
		false,
		outboxTriggerUser,
	)
}

func (b *secretSyncBackend) enqueueAssociationCurrentVersionAsDriftRepair(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	now string,
) ([]string, error) {
	return b.enqueueAssociationCurrentVersionWithSalt(
		ctx,
		storage,
		record,
		metadata,
		now,
		"repair",
		true,
		outboxTriggerDriftRepair,
	)
}

func (b *secretSyncBackend) enqueueAssociationCurrentVersionWithSalt(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	now string,
	idPrefix string,
	dedupeQueuedCurrentVersion bool,
	trigger string,
) ([]string, error) {
	version, err := currentVersionRecord(ctx, storage, record.Path, metadata)
	if err != nil {
		return nil, err
	}
	salt := bestEffortRuntimeID(idPrefix)
	operations, operationIDs, err := newAssociationOutboxRecords(
		[]associationRecord{record},
		metadata.Generation,
		metadata.CurrentVersion,
		version.Data,
		now,
		associationOutboxOptions{
			operationType: outbox.OperationTypeUpsert,
			trigger:       trigger,
			salt:          salt,
		},
	)
	if err != nil {
		return nil, err
	}
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()

	if dedupeQueuedCurrentVersion {
		queued, err := hasQueuedUpsertForAssociationVersion(
			ctx,
			storage,
			record.Path,
			record.ID,
			metadata.CurrentVersion,
		)
		if err != nil || queued {
			return nil, err
		}
	}
	additionalOperations, err := additionalQueuedOperationCount(ctx, storage, operations)
	if err != nil {
		return nil, err
	}
	if err := ensureQueueCapacityFor(ctx, storage, additionalOperations); err != nil {
		return nil, err
	}
	if err := putPendingEnqueueIntent(
		ctx,
		storage,
		record.Path,
		metadata.Generation,
		metadata.CurrentVersion,
		operations,
		nil,
		now,
	); err != nil {
		return nil, err
	}
	if err := putOutboxRecords(ctx, storage, operations); err != nil {
		return nil, err
	}
	if err := completeEnqueueIntent(
		ctx,
		storage,
		record.Path,
		metadata.CurrentVersion,
		operations,
		now,
	); err != nil {
		return nil, err
	}
	return operationIDs, nil
}

func markAssociationStatusDisabled(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	now string,
) error {
	metadata, err := getMetadata(ctx, storage, record.Path)
	if err != nil {
		return err
	}
	if record.Granularity == syncGranularitySecretPath {
		status, err := getStatus(ctx, storage, record.Path, record.ID, syncObjectIDSecretPath)
		if err != nil {
			return err
		}
		if status == nil {
			status = &statusRecord{
				Path:           record.Path,
				AssociationID:  record.ID,
				ObjectID:       syncObjectIDSecretPath,
				DestinationRef: record.DestinationRef,
				ResolvedName:   record.ResolvedName,
			}
		}
		if metadata != nil {
			status.Version = metadata.CurrentVersion
		}
		status.State = string(domain.SyncStateDisabled)
		status.UpdatedTime = now
		return putStatus(ctx, storage, *status)
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return nil
	}
	version, err := getVersion(ctx, storage, record.Path, metadata.CurrentVersion)
	if err != nil {
		return err
	}
	if version == nil {
		return nil
	}
	objectIDs, err := associationObjectIDs(record, version.Data)
	if err != nil {
		return err
	}
	for _, objectID := range objectIDs {
		resolvedName, err := associationResolvedNameForObject(record, objectID)
		if err != nil {
			return err
		}
		if err := putStatus(ctx, storage, statusRecord{
			Path:           record.Path,
			Version:        metadata.CurrentVersion,
			AssociationID:  record.ID,
			ObjectID:       objectID,
			DestinationRef: record.DestinationRef,
			ResolvedName:   resolvedName,
			State:          string(domain.SyncStateDisabled),
			UpdatedTime:    now,
		}); err != nil {
			return err
		}
	}
	return nil
}
