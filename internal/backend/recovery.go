package backend

import (
	"context"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func recoverIncompleteEnqueueIntents(ctx context.Context, storage logical.Storage, now time.Time) error {
	return recoverIncompleteEnqueueIntentsLimit(ctx, storage, now, 0)
}

func recoverIncompleteEnqueueIntentsLimit(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
	maxIntents int,
) error {
	intents, err := listEnqueueIntents(ctx, storage)
	if err != nil {
		return err
	}
	recovered := 0
	for _, intent := range intents {
		if err := recoverEnqueueIntent(ctx, storage, intent, now.Format(timeFormatRFC3339)); err != nil {
			return err
		}
		recovered++
		if maxIntents > 0 && recovered >= maxIntents {
			break
		}
	}
	return nil
}

func recoverEnqueueIntent(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
	now string,
) error {
	version, err := getVersion(ctx, storage, intent.Path, intent.Version)
	if err != nil {
		return err
	}
	versionAvailable := version != nil && !version.Destroyed && version.DeletionTime == ""
	if intentSourceMutationCommitted(intent, versionAvailable) {
		if err := cancelQueuedOutboxIDs(ctx, storage, intent.CancelOperationIDs); err != nil {
			return err
		}
	}
	operations, err := recoverableIntentOperations(ctx, storage, intent, now, versionAvailable)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		existing, err := getOutbox(ctx, storage, operation.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			if err := putOutboxIndexes(ctx, storage, *existing); err != nil {
				return err
			}
			continue
		}
		if err := putOutbox(ctx, storage, operation); err != nil {
			return err
		}
	}
	if intentSourceMetadataRecoverable(intent, versionAvailable) {
		if err := recoverIntentSourceMetadata(ctx, storage, intent); err != nil {
			return err
		}
	}
	return pruneRecoveredIntent(ctx, storage, intent)
}

func (b *secretSyncBackend) recoverIncompleteEnqueueIntents(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
) error {
	return b.recoverIncompleteEnqueueIntentsLimit(ctx, storage, now, 0)
}

func (b *secretSyncBackend) recoverIncompleteEnqueueIntentsLimit(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
	maxIntents int,
) error {
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	return recoverIncompleteEnqueueIntentsLimit(ctx, storage, now, maxIntents)
}

func recoverableIntentOperations(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
	now string,
	versionAvailable bool,
) ([]outboxRecord, error) {
	return outboxRecordsFromIntentOperations(ctx, storage, intent, now, versionAvailable)
}

func outboxRecordsFromIntentOperations(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
	now string,
	versionAvailable bool,
) ([]outboxRecord, error) {
	records := make([]outboxRecord, 0, len(intent.Operations))
	for _, operation := range intent.Operations {
		if !shouldRecoverIntentOperation(operation.Type, versionAvailable) {
			continue
		}
		association, err := getAssociation(ctx, storage, intent.Path, operation.AssociationID)
		if err != nil {
			return nil, err
		}
		if association == nil {
			continue
		}
		records = append(records, outboxRecord{
			ID:             operation.ID,
			Type:           operation.Type,
			Path:           intent.Path,
			Version:        intent.Version,
			AssociationID:  operation.AssociationID,
			ObjectID:       operation.ObjectID,
			DestinationRef: operation.DestinationRef,
			State:          outboxStatePending,
			NotBefore:      operation.NotBefore,
			CreatedTime:    intent.CreatedTime,
			UpdatedTime:    now,
			IdempotencyKey: operation.IdempotencyKey,
			Trigger:        operation.Trigger,
		})
	}
	return records, nil
}

func shouldRecoverIntentOperation(operationType outbox.OperationType, versionAvailable bool) bool {
	switch operationType {
	case outbox.OperationTypeUpsert:
		return versionAvailable
	case outbox.OperationTypeDelete:
		return !versionAvailable
	default:
		return false
	}
}

func intentSourceMutationCommitted(intent enqueueIntentRecord, versionAvailable bool) bool {
	for _, operation := range intent.Operations {
		if shouldRecoverIntentOperation(operation.Type, versionAvailable) {
			return true
		}
	}
	return false
}

func intentSourceMetadataRecoverable(intent enqueueIntentRecord, versionAvailable bool) bool {
	if !versionAvailable {
		return false
	}
	for _, operation := range intent.Operations {
		if operation.Type == outbox.OperationTypeUpsert {
			return true
		}
	}
	return false
}

func recoverIntentSourceMetadata(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
) error {
	metadata, err := getMetadata(ctx, storage, intent.Path)
	if err != nil {
		return err
	}
	if metadata == nil {
		record := newMetadataRecord()
		record.Generation = intent.Generation
		metadata = &record
	}
	if metadata.CurrentVersion >= intent.Version {
		return nil
	}
	return commitSourceMetadata(ctx, storage, intent.Path, metadata, intent.Version, intent.CreatedTime)
}

func pruneRecoveredIntent(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
) error {
	return deleteEnqueueIntent(ctx, storage, intent.Path, intent.Version)
}
