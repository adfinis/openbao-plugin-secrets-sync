//go:build e2e

package localstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/smithy-go"
	"github.com/openbao/openbao/api/v2"
)

const (
	pluginName       = "openbao-plugin-secrets-sync"
	mountPath        = "secret-sync"
	rootToken        = "root"
	awsRegion        = "us-east-1"
	localstackInBao  = "http://localstack:4566"
	testPollInterval = 500 * time.Millisecond
	testTimeout      = 30 * time.Second
)

func TestOpenBaoPluginSyncsToLocalStackSecretsManager(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	baoClient := newOpenBaoClient(t)
	waitForOpenBao(t, ctx, baoClient)
	switch pluginRegistrationMode() {
	case "manual":
		registerPlugin(t, baoClient)
	case "oci":
	default:
		t.Fatalf("unsupported E2E_PLUGIN_REGISTRATION %q", pluginRegistrationMode())
	}
	mountPlugin(t, baoClient)

	awsClient := newSecretsManagerClient(t, ctx)
	remoteName := fmt.Sprintf("openbao-plugin-secrets-sync-e2e/%d", time.Now().UnixNano())
	t.Cleanup(func() {
		forceDeleteSecret(ctx, awsClient, remoteName)
	})

	write(t, baoClient, mountPath+"/destinations/aws-sm/prod", map[string]interface{}{
		awssecretsmanager.ConfigKeyRegion:         awsRegion,
		awssecretsmanager.ConfigKeyEndpointURL:    localstackInBao,
		awssecretsmanager.ConfigKeyEndpointPolicy: awssecretsmanager.EndpointPolicyLocal,
		awssecretsmanager.ConfigKeyAuthMode:       awssecretsmanager.AuthModeDefault,
	})
	acknowledgeRestoreGuard(t, baoClient)
	write(t, baoClient, mountPath+"/metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"syncable": "true",
		},
	})
	writeSource(t, baoClient, "initial")

	plan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := plan.Data["action"]; got != "create" {
		t.Fatalf("plan action = %v, want create", got)
	}

	association := write(t, baoClient, mountPath+"/associations/app/db", associationRequest(remoteName))
	if ids := stringSlice(t, association.Data["sync_operation_ids"]); len(ids) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", ids)
	}
	drainQueue(t, baoClient, 1)
	assertRemotePayload(t, ctx, awsClient, remoteName, "initial")
	assertRemoteTags(t, ctx, awsClient, remoteName, map[string]string{
		"openbao-sync":        "true",
		"openbao-sync-path":   "app/db",
		"openbao-sync-object": "secret-path",
	})
	assertStatus(t, baoClient, "SYNCED")
	assertReconcilePlan(t, baoClient, "SYNCED")
	assertReconcileApply(t, baoClient, "SYNCED")

	noOpPlan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := noOpPlan.Data["action"]; got != "noop" {
		t.Fatalf("noop plan action = %v, want noop", got)
	}

	writeSource(t, baoClient, "updated")
	drainQueue(t, baoClient, 1)
	assertRemotePayload(t, ctx, awsClient, remoteName, "updated")

	deleteSecret := deletePath(t, baoClient, mountPath+"/data/app/db")
	if ids := metadataOperationIDs(t, deleteSecret); len(ids) != 1 {
		t.Fatalf("delete sync_operation_ids = %v, want one operation", ids)
	}
	drainQueue(t, baoClient, 1)
	assertRemoteDeleteScheduled(t, ctx, awsClient, remoteName)
	assertStatus(t, baoClient, "REMOTE_MISSING")

	writeSource(t, baoClient, "recovered")
	recoveryPlan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := recoveryPlan.Data["action"]; got != "update" {
		t.Fatalf("recovery plan action = %v, want update", got)
	}
	if got := recoveryPlan.Data["message"]; got != "aws-sm secret is scheduled for deletion and will be restored before upsert" {
		t.Fatalf("recovery plan message = %v, want restore message", got)
	}
	drainQueue(t, baoClient, 1)
	assertRemotePayload(t, ctx, awsClient, remoteName, "recovered")
	assertStatus(t, baoClient, "SYNCED")
	assertReconcilePlan(t, baoClient, "SYNCED")
}

