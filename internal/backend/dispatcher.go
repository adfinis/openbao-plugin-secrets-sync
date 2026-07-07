package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	retryJitterDivisor                = 5
	retryMaxDelay                     = 5 * time.Minute
	outboxClaimLease                  = 5 * time.Minute
	providerMutationTimeout           = 2 * time.Minute
	driftRepairWarningThreshold       = 3
	defaultPeriodicMaxOperations      = 100
	defaultPeriodicRecoveryMaxIntents = 100
)

func (b *secretSyncBackend) processDueOutboxLimit(
	ctx context.Context,
	storage logical.Storage,
	now time.Time,
	maxOperations int,
	operation string,
) (int, error) {
	claimOwner, err := b.outboxClaimOwner(ctx, storage)
	if err != nil {
		return 0, err
	}
	if err := b.resetOutboxClaimsForOtherOwners(ctx, storage, claimOwner, now); err != nil {
		return 0, err
	}
	ids, err := listDueOutboxIDs(ctx, storage, now)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		claimed, ok, err := b.claimDispatchableOutboxRecord(ctx, storage, id, claimOwner, now, operation)
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

func (b *secretSyncBackend) claimDispatchableOutboxRecord(
	ctx context.Context,
	storage logical.Storage,
	id string,
	claimOwner string,
	now time.Time,
	operation string,
) (*outboxRecord, bool, error) {
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()

	record, err := getOutbox(ctx, storage, id)
	if err != nil {
		return nil, false, err
	}
	if record == nil || !isDispatchableOutboxState(record.State) {
		return nil, false, nil
	}
	if !isSupportedOperation(*record) {
		if err := discardUnsupportedOutboxOperation(ctx, storage, *record); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	blocked, err := b.claimBlockedByGlobalSwitch(ctx, storage, operation)
	if err != nil || blocked {
		return nil, false, err
	}
	return claimOutboxRecord(ctx, storage, *record, claimOwner, now)
}

func (b *secretSyncBackend) claimBlockedByGlobalSwitch(
	ctx context.Context,
	storage logical.Storage,
	operation string,
) (bool, error) {
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return false, err
	}
	switch {
	case cfg.Disabled:
		b.recordRemoteMutationBlocked(ctx, operation, observability.ReasonDisabled)
		return true, nil
	case cfg.RestoreGuard:
		b.recordRemoteMutationBlocked(ctx, operation, observability.ReasonRestoreGuard)
		return true, nil
	default:
		return false, nil
	}
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
	record.ClaimAttempt++
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

func (b *secretSyncBackend) resetOutboxClaimsForOtherOwners(
	ctx context.Context,
	storage logical.Storage,
	claimOwner string,
	now time.Time,
) error {
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()

	ids, err := listQueuedOutboxIDs(ctx, storage)
	if err != nil {
		return err
	}
	nowString := now.Format(timeFormatRFC3339)
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return err
		}
		if record == nil ||
			record.ClaimOwner == "" ||
			record.ClaimOwner == claimOwner {
			continue
		}
		clearOutboxClaim(record)
		record.UpdatedTime = nowString
		if err := putOutbox(ctx, storage, *record); err != nil {
			return err
		}
	}
	return nil
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
		return b.cancelClaimedOutboxOperation(ctx, storage, record)
	}
	upsertContext, failure, err := b.loadUpsertContext(ctx, storage, record)
	if err != nil {
		return err
	}
	if failure != nil {
		return b.markClaimedOperationFailed(ctx, storage, record, *failure, now)
	}
	preparedPayload, failure := prepareProviderPayload(upsertContext, record.ObjectID)
	if failure != nil {
		return b.markClaimedOperationFailed(ctx, storage, record, *failure, now)
	}
	resolvedName, failure := resolvedNameForOperation(upsertContext.association, record.ObjectID)
	if failure != nil {
		return b.markClaimedOperationFailed(ctx, storage, record, *failure, now)
	}
	if handled, err := b.failClaimedUpsertForDestinationPolicy(
		ctx,
		storage,
		record,
		upsertContext,
		resolvedName,
		preparedPayload,
		now,
	); err != nil || handled {
		return err
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
	if err := dispatchProviderContextError(ctx, err); err != nil {
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
		return b.markClaimedOperationFailed(ctx, storage, record, operationFailure{
			class:         errorClass,
			message:       "provider upsert failed",
			resolvedName:  resolvedName,
			payloadSHA256: preparedPayload.SHA256,
		}, now)
	}
	if result == nil {
		result = &providers.SyncResult{}
	}

	return b.commitClaimedUpsertSuccess(ctx, storage, record, resolvedName, preparedPayload, result, now)
}

