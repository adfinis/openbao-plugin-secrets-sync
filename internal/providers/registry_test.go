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

func (testProvider) Validate(context.Context, DestinationConfig) error {
	return nil
}

func (testProvider) Plan(context.Context, PlanRequest) (*PlanResult, error) {
	return nil, nil
}

func (testProvider) Upsert(context.Context, UpsertRequest) (*SyncResult, error) {
	return nil, nil
}

func (testProvider) Delete(context.Context, DeleteRequest) (*SyncResult, error) {
	return nil, nil
}

func (testProvider) ReadState(context.Context, ReadStateRequest) (*RemoteState, error) {
	return nil, nil
}

func (testProvider) Health(context.Context, DestinationConfig) (*HealthResult, error) {
	return nil, nil
}
