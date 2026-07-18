// Package backend implements the OpenBao logical backend for secret sync.
package backend

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/kubernetessecrets"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/version"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/helper/consts"
	"github.com/openbao/openbao/sdk/v2/helper/locksutil"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	destinationSecretsPrefix = "destinations_secrets/"
	localSecretDataPrefix    = "data/"
)

var errBackendUUIDRequired = errors.New("OpenBao backend UUID is required for a mounted backend")

// Factory constructs and initializes a backend instance for OpenBao.
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b := Backend(conf)
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

// Backend creates an uninitialized logical backend.
func Backend(conf *logical.BackendConfig) *secretSyncBackend {
	return backendWithProviders(backendUUIDFromConfig(conf), productionProviders()...)
}

func backendUUIDFromConfig(conf *logical.BackendConfig) string {
	if conf == nil {
		return ""
	}
	return strings.TrimSpace(conf.BackendUUID)
}

func productionProviders() []providers.Provider {
	return []providers.Provider{
		awssecretsmanager.New(),
		gitlab.New(),
		kubernetessecrets.New(),
	}
}

func backendWithProviders(mountUUID string, providerSet ...providers.Provider) *secretSyncBackend {
	b := secretSyncBackend{
		mountUUID:        mountUUID,
		providerRegistry: providers.MustNewRegistry(providerSet...),
		observer:         observability.New(),
		dispatchWorkerID: bestEffortRuntimeID("worker"),
		writeLocks:       locksutil.CreateLocks(),
	}
	b.Backend = &framework.Backend{
		Help: strings.TrimSpace(backendHelp),
		PathsSpecial: &logical.Paths{
			SealWrapStorage: []string{
				destinationSecretsPrefix,
				localSecretDataPrefix,
			},
		},
		Paths: apiPaths(framework.PathAppend(
			[]*framework.Path{pathInfo(&b), pathConfig(&b), pathConfigRestoreGuardAcknowledge(&b)},
			pathDestinations(&b),
			pathAssociations(&b),
			pathMetadata(&b),
			pathSources(&b),
			pathVersionMutations(&b),
			[]*framework.Path{pathData(&b), pathStatus(&b)},
			pathReconcile(&b),
			pathQueue(&b),
		)),
		Invalidate: func(ctx context.Context, key string) {
			b.invalidate(ctx, key)
		},
		Clean: func(ctx context.Context) {
			b.cleanup(ctx)
		},
		InitializeFunc: func(ctx context.Context, req *logical.InitializationRequest) error {
			return b.initialize(ctx, req)
		},
		PeriodicFunc: func(ctx context.Context, req *logical.Request) error {
			return b.periodic(ctx, req)
		},
		BackendType:    logical.TypeLogical,
		RunningVersion: version.BuildInfo().Version,
	}
	return &b
}

type secretSyncBackend struct {
	*framework.Backend

	cacheMu          sync.Mutex
	configMu         sync.Mutex
	enqueueMu        sync.Mutex
	eventDispatchMu  sync.Mutex
	mountUUID        string
	dispatchWorkerID string
	writeLocks       []*locksutil.LockEntry
	providerRegistry *providers.Registry
	observer         observability.Recorder

	eventDispatchCh     chan eventDispatchSignal
	eventDispatchCancel context.CancelFunc
	eventDispatchDone   chan struct{}

	runtimeCache             map[string]destinationRuntimeCacheEntry
	runtimeBuilds            map[string]*destinationRuntimeBuild
	runtimeCacheEpoch        uint64
	runtimeDestinationEpochs map[string]uint64
}

func (b *secretSyncBackend) requiredMountUUID() (string, error) {
	if b == nil || strings.TrimSpace(b.mountUUID) == "" {
		return "", errBackendUUIDRequired
	}
	return b.mountUUID, nil
}

