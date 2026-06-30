// Package awssecretsmanager provides the AWS Secrets Manager destination provider.
package awssecretsmanager

import (
	"context"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
)

const (
	// ProviderType is the stable destination type used by associations.
	ProviderType = "aws-sm"

	// AWS Secrets Manager caps the encrypted secret value at 65,536 bytes.
	secretValueMaxBytes = 65536
)

// Provider is the AWS Secrets Manager provider scaffold.
type Provider struct{}

func (Provider) Type() string {
	return ProviderType
}

func (Provider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsValueReadback:       false,
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		SupportsSecretKey:           false,
		MaxPayloadBytes:             secretValueMaxBytes,
	}
}

func (Provider) Validate(_ context.Context, cfg providers.DestinationConfig) error {
	if strings.TrimSpace(cfg.Name) == "" {
		return &providers.Error{Class: providers.ErrorClassValidation, Message: "aws-sm destination name must not be empty"}
	}
	return nil
}

func (Provider) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{
		Action:     providers.PlanActionBlocked,
		Message:    "aws-sm provider client is not implemented",
		ErrorClass: providers.ErrorClassInternal,
	}, nil
}

func (Provider) Upsert(context.Context, providers.UpsertRequest) (*providers.SyncResult, error) {
	return nil, notImplementedError()
}

func (Provider) Delete(context.Context, providers.DeleteRequest) (*providers.SyncResult, error) {
	return nil, notImplementedError()
}

func (Provider) ReadState(context.Context, providers.ReadStateRequest) (*providers.RemoteState, error) {
	return nil, notImplementedError()
}

func (Provider) Health(context.Context, providers.DestinationConfig) (*providers.HealthResult, error) {
	return &providers.HealthResult{
		Healthy:    false,
		Message:    "aws-sm provider client is not implemented",
		ErrorClass: providers.ErrorClassInternal,
	}, nil
}

func notImplementedError() error {
	return &providers.Error{Class: providers.ErrorClassInternal, Message: "aws-sm provider client is not implemented"}
}
