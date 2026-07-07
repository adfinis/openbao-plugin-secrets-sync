package backend

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

type driftReconcileCandidate struct {
	association       associationRecord
	metadata          metadataRecord
	version           versionRecord
	objectID          string
	lastReconcileTime string
}

func (b *secretSyncBackend) periodicDriftReconcile(
	ctx context.Context,
	storage logical.Storage,
	cfg globalConfig,
	now time.Time,
) error {
	if cfg.DriftRepair == driftRepairOff {
		return nil
	}
	interval, err := time.ParseDuration(cfg.DriftReconcileInterval)
	if err != nil {
		return err
	}
	candidates, err := driftReconcileCandidates(ctx, storage, cfg, now, interval)
	if err != nil {
		return err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.lastReconcileTime != right.lastReconcileTime {
			return left.lastReconcileTime < right.lastReconcileTime
		}
		if left.association.Path != right.association.Path {
			return left.association.Path < right.association.Path
		}
		if left.association.ID != right.association.ID {
			return left.association.ID < right.association.ID
		}
		return left.objectID < right.objectID
	})

	limit := cfg.DriftReconcileBatch
	if limit > len(candidates) {
		limit = len(candidates)
	}
	nowString := now.Format(timeFormatRFC3339)
	for _, candidate := range candidates[:limit] {
		result := b.reconcileAssociationObjectFromStorage(
			ctx,
			storage,
			candidate.association,
			candidate.metadata,
			candidate.version,
			candidate.objectID,
			cfg,
		)
		b.recordReconcileRun(ctx, result)
		if err := putReconcileStatus(ctx, storage, result, nowString); err != nil {
			return err
		}
		if cfg.DriftRepair != driftRepairRepair || !isRepairableDrift(result) {
			continue
		}
		if err := b.enqueuePeriodicDriftRepair(ctx, storage, cfg, candidate, nowString); err != nil {
			return err
		}
	}
	return nil
}

func (b *secretSyncBackend) enqueuePeriodicDriftRepair(
	ctx context.Context,
	storage logical.Storage,
	cfg globalConfig,
	candidate driftReconcileCandidate,
	now string,
) error {
	if cfg.RestoreGuard {
		b.recordRemoteMutationBlocked(ctx, observability.OperationPeriodic, observability.ReasonRestoreGuard)
		return nil
	}
	_, err := b.enqueueAssociationCurrentVersionAsDriftRepair(
		ctx,
		storage,
		candidate.association,
		candidate.metadata,
		now,
	)
	if errors.Is(err, errQueueCapacity) {
		b.recordRemoteMutationBlocked(ctx, observability.OperationPeriodic, observability.ReasonCapacity)
		return nil
	}
	return err
}

func driftReconcileCandidates(
	ctx context.Context,
	storage logical.Storage,
	cfg globalConfig,
	now time.Time,
	interval time.Duration,
) ([]driftReconcileCandidate, error) {
	associations, err := listAllAssociations(ctx, storage)
	if err != nil {
		return nil, err
	}
	candidates := []driftReconcileCandidate{}
	for _, association := range associations {
		associationCandidates, err := driftReconcileCandidatesForAssociation(
			ctx,
			storage,
			cfg,
			association,
			now,
			interval,
		)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, associationCandidates...)
	}
	return candidates, nil
}

