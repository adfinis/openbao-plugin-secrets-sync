package providerutil

import (
	"errors"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

func TestConfigValueTrimsWhitespace(t *testing.T) {
	helpers := New("example")

	value := helpers.ConfigValue(providers.DestinationConfig{
		Config: map[string]string{"enabled": " true "},
	}, "enabled")
	if value != "true" {
		t.Fatalf("value = %q, want true", value)
	}
}

func TestBoolConfigValue(t *testing.T) {
	helpers := New("example")

	unset, err := helpers.BoolConfigValue(providers.DestinationConfig{}, "enabled", true)
	if err != nil {
		t.Fatalf("unset bool config returned error: %v", err)
	}
	if !unset {
		t.Fatalf("unset bool config = false, want fallback true")
	}

	parsed, err := helpers.BoolConfigValue(providers.DestinationConfig{
		Config: map[string]string{"enabled": " false "},
	}, "enabled", true)
	if err != nil {
		t.Fatalf("parsed bool config returned error: %v", err)
	}
	if parsed {
		t.Fatalf("parsed bool config = true, want false")
	}

	_, err = helpers.BoolConfigValue(providers.DestinationConfig{
		Config: map[string]string{"enabled": "sometimes"},
	}, "enabled", false)
	var providerError *providers.Error
	if !errors.As(err, &providerError) {
		t.Fatalf("invalid bool config error = %T, want *providers.Error", err)
	}
	if providerError.Class != providers.ErrorClassValidation {
		t.Fatalf("error class = %q, want %q", providerError.Class, providers.ErrorClassValidation)
	}
	if providerError.Message != "example enabled must be true or false" {
		t.Fatalf("error message = %q", providerError.Message)
	}
}

func TestProviderMessages(t *testing.T) {
	helpers := New("example")

	plan := helpers.BlockedPlan(providers.ErrorClassCapacity)
	if plan.Action != providers.PlanActionBlocked {
		t.Fatalf("plan action = %q, want %q", plan.Action, providers.PlanActionBlocked)
	}
	if plan.ErrorClass != providers.ErrorClassCapacity {
		t.Fatalf("plan error class = %q, want %q", plan.ErrorClass, providers.ErrorClassCapacity)
	}
	if plan.Message != "example provider plan failed" {
		t.Fatalf("plan message = %q", plan.Message)
	}

	err := helpers.ProviderError(providers.ErrorClassDrift)
	var providerError *providers.Error
	if !errors.As(err, &providerError) {
		t.Fatalf("provider error = %T, want *providers.Error", err)
	}
	if providerError.Class != providers.ErrorClassDrift {
		t.Fatalf("provider error class = %q, want %q", providerError.Class, providers.ErrorClassDrift)
	}
	if providerError.Message != "example request failed" {
		t.Fatalf("provider error message = %q", providerError.Message)
	}
}

func TestSetupErrorClass(t *testing.T) {
	helpers := New("example")

	if got := helpers.SetupErrorClass(errors.New("boom")); got != providers.ErrorClassInternal {
		t.Fatalf("plain error class = %q, want %q", got, providers.ErrorClassInternal)
	}

	err := &providers.Error{Class: providers.ErrorClassValidation, Message: "bad config"}
	if got := helpers.SetupErrorClass(err); got != providers.ErrorClassValidation {
		t.Fatalf("provider error class = %q, want %q", got, providers.ErrorClassValidation)
	}
}
