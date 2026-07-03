package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathData(b *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: "data/" + framework.MatchAllRegex("path"),
		Fields: map[string]*framework.FieldSchema{
			"path": {
				Type:        framework.TypeString,
				Description: "Source secret path.",
			},
			"data": {
				Type:        framework.TypeMap,
				Description: "Source secret payload.",
			},
			"options": {
				Type:        framework.TypeMap,
				Description: "Write options such as CAS.",
			},
			"version": {
				Type:        framework.TypeInt,
				Description: "Version to read. Defaults to the latest version.",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.CreateOperation: &framework.PathOperation{
				Callback: b.pathDataWrite,
				Summary:  "Write a new local source secret version.",
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.pathDataWrite,
				Summary:  "Write a new local source secret version.",
			},
			logical.ReadOperation: &framework.PathOperation{
				Callback: pathDataRead,
				Summary:  "Read a local source secret version.",
			},
			logical.DeleteOperation: &framework.PathOperation{
				Callback: b.pathDataDelete,
				Summary:  "Soft-delete the latest local source secret version.",
			},
		},
		HelpSynopsis:    "Manage local source secret data.",
		HelpDescription: "Stores local source secret versions and enqueues pending sync operations.",
	}
}

func (b *secretSyncBackend) pathDataWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	payload, err := payloadFromField(data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePath(path)
	defer unlock()

	plan, response, err := dataWritePlanFromRequest(ctx, req.Storage, path, payload, data)
	if response != nil || err != nil {
		return response, err
	}
	response, err = b.commitDataWritePlan(ctx, req.Storage, path, payload, plan)
	if response != nil || err != nil {
		return response, err
	}

	return &logical.Response{Data: newResponseData(
		responseField("metadata", newResponseData(
			responseField("version", plan.nextVersion),
			responseField("created_time", plan.now),
			responseField("sync_operation_ids", plan.operationIDs),
			responseField("sync_state", string(syncStateForOperationIDs(plan.operationIDs))),
		)),
	)}, nil
}

type dataWritePlan struct {
	metadata     *metadataRecord
	nextVersion  int
	nowTime      time.Time
	now          string
	operations   []outboxRecord
	operationIDs []string
}

func dataWritePlanFromRequest(
	ctx context.Context,
	storage logical.Storage,
	path string,
	payload secretPayload,
	data *framework.FieldData,
) (dataWritePlan, *logical.Response, error) {
	metadata, err := getMetadata(ctx, storage, path)
	if err != nil {
		return dataWritePlan{}, nil, err
	}
	if metadata == nil {
		metadata = newMetadataRecordPtr()
	}

	cas, casSet, err := casFromOptions(data)
	if err != nil {
		return dataWritePlan{}, logical.ErrorResponse(err.Error()), nil
	}
	if err := checkCAS(*metadata, cas, casSet); err != nil {
		return dataWritePlan{}, logical.ErrorResponse(err.Error()), nil
	}

	associations, err := enabledAssociationsForPath(ctx, storage, path)
	if err != nil {
		return dataWritePlan{}, nil, err
	}
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return dataWritePlan{}, nil, err
	}
	if err := validateSourceEligibility(metadata, cfg); err != nil {
		associations = nil
	}

	nextVersion := metadata.CurrentVersion + 1
	nowTime := nowUTC()
	now := nowTime.Format(timeFormatRFC3339)
	operations, operationIDs, err := newAssociationOutboxRecords(
		associations,
		metadata.Generation,
		nextVersion,
		payload,
		now,
	)
	if err != nil {
		return dataWritePlan{}, logical.ErrorResponse(err.Error()), nil
	}
	return dataWritePlan{
		metadata:     metadata,
		nextVersion:  nextVersion,
		nowTime:      nowTime,
		now:          now,
		operations:   operations,
		operationIDs: operationIDs,
	}, nil, nil
}

