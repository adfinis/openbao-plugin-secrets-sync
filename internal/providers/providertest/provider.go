// Package providertest contains reusable provider contract checks.
package providertest

import (
	"context"
	"errors"
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
)

// Harness describes provider contract checks that are expected to pass.
type Harness struct {
	Provider             providers.Provider
	ValidDestination     providers.DestinationConfig
	RequiredCapabilities CapabilityExpectations
	ValidationError      *ValidationErrorCase
	HealthCase           *HealthCase
	PlanCases            []PlanCase
	Lifecycle            *LifecycleCase
	Maturity             *MaturityMatrix
	UpsertSuccess        *UpsertCase
	DeleteSuccess        *DeleteCase
	ReadStateCase        *ReadStateCase
	UpsertErrors         []UpsertErrorCase
	DeleteErrors         []DeleteErrorCase
}

// CapabilityExpectations declares capability bits expected from a provider.
type CapabilityExpectations struct {
	ValueReadback       bool
	MetadataReadback    bool
	SecretPath          bool
	SecretKey           bool
	UpdateIfOwned       bool
	DeleteIfOwned       bool
	PayloadHashMetadata bool
	MinPayloadBytes     int
}

// ValidationErrorCase validates provider destination error classification.
type ValidationErrorCase struct {
	Destination providers.DestinationConfig
	ErrorClass  providers.ErrorClass
}

// HealthCase validates provider health diagnostics.
type HealthCase struct {
	Destination providers.DestinationConfig
	Healthy     bool
	ErrorClass  providers.ErrorClass
}

// PlanCase validates provider dry-run action mapping.
type PlanCase struct {
	Name       string
	Request    providers.PlanRequest
	Action     string
	ErrorClass providers.ErrorClass
}

// UpsertCase validates a successful upsert mutation.
type UpsertCase struct {
	Request       providers.UpsertRequest
	RemoteVersion string
}

// DeleteCase validates a successful delete mutation.
type DeleteCase struct {
	Request       providers.DeleteRequest
	RemoteVersion string
}

// ReadStateCase validates remote state lookup.
type ReadStateCase struct {
	Request        providers.ReadStateRequest
	Exists         bool
	OwnershipKnown bool
	Owned          bool
	PayloadSHA256  string
	SourceVersion  int
	RemoteVersion  string
}

// LifecycleCase validates a stateful create/update/delete provider flow.
type LifecycleCase struct {
	Name                 string
	CreatePlan           PlanCase
	Create               UpsertCase
	StateAfterCreate     ReadStateCase
	NoopPlan             PlanCase
	UpdatePlan           PlanCase
	Update               UpsertCase
	StateAfterUpdate     ReadStateCase
	Delete               DeleteCase
	StateAfterDelete     ReadStateCase
	ExpectRemoteVersions bool
}

// UpsertErrorCase validates upsert error classification.
type UpsertErrorCase struct {
	Name       string
	Request    providers.UpsertRequest
	ErrorClass providers.ErrorClass
}

// DeleteErrorCase validates delete error classification.
type DeleteErrorCase struct {
	Name       string
	Request    providers.DeleteRequest
	ErrorClass providers.ErrorClass
}

// Operation identifies a provider boundary operation exercised by maturity tests.
type Operation string

const (
	OperationPlan      Operation = "plan"
	OperationUpsert    Operation = "upsert"
	OperationDelete    Operation = "delete"
	OperationReadState Operation = "read-state"
	OperationHealth    Operation = "health"
)

// PartialSuccessMode documents whether partial remote mutation is possible for a provider.
type PartialSuccessMode string

const (
	// PartialSuccessAtomic means the provider API writes payload and metadata in one mutation.
	PartialSuccessAtomic PartialSuccessMode = "atomic"
	// PartialSuccessClassifiedFailure means a post-write failure is possible and must be classified.
	PartialSuccessClassifiedFailure PartialSuccessMode = "classified_failure"
)

// MaturityMatrix describes required production-readiness behavior for real providers.
type MaturityMatrix struct {
	OwnershipLoss    []MaturityCase
	AuthFailure      MaturityCase
	Throttling       MaturityCase
	PayloadLimit     MaturityCase
	PartialSuccess   PartialSuccessCase
	StaleRemoteState MaturityCase
	DeleteSemantics  []MaturityCase
}