func newOpenBaoClient(t *testing.T) *api.Client {
	t.Helper()
	config := api.DefaultConfig()
	config.Address = env("E2E_OPENBAO_ADDR", "http://127.0.0.1:18200")
	client, err := api.NewClient(config)
	if err != nil {
		t.Fatalf("create OpenBao client: %v", err)
	}
	client.SetToken(rootToken)
	return client
}

func waitForOpenBao(t *testing.T, ctx context.Context, client *api.Client) {
	t.Helper()
	waitFor(t, ctx, func() error {
		health, err := client.Sys().Health()
		if err != nil {
			return err
		}
		if !health.Initialized || health.Sealed {
			return fmt.Errorf("openbao not ready: initialized=%v sealed=%v", health.Initialized, health.Sealed)
		}
		return nil
	})
}

func registerPlugin(t *testing.T, client *api.Client) {
	t.Helper()
	pluginPath := env("E2E_PLUGIN_PATH", "../../../bin/e2e/"+pluginName)
	absPluginPath, err := filepath.Abs(pluginPath)
	if err != nil {
		t.Fatalf("resolve plugin path: %v", err)
	}
	pluginBytes, err := os.ReadFile(absPluginPath)
	if err != nil {
		t.Fatalf("read plugin binary %s: %v", absPluginPath, err)
	}
	sum := sha256.Sum256(pluginBytes)
	if err := client.Sys().RegisterPlugin(&api.RegisterPluginInput{
		Name:    pluginName,
		Type:    api.PluginTypeSecrets,
		Command: pluginName,
		SHA256:  hex.EncodeToString(sum[:]),
		Version: pluginVersion(),
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
}

func mountPlugin(t *testing.T, client *api.Client) {
	t.Helper()
	_ = client.Sys().Unmount(mountPath)
	if err := client.Sys().Mount(mountPath, &api.MountInput{
		Type:       "plugin",
		PluginName: pluginName,
		Config: api.MountConfigInput{
			PluginVersion: pluginVersion(),
		},
	}); err != nil {
		t.Fatalf("mount plugin: %v", err)
	}
}

func pluginVersion() string {
	return env("E2E_PLUGIN_VERSION", "v0.0.0-dev")
}

func pluginRegistrationMode() string {
	return env("E2E_PLUGIN_REGISTRATION", "manual")
}

func newSecretsManagerClient(t *testing.T, ctx context.Context) *secretsmanager.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(awsRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	return secretsmanager.NewFromConfig(cfg, func(options *secretsmanager.Options) {
		options.BaseEndpoint = aws.String(env("E2E_LOCALSTACK_ENDPOINT", "http://127.0.0.1:4566"))
	})
}

func associationRequest(remoteName string) map[string]interface{} {
	return map[string]interface{}{
		"destination_type": "aws-sm",
		"destination_name": "prod",
		"resolved_name":    remoteName,
		"granularity":      "secret-path",
		"format":           "json",
		"delete_mode":      "delete",
	}
}

func writeSource(t *testing.T, client *api.Client, password string) {
	t.Helper()
	write(t, client, mountPath+"/data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": password,
		},
	})
}

func acknowledgeRestoreGuard(t *testing.T, client *api.Client) {
	t.Helper()
	write(t, client, mountPath+"/config/restore-guard/acknowledge", map[string]interface{}{})
}

func write(t *testing.T, client *api.Client, path string, data map[string]interface{}) *api.Secret {
	t.Helper()
	secret, err := client.Logical().Write(path, data)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if secret == nil {
		return &api.Secret{Data: map[string]interface{}{}}
	}
	return secret
}

func deletePath(t *testing.T, client *api.Client, path string) *api.Secret {
	t.Helper()
	secret, err := client.Logical().Delete(path)
	if err != nil {
		t.Fatalf("delete %s: %v", path, err)
	}
	if secret == nil {
		return &api.Secret{Data: map[string]interface{}{}}
	}
	return secret
}

func drainQueue(t *testing.T, client *api.Client, expectedProcessed int) {
	t.Helper()
	secret := write(t, client, mountPath+"/queue/drain", map[string]interface{}{
		"max_operations": expectedProcessed,
	})
	if got := intFromSecret(t, secret.Data["processed"]); got != expectedProcessed {
		t.Fatalf("queue drain processed = %d, want %d", got, expectedProcessed)
	}
}