func (b *secretSyncBackend) commitDataWritePlan(
	ctx context.Context,
	storage logical.Storage,
	path string,
	payload secretPayload,
	plan dataWritePlan,
) (*logical.Response, error) {
	if len(plan.operations) > 0 {
		b.enqueueMu.Lock()
		defer b.enqueueMu.Unlock()
	}
	staleUpsertIDs, err := staleQueuedUpsertIDsForOperations(ctx, storage, plan.operations, plan.nowTime)
	if err != nil {
		return nil, err
	}
	additionalOperations, err := additionalQueuedOperationCount(ctx, storage, plan.operations)
	if err != nil {
		return nil, err
	}
	if err := ensureQueueCapacityAfterReplacement(
		ctx,
		storage,
		additionalOperations,
		len(staleUpsertIDs),
	); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := putPendingEnqueueIntent(
		ctx,
		storage,
		path,
		plan.metadata.Generation,
		plan.nextVersion,
		plan.operations,
		plan.now,
	); err != nil {
		return nil, err
	}
	if err := putSourceVersionRecord(ctx, storage, path, plan.nextVersion, payload, plan.now); err != nil {
		return nil, err
	}
	if err := cancelQueuedOutboxIDs(ctx, storage, staleUpsertIDs); err != nil {
		return nil, err
	}
	if err := putOutboxRecords(ctx, storage, plan.operations); err != nil {
		return nil, err
	}
	if err := completeEnqueueIntent(ctx, storage, path, plan.nextVersion, plan.operations, plan.now); err != nil {
		return nil, err
	}
	if err := commitSourceMetadata(ctx, storage, path, plan.metadata, plan.nextVersion, plan.now); err != nil {
		return nil, err
	}
	if len(plan.operations) > 0 {
		b.signalEventDispatch()
	}
	return nil, nil
}

func putSourceVersionRecord(
	ctx context.Context,
	storage logical.Storage,
	path string,
	nextVersion int,
	payload secretPayload,
	now string,
) error {
	record := versionRecord{
		Version:     nextVersion,
		CreatedTime: now,
		Data:        payload,
	}
	if err := putVersion(ctx, storage, path, record); err != nil {
		return err
	}
	return nil
}

func commitSourceMetadata(
	ctx context.Context,
	storage logical.Storage,
	path string,
	metadata *metadataRecord,
	nextVersion int,
	now string,
) error {
	if metadata.OldestVersion == 0 {
		metadata.OldestVersion = nextVersion
	}
	metadata.CurrentVersion = nextVersion
	metadata.UpdatedTime = now
	metadata.Versions[versionMetadataKey(nextVersion)] = versionMetadata{
		CreatedTime: now,
	}
	if err := pruneExcessVersions(ctx, storage, path, metadata); err != nil {
		return err
	}
	return putMetadata(ctx, storage, path, *metadata)
}

func pathDataRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return nil, nil
	}

	version := data.Get("version").(int)
	if version == 0 {
		version = metadata.CurrentVersion
	}
	record, err := getVersion(ctx, req.Storage, path, version)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Destroyed || record.DeletionTime != "" {
		return nil, nil
	}

	return &logical.Response{Data: newResponseData(
		responseField("data", record.Data),
		responseField("metadata", newResponseData(
			responseField("version", record.Version),
			responseField("created_time", record.CreatedTime),
			responseField("deletion_time", record.DeletionTime),
			responseField("destroyed", record.Destroyed),
		)),
	)}, nil
}

