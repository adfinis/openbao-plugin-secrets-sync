// Package fake provides a deterministic provider for tests and Phase 0.
package fake

import (
	"context"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
)

const providerType = "fake"

// Provider is a deterministic in-memory destination provider scaffold.
type Provider struct{}

func (Provider) Type() string {
	return providerType
}

func (Provider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsValueReadback:       true,
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		SupportsSecretKey:           true,
		MaxPayloadBytes:             1024 * 1024,
	}
}

func (Provider) Validate(context.Context, providers.DestinationConfig) error {
	return nil
}

func (Provider) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{Action: "noop"}, nil
}

func (Provider) Upsert(context.Context, providers.UpsertRequest) (*providers.SyncResult, error) {
	return &providers.SyncResult{RemoteVersion: "fake"}, nil
}

func (Provider) Delete(context.Context, providers.DeleteRequest) (*providers.SyncResult, error) {
	return &providers.SyncResult{RemoteVersion: "deleted"}, nil
}

func (Provider) ReadState(context.Context, providers.ReadStateRequest) (*providers.RemoteState, error) {
	return &providers.RemoteState{Exists: false}, nil
}

func (Provider) Health(context.Context, providers.DestinationConfig) (*providers.HealthResult, error) {
	return &providers.HealthResult{Healthy: true}, nil
}
