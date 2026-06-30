package awssecretsmanager

import (
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/providertest"
)

func TestProviderConformanceScaffold(t *testing.T) {
	providertest.Run(t, providertest.Harness{
		Provider:         Provider{},
		ValidDestination: providers.DestinationConfig{Name: "prod"},
		RequiredCapabilities: providertest.CapabilityExpectations{
			SecretPath:          true,
			UpdateIfOwned:       true,
			DeleteIfOwned:       true,
			PayloadHashMetadata: true,
			MinPayloadBytes:     secretValueMaxBytes,
		},
		ValidationError: &providertest.ValidationErrorCase{
			Destination: providers.DestinationConfig{Name: ""},
			ErrorClass:  providers.ErrorClassValidation,
		},
		HealthCase: &providertest.HealthCase{
			Destination: providers.DestinationConfig{Name: "prod"},
			Healthy:     false,
			ErrorClass:  providers.ErrorClassInternal,
		},
		PlanCases: []providertest.PlanCase{
			{
				Name:       "blocked-unimplemented",
				Request:    providers.PlanRequest{ResolvedName: "prod/app/db"},
				Action:     providers.PlanActionBlocked,
				ErrorClass: providers.ErrorClassInternal,
			},
		},
	})
}