// MaturityCase validates one provider maturity behavior.
type MaturityCase struct {
	Name              string
	Provider          providers.Provider
	Operation         Operation
	PlanRequest       providers.PlanRequest
	UpsertRequest     providers.UpsertRequest
	DeleteRequest     providers.DeleteRequest
	ReadStateRequest  providers.ReadStateRequest
	HealthDestination providers.DestinationConfig
	PlanAction        string
	ErrorClass        providers.ErrorClass
	RemoteVersion     string
	ReadState         *ReadStateCase
	NoResultOnError   bool
}

// PartialSuccessCase validates or documents the provider's partial-mutation behavior.
type PartialSuccessCase struct {
	Name string
	Mode PartialSuccessMode
	Case MaturityCase
}

// Run executes the configured provider conformance checks.
func Run(t *testing.T, harness Harness) {
	t.Helper()
	if harness.Provider == nil {
		t.Fatal("provider must not be nil")
	}
	runTypeCheck(t, harness.Provider)
	runCapabilityCheck(t, harness.Provider, harness.RequiredCapabilities)
	runValidationCheck(t, harness.Provider, harness.ValidDestination, harness.ValidationError)
	if harness.HealthCase != nil {
		runHealthCheck(t, harness.Provider, *harness.HealthCase)
	}
	for _, planCase := range harness.PlanCases {
		runPlanCheck(t, harness.Provider, planCase)
	}
	if harness.Lifecycle != nil {
		runLifecycleCheck(t, harness.Provider, *harness.Lifecycle)
	}
	if harness.Maturity != nil {
		runMaturityMatrix(t, harness.Provider, *harness.Maturity)
	}
	if harness.UpsertSuccess != nil {
		runUpsertSuccessCheck(t, harness.Provider, *harness.UpsertSuccess)
	}
	if harness.DeleteSuccess != nil {
		runDeleteSuccessCheck(t, harness.Provider, *harness.DeleteSuccess)
	}
	if harness.ReadStateCase != nil {
		runReadStateCheck(t, harness.Provider, *harness.ReadStateCase)
	}
	for _, errorCase := range harness.UpsertErrors {
		runUpsertErrorCheck(t, harness.Provider, errorCase)
	}
	for _, errorCase := range harness.DeleteErrors {
		runDeleteErrorCheck(t, harness.Provider, errorCase)
	}
}

func runTypeCheck(t *testing.T, provider providers.Provider) {
	t.Helper()
	t.Run("type", func(t *testing.T) {
		if got := provider.Type(); got == "" {
			t.Fatal("provider type must not be empty")
		}
	})
}

func runCapabilityCheck(
	t *testing.T,
	provider providers.Provider,
	expected CapabilityExpectations,
) {
	t.Helper()
	t.Run("capabilities", func(t *testing.T) {
		assertCapabilities(t, provider.Capabilities(), expected)
	})
}

func runValidationCheck(
	t *testing.T,
	provider providers.Provider,
	validDestination providers.DestinationConfig,
	errorCase *ValidationErrorCase,
) {
	t.Helper()
	t.Run("validate", func(t *testing.T) {
		if err := provider.Validate(context.Background(), validDestination); err != nil {
			t.Fatalf("validate valid destination: %v", err)
		}
		if errorCase == nil {
			return
		}
		err := provider.Validate(context.Background(), errorCase.Destination)
		assertProviderErrorClass(t, err, errorCase.ErrorClass)
	})
}

func runHealthCheck(t *testing.T, provider providers.Provider, healthCase HealthCase) {
	t.Helper()
	t.Run("health", func(t *testing.T) {
		health, err := provider.Health(context.Background(), healthCase.Destination)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if health == nil {
			t.Fatal("health result must not be nil")
		}
		if health.Healthy != healthCase.Healthy {
			t.Fatalf("health healthy = %v, want %v", health.Healthy, healthCase.Healthy)
		}
		if health.ErrorClass != healthCase.ErrorClass {
			t.Fatalf("health error class = %s, want %s", health.ErrorClass, healthCase.ErrorClass)
		}
	})
}

