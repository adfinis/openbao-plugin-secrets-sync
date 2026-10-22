package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

func TestVariableValueAvailabilityDistinguishesNullAndEmpty(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		unavailable bool
	}{
		{"missing", `{"hidden":true}`, true},
		{"null", `{"hidden":true,"value":null}`, true},
		{"empty", `{"hidden":true,"value":""}`, false},
		{"visible null", `{"hidden":false,"value":null}`, true},
		{"visible empty", `{"hidden":false,"value":""}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var variable gitlabVariable
			if err := json.Unmarshal([]byte(test.body), &variable); err != nil {
				t.Fatal(err)
			}
			if variable.valueUnavailable != test.unavailable {
				t.Fatalf("value unavailable = %t, want %t", variable.valueUnavailable, test.unavailable)
			}
		})
	}
}

func hiddenReadbackFixture(
	t *testing.T,
) (providers.DestinationRuntime, *memoryProjectVariableClient, providers.UpsertRequest) {
	t.Helper()
	value := []byte("fixture_hidden_123")
	req := defaultUpsertRequest(payloadSHA256(value), value, 1)
	req.Association = associationConfigWith(map[string]string{ConfigKeyHidden: testBoolTrue})
	input := variableInputFromUpsert(mustGitLabAssociationOptions(req.Association), req)
	variable := variableFromInput(input)
	variable.Value = ""
	variable.valueUnavailable = true
	client := gitlabClientWithVariable(variable)
	return runtimeWithClient(t, client), client, req
}

func assertHiddenReadbackNoop(t *testing.T, runtime providers.DestinationRuntime, req providers.UpsertRequest) {
	t.Helper()
	planReq := defaultPlanRequest(req.PayloadSHA256, req.SourceVersion)
	planReq.Association = req.Association
	planReq.PayloadBytes = len(req.Payload)
	plan, err := runtime.Plan(context.Background(), planReq)
	if err != nil || plan.Action != providers.PlanActionNoop {
		t.Fatalf("hidden metadata-only plan must be noop: plan=%v err=%v", plan, err)
	}
	if _, err := runtime.Upsert(context.Background(), req); err != nil {
		t.Fatalf("stable hidden upsert must not write: %v", err)
	}
	readReq := defaultReadStateRequest(req.PayloadSHA256, req.SourceVersion)
	readReq.Association = req.Association
	state, err := runtime.ReadState(context.Background(), readReq)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Owned || state.Verification != providers.RemoteStateVerificationMetadata ||
		state.PayloadSHA256 != req.PayloadSHA256 || state.SourceVersion != req.SourceVersion {
		t.Fatal("hidden read-state must return owned metadata and metadata-only verification")
	}
}

func TestHiddenUnavailableValueUsesMetadataForStableNoopAndRotation(t *testing.T) {
	runtime, client, req := hiddenReadbackFixture(t)
	client.errors["update"] = errors.New("unexpected write for unchanged hidden variable")
	assertHiddenReadbackNoop(t, runtime, req)
	delete(client.errors, "update")
	req.SourceVersion = 2
	req.Payload = []byte("fixture_hidden_456")
	req.PayloadSHA256 = payloadSHA256(req.Payload)
	if _, err := runtime.Upsert(context.Background(), req); err != nil {
		t.Fatalf("rotate hidden variable: %v", err)
	}
	variable := client.variables[variableStorageKey(testResolvedName, testEnvScope)]
	if variable.Value != string(req.Payload) || !variable.Hidden || !variable.Masked {
		t.Fatal("hidden rotation must update the payload and preserve visibility attributes")
	}
	client.errors["update"] = errors.New("unexpected write after hidden rotation")
	assertHiddenReadbackNoop(t, runtime, req)
	deleteReq := defaultDeleteRequest(req.SourceVersion)
	deleteReq.Association = req.Association
	if _, err := runtime.Delete(context.Background(), deleteReq); err != nil {
		t.Fatalf("owned delete must not require hidden value readback: %v", err)
	}
	if len(client.variables) != 0 {
		t.Fatal("owned hidden variable remains after deletion")
	}
}

func TestHiddenMetadataCannotIndependentlyDetectValueChanges(t *testing.T) {
	runtime, client, req := hiddenReadbackFixture(t)
	variable := client.variables[variableStorageKey(testResolvedName, testEnvScope)]
	variable.Value = testDriftedValue // Simulated server value remains unavailable to the provider.
	client.errors["update"] = errors.New("metadata-only state must not claim a value mismatch")
	assertHiddenReadbackNoop(t, runtime, req)
}

func TestAvailableEmptyValuesKeepIndependentReadback(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(map[bool]string{false: "visible", true: "hidden"}[hidden], func(t *testing.T) {
			runtime, client, req := hiddenReadbackFixture(t)
			variable := client.variables[variableStorageKey(testResolvedName, testEnvScope)]
			variable.Hidden = hidden
			variable.valueUnavailable = false
			if !hidden {
				req.Association = defaultAssociationConfig()
				variable.Masked = false
			}
			readReq := defaultReadStateRequest(req.PayloadSHA256, req.SourceVersion)
			readReq.Association = req.Association
			state, err := runtime.ReadState(context.Background(), readReq)
			if err != nil {
				t.Fatal(err)
			}
			if state.Verification != providers.RemoteStateVerificationValue || state.PayloadSHA256 != payloadSHA256(nil) {
				t.Fatal("present empty values must use the empty payload hash and independent value verification")
			}
			planReq := defaultPlanRequest(req.PayloadSHA256, req.SourceVersion)
			planReq.Association = req.Association
			planReq.PayloadBytes = len(req.Payload)
			plan, err := runtime.Plan(context.Background(), planReq)
			if err != nil || plan.Action != providers.PlanActionUpdate {
				t.Fatal("available empty value must be detected as drift when the desired payload is nonempty")
			}
			if _, err := runtime.Upsert(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if client.variables[variableStorageKey(testResolvedName, testEnvScope)].Value != string(req.Payload) {
				t.Fatal("readable value drift was not repaired")
			}
		})
	}
}

func TestVisibleEmptyPayloadIsStableNoop(t *testing.T) {
	req := defaultUpsertRequest(payloadSHA256(nil), nil, 1)
	variable := variableFromInput(variableInputFromUpsert(defaultAssociationOptions(), req))
	client := gitlabClientWithVariable(variable)
	client.errors["update"] = errors.New("present empty value must not require an update")
	runtime := runtimeWithClient(t, client)
	planReq := defaultPlanRequest(req.PayloadSHA256, 1)
	planReq.PayloadBytes = 0
	plan, err := runtime.Plan(context.Background(), planReq)
	if err != nil || plan.Action != providers.PlanActionNoop {
		t.Fatal("present empty payload must have a stable no-op plan")
	}
	if _, err := runtime.Upsert(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestVisibleUnavailableValueFailsClosed(t *testing.T) {
	runtime, client, req := hiddenReadbackFixture(t)
	variable := client.variables[variableStorageKey(testResolvedName, testEnvScope)]
	variable.Hidden = false
	req.Association = defaultAssociationConfig()
	planReq := defaultPlanRequest(req.PayloadSHA256, req.SourceVersion)
	plan, err := runtime.Plan(context.Background(), planReq)
	if err != nil || plan.Action != providers.PlanActionBlocked || plan.ErrorClass != providers.ErrorClassUnavailable {
		t.Fatal("unexpected visible null must block the plan as unavailable")
	}
	_, err = runtime.Upsert(context.Background(), req)
	assertProviderErrorClass(t, err, providers.ErrorClassUnavailable)
	_, err = runtime.ReadState(context.Background(), defaultReadStateRequest(req.PayloadSHA256, req.SourceVersion))
	assertProviderErrorClass(t, err, providers.ErrorClassUnavailable)
}

func TestReadStateForeignUnavailableValueRetainsOwnershipLoss(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(map[bool]string{false: "visible", true: "hidden"}[hidden], func(t *testing.T) {
			runtime, client, req := hiddenReadbackFixture(t)
			variable := client.variables[variableStorageKey(testResolvedName, testEnvScope)]
			metadata, _ := ownershipMetadata(variable)
			metadata.AssociationID = "foreign"
			variable.Description = metadataDescription(metadata)
			variable.Hidden = hidden
			readReq := defaultReadStateRequest(req.PayloadSHA256, req.SourceVersion)
			readReq.Association = req.Association
			state, err := runtime.ReadState(context.Background(), readReq)
			if err != nil {
				t.Fatal(err)
			}
			if !state.Exists || !state.OwnershipKnown || state.Owned {
				t.Fatal("unavailable foreign values must retain confirmed ownership-loss state")
			}
		})
	}
}

func TestUnavailableValueDoesNotBypassOwnershipOrNewerVersionGuards(t *testing.T) {
	for _, test := range []struct {
		name       string
		metadata   variableMetadata
		planAction string
		class      providers.ErrorClass
	}{
		{"foreign", variableMetadata{AssociationID: "foreign"}, providers.PlanActionConflict, providers.ErrorClassCollision},
		{"newer", variableMetadata{SourceVersion: 2}, providers.PlanActionBlocked, providers.ErrorClassDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, client, req := hiddenReadbackFixture(t)
			variable := client.variables[variableStorageKey(testResolvedName, testEnvScope)]
			metadata, _ := ownershipMetadata(variable)
			if test.metadata.AssociationID != "" {
				metadata.AssociationID = test.metadata.AssociationID
			}
			if test.metadata.SourceVersion != 0 {
				metadata.SourceVersion = test.metadata.SourceVersion
			}
			variable.Description = metadataDescription(metadata)
			planReq := defaultPlanRequest(req.PayloadSHA256, req.SourceVersion)
			planReq.Association = req.Association
			plan, err := runtime.Plan(context.Background(), planReq)
			if err != nil || plan.Action != test.planAction || plan.ErrorClass != test.class {
				t.Fatalf("plan must retain the %s guard", test.name)
			}
			_, err = runtime.Upsert(context.Background(), req)
			expected := test.class
			if test.name == "foreign" {
				expected = providers.ErrorClassOwnership
			}
			assertProviderErrorClass(t, err, expected)
		})
	}
}