func (b *secretSyncBackend) pathDataDelete(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePath(path)
	defer unlock()

	metadata, shouldDelete, err := metadataForSourceDelete(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if !shouldDelete {
		return nil, nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	deletePlan, err := buildSourceDeletePlan(ctx, req.Storage, path, metadata.Generation, metadata.CurrentVersion, now)
	if err != nil {
		return nil, err
	}
	if err := ensureQueueCapacityAfterReplacement(
		ctx,
		req.Storage,
		deletePlan.additionalOperations,
		len(deletePlan.staleUpsertIDs),
	); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := ensureQueuedOutboxIDsUnclaimed(ctx, req.Storage, deletePlan.staleUpsertIDs); err != nil {
		if isQueuedOperationClaimedError(err) {
			return logical.ErrorResponse(err.Error()), nil
		}
		return nil, err
	}
	if err := putPendingEnqueueIntent(
		ctx,
		req.Storage,
		path,
		metadata.Generation,
		metadata.CurrentVersion,
		deletePlan.operations,
		now,
	); err != nil {
		return nil, err
	}
	if err := softDeleteVersion(ctx, req.Storage, metadata, path, metadata.CurrentVersion, now); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := cancelQueuedOutboxIDs(ctx, req.Storage, deletePlan.staleUpsertIDs); err != nil {
		return nil, err
	}
	if err := putOutboxRecords(ctx, req.Storage, deletePlan.operations); err != nil {
		return nil, err
	}
	if err := completeEnqueueIntent(
		ctx,
		req.Storage,
		path,
		metadata.CurrentVersion,
		deletePlan.operations,
		now,
	); err != nil {
		return nil, err
	}
	if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
		return nil, err
	}
	if len(deletePlan.operations) > 0 {
		b.signalEventDispatch()
	}
	return &logical.Response{Data: newResponseData(
		responseField("metadata", newResponseData(
			responseField("version", metadata.CurrentVersion),
			responseField("deletion_time", now),
			responseField("sync_operation_ids", deletePlan.operationIDs),
			responseField("sync_state", string(syncStateForOperationIDs(deletePlan.operationIDs))),
		)),
	)}, nil
}

func metadataForSourceDelete(
	ctx context.Context,
	storage logical.Storage,
	path string,
) (*metadataRecord, bool, error) {
	metadata, err := getMetadata(ctx, storage, path)
	if err != nil {
		return nil, false, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return nil, false, nil
	}
	version, err := getVersion(ctx, storage, path, metadata.CurrentVersion)
	if err != nil {
		return nil, false, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return nil, false, nil
	}
	return metadata, true, nil
}

type sourceDeletePlan struct {
	operations           []outboxRecord
	operationIDs         []string
	staleUpsertIDs       []string
	additionalOperations int
}

func buildSourceDeletePlan(
	ctx context.Context,
	storage logical.Storage,
	path string,
	generation string,
	version int,
	now string,
) (sourceDeletePlan, error) {
	associations, err := enabledAssociationsForPath(ctx, storage, path)
	if err != nil {
		return sourceDeletePlan{}, err
	}
	versionRecord, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return sourceDeletePlan{}, err
	}
	if versionRecord == nil {
		return sourceDeletePlan{}, fmt.Errorf("source version is unavailable")
	}
	operations, operationIDs, err := newAssociationDeleteOutboxRecords(
		associations,
		generation,
		version,
		versionRecord.Data,
		now,
	)
	if err != nil {
		return sourceDeletePlan{}, err
	}
	staleUpsertIDs, err := queuedUpsertIDsForPathVersion(ctx, storage, path, version)
	if err != nil {
		return sourceDeletePlan{}, err
	}
	additionalOperations, err := additionalQueuedOperationCount(ctx, storage, operations)
	if err != nil {
		return sourceDeletePlan{}, err
	}
	return sourceDeletePlan{
		operations:           operations,
		operationIDs:         operationIDs,
		staleUpsertIDs:       staleUpsertIDs,
		additionalOperations: additionalOperations,
	}, nil
}

func payloadFromField(data *framework.FieldData) (secretPayload, error) {
	raw := data.Get("data")
	payload, ok := raw.(map[string]interface{}) //nolint:forbidigo // OpenBao framework TypeMap boundary.
	if !ok || len(payload) == 0 {
		return nil, fmt.Errorf("data must contain at least one key")
	}
	copied := make(secretPayload, len(payload))
	for key, value := range payload {
		copied[key] = value
	}
	return copied, nil
}

