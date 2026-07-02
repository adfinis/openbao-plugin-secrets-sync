package backend

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/kubernetessecrets"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const redactionCanary = "secret-canary-redaction-boundary"

func TestSecurityBoundaryDestinationReadRedactsSensitiveConfig(t *testing.T) {
	env := newBackendTestEnv(t)

	testCases := []struct {
		name              string
		path              string
		fields            map[string]interface{}
		sensitiveValues   map[string]string
		redactedKeyOrder  []string
		publicConfigKey   string
		publicConfigValue string
	}{
		{
			name: "aws sensitive auth fields",
			path: "destinations/aws-sm/prod",
			fields: map[string]interface{}{
				awssecretsmanager.ConfigKeyRegion:     "eu-central-2",
				awssecretsmanager.ConfigKeyAuthMode:   awssecretsmanager.AuthModeAssumeRole,
				awssecretsmanager.ConfigKeyRoleARN:    "arn:aws:iam::123456789012:role/openbao-sync",
				awssecretsmanager.ConfigKeyExternalID: redactionCanary + "-external-id",
			},
			sensitiveValues: map[string]string{
				awssecretsmanager.ConfigKeyExternalID: redactionCanary + "-external-id",
			},
			redactedKeyOrder:  []string{awssecretsmanager.ConfigKeyExternalID},
			publicConfigKey:   awssecretsmanager.ConfigKeyRegion,
			publicConfigValue: "eu-central-2",
		},
		{
			name: "gitlab token",
			path: "destinations/gitlab/prod",
			fields: map[string]interface{}{
				gitlab.ConfigKeyBaseURL:   "https://gitlab.example.com",
				gitlab.ConfigKeyProjectID: "platform/app",
				gitlab.ConfigKeyToken:     redactionCanary + "-gitlab-token",
			},
			sensitiveValues: map[string]string{
				gitlab.ConfigKeyToken: redactionCanary + "-gitlab-token",
			},
			redactedKeyOrder:  []string{gitlab.ConfigKeyToken},
			publicConfigKey:   gitlab.ConfigKeyProjectID,
			publicConfigValue: "platform/app",
		},
		{
			name: "kubernetes token",
			path: "destinations/k8s/prod",
			fields: map[string]interface{}{
				kubernetessecrets.ConfigKeyNamespace: "apps",
				kubernetessecrets.ConfigKeyAuthMode:  kubernetessecrets.AuthModeToken,
				kubernetessecrets.ConfigKeyAPIServer: "https://kubernetes.example.com",
				kubernetessecrets.ConfigKeyToken:     redactionCanary + "-k8s-token",
			},
			sensitiveValues: map[string]string{
				kubernetessecrets.ConfigKeyToken: redactionCanary + "-k8s-token",
			},
			redactedKeyOrder:  []string{kubernetessecrets.ConfigKeyToken},
			publicConfigKey:   kubernetessecrets.ConfigKeyAPIServer,
			publicConfigValue: "https://kubernetes.example.com",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := env.update(testCase.path, testCase.fields)
			assertNilOrNoErrorResponse(t, resp)

			readResp := env.read(testCase.path)
			assertNoErrorResponse(t, readResp)
			assertDoesNotContainRedactionCanary(t, readResp.Data)

			config := mapFromResponse(t, readResp.Data, "config")
			if got := config[testCase.publicConfigKey]; got != testCase.publicConfigValue {
				t.Fatalf("%s config = %v, want %s", testCase.publicConfigKey, got, testCase.publicConfigValue)
			}
			for key := range testCase.sensitiveValues {
				if _, ok := config[key]; ok {
					t.Fatalf("public config contains sensitive key %q: %#v", key, config)
				}
			}

			sensitiveConfig := mapFromResponse(t, readResp.Data, "sensitive_config")
			if got := sensitiveConfig["redacted"]; got != true {
				t.Fatalf("sensitive_config.redacted = %v, want true", got)
			}
			if got := sensitiveConfig["configured"]; got != true {
				t.Fatalf("sensitive_config.configured = %v, want true", got)
			}
			assertStringSlice(t, stringSliceFromMap(t, sensitiveConfig, "keys"), testCase.redactedKeyOrder)
			assertSensitiveConfigStorageBoundary(t, env.storage, testCase.path, testCase.sensitiveValues)
		})
	}
}

