// Package providertest contains reusable provider contract checks.
package providertest

import (
	"context"
	"errors"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
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
	Idempotency          *IdempotencyCase
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
	DataMap             bool
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
	Name        string
	Destination providers.DestinationConfig
	Request     providers.PlanRequest
	Action      string
	ErrorClass  providers.ErrorClass
}

// UpsertCase validates a successful upsert mutation.
type UpsertCase struct {
	Destination   providers.DestinationConfig
	Request       providers.UpsertRequest
	RemoteVersion string
}

// DeleteCase validates a successful delete mutation.
type DeleteCase struct {
	Destination   providers.DestinationConfig
	Request       providers.DeleteRequest
	RemoteVersion string
}

// ReadStateCase validates remote state lookup.
type ReadStateCase struct {
	Destination    providers.DestinationConfig
	Request        providers.ReadStateRequest
	Exists         bool
	OwnershipKnown bool
	Owned          bool
	PayloadSHA256  string
	SourceVersion  int
	RemoteVersion  string
}

// IdempotencyCase validates repeated same-request mutations.
type IdempotencyCase struct {
	Name                 string
	Provider             providers.Provider
	Destination          providers.DestinationConfig
	UpsertRequest        providers.UpsertRequest
	StateAfterUpsert     *ReadStateCase
	DeleteRequest        providers.DeleteRequest
	StateAfterDelete     *ReadStateCase
	ExpectMutationResult bool
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
	Destination       providers.DestinationConfig
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
		runPlanCheck(t, harness.Provider, harness.ValidDestination, planCase)
	}
	if harness.Lifecycle != nil {
		runLifecycleCheck(t, harness.Provider, harness.ValidDestination, *harness.Lifecycle)
	}
	if harness.Maturity != nil {
		runMaturityMatrix(t, harness.Provider, harness.ValidDestination, *harness.Maturity)
	}
	if harness.Idempotency != nil {
		runIdempotencyCheck(t, harness.Provider, harness.ValidDestination, *harness.Idempotency)
	}
	if harness.UpsertSuccess != nil {
		runUpsertSuccessCheck(t, harness.Provider, harness.ValidDestination, *harness.UpsertSuccess)
	}
	if harness.DeleteSuccess != nil {
		runDeleteSuccessCheck(t, harness.Provider, harness.ValidDestination, *harness.DeleteSuccess)
	}
	if harness.ReadStateCase != nil {
		runReadStateCheck(t, harness.Provider, harness.ValidDestination, *harness.ReadStateCase)
	}
	for _, errorCase := range harness.UpsertErrors {
		runUpsertErrorCheck(t, harness.Provider, harness.ValidDestination, errorCase)
	}
	for _, errorCase := range harness.DeleteErrors {
		runDeleteErrorCheck(t, harness.Provider, harness.ValidDestination, errorCase)
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
		if err := provider.ValidateConfig(context.Background(), validDestination); err != nil {
			t.Fatalf("validate valid destination: %v", err)
		}
		if errorCase == nil {
			return
		}
		err := provider.ValidateConfig(context.Background(), errorCase.Destination)
		assertProviderErrorClass(t, err, errorCase.ErrorClass)
	})
}

