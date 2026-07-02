package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	maxAutomaticRetryAttempts         = 3
	retryBaseDelay                    = 30 * time.Second
	retryMaxDelay                     = 5 * time.Minute
	outboxClaimLease                  = 5 * time.Minute
	defaultPeriodicMaxOperations      = 100
	defaultPeriodicRecoveryMaxIntents = 100
)

func (b *secretSyncBackend) processDueOutboxLimit(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
	maxOperations int,
) (int, error) {
	claimOwner, err := b.outboxClaimOwner(ctx, storage)
	if err != nil {
		return 0, err
	}
	ids, err := listDueOutboxIDs(ctx, storage, now)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return processed, err
		}
		if record == nil || !isDispatchableOutboxState(record.State) {
			continue
		}
		if !isSupportedOperation(*record) {
			if err := discardUnsupportedOutboxOperation(ctx, storage, *record); err != nil {
				return processed, err
			}
			continue
		}
		b.enqueueMu.Lock()
		claimed, ok, err := claimOutboxRecord(ctx, storage, *record, claimOwner, now)
		b.enqueueMu.Unlock()
		if err != nil {
			return processed, err
		}
		if !ok {
			continue
		}
		if err := b.processOutboxRecord(ctx, storage, *claimed, now); err != nil {
			return processed, err
		}
		processed++
		if maxOperations > 0 && processed >= maxOperations {
			break
		}
	}
	return processed, nil
}

func (b *secretSyncBackend) outboxClaimOwner(ctx context.Context, storage logical.Storage) (string, error) {
	state, err := ensureRuntimeState(ctx, storage)
	if err != nil {
		return "", err
	}
	return state.PluginInstance.ID + "/" + b.dispatchWorkerID, nil
}

func claimOutboxRecord(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	owner string,
	now time.Time,
) (*outboxRecord, bool, error) {
	if isOutboxClaimActive(record, now) {
		return nil, false, nil
	}
	claimExpires := now.Add(outboxClaimLease).Format(timeFormatRFC3339)
	record.ClaimOwner = owner
	record.ClaimExpiresTime = claimExpires
	record.ClaimAttempt = record.Attempts + 1
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	if err := putOutbox(ctx, storage, record); err != nil {
		return nil, false, err
	}
	claimed, err := getOutbox(ctx, storage, record.ID)
	if err != nil {
		return nil, false, err
	}
	if claimed == nil ||
		claimed.ClaimOwner != owner ||
		claimed.ClaimExpiresTime != claimExpires ||
		claimed.ClaimAttempt != record.ClaimAttempt {
		return nil, false, nil
	}
	return claimed, true, nil
}

func isOutboxClaimActive(record outboxRecord, now time.Time) bool {
	if record.ClaimOwner == "" || record.ClaimExpiresTime == "" {
		return false
	}
	expires, err := time.Parse(timeFormatRFC3339, record.ClaimExpiresTime)
	if err != nil {
		return false
	}
	return expires.After(now)
}

func clearOutboxClaim(record *outboxRecord) {
	record.ClaimOwner = ""
	record.ClaimExpiresTime = ""
	record.ClaimAttempt = 0
}

func isSupportedOperation(record outboxRecord) bool {
	if record.ObjectID == "" {
		return false
	}
	return record.Type == outbox.OperationTypeUpsert || record.Type == outbox.OperationTypeDelete
}

func discardUnsupportedOutboxOperation(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	return deleteOutbox(ctx, storage, record)
}

func isDispatchableOutboxState(state string) bool {
	return state == outboxStatePending || state == outboxStateRetryWait
}

func (b *secretSyncBackend) processOutboxRecord(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	now time.Time,
) error {
	switch record.Type {
	case outbox.OperationTypeUpsert:
		return b.processUpsert(ctx, storage, record, now)
	case outbox.OperationTypeDelete:
		return b.processDelete(ctx, storage, record, now)
	default:
		return nil
	}
}

