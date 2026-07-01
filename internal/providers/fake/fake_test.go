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
			ValueReadback:       true,
			MetadataReadback:    true,
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
			Request: providers.ReadStateRequest{
				ResolvedName:  "prod/app/db",
				PayloadSHA256: "sha256:test",
				SourcePath:    "app/db",
				SourceVersion: 1,
				AssociationID: "assoc-1",
				ObjectID:      "secret-path",
			},
			Exists:         true,
			OwnershipKnown: true,
			Owned:          true,
			PayloadSHA256:  "sha256:test",
			SourceVersion:  1,
			RemoteVersion:  "fake",
		},
		Maturity: fakeMaturityMatrix(),
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

func fakeMaturityMatrix() *providertest.MaturityMatrix {
	secretPayload := []byte(`{"password":"secret"}`)
	oversizedPayload := make([]byte, Provider{}.Capabilities().MaxPayloadBytes+1)

	return &providertest.MaturityMatrix{
		OwnershipLoss: []providertest.MaturityCase{
			{
				Name:            "upsert-unowned",
				Operation:       providertest.OperationUpsert,
				UpsertRequest:   defaultFakeUpsertRequest("prod/ownership/app/db", secretPayload),
				ErrorClass:      providers.ErrorClassOwnership,
				NoResultOnError: true,
			},
			{
				Name:             "read-state-unowned",
				Operation:        providertest.OperationReadState,
				ReadStateRequest: defaultFakeReadStateRequest("prod/ownership/app/db"),
				ReadState: &providertest.ReadStateCase{
					Request:        defaultFakeReadStateRequest("prod/ownership/app/db"),
					Exists:         true,
					OwnershipKnown: true,
					Owned:          false,
				},
			},
		},
		AuthFailure: providertest.MaturityCase{
			Name:            "upsert-authn",
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultFakeUpsertRequest("prod/authn/app/db", secretPayload),
			ErrorClass:      providers.ErrorClassAuthn,
			NoResultOnError: true,
		},
		Throttling: providertest.MaturityCase{
			Name:            "upsert-rate-limit",
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultFakeUpsertRequest("prod/rate-limit/app/db", secretPayload),
			ErrorClass:      providers.ErrorClassRateLimit,
			NoResultOnError: true,
		},
		PayloadLimit: providertest.MaturityCase{
			Name:            "oversized-payload",
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultFakeUpsertRequest("prod/app/db", oversizedPayload),
			ErrorClass:      providers.ErrorClassCapacity,
			NoResultOnError: true,
		},
		PartialSuccess: providertest.PartialSuccessCase{
			Name: "single-fake-mutation",
			Mode: providertest.PartialSuccessAtomic,
			Case: providertest.MaturityCase{
				Operation:     providertest.OperationUpsert,
				UpsertRequest: defaultFakeUpsertRequest("prod/app/db", secretPayload),
				RemoteVersion: "fake",
			},
		},
		StaleRemoteState: providertest.MaturityCase{
			Name:            "upsert-drift",
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultFakeUpsertRequest("prod/drift-newer/app/db", secretPayload),
			ErrorClass:      providers.ErrorClassDrift,
			NoResultOnError: true,
		},
		DeleteSemantics: []providertest.MaturityCase{
			{
				Name:          "missing-delete-is-idempotent",
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultFakeDeleteRequest("prod/missing/app/db"),
				RemoteVersion: "missing",
			},
			{
				Name:          "owned-delete",
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultFakeDeleteRequest("prod/app/db"),
				RemoteVersion: "deleted",
			},
		},
	}
}

func defaultFakeUpsertRequest(resolvedName string, data []byte) providers.UpsertRequest {
	return providers.UpsertRequest{
		Destination:   providers.DestinationConfig{Name: "default"},
		Runtime:       defaultFakeRuntimeIdentity(),
		ResolvedName:  resolvedName,
		Format:        "json",
		Payload:       data,
		PayloadSHA256: "sha256:test",
		SourcePath:    "app/db",
		SourceVersion: 1,
		AssociationID: "assoc-1",
		ObjectID:      "secret-path",
	}
}

func defaultFakeDeleteRequest(resolvedName string) providers.DeleteRequest {
	return providers.DeleteRequest{
		Destination:   providers.DestinationConfig{Name: "default"},
		Runtime:       defaultFakeRuntimeIdentity(),
		ResolvedName:  resolvedName,
		SourcePath:    "app/db",
		SourceVersion: 1,
		AssociationID: "assoc-1",
		ObjectID:      "secret-path",
	}
}

func defaultFakeReadStateRequest(resolvedName string) providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Destination:   providers.DestinationConfig{Name: "default"},
		Runtime:       defaultFakeRuntimeIdentity(),
		ResolvedName:  resolvedName,
		PayloadSHA256: "sha256:test",
		SourcePath:    "app/db",
		SourceVersion: 1,
		AssociationID: "assoc-1",
		ObjectID:      "secret-path",
	}
}

func defaultFakeRuntimeIdentity() providers.RuntimeIdentity {
	return providers.RuntimeIdentity{
		PluginInstanceID: "inst-test",
		RestoreEpoch:     "epoch-test",
	}
}
