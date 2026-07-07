package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const associationIDPattern = "assoc-[0-9a-f]{32}"

func pathAssociations(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "associations/?",
			Fields:  paginationFields(),
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
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")/disable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationDisable,
					Summary:  "Disable an association.",
				},
			},
			HelpSynopsis:    "Disable an association.",
			HelpDescription: "Disables future enqueue and cancels queued work for one association.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")/enable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationEnable,
					Summary:  "Enable an association.",
				},
			},
			HelpSynopsis:    "Enable an association.",
			HelpDescription: "Enables an association and enqueues the current source version when transitioning from disabled.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")/sync",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationSync,
					Summary:  "Manually enqueue association sync.",
				},
			},
			HelpSynopsis:    "Sync an association.",
			HelpDescription: "Enqueues the current source version for one enabled association.",
		},
		{
			Pattern: "associations/(?P<path>.+)/disable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationDisable,
					Summary:  "Disable an association by destination.",
				},
			},
			HelpSynopsis:    "Disable an association.",
			HelpDescription: "Disables future enqueue and cancels queued work for one association resolved by destination.",
		},
		{
			Pattern: "associations/(?P<path>.+)/enable",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationEnable,
					Summary:  "Enable an association by destination.",
				},
			},
			HelpSynopsis: "Enable an association.",
			HelpDescription: "Enables an association resolved by destination and enqueues the current source version " +
				"when transitioning from disabled.",
		},
		{
			Pattern: "associations/(?P<path>.+)/sync",
			Fields:  associationLifecycleFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathAssociationSync,
					Summary:  "Manually enqueue association sync by destination.",
				},
			},
			HelpSynopsis:    "Sync an association.",
			HelpDescription: "Enqueues the current source version for one enabled association resolved by destination.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>" + associationIDPattern + ")",
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
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathAssociationReadByID,
					Summary:  "Read one association.",
				},
				logical.DeleteOperation: &framework.PathOperation{
					Callback: b.pathAssociationDelete,
					Summary:  "Delete an association.",
				},
			},
			HelpSynopsis:    "Manage one association.",
			HelpDescription: "Reads or deletes one source-to-destination association.",
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
		"destination": {
			Type:        framework.TypeString,
			Description: "Destination reference in <type>/<name> form.",
		},
	}
}

