package backend

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

func TestReconcilePlanDoesNotPersistStatus(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("secret-canary")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)

	planResp := env.read("reconcile/app/db/plan")
	assertNoErrorResponse(t, planResp)
	assertResponseValue(t, planResp, "applied", false)
	assertResponseValue(t, planResp, "state", string(domain.SyncStateSynced))
	objects := planResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 {
		t.Fatalf("reconcile plan objects = %d, want 1", len(objects))
	}
	if got := objects[0]["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("reconcile object state = %v, want %s", got, domain.SyncStateSynced)
	}
	if strings.Contains(fmt.Sprint(planResp.Data), "secret-canary") {
		t.Fatalf("reconcile plan response contains secret value: %#v", planResp.Data)
	}
	status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want nil after reconcile plan", status)
	}
}

func TestReconcileApplyMapsReadStateToStatus(t *testing.T) {
	testCases := []struct {
		name         string
		resolvedName string
		state        domain.SyncState
		errorClass   providers.ErrorClass
	}{
		{
			name:         "synced",
			resolvedName: "prod/app/db",
			state:        domain.SyncStateSynced,
		},
		{
			name:         "missing",
			resolvedName: "prod/missing/app/db",
			state:        domain.SyncStateRemoteMissing,
		},
		{
			name:         "ownership",
			resolvedName: "prod/ownership/app/db",
			state:        domain.SyncStateRemoteOwnershipLost,
			errorClass:   providers.ErrorClassOwnership,
		},
		{
			name:         "drift",
			resolvedName: "prod/drift/app/db",
			state:        domain.SyncStateDrifted,
			errorClass:   providers.ErrorClassDrift,
		},
		{
			name:         "authn",
			resolvedName: "prod/authn/app/db",
			state:        domain.SyncStateDestinationAuthError,
			errorClass:   providers.ErrorClassAuthn,
		},
		{
			name:         "rate-limit",
			resolvedName: "prod/rate-limit/app/db",
			state:        domain.SyncStateDestinationRateLimited,
			errorClass:   providers.ErrorClassRateLimit,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newBackendTestEnv(t)

			env.writeAppDBSecret("secret-canary")
			env.createFakeDestination("default")
			associationResp := env.createFakeAssociationWithResolvedName(testCase.resolvedName)
			associationID := associationIDFromResponse(t, associationResp)

			resp := env.update("reconcile/app/db")
			assertNoErrorResponse(t, resp)
			assertResponseValue(t, resp, "applied", true)
			objects := resp.Data["objects"].([]map[string]interface{})
			if len(objects) != 1 {
				t.Fatalf("reconcile objects = %d, want 1", len(objects))
			}
			if got := objects[0]["state"]; got != string(testCase.state) {
				t.Fatalf("reconcile object state = %v, want %s", got, testCase.state)
			}
			if got := objects[0]["error_class"]; got != string(testCase.errorClass) {
				t.Fatalf("reconcile error class = %v, want %s", got, testCase.errorClass)
			}
			if strings.Contains(fmt.Sprint(resp.Data), "secret-canary") {
				t.Fatalf("reconcile response contains secret value: %#v", resp.Data)
			}

			status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
			if err != nil {
				t.Fatalf("read status: %v", err)
			}
			if status == nil {
				t.Fatal("status must be written")
			}
			if got := status.State; got != string(testCase.state) {
				t.Fatalf("status state = %v, want %s", got, testCase.state)
			}
			if got := status.LastErrorClass; got != string(testCase.errorClass) {
				t.Fatalf("status last error class = %v, want %s", got, testCase.errorClass)
			}
			if strings.Contains(fmt.Sprint(status), "secret-canary") {
				t.Fatalf("status contains secret value: %#v", status)
			}
		})
	}
}

func TestReconcileApplySyncedPreservesRepairCount(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)
	if err := putStatus(context.Background(), env.storage, statusRecord{
		Path:                  "app/db",
		Version:               1,
		AssociationID:         associationID,
		ObjectID:              syncObjectIDSecretPath,
		DestinationRef:        "fake/default",
		ResolvedName:          "prod/app/db",
		State:                 string(domain.SyncStateDrifted),
		LastDriftDetectedTime: "2026-07-01T10:00:00Z",
		LastRepairTime:        "2026-07-01T10:01:00Z",
		RepairCount:           3,
		UpdatedTime:           "2026-07-01T10:01:00Z",
	}); err != nil {
		t.Fatalf("write drift status: %v", err)
	}

	resp := env.update("reconcile/app/db")
	assertNoErrorResponse(t, resp)

	status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status == nil {
		t.Fatal("status must be written")
	}
	if got := status.State; got != string(domain.SyncStateSynced) {
		t.Fatalf("status state = %s, want %s", got, domain.SyncStateSynced)
	}
	if got := status.RepairCount; got != 3 {
		t.Fatalf("repair_count = %d, want 3", got)
	}
	if got := status.LastRepairTime; got != "2026-07-01T10:01:00Z" {
		t.Fatalf("last_repair_time = %s, want 2026-07-01T10:01:00Z", got)
	}
}