func (b *secretSyncBackend) initialize(ctx context.Context, req *logical.InitializationRequest) error {
	if req == nil || req.Storage == nil {
		return nil
	}
	if _, err := b.requiredMountUUID(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Initialize can be called again for an existing backend instance. Stop the
	// previous worker before validating and attaching the new initialized
	// storage handle so it cannot continue dispatching through stale storage.
	b.stopEventDispatcher(ctx)
	if err := b.ensureRuntimeStateForRequest(ctx, req.Storage); err != nil {
		return err
	}
	b.startEventDispatcher(req.Storage)
	return nil
}

func (b *secretSyncBackend) invalidate(ctx context.Context, key string) {
	ref, ok := destinationRefFromInvalidationKey(key)
	if ok {
		b.invalidateDestinationRuntime(ctx, ref)
		return
	}
	if key == "" || strings.HasPrefix(key, destinationStoragePrefix) ||
		strings.HasPrefix(key, destinationSecretsPrefix) {
		b.clearDestinationRuntimes(ctx)
	}
}

func (b *secretSyncBackend) cleanup(ctx context.Context) {
	b.stopEventDispatcher(ctx)
	b.clearDestinationRuntimes(ctx)
}

func (b *secretSyncBackend) HandleRequest(ctx context.Context, req *logical.Request) (*logical.Response, error) {
	if req != nil && req.Storage != nil && req.Operation != logical.HelpOperation {
		if err := b.ensureRuntimeStateForRequest(ctx, req.Storage); err != nil {
			if isStorageSchemaCompatibilityError(err) {
				return logical.ErrorResponse(err.Error()), nil
			}
			return nil, err
		}
	}
	return b.Backend.HandleRequest(ctx, req)
}

func (b *secretSyncBackend) periodic(ctx context.Context, req *logical.Request) error {
	if req == nil || req.Storage == nil {
		return nil
	}
	if !b.remoteMutationAllowed() {
		b.recordRemoteMutationBlocked(ctx, observability.OperationPeriodic, observability.ReasonReplicationState)
		return nil
	}
	if err := b.ensureRuntimeStateForRequest(ctx, req.Storage); err != nil {
		return err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return err
	}
	if err := b.pruneTerminalOutboxRecords(ctx, req.Storage, nowUTC()); err != nil {
		return err
	}
	if cfg.Disabled {
		b.recordRemoteMutationBlocked(ctx, observability.OperationPeriodic, observability.ReasonDisabled)
		return nil
	}
	now := nowUTC()
	if err := b.recoverIncompleteEnqueueIntentsLimit(
		ctx,
		req.Storage,
		now,
		defaultPeriodicRecoveryMaxIntents,
	); err != nil {
		return err
	}
	var periodicErr error
	if err := b.periodicDriftReconcile(ctx, req.Storage, cfg, now); err != nil {
		periodicErr = errors.Join(periodicErr, err)
	}
	if cfg.RestoreGuard {
		b.recordRemoteMutationBlocked(ctx, observability.OperationPeriodic, observability.ReasonRestoreGuard)
		return periodicErr
	}
	_, err = b.processDueOutboxLimit(
		ctx,
		req.Storage,
		now,
		defaultPeriodicMaxOperations,
		observability.OperationPeriodic,
	)
	return errors.Join(periodicErr, err)
}

func (b *secretSyncBackend) ensureRuntimeStateForRequest(
	ctx context.Context,
	storage logical.Storage,
) error {
	if _, err := b.requiredMountUUID(); err != nil {
		return err
	}
	b.configMu.Lock()
	defer b.configMu.Unlock()
	_, err := ensureRuntimeState(ctx, storage)
	return err
}

func (b *secretSyncBackend) remoteMutationAllowed() bool {
	if b.Backend == nil || b.System() == nil {
		return true
	}
	sys := b.System()
	if sys.LocalMount() {
		return true
	}
	state := sys.ReplicationState()
	return !state.HasState(
		consts.ReplicationPerformanceSecondary |
			consts.ReplicationPerformanceStandby |
			consts.ReplicationPerformanceBootstrapping |
			consts.ReplicationDRSecondary |
			consts.ReplicationDRBootstrapping,
	)
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

const backendHelp = `
The OpenBao secret sync backend stores local source secrets and asynchronously
synchronizes eligible secrets to configured external destinations.

It provides versioned source storage, destination configuration, associations,
durable outbox dispatch, queue inspection, status reporting, and manual
reconciliation for this mount.
`