func driftReconcileCandidatesForAssociation(
	ctx context.Context,
	storage logical.Storage,
	cfg globalConfig,
	association associationRecord,
	now time.Time,
	interval time.Duration,
) ([]driftReconcileCandidate, error) {
	if !association.Enabled {
		return nil, nil
	}
	metadata, err := getMetadata(ctx, storage, association.Path)
	if err != nil || metadata == nil || metadata.CurrentVersion == 0 {
		return nil, err
	}
	version, err := getVersion(ctx, storage, association.Path, metadata.CurrentVersion)
	if err != nil || version == nil || version.Destroyed || version.DeletionTime != "" {
		return nil, err
	}
	objectIDs, err := associationObjectIDs(association, version.Data)
	if err != nil {
		return nil, nil
	}
	candidates := make([]driftReconcileCandidate, 0, len(objectIDs))
	for _, objectID := range objectIDs {
		candidate, ok, err := driftReconcileCandidateForObject(
			ctx,
			storage,
			association,
			*metadata,
			*version,
			cfg,
			objectID,
			now,
			interval,
		)
		if err != nil {
			return nil, err
		}
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func driftReconcileCandidateForObject(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
	metadata metadataRecord,
	version versionRecord,
	cfg globalConfig,
	objectID string,
	now time.Time,
	interval time.Duration,
) (driftReconcileCandidate, bool, error) {
	if !driftDetectionAllowsQueuedUpsert(cfg) {
		queued, err := hasQueuedUpsertForAssociationObject(
			ctx,
			storage,
			association.Path,
			association.ID,
			objectID,
			metadata.CurrentVersion,
		)
		if err != nil || queued {
			return driftReconcileCandidate{}, false, err
		}
	}
	status, err := getStatus(ctx, storage, association.Path, association.ID, objectID)
	if err != nil || !driftReconcileDue(status, now, interval) {
		return driftReconcileCandidate{}, false, err
	}
	return driftReconcileCandidate{
		association:       association,
		metadata:          metadata,
		version:           version,
		objectID:          objectID,
		lastReconcileTime: lastReconcileTimeForSort(status),
	}, true, nil
}

func driftDetectionAllowsQueuedUpsert(cfg globalConfig) bool {
	return cfg.RestoreGuard || cfg.DriftRepair == driftRepairDetect
}

func listAllAssociations(ctx context.Context, storage logical.Storage) ([]associationRecord, error) {
	keys, err := logical.CollectKeysWithPrefix(ctx, storage, associationStoragePrefix)
	if err != nil {
		return nil, err
	}
	records := make([]associationRecord, 0, len(keys))
	for _, key := range keys {
		entry, err := storage.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			continue
		}
		var record associationRecord
		if err := entry.DecodeJSON(&record); err != nil {
			return nil, err
		}
		normalizeAssociationDefaults(&record)
		records = append(records, record)
	}
	return records, nil
}

func driftReconcileDue(status *statusRecord, now time.Time, interval time.Duration) bool {
	if status == nil || status.LastReconcileTime == "" {
		return true
	}
	last, err := time.Parse(timeFormatRFC3339, status.LastReconcileTime)
	if err != nil {
		return true
	}
	return !last.Add(interval).After(now)
}

func lastReconcileTimeForSort(status *statusRecord) string {
	if status == nil || status.LastReconcileTime == "" {
		return ""
	}
	return status.LastReconcileTime
}

func (b *secretSyncBackend) reconcileAssociationObjectFromStorage(
	ctx context.Context,
	storage logical.Storage,
	association associationRecord,
	metadata metadataRecord,
	version versionRecord,
	objectID string,
	cfg globalConfig,
) reconcileObjectResult {
	if !association.Enabled {
		return newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			objectID,
			"",
			domain.SyncStateDisabled,
			"",
			"association is disabled",
		)
	}
	provider, err := b.providerRegistry.MustGet(association.DestinationType)
	if err != nil {
		return newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			objectID,
			"",
			domain.SyncStateValidationError,
			providers.ErrorClassValidation,
			"destination provider is unsupported",
		)
	}
	destination, err := getDestination(ctx, storage, association.DestinationType, association.DestinationName)
	if err != nil {
		return newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			objectID,
			"",
			domain.SyncStateInternalError,
			providers.ErrorClassInternal,
			"destination lookup failed",
		)
	}
	if destination == nil {
		return newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			objectID,
			"",
			domain.SyncStateValidationError,
			providers.ErrorClassValidation,
			"destination is missing",
		)
	}
	if destination.Disabled {
		return newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			objectID,
			"",
			domain.SyncStateDisabled,
			"",
			"destination is disabled",
		)
	}
	resolvedDestinationConfig, err := destinationConfig(ctx, storage, *destination)
	if err != nil {
		return newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			objectID,
			"",
			domain.SyncStateInternalError,
			providers.ErrorClassInternal,
			"destination config resolution failed",
		)
	}
	runtimeIdentity, err := providerRuntimeIdentity(ctx, storage)
	if err != nil {
		return newReconcileObjectResult(
			association,
			metadata.CurrentVersion,
			objectID,
			"",
			domain.SyncStateInternalError,
			providers.ErrorClassInternal,
			"runtime identity resolution failed",
		)
	}
	return b.reconcileAssociationObject(
		ctx,
		association,
		provider,
		*destination,
		resolvedDestinationConfig,
		runtimeIdentity,
		metadata.CurrentVersion,
		version,
		objectID,
		cfg,
	)
}

func isRepairableDrift(result reconcileObjectResult) bool {
	if result.state != domain.SyncStateDrifted || result.remoteState == nil {
		return false
	}
	if !result.remoteState.Exists || !result.remoteState.OwnershipKnown || !result.remoteState.Owned {
		return false
	}
	if result.remoteState.PayloadSHA256 == "" {
		return false
	}
	return strings.TrimSpace(result.remoteState.Verification) != ""
}
