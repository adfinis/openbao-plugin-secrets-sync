//go:build e2e

package resilience

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"
	"github.com/openbao/openbao/api/v2"
)

const (
	pluginName       = "openbao-plugin-secrets-sync"
	mountPath        = "secret-sync"
	awsRegion        = "us-east-1"
	localstackInBao  = "http://localstack:4566"
	testPollInterval = 500 * time.Millisecond
	testTimeout      = 90 * time.Second
	raftNode0ID      = "openbao-node0"
	raftNode1ID      = "openbao-node1"
	raftNode2ID      = "openbao-node2"
)

func TestOpenBaoLifecyclePreservesSecretSyncState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()

	baoClient := newOpenBaoClient(t, "")
	rootToken := initializeOpenBao(t, ctx, baoClient)
	baoClient.SetToken(rootToken)
	waitForOpenBaoReady(t, ctx, baoClient)
	standbyClient := newOpenBaoStandbyClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, standbyClient)
	standby2Client := newOpenBaoStandby2Client(t, rootToken)
	waitForOpenBaoReady(t, ctx, standby2Client)
	waitForRaftPeers(t, ctx, baoClient, raftNode0ID, raftNode1ID, raftNode2ID)

	registerPlugin(t, baoClient)
	mountPlugin(t, baoClient)

	awsClient := newSecretsManagerClient(t, ctx)
	remoteName := fmt.Sprintf("openbao-plugin-secrets-sync-resilience/%d", time.Now().UnixNano())
	t.Cleanup(func() {
		forceDeleteSecret(ctx, awsClient, remoteName)
	})

	assertConfig(t, baoClient, false, false, false)
	disableEventDispatch(t, baoClient)
	write(t, baoClient, mountPath+"/config", map[string]interface{}{
		"disabled": true,
	})
	assertConfig(t, baoClient, true, false, false)

	write(t, baoClient, mountPath+"/destinations/aws-sm/prod", map[string]interface{}{
		awssecretsmanager.ConfigKeyRegion:              awsRegion,
		awssecretsmanager.ConfigKeyEndpointURL:         localstackInBao,
		awssecretsmanager.ConfigKeyEndpointPolicy:      awssecretsmanager.EndpointPolicyLocal,
		awssecretsmanager.ConfigKeyAuthMode:            awssecretsmanager.AuthModeDefault,
		awssecretsmanager.ConfigKeyValueDriftDetection: "true",
	})
	writeSource(t, baoClient, "initial")

	association := write(t, baoClient, mountPath+"/associations/app/db", associationRequest(remoteName))
	if ids := stringSlice(t, association.Data["sync_operation_ids"]); len(ids) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", ids)
	}
	assertQueue(t, baoClient, 1, 0)
	assertStatus(t, baoClient, "PENDING")
	assertRemoteMissing(t, ctx, awsClient, remoteName)

	waitForRaftLeader(t, ctx, baoClient, raftNode0ID)
	stopOpenBao(t, ctx)
	standbyClient = newOpenBaoStandbyClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, standbyClient)
	assertConfig(t, standbyClient, true, false, false)
	assertQueue(t, standbyClient, 1, 0)
	assertStatus(t, standbyClient, "PENDING")
	assertRemoteMissing(t, ctx, awsClient, remoteName)
	startOpenBao(t, ctx)
	baoClient = newOpenBaoClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, baoClient)
	standby2Client = newOpenBaoStandby2Client(t, rootToken)
	waitForOpenBaoReady(t, ctx, standby2Client)
	waitForRaftPeers(t, ctx, baoClient, raftNode0ID, raftNode1ID, raftNode2ID)
	assertConfig(t, baoClient, true, false, false)
	assertQueue(t, baoClient, 1, 0)
	assertStatus(t, baoClient, "PENDING")

	write(t, baoClient, mountPath+"/config", map[string]interface{}{
		"disabled": false,
	})
	drainQueue(t, baoClient, 1)
	assertQueue(t, baoClient, 0, 0)
	assertRemotePayload(t, ctx, awsClient, remoteName, "initial")
	assertStatus(t, baoClient, "SYNCED")
	assertStatus(t, standbyClient, "SYNCED")

	restartOpenBao(t, ctx)
	baoClient = newOpenBaoClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, baoClient)
	standbyClient = newOpenBaoStandbyClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, standbyClient)
	waitForRaftPeers(t, ctx, baoClient, raftNode0ID, raftNode1ID, raftNode2ID)
	assertConfig(t, baoClient, false, false, false)
	assertQueue(t, baoClient, 0, 0)
	assertStatus(t, baoClient, "SYNCED")
	assertRemotePayload(t, ctx, awsClient, remoteName, "initial")

	restartOpenBaoStandby(t, ctx)
	standbyClient = newOpenBaoStandbyClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, standbyClient)
	waitForRaftPeers(t, ctx, baoClient, raftNode0ID, raftNode1ID, raftNode2ID)
	assertStatus(t, standbyClient, "SYNCED")

	write(t, baoClient, mountPath+"/config", map[string]interface{}{
		"disabled": true,
	})
	writeSource(t, baoClient, "after-seal")
	assertConfig(t, baoClient, true, false, false)
	assertQueue(t, baoClient, 1, 0)
	assertStatus(t, baoClient, "PENDING")
	assertRemotePayload(t, ctx, awsClient, remoteName, "initial")

	sealOpenBao(t, ctx, baoClient)
	waitForOpenBaoSealed(t, ctx, baoClient)

	restartOpenBao(t, ctx)
	baoClient = newOpenBaoClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, baoClient)
	standbyClient = newOpenBaoStandbyClient(t, rootToken)
	waitForOpenBaoReady(t, ctx, standbyClient)
	waitForRaftPeers(t, ctx, baoClient, raftNode0ID, raftNode1ID, raftNode2ID)
	assertConfig(t, baoClient, true, false, false)
	assertQueue(t, baoClient, 1, 0)
	assertStatus(t, baoClient, "PENDING")

	write(t, baoClient, mountPath+"/config", map[string]interface{}{
		"disabled": false,
	})
	drainQueue(t, baoClient, 1)
	assertQueue(t, baoClient, 0, 0)
	assertRemotePayload(t, ctx, awsClient, remoteName, "after-seal")
	assertStatus(t, baoClient, "SYNCED")
	assertStatus(t, standbyClient, "SYNCED")

	write(t, baoClient, mountPath+"/config", map[string]interface{}{
		"restore_guard":            true,
		"drift_repair":             "repair",
		"drift_reconcile_interval": "1m",
		"drift_reconcile_batch":    4,
	})
	putRemotePayload(t, ctx, awsClient, remoteName, "guard-drift")
	assertStatusEventually(t, ctx, baoClient, "DRIFTED")
	assertQueue(t, baoClient, 0, 0)
	assertRemotePayload(t, ctx, awsClient, remoteName, "guard-drift")
}