func (b *secretSyncBackend) processUpsert(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	now time.Time,
) error {
	stale, err := staleUpsertForCurrentVersion(ctx, storage, record)
	if err != nil {
		return err
	}
	if stale {
		return cancelOutboxOperation(ctx, storage, record)
	}
	upsertContext, failure, err := b.loadUpsertContext(ctx, storage, record)
	if err != nil {
		return err
	}
	if failure != nil {
		b.recordOperationFailure(ctx, record, failure.class)
		return markOperationFailed(ctx, storage, record, *failure, now)
	}
	preparedPayload, failure := prepareProviderPayload(upsertContext, record.ObjectID)
	if failure != nil {
		b.recordOperationFailure(ctx, record, failure.class)
		return markOperationFailed(ctx, storage, record, *failure, now)
	}
	resolvedName, failure := resolvedNameForOperation(upsertContext.association, record.ObjectID)
	if failure != nil {
		b.recordOperationFailure(ctx, record, failure.class)
		return markOperationFailed(ctx, storage, record, *failure, now)
	}
	if err := validateDestinationPolicyForObject(
		*upsertContext.destination,
		*upsertContext.association,
		record.ObjectID,
		resolvedName,
	); err != nil {
		failure := operationFailure{
			class:         providers.ErrorClassValidation,
			message:       err.Error(),
			resolvedName:  resolvedName,
			payloadSHA256: preparedPayload.SHA256,
		}
		b.recordOperationFailure(ctx, record, failure.class)
		return markOperationFailed(ctx, storage, record, failure, now)
	}

	resolvedDestinationConfig, err := destinationConfig(ctx, storage, *upsertContext.destination)
	if err != nil {
		return err
	}
	runtimeIdentity, err := providerRuntimeIdentity(ctx, storage)
	if err != nil {
		return err
	}
	providerStart := time.Now()
	result, err := b.providerUpsert(
		ctx,
		upsertContext,
		resolvedDestinationConfig,
		runtimeIdentity,
		record,
		resolvedName,
		preparedPayload,
	)
	if isDispatchContextCanceled(ctx, err) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	b.recordProviderRequest(
		ctx,
		upsertContext.provider.Type(),
		observability.OperationUpsert,
		err,
		time.Since(providerStart),
	)
	if err != nil {
		errorClass := providerErrorClass(err)
		b.recordOperationFailure(ctx, record, errorClass)
		return markOperationFailed(ctx, storage, record, operationFailure{
			class:         errorClass,
			message:       "provider upsert failed",
			resolvedName:  resolvedName,
			payloadSHA256: preparedPayload.SHA256,
		}, now)
	}
	if result == nil {
		result = &providers.SyncResult{}
	}

	record.Attempts++
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	clearOutboxClaim(&record)
	b.recordOperationSuccess(ctx, record)
	if err := putStatus(ctx, storage, statusRecord{
		Path:            record.Path,
		Version:         record.Version,
		AssociationID:   record.AssociationID,
		ObjectID:        record.ObjectID,
		DestinationRef:  record.DestinationRef,
		ResolvedName:    resolvedName,
		State:           string(domain.SyncStateSynced),
		PayloadSHA256:   preparedPayload.SHA256,
		RemoteVersion:   result.RemoteVersion,
		LastOperationID: record.ID,
		LastSuccessTime: now.Format(timeFormatRFC3339),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	}); err != nil {
		return err
	}
	return deleteOutbox(ctx, storage, record)
}

