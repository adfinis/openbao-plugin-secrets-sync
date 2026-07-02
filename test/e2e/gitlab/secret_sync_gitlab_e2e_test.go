//go:build e2e

package gitlabe2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/openbao/openbao/api/v2"
)

const (
	pluginName       = "openbao-plugin-secrets-sync"
	pluginVersion    = "v0.0.0-dev"
	mountPath        = "secret-sync"
	rootToken        = "root"
	testPollInterval = 500 * time.Millisecond
	testTimeout      = 90 * time.Second
)

func TestOpenBaoPluginSyncsToGitLabProjectVariables(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	baoClient := newOpenBaoClient(t)
	waitForOpenBao(t, ctx, baoClient)
	registerPlugin(t, baoClient)
	mountPlugin(t, baoClient)

	gitLabClient := newGitLabClient(t)
	variableKey := fmt.Sprintf("OPENBAO_SECRET_SYNC_E2E_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = gitLabClient.deleteVariable(context.Background(), variableKey)
	})

	writeGitLabDestination(t, baoClient, nil)
	assertDestinationValid(t, baoClient)
	assertDestinationHealthy(t, baoClient)
	acknowledgeRestoreGuard(t, baoClient)
	write(t, baoClient, mountPath+"/metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"syncable": "true",
		},
	})
	writeSource(t, baoClient, variableKey, "initial")

	plan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest())
	if got := plan.Data["action"]; got != "create" {
		t.Fatalf("plan action = %v, want create", got)
	}

	association := write(t, baoClient, mountPath+"/associations/app/db", associationRequest())
	if ids := stringSlice(t, association.Data["sync_operation_ids"]); len(ids) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", ids)
	}
	associationID := associationIDFromResponse(t, association)
	drainQueue(t, baoClient, 1)
	assertStatusObject(t, baoClient, variableKey, "SYNCED", "")
	assertGitLabVariable(t, ctx, gitLabClient, variableKey, "initial", "1")
	assertReconcilePlan(t, baoClient, variableKey, "SYNCED")

	noOpPlan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest())
	if got := noOpPlan.Data["action"]; got != "noop" {
		t.Fatalf("noop plan action = %v, want noop", got)
	}

	if err := gitLabClient.updateVariableValue(ctx, variableKey, "drifted"); err != nil {
		t.Fatalf("manually drift GitLab variable: %v", err)
	}
	enableBackgroundRepair(t, baoClient)
	assertGitLabVariable(t, ctx, gitLabClient, variableKey, "initial", "1")
	assertBackgroundRepairStatus(t, baoClient, variableKey, "value")
	disableBackgroundDrift(t, baoClient)
	assertReconcileApply(t, baoClient, variableKey, "SYNCED")

	writeSource(t, baoClient, variableKey, "updated")
	drainQueue(t, baoClient, 1)
	assertGitLabVariable(t, ctx, gitLabClient, variableKey, "updated", "2")

	writeSource(t, baoClient, variableKey, "token_123")
	drainQueue(t, baoClient, 1)
	assertGitLabVariable(t, ctx, gitLabClient, variableKey, "token_123", "3")
	assertGitLabVariableAttributes(t, ctx, gitLabClient, variableKey, false, false, true, gitlab.VariableTypeEnvVar)

	writeGitLabDestination(t, baoClient, map[string]interface{}{
		gitlab.ConfigKeyProtected: "true",
		gitlab.ConfigKeyMasked:    "true",
	})
	attributePlan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest())
	if got := attributePlan.Data["action"]; got != "update" {
		t.Fatalf("attribute drift plan action = %v, want update", got)
	}
	sync := write(t, baoClient, mountPath+"/associations/app/db/"+associationID+"/sync", map[string]interface{}{})
	if ids := stringSlice(t, sync.Data["sync_operation_ids"]); len(ids) != 1 {
		t.Fatalf("attribute drift sync_operation_ids = %v, want one operation", ids)
	}
	drainQueue(t, baoClient, 1)
	assertGitLabVariable(t, ctx, gitLabClient, variableKey, "token_123", "3")
	assertGitLabVariableAttributes(t, ctx, gitLabClient, variableKey, true, true, true, gitlab.VariableTypeEnvVar)

	deleteSecret := deletePath(t, baoClient, mountPath+"/data/app/db")
	if ids := metadataOperationIDs(t, deleteSecret); len(ids) != 1 {
		t.Fatalf("delete sync_operation_ids = %v, want one operation", ids)
	}
	drainQueue(t, baoClient, 1)
	assertGitLabVariableMissing(t, ctx, gitLabClient, variableKey)
	assertStatusObject(t, baoClient, variableKey, "REMOTE_MISSING", "")
}

