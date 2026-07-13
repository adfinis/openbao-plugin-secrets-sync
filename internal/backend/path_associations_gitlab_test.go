package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
)

func TestGitLabAssociationStoresNormalizedProviderConfig(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createGitLabDestination(t, env)

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		"resolved_name":                  "APP_DB",
		"enabled":                        false,
		gitlab.ConfigKeyEnvironmentScope: "production",
		gitlab.ConfigKeyProtected:        true,
		gitlab.ConfigKeyVariableRaw:      false,
		gitlab.ConfigKeyVariableType:     gitlab.VariableTypeFile,
	})
	assertNoErrorResponse(t, resp)

	want := map[string]string{
		gitlab.ConfigKeyEnvironmentScope: "production",
		gitlab.ConfigKeyProtected:        "true",
		gitlab.ConfigKeyMasked:           "false",
		gitlab.ConfigKeyHidden:           "false",
		gitlab.ConfigKeyVariableRaw:      "false",
		gitlab.ConfigKeyVariableType:     gitlab.VariableTypeFile,
	}
	assertAssociationProviderConfig(t, resp.Data["provider_config"], want)

	associationID := associationIDFromResponse(t, resp)
	record, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read stored association: %v", err)
	}
	if record == nil {
		t.Fatalf("association %s must exist", associationID)
	}
	assertAssociationProviderConfig(t, record.ProviderConfig, want)
	if got := record.ProviderIdentity; got != "production" {
		t.Fatalf("provider identity = %q, want production", got)
	}

	readResp := env.read("associations/app/db/" + associationID)
	assertNoErrorResponse(t, readResp)
	assertAssociationProviderConfig(t, readResp.Data["provider_config"], want)
}

func TestGitLabAssociationScopeParticipatesInIdentity(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createGitLabDestination(t, env)

	firstResp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		"resolved_name":                  "APP_DB",
		"enabled":                        false,
		gitlab.ConfigKeyEnvironmentScope: "staging",
	})
	assertNoErrorResponse(t, firstResp)
	secondResp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		"resolved_name":                  "APP_DB",
		"enabled":                        false,
		gitlab.ConfigKeyEnvironmentScope: "production",
	})
	assertNoErrorResponse(t, secondResp)

	firstID := associationIDFromResponse(t, firstResp)
	secondID := associationIDFromResponse(t, secondResp)
	if firstID == secondID {
		t.Fatalf("association IDs = %q, want distinct IDs across environment scopes", firstID)
	}
	assertAppDBAssociationCount(t, env.storage, 2)

	idempotentResp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		"resolved_name":                  "APP_DB",
		"enabled":                        false,
		gitlab.ConfigKeyEnvironmentScope: "production",
	})
	assertNoErrorResponse(t, idempotentResp)
	if got := associationIDFromResponse(t, idempotentResp); got != secondID {
		t.Fatalf("same-scope association ID = %q, want existing ID %q", got, secondID)
	}
	assertAppDBAssociationCount(t, env.storage, 2)
}

func TestGitLabAssociationEnvironmentScopeSelectsExistingAssociation(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createGitLabDestination(t, env)

	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		"resolved_name":                  "APP_DB",
		"enabled":                        false,
		gitlab.ConfigKeyEnvironmentScope: "staging",
	})
	assertNoErrorResponse(t, initialResp)
	associationID := associationIDFromResponse(t, initialResp)

	secondResp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		gitlab.ConfigKeyEnvironmentScope: "production",
		"enabled":                        false,
	})
	assertNoErrorResponse(t, secondResp)
	secondID := associationIDFromResponse(t, secondResp)
	if secondID == associationID {
		t.Fatalf("new environment scope association ID = %q, want distinct ID", secondID)
	}

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		gitlab.ConfigKeyEnvironmentScope: "staging",
		gitlab.ConfigKeyProtected:        true,
	})
	assertNoErrorResponse(t, updateResp)
	if got := associationIDFromResponse(t, updateResp); got != associationID {
		t.Fatalf("selected association ID = %q, want staging association %q", got, associationID)
	}

	record, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read stored association: %v", err)
	}
	if record == nil || record.ProviderIdentity != "staging" ||
		record.ProviderConfig[gitlab.ConfigKeyProtected] != "true" {
		t.Fatalf("stored staging association = %#v, want protected staging config", record)
	}
}

