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

func TestReconcilePlanRejectsDestinationPolicyBeforeReadState(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/authn/app/db")
	associationID := associationIDFromResponse(t, associationResp)
	restrictResp := env.update("destinations/fake/default", map[string]interface{}{
		destinationAllowedResolvedNamePrefixesField: "safe",
	})
	if restrictResp != nil && restrictResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", restrictResp.Error())
	}

	resp := env.read("reconcile/app/db/plan")
	assertNoErrorResponse(t, resp)
	assertResponseValue(t, resp, "applied", false)
	assertResponseValue(t, resp, "state", string(domain.SyncStateValidationError))
	objects := resp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 {
		t.Fatalf("reconcile plan objects = %d, want 1", len(objects))
	}
	object := objects[0]
	if got := object["state"]; got != string(domain.SyncStateValidationError) {
		t.Fatalf("reconcile object state = %v, want %s", got, domain.SyncStateValidationError)
	}
	if got := object["error_class"]; got != string(providers.ErrorClassValidation) {
		t.Fatalf("reconcile error class = %v, want %s", got, providers.ErrorClassValidation)
	}
	if got := fmt.Sprint(object["message"]); !strings.Contains(got, "does not allow resolved name") {
		t.Fatalf("reconcile message = %q, want destination policy failure", got)
	}

	status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want nil after reconcile plan", status)
	}
}

func TestLoadReconcileLookupContextFailures(t *testing.T) {
	testCases := []struct {
		name        string
		association associationRecord
		destination *destinationRecord
		wantState   domain.SyncState
		wantClass   providers.ErrorClass
		wantMessage string
	}{
		{
			name: "association disabled",
			association: reconcileLookupAssociationFixture(func(record *associationRecord) {
				record.Enabled = false
			}),
			wantState:   domain.SyncStateDisabled,
			wantMessage: "association is disabled",
		},
		{
			name: "provider unsupported",
			association: reconcileLookupAssociationFixture(func(record *associationRecord) {
				record.DestinationType = "unsupported"
				record.DestinationName = "default"
				record.DestinationRef = "unsupported/default"
			}),
			wantState:   domain.SyncStateValidationError,
			wantClass:   providers.ErrorClassValidation,
			wantMessage: "destination provider is unsupported",
		},
		{
			name:        "destination missing",
			association: reconcileLookupAssociationFixture(),
			wantState:   domain.SyncStateValidationError,
			wantClass:   providers.ErrorClassValidation,
			wantMessage: "destination is missing",
		},
		{
			name:        "destination disabled",
			association: reconcileLookupAssociationFixture(),
			destination: &destinationRecord{
				Type:     providerTypeFake,
				Name:     "default",
				Disabled: true,
			},
			wantState:   domain.SyncStateDisabled,
			wantMessage: "destination is disabled",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newBackendTestEnv(t)
			if testCase.destination != nil {
				if err := putDestination(context.Background(), env.storage, *testCase.destination); err != nil {
					t.Fatalf("write destination fixture: %v", err)
				}
			}
			cfg := defaultGlobalConfig()

			_, failure := env.b.loadReconcileLookupContext(
				context.Background(),
				env.storage,
				testCase.association,
				&cfg,
			)
			if failure == nil {
				t.Fatal("loadReconcileLookupContext() failure = nil, want failure")
			}
			assertReconcileLookupFailure(t, failure, testCase.wantState, testCase.wantClass, testCase.wantMessage)

			results := failure.results(testCase.association, 7, []string{"username", "password"})
			if len(results) != 2 {
				t.Fatalf("failure results = %d, want 2", len(results))
			}
			assertReconcileObjectFailure(t, results[0], testCase.wantState, testCase.wantClass, testCase.wantMessage)
			assertReconcileObjectFailure(t, results[1], testCase.wantState, testCase.wantClass, testCase.wantMessage)

			result := failure.result(testCase.association, 7, "password")
			assertReconcileObjectFailure(t, result, testCase.wantState, testCase.wantClass, testCase.wantMessage)
		})
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
			if testCase.state == domain.SyncStateRemoteOwnershipLost {
				assertHintContains(t, objects[0], "Inspect or remove the remote object first")
				assertNextActionCommand(
					t,
					objects[0],
					"manual_sync",
					"bao write <mount>/associations/app/db/sync destination=fake/default",
				)
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

func reconcileLookupAssociationFixture(modifiers ...func(*associationRecord)) associationRecord {
	record := associationRecord{
		ID:              "assoc-reconcile-lookup-test",
		Path:            "app/db",
		DestinationType: providerTypeFake,
		DestinationName: "default",
		DestinationRef:  "fake/default",
		ResolvedName:    "prod/app/db",
		Granularity:     syncGranularitySecretPath,
		Format:          defaultAssociationFormat,
		DeleteMode:      deleteModeRetain,
		Enabled:         true,
	}
	for _, modifier := range modifiers {
		modifier(&record)
	}
	return record
}

func assertReconcileLookupFailure(
	t *testing.T,
	failure *reconcileLookupFailure,
	wantState domain.SyncState,
	wantClass providers.ErrorClass,
	wantMessage string,
) {
	t.Helper()
	if failure.state != wantState {
		t.Fatalf("failure state = %s, want %s", failure.state, wantState)
	}
	if failure.errorClass != wantClass {
		t.Fatalf("failure errorClass = %s, want %s", failure.errorClass, wantClass)
	}
	if failure.message != wantMessage {
		t.Fatalf("failure message = %q, want %q", failure.message, wantMessage)
	}
}

func assertReconcileObjectFailure(
	t *testing.T,
	result reconcileObjectResult,
	wantState domain.SyncState,
	wantClass providers.ErrorClass,
	wantMessage string,
) {
	t.Helper()
	if result.state != wantState {
		t.Fatalf("result state = %s, want %s", result.state, wantState)
	}
	if result.errorClass != wantClass {
		t.Fatalf("result errorClass = %s, want %s", result.errorClass, wantClass)
	}
	if result.message != wantMessage {
		t.Fatalf("result message = %q, want %q", result.message, wantMessage)
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
