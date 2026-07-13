package providertest

import (
	"context"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

func TestRunExercisesHarnessContracts(t *testing.T) {
	Run(t, Harness{
		Provider:         contractProvider{},
		ValidDestination: providers.DestinationConfig{Name: "valid"},
		RequiredCapabilities: CapabilityExpectations{
			ValueReadback:       true,
			MetadataReadback:    true,
			SecretPath:          true,
			SecretKey:           true,
			UpdateIfOwned:       true,
			DeleteIfOwned:       true,
			PayloadHashMetadata: true,
			MinPayloadBytes:     10,
		},
		ValidationError: &ValidationErrorCase{
			Destination: providers.DestinationConfig{Name: "invalid"},
			ErrorClass:  providers.ErrorClassValidation,
		},
		HealthCase: &HealthCase{
			Destination: providers.DestinationConfig{Name: "unhealthy"},
			Healthy:     false,
			ErrorClass:  providers.ErrorClassUnavailable,
		},
		PlanCases: []PlanCase{
			{Name: "create", Request: contractPlanRequest("create"), Action: providers.PlanActionCreate},
			{Name: "update", Request: contractPlanRequest("update"), Action: providers.PlanActionUpdate},
			{Name: "noop", Request: contractPlanRequest("noop"), Action: providers.PlanActionNoop},
			{
				Name:       "conflict",
				Request:    contractPlanRequest("conflict"),
				Action:     providers.PlanActionConflict,
				ErrorClass: providers.ErrorClassCollision,
			},
			{
				Name:       "blocked",
				Request:    contractPlanRequest("blocked"),
				Action:     providers.PlanActionBlocked,
				ErrorClass: providers.ErrorClassValidation,
			},
		},
		Lifecycle: &LifecycleCase{
			Name: "default",
			CreatePlan: PlanCase{
				Name:    "create",
				Request: contractPlanRequest("create"),
				Action:  providers.PlanActionCreate,
			},
			Create: UpsertCase{
				Request:       contractUpsertRequest("lifecycle", 1),
				RemoteVersion: "upserted",
			},
			StateAfterCreate: ReadStateCase{
				Request:        contractReadStateRequest("lifecycle", 1),
				Exists:         true,
				OwnershipKnown: true,
				Owned:          true,
				PayloadSHA256:  "sha256:test",
				SourceVersion:  1,
				RemoteVersion:  "state",
			},
			NoopPlan: PlanCase{
				Name:    "noop",
				Request: contractPlanRequest("noop"),
				Action:  providers.PlanActionNoop,
			},
			UpdatePlan: PlanCase{
				Name:    "update",
				Request: contractPlanRequest("update"),
				Action:  providers.PlanActionUpdate,
			},
			Update: UpsertCase{
				Request:       contractUpsertRequest("lifecycle", 2),
				RemoteVersion: "upserted",
			},
			StateAfterUpdate: ReadStateCase{
				Request:        contractReadStateRequest("lifecycle", 2),
				Exists:         true,
				OwnershipKnown: true,
				Owned:          true,
				PayloadSHA256:  "sha256:test",
				SourceVersion:  2,
				RemoteVersion:  "state",
			},
			Delete: DeleteCase{
				Request:       contractDeleteRequest("lifecycle", 2),
				RemoteVersion: "deleted",
			},
			StateAfterDelete: ReadStateCase{
				Request: contractReadStateRequest("missing", 2),
			},
		},
		Maturity: &MaturityMatrix{
			OwnershipLoss: []MaturityCase{
				{
					Name:            "upsert-unowned",
					Operation:       OperationUpsert,
					UpsertRequest:   contractUpsertRequest("ownership", 1),
					ErrorClass:      providers.ErrorClassOwnership,
					NoResultOnError: true,
				},
				{
					Name:             "read-state-unowned",
					Operation:        OperationReadState,
					ReadStateRequest: contractReadStateRequest("unowned", 1),
					ReadState: &ReadStateCase{
						Request:        contractReadStateRequest("unowned", 1),
						Exists:         true,
						OwnershipKnown: true,
						Owned:          false,
					},
				},
			},
			AuthFailure: MaturityCase{
				Name:            "authn",
				Operation:       OperationUpsert,
				UpsertRequest:   contractUpsertRequest("authn", 1),
				ErrorClass:      providers.ErrorClassAuthn,
				NoResultOnError: true,
			},
			Throttling: MaturityCase{
				Name:            "rate-limit",
				Operation:       OperationUpsert,
				UpsertRequest:   contractUpsertRequest("rate-limit", 1),
				ErrorClass:      providers.ErrorClassRateLimit,
				NoResultOnError: true,
			},
			PayloadLimit: MaturityCase{
				Name:            "capacity",
				Operation:       OperationUpsert,
				UpsertRequest:   contractUpsertWithPayload("capacity", 1, []byte("01234567890")),
				ErrorClass:      providers.ErrorClassCapacity,
				NoResultOnError: true,
			},
			PartialSuccess: PartialSuccessCase{
				Name: "atomic",
				Mode: PartialSuccessAtomic,
				Case: MaturityCase{
					Operation:     OperationUpsert,
					UpsertRequest: contractUpsertRequest("partial", 1),
					RemoteVersion: "upserted",
				},
			},
			StaleRemoteState: MaturityCase{
				Name:            "drift",
				Operation:       OperationUpsert,
				UpsertRequest:   contractUpsertRequest("drift", 1),
				ErrorClass:      providers.ErrorClassDrift,
				NoResultOnError: true,
			},
			DeleteSemantics: []MaturityCase{
				{
					Name:          "missing",
					Operation:     OperationDelete,
					DeleteRequest: contractDeleteRequest("missing", 1),
					RemoteVersion: "missing",
				},
				{
					Name:          "owned",
					Operation:     OperationDelete,
					DeleteRequest: contractDeleteRequest("owned", 1),
					RemoteVersion: "deleted",
				},
			},
		},
		Idempotency: &IdempotencyCase{
			Name:                 "same-request",
			UpsertRequest:        contractUpsertRequest("idempotent", 1),
			DeleteRequest:        contractDeleteRequest("idempotent", 1),
			ExpectMutationResult: true,
		},
		UpsertSuccess: &UpsertCase{
			Request:       contractUpsertRequest("success", 1),
			RemoteVersion: "upserted",
		},
		DeleteSuccess: &DeleteCase{
			Request:       contractDeleteRequest("success", 1),
			RemoteVersion: "deleted",
		},
		ReadStateCase: &ReadStateCase{
			Request:        contractReadStateRequest("success", 1),
			Exists:         true,
			OwnershipKnown: true,
			Owned:          true,
			PayloadSHA256:  "sha256:test",
			SourceVersion:  1,
			RemoteVersion:  "state",
		},
		UpsertErrors: []UpsertErrorCase{
			{
				Name:       "validation",
				Request:    contractUpsertRequest("validation", 1),
				ErrorClass: providers.ErrorClassValidation,
			},
		},
		DeleteErrors: []DeleteErrorCase{
			{
				Name:       "authz",
				Request:    contractDeleteRequest("authz", 1),
				ErrorClass: providers.ErrorClassAuthz,
			},
		},
	})
}

type contractProvider struct{}

type contractRuntime struct {
	destinationName string
}

func (contractProvider) Type() string {
	return "contract"
}

func (contractProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsValueReadback:       true,
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		SupportsSecretKey:           true,
		MaxPayloadBytes:             10,
	}
}