func associationRequestFields() map[string]*framework.FieldSchema {
	return map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
		"destination": {
			Type:        framework.TypeString,
			Description: "Destination reference in <type>/<name> form.",
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
		"data_mapping": {
			Type: framework.TypeString,
			Description: "Destination data mapping mode. payload stores the canonical payload as one destination value; " +
				"source-keys maps top-level source keys into destination-native data keys when supported.",
		},
		"data_key_template": {
			Type: framework.TypeString,
			Description: "Destination-native data key template for data_mapping source-keys. " +
				"Requires {{ key }} and defaults to {{ key }} when source-key data mapping is enabled.",
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

func (b *secretSyncBackend) pathAssociationDisable(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, unlock, response, err := b.associationFromLifecycleRequest(ctx, req, data)
	if unlock != nil {
		defer unlock()
	}
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	b.enqueueMu.Lock()
	canceledOperationIDs, err := cancelQueuedOutboxForAssociation(ctx, req.Storage, *record)
	b.enqueueMu.Unlock()
	if err != nil {
		if isQueuedOperationClaimedError(err) {
			return logical.ErrorResponse(err.Error()), nil
		}
		return nil, err
	}
	record.Enabled = false
	record.UpdatedTime = now
	if err := putAssociation(ctx, req.Storage, *record); err != nil {
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

func (b *secretSyncBackend) pathAssociationEnable(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	mount := requestMountPath(req)
	record, unlock, response, err := b.associationFromLifecycleRequest(ctx, req, data)
	if unlock != nil {
		defer unlock()
	}
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
	activationRecord, response, err := associationRecordForEnable(
		ctx,
		req.Storage,
		mount,
		*record,
		metadata,
	)
	if response != nil || err != nil {
		return response, err
	}
	previous := activationRecord
	wasEnabled := activationRecord.Enabled
	now := nowUTC().Format(timeFormatRFC3339)
	activationRecord.Enabled = true
	activationRecord.UpdatedTime = now
	if err := putAssociation(ctx, req.Storage, activationRecord); err != nil {
		return nil, err
	}
	operationIDs := []string{}
	if !wasEnabled {
		operationIDs, response, err = b.enqueueEnabledAssociationCurrentVersion(
			ctx,
			req.Storage,
			activationRecord,
			metadata,
			previous,
			mount,
			now,
		)
		if response != nil || err != nil {
			return response, err
		}
	}
	return &logical.Response{Data: associationLifecycleResponse(
		activationRecord,
		associationSyncLifecycleFields(mount, activationRecord, operationIDs, wasEnabled && len(operationIDs) == 0)...,
	)}, nil
}

func associationRecordForEnable(
	ctx context.Context,
	storage logical.Storage,
	mount string,
	record associationRecord,
	metadata *metadataRecord,
) (associationRecord, *logical.Response, error) {
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return associationRecord{}, nil, err
	}
	if err := validateSourceEligibility(metadata, cfg); err != nil {
		return associationRecord{}, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	if err := validateAssociationDestination(ctx, storage, record, cfg); err != nil {
		return associationRecord{}, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	version, err := getVersion(ctx, storage, record.Path, metadata.CurrentVersion)
	if err != nil {
		return associationRecord{}, nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return associationRecord{}, logical.ErrorResponse("current source version is unavailable"), nil
	}
	recordWithReservations, err := associationWithConcreteReservationNames(record, version.Data)
	if err != nil {
		return associationRecord{}, logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationNameReservations(
		ctx,
		storage,
		recordWithReservations.DestinationRef,
		recordWithReservations.reservationNames(),
		recordWithReservations.ID,
	); err != nil {
		return associationRecord{}, logical.ErrorResponse(err.Error()), nil
	}
	return recordWithReservations, nil, nil
}

func (b *secretSyncBackend) enqueueEnabledAssociationCurrentVersion(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata *metadataRecord,
	previous associationRecord,
	mount string,
	now string,
) ([]string, *logical.Response, error) {
	operationIDs, err := b.enqueueAssociationCurrentVersion(ctx, storage, record, *metadata, now)
	if err != nil {
		if rollbackErr := rollbackAssociationPersistence(ctx, storage, record, &previous); rollbackErr != nil {
			return nil, nil, rollbackAssociationPersistenceError(err, rollbackErr)
		}
		return nil, errorResponseForOperationError(err, mount), nil
	}
	if len(operationIDs) > 0 {
		b.signalEventDispatch()
	}
	return operationIDs, nil, nil
}

func (b *secretSyncBackend) pathAssociationSync(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	mount := requestMountPath(req)
	record, unlock, response, err := b.associationFromLifecycleRequest(ctx, req, data)
	if unlock != nil {
		defer unlock()
	}
	if response != nil || err != nil {
		return response, err
	}
	if record == nil {
		return nil, nil
	}
	if !record.Enabled {
		return errorResponseWithDiagnostic("association is disabled", associationDisabledDiagnostic(mount, *record)), nil
	}
	metadata, response, err := metadataForAssociationActivation(ctx, req.Storage, *record)
	if response != nil || err != nil {
		return response, err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if err := validateAssociationActivation(*record, metadata, cfg); err != nil {
		return errorResponseWithDiagnostic(err.Error(), validationDiagnosticForAssociation(mount, *record, err.Error())), nil
	}
	if err := validateAssociationDestination(ctx, req.Storage, *record, cfg); err != nil {
		return errorResponseWithDiagnostic(err.Error(), validationDiagnosticForAssociation(mount, *record, err.Error())), nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	operationIDs, err := b.enqueueAssociationCurrentVersionAsManualSync(ctx, req.Storage, *record, *metadata, now)
	if err != nil {
		return errorResponseForOperationError(err, mount), nil
	}
	if len(operationIDs) > 0 {
		b.signalEventDispatch()
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
	baseRecord, response, err := associationUpdateBase(ctx, req, path, data)
	if response != nil || err != nil {
		return response, err
	}
	record, err := b.associationRecordFromFieldData(ctx, req.Storage, path, data, baseRecord)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationIdentityUpdate(baseRecord, record); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePathAssociationNameAndDestination(path, record.DestinationRef, record.reservationName())
	defer unlock()

	mount := requestMountPath(req)
	plan, response, err := b.associationWritePlan(ctx, req.Storage, path, record, mount)
	if response != nil || err != nil {
		return response, err
	}

	return &logical.Response{Data: associationLifecycleResponse(
		plan.record,
		associationSyncLifecycleFields(
			mount,
			plan.record,
			plan.operationIDs,
			plan.existingWasEnabled && plan.record.Enabled && len(plan.operationIDs) == 0,
		)...,
	)}, nil
}

type associationWritePlan struct {
	record             associationRecord
	operationIDs       []string
	existingWasEnabled bool
}

func (b *secretSyncBackend) associationWritePlan(
	ctx context.Context,
	storage logical.Storage,
	path string,
	record associationRecord,
	mount string,
) (associationWritePlan, *logical.Response, error) {
	record, metadata, existing, response, err := associationWritePreflight(ctx, storage, path, record, mount)
	if response != nil || err != nil {
		return associationWritePlan{}, response, err
	}
	existingWasEnabled := existing != nil && existing.Enabled
	if existing != nil {
		record.CreatedTime = existing.CreatedTime
	}
	shouldEnqueue := record.Enabled && (existing == nil || !existing.Enabled)
	if err := putAssociation(ctx, storage, record); err != nil {
		return associationWritePlan{}, nil, err
	}
	operationIDs := []string{}
	if shouldEnqueue {
		operationIDs, err = b.enqueueAssociationCurrentVersion(
			ctx,
			storage,
			record,
			*metadata,
			nowUTC().Format(timeFormatRFC3339),
		)
		if err != nil {
			if rollbackErr := rollbackAssociationPersistence(ctx, storage, record, existing); rollbackErr != nil {
				return associationWritePlan{}, nil, rollbackAssociationPersistenceError(err, rollbackErr)
			}
			return associationWritePlan{}, errorResponseForOperationError(err, mount), nil
		}
		if len(operationIDs) > 0 {
			b.signalEventDispatch()
		}
	}
	return associationWritePlan{
		record:             record,
		operationIDs:       operationIDs,
		existingWasEnabled: existingWasEnabled,
	}, nil, nil
}

func rollbackAssociationPersistence(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	previous *associationRecord,
) error {
	if previous != nil {
		return putAssociation(ctx, storage, *previous)
	}
	return deleteAssociation(ctx, storage, record)
}

func rollbackAssociationPersistenceError(operationErr error, rollbackErr error) error {
	return fmt.Errorf("%w; association rollback failed: %v", operationErr, rollbackErr)
}

func associationWritePreflight(
	ctx context.Context,
	storage logical.Storage,
	path string,
	record associationRecord,
	mount string,
) (associationRecord, *metadataRecord, *associationRecord, *logical.Response, error) {
	metadata, err := getMetadata(ctx, storage, path)
	if err != nil {
		return associationRecord{}, nil, nil, nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return associationRecord{}, nil, nil, logical.ErrorResponse("source path does not exist"), nil
	}
	version, err := getVersion(ctx, storage, path, metadata.CurrentVersion)
	if err != nil {
		return associationRecord{}, nil, nil, nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return associationRecord{}, nil, nil, logical.ErrorResponse("current source version is unavailable"), nil
	}
	record, err = associationWithConcreteReservationNames(record, version.Data)
	if err != nil {
		return associationRecord{}, nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	if err := validateAssociationNameReservations(
		ctx,
		storage,
		record.DestinationRef,
		record.reservationNames(),
		record.ID,
	); err != nil {
		return associationRecord{}, nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return associationRecord{}, nil, nil, nil, err
	}
	if err := validateAssociationActivation(record, metadata, cfg); err != nil {
		return associationRecord{}, nil, nil, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	if err := validateAssociationDestination(ctx, storage, record, cfg); err != nil {
		return associationRecord{}, nil, nil, errorResponseWithDiagnostic(
			err.Error(),
			validationDiagnosticForAssociation(mount, record, err.Error()),
		), nil
	}
	existing, err := getAssociation(ctx, storage, path, record.ID)
	if err != nil {
		return associationRecord{}, nil, nil, nil, err
	}
	return record, metadata, existing, nil, nil
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
	baseRecord, response, err := associationUpdateBase(ctx, req, path, data)
	if response != nil || err != nil {
		return response, err
	}
	record, err := b.associationRecordFromFieldData(ctx, req.Storage, path, data, baseRecord)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
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
	runtimeIdentity, err := providerRuntimeIdentity(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	eligibilityErr := validateAssociationActivation(record, metadata, cfg)
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
			runtimeIdentity,
			sourceEligible,
			cfg,
		)
	}
	preflightErr := eligibilityErr
	if preflightErr == nil {
		preflightErr = validateAssociationDestinationPolicy(*destination, record, version.Data, cfg)
	}
	return b.pathAssociationSecretPathPlan(
		ctx,
		req.Storage,
		record,
		*metadata,
		*version,
		*destination,
		provider,
		runtimeIdentity,
		sourceEligible,
		preflightErr,
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
	runtimeIdentity providers.RuntimeIdentity,
	sourceEligible bool,
	preflightErr error,
) (*logical.Response, error) {
	preparedPayload, err := buildCanonicalPayloadForObject(
		record,
		version.Data,
		syncObjectIDSecretPath,
	)
	if err != nil {
		return logical.ErrorResponse("source payload encoding failed"), nil
	}
	if preflightErr != nil {
		return &logical.Response{Data: associationPlanResponse(
			record,
			metadata.CurrentVersion,
			preparedPayload,
			sourceEligible,
			providers.PlanResult{
				Action:     providers.PlanActionBlocked,
				Message:    preflightErr.Error(),
				ErrorClass: providers.ErrorClassValidation,
			},
		)}, nil
	}
	if limitErr := enforceProviderPayloadLimit(provider.Capabilities(), preparedPayload); limitErr != nil {
		return &logical.Response{Data: associationPlanResponse(
			record,
			metadata.CurrentVersion,
			preparedPayload,
			sourceEligible,
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
	planRequest := providerPlanRequest(
		record,
		runtimeIdentity,
		metadata.CurrentVersion,
		preparedPayload,
	)
	providerStart := time.Now()
	runtime, providerErr := b.destinationRuntime(ctx, provider, destination, resolvedDestinationConfig)
	var plan *providers.PlanResult
	if providerErr == nil {
		plan, providerErr = runtime.Plan(ctx, planRequest)
	}
	b.recordProviderRequest(ctx, provider.Type(), observability.OperationPlan, providerErr, time.Since(providerStart))
	if providerErr != nil {
		return &logical.Response{Data: associationPlanResponse(
			record,
			metadata.CurrentVersion,
			preparedPayload,
			sourceEligible,
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
		sourceEligible,
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
	runtimeIdentity providers.RuntimeIdentity,
	version int,
	preparedPayload payloadpkg.CanonicalPayload,
) providers.PlanRequest {
	resolvedName, err := associationResolvedNameForObject(record, syncObjectIDSecretPath)
	if err != nil {
		resolvedName = record.ResolvedName
	}
	return providers.PlanRequest{
		Runtime:       runtimeIdentity,
		ResolvedName:  resolvedName,
		Format:        preparedPayload.Format,
		PayloadSHA256: preparedPayload.SHA256,
		PayloadBytes:  len(preparedPayload.Bytes),
		DataMap:       normalizedDataMapping(record.DataMapping) == dataMappingSourceKeys,
		DataMapKeys:   dataMapKeys(preparedPayload.Data),
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
	runtimeIdentity providers.RuntimeIdentity,
	sourceEligible bool,
	cfg globalConfig,
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
			destination,
			resolvedDestinationConfig,
			provider,
			runtimeIdentity,
			metadata.CurrentVersion,
			version.Data,
			objectID,
			sourceEligible,
			cfg,
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
	destinationRecord destinationRecord,
	destination providers.DestinationConfig,
	provider providers.Provider,
	runtimeIdentity providers.RuntimeIdentity,
	version int,
	data secretPayload,
	objectID string,
	sourceEligible bool,
	cfg globalConfig,
) (secretKeyPlanObject, error) {
	payload, err := buildCanonicalPayloadForObject(record, data, objectID)
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
	if policyErr := validateDestinationPolicyForObject(
		destinationRecord,
		record,
		objectID,
		resolvedName,
		cfg,
	); policyErr != nil {
		object.Action = providers.PlanActionBlocked
		object.ErrorClass = providers.ErrorClassValidation
		object.Message = policyErr.Error()
		return object, nil
	}
	if limitErr := enforceProviderPayloadLimit(provider.Capabilities(), payload); limitErr != nil {
		object.Action = providers.PlanActionBlocked
		object.ErrorClass = providers.ErrorClassCapacity
		object.Message = limitErr.Error()
		return object, nil
	}
	providerStart := time.Now()
	runtime, providerErr := b.destinationRuntime(ctx, provider, destinationRecord, destination)
	var plan *providers.PlanResult
	if providerErr == nil {
		plan, providerErr = runtime.Plan(ctx, providers.PlanRequest{
			Runtime:       runtimeIdentity,
			ResolvedName:  resolvedName,
			Format:        payload.Format,
			PayloadSHA256: payload.SHA256,
			PayloadBytes:  len(payload.Bytes),
			DataMap:       false,
			SourcePath:    record.Path,
			SourceVersion: version,
			AssociationID: record.ID,
			ObjectID:      objectID,
		})
	}
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
		responseField("data_mapping", normalizedDataMapping(record.DataMapping)),
		responseField("data_key_template", record.DataKeyTemplate),
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
		responseField("data_mapping", normalizedDataMapping(record.DataMapping)),
		responseField("data_key_template", record.DataKeyTemplate),
		responseField("delete_mode", normalizedDeleteMode(record.DeleteMode)),
		responseField("enabled", record.Enabled),
	}
}

func associationLifecycleResponse(record associationRecord, fields ...responseEntry) map[string]interface{} {
	return newResponseData(append(associationSummaryFields(record), fields...)...)
}

func associationSyncLifecycleFields(
	mount string,
	record associationRecord,
	operationIDs []string,
	includeManualSyncHint bool,
) []responseEntry {
	fields := []responseEntry{
		responseField("association", associationResponse(record)),
		responseField("sync_operation_ids", operationIDs),
	}
	if includeManualSyncHint {
		fields = append(fields, diagnosticResponseFields(associationAlreadyEnabledDiagnostic(mount, record))...)
	}
	return fields
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

func pathAssociationReadByID(
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
	return &logical.Response{Data: associationResponse(*record)}, nil
}

func pathAssociationList(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	pagination := listPaginationFromFieldData(data)
	keys, err := req.Storage.ListPage(ctx, associationStoragePrefix, pagination.after, pagination.limit)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(keys), nil
}

func (b *secretSyncBackend) pathAssociationDelete(
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
	unlock := b.lockSourcePathAndAssociationName(path, record.DestinationRef, record.reservationName())
	defer unlock()

	record, err = getAssociation(ctx, req.Storage, path, data.Get("association_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	b.enqueueMu.Lock()
	if err := deleteQueuedOutboxForAssociation(ctx, req.Storage, *record); err != nil {
		b.enqueueMu.Unlock()
		if isQueuedOperationClaimedError(err) {
			return logical.ErrorResponse(err.Error()), nil
		}
		return nil, err
	}
	b.enqueueMu.Unlock()
	if err := deleteAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	return nil, nil
}

func (b *secretSyncBackend) associationFromLifecycleRequest(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*associationRecord, func(), *logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePath(path)
	associationID := strings.TrimSpace(data.Get("association_id").(string))
	if associationID == "" {
		destinationType, destinationName, err := associationDestinationFromFieldData(data, true)
		if err != nil {
			unlock()
			return nil, nil, logical.ErrorResponse(err.Error()), nil
		}
		record, response, err := associationForDestination(ctx, req.Storage, path, destinationType, destinationName)
		if response != nil || err != nil {
			unlock()
			return nil, nil, response, err
		}
		return record, unlock, nil, nil
	}
	record, err := getAssociation(ctx, req.Storage, path, associationID)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	if record == nil {
		unlock()
		return nil, nil, nil, nil
	}
	return record, unlock, nil, nil
}

func associationForDestination(
	ctx context.Context,
	storage logical.Storage,
	path string,
	destinationType string,
	destinationName string,
) (*associationRecord, *logical.Response, error) {
	records, err := listAssociationsForPath(ctx, storage, path)
	if err != nil {
		return nil, nil, err
	}
	var match *associationRecord
	for i := range records {
		if records[i].DestinationType != destinationType || records[i].DestinationName != destinationName {
			continue
		}
		if match != nil {
			return nil, logical.ErrorResponse(
				"association request is ambiguous for destination %s/%s; use the association_id path",
				destinationType,
				destinationName,
			), nil
		}
		match = &records[i]
	}
	if match == nil {
		return nil, logical.ErrorResponse(
			"association for destination %s/%s does not exist at source path %q",
			destinationType,
			destinationName,
			path,
		), nil
	}
	return match, nil, nil
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

func validateAssociationDestination(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	cfg globalConfig,
) error {
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
	metadata, err := getMetadata(ctx, storage, record.Path)
	if err != nil {
		return err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return fmt.Errorf("source path does not exist")
	}
	version, err := getVersion(ctx, storage, record.Path, metadata.CurrentVersion)
	if err != nil {
		return err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return fmt.Errorf("current source version is unavailable")
	}
	if err := validateAssociationDestinationPolicy(*destination, record, version.Data, cfg); err != nil {
		return err
	}
	return nil
}

func validateAssociationDestinationPolicy(
	destination destinationRecord,
	record associationRecord,
	data secretPayload,
	cfg globalConfig,
) error {
	if err := validateDestinationDelegationConstraints(destination, record, cfg); err != nil {
		return err
	}
	if !sourcePathAllowed(record.Path, destination.AllowedSourcePathPrefixes) {
		return fmt.Errorf(
			"destination %s does not allow source path %q",
			record.DestinationRef,
			record.Path,
		)
	}
	objectIDs, err := associationObjectIDs(record, data)
	if err != nil {
		return err
	}
	for _, objectID := range objectIDs {
		resolvedName, err := associationResolvedNameForObject(record, objectID)
		if err != nil {
			return err
		}
		if err := validateDestinationPolicyForObject(destination, record, objectID, resolvedName, cfg); err != nil {
			return err
		}
	}
	return nil
}

func validateDestinationPolicyForObject(
	destination destinationRecord,
	record associationRecord,
	objectID string,
	resolvedName string,
	cfg globalConfig,
) error {
	if err := validateDestinationDelegationConstraints(destination, record, cfg); err != nil {
		return err
	}
	if !sourcePathAllowed(record.Path, destination.AllowedSourcePathPrefixes) {
		return fmt.Errorf(
			"destination %s does not allow source path %q",
			record.DestinationRef,
			record.Path,
		)
	}
	if !resolvedNameAllowed(resolvedName, destination.AllowedResolvedNamePrefixes) {
		return fmt.Errorf(
			"destination %s does not allow resolved name %q for object %q",
			record.DestinationRef,
			resolvedName,
			objectID,
		)
	}
	return nil
}

const destinationUnconstrainedBlocker = "destination_unconstrained"

func destinationDelegationConstraintBlockers(destination destinationRecord, cfg globalConfig) []string {
	if !cfg.DelegatedMode || destinationHasDelegationConstraints(destination) {
		return nil
	}
	return []string{destinationUnconstrainedBlocker}
}

func destinationHasDelegationConstraints(destination destinationRecord) bool {
	return len(destination.AllowedSourcePathPrefixes) > 0 &&
		len(destination.AllowedResolvedNamePrefixes) > 0
}

func validateDestinationDelegationConstraints(
	destination destinationRecord,
	record associationRecord,
	cfg globalConfig,
) error {
	if len(destinationDelegationConstraintBlockers(destination, cfg)) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s: delegated_mode requires destination %s to set allowed_source_path_prefixes and allowed_resolved_name_prefixes",
		destinationUnconstrainedBlocker,
		record.DestinationRef,
	)
}

func sourcePathAllowed(path string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func resolvedNameAllowed(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	name = strings.TrimLeft(name, "/")
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(prefix, "/")
		if prefix == "" {
			continue
		}
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

func (b *secretSyncBackend) enqueueAssociationCurrentVersion(
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
		metadata.Generation,
		metadata.CurrentVersion,
		version.Data,
		now,
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
		newAssociationManualSyncOutboxRecords,
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
		newAssociationDriftRepairOutboxRecords,
	)
}

type saltedAssociationOutboxBuilder func(
	associationRecord,
	string,
	int,
	secretPayload,
	string,
	string,
) ([]outboxRecord, []string, error)

func (b *secretSyncBackend) enqueueAssociationCurrentVersionWithSalt(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	metadata metadataRecord,
	now string,
	idPrefix string,
	dedupeQueuedCurrentVersion bool,
	build saltedAssociationOutboxBuilder,
) ([]string, error) {
	version, err := getVersion(ctx, storage, record.Path, metadata.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return nil, fmt.Errorf("current source version is unavailable")
	}
	salt := bestEffortRuntimeID(idPrefix)
	operations, operationIDs, err := build(
		record,
		metadata.Generation,
		metadata.CurrentVersion,
		version.Data,
		now,
		salt,
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

func (b *secretSyncBackend) associationRecordFromFieldData(
	ctx context.Context,
	storage logical.Storage,
	path string,
	data *framework.FieldData,
	base *associationRecord,
) (associationRecord, error) {
	destinationType, destinationName, err := associationDestinationFromFieldData(data, true)
	if err != nil {
		return associationRecord{}, err
	}
	provider, err := b.associationProvider(ctx, storage, destinationType, destinationName)
	if err != nil {
		return associationRecord{}, err
	}

	granularity := stringFromField(data, "granularity", associationGranularityDefault(base))
	baseMatchesGranularity := base != nil && base.Granularity == granularity
	format := stringFromField(data, "format", associationFormatDefault(base, baseMatchesGranularity))
	dataMapping := stringFromField(
		data,
		"data_mapping",
		associationDataMappingDefault(base, baseMatchesGranularity),
	)
	dataKeyTemplate := stringFromField(
		data,
		"data_key_template",
		associationDataKeyTemplateDefault(base, dataMapping, baseMatchesGranularity),
	)
	deleteMode := stringFromField(data, "delete_mode", associationDeleteModeDefault(base))
	nameTemplate := stringFromField(
		data,
		"name_template",
		associationNameTemplateDefault(base, granularity, baseMatchesGranularity),
	)
	resolvedName := stringFromField(data, "resolved_name", associationResolvedNameDefault(base, baseMatchesGranularity))
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
	if err := validateAssociationDataMapping(
		provider.Capabilities(),
		granularity,
		format,
		dataMapping,
		dataKeyTemplate,
	); err != nil {
		return associationRecord{}, err
	}
	if err := validateDeleteMode(provider.Capabilities(), deleteMode); err != nil {
		return associationRecord{}, err
	}

	id := newAssociationID(path, destinationType, destinationName, reservationName, granularity)
	destinationReference := destinationRef(destinationType, destinationName)

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
		DataMapping:     dataMapping,
		DataKeyTemplate: dataKeyTemplate,
		DeleteMode:      deleteMode,
		Enabled:         boolFromField(data, "enabled", associationEnabledDefault(base)),
		CreatedTime:     now,
		UpdatedTime:     now,
	}, nil
}

func validateAssociationIdentityUpdate(base *associationRecord, record associationRecord) error {
	if base == nil {
		return nil
	}
	if record.Granularity != base.Granularity {
		return associationIdentityChangeError("granularity", base.ID)
	}
	if record.reservationName() != base.reservationName() {
		return associationIdentityChangeError(associationReservationIdentityField(*base), base.ID)
	}
	return nil
}

func associationReservationIdentityField(record associationRecord) string {
	if record.Granularity == syncGranularitySecretKey {
		return "name_template"
	}
	return "resolved_name"
}

func associationIdentityChangeError(field string, associationID string) error {
	return fmt.Errorf(
		"%s change would create a new association identity; delete %s first, "+
			"or create the new association and delete the old one explicitly",
		field,
		associationID,
	)
}

func associationUpdateBase(
	ctx context.Context,
	req *logical.Request,
	path string,
	data *framework.FieldData,
) (*associationRecord, *logical.Response, error) {
	if req.Operation != logical.UpdateOperation {
		return nil, nil, nil
	}
	destinationType, destinationName, err := associationDestinationFromFieldData(data, false)
	if err != nil {
		return nil, logical.ErrorResponse(err.Error()), nil
	}
	if destinationType == "" || destinationName == "" {
		return nil, nil, nil
	}
	records, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, nil, err
	}
	var match *associationRecord
	for i := range records {
		if records[i].DestinationType != destinationType || records[i].DestinationName != destinationName {
			continue
		}
		if match != nil {
			return nil, logical.ErrorResponse(
				"association update is ambiguous for destination %s/%s; delete or address one association explicitly",
				destinationType,
				destinationName,
			), nil
		}
		match = &records[i]
	}
	return match, nil, nil
}

func associationDestinationFromFieldData(
	data *framework.FieldData,
	required bool,
) (string, string, error) {
	destination := strings.TrimSpace(data.Get("destination").(string))
	if destination == "" {
		if required {
			return "", "", fmt.Errorf("destination is required")
		}
		return "", "", nil
	}
	return parseDestinationRef(destination)
}

func parseDestinationRef(destination string) (string, string, error) {
	parts := strings.Split(destination, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("destination must be in <type>/<name> form")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func associationGranularityDefault(base *associationRecord) string {
	if base != nil && base.Granularity != "" {
		return base.Granularity
	}
	return syncGranularitySecretPath
}

func associationFormatDefault(base *associationRecord, baseMatchesGranularity bool) string {
	if baseMatchesGranularity && base.Format != "" {
		return base.Format
	}
	return defaultAssociationFormat
}

func associationDataMappingDefault(base *associationRecord, baseMatchesGranularity bool) string {
	if baseMatchesGranularity && base.DataMapping != "" {
		return base.DataMapping
	}
	return defaultDataMapping
}

func associationDataKeyTemplateDefault(
	base *associationRecord,
	dataMapping string,
	baseMatchesGranularity bool,
) string {
	if dataMapping != dataMappingSourceKeys {
		return ""
	}
	if baseMatchesGranularity && base.DataMapping == dataMappingSourceKeys && base.DataKeyTemplate != "" {
		return base.DataKeyTemplate
	}
	return defaultDataKeyTemplate
}

func associationDeleteModeDefault(base *associationRecord) string {
	if base != nil && base.DeleteMode != "" {
		return base.DeleteMode
	}
	return defaultDeleteMode
}

func associationNameTemplateDefault(
	base *associationRecord,
	granularity string,
	baseMatchesGranularity bool,
) string {
	if baseMatchesGranularity && base.NameTemplate != "" {
		return base.NameTemplate
	}
	return defaultNameTemplateForGranularity(granularity)
}

func associationResolvedNameDefault(base *associationRecord, baseMatchesGranularity bool) string {
	if baseMatchesGranularity {
		return base.ResolvedName
	}
	return ""
}

func associationEnabledDefault(base *associationRecord) bool {
	if base != nil {
		return base.Enabled
	}
	return true
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
		reservationName, err := secretKeyReservationName(nameTemplate, path, destinationType, destinationName)
		if err != nil {
			return "", "", err
		}
		return "", reservationName, nil
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

func associationWithConcreteReservationNames(
	record associationRecord,
	data secretPayload,
) (associationRecord, error) {
	if record.Granularity != syncGranularitySecretKey {
		record.ReservationNames = nil
		return record, nil
	}
	objectIDs, err := associationObjectIDs(record, data)
	if err != nil {
		return associationRecord{}, err
	}
	reservationNames := make([]string, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		resolvedName, err := associationResolvedNameForObject(record, objectID)
		if err != nil {
			return associationRecord{}, err
		}
		reservationNames = append(reservationNames, resolvedName)
	}
	record.ReservationNames = uniqueSortedStrings(reservationNames)
	return record, nil
}

func validateAssociationNameReservations(
	ctx context.Context,
	storage logical.Storage,
	destinationReference string,
	reservationNames []string,
	associationID string,
) error {
	for _, reservationName := range reservationNames {
		reservations, err := listAssociationNameReservationIDs(ctx, storage, destinationReference, reservationName)
		if err != nil {
			return err
		}
		if len(reservations) == 0 || (len(reservations) == 1 && reservations[0] == associationID) {
			continue
		}
		return fmt.Errorf(
			"resolved_name %q is already reserved for destination %s",
			reservationName,
			destinationReference,
		)
	}
	return nil
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

func validateAssociationDataMapping(
	capabilities providers.Capabilities,
	granularity string,
	format string,
	dataMapping string,
	dataKeyTemplate string,
) error {
	switch dataMapping {
	case "", defaultDataMapping:
		if strings.TrimSpace(dataKeyTemplate) != "" {
			return fmt.Errorf("data_key_template requires data_mapping %q", dataMappingSourceKeys)
		}
		return nil
	case dataMappingSourceKeys:
		if !capabilities.SupportsDataMap {
			return fmt.Errorf("destination provider does not support source-key data mapping")
		}
		if granularity != syncGranularitySecretPath {
			return fmt.Errorf("data_mapping %q requires secret-path granularity", dataMappingSourceKeys)
		}
		if format != defaultAssociationFormat {
			return fmt.Errorf("data_mapping %q requires format %q", dataMappingSourceKeys, defaultAssociationFormat)
		}
		if !strings.Contains(dataKeyTemplate, "{{ key }}") {
			return fmt.Errorf("data_key_template must include {{ key }}")
		}
		rendered, err := renderDataKeyTemplate(dataKeyTemplate, "key")
		if err != nil {
			return err
		}
		if err := validateDataMapKey(rendered); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported data_mapping %q", dataMapping)
	}
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

func validateAssociationActivation(record associationRecord, metadata *metadataRecord, cfg globalConfig) error {
	if !record.Enabled {
		return nil
	}
	return validateSourceEligibility(metadata, cfg)
}

func validateSourceEligibility(metadata *metadataRecord, cfg globalConfig) error {
	if !cfg.RequireSourceOptIn {
		return nil
	}
	if metadata == nil || metadata.CustomMetadata[sourceMetadataKeySyncable] != sourceMetadataValueTrue {
		return fmt.Errorf(
			"source path is not eligible for sync: custom_metadata.syncable must be true when require_source_opt_in=true",
		)
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
		responseField("data_mapping", normalizedDataMapping(record.DataMapping)),
		responseField("data_key_template", record.DataKeyTemplate),
		responseField("delete_mode", normalizedDeleteMode(record.DeleteMode)),
		responseField("enabled", record.Enabled),
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
	)
}

func associationDefaultsResponse() map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("granularity", syncGranularitySecretPath),
		responseField("format", defaultAssociationFormat),
		responseField("data_mapping", defaultDataMapping),
		responseField("data_key_template", ""),
		responseField("delete_mode", defaultDeleteMode),
		responseField("enabled", true),
		responseField("secret_path_name_template", defaultNameTemplate),
		responseField("secret_key_name_template", defaultPerKeyNameTemplate),
	)
}

func normalizedDeleteMode(deleteMode string) string {
	if deleteMode == "" {
		return defaultDeleteMode
	}
	return deleteMode
}

func normalizedDataMapping(dataMapping string) string {
	if dataMapping == "" {
		return defaultDataMapping
	}
	return dataMapping
}