func writeGitLabDestination(t *testing.T, client *api.Client, overrides map[string]interface{}) {
	t.Helper()
	config := map[string]interface{}{
		gitlab.ConfigKeyBaseURL:           env("E2E_GITLAB_BASE_URL_IN_BAO", "http://gitlab"),
		gitlab.ConfigKeyProjectID:         env("E2E_GITLAB_PROJECT_PATH", "root/openbao-plugin-secrets-sync-e2e"),
		gitlab.ConfigKeyEnvironmentScope:  env("E2E_GITLAB_ENVIRONMENT_SCOPE", "production"),
		gitlab.ConfigKeyToken:             env("E2E_GITLAB_TOKEN", "glpat-openbao-plugin-secrets-sync-e2e-token-000000"),
		gitlab.ConfigKeyVariableRaw:       "true",
		gitlab.ConfigKeyVariableType:      gitlab.VariableTypeEnvVar,
		gitlab.ConfigKeyAllowInsecureHTTP: "true",
	}
	for key, value := range overrides {
		config[key] = value
	}
	write(t, client, mountPath+"/destinations/gitlab/local", config)
}

func associationRequest() map[string]interface{} {
	return map[string]interface{}{
		"destination_type": "gitlab",
		"destination_name": "local",
		"name_template":    "{{ key }}",
		"granularity":      "secret-key",
		"format":           "raw",
		"delete_mode":      "delete",
	}
}

func writeSource(t *testing.T, client *api.Client, variableKey string, value string) {
	t.Helper()
	write(t, client, mountPath+"/data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			variableKey: value,
		},
	})
}

func newOpenBaoClient(t *testing.T) *api.Client {
	t.Helper()
	config := api.DefaultConfig()
	config.Address = env("E2E_GITLAB_OPENBAO_ADDR", "http://127.0.0.1:18203")
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
		Version: pluginVersion,
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
			PluginVersion: pluginVersion,
		},
	}); err != nil {
		t.Fatalf("mount plugin: %v", err)
	}
}

func acknowledgeRestoreGuard(t *testing.T, client *api.Client) {
	t.Helper()
	write(t, client, mountPath+"/config/restore-guard/acknowledge", map[string]interface{}{})
}

func assertDestinationValid(t *testing.T, client *api.Client) {
	t.Helper()
	secret := write(t, client, mountPath+"/destinations/gitlab/local/validate", map[string]interface{}{})
	if got := secret.Data["valid"]; got != true {
		t.Fatalf("destination valid = %v, want true", got)
	}
}

func assertDestinationHealthy(t *testing.T, client *api.Client) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/destinations/gitlab/local/health")
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	if got := secret.Data["healthy"]; got != true {
		t.Fatalf("destination healthy = %v, want true: %#v", got, secret.Data)
	}
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

func assertReconcilePlan(t *testing.T, client *api.Client, objectID string, expectedState string) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/reconcile/app/db/plan")
	if err != nil {
		t.Fatalf("read reconcile plan: %v", err)
	}
	assertReconcileObject(t, secret, objectID, expectedState)
}