func TestGitLabAssociationMutableConfigUpdateEnqueuesCurrentVersion(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createGitLabDestination(t, env)

	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(gitlab.ProviderType, "prod"),
		"resolved_name": "APP_DB",
	})
	assertNoErrorResponse(t, initialResp)
	initialOperationID := requireSingleOperationID(
		t,
		operationIDsFromResponse(t, initialResp),
		"initial GitLab association",
	)
	associationID := associationIDFromResponse(t, initialResp)

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination":             destinationRef(gitlab.ProviderType, "prod"),
		gitlab.ConfigKeyProtected: true,
	})
	assertNoErrorResponse(t, updateResp)
	updateOperationID := requireSingleOperationID(
		t,
		operationIDsFromResponse(t, updateResp),
		"GitLab association config update",
	)
	if updateOperationID == initialOperationID {
		t.Fatalf("config update operation ID = %q, want a new salted operation", updateOperationID)
	}
	if got := associationIDFromResponse(t, updateResp); got != associationID {
		t.Fatalf("config update association ID = %q, want %q", got, associationID)
	}
	assertOutboxOperation(t, env.storage, updateOperationID, 1, outboxStatePending)

	record, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read stored association: %v", err)
	}
	if record == nil || record.ProviderConfig[gitlab.ConfigKeyProtected] != "true" {
		t.Fatalf("stored provider config = %#v, want protected=true", record)
	}
}

func TestGitLabAssociationConfigQueueFailureRollsBackConfig(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createGitLabDestination(t, env)
	configResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": 1,
	})
	if configResp != nil && configResp.IsError() {
		t.Fatalf("set queue capacity: %v", configResp.Error())
	}

	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(gitlab.ProviderType, "prod"),
		"resolved_name": "APP_DB",
	})
	assertNoErrorResponse(t, initialResp)
	associationID := associationIDFromResponse(t, initialResp)
	requireSingleOperationID(t, operationIDsFromResponse(t, initialResp), "initial GitLab association")

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination":             destinationRef(gitlab.ProviderType, "prod"),
		gitlab.ConfigKeyProtected: true,
	})
	if updateResp == nil || !updateResp.IsError() {
		t.Fatalf("config update response = %#v, want queue capacity error", updateResp)
	}
	assertHintContains(t, updateResp.Data, "Queue capacity is exhausted")

	record, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read stored association: %v", err)
	}
	if record == nil {
		t.Fatalf("association %s must exist", associationID)
	}
	if got := record.ProviderConfig[gitlab.ConfigKeyProtected]; got != "false" {
		t.Fatalf("rolled-back protected config = %q, want false", got)
	}
	assertQueueCount(t, env.b, env.storage, "pending", 1)
}

func createGitLabDestination(t *testing.T, env *backendTestEnv) {
	t.Helper()
	resp := env.update("destinations/gitlab/prod", map[string]interface{}{
		gitlab.ConfigKeyProjectID: "platform/app",
		gitlab.ConfigKeyToken:     "test-token",
	})
	if resp != nil && resp.IsError() {
		t.Fatalf("create GitLab destination: %v", resp.Error())
	}
}

func assertAssociationProviderConfig(t *testing.T, raw interface{}, want map[string]string) {
	t.Helper()
	var got map[string]string
	switch config := raw.(type) {
	case map[string]string:
		got = config
	case map[string]interface{}:
		got = make(map[string]string, len(config))
		for key, value := range config {
			stringValue, ok := value.(string)
			if !ok {
				t.Fatalf("provider_config[%q] = %#v, want string", key, value)
			}
			got[key] = stringValue
		}
	default:
		t.Fatalf("provider_config = %#v, want string map", raw)
	}
	if !stringMapsEqual(got, want) {
		t.Fatalf("provider_config = %#v, want %#v", got, want)
	}
}