func (b *secretSyncBackend) processDelete(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	now time.Time,
) error {
	deleteContext, failure, err := b.loadDeleteContext(ctx, storage, record)
	if err != nil {
		return err
	}
	if failure != nil {
		b.recordOperationFailure(ctx, record, failure.class)
		return markOperationFailed(ctx, storage, record, *failure, now)
	}
	resolvedName, failure := resolvedNameForOperation(deleteContext.association, record.ObjectID)
	if failure != nil {
		b.recordOperationFailure(ctx, record, failure.class)
		return markOperationFailed(ctx, storage, record, *failure, now)
	}
	if err := validateDestinationPolicyForObject(
		*deleteContext.destination,
		*deleteContext.association,
		record.ObjectID,
		resolvedName,
	); err != nil {
		failure := operationFailure{
			class:        providers.ErrorClassValidation,
			message:      err.Error(),
			resolvedName: resolvedName,
		}
		b.recordOperationFailure(ctx, record, failure.class)
		return markOperationFailed(ctx, storage, record, failure, now)
	}
	resolvedDestinationConfig, err := destinationConfig(ctx, storage, *deleteContext.destination)
	if err != nil {
		return err
	}
	runtimeIdentity, err := providerRuntimeIdentity(ctx, storage)
	if err != nil {
		return err
	}
	providerStart := time.Now()
	result, err := b.providerDelete(
		ctx,
		deleteContext,
		resolvedDestinationConfig,
		runtimeIdentity,
		record,
		resolvedName,
	)
	if isDispatchContextCanceled(ctx, err) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	b.recordProviderRequest(
		ctx,
		deleteContext.provider.Type(),
		observability.OperationDelete,
		err,
		time.Since(providerStart),
	)
	if err != nil {
		errorClass := providerErrorClass(err)
		b.recordOperationFailure(ctx, record, errorClass)
		return markOperationFailed(ctx, storage, record, operationFailure{
			class:        errorClass,
			message:      "provider delete failed",
			resolvedName: resolvedName,
		}, now)
	}
	if result == nil {
		result = &providers.SyncResult{}
	}

	record.Attempts++
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	clearOutboxClaim(&record)
	b.recordOperationSuccess(ctx, record)
	if err := putStatus(ctx, storage, statusRecord{
		Path:            record.Path,
		Version:         record.Version,
		AssociationID:   record.AssociationID,
		ObjectID:        record.ObjectID,
		DestinationRef:  record.DestinationRef,
		ResolvedName:    resolvedName,
		State:           string(domain.SyncStateRemoteMissing),
		RemoteVersion:   result.RemoteVersion,
		LastOperationID: record.ID,
		LastSuccessTime: now.Format(timeFormatRFC3339),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	}); err != nil {
		return err
	}
	return deleteOutbox(ctx, storage, record)
}

func (b *secretSyncBackend) providerUpsert(
	ctx context.Context,
	ctxData *upsertContext,
	destinationConfig providers.DestinationConfig,
	runtimeIdentity providers.RuntimeIdentity,
	record outboxRecord,
	resolvedName string,
	preparedPayload payloadpkg.CanonicalPayload,
) (*providers.SyncResult, error) {
	runtime, err := b.destinationRuntime(ctx, ctxData.provider, *ctxData.destination, destinationConfig)
	if err != nil {
		return nil, err
	}
	return runtime.Upsert(ctx, providers.UpsertRequest{
		Runtime:       runtimeIdentity,
		ResolvedName:  resolvedName,
		Format:        preparedPayload.Format,
		Payload:       preparedPayload.Bytes,
		PayloadSHA256: preparedPayload.SHA256,
		SourcePath:    record.Path,
		SourceVersion: record.Version,
		AssociationID: record.AssociationID,
		ObjectID:      record.ObjectID,
	})
}

func (b *secretSyncBackend) providerDelete(
	ctx context.Context,
	ctxData *deleteContext,
	destinationConfig providers.DestinationConfig,
	runtimeIdentity providers.RuntimeIdentity,
	record outboxRecord,
	resolvedName string,
) (*providers.SyncResult, error) {
	runtime, err := b.destinationRuntime(ctx, ctxData.provider, *ctxData.destination, destinationConfig)
	if err != nil {
		return nil, err
	}
	return runtime.Delete(ctx, providers.DeleteRequest{
		Runtime:       runtimeIdentity,
		ResolvedName:  resolvedName,
		SourcePath:    record.Path,
		SourceVersion: record.Version,
		AssociationID: record.AssociationID,
		ObjectID:      record.ObjectID,
	})
}

