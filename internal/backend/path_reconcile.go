package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/adfinis/openbao-secret-sync/internal/observability"
	payloadpkg "github.com/adfinis/openbao-secret-sync/internal/payload"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
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
		result := b.reconcileAssociation(ctx, storage, association, *metadata, *version)
		results = append(results, result)
		b.recordReconcileRun(ctx, result)
		if apply {
			if err := putReconcileStatus(ctx, storage, result, now); err != nil {
				return nil, err
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
) reconcileObjectResult {
	result := reconcileObjectResult{
		association: association,
		version:     metadata.CurrentVersion,
		state:       domain.SyncStateUnknown,
	}
	if !association.Enabled {
		result.state = domain.SyncStateDisabled
		result.message = "association is disabled"
		return result
	}
	if association.Granularity != syncObjectIDSecretPath {
		result.state = domain.SyncStateValidationError
		result.errorClass = providers.ErrorClassValidation
		result.message = "reconcile supports secret-path granularity"
		return result
	}
	provider, err := b.providerRegistry.MustGet(association.DestinationType)
	if err != nil {
		result.state = domain.SyncStateValidationError
		result.errorClass = providers.ErrorClassValidation
		result.message = "destination provider is unsupported"
		return result
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		result.state = domain.SyncStateInternalError
		result.errorClass = providers.ErrorClassInternal
		result.message = "destination lookup failed"
		return result
	}
	if destination == nil {
		result.state = domain.SyncStateValidationError
		result.errorClass = providers.ErrorClassValidation
		result.message = "destination is missing"
		return result
	}
	if destination.Disabled {
		result.state = domain.SyncStateDisabled
		result.message = "destination is disabled"
		return result
	}
	payload, failure := prepareReconcilePayload(provider, association, version)
	if failure != nil {
		result.state = syncStateForFailureClass(failure.class)
		result.errorClass = failure.class
		result.message = failure.message
		result.payload = payload
		return result
	}
	result.payload = payload
	resolvedDestinationConfig, err := destinationConfig(ctx, storage, *destination)
	if err != nil {
		result.state = domain.SyncStateInternalError
		result.errorClass = providers.ErrorClassInternal
		result.message = "destination config resolution failed"
		return result
	}
	providerStart := time.Now()
	remoteState, err := provider.ReadState(ctx, providerReadStateRequest(
		association,
		resolvedDestinationConfig,
		metadata.CurrentVersion,
		payload,
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
		metadata.CurrentVersion,
		payload,
		*remoteState,
	)
	return result
}

func prepareReconcilePayload(
	provider providers.Provider,
	association associationRecord,
	version versionRecord,
) (payloadpkg.CanonicalPayload, *operationFailure) {
	payload, err := buildCanonicalPayload(association.Format, version.Data)
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
	version int,
	payload payloadpkg.CanonicalPayload,
) providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Destination:   destination,
		ResolvedName:  association.ResolvedName,
		PayloadSHA256: payload.SHA256,
		SourcePath:    association.Path,
		SourceVersion: version,
		AssociationID: association.ID,
		ObjectID:      association.Granularity,
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
		ObjectID:       syncObjectIDSecretPath,
		DestinationRef: result.association.DestinationRef,
		ResolvedName:   result.association.ResolvedName,
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
	existing, err := getStatus(ctx, storage, result.association.Path, result.association.ID, syncObjectIDSecretPath)
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
	association associationRecord
	version     int
	payload     payloadpkg.CanonicalPayload
	remoteState *providers.RemoteState
	state       domain.SyncState
	errorClass  providers.ErrorClass
	message     string
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
		responseField("object_id", syncObjectIDSecretPath),
		responseField("destination_ref", result.association.DestinationRef),
		responseField("resolved_name", result.association.ResolvedName),
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
