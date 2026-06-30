package backend

import (
	"context"
	"strconv"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func recoverIncompleteEnqueueIntents(ctx context.Context, storage logical.Storage, now time.Time) error {
	intents, err := listEnqueueIntents(ctx, storage)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if intent.Complete {
			continue
		}
		if err := recoverEnqueueIntent(ctx, storage, intent, now.Format(timeFormatRFC3339)); err != nil {
			return err
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
	return completeRecoveredIntent(ctx, storage, intent, now)
}

func recoverableIntentOperations(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
	now string,
	versionAvailable bool,
) ([]outboxRecord, error) {
	if len(intent.Operations) > 0 {
		return outboxRecordsFromIntentOperations(ctx, storage, intent, now, versionAvailable)
	}
	return legacyOutboxRecordsForIntent(ctx, storage, intent, now, versionAvailable)
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
			IdempotencyKey: intent.Path + ":" + strconv.Itoa(intent.Version) + ":" +
				operation.AssociationID + ":" + operation.ObjectID + ":" + string(operation.Type),
		})
	}
	return records, nil
}

func legacyOutboxRecordsForIntent(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
	now string,
	versionAvailable bool,
) ([]outboxRecord, error) {
	if !versionAvailable {
		return nil, nil
	}
	associations, err := listAssociationsForPath(ctx, storage, intent.Path)
	if err != nil {
		return nil, err
	}
	records := []outboxRecord{}
	for _, association := range associations {
		operation := newAssociationOutboxRecord(association, intent.Version, syncObjectIDSecretPath, now)
		for _, id := range intent.OperationIDs {
			if operation.ID == id {
				records = append(records, operation)
				break
			}
		}
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

func completeRecoveredIntent(
	ctx context.Context,
	storage logical.Storage,
	intent enqueueIntentRecord,
	now string,
) error {
	intent.Complete = true
	intent.CompletedTime = now
	intent.UpdatedTime = now
	return putEnqueueIntent(ctx, storage, intent)
}
