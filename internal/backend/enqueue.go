package backend

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func ensureQueueCapacityFor(ctx context.Context, storage logical.Storage, additionalOperations int) error {
	if additionalOperations == 0 {
		return nil
	}
	return ensureQueueCapacityAfterReplacement(ctx, storage, additionalOperations, 0)
}

func ensureQueueCapacityAfterReplacement(
	ctx context.Context,
	storage logical.Storage,
	additionalOperations int,
	replacedOperations int,
) error {
	if additionalOperations <= replacedOperations {
		return nil
	}
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return err
	}
	ids, err := listQueuedOutboxIDs(ctx, storage)
	if err != nil {
		return err
	}
	projectedDepth := len(ids) - replacedOperations + additionalOperations
	if projectedDepth > cfg.QueueCapacity {
		return fmt.Errorf("%w: capacity %d", errQueueCapacity, cfg.QueueCapacity)
	}
	return nil
}

func newAssociationOutboxRecords(
	associations []associationRecord,
	generation string,
	version int,
	payload secretPayload,
	now string,
	options associationOutboxOptions,
) ([]outboxRecord, []string, error) {
	operations := make([]outboxRecord, 0, len(associations))
	operationIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		if options.requireDeleteMode && normalizedDeleteMode(association.DeleteMode) != deleteModeDelete {
			continue
		}
		objectIDs, err := associationObjectIDs(association, payload)
		if err != nil {
			return nil, nil, err
		}
		for _, objectID := range objectIDs {
			operation := newAssociationOutboxRecord(association, generation, version, objectID, now, options)
			operations = append(operations, operation)
			operationIDs = append(operationIDs, operation.ID)
		}
	}
	return operations, operationIDs, nil
}

func additionalQueuedOperationCount(
	ctx context.Context,
	storage logical.Storage,
	operations []outboxRecord,
) (int, error) {
	count := 0
	for _, operation := range operations {
		existing, err := getOutbox(ctx, storage, operation.ID)
		if err != nil {
			return 0, err
		}
		if existing == nil || !isQueuedOutboxState(existing.State) {
			count++
		}
	}
	return count, nil
}

func staleQueuedUpsertIDsForOperations(
	ctx context.Context,
	storage logical.Storage,
	operations []outboxRecord,
	now time.Time,
) ([]string, error) {
	targetVersions := make(map[string]int, len(operations))
	path := ""
	for _, operation := range operations {
		if operation.Type != outbox.OperationTypeUpsert {
			continue
		}
		if path == "" {
			path = operation.Path
		}
		targetVersions[outboxObjectKey(operation.AssociationID, operation.ObjectID)] = operation.Version
	}
	if len(targetVersions) == 0 || path == "" {
		return nil, nil
	}
	ids, err := listQueuedOutboxIDsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	staleIDs := []string{}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil || record.Type != outbox.OperationTypeUpsert {
			continue
		}
		if isOutboxClaimActive(*record, now) {
			continue
		}
		targetVersion, ok := targetVersions[outboxObjectKey(record.AssociationID, record.ObjectID)]
		if !ok || record.Version >= targetVersion {
			continue
		}
		staleIDs = append(staleIDs, record.ID)
	}
	return staleIDs, nil
}

func queuedDeleteIDsForUpsertOperations(
	ctx context.Context,
	storage logical.Storage,
	operations []outboxRecord,
) ([]string, error) {
	targetVersions := make(map[string]int, len(operations))
	path := ""
	for _, operation := range operations {
		if operation.Type != outbox.OperationTypeUpsert {
			continue
		}
		if path == "" {
			path = operation.Path
		}
		targetVersions[outboxObjectKey(operation.AssociationID, operation.ObjectID)] = operation.Version
	}
	if len(targetVersions) == 0 || path == "" {
		return nil, nil
	}
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
		if record == nil || record.Type != outbox.OperationTypeDelete {
			continue
		}
		targetVersion, ok := targetVersions[outboxObjectKey(record.AssociationID, record.ObjectID)]
		if !ok || record.Version != targetVersion {
			continue
		}
		matchingIDs = append(matchingIDs, record.ID)
	}
	return matchingIDs, nil
}

func outboxObjectKey(associationID string, objectID string) string {
	return associationID + "\x00" + objectID
}

