package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathReconcile(b *secretSyncBackend) []*framework.Path {
	fields := map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
	}
	return []*framework.Path{
		{
			Pattern: "reconcile/(?P<path>.+)/plan",
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback: b.pathReconcilePlan,
					Summary:  "Plan remote state reconciliation.",
				},
			},
			HelpSynopsis: "Plan reconcile.",
			HelpDescription: "Reads provider remote state for a source path without updating local " +
				"status or mutating remote objects.",
		},
		{
			Pattern: "reconcile/" + framework.MatchAllRegex("path"),
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathReconcileApply,
					Summary:  "Reconcile remote state into local status.",
				},
			},
			HelpSynopsis: "Reconcile remote state.",
			HelpDescription: "Reads provider remote state for a source path and updates local " +
				"status without mutating remote objects.",
		},
	}
}

func (b *secretSyncBackend) pathReconcilePlan(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return b.reconcilePath(ctx, req.Storage, data.Get("path").(string), false)
}

func (b *secretSyncBackend) pathReconcileApply(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return b.reconcilePath(ctx, req.Storage, data.Get("path").(string), true)
}

func (b *secretSyncBackend) reconcilePath(
	ctx context.Context,
	storage logical.Storage,
	rawPath string,
	apply bool,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(rawPath)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return logical.ErrorResponse("source path does not exist"), nil
	}
	version, err := getVersion(ctx, storage, path, metadata.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return logical.ErrorResponse("current source version is unavailable"), nil
	}
	associations, err := listAssociationsForPath(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	results := make([]reconcileObjectResult, 0, len(associations))
	now := nowUTC().Format(timeFormatRFC3339)
	for _, association := range associations {
		associationResults := b.reconcileAssociation(ctx, storage, association, *metadata, *version)
		for _, result := range associationResults {
			results = append(results, result)
			b.recordReconcileRun(ctx, result)
			if apply {
				if err := putReconcileStatus(ctx, storage, result, now); err != nil {
					return nil, err
				}
			}
		}
	}
	objects := make([]map[string]interface{}, 0, len(results)) //nolint:forbidigo
	for _, result := range results {
		objects = append(objects, reconcileObjectResponse(result))
	}
	return &logical.Response{Data: newResponseData(
		responseField("path", path),
		responseField("version", metadata.CurrentVersion),
		responseField("applied", apply),
		responseField("state", string(reconcileSummaryState(results))),
		responseField("objects", objects),
	)}, nil
}

func (b *secretSyncBackend) reconcileAssociation(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
	metadata metadataRecord,
	version versionRecord,
) []reconcileObjectResult {
	objectIDs, objectErr := associationObjectIDs(association, version.Data)
	if objectErr != nil {
		return []reconcileObjectResult{newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			"",
			"",
			domain.SyncStateValidationError,
			providers.ErrorClassValidation,
			objectErr.Error(),
		)}
	}
	if !association.Enabled {
		return reconcileStaticResults(
			association,
			metadata.CurrentVersion,
			objectIDs,
			domain.SyncStateDisabled,
			"",
			"association is disabled",
		)
	}
	provider, err := b.providerRegistry.MustGet(association.DestinationType)
	if err != nil {
		return reconcileStaticResults(
			association,
			metadata.CurrentVersion,
			objectIDs,
			domain.SyncStateValidationError,
			providers.ErrorClassValidation,
			"destination provider is unsupported",
		)
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		return reconcileStaticResults(
			association,
			metadata.CurrentVersion,
			objectIDs,
			domain.SyncStateInternalError,
			providers.ErrorClassInternal,
			"destination lookup failed",
		)
	}
	if destination == nil {
		return reconcileStaticResults(
			association,
			metadata.CurrentVersion,
			objectIDs,
			domain.SyncStateValidationError,
			providers.ErrorClassValidation,
			"destination is missing",
		)
	}
	if destination.Disabled {
		return reconcileStaticResults(
			association,
			metadata.CurrentVersion,
			objectIDs,
			domain.SyncStateDisabled,
			"",
			"destination is disabled",
		)
	}
	resolvedDestinationConfig, err := destinationConfig(ctx, storage, *destination)
	if err != nil {
		return reconcileStaticResults(
			association,
			metadata.CurrentVersion,
			objectIDs,
			domain.SyncStateInternalError,
			providers.ErrorClassInternal,
			"destination config resolution failed",
		)
	}
	runtimeIdentity, err := providerRuntimeIdentity(ctx, storage)
	if err != nil {
		return reconcileStaticResults(
			association,
			metadata.CurrentVersion,
			objectIDs,
			domain.SyncStateInternalError,
			providers.ErrorClassInternal,
			"runtime identity resolution failed",
		)
	}
	results := make([]reconcileObjectResult, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		results = append(results, b.reconcileAssociationObject(
			ctx,
			association,
			provider,
			resolvedDestinationConfig,
			runtimeIdentity,
			metadata.CurrentVersion,
			version,
			objectID,
		))
	}
	return results
}

