package backend

import (
	"context"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func (b *secretSyncBackend) pathAssociationPlan(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, metadata, version, response, err := currentSourceVersionFromPlanRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	baseRecord, response, err := b.associationUpdateBase(ctx, req, path, data)
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
	metadata, version, err := currentSourceVersion(ctx, req.Storage, path)
	if err != nil {
		if response := currentSourceVersionErrorResponse(err); response != nil {
			return "", nil, nil, response, nil
		}
		return "", nil, nil, nil, err
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
		Association:   providerAssociationConfig(record),
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
			Association:   providerAssociationConfig(record),
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