func putPendingEnqueueIntent(
	ctx context.Context,
	storage logical.Storage,
	path string,
	generation string,
	version int,
	operations []outboxRecord,
	cancelOperationIDs []string,
	now string,
) error {
	if len(operations) == 0 {
		return nil
	}
	return putEnqueueIntent(ctx, storage, newEnqueueIntentRecord(
		path,
		generation,
		version,
		operations,
		cancelOperationIDs,
		now,
	))
}

func putOutboxRecords(ctx context.Context, storage logical.Storage, operations []outboxRecord) error {
	for _, operation := range operations {
		if err := putOutbox(ctx, storage, operation); err != nil {
			return err
		}
	}
	return nil
}

func completeEnqueueIntent(
	ctx context.Context,
	storage logical.Storage,
	path string,
	version int,
	operations []outboxRecord,
	_ string,
) error {
	if len(operations) == 0 {
		return nil
	}
	return deleteEnqueueIntent(ctx, storage, path, version)
}

func syncStateForOperationIDs(operationIDs []string) domain.SyncState {
	if len(operationIDs) > 0 {
		return domain.SyncStatePending
	}
	return domain.SyncStateNoAssociation
}

func newEnqueueIntentRecord(
	path string,
	generation string,
	version int,
	operations []outboxRecord,
	cancelOperationIDs []string,
	now string,
) enqueueIntentRecord {
	return enqueueIntentRecord{
		Path:               path,
		Generation:         generation,
		Version:            version,
		Operations:         enqueueIntentOperations(operations),
		CancelOperationIDs: uniqueSortedStrings(copyStringSlice(cancelOperationIDs)),
		CreatedTime:        now,
		UpdatedTime:        now,
	}
}

func enqueueIntentOperations(operations []outboxRecord) []enqueueIntentOperation {
	intentOperations := make([]enqueueIntentOperation, 0, len(operations))
	for _, operation := range operations {
		intentOperations = append(intentOperations, enqueueIntentOperation{
			ID:             operation.ID,
			Type:           operation.Type,
			AssociationID:  operation.AssociationID,
			ObjectID:       operation.ObjectID,
			DestinationRef: operation.DestinationRef,
			NotBefore:      operation.NotBefore,
			IdempotencyKey: operation.IdempotencyKey,
			Trigger:        operation.Trigger,
		})
	}
	return intentOperations
}

type associationOutboxOptions struct {
	operationType     outbox.OperationType
	trigger           string
	salt              string
	requireDeleteMode bool
}

func newAssociationOutboxRecord(
	association associationRecord,
	generation string,
	version int,
	objectID string,
	now string,
	options associationOutboxOptions,
) outboxRecord {
	id := newOperationID(
		generation,
		association.Path,
		version,
		association.ID,
		objectID,
		options.operationType,
	)
	if options.salt != "" {
		id = newOperationIDWithSalt(
			generation,
			association.Path,
			version,
			association.ID,
			objectID,
			options.operationType,
			options.salt,
		)
	}
	idempotencyKey := operationIdempotencyKey(
		generation,
		association.Path,
		version,
		association.ID,
		objectID,
		options.operationType,
	)
	if options.salt != "" {
		idempotencyKey += ":" + options.salt
	}
	return outboxRecord{
		ID:             id,
		Type:           options.operationType,
		Path:           association.Path,
		Version:        version,
		AssociationID:  association.ID,
		ObjectID:       objectID,
		DestinationRef: association.DestinationRef,
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
		IdempotencyKey: idempotencyKey,
		Trigger:        options.trigger,
	}
}

func operationIdempotencyKey(
	generation string,
	path string,
	version int,
	associationID string,
	objectID string,
	operationType outbox.OperationType,
) string {
	return generation + ":" +
		path + ":" +
		strconv.Itoa(version) + ":" +
		associationID + ":" +
		objectID + ":" +
		string(operationType)
}

func enabledAssociationsForPath(
	ctx context.Context,
	storage logical.Storage,
	path string,
) ([]associationRecord, error) {
	records, err := listAssociationsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	enabled := make([]associationRecord, 0, len(records))
	for _, record := range records {
		if !record.Enabled {
			continue
		}
		destination, err := getDestination(ctx, storage, record.DestinationType, record.DestinationName)
		if err != nil {
			return nil, err
		}
		if destination == nil || destination.Disabled {
			continue
		}
		enabled = append(enabled, record)
	}
	return enabled, nil
}