func casFromOptions(data *framework.FieldData) (int, bool, error) {
	rawOptions := data.Get("options")
	options, ok := rawOptions.(map[string]interface{}) //nolint:forbidigo // OpenBao framework TypeMap boundary.
	if !ok || len(options) == 0 {
		return 0, false, nil
	}
	rawCAS, ok := options["cas"]
	if !ok {
		return 0, false, nil
	}
	cas, err := intFromDynamic(rawCAS)
	if err != nil {
		return 0, false, fmt.Errorf("options.cas must be an integer: %w", err)
	}
	if cas < 0 {
		return 0, false, fmt.Errorf("options.cas must be greater than or equal to zero")
	}
	return cas, true, nil
}

func intFromDynamic(value interface{}) (int, error) { //nolint:forbidigo // OpenBao framework TypeMap boundary.
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("non-integer number %v", typed)
		}
		return int(typed), nil
	case json.Number:
		return strconv.Atoi(typed.String())
	default:
		return 0, fmt.Errorf("unsupported type %T", value)
	}
}

func checkCAS(metadata metadataRecord, cas int, casSet bool) error {
	if !casSet {
		if metadata.CASRequired {
			return fmt.Errorf("check-and-set required for this secret")
		}
		return nil
	}
	switch {
	case cas == 0 && metadata.CurrentVersion != 0:
		return fmt.Errorf("check-and-set failed: secret already exists")
	case cas > 0 && metadata.CurrentVersion != cas:
		return fmt.Errorf(
			"check-and-set failed: current version is %d, expected %d",
			metadata.CurrentVersion,
			cas,
		)
	default:
		return nil
	}
}

func newMetadataRecordPtr() *metadataRecord {
	record := newMetadataRecord()
	return &record
}

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
) ([]outboxRecord, []string, error) {
	operations := make([]outboxRecord, 0, len(associations))
	operationIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		objectIDs, err := associationObjectIDs(association, payload)
		if err != nil {
			return nil, nil, err
		}
		for _, objectID := range objectIDs {
			operation := newAssociationOutboxRecord(association, generation, version, objectID, now)
			operations = append(operations, operation)
			operationIDs = append(operationIDs, operation.ID)
		}
	}
	return operations, operationIDs, nil
}

func newAssociationDriftRepairOutboxRecords(
	association associationRecord,
	generation string,
	version int,
	payload secretPayload,
	now string,
	repairID string,
) ([]outboxRecord, []string, error) {
	objectIDs, err := associationObjectIDs(association, payload)
	if err != nil {
		return nil, nil, err
	}
	operations := make([]outboxRecord, 0, len(objectIDs))
	operationIDs := make([]string, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		operation := newAssociationDriftRepairOutboxRecord(association, generation, version, objectID, now, repairID)
		operations = append(operations, operation)
		operationIDs = append(operationIDs, operation.ID)
	}
	return operations, operationIDs, nil
}

func newAssociationManualSyncOutboxRecords(
	association associationRecord,
	generation string,
	version int,
	payload secretPayload,
	now string,
	syncID string,
) ([]outboxRecord, []string, error) {
	objectIDs, err := associationObjectIDs(association, payload)
	if err != nil {
		return nil, nil, err
	}
	operations := make([]outboxRecord, 0, len(objectIDs))
	operationIDs := make([]string, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		operation := newAssociationManualSyncOutboxRecord(association, generation, version, objectID, now, syncID)
		operations = append(operations, operation)
		operationIDs = append(operationIDs, operation.ID)
	}
	return operations, operationIDs, nil
}

