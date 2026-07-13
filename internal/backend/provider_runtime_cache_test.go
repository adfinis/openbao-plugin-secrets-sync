package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

func TestDestinationRuntimeCacheReusesAndInvalidates(t *testing.T) {
	ctx := context.Background()
	b := Backend(nil)
	provider := &countingProvider{providerType: "counting"}
	record := destinationRecord{Type: provider.Type(), Name: "prod"}
	cfg := providers.DestinationConfig{
		Name:   "prod",
		Config: map[string]string{"token": "one"},
	}

	first, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("first runtime: %v", err)
	}
	second, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("second runtime: %v", err)
	}
	if first != second {
		t.Fatal("matching destination config must reuse cached runtime")
	}
	if provider.opens != 1 {
		t.Fatalf("opens = %d, want 1", provider.opens)
	}

	changed := providers.DestinationConfig{
		Name:   "prod",
		Config: map[string]string{"token": "two"},
	}
	third, err := b.destinationRuntime(ctx, provider, record, changed)
	if err != nil {
		t.Fatalf("changed runtime: %v", err)
	}
	if third == first {
		t.Fatal("changed destination config must build a new runtime")
	}
	if provider.opens != 2 {
		t.Fatalf("opens after config change = %d, want 2", provider.opens)
	}
	if provider.runtimes[0].closed != 1 {
		t.Fatalf("stale runtime closes = %d, want 1", provider.runtimes[0].closed)
	}

	b.invalidate(ctx, destinationStorageKey(record.Type, record.Name))
	if provider.runtimes[1].closed != 1 {
		t.Fatalf("invalidated runtime closes = %d, want 1", provider.runtimes[1].closed)
	}
	fourth, err := b.destinationRuntime(ctx, provider, record, changed)
	if err != nil {
		t.Fatalf("runtime after invalidation: %v", err)
	}
	if fourth == third {
		t.Fatal("destination invalidation must evict cached runtime")
	}
	if provider.opens != 3 {
		t.Fatalf("opens after invalidation = %d, want 3", provider.opens)
	}

	b.invalidate(ctx, destinationSensitiveStorageKey(record.Type, record.Name))
	if provider.runtimes[2].closed != 1 {
		t.Fatalf("sensitive invalidation closes = %d, want 1", provider.runtimes[2].closed)
	}
}

func TestDestinationWriteAndDeleteEvictRuntimeCache(t *testing.T) {
	env := newBackendTestEnv(t)
	ref := destinationRef(providerTypeFake, "default")
	writeRuntime := &countingRuntime{}
	env.b.runtimeCache = map[string]destinationRuntimeCacheEntry{
		ref: {fingerprint: "stale", runtime: writeRuntime},
	}

	writeResp := env.update("destinations/fake/default")
	assertNilOrNoErrorResponse(t, writeResp)
	if writeRuntime.closed != 1 {
		t.Fatalf("runtime closes after destination write = %d, want 1", writeRuntime.closed)
	}

	deleteRuntime := &countingRuntime{}
	env.b.runtimeCache = map[string]destinationRuntimeCacheEntry{
		ref: {fingerprint: "stale", runtime: deleteRuntime},
	}
	deleteResp := env.delete("destinations/fake/default")
	assertNilOrNoErrorResponse(t, deleteResp)
	if deleteRuntime.closed != 1 {
		t.Fatalf("runtime closes after destination delete = %d, want 1", deleteRuntime.closed)
	}
}

type countingProvider struct {
	providerType string
	opens        int
	runtimes     []*countingRuntime
}

func (p *countingProvider) Type() string {
	return p.providerType
}

func (*countingProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{MaxPayloadBytes: 1024}
}

func (*countingProvider) ValidateConfig(context.Context, providers.DestinationConfig) error {
	return nil
}

func (*countingProvider) NormalizeAssociationConfig(
	context.Context,
	providers.DestinationConfig,
	providers.AssociationConfig,
) (providers.AssociationConfig, error) {
	return providers.AssociationConfig{Config: map[string]string{}}, nil
}

func (p *countingProvider) OpenDestination(
	context.Context,
	providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	p.opens++
	runtime := &countingRuntime{}
	p.runtimes = append(p.runtimes, runtime)
	return runtime, nil
}

type countingRuntime struct {
	closed int
}

func (*countingRuntime) Health(context.Context) (*providers.HealthResult, error) {
	return &providers.HealthResult{Healthy: true}, nil
}

func (*countingRuntime) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
}

func (*countingRuntime) Upsert(context.Context, providers.UpsertRequest) (*providers.SyncResult, error) {
	return &providers.SyncResult{}, nil
}

func (*countingRuntime) Delete(context.Context, providers.DeleteRequest) (*providers.SyncResult, error) {
	return &providers.SyncResult{}, nil
}

func (*countingRuntime) ReadState(context.Context, providers.ReadStateRequest) (*providers.RemoteState, error) {
	return &providers.RemoteState{}, nil
}

func (r *countingRuntime) Close(context.Context) error {
	r.closed++
	return nil
}