func (b *secretSyncBackend) reconcileAssociationObject(
	ctx context.Context,
	association associationRecord,
	provider providers.Provider,
	destinationConfig providers.DestinationConfig,
	runtimeIdentity providers.RuntimeIdentity,
	sourceVersion int,
	version versionRecord,
	objectID string,
) reconcileObjectResult {
	resolvedName, err := associationResolvedNameForObject(association, objectID)
	if err != nil {
		return newReconcileObjectResult(
			association,
			sourceVersion,
			objectID,
			"",
			domain.SyncStateValidationError,
			providers.ErrorClassValidation,
			err.Error(),
		)
	}
	result := newReconcileObjectResult(
		association,
		sourceVersion,
		objectID,
		resolvedName,
		domain.SyncStateUnknown,
		"",
		"",
	)
	payload, failure := prepareReconcilePayload(provider, association, version, objectID)
	if failure != nil {
		result.state = syncStateForFailureClass(failure.class)
		result.errorClass = failure.class
		result.message = failure.message
		result.payload = payload
		return result
	}
	result.payload = payload
	providerStart := time.Now()
	remoteState, err := provider.ReadState(ctx, providerReadStateRequest(
		association,
		destinationConfig,
		runtimeIdentity,
		sourceVersion,
		payload,
		objectID,
		resolvedName,
	))
	b.recordProviderRequest(ctx, provider.Type(), observability.OperationReadState, err, time.Since(providerStart))
	if err != nil {
		result.state = syncStateForFailureClass(providerErrorClass(err))
		result.errorClass = providerErrorClass(err)
		result.message = "provider read-state failed"
		return result
	}
	if remoteState == nil {
		result.state = domain.SyncStateInternalError
		result.errorClass = providers.ErrorClassInternal
		result.message = "provider returned nil remote state"
		return result
	}
	result.remoteState = remoteState
	result.state, result.errorClass, result.message = reconcileStateForRemoteState(
		sourceVersion,
		payload,
		*remoteState,
	)
	return result
}

func prepareReconcilePayload(
	provider providers.Provider,
	association associationRecord,
	version versionRecord,
	objectID string,
) (payloadpkg.CanonicalPayload, *operationFailure) {
	payload, err := buildCanonicalPayloadForObject(association.Format, version.Data, association.Granularity, objectID)
	if err != nil {
		return payloadpkg.CanonicalPayload{}, &operationFailure{
			class:   providers.ErrorClassValidation,
			message: "source payload encoding failed",
		}
	}
	if err := enforceProviderPayloadLimit(provider.Capabilities(), payload); err != nil {
		return payload, &operationFailure{
			class:   providers.ErrorClassCapacity,
			message: err.Error(),
		}
	}
	return payload, nil
}

func providerReadStateRequest(
	association associationRecord,
	destination providers.DestinationConfig,
	runtimeIdentity providers.RuntimeIdentity,
	version int,
	payload payloadpkg.CanonicalPayload,
	objectID string,
	resolvedName string,
) providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Destination:   destination,
		Runtime:       runtimeIdentity,
		ResolvedName:  resolvedName,
		PayloadSHA256: payload.SHA256,
		SourcePath:    association.Path,
		SourceVersion: version,
		AssociationID: association.ID,
		ObjectID:      objectID,
	}
}

func reconcileStateForRemoteState(
	version int,
	payload payloadpkg.CanonicalPayload,
	remoteState providers.RemoteState,
) (domain.SyncState, providers.ErrorClass, string) {
	if !remoteState.Exists {
		return domain.SyncStateRemoteMissing, "", "remote object is missing"
	}
	if remoteState.OwnershipKnown && !remoteState.Owned {
		return domain.SyncStateRemoteOwnershipLost,
			providers.ErrorClassOwnership,
			"remote object is not owned by this association"
	}
	if remoteState.SourceVersion > 0 && remoteState.SourceVersion != version {
		return domain.SyncStateDrifted, providers.ErrorClassDrift, fmt.Sprintf(
			"remote source version %d differs from desired version %d",
			remoteState.SourceVersion,
			version,
		)
	}
	if remoteState.PayloadSHA256 != "" && remoteState.PayloadSHA256 != payload.SHA256 {
		return domain.SyncStateDrifted, providers.ErrorClassDrift, "remote payload hash differs from desired payload hash"
	}
	if remoteState.PayloadSHA256 == payload.SHA256 {
		return domain.SyncStateSynced, "", ""
	}
	return domain.SyncStateUnknown, "", "remote state lacks comparable payload metadata"
}

