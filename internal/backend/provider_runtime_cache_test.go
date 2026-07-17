package backend

import (
	"context"
	"sync"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
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

	first, releaseFirst, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("first runtime: %v", err)
	}
	defer releaseFirst(ctx)
	second, releaseSecond, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("second runtime: %v", err)
	}
	defer releaseSecond(ctx)
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
	third, releaseThird, err := b.destinationRuntime(ctx, provider, record, changed)
	if err != nil {
		t.Fatalf("changed runtime: %v", err)
	}
	defer releaseThird(ctx)
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
	fourth, releaseFourth, err := b.destinationRuntime(ctx, provider, record, changed)
	if err != nil {
		t.Fatalf("runtime after invalidation: %v", err)
	}
	defer releaseFourth(ctx)
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

func TestDestinationRuntimeCacheHonorsCachingDisabled(t *testing.T) {
	ctx := context.Background()
	system := &logical.StaticSystemView{}
	b := Backend(nil)
	if err := b.Setup(ctx, &logical.BackendConfig{System: system}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	provider := &countingProvider{providerType: "counting"}
	record := destinationRecord{Type: provider.Type(), Name: "prod"}
	cfg := providers.DestinationConfig{Name: "prod"}

	cached, releaseCached, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("cached runtime: %v", err)
	}
	releaseCached(ctx)
	if provider.runtimes[0].closed != 0 {
		t.Fatalf("cached runtime closes = %d, want 0", provider.runtimes[0].closed)
	}

	system.CachingDisabledVal = true
	uncached, releaseUncached, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("uncached runtime: %v", err)
	}
	if uncached == cached {
		t.Fatal("disabled caching must open a fresh runtime")
	}
	if provider.runtimes[0].closed != 1 {
		t.Fatalf("previous cached runtime closes = %d, want 1", provider.runtimes[0].closed)
	}
	if len(b.runtimeCache) != 0 {
		t.Fatalf("runtime cache size = %d, want 0", len(b.runtimeCache))
	}
	releaseUncached(ctx)
	if provider.runtimes[1].closed != 1 {
		t.Fatalf("uncached runtime closes = %d, want 1", provider.runtimes[1].closed)
	}

	_, releaseNext, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("next uncached runtime: %v", err)
	}
	releaseNext(ctx)
	if provider.opens != 3 {
		t.Fatalf("opens with caching disabled = %d, want 3", provider.opens)
	}
}

func TestDestinationRuntimeInvalidationDuringBuildPreservesNewBuild(t *testing.T) {
	ctx := context.Background()
	b := Backend(nil)
	provider := newStagedOpenProvider("staged", 2)
	record := destinationRecord{Type: provider.Type(), Name: "prod"}
	cfg := providers.DestinationConfig{Name: "prod"}
	ref := destinationRef(record.Type, record.Name)

	firstResult := acquireDestinationRuntimeAsync(ctx, b, provider, record, cfg)
	if opened := <-provider.started; opened != 1 {
		t.Fatalf("first open number = %d, want 1", opened)
	}
	b.invalidate(ctx, "")

	secondResult := acquireDestinationRuntimeAsync(ctx, b, provider, record, cfg)
	if opened := <-provider.started; opened != 2 {
		t.Fatalf("second open number = %d, want 2", opened)
	}

	close(provider.release[0])
	first := <-firstResult
	if first.err != nil {
		t.Fatalf("first runtime: %v", first.err)
	}
	first.release(ctx)
	if provider.runtimes[0].closed != 1 {
		t.Fatalf("invalidated in-flight runtime closes = %d, want 1", provider.runtimes[0].closed)
	}

	b.cacheMu.Lock()
	currentBuild := b.runtimeBuilds[ref]
	b.cacheMu.Unlock()
	if currentBuild == nil {
		t.Fatal("stale build completion removed the replacement build")
	}

	close(provider.release[1])
	second := <-secondResult
	if second.err != nil {
		t.Fatalf("second runtime: %v", second.err)
	}
	defer second.release(ctx)
	thirdRuntime, releaseThird, err := b.destinationRuntime(ctx, provider, record, cfg)
	if err != nil {
		t.Fatalf("cached runtime after replacement build: %v", err)
	}
	defer releaseThird(ctx)
	if thirdRuntime != second.runtime {
		t.Fatal("replacement build must populate the runtime cache")
	}
	if provider.opens != 2 {
		t.Fatalf("opens = %d, want 2", provider.opens)
	}

	b.invalidate(ctx, destinationStorageKey(record.Type, record.Name))
	if provider.runtimes[1].closed != 1 {
		t.Fatalf("replacement cached runtime closes = %d, want 1", provider.runtimes[1].closed)
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

type destinationRuntimeResult struct {
	runtime providers.DestinationRuntime
	release func(context.Context)
	err     error
}

func acquireDestinationRuntimeAsync(
	ctx context.Context,
	b *secretSyncBackend,
	provider providers.Provider,
	record destinationRecord,
	cfg providers.DestinationConfig,
) <-chan destinationRuntimeResult {
	result := make(chan destinationRuntimeResult, 1)
	go func() {
		runtime, release, err := b.destinationRuntime(ctx, provider, record, cfg)
		result <- destinationRuntimeResult{runtime: runtime, release: release, err: err}
	}()
	return result
}

type stagedOpenProvider struct {
	*countingProvider

	mu      sync.Mutex
	started chan int
	release []chan struct{}
}

func newStagedOpenProvider(providerType string, stages int) *stagedOpenProvider {
	release := make([]chan struct{}, stages)
	for i := range release {
		release[i] = make(chan struct{})
	}
	return &stagedOpenProvider{
		countingProvider: &countingProvider{providerType: providerType},
		started:          make(chan int, stages),
		release:          release,
	}
}

func (p *stagedOpenProvider) OpenDestination(
	ctx context.Context,
	_ providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	p.mu.Lock()
	p.opens++
	openNumber := p.opens
	runtime := &countingRuntime{}
	p.runtimes = append(p.runtimes, runtime)
	release := p.release[openNumber-1]
	p.mu.Unlock()

	p.started <- openNumber
	select {
	case <-release:
		return runtime, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
