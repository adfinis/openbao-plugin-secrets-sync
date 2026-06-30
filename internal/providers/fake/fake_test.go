package fake

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
)

func TestPlanActions(t *testing.T) {
	tests := map[string]string{
		"prod/app/db":          providers.PlanActionCreate,
		"prod/update/app/db":   providers.PlanActionUpdate,
		"prod/noop/app/db":     providers.PlanActionNoop,
		"prod/conflict/app/db": providers.PlanActionConflict,
		"prod/blocked/app/db":  providers.PlanActionBlocked,
	}
	for resolvedName, expected := range tests {
		t.Run(resolvedName, func(t *testing.T) {
			result, err := Provider{}.Plan(context.Background(), providers.PlanRequest{ResolvedName: resolvedName})
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if result == nil || result.Action != expected {
				t.Fatalf("plan result = %#v, want action %s", result, expected)
			}
		})
	}
}

func TestValidateAndHealthDiagnostics(t *testing.T) {
	if err := (Provider{}).Validate(context.Background(), providers.DestinationConfig{Name: "invalid"}); err == nil {
		t.Fatal("invalid fake destination must fail validation")
	}
	health, err := Provider{}.Health(context.Background(), providers.DestinationConfig{Name: "unhealthy"})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health == nil || health.Healthy {
		t.Fatalf("health = %#v, want unhealthy", health)
	}
	if health.ErrorClass != providers.ErrorClassUnavailable {
		t.Fatalf("health error class = %s, want %s", health.ErrorClass, providers.ErrorClassUnavailable)
	}
}