func newAssociationDeleteOutboxRecords(
	associations []associationRecord,
	generation string,
	version int,
	payload secretPayload,
	now string,
) ([]outboxRecord, []string, error) {
	operations := []outboxRecord{}
	operationIDs := []string{}
	for _, association := range associations {
		if normalizedDeleteMode(association.DeleteMode) != deleteModeDelete {
			continue
		}
		objectIDs, err := associationObjectIDs(association, payload)
		if err != nil {
			return nil, nil, err
		}
		for _, objectID := range objectIDs {
			operation := newAssociationDeleteOutboxRecord(association, generation, version, objectID, now)
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
	now string,
) error {
	if len(operations) == 0 {
		return nil
	}
	return putEnqueueIntent(ctx, storage, newEnqueueIntentRecord(path, generation, version, operations, now))
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
	now string,
) enqueueIntentRecord {
	return enqueueIntentRecord{
		Path:        path,
		Generation:  generation,
		Version:     version,
		Operations:  enqueueIntentOperations(operations),
		CreatedTime: now,
		UpdatedTime: now,
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
		})
	}
	return intentOperations
}

func newAssociationOutboxRecord(
	association associationRecord,
	generation string,
	version int,
	objectID string,
	now string,
) outboxRecord {
	id := newOperationID(
		generation,
		association.Path,
		version,
		association.ID,
		objectID,
		outbox.OperationTypeUpsert,
	)
	return outboxRecord{
		ID:             id,
		Type:           outbox.OperationTypeUpsert,
		Path:           association.Path,
		Version:        version,
		AssociationID:  association.ID,
		ObjectID:       objectID,
		DestinationRef: association.DestinationRef,
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
		IdempotencyKey: operationIdempotencyKey(
			generation,
			association.Path,
			version,
			association.ID,
			objectID,
			outbox.OperationTypeUpsert,
		),
		Trigger: outboxTriggerUser,
	}
}

func newAssociationManualSyncOutboxRecord(
	association associationRecord,
	generation string,
	version int,
	objectID string,
	now string,
	syncID string,
) outboxRecord {
	id := newOperationIDWithSalt(
		generation,
		association.Path,
		version,
		association.ID,
		objectID,
		outbox.OperationTypeUpsert,
		syncID,
	)
	return outboxRecord{
		ID:             id,
		Type:           outbox.OperationTypeUpsert,
		Path:           association.Path,
		Version:        version,
		AssociationID:  association.ID,
		ObjectID:       objectID,
		DestinationRef: association.DestinationRef,
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
		IdempotencyKey: operationIdempotencyKey(
			generation,
			association.Path,
			version,
			association.ID,
			objectID,
			outbox.OperationTypeUpsert,
		) + ":" + syncID,
		Trigger: outboxTriggerUser,
	}
}

func newAssociationDriftRepairOutboxRecord(
	association associationRecord,
	generation string,
	version int,
	objectID string,
	now string,
	repairID string,
) outboxRecord {
	id := newOperationIDWithSalt(
		generation,
		association.Path,
		version,
		association.ID,
		objectID,
		outbox.OperationTypeUpsert,
		repairID,
	)
	return outboxRecord{
		ID:             id,
		Type:           outbox.OperationTypeUpsert,
		Path:           association.Path,
		Version:        version,
		AssociationID:  association.ID,
		ObjectID:       objectID,
		DestinationRef: association.DestinationRef,
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
		IdempotencyKey: operationIdempotencyKey(
			generation,
			association.Path,
			version,
			association.ID,
			objectID,
			outbox.OperationTypeUpsert,
		) + ":" + repairID,
		Trigger: outboxTriggerDriftRepair,
	}
}

func newAssociationDeleteOutboxRecord(
	association associationRecord,
	generation string,
	version int,
	objectID string,
	now string,
) outboxRecord {
	id := newOperationID(
		generation,
		association.Path,
		version,
		association.ID,
		objectID,
		outbox.OperationTypeDelete,
	)
	return outboxRecord{
		ID:             id,
		Type:           outbox.OperationTypeDelete,
		Path:           association.Path,
		Version:        version,
		AssociationID:  association.ID,
		ObjectID:       objectID,
		DestinationRef: association.DestinationRef,
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
		IdempotencyKey: operationIdempotencyKey(
			generation,
			association.Path,
			version,
			association.ID,
			objectID,
			outbox.OperationTypeDelete,
		),
		Trigger: outboxTriggerUser,
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