func assertReconcileApply(t *testing.T, client *api.Client, objectID string, expectedState string) {
	t.Helper()
	secret := write(t, client, mountPath+"/reconcile/app/db", map[string]interface{}{})
	assertReconcileObject(t, secret, objectID, expectedState)
}

func assertReconcileObject(t *testing.T, secret *api.Secret, objectID string, expectedState string) {
	t.Helper()
	if got := secret.Data["state"]; got != expectedState {
		t.Fatalf("reconcile state = %v, want %s", got, expectedState)
	}
	objects := objectsFromSecret(t, secret.Data["objects"])
	object := objectByID(t, objects, objectID)
	if got := object["state"]; got != expectedState {
		t.Fatalf("reconcile object state = %v, want %s", got, expectedState)
	}
}

func assertStatusObject(
	t *testing.T,
	client *api.Client,
	objectID string,
	expectedState string,
	expectedErrorClass string,
) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/status/app/db")
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if got := secret.Data["state"]; got != expectedState {
		t.Fatalf("status state = %v, want %s: %#v", got, expectedState, secret.Data)
	}
	object := objectByID(t, objectsFromSecret(t, secret.Data["objects"]), objectID)
	if got := object["state"]; got != expectedState {
		t.Fatalf("status object state = %v, want %s", got, expectedState)
	}
	if got := object["last_error_class"]; got != expectedErrorClass {
		t.Fatalf("last_error_class = %v, want %s", got, expectedErrorClass)
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

func assertBackgroundRepairStatus(
	t *testing.T,
	client *api.Client,
	objectID string,
	expectedVerification string,
) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/status/app/db")
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if secret == nil {
		t.Fatal("status response is nil")
	}
	if got := secret.Data["state"]; got != "SYNCED" {
		t.Fatalf("status state = %v, want SYNCED: %#v", got, secret.Data)
	}
	object := objectByID(t, objectsFromSecret(t, secret.Data["objects"]), objectID)
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

type gitLabClient struct {
	baseURL          string
	token            string
	projectPath      string
	environmentScope string
	httpClient       *http.Client
}

type gitLabVariable struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	EnvironmentScope string `json:"environment_scope"`
	Protected        bool   `json:"protected"`
	Masked           bool   `json:"masked"`
	VariableRaw      bool   `json:"raw"`
	VariableType     string `json:"variable_type"`
	Description      string `json:"description"`
}

type variableMetadata struct {
	ManagedBy      string `json:"m"`
	AssociationID  string `json:"a"`
	SourcePath     string `json:"p"`
	SourcePathHash string `json:"ph"`
	ObjectID       string `json:"o"`
	ObjectIDHash   string `json:"oh"`
	SourceVersion  int    `json:"v"`
	PayloadSHA256  string `json:"h"`
	PayloadFormat  string `json:"f"`
}

func newGitLabClient(t *testing.T) gitLabClient {
	t.Helper()
	return gitLabClient{
		baseURL:          strings.TrimRight(env("E2E_GITLAB_URL", "http://127.0.0.1:18080"), "/"),
		token:            env("E2E_GITLAB_TOKEN", "glpat-openbao-plugin-secrets-sync-e2e-token-000000"),
		projectPath:      env("E2E_GITLAB_PROJECT_PATH", "root/openbao-plugin-secrets-sync-e2e"),
		environmentScope: env("E2E_GITLAB_ENVIRONMENT_SCOPE", "production"),
		httpClient:       &http.Client{Timeout: 10 * time.Second},
	}
}

func (c gitLabClient) getVariable(ctx context.Context, key string) (*gitLabVariable, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.variableURL(key), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	resp, err := c.httpClient.Do(req) //nolint:gosec // Opt-in e2e endpoint is local Docker/loopback GitLab.
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gitlab get variable status %d: %s", resp.StatusCode, string(body))
	}
	var variable gitLabVariable
	if err := json.NewDecoder(resp.Body).Decode(&variable); err != nil {
		return nil, err
	}
	return &variable, nil
}