func runPlanCheck(t *testing.T, provider providers.Provider, planCase PlanCase) {
	t.Helper()
	t.Run("plan/"+planCase.Name, func(t *testing.T) {
		result, err := provider.Plan(context.Background(), planCase.Request)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if result == nil {
			t.Fatal("plan result must not be nil")
		}
		if result.Action != planCase.Action {
			t.Fatalf("plan action = %s, want %s", result.Action, planCase.Action)
		}
		if result.ErrorClass != planCase.ErrorClass {
			t.Fatalf("plan error class = %s, want %s", result.ErrorClass, planCase.ErrorClass)
		}
	})
}

func runUpsertSuccessCheck(t *testing.T, provider providers.Provider, upsertCase UpsertCase) {
	t.Helper()
	t.Run("upsert", func(t *testing.T) {
		result, err := provider.Upsert(context.Background(), upsertCase.Request)
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		assertRemoteVersion(t, result, upsertCase.RemoteVersion)
	})
}

func runDeleteSuccessCheck(t *testing.T, provider providers.Provider, deleteCase DeleteCase) {
	t.Helper()
	t.Run("delete", func(t *testing.T) {
		result, err := provider.Delete(context.Background(), deleteCase.Request)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		assertRemoteVersion(t, result, deleteCase.RemoteVersion)
	})
}

func runReadStateCheck(t *testing.T, provider providers.Provider, readStateCase ReadStateCase) {
	t.Helper()
	t.Run("read-state", func(t *testing.T) {
		state, err := provider.ReadState(context.Background(), readStateCase.Request)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		if state == nil {
			t.Fatal("remote state must not be nil")
		}
		assertRemoteState(t, state, readStateCase)
	})
}

func runLifecycleCheck(t *testing.T, provider providers.Provider, lifecycleCase LifecycleCase) {
	t.Helper()
	name := lifecycleCase.Name
	if name == "" {
		name = "default"
	}
	t.Run("lifecycle/"+name, func(t *testing.T) {
		runPlanCheck(t, provider, lifecycleCase.CreatePlan)
		runUpsertSuccessCheck(t, provider, lifecycleCase.Create)
		runReadStateCheck(t, provider, lifecycleCase.StateAfterCreate)
		runPlanCheck(t, provider, lifecycleCase.NoopPlan)
		runPlanCheck(t, provider, lifecycleCase.UpdatePlan)
		runUpsertSuccessCheck(t, provider, lifecycleCase.Update)
		runReadStateCheck(t, provider, lifecycleCase.StateAfterUpdate)
		runDeleteSuccessCheck(t, provider, lifecycleCase.Delete)
		runReadStateCheck(t, provider, lifecycleCase.StateAfterDelete)
	})
}

func runMaturityMatrix(t *testing.T, provider providers.Provider, matrix MaturityMatrix) {
	t.Helper()
	t.Run("maturity", func(t *testing.T) {
		runMaturityGroup(t, provider, "ownership-loss", matrix.OwnershipLoss)
		runRequiredMaturityCase(t, provider, "auth-failure", matrix.AuthFailure)
		runRequiredMaturityCase(t, provider, "throttling", matrix.Throttling)
		runRequiredMaturityCase(t, provider, "payload-limit", matrix.PayloadLimit)
		runPartialSuccessCase(t, provider, matrix.PartialSuccess)
		runRequiredMaturityCase(t, provider, "stale-remote-state", matrix.StaleRemoteState)
		runMaturityGroup(t, provider, "delete-semantics", matrix.DeleteSemantics)
	})
}

func runMaturityGroup(
	t *testing.T,
	provider providers.Provider,
	name string,
	cases []MaturityCase,
) {
	t.Helper()
	if len(cases) == 0 {
		t.Fatalf("maturity/%s requires at least one case", name)
	}
	for _, maturityCase := range cases {
		runRequiredMaturityCase(t, provider, name, maturityCase)
	}
}

func runRequiredMaturityCase(
	t *testing.T,
	defaultProvider providers.Provider,
	group string,
	maturityCase MaturityCase,
) {
	t.Helper()
	if maturityCase.Operation == "" {
		t.Fatalf("maturity/%s requires an operation", group)
	}
	name := maturityCase.Name
	if name == "" {
		name = string(maturityCase.Operation)
	}
	t.Run(group+"/"+name, func(t *testing.T) {
		runMaturityCase(t, defaultProvider, maturityCase)
	})
}

