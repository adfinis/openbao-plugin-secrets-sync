package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestErrorResponseWithDiagnosticPreservesOpenBaoErrorShape(t *testing.T) {
	resp := errorResponseWithDiagnostic("blocked", queueCapacityDiagnostic("secret-sync"))
	if resp == nil || !resp.IsError() {
		t.Fatalf("response = %#v, want OpenBao error response", resp)
	}
	errorText := resp.Error().Error()
	for _, want := range []string{
		"blocked",
		"Hint: Queue capacity is exhausted",
		"Next action: bao read secret-sync/queue",
	} {
		if !strings.Contains(errorText, want) {
			t.Fatalf("error text = %q, want substring %q", errorText, want)
		}
	}
	data, ok := resp.Data["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("error data = %T, want map[string]interface{}", resp.Data["data"])
	}
	assertHintContains(t, data, "Queue capacity is exhausted")
	assertNextActionCommand(t, data, "read_queue", "bao read secret-sync/queue")
}

func TestDestinationHealthDiagnosticUsesReadCommand(t *testing.T) {
	diagnostic := destinationFailureDiagnostic(
		"secret-sync",
		"aws-sm/prod",
		"",
		"Destination is unavailable. Check destination health.",
	)
	data := newResponseData(diagnosticResponseFields(diagnostic)...)

	assertNextActionCommand(
		t,
		data,
		"check_destination_health",
		"bao read secret-sync/destinations/aws-sm/prod/health",
	)
}

func TestAssociationPlanDiagnosticUsesSupportedOperation(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createDefaultFakeAssociation()
	action := associationPlanAction("secret-sync", "app/db", "fake/default")
	assertNextActionCommand(t, newResponseData(diagnosticResponseFields(diagnostic{
		NextActions: []diagnosticAction{action},
	})...), "inspect_association_plan", "bao write secret-sync/associations/app/db/plan destination=fake/default")
	response, err := env.b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation, Path: action.Path, Storage: env.storage,
		Data: map[string]interface{}{"destination": action.Parameters["destination"]},
	})
	if err != nil {
		t.Fatalf("suggested plan operation failed: %v", err)
	}
	assertNoErrorResponse(t, response)
	if action.MutatesRemote {
		t.Fatal("association plan diagnostic must not mutate the remote object")
	}
}

func TestCollisionStatusUsesConflictDiagnostic(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createFakeAssociationWithResolvedName("prod/collision/app/db")
	env.runPeriodicAllowed("collision dispatch")
	assertStatusObjectState(t, env.b, env.storage, domain.SyncState("REMOTE_CONFLICT"))

	for _, state := range []domain.SyncState{domain.SyncState("REMOTE_CONFLICT"), domain.SyncStateDrifted} {
		record := statusRecord{
			Path: "app/db", DestinationRef: "fake/default", State: string(state),
			LastErrorClass: string(providers.ErrorClassCollision),
		}
		result := statusDiagnosticForRecord("secret-sync", record)
		if !strings.Contains(result.Hint, "name conflict") {
			t.Fatalf("collision hint = %q, want name conflict guidance", result.Hint)
		}
		if len(result.NextActions) != 1 || result.NextActions[0].MutatesRemote {
			t.Fatalf("collision actions = %#v, want one non-mutating inspection", result.NextActions)
		}
	}
}

func TestPayloadCapacityDiagnosticDoesNotSuggestQueueRetry(t *testing.T) {
	result := statusDiagnosticForRecord("secret-sync", statusRecord{
		Path: "app/db", DestinationRef: "fake/default", State: string(domain.SyncStateQueueBlocked),
		LastOperationID: "oversized-operation", LastErrorClass: string(providers.ErrorClassCapacity),
	})
	if !strings.Contains(result.Hint, "payload") || !strings.Contains(result.Hint, "size limit") {
		t.Fatalf("capacity hint = %q, want payload size guidance", result.Hint)
	}
	for _, action := range result.NextActions {
		if action.MutatesRemote || strings.HasPrefix(action.Path, "queue") || action.Path == "config" {
			t.Fatalf("capacity action = %#v, want association inspection", action)
		}
	}
}