func (c gitLabClient) deleteVariable(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.variableURL(key), nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	resp, err := c.httpClient.Do(req) //nolint:gosec // Opt-in e2e endpoint is local Docker/loopback GitLab.
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound ||
		(resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices) {
		return nil
	}
	return fmt.Errorf("gitlab delete variable status %d", resp.StatusCode)
}

func (c gitLabClient) updateVariableValue(ctx context.Context, key string, value string) error {
	form := url.Values{}
	form.Set("key", key)
	form.Set("value", value)
	form.Set("environment_scope", c.environmentScope)
	form.Set("protected", "false")
	form.Set("masked", "false")
	form.Set("raw", "true")
	form.Set("variable_type", gitlab.VariableTypeEnvVar)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.variableURL(key), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req) //nolint:gosec // Opt-in e2e endpoint is local Docker/loopback GitLab.
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("gitlab update variable status %d", resp.StatusCode)
	}
	return nil
}

func (c gitLabClient) variableURL(key string) string {
	query := url.Values{}
	query.Set("filter[environment_scope]", c.environmentScope)
	return fmt.Sprintf(
		"%s/api/v4/projects/%s/variables/%s?%s",
		c.baseURL,
		url.PathEscape(c.projectPath),
		url.PathEscape(key),
		query.Encode(),
	)
}

func assertGitLabVariable(
	t *testing.T,
	ctx context.Context,
	client gitLabClient,
	key string,
	expectedValue string,
	expectedVersion string,
) {
	t.Helper()
	waitFor(t, ctx, func() error {
		variable, err := client.getVariable(ctx, key)
		if err != nil {
			return err
		}
		if variable == nil {
			return fmt.Errorf("gitlab variable %s missing", key)
		}
		if variable.Value != expectedValue {
			return fmt.Errorf("gitlab variable value = %q, want %q", variable.Value, expectedValue)
		}
		if variable.EnvironmentScope != client.environmentScope {
			return fmt.Errorf("environment scope = %q, want %q", variable.EnvironmentScope, client.environmentScope)
		}
		metadata := variableMetadataFromDescription(t, variable.Description)
		if metadata.SourceVersion != intFromString(t, expectedVersion) {
			return fmt.Errorf("metadata source version = %d, want %s", metadata.SourceVersion, expectedVersion)
		}
		if err := assertMetadataIdentity(metadata.ObjectID, metadata.ObjectIDHash, key, "object id"); err != nil {
			return err
		}
		if err := assertMetadataIdentity(metadata.SourcePath, metadata.SourcePathHash, "app/db", "source path"); err != nil {
			return err
		}
		if metadata.PayloadFormat != "raw" {
			return fmt.Errorf("metadata payload format = %q, want raw", metadata.PayloadFormat)
		}
		if !strings.HasPrefix(metadata.PayloadSHA256, "sha256:") {
			return fmt.Errorf("metadata payload sha = %q, want sha256 prefix", metadata.PayloadSHA256)
		}
		if strings.Contains(variable.Description, expectedValue) {
			return errors.New("variable description contains secret value")
		}
		return nil
	})
}

func assertMetadataIdentity(actual string, actualHash string, expected string, label string) error {
	if actual == expected {
		return nil
	}
	if actual == "" && actualHash != "" {
		return nil
	}
	if actual == "" {
		return fmt.Errorf("metadata %s is empty and has no compact hash", label)
	}
	return fmt.Errorf("metadata %s = %q, want %q", label, actual, expected)
}