func runPartialSuccessCase(
	t *testing.T,
	defaultProvider providers.Provider,
	partialCase PartialSuccessCase,
) {
	t.Helper()
	if partialCase.Mode == "" {
		t.Fatal("maturity/partial-success requires a mode")
	}
	name := partialCase.Name
	if name == "" {
		name = string(partialCase.Mode)
	}
	t.Run("partial-success/"+name, func(t *testing.T) {
		switch partialCase.Mode {
		case PartialSuccessAtomic:
			if partialCase.Case.ErrorClass != "" {
				t.Fatal("atomic partial-success case must not expect an error class")
			}
			if partialCase.Case.Operation != "" {
				runMaturityCase(t, defaultProvider, partialCase.Case)
			}
		case PartialSuccessClassifiedFailure:
			if partialCase.Case.Operation == "" {
				t.Fatal("classified partial-success requires an operation case")
			}
			if partialCase.Case.ErrorClass == "" {
				t.Fatal("classified partial-success requires an expected error class")
			}
			runMaturityCase(t, defaultProvider, partialCase.Case)
		default:
			t.Fatalf("unknown partial-success mode %q", partialCase.Mode)
		}
	})
}

func runMaturityCase(
	t *testing.T,
	defaultProvider providers.Provider,
	maturityCase MaturityCase,
) {
	t.Helper()
	assertMaturityCaseWellFormed(t, maturityCase)
	provider := maturityCase.Provider
	if provider == nil {
		provider = defaultProvider
	}
	switch maturityCase.Operation {
	case OperationPlan:
		runMaturityPlanCase(t, provider, maturityCase)
	case OperationUpsert:
		runMaturityUpsertCase(t, provider, maturityCase)
	case OperationDelete:
		runMaturityDeleteCase(t, provider, maturityCase)
	case OperationReadState:
		runMaturityReadStateCase(t, provider, maturityCase)
	case OperationHealth:
		runMaturityHealthCase(t, provider, maturityCase)
	default:
		t.Fatalf("unknown maturity operation %q", maturityCase.Operation)
	}
}

func assertMaturityCaseWellFormed(t *testing.T, maturityCase MaturityCase) {
	t.Helper()
	switch maturityCase.Operation {
	case OperationPlan:
		assertPlanRequestWellFormed(t, maturityCase.PlanRequest)
		if maturityCase.PlanAction == "" {
			t.Fatal("plan maturity case requires expected plan action")
		}
	case OperationUpsert:
		assertUpsertRequestWellFormed(t, maturityCase.UpsertRequest)
		assertMutationExpectationWellFormed(t, maturityCase)
	case OperationDelete:
		assertDeleteRequestWellFormed(t, maturityCase.DeleteRequest)
		assertMutationExpectationWellFormed(t, maturityCase)
	case OperationReadState:
		assertReadStateRequestWellFormed(t, maturityCase.ReadStateRequest)
		if maturityCase.ErrorClass == "" && maturityCase.ReadState != nil {
			assertReadStateRequestWellFormed(t, maturityCase.ReadState.Request)
		}
	case OperationHealth:
		if maturityCase.HealthDestination.Name == "" {
			t.Fatal("health maturity case requires destination name")
		}
	default:
		return
	}
}

func assertPlanRequestWellFormed(t *testing.T, request providers.PlanRequest) {
	t.Helper()
	if request.ResolvedName == "" ||
		request.SourcePath == "" ||
		request.SourceVersion <= 0 ||
		request.AssociationID == "" ||
		request.ObjectID == "" {
		t.Fatalf("plan maturity request missing identity fields: %#v", request)
	}
}

func assertUpsertRequestWellFormed(t *testing.T, request providers.UpsertRequest) {
	t.Helper()
	if request.ResolvedName == "" ||
		request.SourcePath == "" ||
		request.SourceVersion <= 0 ||
		request.AssociationID == "" ||
		request.ObjectID == "" ||
		request.PayloadSHA256 == "" ||
		request.Payload == nil {
		t.Fatalf("upsert maturity request missing identity or payload fields: %#v", request)
	}
}

