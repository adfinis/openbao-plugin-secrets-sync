package fake

import (
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/providertest"
)

func TestProviderConformance(t *testing.T) {
	providertest.Run(t, providertest.Harness{
		Provider:         Provider{},
		ValidDestination: providers.DestinationConfig{Name: "default"},
		RequiredCapabilities: providertest.CapabilityExpectations{
			SecretPath:          true,
			SecretKey:           true,
			UpdateIfOwned:       true,
			DeleteIfOwned:       true,
			PayloadHashMetadata: true,
			MinPayloadBytes:     1024 * 1024,
		},
		ValidationError: &providertest.ValidationErrorCase{
			Destination: providers.DestinationConfig{Name: "invalid"},
			ErrorClass:  providers.ErrorClassValidation,
		},
		HealthCase: &providertest.HealthCase{
			Destination: providers.DestinationConfig{Name: "unhealthy"},
			Healthy:     false,
			ErrorClass:  providers.ErrorClassUnavailable,
		},
		PlanCases: []providertest.PlanCase{
			{
				Name:    "create",
				Request: providers.PlanRequest{ResolvedName: "prod/app/db"},
				Action:  providers.PlanActionCreate,
			},
			{
				Name:    "update",
				Request: providers.PlanRequest{ResolvedName: "prod/update/app/db"},
				Action:  providers.PlanActionUpdate,
			},
			{
				Name:    "noop",
				Request: providers.PlanRequest{ResolvedName: "prod/noop/app/db"},
				Action:  providers.PlanActionNoop,
			},
			{
				Name:       "conflict",
				Request:    providers.PlanRequest{ResolvedName: "prod/conflict/app/db"},
				Action:     providers.PlanActionConflict,
				ErrorClass: providers.ErrorClassCollision,
			},
			{
				Name:       "blocked",
				Request:    providers.PlanRequest{ResolvedName: "prod/blocked/app/db"},
				Action:     providers.PlanActionBlocked,
				ErrorClass: providers.ErrorClassValidation,
			},
		},
		UpsertSuccess: &providertest.UpsertCase{
			Request: providers.UpsertRequest{
				Destination:   providers.DestinationConfig{Name: "default"},
				ResolvedName:  "prod/app/db",
				Format:        "json",
				Payload:       []byte(`{"password":"secret"}`),
				PayloadSHA256: "sha256:test",
			},
			RemoteVersion: "fake",
		},
		DeleteSuccess: &providertest.DeleteCase{
			Request: providers.DeleteRequest{
				Destination:   providers.DestinationConfig{Name: "default"},
				ResolvedName:  "prod/app/db",
				SourcePath:    "app/db",
				SourceVersion: 1,
			},
			RemoteVersion: "deleted",
		},
		ReadStateCase: &providertest.ReadStateCase{
			Request: providers.ReadStateRequest{ResolvedName: "prod/app/db"},
			Exists:  false,
		},
		UpsertErrors: []providertest.UpsertErrorCase{
			{
				Name:       "rate-limit",
				Request:    providers.UpsertRequest{ResolvedName: "prod/rate-limit/app/db"},
				ErrorClass: providers.ErrorClassRateLimit,
			},
			{
				Name:       "unavailable",
				Request:    providers.UpsertRequest{ResolvedName: "prod/unavailable/app/db"},
				ErrorClass: providers.ErrorClassUnavailable,
			},
			{
				Name:       "ownership",
				Request:    providers.UpsertRequest{ResolvedName: "prod/ownership/app/db"},
				ErrorClass: providers.ErrorClassOwnership,
			},
		},
		DeleteErrors: []providertest.DeleteErrorCase{
			{
				Name:       "collision",
				Request:    providers.DeleteRequest{ResolvedName: "prod/collision/app/db"},
				ErrorClass: providers.ErrorClassCollision,
			},
		},
	})
}