type upsertContext struct {
	version     *versionRecord
	association *associationRecord
	destination *destinationRecord
	provider    providers.Provider
}

func (b *secretSyncBackend) loadUpsertContext(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
) (*upsertContext, *operationFailure, error) {
	version, err := getVersion(ctx, storage, record.Path, record.Version)
	if err != nil {
		return nil, nil, err
	}
	if version == nil || version.Destroyed || version.DeletionTime != "" {
		return nil, &operationFailure{class: providers.ErrorClassInternal, message: "source version is unavailable"}, nil
	}
	association, err := getAssociation(ctx, storage, record.Path, record.AssociationID)
	if err != nil {
		return nil, nil, err
	}
	if association == nil {
		return nil, &operationFailure{class: providers.ErrorClassInternal, message: "association is missing"}, nil
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		return nil, nil, err
	}
	if destination == nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassInternal,
			message:      "destination is missing",
			resolvedName: association.ResolvedName,
		}, nil
	}
	provider, err := b.providerRegistry.MustGet(destination.Type)
	if err != nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "destination provider is unsupported",
			resolvedName: association.ResolvedName,
		}, nil
	}
	if destination.Disabled || !association.Enabled {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "association or destination is disabled",
			resolvedName: association.ResolvedName,
		}, nil
	}
	return &upsertContext{
		version:     version,
		association: association,
		destination: destination,
		provider:    provider,
	}, nil, nil
}

type deleteContext struct {
	association *associationRecord
	destination *destinationRecord
	provider    providers.Provider
}

func (b *secretSyncBackend) loadDeleteContext(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
) (*deleteContext, *operationFailure, error) {
	version, err := getVersion(ctx, storage, record.Path, record.Version)
	if err != nil {
		return nil, nil, err
	}
	if version != nil && !version.Destroyed && version.DeletionTime == "" {
		return nil, &operationFailure{class: providers.ErrorClassValidation, message: "source version is not deleted"}, nil
	}
	association, err := getAssociation(ctx, storage, record.Path, record.AssociationID)
	if err != nil {
		return nil, nil, err
	}
	if association == nil {
		return nil, &operationFailure{class: providers.ErrorClassInternal, message: "association is missing"}, nil
	}
	if normalizedDeleteMode(association.DeleteMode) != deleteModeDelete {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "association delete_mode does not permit remote delete",
			resolvedName: association.ResolvedName,
		}, nil
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		return nil, nil, err
	}
	if destination == nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassInternal,
			message:      "destination is missing",
			resolvedName: association.ResolvedName,
		}, nil
	}
	provider, err := b.providerRegistry.MustGet(destination.Type)
	if err != nil {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "destination provider is unsupported",
			resolvedName: association.ResolvedName,
		}, nil
	}
	if destination.Disabled || !association.Enabled {
		return nil, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "association or destination is disabled",
			resolvedName: association.ResolvedName,
		}, nil
	}
	return &deleteContext{
		association: association,
		destination: destination,
		provider:    provider,
	}, nil, nil
}

func prepareProviderPayload(
	upsertContext *upsertContext,
	objectID string,
) (payloadpkg.CanonicalPayload, *operationFailure) {
	preparedPayload, err := buildCanonicalPayloadForObject(
		upsertContext.association.Format,
		upsertContext.version.Data,
		upsertContext.association.Granularity,
		objectID,
	)
	if err != nil {
		return payloadpkg.CanonicalPayload{}, &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      "source payload encoding failed",
			resolvedName: upsertContext.association.ResolvedName,
		}
	}
	capabilities := upsertContext.provider.Capabilities()
	if err := enforceProviderPayloadLimit(capabilities, preparedPayload); err != nil {
		return payloadpkg.CanonicalPayload{}, &operationFailure{
			class:         providers.ErrorClassCapacity,
			message:       err.Error(),
			resolvedName:  upsertContext.association.ResolvedName,
			payloadSHA256: preparedPayload.SHA256,
		}
	}
	return preparedPayload, nil
}