func runHealthCheck(t *testing.T, provider providers.Provider, healthCase HealthCase) {
	t.Helper()
	t.Run("health", func(t *testing.T) {
		runtime := openRuntime(t, provider, healthCase.Destination)
		health, err := runtime.Health(context.Background())
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

func runPlanCheck(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	planCase PlanCase,
) {
	t.Helper()
	t.Run("plan/"+planCase.Name, func(t *testing.T) {
		runtime := openRuntime(t, provider, destinationOrDefault(planCase.Destination, defaultDestination))
		result, err := runtime.Plan(context.Background(), planCase.Request)
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

func runUpsertSuccessCheck(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	upsertCase UpsertCase,
) {
	t.Helper()
	t.Run("upsert", func(t *testing.T) {
		runtime := openRuntime(t, provider, destinationOrDefault(upsertCase.Destination, defaultDestination))
		result, err := runtime.Upsert(context.Background(), upsertCase.Request)
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		assertRemoteVersion(t, result, upsertCase.RemoteVersion)
	})
}

func runDeleteSuccessCheck(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	deleteCase DeleteCase,
) {
	t.Helper()
	t.Run("delete", func(t *testing.T) {
		runtime := openRuntime(t, provider, destinationOrDefault(deleteCase.Destination, defaultDestination))
		result, err := runtime.Delete(context.Background(), deleteCase.Request)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		assertRemoteVersion(t, result, deleteCase.RemoteVersion)
	})
}

func runReadStateCheck(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	readStateCase ReadStateCase,
) {
	t.Helper()
	t.Run("read-state", func(t *testing.T) {
		runtime := openRuntime(t, provider, destinationOrDefault(readStateCase.Destination, defaultDestination))
		state, err := runtime.ReadState(context.Background(), readStateCase.Request)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		if state == nil {
			t.Fatal("remote state must not be nil")
		}
		assertRemoteState(t, state, readStateCase)
	})
}

func runIdempotencyCheck(
	t *testing.T,
	defaultProvider providers.Provider,
	defaultDestination providers.DestinationConfig,
	idempotencyCase IdempotencyCase,
) {
	t.Helper()
	name := idempotencyCase.Name
	if name == "" {
		name = "default"
	}
	t.Run("idempotency/"+name, func(t *testing.T) {
		assertUpsertRequestWellFormed(t, idempotencyCase.UpsertRequest)
		if idempotencyCase.DeleteRequest.ResolvedName != "" {
			assertDeleteRequestWellFormed(t, idempotencyCase.DeleteRequest)
		}
		if idempotencyCase.StateAfterUpsert != nil {
			assertReadStateRequestWellFormed(t, idempotencyCase.StateAfterUpsert.Request)
		}
		if idempotencyCase.StateAfterDelete != nil {
			assertReadStateRequestWellFormed(t, idempotencyCase.StateAfterDelete.Request)
		}

		provider := idempotencyCase.Provider
		if provider == nil {
			provider = defaultProvider
		}
		destination := destinationOrDefault(idempotencyCase.Destination, defaultDestination)
		runtime := openRuntime(t, provider, destination)

		assertIdempotentUpsert(t, runtime, idempotencyCase.UpsertRequest, idempotencyCase.ExpectMutationResult)
		if idempotencyCase.StateAfterUpsert != nil {
			state, err := runtime.ReadState(context.Background(), idempotencyCase.StateAfterUpsert.Request)
			if err != nil {
				t.Fatalf("read state after repeated upsert: %v", err)
			}
			assertRemoteState(t, state, *idempotencyCase.StateAfterUpsert)
		}
		if idempotencyCase.DeleteRequest.ResolvedName == "" {
			return
		}
		assertIdempotentDelete(t, runtime, idempotencyCase.DeleteRequest, idempotencyCase.ExpectMutationResult)
		if idempotencyCase.StateAfterDelete != nil {
			state, err := runtime.ReadState(context.Background(), idempotencyCase.StateAfterDelete.Request)
			if err != nil {
				t.Fatalf("read state after repeated delete: %v", err)
			}
			assertRemoteState(t, state, *idempotencyCase.StateAfterDelete)
		}
	})
}

func runLifecycleCheck(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	lifecycleCase LifecycleCase,
) {
	t.Helper()
	name := lifecycleCase.Name
	if name == "" {
		name = "default"
	}
	t.Run("lifecycle/"+name, func(t *testing.T) {
		runPlanCheck(t, provider, defaultDestination, lifecycleCase.CreatePlan)
		runUpsertSuccessCheck(t, provider, defaultDestination, lifecycleCase.Create)
		runReadStateCheck(t, provider, defaultDestination, lifecycleCase.StateAfterCreate)
		runPlanCheck(t, provider, defaultDestination, lifecycleCase.NoopPlan)
		runPlanCheck(t, provider, defaultDestination, lifecycleCase.UpdatePlan)
		runUpsertSuccessCheck(t, provider, defaultDestination, lifecycleCase.Update)
		runReadStateCheck(t, provider, defaultDestination, lifecycleCase.StateAfterUpdate)
		runDeleteSuccessCheck(t, provider, defaultDestination, lifecycleCase.Delete)
		runReadStateCheck(t, provider, defaultDestination, lifecycleCase.StateAfterDelete)
	})
}

func runMaturityMatrix(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	matrix MaturityMatrix,
) {
	t.Helper()
	t.Run("maturity", func(t *testing.T) {
		runMaturityGroup(t, provider, defaultDestination, "ownership-loss", matrix.OwnershipLoss)
		runRequiredMaturityCase(t, provider, defaultDestination, "auth-failure", matrix.AuthFailure)
		runRequiredMaturityCase(t, provider, defaultDestination, "throttling", matrix.Throttling)
		runRequiredMaturityCase(t, provider, defaultDestination, "payload-limit", matrix.PayloadLimit)
		runPartialSuccessCase(t, provider, defaultDestination, matrix.PartialSuccess)
		runRequiredMaturityCase(t, provider, defaultDestination, "stale-remote-state", matrix.StaleRemoteState)
		runMaturityGroup(t, provider, defaultDestination, "delete-semantics", matrix.DeleteSemantics)
	})
}

func runMaturityGroup(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	name string,
	cases []MaturityCase,
) {
	t.Helper()
	if len(cases) == 0 {
		t.Fatalf("maturity/%s requires at least one case", name)
	}
	for _, maturityCase := range cases {
		runRequiredMaturityCase(t, provider, defaultDestination, name, maturityCase)
	}
}

func runRequiredMaturityCase(
	t *testing.T,
	defaultProvider providers.Provider,
	defaultDestination providers.DestinationConfig,
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
		runMaturityCase(t, defaultProvider, defaultDestination, maturityCase)
	})
}

func runPartialSuccessCase(
	t *testing.T,
	defaultProvider providers.Provider,
	defaultDestination providers.DestinationConfig,
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
				runMaturityCase(t, defaultProvider, defaultDestination, partialCase.Case)
			}
		case PartialSuccessClassifiedFailure:
			if partialCase.Case.Operation == "" {
				t.Fatal("classified partial-success requires an operation case")
			}
			if partialCase.Case.ErrorClass == "" {
				t.Fatal("classified partial-success requires an expected error class")
			}
			runMaturityCase(t, defaultProvider, defaultDestination, partialCase.Case)
		default:
			t.Fatalf("unknown partial-success mode %q", partialCase.Mode)
		}
	})
}

