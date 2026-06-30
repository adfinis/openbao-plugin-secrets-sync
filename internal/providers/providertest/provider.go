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
	UpsertSuccess        *UpsertCase
	DeleteSuccess        *DeleteCase
	ReadStateCase        *ReadStateCase
	UpsertErrors         []UpsertErrorCase
	DeleteErrors         []DeleteErrorCase
}

// CapabilityExpectations declares capability bits expected from a provider.
type CapabilityExpectations struct {
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
	Request providers.ReadStateRequest
	Exists  bool
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
		if state.Exists != readStateCase.Exists {
			t.Fatalf("remote state exists = %v, want %v", state.Exists, readStateCase.Exists)
		}
	})
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
	if expected.SecretPath && !capabilities.SupportsSecretPath {
		t.Fatal("provider must support secret-path granularity")
	}
	if expected.SecretKey && !capabilities.SupportsSecretKey {
		t.Fatal("provider must support secret-key granularity")
	}
	if expected.UpdateIfOwned && !capabilities.SupportsUpdateIfOwned {
		t.Fatal("provider must support owned updates")
	}
	if expected.DeleteIfOwned && !capabilities.SupportsDeleteIfOwned {
		t.Fatal("provider must support owned deletes")
	}
	if expected.PayloadHashMetadata && !capabilities.SupportsPayloadHashMetadata {
		t.Fatal("provider must support payload hash metadata")
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