func assertRemotePayload(
	t *testing.T,
	ctx context.Context,
	client *secretsmanager.Client,
	secretName string,
	expectedPassword string,
) {
	t.Helper()
	var payload map[string]string
	waitFor(t, ctx, func() error {
		result, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
			SecretId: aws.String(secretName),
		})
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(aws.ToString(result.SecretString)), &payload); err != nil {
			return err
		}
		if payload["password"] != expectedPassword {
			return fmt.Errorf("password = %q, want %q", payload["password"], expectedPassword)
		}
		return nil
	})
}

func assertRemoteTags(
	t *testing.T,
	ctx context.Context,
	client *secretsmanager.Client,
	secretName string,
	expectedTags map[string]string,
) {
	t.Helper()
	result, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(secretName),
	})
	if err != nil {
		t.Fatalf("describe remote secret: %v", err)
	}
	tags := tagsToMap(result.Tags)
	for key, expected := range expectedTags {
		if got := tags[key]; got != expected {
			t.Fatalf("tag %s = %q, want %q", key, got, expected)
		}
	}
}

func assertRemoteDeleteScheduled(
	t *testing.T,
	ctx context.Context,
	client *secretsmanager.Client,
	secretName string,
) {
	t.Helper()
	waitFor(t, ctx, func() error {
		result, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
			SecretId: aws.String(secretName),
		})
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if result.DeletedDate == nil {
			return errors.New("remote secret is not scheduled for deletion")
		}
		return nil
	})
}

func assertStatus(t *testing.T, client *api.Client, expectedState string) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/status/app/db")
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if secret == nil {
		t.Fatal("status response is nil")
	}
	if got := secret.Data["state"]; got != expectedState {
		t.Fatalf("status state = %v, want %s", got, expectedState)
	}
}

func assertReconcilePlan(t *testing.T, client *api.Client, expectedState string) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/reconcile/app/db/plan")
	if err != nil {
		t.Fatalf("read reconcile plan: %v", err)
	}
	assertReconcileState(t, secret, expectedState)
}

func assertReconcileApply(t *testing.T, client *api.Client, expectedState string) {
	t.Helper()
	secret := write(t, client, mountPath+"/reconcile/app/db", map[string]interface{}{})
	assertReconcileState(t, secret, expectedState)
}

func assertReconcileState(t *testing.T, secret *api.Secret, expectedState string) {
	t.Helper()
	if secret == nil {
		t.Fatal("reconcile response is nil")
	}
	if got := secret.Data["state"]; got != expectedState {
		t.Fatalf("reconcile state = %v, want %s", got, expectedState)
	}
}

func forceDeleteSecret(ctx context.Context, client *secretsmanager.Client, secretName string) {
	_, _ = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(secretName),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
}

func tagsToMap(tags []smtypes.Tag) map[string]string {
	result := make(map[string]string, len(tags))
	for _, tag := range tags {
		result[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return result
}

func metadataOperationIDs(t *testing.T, secret *api.Secret) []string {
	t.Helper()
	metadata, ok := secret.Data["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata = %T, want map", secret.Data["metadata"])
	}
	return stringSlice(t, metadata["sync_operation_ids"])
}

func stringSlice(t *testing.T, raw interface{}) []string {
	t.Helper()
	values, ok := raw.([]interface{})
	if ok {
		result := make([]string, 0, len(values))
		for _, value := range values {
			result = append(result, fmt.Sprint(value))
		}
		return result
	}
	strings, ok := raw.([]string)
	if !ok {
		t.Fatalf("value = %T, want string slice", raw)
	}
	return strings
}

func intFromSecret(t *testing.T, raw interface{}) int {
	t.Helper()
	switch value := raw.(type) {
	case int:
		return value
	case json.Number:
		result, err := value.Int64()
		if err != nil {
			t.Fatalf("parse json number: %v", err)
		}
		return int(result)
	case float64:
		return int(value)
	default:
		t.Fatalf("value = %T, want number", raw)
		return 0
	}
}

func waitFor(t *testing.T, ctx context.Context, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled while waiting: %v", ctx.Err())
		case <-time.After(testPollInterval):
		}
	}
	t.Fatalf("condition not met after %s: %v", testTimeout, lastErr)
}

func isNotFound(err error) bool {
	var apiError smithy.APIError
	return errors.As(err, &apiError) && apiError.ErrorCode() == "ResourceNotFoundException"
}

func env(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