func initializeOpenBao(t *testing.T, ctx context.Context, client *api.Client) string {
	t.Helper()

	if existingToken := os.Getenv("E2E_RESILIENCE_ROOT_TOKEN"); existingToken != "" {
		return existingToken
	}

	var initialized bool
	waitFor(t, ctx, func() error {
		var err error
		initialized, err = client.Sys().InitStatusWithContext(ctx)
		return err
	})
	if initialized {
		t.Fatal("OpenBao is already initialized; set E2E_RESILIENCE_ROOT_TOKEN or start with a fresh resilience stack")
	}

	initResponse, err := client.Sys().InitWithContext(ctx, &api.InitRequest{
		RecoveryShares:    1,
		RecoveryThreshold: 1,
	})
	if err != nil {
		t.Fatalf("initialize OpenBao: %v", err)
	}
	if initResponse == nil || initResponse.RootToken == "" {
		t.Fatal("initialize OpenBao returned no root token")
	}
	return initResponse.RootToken
}

func newOpenBaoClient(t *testing.T, token string) *api.Client {
	t.Helper()
	return newOpenBaoClientAt(t, env("E2E_RESILIENCE_OPENBAO_ADDR", "http://127.0.0.1:18205"), token)
}

func newOpenBaoStandbyClient(t *testing.T, token string) *api.Client {
	t.Helper()
	return newOpenBaoClientAt(t, env("E2E_RESILIENCE_OPENBAO_STANDBY_ADDR", "http://127.0.0.1:18206"), token)
}