func runMaturityCase(
	t *testing.T,
	defaultProvider providers.Provider,
	defaultDestination providers.DestinationConfig,
	maturityCase MaturityCase,
) {
	t.Helper()
	assertMaturityCaseWellFormed(t, maturityCase)
	provider := maturityCase.Provider
	if provider == nil {
		provider = defaultProvider
	}
	destination := destinationOrDefault(maturityCase.Destination, defaultDestination)
	switch maturityCase.Operation {
	case OperationPlan:
		runMaturityPlanCase(t, provider, destination, maturityCase)
	case OperationUpsert:
		runMaturityUpsertCase(t, provider, destination, maturityCase)
	case OperationDelete:
		runMaturityDeleteCase(t, provider, destination, maturityCase)
	case OperationReadState:
		runMaturityReadStateCase(t, provider, destination, maturityCase)
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
	destination providers.DestinationConfig,
	maturityCase MaturityCase,
) {
	t.Helper()
	runtime := openRuntime(t, provider, destination)
	result, err := runtime.Plan(context.Background(), maturityCase.PlanRequest)
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
	destination providers.DestinationConfig,
	maturityCase MaturityCase,
) {
	t.Helper()
	runtime := openRuntime(t, provider, destination)
	result, err := runtime.Upsert(context.Background(), maturityCase.UpsertRequest)
	assertMutationOutcome(t, result, err, maturityCase)
}

func runMaturityDeleteCase(
	t *testing.T,
	provider providers.Provider,
	destination providers.DestinationConfig,
	maturityCase MaturityCase,
) {
	t.Helper()
	runtime := openRuntime(t, provider, destination)
	result, err := runtime.Delete(context.Background(), maturityCase.DeleteRequest)
	assertMutationOutcome(t, result, err, maturityCase)
}

func runMaturityReadStateCase(
	t *testing.T,
	provider providers.Provider,
	destination providers.DestinationConfig,
	maturityCase MaturityCase,
) {
	t.Helper()
	runtime := openRuntime(t, provider, destination)
	state, err := runtime.ReadState(context.Background(), maturityCase.ReadStateRequest)
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
	runtime := openRuntime(t, provider, maturityCase.HealthDestination)
	health, err := runtime.Health(context.Background())
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

func assertIdempotentUpsert(
	t *testing.T,
	runtime providers.DestinationRuntime,
	request providers.UpsertRequest,
	expectResult bool,
) {
	t.Helper()
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := runtime.Upsert(context.Background(), request)
		if err != nil {
			t.Fatalf("idempotent upsert attempt %d: %v", attempt, err)
		}
		if expectResult && result == nil {
			t.Fatalf("idempotent upsert attempt %d result must not be nil", attempt)
		}
	}
}

func assertIdempotentDelete(
	t *testing.T,
	runtime providers.DestinationRuntime,
	request providers.DeleteRequest,
	expectResult bool,
) {
	t.Helper()
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := runtime.Delete(context.Background(), request)
		if err != nil {
			t.Fatalf("idempotent delete attempt %d: %v", attempt, err)
		}
		if expectResult && result == nil {
			t.Fatalf("idempotent delete attempt %d result must not be nil", attempt)
		}
	}
}