func assertGitLabVariableAttributes(
	t *testing.T,
	ctx context.Context,
	client gitLabClient,
	key string,
	protected bool,
	masked bool,
	variableRaw bool,
	variableType string,
) {
	t.Helper()
	waitFor(t, ctx, func() error {
		variable, err := client.getVariable(ctx, key)
		if err != nil {
			return err
		}
		if variable == nil {
			return fmt.Errorf("gitlab variable %s missing", key)
		}
		if variable.Protected != protected {
			return fmt.Errorf("protected = %t, want %t", variable.Protected, protected)
		}
		if variable.Masked != masked {
			return fmt.Errorf("masked = %t, want %t", variable.Masked, masked)
		}
		if variable.VariableRaw != variableRaw {
			return fmt.Errorf("raw = %t, want %t", variable.VariableRaw, variableRaw)
		}
		if variable.VariableType != variableType {
			return fmt.Errorf("variable type = %q, want %q", variable.VariableType, variableType)
		}
		return nil
	})
}

func assertGitLabVariableMissing(t *testing.T, ctx context.Context, client gitLabClient, key string) {
	t.Helper()
	waitFor(t, ctx, func() error {
		variable, err := client.getVariable(ctx, key)
		if err != nil {
			return err
		}
		if variable != nil {
			return fmt.Errorf("gitlab variable %s still exists", key)
		}
		return nil
	})
}

func variableMetadataFromDescription(t *testing.T, description string) variableMetadata {
	t.Helper()
	var metadata variableMetadata
	if err := json.Unmarshal([]byte(description), &metadata); err != nil {
		t.Fatalf("parse variable metadata description: %v", err)
	}
	if metadata.ManagedBy != "1" {
		t.Fatalf("metadata managed_by = %q, want 1", metadata.ManagedBy)
	}
	return metadata
}

func associationIDFromResponse(t *testing.T, secret *api.Secret) string {
	t.Helper()
	association, ok := secret.Data["association"].(map[string]interface{})
	if !ok {
		t.Fatalf("association = %T, want map[string]interface{}", secret.Data["association"])
	}
	id, ok := association["id"].(string)
	if !ok || id == "" {
		t.Fatalf("association id = %v, want non-empty string", association["id"])
	}
	return id
}

func metadataOperationIDs(t *testing.T, secret *api.Secret) []string {
	t.Helper()
	metadata, ok := secret.Data["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata = %T, want map[string]interface{}", secret.Data["metadata"])
	}
	return stringSlice(t, metadata["sync_operation_ids"])
}

func stringSlice(t *testing.T, raw interface{}) []string {
	t.Helper()
	values, ok := raw.([]interface{})
	if ok {
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = append(out, value.(string))
		}
		return out
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
	case int64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			t.Fatalf("parse json number: %v", err)
		}
		return int(parsed)
	default:
		t.Fatalf("value = %T, want number", raw)
		return 0
	}
}

func intFromString(t *testing.T, raw string) int {
	t.Helper()
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		t.Fatalf("parse int %q: %v", raw, err)
	}
	return value
}

func objectsFromSecret(t *testing.T, raw interface{}) []map[string]interface{} {
	t.Helper()
	objects, ok := raw.([]interface{})
	if ok {
		out := make([]map[string]interface{}, 0, len(objects))
		for _, object := range objects {
			typed, ok := object.(map[string]interface{})
			if !ok {
				t.Fatalf("object = %T, want map[string]interface{}", object)
			}
			out = append(out, typed)
		}
		return out
	}
	typed, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("objects = %T, want object slice", raw)
	}
	return typed
}

func objectByID(t *testing.T, objects []map[string]interface{}, objectID string) map[string]interface{} {
	t.Helper()
	for _, object := range objects {
		if object["object_id"] == objectID {
			return object
		}
	}
	t.Fatalf("object %s missing in %#v", objectID, objects)
	return nil
}

func waitFor(t *testing.T, ctx context.Context, check func() error) {
	t.Helper()
	deadline := time.NewTimer(testTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(testPollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := check(); err == nil {
			return
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended while waiting: %v; last error: %v", ctx.Err(), lastErr)
		case <-deadline.C:
			t.Fatalf("timed out waiting: %v", lastErr)
		case <-ticker.C:
		}
	}
}

func env(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