func newOpenBaoStandby2Client(t *testing.T, token string) *api.Client {
	t.Helper()
	return newOpenBaoClientAt(t, env("E2E_RESILIENCE_OPENBAO_STANDBY_2_ADDR", "http://127.0.0.1:18207"), token)
}

func newOpenBaoClientAt(t *testing.T, address string, token string) *api.Client {
	t.Helper()
	config := api.DefaultConfig()
	config.Address = address
	client, err := api.NewClient(config)
	if err != nil {
		t.Fatalf("create OpenBao client: %v", err)
	}
	if token != "" {
		client.SetToken(token)
	}
	return client
}

func waitForOpenBaoReady(t *testing.T, ctx context.Context, client *api.Client) {
	t.Helper()
	waitFor(t, ctx, func() error {
		status, err := client.Sys().SealStatusWithContext(ctx)
		if err != nil {
			return err
		}
		if !status.Initialized || status.Sealed {
			return fmt.Errorf("openbao not ready: initialized=%v sealed=%v", status.Initialized, status.Sealed)
		}
		return nil
	})
}

func waitForOpenBaoSealed(t *testing.T, ctx context.Context, client *api.Client) {
	t.Helper()
	waitFor(t, ctx, func() error {
		status, err := client.Sys().SealStatusWithContext(ctx)
		if err != nil {
			return err
		}
		if !status.Initialized || !status.Sealed {
			return fmt.Errorf("openbao not sealed: initialized=%v sealed=%v", status.Initialized, status.Sealed)
		}
		return nil
	})
}

func waitForRaftPeers(t *testing.T, ctx context.Context, client *api.Client, expectedIDs ...string) {
	t.Helper()
	waitFor(t, ctx, func() error {
		state, err := client.Sys().RaftAutopilotStateWithContext(ctx)
		if err != nil {
			return err
		}
		if state == nil {
			return errors.New("raft autopilot state response is nil")
		}

		peers := make(map[string]struct{}, len(state.Servers)*2)
		for key, server := range state.Servers {
			peers[key] = struct{}{}
			if server != nil {
				peers[server.ID] = struct{}{}
				peers[server.Name] = struct{}{}
			}
		}

		var missing []string
		for _, expectedID := range expectedIDs {
			if _, ok := peers[expectedID]; !ok {
				missing = append(missing, expectedID)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("raft peers missing %v; got %v", missing, peers)
		}
		return nil
	})
}

func waitForRaftLeader(t *testing.T, ctx context.Context, client *api.Client, expectedID string) {
	t.Helper()
	waitFor(t, ctx, func() error {
		state, err := client.Sys().RaftAutopilotStateWithContext(ctx)
		if err != nil {
			return err
		}
		if state == nil {
			return errors.New("raft autopilot state response is nil")
		}

		leaderAliases := map[string]struct{}{
			state.Leader: {},
		}
		if server := state.Servers[state.Leader]; server != nil {
			leaderAliases[server.ID] = struct{}{}
			leaderAliases[server.Name] = struct{}{}
		}
		if _, ok := leaderAliases[expectedID]; !ok {
			return fmt.Errorf("raft leader = %q, want %q", state.Leader, expectedID)
		}
		return nil
	})
}

func sealOpenBao(t *testing.T, ctx context.Context, client *api.Client) {
	t.Helper()
	if err := client.Sys().SealWithContext(ctx); err != nil {
		t.Fatalf("seal OpenBao: %v", err)
	}
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
		options.BaseEndpoint = aws.String(env("E2E_RESILIENCE_LOCALSTACK_ENDPOINT", "http://127.0.0.1:4567"))
	})
}

func restartOpenBao(t *testing.T, ctx context.Context) {
	t.Helper()
	runOpenBaoServiceCommand(t, ctx, "restart", "openbao")
}

