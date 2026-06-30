package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/adfinis/openbao-secret-sync/internal/observability"
	payloadpkg "github.com/adfinis/openbao-secret-sync/internal/payload"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathAssociations(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "associations/?",
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback: pathAssociationList,
					Summary:  "List association source path prefixes.",
				},
			},
			HelpSynopsis:    "List associations.",
			HelpDescription: "Lists configured association source path prefixes.",
		},
		{
			Pattern: "associations/(?P<path>.+)/plan",
			Fields:  associationRequestFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationPlan,
					Summary:  "Plan an association sync operation.",
				},
			},
			HelpSynopsis:    "Plan association sync.",
			HelpDescription: "Builds a non-mutating provider plan for the current source version.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>assoc-[0-9a-f]+)/disable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathAssociationDisable,
					Summary:  "Disable an association.",
				},
			},
			HelpSynopsis:    "Disable an association.",
			HelpDescription: "Disables future enqueue and cancels queued work for one association.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>assoc-[0-9a-f]+)/enable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathAssociationEnable,
					Summary:  "Enable an association.",
				},
			},
			HelpSynopsis:    "Enable an association.",
			HelpDescription: "Enables an association and enqueues the current source version when transitioning from disabled.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>assoc-[0-9a-f]+)/sync",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathAssociationSync,
					Summary:  "Manually enqueue association sync.",
				},
			},
			HelpSynopsis:    "Sync an association.",
			HelpDescription: "Enqueues the current source version for one enabled association.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>assoc-[0-9a-f]+)",
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path.",
				},
				"association_id": {
					Type:        framework.TypeString,
					Description: "Association identifier.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.DeleteOperation: &framework.PathOperation{
					Callback: pathAssociationDelete,
					Summary:  "Delete an association.",
				},
			},
			HelpSynopsis:    "Delete associations.",
			HelpDescription: "Deletes one source-to-destination association.",
		},
		{
			Pattern: "associations/" + framework.MatchAllRegex("path"),
			Fields:  associationRequestFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.CreateOperation: &framework.PathOperation{
					Callback: b.pathAssociationWrite,
					Summary:  "Create an association.",
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationWrite,
					Summary:  "Create or update an association.",
				},
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathAssociationRead,
					Summary:  "Read associations for a source path.",
				},
			},
			HelpSynopsis:    "Manage associations.",
			HelpDescription: "Associates source secrets with configured destinations for asynchronous sync.",
		},
	}
}

func associationLifecycleFields() map[string]*framework.FieldSchema {
	return map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
		"association_id": {
			Type:        framework.TypeString,
			Description: "Association identifier.",
		},
	}
}

func associationRequestFields() map[string]*framework.FieldSchema {
	return map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
		"destination_type": {
			Type:        framework.TypeString,
			Description: "Destination provider type.",
		},
		"destination_name": {
			Type:        framework.TypeString,
			Description: "Destination name.",
		},
		"name_template": {
			Type:        framework.TypeString,
			Description: "Destination object name template.",
		},
		"resolved_name": {
			Type:        framework.TypeString,
			Description: "Explicit resolved destination object name.",
		},
		"granularity": {
			Type:        framework.TypeString,
			Description: "Sync granularity: secret-path or secret-key.",
		},
		"format": {
			Type:        framework.TypeString,
			Description: "Payload format: json or raw. Raw requires secret-key granularity.",
		},
		"delete_mode": {
			Type:        framework.TypeString,
			Description: "Remote delete behavior for this association: retain, delete, or orphan.",
		},
		"enabled": {
			Type:        framework.TypeBool,
			Description: "Whether the association should enqueue sync work.",
		},
	}
}

func pathAssociationDisable(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, response, err := associationFromLifecycleRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	record.Enabled = false
	record.UpdatedTime = now
	if err := putAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	canceledOperationIDs, err := cancelQueuedOutboxForAssociation(ctx, req.Storage, *record, now)
	if err != nil {
		return nil, err
	}
	if err := markAssociationStatusDisabled(ctx, req.Storage, *record, now); err != nil {
		return nil, err
	}
	return &logical.Response{Data: associationLifecycleResponse(
		*record,
		responseField("association", associationResponse(*record)),
		responseField("canceled_operation_ids", canceledOperationIDs),
	)}, nil
}

