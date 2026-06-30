// Package backend implements the OpenBao logical backend for secret sync.
package backend

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/observability"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-secret-sync/internal/providers/fake"
	"github.com/adfinis/openbao-secret-sync/internal/providers/kubernetessecrets"
	"github.com/adfinis/openbao-secret-sync/internal/version"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	destinationSecretsPrefix = "destinations_secrets/"
	localSecretDataPrefix    = "data/"
)

// Factory constructs and initializes a backend instance for OpenBao.
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b := Backend(conf)
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

// Backend creates an uninitialized logical backend.
func Backend(_ *logical.BackendConfig) *secretSyncBackend {
	b := secretSyncBackend{
		providerRegistry: providers.MustNewRegistry(
			fake.Provider{},
			awssecretsmanager.New(),
			kubernetessecrets.New(),
		),
		observer: observability.New(),
	}
	b.Backend = &framework.Backend{
		Help: strings.TrimSpace(backendHelp),
		PathsSpecial: &logical.Paths{
			SealWrapStorage: []string{
				destinationSecretsPrefix,
				localSecretDataPrefix,
			},
		},
		Paths: framework.PathAppend(
			[]*framework.Path{pathConfig(&b), pathConfigRestoreGuardAcknowledge(&b)},
			pathDestinations(&b),
			pathAssociations(&b),
			pathMetadata(&b),
			pathVersionMutations(&b),
			[]*framework.Path{pathData(&b), pathStatus(&b)},
			pathReconcile(&b),
			pathQueue(&b),
		),
		Invalidate: func(_ context.Context, _ string) {
			b.invalidate()
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
	providerRegistry *providers.Registry
	observer         observability.Recorder
}

func (b *secretSyncBackend) invalidate() {
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
}

func (b *secretSyncBackend) periodic(ctx context.Context, req *logical.Request) error {
	if req == nil || req.Storage == nil {
		return nil
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return err
	}
	if cfg.Disabled || cfg.RestoreGuard {
		return nil
	}
	now := nowUTC()
	if err := recoverIncompleteEnqueueIntents(ctx, req.Storage, now); err != nil {
		return err
	}
	return b.processDueOutbox(ctx, req.Storage, now)
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

const backendHelp = `
The OpenBao secret sync backend stores local source secrets and asynchronously
synchronizes eligible secrets to configured external destinations.

This scaffold exposes the initial plugin paths and versioned backend shape. The
KV, outbox, provider, and reconciliation implementations are added in focused
implementation phases.
`