func putReconcileStatus(
	ctx context.Context,
	storage logical.Storage,
	result reconcileObjectResult,
	now string,
) error {
	status := statusRecord{
		Path:           result.association.Path,
		Version:        result.version,
		AssociationID:  result.association.ID,
		ObjectID:       result.objectID,
		DestinationRef: result.association.DestinationRef,
		ResolvedName:   result.resolvedName,
		State:          string(result.state),
		PayloadSHA256:  result.payload.SHA256,
		LastErrorClass: string(result.errorClass),
		UpdatedTime:    now,
	}
	if result.remoteState != nil {
		status.RemoteVersion = result.remoteState.RemoteVersion
	}
	if result.errorClass != "" {
		status.LastError = "reconcile failed: " + result.message
	}
	existing, err := getStatus(ctx, storage, result.association.Path, result.association.ID, result.objectID)
	if err != nil {
		return err
	}
	if existing != nil {
		status.LastOperationID = existing.LastOperationID
		status.LastSuccessTime = existing.LastSuccessTime
	}
	return putStatus(ctx, storage, status)
}

func reconcileSummaryState(results []reconcileObjectResult) domain.SyncState {
	if len(results) == 0 {
		return domain.SyncStateNoAssociation
	}
	state := domain.SyncStateSynced
	for _, result := range results {
		if result.state != domain.SyncStateSynced {
			return result.state
		}
	}
	return state
}

func (b *secretSyncBackend) recordReconcileRun(ctx context.Context, result reconcileObjectResult) {
	b.observer.ReconcileRun(ctx, observability.ReconcileRunEvent{
		Result:          reconcileObservabilityResult(result),
		ErrorClass:      string(result.errorClass),
		DestinationType: result.association.DestinationType,
		Granularity:     result.association.Granularity,
	})
}

func reconcileObservabilityResult(result reconcileObjectResult) string {
	switch result.state {
	case domain.SyncStateSynced:
		return observability.ResultSuccess
	case domain.SyncStateDisabled:
		return observability.ResultSkipped
	default:
		return observability.ResultFailure
	}
}

type reconcileObjectResult struct {
	association  associationRecord
	version      int
	objectID     string
	resolvedName string
	payload      payloadpkg.CanonicalPayload
	remoteState  *providers.RemoteState
	state        domain.SyncState
	errorClass   providers.ErrorClass
	message      string
}

func newReconcileObjectResult(
	association associationRecord,
	version int,
	objectID string,
	resolvedName string,
	state domain.SyncState,
	errorClass providers.ErrorClass,
	message string,
) reconcileObjectResult {
	if objectID == "" {
		objectID = syncObjectIDSecretPath
	}
	if resolvedName == "" {
		resolvedName, _ = associationResolvedNameForObject(association, objectID)
	}
	return reconcileObjectResult{
		association:  association,
		version:      version,
		objectID:     objectID,
		resolvedName: resolvedName,
		state:        state,
		errorClass:   errorClass,
		message:      message,
	}
}

func reconcileStaticResults(
	association associationRecord,
	version int,
	objectIDs []string,
	state domain.SyncState,
	errorClass providers.ErrorClass,
	message string,
) []reconcileObjectResult {
	results := make([]reconcileObjectResult, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		resolvedName, _ := associationResolvedNameForObject(association, objectID)
		results = append(results, newReconcileObjectResult(
			association,
			version,
			objectID,
			resolvedName,
			state,
			errorClass,
			message,
		))
	}
	return results
}

func reconcileObjectResponse(result reconcileObjectResult) map[string]interface{} { //nolint:forbidigo
	remoteExists := false
	remoteOwnershipKnown := false
	remoteOwned := false
	remotePayloadSHA256 := ""
	remoteSourceVersion := 0
	remoteVersion := ""
	if result.remoteState != nil {
		remoteExists = result.remoteState.Exists
		remoteOwnershipKnown = result.remoteState.OwnershipKnown
		remoteOwned = result.remoteState.Owned
		remotePayloadSHA256 = result.remoteState.PayloadSHA256
		remoteSourceVersion = result.remoteState.SourceVersion
		remoteVersion = result.remoteState.RemoteVersion
	}
	return newResponseData(
		responseField("association_id", result.association.ID),
		responseField("object_id", result.objectID),
		responseField("destination_ref", result.association.DestinationRef),
		responseField("resolved_name", result.resolvedName),
		responseField("state", string(result.state)),
		responseField("version", result.version),
		responseField("payload_sha256", result.payload.SHA256),
		responseField("remote_exists", remoteExists),
		responseField("remote_ownership_known", remoteOwnershipKnown),
		responseField("remote_owned", remoteOwned),
		responseField("remote_payload_sha256", remotePayloadSHA256),
		responseField("remote_source_version", remoteSourceVersion),
		responseField("remote_version", remoteVersion),
		responseField("error_class", string(result.errorClass)),
		responseField("message", result.message),
	)
}
