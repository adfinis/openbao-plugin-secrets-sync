package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/adfinis/openbao-secret-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathData(_ *secretSyncBackend) *framework.Path {
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
				Callback: pathDataWrite,
				Summary:  "Write a new local source secret version.",
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: pathDataWrite,
				Summary:  "Write a new local source secret version.",
			},
			logical.ReadOperation: &framework.PathOperation{
				Callback: pathDataRead,
				Summary:  "Read a local source secret version.",
			},
			logical.DeleteOperation: &framework.PathOperation{
				Callback: pathDataDelete,
				Summary:  "Soft-delete the latest local source secret version.",
			},
		},
		HelpSynopsis:    "Manage local source secret data.",
		HelpDescription: "Stores local source secret versions and enqueues pending sync operations.",
	}
}

func pathDataWrite(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	payload, err := payloadFromField(data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = newMetadataRecordPtr()
	}

	cas, casSet, err := casFromOptions(data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := checkCAS(*metadata, cas, casSet); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	associations, err := enabledAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if err := ensureQueueCapacityFor(ctx, req.Storage, len(associations)); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	nextVersion := metadata.CurrentVersion + 1
	now := nowUTC().Format(timeFormatRFC3339)
	operations, operationIDs := newAssociationOutboxRecords(associations, nextVersion, now)
	if err := putPendingEnqueueIntent(ctx, req.Storage, path, nextVersion, operations, now); err != nil {
		return nil, err
	}

	record := versionRecord{
		Version:     nextVersion,
		CreatedTime: now,
		Data:        payload,
	}
	if err := putVersion(ctx, req.Storage, path, record); err != nil {
		return nil, err
	}
	if err := putOutboxRecords(ctx, req.Storage, operations); err != nil {
		return nil, err
	}
	if err := completeEnqueueIntent(ctx, req.Storage, path, nextVersion, operations, now); err != nil {
		return nil, err
	}

	if metadata.OldestVersion == 0 {
		metadata.OldestVersion = nextVersion
	}
	metadata.CurrentVersion = nextVersion
	metadata.UpdatedTime = now
	metadata.Versions[versionMetadataKey(nextVersion)] = versionMetadata{
		CreatedTime: now,
	}
	if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
		return nil, err
	}

	return &logical.Response{Data: newResponseData(
		responseField("metadata", newResponseData(
			responseField("version", nextVersion),
			responseField("created_time", now),
			responseField("sync_operation_ids", operationIDs),
			responseField("sync_state", string(syncStateForOperationIDs(operationIDs))),
		)),
	)}, nil
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

func pathDataDelete(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
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
	record, err := getVersion(ctx, req.Storage, path, metadata.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Destroyed || record.DeletionTime != "" {
		return nil, nil
	}

	now := nowUTC().Format(timeFormatRFC3339)
	record.DeletionTime = now
	if err := putVersion(ctx, req.Storage, path, *record); err != nil {
		return nil, err
	}
	versionKey := versionMetadataKey(record.Version)
	version := metadata.Versions[versionKey]
	version.DeletionTime = now
	metadata.Versions[versionKey] = version
	metadata.UpdatedTime = now
	if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
		return nil, err
	}
	return nil, nil
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
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return err
	}
	ids, err := listQueuedOutboxIDs(ctx, storage)
	if err != nil {
		return err
	}
	if len(ids)+additionalOperations > cfg.QueueCapacity {
		return fmt.Errorf("sync queue is full: capacity %d", cfg.QueueCapacity)
	}
	return nil
}

func newAssociationOutboxRecords(
	associations []associationRecord,
	version int,
	now string,
) ([]outboxRecord, []string) {
	operations := make([]outboxRecord, 0, len(associations))
	operationIDs := make([]string, 0, len(associations))
	for _, association := range associations {
		operation := newAssociationOutboxRecord(association, version, now)
		operations = append(operations, operation)
		operationIDs = append(operationIDs, operation.ID)
	}
	return operations, operationIDs
}

func putPendingEnqueueIntent(
	ctx context.Context,
	storage logical.Storage,
	path string,
	version int,
	operations []outboxRecord,
	now string,
) error {
	if len(operations) == 0 {
		return nil
	}
	return putEnqueueIntent(ctx, storage, newEnqueueIntentRecord(path, version, operations, now))
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
	now string,
) error {
	if len(operations) == 0 {
		return nil
	}
	intent := newEnqueueIntentRecord(path, version, operations, now)
	intent.Complete = true
	intent.CompletedTime = now
	intent.UpdatedTime = now
	return putEnqueueIntent(ctx, storage, intent)
}

func syncStateForOperationIDs(operationIDs []string) domain.SyncState {
	if len(operationIDs) > 0 {
		return domain.SyncStatePending
	}
	return domain.SyncStateUnknown
}

func newEnqueueIntentRecord(path string, version int, operations []outboxRecord, now string) enqueueIntentRecord {
	return enqueueIntentRecord{
		Path:         path,
		Version:      version,
		OperationIDs: outboxOperationIDs(operations),
		Operations:   enqueueIntentOperations(operations),
		CreatedTime:  now,
		UpdatedTime:  now,
	}
}

func outboxOperationIDs(operations []outboxRecord) []string {
	ids := make([]string, 0, len(operations))
	for _, operation := range operations {
		ids = append(ids, operation.ID)
	}
	return ids
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

func newAssociationOutboxRecord(association associationRecord, version int, now string) outboxRecord {
	id := newOperationID(
		association.Path,
		version,
		association.ID,
		syncObjectIDSecretPath,
		outbox.OperationTypeUpsert,
	)
	return outboxRecord{
		ID:             id,
		Type:           outbox.OperationTypeUpsert,
		Path:           association.Path,
		Version:        version,
		AssociationID:  association.ID,
		ObjectID:       syncObjectIDSecretPath,
		DestinationRef: association.DestinationRef,
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
		IdempotencyKey: association.Path + ":" + strconv.Itoa(version) + ":" + association.ID + ":secret-path:upsert",
	}
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
