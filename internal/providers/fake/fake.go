// Package fake provides a deterministic provider for tests and Phase 0.
package fake

import (
	"context"
	"strings"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

const providerType = "fake"

// Provider is a deterministic destination provider scaffold.
type Provider struct{}

type destinationRuntime struct {
	destinationName string
}

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

func (Provider) ValidateConfig(_ context.Context, cfg providers.DestinationConfig) error {
	switch {
	case strings.Contains(cfg.Name, "invalid"):
		return &providers.Error{Class: providers.ErrorClassValidation, Message: "fake destination config invalid"}
	case strings.Contains(cfg.Name, "authn"):
		return &providers.Error{Class: providers.ErrorClassAuthn, Message: "fake destination authentication failed"}
	case strings.Contains(cfg.Name, "authz"):
		return &providers.Error{Class: providers.ErrorClassAuthz, Message: "fake destination authorization failed"}
	default:
		return nil
	}
}

func (Provider) OpenDestination(
	_ context.Context,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	if err := (Provider{}).ValidateConfig(context.Background(), cfg); err != nil {
		return nil, err
	}
	return destinationRuntime{destinationName: cfg.Name}, nil
}

func (r destinationRuntime) Health(_ context.Context) (*providers.HealthResult, error) {
	if strings.Contains(r.destinationName, "unhealthy") {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "fake destination is unavailable",
			ErrorClass: providers.ErrorClassUnavailable,
		}, nil
	}
	return &providers.HealthResult{Healthy: true}, nil
}

func (destinationRuntime) Plan(_ context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	switch {
	case strings.Contains(req.ResolvedName, "blocked"):
		return &providers.PlanResult{
			Action:     providers.PlanActionBlocked,
			Message:    "fake provider blocked the requested operation",
			ErrorClass: providers.ErrorClassValidation,
		}, nil
	case strings.Contains(req.ResolvedName, "conflict"):
		return &providers.PlanResult{
			Action:     providers.PlanActionConflict,
			Message:    "fake provider detected a remote collision",
			ErrorClass: providers.ErrorClassCollision,
		}, nil
	case strings.Contains(req.ResolvedName, "update"):
		return &providers.PlanResult{Action: providers.PlanActionUpdate}, nil
	case strings.Contains(req.ResolvedName, "noop"):
		return &providers.PlanResult{Action: providers.PlanActionNoop}, nil
	default:
		return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
	}
}

func (destinationRuntime) Upsert(_ context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	if len(req.Payload) > (Provider{}).Capabilities().MaxPayloadBytes {
		return nil, &providers.Error{Class: providers.ErrorClassCapacity, Message: "fake payload too large"}
	}
	if err := fakeMutationError(req.ResolvedName); err != nil {
		return nil, err
	}
	return &providers.SyncResult{RemoteVersion: "fake"}, nil
}

func (destinationRuntime) Delete(_ context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	if strings.Contains(req.ResolvedName, "missing") {
		return &providers.SyncResult{RemoteVersion: "missing"}, nil
	}
	if err := fakeMutationError(req.ResolvedName); err != nil {
		return nil, err
	}
	return &providers.SyncResult{RemoteVersion: "deleted"}, nil
}

func (destinationRuntime) ReadState(_ context.Context, req providers.ReadStateRequest) (*providers.RemoteState, error) {
	switch {
	case strings.Contains(req.ResolvedName, "missing"):
		return &providers.RemoteState{Exists: false}, nil
	case strings.Contains(req.ResolvedName, "ownership"):
		return &providers.RemoteState{
			Exists:         true,
			OwnershipKnown: true,
			Owned:          false,
		}, nil
	case strings.Contains(req.ResolvedName, "drift-newer"):
		return &providers.RemoteState{
			Exists:         true,
			OwnershipKnown: true,
			Owned:          true,
			PayloadSHA256:  "sha256:remote",
			SourceVersion:  req.SourceVersion + 1,
			RemoteVersion:  "fake",
		}, nil
	case strings.Contains(req.ResolvedName, "drift"):
		return &providers.RemoteState{
			Exists:         true,
			OwnershipKnown: true,
			Owned:          true,
			PayloadSHA256:  "sha256:remote",
			SourceVersion:  req.SourceVersion,
			RemoteVersion:  "fake",
		}, nil
	}
	if err := fakeMutationError(req.ResolvedName); err != nil {
		return nil, err
	}
	return &providers.RemoteState{
		Exists:         true,
		OwnershipKnown: true,
		Owned:          true,
		PayloadSHA256:  req.PayloadSHA256,
		SourceVersion:  req.SourceVersion,
		RemoteVersion:  "fake",
	}, nil
}

func (destinationRuntime) Close(context.Context) error {
	return nil
}

func fakeMutationError(resolvedName string) error {
	switch {
	case strings.Contains(resolvedName, "context-canceled"):
		return context.Canceled
	case strings.Contains(resolvedName, "validation"):
		return &providers.Error{Class: providers.ErrorClassValidation, Message: "fake validation failure"}
	case strings.Contains(resolvedName, "authn"):
		return &providers.Error{Class: providers.ErrorClassAuthn, Message: "fake authentication failure"}
	case strings.Contains(resolvedName, "authz"):
		return &providers.Error{Class: providers.ErrorClassAuthz, Message: "fake authorization failure"}
	case strings.Contains(resolvedName, "rate-limit"):
		return &providers.Error{Class: providers.ErrorClassRateLimit, Message: "fake rate limit"}
	case strings.Contains(resolvedName, "unavailable"):
		return &providers.Error{Class: providers.ErrorClassUnavailable, Message: "fake unavailable"}
	case strings.Contains(resolvedName, "ownership"):
		return &providers.Error{Class: providers.ErrorClassOwnership, Message: "fake ownership failure"}
	case strings.Contains(resolvedName, "collision"):
		return &providers.Error{Class: providers.ErrorClassCollision, Message: "fake collision"}
	case strings.Contains(resolvedName, "drift"):
		return &providers.Error{Class: providers.ErrorClassDrift, Message: "fake remote drift"}
	default:
		return nil
	}
}