func assertDeleteRequestWellFormed(t *testing.T, request providers.DeleteRequest) {
	t.Helper()
	if request.ResolvedName == "" ||
		request.SourcePath == "" ||
		request.SourceVersion <= 0 ||
		request.AssociationID == "" ||
		request.ObjectID == "" {
		t.Fatalf("delete maturity request missing identity fields: %#v", request)
	}
}

func assertReadStateRequestWellFormed(t *testing.T, request providers.ReadStateRequest) {
	t.Helper()
	if request.ResolvedName == "" ||
		request.SourcePath == "" ||
		request.SourceVersion <= 0 ||
		request.AssociationID == "" ||
		request.ObjectID == "" {
		t.Fatalf("read-state maturity request missing identity fields: %#v", request)
	}
}

func assertMutationExpectationWellFormed(t *testing.T, maturityCase MaturityCase) {
	t.Helper()
	if maturityCase.ErrorClass != "" {
		if !maturityCase.NoResultOnError {
			t.Fatal("mutation error maturity case must require no result on error")
		}
		return
	}
	if maturityCase.RemoteVersion == "" {
		t.Fatal("successful mutation maturity case requires expected remote version")
	}
}

func runMaturityPlanCase(
	t *testing.T,
	provider providers.Provider,
	maturityCase MaturityCase,
) {
	t.Helper()
	result, err := provider.Plan(context.Background(), maturityCase.PlanRequest)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result == nil {
		t.Fatal("plan result must not be nil")
	}
	if result.Action != maturityCase.PlanAction {
		t.Fatalf("plan action = %s, want %s", result.Action, maturityCase.PlanAction)
	}
	if result.ErrorClass != maturityCase.ErrorClass {
		t.Fatalf("plan error class = %s, want %s", result.ErrorClass, maturityCase.ErrorClass)
	}
}

func runMaturityUpsertCase(
	t *testing.T,
	provider providers.Provider,
	maturityCase MaturityCase,
) {
	t.Helper()
	result, err := provider.Upsert(context.Background(), maturityCase.UpsertRequest)
	assertMutationOutcome(t, result, err, maturityCase)
}

func runMaturityDeleteCase(
	t *testing.T,
	provider providers.Provider,
	maturityCase MaturityCase,
) {
	t.Helper()
	result, err := provider.Delete(context.Background(), maturityCase.DeleteRequest)
	assertMutationOutcome(t, result, err, maturityCase)
}

func runMaturityReadStateCase(
	t *testing.T,
	provider providers.Provider,
	maturityCase MaturityCase,
) {
	t.Helper()
	state, err := provider.ReadState(context.Background(), maturityCase.ReadStateRequest)
	if maturityCase.ErrorClass != "" {
		assertProviderErrorClass(t, err, maturityCase.ErrorClass)
		return
	}
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if maturityCase.ReadState == nil {
		return
	}
	assertRemoteState(t, state, *maturityCase.ReadState)
}

func runMaturityHealthCase(
	t *testing.T,
	provider providers.Provider,
	maturityCase MaturityCase,
) {
	t.Helper()
	health, err := provider.Health(context.Background(), maturityCase.HealthDestination)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health == nil {
		t.Fatal("health result must not be nil")
	}
	if health.ErrorClass != maturityCase.ErrorClass {
		t.Fatalf("health error class = %s, want %s", health.ErrorClass, maturityCase.ErrorClass)
	}
}

func runUpsertErrorCheck(t *testing.T, provider providers.Provider, errorCase UpsertErrorCase) {
	t.Helper()
	t.Run("upsert-error/"+errorCase.Name, func(t *testing.T) {
		_, err := provider.Upsert(context.Background(), errorCase.Request)
		assertProviderErrorClass(t, err, errorCase.ErrorClass)
	})
}

func runDeleteErrorCheck(t *testing.T, provider providers.Provider, errorCase DeleteErrorCase) {
	t.Helper()
	t.Run("delete-error/"+errorCase.Name, func(t *testing.T) {
		_, err := provider.Delete(context.Background(), errorCase.Request)
		assertProviderErrorClass(t, err, errorCase.ErrorClass)
	})
}