func restartOpenBaoStandby(t *testing.T, ctx context.Context) {
	t.Helper()
	runOpenBaoServiceCommand(t, ctx, "restart", "openbao-standby")
}

func stopOpenBao(t *testing.T, ctx context.Context) {
	t.Helper()
	runOpenBaoServiceCommand(t, ctx, "stop", "openbao")
}

func startOpenBao(t *testing.T, ctx context.Context) {
	t.Helper()
	runOpenBaoServiceCommand(t, ctx, "start", "openbao")
}

func runOpenBaoServiceCommand(t *testing.T, ctx context.Context, command string, service string) {
	t.Helper()
	composeCommand := strings.Fields(env("E2E_DOCKER_COMPOSE", "docker compose"))
	if len(composeCommand) == 0 {
		t.Fatal("E2E_DOCKER_COMPOSE resolved to an empty command")
	}
	args := append(composeCommand[1:],
		"-f", env("E2E_RESILIENCE_COMPOSE_FILE", "compose.yaml"),
		command, service,
	)
	cmd := exec.CommandContext(ctx, composeCommand[0], args...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s container: %v\n%s", command, service, err, output)
	}
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

func drainQueue(t *testing.T, client *api.Client, expectedProcessed int) {
	t.Helper()
	secret := write(t, client, mountPath+"/queue/drain", map[string]interface{}{
		"max_operations": expectedProcessed,
	})
	if got := intFromSecret(t, secret.Data["processed"]); got != expectedProcessed {
		t.Fatalf("queue drain processed = %d, want %d", got, expectedProcessed)
	}
}

func disableEventDispatch(t *testing.T, client *api.Client) {
	t.Helper()
	write(t, client, mountPath+"/config", map[string]interface{}{
		"event_dispatch_enabled": false,
	})
}

func assertConfig(
	t *testing.T,
	client *api.Client,
	expectedDisabled bool,
	expectedRestoreGuard bool,
	expectedSourceOptIn bool,
) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/config")
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if secret == nil {
		t.Fatal("config response is nil")
	}
	if got := secret.Data["disabled"]; got != expectedDisabled {
		t.Fatalf("config disabled = %v, want %v", got, expectedDisabled)
	}
	if got := secret.Data["restore_guard"]; got != expectedRestoreGuard {
		t.Fatalf("config restore_guard = %v, want %v", got, expectedRestoreGuard)
	}
	if got := secret.Data["require_source_opt_in"]; got != expectedSourceOptIn {
		t.Fatalf("config require_source_opt_in = %v, want %v", got, expectedSourceOptIn)
	}
}

func assertQueue(t *testing.T, client *api.Client, expectedPending int, expectedRetryWait int) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/queue")
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if secret == nil {
		t.Fatal("queue response is nil")
	}
	if got := intFromSecret(t, secret.Data["pending"]); got != expectedPending {
		t.Fatalf("queue pending = %d, want %d", got, expectedPending)
	}
	if got := intFromSecret(t, secret.Data["retry_wait"]); got != expectedRetryWait {
		t.Fatalf("queue retry_wait = %d, want %d", got, expectedRetryWait)
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

func assertStatusEventually(t *testing.T, ctx context.Context, client *api.Client, expectedState string) {
	t.Helper()
	waitFor(t, ctx, func() error {
		secret, err := client.Logical().Read(mountPath + "/status/app/db")
		if err != nil {
			return err
		}
		if secret == nil {
			return errors.New("status response is nil")
		}
		if got := secret.Data["state"]; got != expectedState {
			return fmt.Errorf("status state = %v, want %s", got, expectedState)
		}
		return nil
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

func assertRemoteMissing(t *testing.T, ctx context.Context, client *secretsmanager.Client, secretName string) {
	t.Helper()
	_, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(secretName),
	})
	if err == nil {
		t.Fatalf("remote secret %q exists before dispatch", secretName)
	}
	if !isNotFound(err) {
		t.Fatalf("describe remote secret before dispatch: %v", err)
	}
}

func forceDeleteSecret(ctx context.Context, client *secretsmanager.Client, secretName string) {
	_, _ = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(secretName),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
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
