package backend

import (
	"context"
	"sync"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestDispatchRechecksDisabledBeforeEachClaim(t *testing.T) {
	runDispatchRechecksGlobalSwitchBeforeEachClaim(t, func(cfg *globalConfig) {
		cfg.Disabled = true
	}, func(t *testing.T, cfg globalConfig) {
		t.Helper()
		if !cfg.Disabled {
			t.Fatal("config disabled must be true after first mutation")
		}
	})
}

func TestDispatchRechecksRestoreGuardBeforeEachClaim(t *testing.T) {
	runDispatchRechecksGlobalSwitchBeforeEachClaim(t, func(cfg *globalConfig) {
		cfg.RestoreGuard = true
	}, func(t *testing.T, cfg globalConfig) {
		t.Helper()
		if !cfg.RestoreGuard {
			t.Fatal("restore guard must be true after first mutation")
		}
	})
}

func runDispatchRechecksGlobalSwitchBeforeEachClaim(
	t *testing.T,
	flip func(*globalConfig),
	assertConfig func(*testing.T, globalConfig),
) {
	t.Helper()
	env := newBackendTestEnv(t)
	provider := &configFlippingProvider{
		storage: env.storage,
		flip:    flip,
	}
	env.b.providerRegistry = providers.MustNewRegistry(provider)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	env.enableAppDBSourceSync()
	destinationResp := env.update("destinations/guard/prod")
	if destinationResp != nil && destinationResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", destinationResp.Error())
	}
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef("guard", "prod"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, associationResp)
	operationIDs := operationIDsFromResponse(t, associationResp)
	if len(operationIDs) != 2 {
		t.Fatalf("operation IDs = %v, want two queued operations", operationIDs)
	}

	processed, err := env.b.processDueOutboxLimit(
		context.Background(),
		env.storage,
		nowUTC(),
		10,
		observability.OperationPeriodic,
	)
	if err != nil {
		t.Fatalf("process due outbox: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed operations = %d, want 1", processed)
	}
	if got := provider.mutationCount(); got != 1 {
		t.Fatalf("provider mutations = %d, want 1", got)
	}
	remainingIDs, err := listQueuedOutboxIDs(context.Background(), env.storage)
	if err != nil {
		t.Fatalf("list queued operations: %v", err)
	}
	if len(remainingIDs) != 1 {
		t.Fatalf("remaining queued operations = %v, want one", remainingIDs)
	}
	remaining := assertOutboxOperation(t, env.storage, remainingIDs[0], 1, outboxStatePending)
	if remaining.ClaimOwner != "" || remaining.ClaimExpiresTime != "" {
		t.Fatalf("remaining claim = owner %q expires %q, want unclaimed", remaining.ClaimOwner, remaining.ClaimExpiresTime)
	}
	cfg, err := readGlobalConfig(context.Background(), env.storage)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	assertConfig(t, cfg)
}

type configFlippingProvider struct {
	storage logical.Storage
	flip    func(*globalConfig)

	mu        sync.Mutex
	mutations int
}

func (p *configFlippingProvider) Type() string {
	return "guard"
}

func (p *configFlippingProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		SupportsSecretKey:           true,
		MaxPayloadBytes:             1024 * 1024,
	}
}

func (*configFlippingProvider) ValidateConfig(context.Context, providers.DestinationConfig) error {
	return nil
}

func (p *configFlippingProvider) OpenDestination(
	context.Context,
	providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	return configFlippingRuntime{provider: p}, nil
}

func (p *configFlippingProvider) mutationCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mutations
}

func (p *configFlippingProvider) recordMutation() error {
	p.mu.Lock()
	p.mutations++
	mutations := p.mutations
	p.mu.Unlock()
	if mutations != 1 || p.flip == nil {
		return nil
	}
	cfg, err := readGlobalConfig(context.Background(), p.storage)
	if err != nil {
		return err
	}
	p.flip(&cfg)
	cfg.UpdatedTime = nowUTC().Format(timeFormatRFC3339)
	return putGlobalConfig(context.Background(), p.storage, cfg)
}

type configFlippingRuntime struct {
	provider *configFlippingProvider
}

func (configFlippingRuntime) Health(context.Context) (*providers.HealthResult, error) {
	return &providers.HealthResult{Healthy: true}, nil
}

func (configFlippingRuntime) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
}

func (r configFlippingRuntime) Upsert(context.Context, providers.UpsertRequest) (*providers.SyncResult, error) {
	if err := r.provider.recordMutation(); err != nil {
		return nil, err
	}
	return &providers.SyncResult{RemoteVersion: "guard"}, nil
}

func (r configFlippingRuntime) Delete(context.Context, providers.DeleteRequest) (*providers.SyncResult, error) {
	if err := r.provider.recordMutation(); err != nil {
		return nil, err
	}
	return &providers.SyncResult{RemoteVersion: "guard-delete"}, nil
}

func (configFlippingRuntime) ReadState(context.Context, providers.ReadStateRequest) (*providers.RemoteState, error) {
	return &providers.RemoteState{Exists: false}, nil
}

func (configFlippingRuntime) Close(context.Context) error {
	return nil
}
