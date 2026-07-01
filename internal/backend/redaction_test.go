package backend

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-secret-sync/internal/providers/gitlab"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const redactionCanary = "secret-canary-redaction-boundary"

func TestSecurityBoundaryDestinationReadRedactsSensitiveConfig(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

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
				awssecretsmanager.ConfigKeyRegion:          "eu-central-2",
				awssecretsmanager.ConfigKeyAuthMode:        awssecretsmanager.AuthModeAssumeRole,
				awssecretsmanager.ConfigKeyRoleARN:         "arn:aws:iam::123456789012:role/openbao-sync",
				awssecretsmanager.ConfigKeyExternalID:      redactionCanary + "-external-id",
				awssecretsmanager.ConfigKeyAccessKeyID:     redactionCanary + "-access-key-id",
				awssecretsmanager.ConfigKeySecretAccessKey: redactionCanary + "-secret-access-key",
				awssecretsmanager.ConfigKeySessionToken:    redactionCanary + "-session-token",
			},
			sensitiveValues: map[string]string{
				awssecretsmanager.ConfigKeyExternalID:      redactionCanary + "-external-id",
				awssecretsmanager.ConfigKeyAccessKeyID:     redactionCanary + "-access-key-id",
				awssecretsmanager.ConfigKeySecretAccessKey: redactionCanary + "-secret-access-key",
				awssecretsmanager.ConfigKeySessionToken:    redactionCanary + "-session-token",
			},
			redactedKeyOrder: []string{
				awssecretsmanager.ConfigKeyAccessKeyID,
				awssecretsmanager.ConfigKeyExternalID,
				awssecretsmanager.ConfigKeySecretAccessKey,
				awssecretsmanager.ConfigKeySessionToken,
			},
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
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := handleRequest(t, b, storage, logical.UpdateOperation, testCase.path, testCase.fields)
			assertNilOrNoErrorResponse(t, resp)

			readResp := handleRequest(t, b, storage, logical.ReadOperation, testCase.path, nil)
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
			assertSensitiveConfigStorageBoundary(t, storage, testCase.path, testCase.sensitiveValues)
		})
	}
}

func TestSecurityBoundarySourcePayloadDoesNotLeakThroughOperationalResponses(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, redactionCanary)
	createFakeDestination(t, b, storage, "default")
	markAppDBSyncable(t, b, storage)

	planResp := planDefaultFakeAssociation(t, b, storage, "prod/app/db")
	assertNoErrorResponse(t, planResp)
	assertDoesNotContainRedactionCanary(t, planResp.Data)

	associationResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
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

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	assertDoesNotContainRedactionCanary(t, queueResp.Data)

	operationResp := handleRequest(t, b, storage, logical.ReadOperation, "queue/"+operationID, nil)
	assertNoErrorResponse(t, operationResp)
	assertDoesNotContainRedactionCanary(t, operationResp.Data)

	statusPendingResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusPendingResp)
	assertDoesNotContainRedactionCanary(t, statusPendingResp.Data)

	acknowledgeRestoreGuard(t, b, storage)
	drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 10,
	})
	assertNoErrorResponse(t, drainResp)
	assertDoesNotContainRedactionCanary(t, drainResp.Data)

	statusSyncedResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusSyncedResp)
	assertDoesNotContainRedactionCanary(t, statusSyncedResp.Data)

	reconcilePlanResp := handleRequest(t, b, storage, logical.ReadOperation, "reconcile/app/db/plan", nil)
	assertNoErrorResponse(t, reconcilePlanResp)
	assertDoesNotContainRedactionCanary(t, reconcilePlanResp.Data)

	reconcileApplyResp := handleRequest(t, b, storage, logical.UpdateOperation, "reconcile/app/db", nil)
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