func assertCapabilities(
	t *testing.T,
	capabilities providers.Capabilities,
	expected CapabilityExpectations,
) {
	t.Helper()
	capabilityChecks := []struct {
		name     string
		required bool
		actual   bool
	}{
		{name: "value readback", required: expected.ValueReadback, actual: capabilities.SupportsValueReadback},
		{name: "metadata readback", required: expected.MetadataReadback, actual: capabilities.SupportsMetadataReadback},
		{name: "secret-path granularity", required: expected.SecretPath, actual: capabilities.SupportsSecretPath},
		{name: "secret-key granularity", required: expected.SecretKey, actual: capabilities.SupportsSecretKey},
		{name: "owned updates", required: expected.UpdateIfOwned, actual: capabilities.SupportsUpdateIfOwned},
		{name: "owned deletes", required: expected.DeleteIfOwned, actual: capabilities.SupportsDeleteIfOwned},
		{
			name:     "payload hash metadata",
			required: expected.PayloadHashMetadata,
			actual:   capabilities.SupportsPayloadHashMetadata,
		},
	}
	for _, check := range capabilityChecks {
		if check.required && !check.actual {
			t.Fatalf("provider must support %s", check.name)
		}
	}
	if capabilities.MaxPayloadBytes < expected.MinPayloadBytes {
		t.Fatalf("max payload bytes = %d, want at least %d", capabilities.MaxPayloadBytes, expected.MinPayloadBytes)
	}
}

func assertProviderErrorClass(t *testing.T, err error, expected providers.ErrorClass) {
	t.Helper()
	if expected == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("error = nil, want class %s", expected)
	}
	var providerError *providers.Error
	if !errors.As(err, &providerError) {
		t.Fatalf("error = %T, want *providers.Error", err)
	}
	if providerError.Class != expected {
		t.Fatalf("error class = %s, want %s", providerError.Class, expected)
	}
}

func assertRemoteVersion(t *testing.T, result *providers.SyncResult, expected string) {
	t.Helper()
	if result == nil {
		t.Fatal("sync result must not be nil")
	}
	if result.RemoteVersion != expected {
		t.Fatalf("remote version = %q, want %q", result.RemoteVersion, expected)
	}
}

func assertMutationOutcome(
	t *testing.T,
	result *providers.SyncResult,
	err error,
	maturityCase MaturityCase,
) {
	t.Helper()
	if maturityCase.ErrorClass != "" {
		assertProviderErrorClass(t, err, maturityCase.ErrorClass)
		if maturityCase.NoResultOnError && result != nil {
			t.Fatalf("sync result = %#v, want nil on error", result)
		}
		return
	}
	if err != nil {
		t.Fatalf("%s: %v", maturityCase.Operation, err)
	}
	assertRemoteVersion(t, result, maturityCase.RemoteVersion)
}

func assertRemoteState(
	t *testing.T,
	state *providers.RemoteState,
	readStateCase ReadStateCase,
) {
	t.Helper()
	if state == nil {
		t.Fatal("remote state must not be nil")
	}
	if state.Exists != readStateCase.Exists {
		t.Fatalf("remote state exists = %v, want %v", state.Exists, readStateCase.Exists)
	}
	if state.OwnershipKnown != readStateCase.OwnershipKnown {
		t.Fatalf("remote state ownership known = %v, want %v", state.OwnershipKnown, readStateCase.OwnershipKnown)
	}
	if state.Owned != readStateCase.Owned {
		t.Fatalf("remote state owned = %v, want %v", state.Owned, readStateCase.Owned)
	}
	if state.PayloadSHA256 != readStateCase.PayloadSHA256 {
		t.Fatalf("remote state payload sha = %q, want %q", state.PayloadSHA256, readStateCase.PayloadSHA256)
	}
	if state.SourceVersion != readStateCase.SourceVersion {
		t.Fatalf("remote state source version = %d, want %d", state.SourceVersion, readStateCase.SourceVersion)
	}
	if state.RemoteVersion != readStateCase.RemoteVersion {
		t.Fatalf("remote state version = %q, want %q", state.RemoteVersion, readStateCase.RemoteVersion)
	}
}
