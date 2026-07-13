package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
)

func TestAWSAssociationStoresNormalizedDeleteRecoveryWindow(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createAWSDestination(t, env)

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(awssecretsmanager.ProviderType, "prod"),
		"resolved_name": "prod/app/db",
		"enabled":       false,
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: 14,
	})
	assertNoErrorResponse(t, resp)

	want := map[string]string{
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: "14",
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

	readResp := env.read("associations/app/db/" + associationID)
	assertNoErrorResponse(t, readResp)
	assertAssociationProviderConfig(t, readResp.Data["provider_config"], want)
}

func TestAWSAssociationDefaultsDeleteRecoveryWindow(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createAWSDestination(t, env)

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(awssecretsmanager.ProviderType, "prod"),
		"resolved_name": "prod/app/db",
		"enabled":       false,
	})
	assertNoErrorResponse(t, resp)
	assertAssociationProviderConfig(t, resp.Data["provider_config"], map[string]string{
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: "7",
	})
}

func TestAWSAssociationRejectsInvalidDeleteRecoveryWindow(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
	}{
		{name: "below minimum", value: 6},
		{name: "above maximum", value: 31},
		{name: "not an integer", value: "six"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newBackendTestEnv(t)
			env.writeAppDBSecret("initial")
			createAWSDestination(t, env)

			resp := env.update("associations/app/db", map[string]interface{}{
				"destination":   destinationRef(awssecretsmanager.ProviderType, "prod"),
				"resolved_name": "prod/app/db",
				"enabled":       false,
				awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: tt.value,
			})
			if resp == nil || !resp.IsError() {
				t.Fatalf("association response = %#v, want validation error", resp)
			}
		})
	}
}

func TestAWSAssociationDeleteRecoveryWindowUpdateDoesNotEnqueue(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createAWSDestination(t, env)

	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(awssecretsmanager.ProviderType, "prod"),
		"resolved_name": "prod/app/db",
	})
	assertNoErrorResponse(t, initialResp)
	requireSingleOperationID(t, operationIDsFromResponse(t, initialResp), "initial AWS association")
	associationID := associationIDFromResponse(t, initialResp)

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination": destinationRef(awssecretsmanager.ProviderType, "prod"),
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: 14,
	})
	assertNoErrorResponse(t, updateResp)
	if operationIDs := operationIDsFromResponse(t, updateResp); len(operationIDs) != 0 {
		t.Fatalf("delete policy update operation IDs = %#v, want none", operationIDs)
	}
	if got := associationIDFromResponse(t, updateResp); got != associationID {
		t.Fatalf("updated association ID = %q, want %q", got, associationID)
	}
	assertAssociationProviderConfig(t, updateResp.Data["provider_config"], map[string]string{
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: "14",
	})
}

func createAWSDestination(t *testing.T, env *backendTestEnv) {
	t.Helper()
	resp := env.update("destinations/aws-sm/prod", map[string]interface{}{
		awssecretsmanager.ConfigKeyRegion: "eu-central-1",
	})
	if resp != nil && resp.IsError() {
		t.Fatalf("create AWS destination: %v", resp.Error())
	}
}