func pathAssociationEnable(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, response, err := associationFromLifecycleRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	metadata, response, err := metadataForAssociationActivation(ctx, req.Storage, *record)
	if response != nil || err != nil {
		return response, err
	}
	if err := validateSourceEligibility(*metadata); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationDestination(ctx, req.Storage, *record); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	wasEnabled := record.Enabled
	now := nowUTC().Format(timeFormatRFC3339)
	record.Enabled = true
	record.UpdatedTime = now
	if err := putAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	operationIDs := []string{}
	if !wasEnabled {
		operationIDs, err = enqueueAssociationCurrentVersion(ctx, req.Storage, *record, *metadata, now)
		if err != nil {
			return logical.ErrorResponse(err.Error()), nil
		}
	}
	return &logical.Response{Data: associationLifecycleResponse(
		*record,
		responseField("association", associationResponse(*record)),
		responseField("sync_operation_ids", operationIDs),
	)}, nil
}

func pathAssociationSync(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, response, err := associationFromLifecycleRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	if !record.Enabled {
		return logical.ErrorResponse("association is disabled"), nil
	}
	metadata, response, err := metadataForAssociationActivation(ctx, req.Storage, *record)
	if response != nil || err != nil {
		return response, err
	}
	if err := validateAssociationActivation(*record, *metadata); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationDestination(ctx, req.Storage, *record); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	operationIDs, err := enqueueAssociationCurrentVersion(ctx, req.Storage, *record, *metadata, now)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	return &logical.Response{Data: associationLifecycleResponse(
		*record,
		responseField("association", associationResponse(*record)),
		responseField("sync_operation_ids", operationIDs),
	)}, nil
}

func (b *secretSyncBackend) pathAssociationWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return logical.ErrorResponse("source path does not exist"), nil
	}

	record, err := b.associationRecordFromFieldData(ctx, req.Storage, path, data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationActivation(record, *metadata); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	existing, err := getAssociation(ctx, req.Storage, path, record.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		record.CreatedTime = existing.CreatedTime
	}

	shouldEnqueue := record.Enabled && existing == nil
	if err := putAssociation(ctx, req.Storage, record); err != nil {
		return nil, err
	}

	operationIDs := []string{}
	if shouldEnqueue {
		operationIDs, err = enqueueAssociationCurrentVersion(
			ctx,
			req.Storage,
			record,
			*metadata,
			nowUTC().Format(timeFormatRFC3339),
		)
		if err != nil {
			return logical.ErrorResponse(err.Error()), nil
		}
	}

	return &logical.Response{Data: associationLifecycleResponse(
		record,
		responseField("association", associationResponse(record)),
		responseField("sync_operation_ids", operationIDs),
	)}, nil
}