func (contractProvider) ValidateConfig(_ context.Context, cfg providers.DestinationConfig) error {
	if cfg.Name == "invalid" {
		return &providers.Error{Class: providers.ErrorClassValidation, Message: "invalid destination"}
	}
	return nil
}

func (contractProvider) NormalizeAssociationConfig(
	_ context.Context,
	_ providers.DestinationConfig,
	cfg providers.AssociationConfig,
) (providers.AssociationConfig, error) {
	return providers.AssociationConfig{Config: cfg.Config, Identity: cfg.Identity}, nil
}

func (contractProvider) OpenDestination(
	_ context.Context,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	if err := (contractProvider{}).ValidateConfig(context.Background(), cfg); err != nil {
		return nil, err
	}
	return contractRuntime{destinationName: cfg.Name}, nil
}

func (r contractRuntime) Health(_ context.Context) (*providers.HealthResult, error) {
	if r.destinationName == "unhealthy" {
		return &providers.HealthResult{
			Healthy:    false,
			ErrorClass: providers.ErrorClassUnavailable,
		}, nil
	}
	return &providers.HealthResult{Healthy: true}, nil
}

func (contractRuntime) Plan(_ context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	switch {
	case strings.Contains(req.ResolvedName, "update"):
		return &providers.PlanResult{Action: providers.PlanActionUpdate}, nil
	case strings.Contains(req.ResolvedName, "noop"):
		return &providers.PlanResult{Action: providers.PlanActionNoop}, nil
	case strings.Contains(req.ResolvedName, "conflict"):
		return &providers.PlanResult{Action: providers.PlanActionConflict, ErrorClass: providers.ErrorClassCollision}, nil
	case strings.Contains(req.ResolvedName, "blocked"):
		return &providers.PlanResult{Action: providers.PlanActionBlocked, ErrorClass: providers.ErrorClassValidation}, nil
	default:
		return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
	}
}