func buildCanonicalPayloadForObject(
	format string,
	data secretPayload,
	granularity string,
	objectID string,
) (payloadpkg.CanonicalPayload, error) {
	switch format {
	case defaultAssociationFormat:
		switch granularity {
		case syncGranularitySecretPath:
			return payloadpkg.BuildJSON(map[string]interface{}(data))
		case syncGranularitySecretKey:
			value, ok := data[objectID]
			if !ok {
				return payloadpkg.CanonicalPayload{}, fmt.Errorf("source key %q does not exist", objectID)
			}
			return payloadpkg.BuildJSON(map[string]interface{}{objectID: value})
		default:
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("unsupported granularity %q", granularity)
		}
	case rawAssociationFormat:
		if granularity != syncGranularitySecretKey {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("raw payload format requires secret-key granularity")
		}
		value, ok := data[objectID]
		if !ok {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("source key %q does not exist", objectID)
		}
		return payloadpkg.BuildRaw(value)
	default:
		return payloadpkg.CanonicalPayload{}, fmt.Errorf("unsupported payload format %q", format)
	}
}

func resolvedNameForOperation(association *associationRecord, objectID string) (string, *operationFailure) {
	resolvedName, err := associationResolvedNameForObject(*association, objectID)
	if err != nil {
		return "", &operationFailure{
			class:   providers.ErrorClassValidation,
			message: err.Error(),
		}
	}
	return resolvedName, nil
}

func enforceProviderPayloadLimit(
	capabilities providers.Capabilities,
	payload payloadpkg.CanonicalPayload,
) error {
	if capabilities.MaxPayloadBytes <= 0 || len(payload.Bytes) <= capabilities.MaxPayloadBytes {
		return nil
	}
	return fmt.Errorf("payload exceeds provider max_payload_bytes %d", capabilities.MaxPayloadBytes)
}

func providerErrorClass(err error) providers.ErrorClass {
	var providerError *providers.Error
	if errors.As(err, &providerError) && providerError.Class != "" {
		return providerError.Class
	}
	return providers.ErrorClassInternal
}