func (b *secretSyncBackend) pathAssociationPlan(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, metadata, version, response, err := currentSourceVersionFromPlanRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	record, err := b.associationRecordFromFieldData(ctx, req.Storage, path, data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	destination, err := getDestination(ctx, req.Storage, record.DestinationType, record.DestinationName)
	if err != nil {
		return nil, err
	}
	if destination == nil {
		return logical.ErrorResponse("destination does not exist"), nil
	}
	provider, err := b.providerRegistry.MustGet(record.DestinationType)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	eligibilityErr := validateAssociationActivation(record, *metadata)
	sourceEligible := eligibilityErr == nil
	if record.Granularity == syncGranularitySecretKey {
		return b.pathAssociationSecretKeyPlan(
			ctx,
			req.Storage,
			record,
			*metadata,
			*version,
			*destination,
			provider,
			sourceEligible,
		)
	}
	return b.pathAssociationSecretPathPlan(
		ctx,
		req.Storage,
		record,
		*metadata,
		*version,
		*destination,
		provider,
		eligibilityErr,
	)
}

func (b *secretSyncBackend) pathAssociationSecretPathPlan(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	version versionRecord,
	destination destinationRecord,
	provider providers.Provider,
	eligibilityErr error,
) (*logical.Response, error) {
	preparedPayload, err := buildCanonicalPayloadForObject(
		record.Format,
		version.Data,
		record.Granularity,
		syncObjectIDSecretPath,
	)
	if err != nil {
		return logical.ErrorResponse("source payload encoding failed"), nil
	}
	if eligibilityErr != nil {
		return &logical.Response{Data: associationPlanResponse(
			record,
			metadata.CurrentVersion,
			preparedPayload,
			false,
			providers.PlanResult{
				Action:     providers.PlanActionBlocked,
				Message:    eligibilityErr.Error(),
				ErrorClass: providers.ErrorClassValidation,
			},
		)}, nil
	}
	if limitErr := enforceProviderPayloadLimit(provider.Capabilities(), preparedPayload); limitErr != nil {
		return &logical.Response{Data: associationPlanResponse(
			record,
			metadata.CurrentVersion,
			preparedPayload,
			true,
			providers.PlanResult{
				Action:     providers.PlanActionBlocked,
				Message:    limitErr.Error(),
				ErrorClass: providers.ErrorClassCapacity,
			},
		)}, nil
	}
	resolvedDestinationConfig, err := destinationConfig(ctx, storage, destination)
	if err != nil {
		return nil, err
	}
	planRequest := providerPlanRequest(record, resolvedDestinationConfig, metadata.CurrentVersion, preparedPayload)
	providerStart := time.Now()
	plan, providerErr := provider.Plan(ctx, planRequest)
	b.recordProviderRequest(ctx, provider.Type(), observability.OperationPlan, providerErr, time.Since(providerStart))
	if providerErr != nil {
		return &logical.Response{Data: associationPlanResponse(
			record,
			metadata.CurrentVersion,
			preparedPayload,
			true,
			providers.PlanResult{
				Action:     providers.PlanActionBlocked,
				Message:    providerErr.Error(),
				ErrorClass: providerErrorClass(providerErr),
			},
		)}, nil
	}
	if plan == nil {
		plan = &providers.PlanResult{Action: providers.PlanActionBlocked}
	}
	return &logical.Response{Data: associationPlanResponse(
		record,
		metadata.CurrentVersion,
		preparedPayload,
		true,
		*plan,
	)}, nil
}

func currentSourceVersionFromPlanRequest(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (string, *metadataRecord, *versionRecord, *logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return "", nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return "", nil, nil, logical.ErrorResponse("source path does not exist"), nil
	}
	version, err := getVersion(ctx, req.Storage, path, metadata.CurrentVersion)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return "", nil, nil, logical.ErrorResponse("current source version is unavailable"), nil
	}
	return path, metadata, version, nil, nil
}

func providerPlanRequest(
	record associationRecord,
	destination providers.DestinationConfig,
	version int,
	preparedPayload payloadpkg.CanonicalPayload,
) providers.PlanRequest {
	resolvedName, err := associationResolvedNameForObject(record, syncObjectIDSecretPath)
	if err != nil {
		resolvedName = record.ResolvedName
	}
	return providers.PlanRequest{
		Destination:   destination,
		ResolvedName:  resolvedName,
		Format:        preparedPayload.Format,
		PayloadSHA256: preparedPayload.SHA256,
		PayloadBytes:  len(preparedPayload.Bytes),
		SourcePath:    record.Path,
		SourceVersion: version,
		AssociationID: record.ID,
		ObjectID:      syncObjectIDSecretPath,
	}
}

