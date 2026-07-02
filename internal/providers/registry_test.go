package providers

import (
	"context"
	"testing"
)

func TestRegistryRejectsDuplicateProviderTypes(t *testing.T) {
	_, err := NewRegistry(testProvider{providerType: "fake"}, testProvider{providerType: "fake"})
	if err == nil {
		t.Fatal("duplicate provider types must fail")
	}
}

func TestRegistryMustGet(t *testing.T) {
	registry, err := NewRegistry(testProvider{providerType: "fake"})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	provider, err := registry.MustGet("fake")
	if err != nil {
		t.Fatalf("must get fake provider: %v", err)
	}
	if got := provider.Type(); got != "fake" {
		t.Fatalf("provider type = %q, want fake", got)
	}
	if _, err := registry.MustGet("missing"); err == nil {
		t.Fatal("missing provider type must fail")
	}
}

type testProvider struct {
	providerType string
}

func (p testProvider) Type() string {
	return p.providerType
}

func (testProvider) Capabilities() Capabilities {
	return Capabilities{}
}

func (testProvider) ValidateConfig(context.Context, DestinationConfig) error {
	return nil
}

func (testProvider) OpenDestination(context.Context, DestinationConfig) (DestinationRuntime, error) {
	return testDestinationRuntime{}, nil
}

type testDestinationRuntime struct{}

func (testDestinationRuntime) Health(context.Context) (*HealthResult, error) {
	return &HealthResult{Healthy: true}, nil
}

func (testDestinationRuntime) Plan(context.Context, PlanRequest) (*PlanResult, error) {
	return &PlanResult{Action: PlanActionCreate}, nil
}

func (testDestinationRuntime) Upsert(context.Context, UpsertRequest) (*SyncResult, error) {
	return &SyncResult{}, nil
}

func (testDestinationRuntime) Delete(context.Context, DeleteRequest) (*SyncResult, error) {
	return &SyncResult{}, nil
}

func (testDestinationRuntime) ReadState(context.Context, ReadStateRequest) (*RemoteState, error) {
	return &RemoteState{}, nil
}

func (testDestinationRuntime) Close(context.Context) error {
	return nil
}
