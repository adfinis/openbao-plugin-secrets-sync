package backend

import (
	"context"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func recoverIncompleteEnqueueIntents(ctx context.Context, storage logical.Storage, now time.Time) error {
	_, err := recoverIncompleteEnqueueIntentsLimit(ctx, storage, now, 0)
	return err
}

func recoverIncompleteEnqueueIntentsLimit(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
	maxIntents int,
) (int, error) {
	intents, err := listEnqueueIntents(ctx, storage)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, intent := range intents {
		if intent.Complete {
			if err := deleteEnqueueIntent(ctx, storage, intent.Path, intent.Version); err != nil {
				return recovered, err
			}
			recovered++
			if maxIntents > 0 && recovered >= maxIntents {
				break
			}
			continue
		}
		if err := recoverEnqueueIntent(ctx, storage, intent, now.Format(timeFormatRFC3339)); err != nil {
			return recovered, err
		}
		recovered++
		if maxIntents > 0 && recovered >= maxIntents {
			break
		}
	}
	return recovered, nil
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
			continue
		}
		if err := putOutbox(ctx, storage, operation); err != nil {
			return err
		}
	}
	return pruneRecoveredIntent(ctx, storage, intent)
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
			NotBefore:      now,
			CreatedTime:    intent.CreatedTime,
			UpdatedTime:    now,
			IdempotencyKey: operationIdempotencyKey(
				intent.Generation,
				intent.Path,
				intent.Version,
				operation.AssociationID,
				operation.ObjectID,
				operation.Type,
			),
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

func pruneRecoveredIntent(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
) error {
	return deleteEnqueueIntent(ctx, storage, intent.Path, intent.Version)
}