func (b *secretSyncBackend) pathAssociationSecretKeyPlan(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	version versionRecord,
	destination destinationRecord,
	provider providers.Provider,
	sourceEligible bool,
) (*logical.Response, error) {
	resolvedDestinationConfig, err := destinationConfig(ctx, storage, destination)
	if err != nil {
		return nil, err
	}
	objectIDs, err := associationObjectIDs(record, version.Data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	objects := make([]secretKeyPlanObject, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		object, err := b.planSecretKeyObject(
			ctx,
			record,
			resolvedDestinationConfig,
			provider,
			metadata.CurrentVersion,
			version.Data,
			objectID,
			sourceEligible,
		)
		if err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return &logical.Response{Data: associationSecretKeyPlanResponse(
		record,
		metadata.CurrentVersion,
		sourceEligible,
		objects,
	)}, nil
}

func (b *secretSyncBackend) planSecretKeyObject(
	ctx context.Context,
	record associationRecord,
	destination providers.DestinationConfig,
	provider providers.Provider,
	version int,
	data secretPayload,
	objectID string,
	sourceEligible bool,
) (secretKeyPlanObject, error) {
	payload, err := buildCanonicalPayloadForObject(record.Format, data, record.Granularity, objectID)
	if err != nil {
		return secretKeyPlanObject{
			ObjectID:   objectID,
			Action:     providers.PlanActionBlocked,
			ErrorClass: providers.ErrorClassValidation,
			Message:    "source payload encoding failed",
		}, nil
	}
	resolvedName, err := associationResolvedNameForObject(record, objectID)
	if err != nil {
		return secretKeyPlanObject{}, err
	}
	object := secretKeyPlanObject{
		ObjectID:      objectID,
		ResolvedName:  resolvedName,
		PayloadSHA256: payload.SHA256,
		PayloadBytes:  len(payload.Bytes),
	}
	if !sourceEligible {
		object.Action = providers.PlanActionBlocked
		object.ErrorClass = providers.ErrorClassValidation
		object.Message = "source is not eligible"
		return object, nil
	}
	if limitErr := enforceProviderPayloadLimit(provider.Capabilities(), payload); limitErr != nil {
		object.Action = providers.PlanActionBlocked
		object.ErrorClass = providers.ErrorClassCapacity
		object.Message = limitErr.Error()
		return object, nil
	}
	providerStart := time.Now()
	plan, providerErr := provider.Plan(ctx, providers.PlanRequest{
		Destination:   destination,
		ResolvedName:  resolvedName,
		Format:        payload.Format,
		PayloadSHA256: payload.SHA256,
		PayloadBytes:  len(payload.Bytes),
		SourcePath:    record.Path,
		SourceVersion: version,
		AssociationID: record.ID,
		ObjectID:      objectID,
	})
	b.recordProviderRequest(ctx, provider.Type(), observability.OperationPlan, providerErr, time.Since(providerStart))
	if providerErr != nil {
		object.Action = providers.PlanActionBlocked
		object.ErrorClass = providerErrorClass(providerErr)
		object.Message = providerErr.Error()
		return object, nil
	}
	if plan == nil {
		plan = &providers.PlanResult{Action: providers.PlanActionBlocked}
	}
	object.Action = plan.Action
	object.ErrorClass = plan.ErrorClass
	object.Message = plan.Message
	return object, nil
}

type secretKeyPlanObject struct {
	ObjectID      string
	ResolvedName  string
	PayloadSHA256 string
	PayloadBytes  int
	Action        string
	ErrorClass    providers.ErrorClass
	Message       string
}

func associationSecretKeyPlanResponse(
	record associationRecord,
	version int,
	sourceEligible bool,
	objects []secretKeyPlanObject,
) map[string]interface{} { //nolint:forbidigo
	objectResponses := make([]map[string]interface{}, 0, len(objects)) //nolint:forbidigo
	action := ""
	for index, object := range objects {
		if index == 0 {
			action = object.Action
		} else if action != object.Action {
			action = "multiple"
		}
		objectResponses = append(objectResponses, newResponseData(
			responseField("object_id", object.ObjectID),
			responseField("resolved_name", object.ResolvedName),
			responseField("action", object.Action),
			responseField("payload_sha256", object.PayloadSHA256),
			responseField("payload_bytes", object.PayloadBytes),
			responseField("error_class", string(object.ErrorClass)),
			responseField("message", object.Message),
		))
	}
	return newResponseData(
		responseField("path", record.Path),
		responseField("version", version),
		responseField("action", action),
		responseField("source_eligible", sourceEligible),
		responseField("association_id", record.ID),
		responseField("destination_ref", record.DestinationRef),
		responseField("association", associationResponse(record)),
		responseField("destination", newResponseData(
			responseField("type", record.DestinationType),
			responseField("name", record.DestinationName),
		)),
		responseField("granularity", record.Granularity),
		responseField("objects", objectResponses),
	)
}

func associationPlanResponse(
	record associationRecord,
	version int,
	preparedPayload payloadpkg.CanonicalPayload,
	sourceEligible bool,
	plan providers.PlanResult,
) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("path", record.Path),
		responseField("version", version),
		responseField("action", plan.Action),
		responseField("source_eligible", sourceEligible),
		responseField("association_id", record.ID),
		responseField("destination_ref", record.DestinationRef),
		responseField("association", associationResponse(record)),
		responseField("destination", newResponseData(
			responseField("type", record.DestinationType),
			responseField("name", record.DestinationName),
		)),
		responseField("resolved_name", record.ResolvedName),
		responseField("format", preparedPayload.Format),
		responseField("payload_sha256", preparedPayload.SHA256),
		responseField("payload_bytes", len(preparedPayload.Bytes)),
		responseField("error_class", string(plan.ErrorClass)),
		responseField("message", plan.Message),
	)
}

