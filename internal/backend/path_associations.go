package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
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
			Description: "Sync granularity. This phase supports secret-path.",
		},
		"format": {
			Type:        framework.TypeString,
			Description: "Payload format. This phase supports json.",
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
	return &logical.Response{Data: newResponseData(
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
		operationID, err := enqueueAssociationCurrentVersion(ctx, req.Storage, *record, *metadata, now)
		if err != nil {
			return logical.ErrorResponse(err.Error()), nil
		}
		operationIDs = append(operationIDs, operationID)
	}
	return &logical.Response{Data: newResponseData(
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
	operationID, err := enqueueAssociationCurrentVersion(ctx, req.Storage, *record, *metadata, now)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	return &logical.Response{Data: newResponseData(
		responseField("association", associationResponse(*record)),
		responseField("sync_operation_ids", []string{operationID}),
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
	if shouldEnqueue {
		if err := ensureQueueCapacityFor(ctx, req.Storage, 1); err != nil {
			return logical.ErrorResponse(err.Error()), nil
		}
	}
	if err := putAssociation(ctx, req.Storage, record); err != nil {
		return nil, err
	}

	operationIDs := []string{}
	if shouldEnqueue {
		operation := newAssociationOutboxRecord(record, metadata.CurrentVersion, nowUTC().Format(timeFormatRFC3339))
		if err := putOutbox(ctx, req.Storage, operation); err != nil {
			return nil, err
		}
		operationIDs = append(operationIDs, operation.ID)
	}

	return &logical.Response{Data: newResponseData(
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
	provider, err := b.providerRegistry.MustGet(record.DestinationType)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	preparedPayload, err := buildCanonicalPayload(record.Format, version.Data)
	if err != nil {
		return logical.ErrorResponse("source payload encoding failed"), nil
	}
	if eligibilityErr := validateAssociationActivation(record, *metadata); eligibilityErr != nil {
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
	plan, providerErr := provider.Plan(ctx, providerPlanRequest(record, metadata.CurrentVersion, preparedPayload))
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
	version int,
	preparedPayload payloadpkg.CanonicalPayload,
) providers.PlanRequest {
	return providers.PlanRequest{
		Destination: providers.DestinationConfig{
			Name: record.DestinationName,
		},
		ResolvedName:  record.ResolvedName,
		Format:        preparedPayload.Format,
		PayloadSHA256: preparedPayload.SHA256,
		PayloadBytes:  len(preparedPayload.Bytes),
		SourcePath:    record.Path,
		SourceVersion: version,
	}
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
) (string, error) {
	version, err := getVersion(ctx, storage, record.Path, metadata.CurrentVersion)
	if err != nil {
		return "", err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return "", fmt.Errorf("current source version is unavailable")
	}
	operation := newAssociationOutboxRecord(record, metadata.CurrentVersion, now)
	existing, err := getOutbox(ctx, storage, operation.ID)
	if err != nil {
		return "", err
	}
	additionalOperations := 1
	if existing != nil && isQueuedOutboxState(existing.State) {
		additionalOperations = 0
	}
	if err := ensureQueueCapacityFor(ctx, storage, additionalOperations); err != nil {
		return "", err
	}
	if existing != nil {
		operation.CreatedTime = existing.CreatedTime
	}
	if err := putOutbox(ctx, storage, operation); err != nil {
		return "", err
	}
	return operation.ID, nil
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
	return putStatus(ctx, storage, *status)
}

func (b *secretSyncBackend) associationRecordFromFieldData(
	ctx context.Context,
	storage logical.Storage,
	path string,
	data *framework.FieldData,
) (associationRecord, error) {
	destinationType := data.Get("destination_type").(string)
	destinationName := data.Get("destination_name").(string)
	provider, err := b.providerRegistry.MustGet(destinationType)
	if err != nil {
		return associationRecord{}, err
	}
	destination, err := getDestination(ctx, storage, destinationType, destinationName)
	if err != nil {
		return associationRecord{}, err
	}
	if destination == nil {
		return associationRecord{}, fmt.Errorf("destination %s/%s does not exist", destinationType, destinationName)
	}
	if destination.Disabled {
		return associationRecord{}, fmt.Errorf("destination %s/%s is disabled", destinationType, destinationName)
	}

	granularity := stringFromField(data, "granularity", syncObjectIDSecretPath)
	format := stringFromField(data, "format", defaultAssociationFormat)
	deleteMode := stringFromField(data, "delete_mode", defaultDeleteMode)
	nameTemplate := stringFromField(data, "name_template", defaultNameTemplate)
	resolvedName := stringFromField(data, "resolved_name", "")
	if resolvedName == "" {
		renderedName, err := renderAssociationName(nameTemplate, path, destinationType, destinationName)
		if err != nil {
			return associationRecord{}, err
		}
		resolvedName = renderedName
	}
	resolvedName = strings.Trim(resolvedName, "/")
	if resolvedName == "" {
		return associationRecord{}, fmt.Errorf("resolved_name must not be empty")
	}
	if err := validateAssociationCapabilities(provider.Capabilities(), granularity, format); err != nil {
		return associationRecord{}, err
	}
	if err := validateDeleteMode(provider.Capabilities(), deleteMode); err != nil {
		return associationRecord{}, err
	}

	id := newAssociationID(path, destinationType, destinationName, resolvedName, granularity)
	destinationReference := destinationRef(destinationType, destinationName)
	reservations, err := listAssociationNameReservationIDs(ctx, storage, destinationReference, resolvedName)
	if err != nil {
		return associationRecord{}, err
	}
	if len(reservations) > 0 && (len(reservations) != 1 || reservations[0] != id) {
		return associationRecord{}, fmt.Errorf(
			"resolved_name %q is already reserved for destination %s",
			resolvedName,
			destinationReference,
		)
	}

	enabled := true
	if value, ok := data.GetOk("enabled"); ok {
		enabled = value.(bool)
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
		Enabled:         enabled,
		CreatedTime:     now,
		UpdatedTime:     now,
	}, nil
}

func validateAssociationCapabilities(capabilities providers.Capabilities, granularity string, format string) error {
	if granularity != syncObjectIDSecretPath {
		return fmt.Errorf("unsupported granularity %q", granularity)
	}
	if format != defaultAssociationFormat {
		return fmt.Errorf("unsupported format %q", format)
	}
	if !capabilities.SupportsSecretPath {
		return fmt.Errorf("destination provider does not support secret-path granularity")
	}
	return nil
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

func renderAssociationName(
	template string,
	path string,
	destinationType string,
	destinationName string,
) (string, error) {
	rendered := strings.NewReplacer(
		"{{ path }}", path,
		"{{ destination.type }}", destinationType,
		"{{ destination.name }}", destinationName,
	).Replace(template)
	if strings.Contains(rendered, "{{") || strings.Contains(rendered, "}}") {
		return "", fmt.Errorf("unsupported name_template %q", template)
	}
	return rendered, nil
}

func stringFromField(data *framework.FieldData, key string, fallback string) string {
	value := strings.TrimSpace(data.Get(key).(string))
	if value == "" {
		return fallback
	}
	return value
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
