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
	testTimeout      = 90 * time.Second
)

func TestOpenBaoPluginSyncsToLocalStackSecretsManager(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
		awssecretsmanager.ConfigKeyRegion:              awsRegion,
		awssecretsmanager.ConfigKeyEndpointURL:         localstackInBao,
		awssecretsmanager.ConfigKeyEndpointPolicy:      awssecretsmanager.EndpointPolicyLocal,
		awssecretsmanager.ConfigKeyAuthMode:            awssecretsmanager.AuthModeDefault,
		awssecretsmanager.ConfigKeyValueDriftDetection: "true",
	})
	assertFreshConfigDefaults(t, baoClient)
	writeSource(t, baoClient, "initial")
	assertSourceReadyWithoutOptIn(t, baoClient)

	plan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := plan.Data["action"]; got != "create" {
		t.Fatalf("plan action = %v, want create", got)
	}

	association := write(t, baoClient, mountPath+"/associations/app/db", associationRequest(remoteName))
	if ids := stringSlice(t, association.Data["sync_operation_ids"]); len(ids) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", ids)
	}
	assertRemotePayload(t, ctx, awsClient, remoteName, "initial")
	assertRemoteTags(t, ctx, awsClient, remoteName, map[string]string{
		"openbao-sync":        "true",
		"openbao-sync-path":   "app/db",
		"openbao-sync-object": "secret-path",
	})
	assertStatus(t, baoClient, "SYNCED")
	assertReconcilePlan(t, baoClient, "SYNCED")

	noOpPlan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := noOpPlan.Data["action"]; got != "noop" {
		t.Fatalf("noop plan action = %v, want noop", got)
	}

	putRemotePayload(t, ctx, awsClient, remoteName, "drifted")
	driftPlan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := driftPlan.Data["action"]; got != "update" {
		t.Fatalf("drift plan action = %v, want update", got)
	}
	enableBackgroundRepair(t, baoClient)
	assertRemotePayload(t, ctx, awsClient, remoteName, "initial")
	assertBackgroundRepairStatus(t, baoClient, "value")
	disableBackgroundDrift(t, baoClient)
	assertReconcileApply(t, baoClient, "SYNCED")

	writeSource(t, baoClient, "updated")
	assertRemotePayload(t, ctx, awsClient, remoteName, "updated")

	deleteSecret := deletePath(t, baoClient, mountPath+"/data/app/db")
	if ids := metadataOperationIDs(t, deleteSecret); len(ids) != 1 {
		t.Fatalf("delete sync_operation_ids = %v, want one operation", ids)
	}
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
		"destination":   "aws-sm/prod",
		"resolved_name": remoteName,
		"granularity":   "secret-path",
		"format":        "json",
		"delete_mode":   "delete",
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

func enableBackgroundRepair(t *testing.T, client *api.Client) {
	t.Helper()
	write(t, client, mountPath+"/config", map[string]interface{}{
		"drift_repair":             "repair",
		"drift_reconcile_interval": "1m",
		"drift_reconcile_batch":    4,
	})
}

func disableBackgroundDrift(t *testing.T, client *api.Client) {
	t.Helper()
	write(t, client, mountPath+"/config", map[string]interface{}{
		"drift_repair": "off",
	})
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

func putRemotePayload(
	t *testing.T,
	ctx context.Context,
	client *secretsmanager.Client,
	secretName string,
	password string,
) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		t.Fatalf("marshal remote payload: %v", err)
	}
	_, err = client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(secretName),
		SecretString: aws.String(string(payload)),
	})
	if err != nil {
		t.Fatalf("put remote payload: %v", err)
	}
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

func assertFreshConfigDefaults(t *testing.T, client *api.Client) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/config")
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if secret == nil {
		t.Fatal("config response is nil")
	}
	if got := secret.Data["restore_guard"]; got != false {
		t.Fatalf("restore_guard = %v, want false", got)
	}
	if got := secret.Data["require_source_opt_in"]; got != false {
		t.Fatalf("require_source_opt_in = %v, want false", got)
	}
	if got := secret.Data["restore_guard_acknowledged_time"]; got == "" {
		t.Fatal("restore_guard_acknowledged_time must be set")
	}
}

func assertSourceReadyWithoutOptIn(t *testing.T, client *api.Client) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/sources/app/db/check")
	if err != nil {
		t.Fatalf("read source check: %v", err)
	}
	if secret == nil {
		t.Fatal("source check response is nil")
	}
	if got := secret.Data["source_opt_in_required"]; got != false {
		t.Fatalf("source_opt_in_required = %v, want false", got)
	}
	if got := secret.Data["syncable"]; got != false {
		t.Fatalf("syncable = %v, want false", got)
	}
	if got := secret.Data["ready"]; got != true {
		t.Fatalf("ready = %v, want true", got)
	}
	if blockers := stringSlice(t, secret.Data["blockers"]); len(blockers) != 0 {
		t.Fatalf("source blockers = %v, want none", blockers)
	}
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

func assertBackgroundRepairStatus(t *testing.T, client *api.Client, expectedVerification string) {
	t.Helper()
	secret := readStatus(t, client)
	if got := secret.Data["state"]; got != "SYNCED" {
		t.Fatalf("status state = %v, want SYNCED", got)
	}
	object := objectByID(t, objectsFromSecret(t, secret.Data["objects"]), "secret-path")
	if got := object["state"]; got != "SYNCED" {
		t.Fatalf("status object state = %v, want SYNCED", got)
	}
	if got := object["verification"]; got != expectedVerification {
		t.Fatalf("status verification = %v, want %s", got, expectedVerification)
	}
	if got := object["last_drift_detected_time"]; got == "" {
		t.Fatal("last_drift_detected_time must be set after background repair")
	}
	if got := object["last_repair_time"]; got == "" {
		t.Fatal("last_repair_time must be set after background repair")
	}
	if got := intFromSecret(t, object["repair_count"]); got < 1 {
		t.Fatalf("repair_count = %d, want at least 1", got)
	}
}

func readStatus(t *testing.T, client *api.Client) *api.Secret {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/status/app/db")
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if secret == nil {
		t.Fatal("status response is nil")
	}
	return secret
}

func objectsFromSecret(t *testing.T, raw interface{}) []map[string]interface{} {
	t.Helper()
	if values, ok := raw.([]interface{}); ok {
		objects := make([]map[string]interface{}, 0, len(values))
		for _, value := range values {
			object, ok := value.(map[string]interface{})
			if !ok {
				t.Fatalf("object = %T, want map[string]interface{}", value)
			}
			objects = append(objects, object)
		}
		return objects
	}
	objects, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("objects = %T, want []map[string]interface{}", raw)
	}
	return objects
}

func objectByID(t *testing.T, objects []map[string]interface{}, objectID string) map[string]interface{} {
	t.Helper()
	for _, object := range objects {
		if object["object_id"] == objectID {
			return object
		}
	}
	t.Fatalf("object %q missing in %#v", objectID, objects)
	return nil
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

func associationIDFromSecret(t *testing.T, secret *api.Secret) string {
	t.Helper()
	association, ok := secret.Data["association"].(map[string]interface{})
	if !ok {
		t.Fatalf("association = %T, want map", secret.Data["association"])
	}
	id, ok := association["id"].(string)
	if !ok || id == "" {
		t.Fatalf("association id = %v, want non-empty string", association["id"])
	}
	return id
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