func isDispatchContextCanceled(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

type operationFailure struct {
	class         providers.ErrorClass
	message       string
	resolvedName  string
	payloadSHA256 string
}

func markOperationFailed(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	failure operationFailure,
	now time.Time,
) error {
	if failure.class == "" {
		failure.class = providers.ErrorClassInternal
	}
	if failure.resolvedName == "" {
		failure.resolvedName = record.Path
	}
	record.State = outboxStateFailedTerminal
	record.Attempts++
	if shouldRetryAutomatically(failure.class, record.Attempts) {
		record.State = outboxStateRetryWait
		record.NotBefore = now.Add(retryDelay(record.Attempts)).Format(timeFormatRFC3339)
	} else {
		record.NotBefore = ""
	}
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	clearOutboxClaim(&record)
	if err := putOutbox(ctx, storage, record); err != nil {
		return err
	}
	return putStatus(ctx, storage, statusRecord{
		Path:            record.Path,
		Version:         record.Version,
		AssociationID:   record.AssociationID,
		ObjectID:        record.ObjectID,
		DestinationRef:  record.DestinationRef,
		ResolvedName:    failure.resolvedName,
		State:           string(syncStateForFailureClass(failure.class)),
		PayloadSHA256:   failure.payloadSHA256,
		LastOperationID: record.ID,
		LastErrorClass:  string(failure.class),
		LastError:       fmt.Sprintf("dispatch failed: %s", failure.message),
		UpdatedTime:     now.Format(timeFormatRFC3339),
	})
}

func staleUpsertForCurrentVersion(ctx context.Context, storage logical.Storage, record outboxRecord) (bool, error) {
	if record.Type != outbox.OperationTypeUpsert {
		return false, nil
	}
	metadata, err := getMetadata(ctx, storage, record.Path)
	if err != nil {
		return false, err
	}
	return metadata != nil && metadata.CurrentVersion > record.Version, nil
}

func cancelOutboxOperation(ctx context.Context, storage logical.Storage, record outboxRecord) error {
	return deleteOutbox(ctx, storage, record)
}

func syncStateForFailureClass(errorClass providers.ErrorClass) domain.SyncState {
	switch errorClass {
	case providers.ErrorClassAuthn:
		return domain.SyncStateDestinationAuthError
	case providers.ErrorClassAuthz:
		return domain.SyncStateDestinationPolicyError
	case providers.ErrorClassRateLimit:
		return domain.SyncStateDestinationRateLimited
	case providers.ErrorClassUnavailable:
		return domain.SyncStateDestinationUnavailable
	case providers.ErrorClassOwnership:
		return domain.SyncStateRemoteOwnershipLost
	case providers.ErrorClassDrift, providers.ErrorClassCollision:
		return domain.SyncStateDrifted
	case providers.ErrorClassValidation:
		return domain.SyncStateValidationError
	case providers.ErrorClassCapacity:
		return domain.SyncStateQueueBlocked
	default:
		return domain.SyncStateInternalError
	}
}

func (b *secretSyncBackend) recordOperationSuccess(ctx context.Context, record outboxRecord) {
	b.observer.Operation(ctx, observability.OperationEvent{
		Operation:       observabilityOperation(record.Type),
		Result:          observability.ResultSuccess,
		DestinationType: destinationTypeFromRef(record.DestinationRef),
		Granularity:     operationGranularity(record),
	})
}

func (b *secretSyncBackend) recordOperationFailure(
	ctx context.Context,
	record outboxRecord,
	errorClass providers.ErrorClass,
) {
	result := observability.ResultFailure
	if shouldRetryAutomatically(errorClass, record.Attempts+1) {
		result = observability.ResultRetry
	}
	b.observer.Operation(ctx, observability.OperationEvent{
		Operation:       observabilityOperation(record.Type),
		Result:          result,
		ErrorClass:      string(errorClass),
		DestinationType: destinationTypeFromRef(record.DestinationRef),
		Granularity:     operationGranularity(record),
	})
}

func operationGranularity(record outboxRecord) string {
	if record.ObjectID == "" {
		return observability.ValueUnknown
	}
	if record.ObjectID == syncObjectIDSecretPath {
		return syncGranularitySecretPath
	}
	return syncGranularitySecretKey
}

func (b *secretSyncBackend) recordProviderRequest(
	ctx context.Context,
	providerType string,
	operation string,
	err error,
	duration time.Duration,
) {
	result := observability.ResultSuccess
	errorClass := ""
	if err != nil {
		result = observability.ResultFailure
		errorClass = string(providerErrorClass(err))
	}
	b.observer.ProviderRequest(ctx, observability.ProviderRequestEvent{
		Provider:   providerType,
		Operation:  operation,
		Result:     result,
		ErrorClass: errorClass,
		Duration:   duration,
	})
}

func observabilityOperation(operationType outbox.OperationType) string {
	switch operationType {
	case outbox.OperationTypeUpsert:
		return observability.OperationUpsert
	case outbox.OperationTypeDelete:
		return observability.OperationDelete
	default:
		return string(operationType)
	}
}

func destinationTypeFromRef(destinationRef string) string {
	destinationType, _, ok := strings.Cut(destinationRef, "/")
	if !ok {
		return destinationRef
	}
	return destinationType
}

func shouldRetryAutomatically(errorClass providers.ErrorClass, attempts int) bool {
	if attempts >= maxAutomaticRetryAttempts {
		return false
	}
	return errorClass == providers.ErrorClassRateLimit || errorClass == providers.ErrorClassUnavailable
}

func retryDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return retryBaseDelay
	}
	delay := retryBaseDelay << (attempts - 1)
	if delay > retryMaxDelay {
		return retryMaxDelay
	}
	return delay
}