func TestSecurityBoundarySourcePayloadDoesNotLeakThroughOperationalResponses(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret(redactionCanary)
	env.createFakeDestination("default")
	env.markAppDBSyncable()

	planResp := env.planDefaultFakeAssociation("prod/app/db")
	assertNoErrorResponse(t, planResp)
	assertDoesNotContainRedactionCanary(t, planResp.Data)

	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncGranularitySecretPath,
		"format":           defaultAssociationFormat,
		"delete_mode":      deleteModeDelete,
	})
	assertNoErrorResponse(t, associationResp)
	assertDoesNotContainRedactionCanary(t, associationResp.Data)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertDoesNotContainRedactionCanary(t, queueResp.Data)

	operationResp := env.read("queue/" + operationID)
	assertNoErrorResponse(t, operationResp)
	assertDoesNotContainRedactionCanary(t, operationResp.Data)

	statusPendingResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusPendingResp)
	assertDoesNotContainRedactionCanary(t, statusPendingResp.Data)

	env.acknowledgeRestoreGuard()
	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 10,
	})
	assertNoErrorResponse(t, drainResp)
	assertDoesNotContainRedactionCanary(t, drainResp.Data)

	statusSyncedResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusSyncedResp)
	assertDoesNotContainRedactionCanary(t, statusSyncedResp.Data)

	reconcilePlanResp := env.read("reconcile/app/db/plan")
	assertNoErrorResponse(t, reconcilePlanResp)
	assertDoesNotContainRedactionCanary(t, reconcilePlanResp.Data)

	reconcileApplyResp := env.update("reconcile/app/db")
	assertNoErrorResponse(t, reconcileApplyResp)
	assertDoesNotContainRedactionCanary(t, reconcileApplyResp.Data)
}

func assertSensitiveConfigStorageBoundary(
	t *testing.T,
	storage logical.Storage,
	destinationPath string,
	wantSensitive map[string]string,
) {
	t.Helper()
	parts := strings.Split(destinationPath, "/")
	if len(parts) != 3 || parts[0] != "destinations" {
		t.Fatalf("destination path = %q, want destinations/<type>/<name>", destinationPath)
	}
	record, err := getDestination(context.Background(), storage, parts[1], parts[2])
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if record == nil {
		t.Fatal("destination record must exist")
	}
	for key := range wantSensitive {
		if _, ok := record.Config[key]; ok {
			t.Fatalf("destination record contains sensitive key %q: %#v", key, record.Config)
		}
	}
	sensitiveRecord, err := getDestinationSensitiveConfig(context.Background(), storage, parts[1], parts[2])
	if err != nil {
		t.Fatalf("read sensitive destination config: %v", err)
	}
	if sensitiveRecord == nil {
		t.Fatal("sensitive destination record must exist")
	}
	for key, want := range wantSensitive {
		if got := sensitiveRecord.Config[key]; got != want {
			t.Fatalf("sensitive config %s = %q, want %q", key, got, want)
		}
	}
}

func assertNilOrNoErrorResponse(t *testing.T, resp *logical.Response) {
	t.Helper()
	if resp == nil {
		return
	}
	if resp.IsError() {
		t.Fatalf("unexpected error response: %v", resp.Error())
	}
}

func assertDoesNotContainRedactionCanary(t *testing.T, value interface{}) {
	t.Helper()
	if strings.Contains(fmt.Sprint(value), redactionCanary) {
		t.Fatalf("response leaks canary %q: %#v", redactionCanary, value)
	}
}

func mapFromResponse(t *testing.T, data map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	value, ok := data[key].(map[string]interface{})
	if !ok {
		t.Fatalf("%s = %T, want map[string]interface{}", key, data[key])
	}
	return value
}

func stringSliceFromMap(t *testing.T, data map[string]interface{}, key string) []string {
	t.Helper()
	value, ok := data[key].([]string)
	if !ok {
		t.Fatalf("%s = %T, want []string", key, data[key])
	}
	return value
}