func (contractRuntime) Upsert(_ context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	if len(req.Payload) > (contractProvider{}).Capabilities().MaxPayloadBytes {
		return nil, &providers.Error{Class: providers.ErrorClassCapacity, Message: "payload too large"}
	}
	if err := contractMutationError(req.ResolvedName); err != nil {
		return nil, err
	}
	return &providers.SyncResult{RemoteVersion: "upserted"}, nil
}

func (contractRuntime) Delete(_ context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	if strings.Contains(req.ResolvedName, "missing") {
		return &providers.SyncResult{RemoteVersion: "missing"}, nil
	}
	if err := contractMutationError(req.ResolvedName); err != nil {
		return nil, err
	}
	return &providers.SyncResult{RemoteVersion: "deleted"}, nil
}

func (contractRuntime) ReadState(_ context.Context, req providers.ReadStateRequest) (*providers.RemoteState, error) {
	if strings.Contains(req.ResolvedName, "missing") {
		return &providers.RemoteState{Exists: false}, nil
	}
	if strings.Contains(req.ResolvedName, "unowned") {
		return &providers.RemoteState{Exists: true, OwnershipKnown: true, Owned: false}, nil
	}
	return &providers.RemoteState{
		Exists:         true,
		OwnershipKnown: true,
		Owned:          true,
		PayloadSHA256:  req.PayloadSHA256,
		SourceVersion:  req.SourceVersion,
		RemoteVersion:  "state",
	}, nil
}

func (contractRuntime) Close(context.Context) error {
	return nil
}

func contractMutationError(resolvedName string) error {
	switch {
	case strings.Contains(resolvedName, "authn"):
		return &providers.Error{Class: providers.ErrorClassAuthn, Message: "authn"}
	case strings.Contains(resolvedName, "authz"):
		return &providers.Error{Class: providers.ErrorClassAuthz, Message: "authz"}
	case strings.Contains(resolvedName, "rate-limit"):
		return &providers.Error{Class: providers.ErrorClassRateLimit, Message: "rate limited"}
	case strings.Contains(resolvedName, "ownership"):
		return &providers.Error{Class: providers.ErrorClassOwnership, Message: "ownership"}
	case strings.Contains(resolvedName, "drift"):
		return &providers.Error{Class: providers.ErrorClassDrift, Message: "drift"}
	case strings.Contains(resolvedName, "validation"):
		return &providers.Error{Class: providers.ErrorClassValidation, Message: "validation"}
	default:
		return nil
	}
}

func contractPlanRequest(suffix string) providers.PlanRequest {
	return providers.PlanRequest{
		Runtime:       contractRuntimeIdentity(),
		ResolvedName:  "prod/" + suffix,
		Format:        "json",
		PayloadSHA256: "sha256:test",
		PayloadBytes:  10,
		SourcePath:    "app/db",
		SourceVersion: 1,
		AssociationID: "assoc-1",
		ObjectID:      "secret-path",
	}
}

func contractUpsertRequest(suffix string, sourceVersion int) providers.UpsertRequest {
	return contractUpsertWithPayload(suffix, sourceVersion, []byte("secret"))
}

func contractUpsertWithPayload(suffix string, sourceVersion int, payload []byte) providers.UpsertRequest {
	return providers.UpsertRequest{
		Runtime:       contractRuntimeIdentity(),
		ResolvedName:  "prod/" + suffix,
		Format:        "json",
		Payload:       payload,
		PayloadSHA256: "sha256:test",
		SourcePath:    "app/db",
		SourceVersion: sourceVersion,
		AssociationID: "assoc-1",
		ObjectID:      "secret-path",
	}
}

func contractDeleteRequest(suffix string, sourceVersion int) providers.DeleteRequest {
	return providers.DeleteRequest{
		Runtime:       contractRuntimeIdentity(),
		ResolvedName:  "prod/" + suffix,
		SourcePath:    "app/db",
		SourceVersion: sourceVersion,
		AssociationID: "assoc-1",
		ObjectID:      "secret-path",
	}
}

func contractReadStateRequest(suffix string, sourceVersion int) providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Runtime:       contractRuntimeIdentity(),
		ResolvedName:  "prod/" + suffix,
		PayloadSHA256: "sha256:test",
		SourcePath:    "app/db",
		SourceVersion: sourceVersion,
		AssociationID: "assoc-1",
		ObjectID:      "secret-path",
	}
}

func contractRuntimeIdentity() providers.RuntimeIdentity {
	return providers.RuntimeIdentity{
		PluginInstanceID: "inst-test",
		RestoreEpoch:     "epoch-test",
	}
}