func dispatchProviderContextError(ctx context.Context, providerErr error) error {
	if providerErr != nil && isDispatchContextCanceled(ctx, providerErr) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return providerErr
	}
	return ctx.Err()
}

func (b *secretSyncBackend) failClaimedUpsertForDestinationPolicy(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	upsertContext *upsertContext,
	resolvedName string,
	preparedPayload payloadpkg.CanonicalPayload,
	now time.Time,
) (bool, error) {
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return false, err
	}
	if err := validateDestinationPolicyForObject(
		*upsertContext.destination,
		*upsertContext.association,
		record.ObjectID,
		resolvedName,
		cfg,
	); err != nil {
		failure := operationFailure{
			class:         providers.ErrorClassValidation,
			message:       err.Error(),
			resolvedName:  resolvedName,
			payloadSHA256: preparedPayload.SHA256,
		}
		return true, b.markClaimedOperationFailed(ctx, storage, record, failure, now)
	}
	return false, nil
}

func markUpsertSuccessStatus(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	resolvedName string,
	preparedPayload payloadpkg.CanonicalPayload,
	result *providers.SyncResult,
	now time.Time,
) (*statusRecord, error) {
	status := statusRecord{
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
	}
	existingStatus, err := getStatus(ctx, storage, record.Path, record.AssociationID, record.ObjectID)
	if err != nil {
		return nil, err
	}
	if outboxTrigger(record) == outboxTriggerDriftRepair {
		if existingStatus != nil {
			status.RepairCount = existingStatus.RepairCount
		}
		status.LastRepairTime = now.Format(timeFormatRFC3339)
		status.RepairCount++
	}
	if err := putStatus(ctx, storage, status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (b *secretSyncBackend) commitClaimedUpsertSuccess(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	resolvedName string,
	preparedPayload payloadpkg.CanonicalPayload,
	result *providers.SyncResult,
	now time.Time,
) error {
	claimed := record
	record.Attempts++
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	clearOutboxClaim(&record)
	var status *statusRecord
	committed, err := b.commitIfOutboxClaimHeld(ctx, storage, claimed, func() error {
		var err error
		status, err = markUpsertSuccessStatus(ctx, storage, record, resolvedName, preparedPayload, result, now)
		if err != nil {
			return err
		}
		return deleteOutbox(ctx, storage, record)
	})
	if err != nil {
		return err
	}
	if !committed {
		return nil
	}
	b.recordOperationSuccess(ctx, record)
	b.warnIfDriftRepairThresholdExceeded(record, status)
	return nil
}

func (b *secretSyncBackend) warnIfDriftRepairThresholdExceeded(record outboxRecord, status *statusRecord) {
	if outboxTrigger(record) != outboxTriggerDriftRepair ||
		status == nil ||
		status.RepairCount <= driftRepairWarningThreshold {
		return
	}
	b.Logger().Warn(
		"background drift repair count exceeded threshold",
		"destination_type", destinationTypeFromRef(record.DestinationRef),
		"granularity", operationGranularity(record),
		"repair_count", status.RepairCount,
		"threshold", driftRepairWarningThreshold,
	)
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
		return b.markClaimedOperationFailed(ctx, storage, record, *failure, now)
	}
	resolvedName, failure := resolvedNameForOperation(deleteContext.association, record.ObjectID)
	if failure != nil {
		return b.markClaimedOperationFailed(ctx, storage, record, *failure, now)
	}
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return err
	}
	if err := validateDestinationPolicyForObject(
		*deleteContext.destination,
		*deleteContext.association,
		record.ObjectID,
		resolvedName,
		cfg,
	); err != nil {
		failure := operationFailure{
			class:        providers.ErrorClassValidation,
			message:      err.Error(),
			resolvedName: resolvedName,
		}
		return b.markClaimedOperationFailed(ctx, storage, record, failure, now)
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
	if err := dispatchProviderContextError(ctx, err); err != nil {
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
		return b.markClaimedOperationFailed(ctx, storage, record, operationFailure{
			class:        errorClass,
			message:      "provider delete failed",
			resolvedName: resolvedName,
		}, now)
	}
	if result == nil {
		result = &providers.SyncResult{}
	}

	return b.commitClaimedDeleteSuccess(ctx, storage, record, resolvedName, result, now)
}

func (b *secretSyncBackend) commitClaimedDeleteSuccess(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	resolvedName string,
	result *providers.SyncResult,
	now time.Time,
) error {
	claimed := record
	record.Attempts++
	record.UpdatedTime = now.Format(timeFormatRFC3339)
	clearOutboxClaim(&record)
	committed, err := b.commitIfOutboxClaimHeld(ctx, storage, claimed, func() error {
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
	})
	if err != nil {
		return err
	}
	if !committed {
		return nil
	}
	b.recordOperationSuccess(ctx, record)
	return nil
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
	mutationCtx, cancel := providerMutationContext(ctx)
	defer cancel()

	runtime, err := b.destinationRuntime(mutationCtx, ctxData.provider, *ctxData.destination, destinationConfig)
	if err != nil {
		return nil, err
	}
	return runtime.Upsert(mutationCtx, providers.UpsertRequest{
		Runtime:        runtimeIdentity,
		ResolvedName:   resolvedName,
		Format:         preparedPayload.Format,
		Payload:        preparedPayload.Bytes,
		PayloadSHA256:  preparedPayload.SHA256,
		IdempotencyKey: record.IdempotencyKey,
		DataMap:        preparedPayload.Data,
		SourcePath:     record.Path,
		SourceVersion:  record.Version,
		AssociationID:  record.AssociationID,
		ObjectID:       record.ObjectID,
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
	mutationCtx, cancel := providerMutationContext(ctx)
	defer cancel()

	runtime, err := b.destinationRuntime(mutationCtx, ctxData.provider, *ctxData.destination, destinationConfig)
	if err != nil {
		return nil, err
	}
	return runtime.Delete(mutationCtx, providers.DeleteRequest{
		Runtime:        runtimeIdentity,
		ResolvedName:   resolvedName,
		IdempotencyKey: record.IdempotencyKey,
		DataMap:        normalizedDataMapping(ctxData.association.DataMapping) == dataMappingSourceKeys,
		SourcePath:     record.Path,
		SourceVersion:  record.Version,
		AssociationID:  record.AssociationID,
		ObjectID:       record.ObjectID,
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
	failure, err := sourceEligibilityFailureForDispatch(ctx, storage, *association)
	if failure != nil || err != nil {
		return nil, failure, err
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
	failure, err := sourceEligibilityFailureForDispatch(ctx, storage, *association)
	if failure != nil || err != nil {
		return nil, failure, err
	}
	return &deleteContext{
		association: association,
		destination: destination,
		provider:    provider,
	}, nil, nil
}

func sourceEligibilityFailureForDispatch(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
) (*operationFailure, error) {
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		return nil, err
	}
	metadata, err := getMetadata(ctx, storage, association.Path)
	if err != nil {
		return nil, err
	}
	if err := validateAssociationActivation(association, metadata, cfg); err != nil {
		return &operationFailure{
			class:        providers.ErrorClassValidation,
			message:      err.Error(),
			resolvedName: association.ResolvedName,
		}, nil
	}
	return nil, nil
}

func prepareProviderPayload(
	upsertContext *upsertContext,
	objectID string,
) (payloadpkg.CanonicalPayload, *operationFailure) {
	preparedPayload, err := buildCanonicalPayloadForObject(
		*upsertContext.association,
		upsertContext.version.Data,
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
	association associationRecord,
	data secretPayload,
	objectID string,
) (payloadpkg.CanonicalPayload, error) {
	if normalizedDataMapping(association.DataMapping) == dataMappingSourceKeys {
		if association.Granularity != syncGranularitySecretPath || objectID != syncObjectIDSecretPath {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf(
				"data_mapping %q requires secret-path granularity",
				dataMappingSourceKeys,
			)
		}
		return buildDataMapPayloadForAssociation(association, data)
	}
	switch association.Format {
	case defaultAssociationFormat:
		switch association.Granularity {
		case syncGranularitySecretPath:
			return payloadpkg.BuildJSON(map[string]interface{}(data))
		case syncGranularitySecretKey:
			value, ok := data[objectID]
			if !ok {
				return payloadpkg.CanonicalPayload{}, fmt.Errorf("source key %q does not exist", objectID)
			}
			return payloadpkg.BuildJSON(map[string]interface{}{objectID: value})
		default:
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("unsupported granularity %q", association.Granularity)
		}
	case rawAssociationFormat:
		if association.Granularity != syncGranularitySecretKey {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("raw payload format requires secret-key granularity")
		}
		value, ok := data[objectID]
		if !ok {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("source key %q does not exist", objectID)
		}
		return payloadpkg.BuildRaw(value)
	default:
		return payloadpkg.CanonicalPayload{}, fmt.Errorf("unsupported payload format %q", association.Format)
	}
}

func buildDataMapPayloadForAssociation(
	association associationRecord,
	data secretPayload,
) (payloadpkg.CanonicalPayload, error) {
	dataMap := make(map[string][]byte, len(data))
	for sourceKey, value := range data {
		if strings.TrimSpace(sourceKey) != sourceKey || sourceKey == "" {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("source key must not be empty or have surrounding whitespace")
		}
		dataKey, err := renderDataKeyTemplate(association.DataKeyTemplate, sourceKey)
		if err != nil {
			return payloadpkg.CanonicalPayload{}, err
		}
		if err := validateDataMapKey(dataKey); err != nil {
			return payloadpkg.CanonicalPayload{}, err
		}
		if _, exists := dataMap[dataKey]; exists {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("data_key_template maps multiple source keys to %q", dataKey)
		}
		rawBytes, err := payloadpkg.RawBytes(value)
		if err != nil {
			return payloadpkg.CanonicalPayload{}, fmt.Errorf("source key %q is not string or bytes", sourceKey)
		}
		dataMap[dataKey] = rawBytes
	}
	return payloadpkg.BuildDataMap(dataMap)
}

func dataMapKeys(data map[string][]byte) []string {
	if len(data) == 0 {
		return nil
	}
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
	if errorClass, ok := providers.ClassifyTransportError(err); ok {
		return errorClass
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
	return errors.Is(err, context.Canceled)
}

func providerMutationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, providerMutationTimeout)
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
		record.NotBefore = now.Add(retryDelay(record)).Format(timeFormatRFC3339)
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

func (b *secretSyncBackend) markClaimedOperationFailed(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	failure operationFailure,
	now time.Time,
) error {
	errorClass := failure.class
	if errorClass == "" {
		errorClass = providers.ErrorClassInternal
	}
	committed, err := b.commitIfOutboxClaimHeld(ctx, storage, record, func() error {
		return markOperationFailed(ctx, storage, record, failure, now)
	})
	if err != nil {
		return err
	}
	if committed {
		b.recordOperationFailure(ctx, record, errorClass)
	}
	return nil
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

func (b *secretSyncBackend) cancelClaimedOutboxOperation(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
) error {
	_, err := b.commitIfOutboxClaimHeld(ctx, storage, record, func() error {
		return deleteOutbox(ctx, storage, record)
	})
	return err
}

func (b *secretSyncBackend) commitIfOutboxClaimHeld(
	ctx context.Context,
	storage logical.Storage,
	record outboxRecord,
	commit func() error,
) (bool, error) {
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()

	current, err := getOutbox(ctx, storage, record.ID)
	if err != nil {
		return false, err
	}
	if !outboxClaimMatches(current, record) {
		return false, nil
	}
	if err := commit(); err != nil {
		return false, err
	}
	return true, nil
}

func outboxClaimMatches(current *outboxRecord, claimed outboxRecord) bool {
	return current != nil &&
		claimed.ClaimOwner != "" &&
		current.ClaimOwner == claimed.ClaimOwner &&
		current.ClaimAttempt == claimed.ClaimAttempt
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
	if outboxTrigger(record) == outboxTriggerDriftRepair {
		b.recordDriftRepair(ctx, record, observability.ResultSuccess, "")
	}
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
	if outboxTrigger(record) == outboxTriggerDriftRepair {
		b.recordDriftRepair(ctx, record, result, string(errorClass))
	}
}

func (b *secretSyncBackend) recordDriftRepair(
	ctx context.Context,
	record outboxRecord,
	result string,
	errorClass string,
) {
	b.observer.DriftRepair(ctx, observability.DriftRepairEvent{
		Result:          result,
		ErrorClass:      errorClass,
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

func retryDelay(record outboxRecord) time.Duration {
	delay := retryBaseDelayForAttempts(record.Attempts)
	if delay >= retryMaxDelay {
		return retryMaxDelay
	}
	jitter := retryJitter(record, delay)
	if delay+jitter > retryMaxDelay {
		return retryMaxDelay
	}
	return delay + jitter
}

func retryBaseDelayForAttempts(attempts int) time.Duration {
	if attempts <= 1 {
		return retryBaseDelay
	}
	delay := retryBaseDelay << (attempts - 1)
	if delay > retryMaxDelay {
		return retryMaxDelay
	}
	return delay
}

func retryJitter(record outboxRecord, delay time.Duration) time.Duration {
	maxJitter := delay / retryJitterDivisor
	maxJitterSeconds := int64(maxJitter / time.Second)
	if maxJitterSeconds <= 0 {
		return 0
	}
	identity := record.ID
	if identity == "" {
		identity = strings.Join([]string{
			record.Path,
			record.AssociationID,
			record.ObjectID,
			record.DestinationRef,
		}, "\x00")
	}
	return time.Duration(retryJitterSlot(identity, record.Attempts, maxJitterSeconds+1)) * time.Second
}

func retryJitterSlot(identity string, attempts int, slots int64) int64 {
	if slots <= 1 {
		return 0
	}
	hash := int64(17)
	for i := 0; i < len(identity); i++ {
		hash = (hash*31 + int64(identity[i])) % slots
	}
	attempt := int64(attempts)
	if attempt < 0 {
		attempt = -attempt
	}
	return (hash*31 + attempt) % slots
}