func associationSummaryFields(record associationRecord) []responseEntry {
	return []responseEntry{
		responseField("association_id", record.ID),
		responseField("destination_ref", record.DestinationRef),
		responseField("resolved_name", record.ResolvedName),
		responseField("granularity", record.Granularity),
		responseField("format", record.Format),
		responseField("delete_mode", normalizedDeleteMode(record.DeleteMode)),
		responseField("enabled", record.Enabled),
	}
}

func associationLifecycleResponse(record associationRecord, fields ...responseEntry) map[string]interface{} {
	return newResponseData(append(associationSummaryFields(record), fields...)...)
}

func pathAssociationRead(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	records, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	associations := make([]map[string]interface{}, 0, len(records)) //nolint:forbidigo
	for _, record := range records {
		associations = append(associations, associationResponse(record))
	}
	return &logical.Response{Data: newResponseData(
		responseField("path", path),
		responseField("associations", associations),
	)}, nil
}

func pathAssociationList(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	keys, err := req.Storage.List(ctx, associationStoragePrefix)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(keys), nil
}

func pathAssociationDelete(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	record, err := getAssociation(ctx, req.Storage, path, data.Get("association_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	if err := deleteQueuedOutboxForAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	if err := deleteAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	return nil, nil
}

func associationFromLifecycleRequest(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*associationRecord, *logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return nil, logical.ErrorResponse(err.Error()), nil
	}
	record, err := getAssociation(ctx, req.Storage, path, data.Get("association_id").(string))
	if err != nil {
		return nil, nil, err
	}
	if record == nil {
		return nil, nil, nil
	}
	return record, nil, nil
}

func metadataForAssociationActivation(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
) (*metadataRecord, *logical.Response, error) {
	metadata, err := getMetadata(ctx, storage, record.Path)
	if err != nil {
		return nil, nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return nil, logical.ErrorResponse("source path does not exist"), nil
	}
	return metadata, nil, nil
}

func validateAssociationDestination(ctx context.Context, storage logical.Storage, record associationRecord) error {
	destination, err := getDestination(ctx, storage, record.DestinationType, record.DestinationName)
	if err != nil {
		return err
	}
	if destination == nil {
		return fmt.Errorf("destination %s does not exist", record.DestinationRef)
	}
	if destination.Disabled {
		return fmt.Errorf("destination %s is disabled", record.DestinationRef)
	}
	return nil
}

func enqueueAssociationCurrentVersion(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	now string,
) ([]string, error) {
	version, err := getVersion(ctx, storage, record.Path, metadata.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return nil, fmt.Errorf("current source version is unavailable")
	}
	operations, operationIDs, err := newAssociationOutboxRecords(
		[]associationRecord{record},
		metadata.CurrentVersion,
		version.Data,
		now,
	)
	if err != nil {
		return nil, err
	}
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
	if err := putOutboxRecords(ctx, storage, operations); err != nil {
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
	if record.Granularity == syncGranularitySecretPath || metadata == nil || metadata.CurrentVersion == 0 {
		return putStatus(ctx, storage, *status)
	}
	version, err := getVersion(ctx, storage, record.Path, metadata.CurrentVersion)
	if err != nil {
		return err
	}
	if version == nil {
		return putStatus(ctx, storage, *status)
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

func (b *secretSyncBackend) associationRecordFromFieldData(
	ctx context.Context,
	storage logical.Storage,
	path string,
	data *framework.FieldData,
) (associationRecord, error) {
	destinationType := data.Get("destination_type").(string)
	destinationName := data.Get("destination_name").(string)
	provider, err := b.associationProvider(ctx, storage, destinationType, destinationName)
	if err != nil {
		return associationRecord{}, err
	}

	granularity := stringFromField(data, "granularity", syncGranularitySecretPath)
	format := stringFromField(data, "format", defaultAssociationFormat)
	deleteMode := stringFromField(data, "delete_mode", defaultDeleteMode)
	nameTemplate := stringFromField(data, "name_template", defaultNameTemplateForGranularity(granularity))
	resolvedName := stringFromField(data, "resolved_name", "")
	resolvedName, reservationName, err := resolveAssociationNames(
		path,
		destinationType,
		destinationName,
		granularity,
		nameTemplate,
		resolvedName,
	)
	if err != nil {
		return associationRecord{}, err
	}
	if err := validateAssociationCapabilities(provider.Capabilities(), granularity, format); err != nil {
		return associationRecord{}, err
	}
	if err := validateDeleteMode(provider.Capabilities(), deleteMode); err != nil {
		return associationRecord{}, err
	}

	id := newAssociationID(path, destinationType, destinationName, reservationName, granularity)
	destinationReference := destinationRef(destinationType, destinationName)
	if err := validateAssociationNameReservation(ctx, storage, destinationReference, reservationName, id); err != nil {
		return associationRecord{}, err
	}

	now := nowUTC().Format(timeFormatRFC3339)
	return associationRecord{
		ID:              id,
		Path:            path,
		DestinationType: destinationType,
		DestinationName: destinationName,
		DestinationRef:  destinationReference,
		NameTemplate:    nameTemplate,
		ResolvedName:    resolvedName,
		Granularity:     granularity,
		Format:          format,
		DeleteMode:      deleteMode,
		Enabled:         boolFromField(data, "enabled", true),
		CreatedTime:     now,
		UpdatedTime:     now,
	}, nil
}

func (b *secretSyncBackend) associationProvider(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	destinationName string,
) (providers.Provider, error) {
	provider, err := b.providerRegistry.MustGet(destinationType)
	if err != nil {
		return nil, err
	}
	destination, err := getDestination(ctx, storage, destinationType, destinationName)
	if err != nil {
		return nil, err
	}
	if destination == nil {
		return nil, fmt.Errorf("destination %s/%s does not exist", destinationType, destinationName)
	}
	if destination.Disabled {
		return nil, fmt.Errorf("destination %s/%s is disabled", destinationType, destinationName)
	}
	return provider, nil
}

func resolveAssociationNames(
	path string,
	destinationType string,
	destinationName string,
	granularity string,
	nameTemplate string,
	resolvedName string,
) (string, string, error) {
	switch granularity {
	case syncGranularitySecretPath:
		resolvedName, err := resolveSecretPathAssociationName(
			path,
			destinationType,
			destinationName,
			nameTemplate,
			resolvedName,
		)
		if err != nil {
			return "", "", err
		}
		return resolvedName, resolvedName, nil
	case syncGranularitySecretKey:
		if err := validateSecretKeyAssociationTemplate(
			path,
			destinationType,
			destinationName,
			nameTemplate,
			resolvedName,
		); err != nil {
			return "", "", err
		}
		return "", nameTemplate, nil
	default:
		return "", "", fmt.Errorf("unsupported granularity %q", granularity)
	}
}

func resolveSecretPathAssociationName(
	path string,
	destinationType string,
	destinationName string,
	nameTemplate string,
	resolvedName string,
) (string, error) {
	if resolvedName == "" {
		renderedName, err := renderAssociationObjectName(
			nameTemplate,
			path,
			destinationType,
			destinationName,
			syncObjectIDSecretPath,
		)
		if err != nil {
			return "", err
		}
		resolvedName = renderedName
	}
	resolvedName = strings.Trim(resolvedName, "/")
	if resolvedName == "" {
		return "", fmt.Errorf("resolved_name must not be empty")
	}
	return resolvedName, nil
}

func validateSecretKeyAssociationTemplate(
	path string,
	destinationType string,
	destinationName string,
	nameTemplate string,
	resolvedName string,
) error {
	if resolvedName != "" {
		return fmt.Errorf("secret-key granularity requires name_template instead of resolved_name")
	}
	if !strings.Contains(nameTemplate, "{{ key }}") {
		return fmt.Errorf("secret-key name_template must include {{ key }}")
	}
	_, err := renderAssociationObjectName(
		nameTemplate,
		path,
		destinationType,
		destinationName,
		"key",
	)
	return err
}

func validateAssociationNameReservation(
	ctx context.Context,
	storage logical.Storage,
	destinationReference string,
	reservationName string,
	associationID string,
) error {
	reservations, err := listAssociationNameReservationIDs(ctx, storage, destinationReference, reservationName)
	if err != nil {
		return err
	}
	if len(reservations) == 0 || (len(reservations) == 1 && reservations[0] == associationID) {
		return nil
	}
	return fmt.Errorf(
		"resolved_name %q is already reserved for destination %s",
		reservationName,
		destinationReference,
	)
}

func validateAssociationCapabilities(capabilities providers.Capabilities, granularity string, format string) error {
	if err := validateAssociationFormat(granularity, format); err != nil {
		return err
	}
	switch granularity {
	case syncGranularitySecretPath:
		if !capabilities.SupportsSecretPath {
			return fmt.Errorf("destination provider does not support secret-path granularity")
		}
	case syncGranularitySecretKey:
		if !capabilities.SupportsSecretKey {
			return fmt.Errorf("destination provider does not support secret-key granularity")
		}
	default:
		return fmt.Errorf("unsupported granularity %q", granularity)
	}
	return nil
}

func validateAssociationFormat(granularity string, format string) error {
	switch format {
	case defaultAssociationFormat:
		return nil
	case rawAssociationFormat:
		if granularity != syncGranularitySecretKey {
			return fmt.Errorf("format %q requires secret-key granularity", rawAssociationFormat)
		}
		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func validateDeleteMode(capabilities providers.Capabilities, deleteMode string) error {
	switch deleteMode {
	case deleteModeRetain, deleteModeOrphan:
		return nil
	case deleteModeDelete:
		if !capabilities.SupportsDeleteIfOwned {
			return fmt.Errorf("delete_mode %q requires provider delete-if-owned capability", deleteMode)
		}
		return nil
	default:
		return fmt.Errorf("unsupported delete_mode %q", deleteMode)
	}
}

func validateAssociationActivation(record associationRecord, metadata metadataRecord) error {
	if !record.Enabled {
		return nil
	}
	return validateSourceEligibility(metadata)
}

func validateSourceEligibility(metadata metadataRecord) error {
	if metadata.CustomMetadata["syncable"] != "true" {
		return fmt.Errorf("source path is not eligible for sync: custom_metadata.syncable must be true")
	}
	return nil
}

func stringFromField(data *framework.FieldData, key string, fallback string) string {
	value := strings.TrimSpace(data.Get(key).(string))
	if value == "" {
		return fallback
	}
	return value
}

func boolFromField(data *framework.FieldData, key string, fallback bool) bool {
	value, ok := data.GetOk(key)
	if !ok {
		return fallback
	}
	return value.(bool)
}

func associationResponse(record associationRecord) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("id", record.ID),
		responseField("path", record.Path),
		responseField("destination", newResponseData(
			responseField("type", record.DestinationType),
			responseField("name", record.DestinationName),
		)),
		responseField("destination_ref", record.DestinationRef),
		responseField("name_template", record.NameTemplate),
		responseField("resolved_name", record.ResolvedName),
		responseField("granularity", record.Granularity),
		responseField("format", record.Format),
		responseField("delete_mode", normalizedDeleteMode(record.DeleteMode)),
		responseField("enabled", record.Enabled),
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
	)
}

func normalizedDeleteMode(deleteMode string) string {
	if deleteMode == "" {
		return defaultDeleteMode
	}
	return deleteMode
}