func runUpsertErrorCheck(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	errorCase UpsertErrorCase,
) {
	t.Helper()
	t.Run("upsert-error/"+errorCase.Name, func(t *testing.T) {
		runtime := openRuntime(t, provider, defaultDestination)
		_, err := runtime.Upsert(context.Background(), errorCase.Request)
		assertProviderErrorClass(t, err, errorCase.ErrorClass)
	})
}

func runDeleteErrorCheck(
	t *testing.T,
	provider providers.Provider,
	defaultDestination providers.DestinationConfig,
	errorCase DeleteErrorCase,
) {
	t.Helper()
	t.Run("delete-error/"+errorCase.Name, func(t *testing.T) {
		runtime := openRuntime(t, provider, defaultDestination)
		_, err := runtime.Delete(context.Background(), errorCase.Request)
		assertProviderErrorClass(t, err, errorCase.ErrorClass)
	})
}

func openRuntime(
	t *testing.T,
	provider providers.Provider,
	destination providers.DestinationConfig,
) providers.DestinationRuntime {
	t.Helper()
	runtime, err := provider.OpenDestination(context.Background(), destination)
	if err != nil {
		t.Fatalf("open destination runtime: %v", err)
	}
	if runtime == nil {
		t.Fatal("destination runtime must not be nil")
	}
	return runtime
}

func destinationOrDefault(
	destination providers.DestinationConfig,
	defaultDestination providers.DestinationConfig,
) providers.DestinationConfig {
	if destination.Name == "" && destination.Config == nil {
		return defaultDestination
	}
	return destination
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
		{name: "data-map payloads", required: expected.DataMap, actual: capabilities.SupportsDataMap},
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
