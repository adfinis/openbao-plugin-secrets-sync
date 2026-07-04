package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	apiGoldenUpdateEnv = "UPDATE_API_GOLDEN"
	apiGoldenFile      = "testdata/api_golden/responses.json"
)

type apiGoldenCapture struct {
	responses map[string]interface{}
}

func TestAPIGoldenResponses(t *testing.T) {
	env := newBackendTestEnv(t)
	capture := newAPIGoldenCapture()

	capture.response(t, "info.read", env.read("info"))
	capture.response(t, "config.read.initial", env.read(configPath))
	capture.response(t, "config.update", env.update(configPath, map[string]interface{}{
		"event_dispatch_enabled": false,
		"queue_capacity":         10,
		"require_source_opt_in":  false,
	}))
	capture.response(t, "sources.check.empty", env.read("sources/app/db/check"))
	capture.response(t, "metadata.write", env.update("metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"owner":    "platform",
			"syncable": "true",
		},
		"max_versions": 5,
	}))
	capture.response(t, "data.write.initial", env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"setting":  "enabled",
			"username": "app",
		},
	}))
	capture.response(t, "data.read.latest", env.read("data/app/db"))
	capture.response(t, "metadata.read", env.read("metadata/app/db"))
	capture.response(t, "sources.check.ready", env.read("sources/app/db/check"))

	capture.response(t, "destinations.write.empty", env.update("destinations/fake/default", map[string]interface{}{
		"description": "test destination",
	}))
	capture.response(t, "destinations.read", env.read("destinations/fake/default"))
	capture.response(t, "destinations.list", env.list("destinations/fake"))
	capture.response(t, "destinations.check", env.read("destinations/fake/default/check"))
	capture.response(t, "destinations.validate", env.read("destinations/fake/default/validate"))
	capture.response(t, "destinations.health", env.read("destinations/fake/default/health"))

	capture.response(t, "associations.plan.create", env.update("associations/app/db/plan", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"format":        defaultAssociationFormat,
		"granularity":   syncObjectIDSecretPath,
		"resolved_name": "prod/app/db",
	}))
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"format":        defaultAssociationFormat,
		"granularity":   syncObjectIDSecretPath,
		"resolved_name": "prod/app/db",
	})
	capture.response(t, "associations.write.create", associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	capture.response(t, "associations.read.path", env.read("associations/app/db"))
	capture.response(t, "associations.list", env.list("associations"))
	capture.response(t, "queue.read.pending", env.read("queue"))
	capture.response(t, "queue.operation.read", env.read("queue/"+operationID))
	capture.response(t, "status.read.pending", env.read("status/app/db"))

	env.acknowledgeRestoreGuard()
	capture.response(t, "queue.drain", env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	}))
	capture.response(t, "queue.read.synced", env.read("queue"))
	capture.response(t, "status.read.synced", env.read("status/app/db"))
	capture.response(t, "reconcile.plan.synced", env.read("reconcile/app/db/plan"))
	capture.response(t, "reconcile.apply.synced", env.update("reconcile/app/db"))

	assertAPIGolden(t, capture.responses)
}

func newAPIGoldenCapture() apiGoldenCapture {
	return apiGoldenCapture{responses: make(map[string]interface{})}
}

func (capture apiGoldenCapture) response(t *testing.T, name string, resp *logical.Response) {
	t.Helper()
	capture.responses[name] = canonicalAPIGoldenResponse(t, resp)
}

func canonicalAPIGoldenResponse(t *testing.T, resp *logical.Response) interface{} {
	t.Helper()
	if resp == nil {
		return nil
	}
	envelope := map[string]interface{}{
		"data": canonicalAPIGoldenValue(t, resp.Data),
	}
	if resp.IsError() {
		envelope["error"] = resp.Error().Error()
	}
	return envelope
}

func canonicalAPIGoldenValue(t *testing.T, value interface{}) interface{} {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal API value: %v", err)
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode API value: %v", err)
	}
	return canonicalAPIGoldenDecoded(decoded, "")
}

func canonicalAPIGoldenDecoded(value interface{}, key string) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for nestedKey, nestedValue := range typed {
			result[nestedKey] = canonicalAPIGoldenDecoded(nestedValue, nestedKey)
		}
		return result
	case []interface{}:
		result := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			result = append(result, canonicalAPIGoldenDecoded(item, key))
		}
		return canonicalAPIGoldenSlice(key, result)
	case string:
		return canonicalAPIGoldenString(key, typed)
	default:
		return typed
	}
}

func canonicalAPIGoldenSlice(key string, values []interface{}) interface{} {
	switch key {
	case "sync_operation_ids", "operation_ids", "canceled_operation_ids":
		return repeatedPlaceholder(len(values), "<operation-id>")
	default:
		return values
	}
}

func canonicalAPIGoldenString(key string, value string) interface{} {
	if value == "" {
		return value
	}
	if apiGoldenTimestampKeys[key] || isRFC3339Timestamp(value) {
		return "<timestamp>"
	}
	if placeholder, ok := apiGoldenPlaceholderForKey(key); ok {
		return placeholder
	}
	if placeholder, ok := apiGoldenPlaceholderForValue(value); ok {
		return placeholder
	}
	return value
}

func apiGoldenPlaceholderForKey(key string) (string, bool) {
	placeholders := map[string]string{
		"association_id":     "<association-id>",
		"claim_owner":        "<claim-owner>",
		"generation":         "<generation>",
		"idempotency_key":    "<operation-id>",
		"last_operation_id":  "<operation-id>",
		"plugin_instance_id": "<plugin-instance-id>",
		"restore_epoch":      "<restore-epoch>",
	}
	placeholder, ok := placeholders[key]
	return placeholder, ok
}

func apiGoldenPlaceholderForValue(value string) (string, bool) {
	switch {
	case strings.HasPrefix(value, "inst-"):
		return "<plugin-instance-id>", true
	case strings.HasPrefix(value, "epoch-"):
		return "<restore-epoch>", true
	case strings.HasPrefix(value, "gen-"):
		return "<generation>", true
	case strings.HasPrefix(value, "assoc-"):
		return "<association-id>", true
	case strings.HasPrefix(value, "op-"):
		return "<operation-id>", true
	default:
		return "", false
	}
}

var apiGoldenTimestampKeys = map[string]bool{
	"claim_expires_time":              true,
	"created_time":                    true,
	"deletion_time":                   true,
	"last_drift_detected_time":        true,
	"last_reconcile_time":             true,
	"last_repair_time":                true,
	"last_success_time":               true,
	"not_before":                      true,
	"restore_guard_acknowledged_time": true,
	"updated_time":                    true,
}

func isRFC3339Timestamp(value string) bool {
	_, err := time.Parse(timeFormatRFC3339, value)
	return err == nil
}

func repeatedPlaceholder(count int, placeholder string) []interface{} {
	values := make([]interface{}, count)
	for i := range values {
		values[i] = placeholder
	}
	return values
}

func assertAPIGolden(t *testing.T, responses map[string]interface{}) {
	t.Helper()
	actual := marshalAPIGolden(t, responses)
	path := filepath.FromSlash(apiGoldenFile)
	if os.Getenv(apiGoldenUpdateEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, actual, 0o600); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
		return
	}
	expected, err := os.ReadFile(path) //nolint:gosec // Test fixture path is fixed by apiGoldenFile.
	if err != nil {
		t.Fatalf("read golden file: %v; run %s", err, apiGoldenUpdateCommand())
	}
	if !bytes.Equal(bytes.TrimSpace(expected), bytes.TrimSpace(actual)) {
		t.Fatalf(
			"API golden responses changed; review the diff and run %s if intentional\n%s",
			apiGoldenUpdateCommand(),
			apiGoldenMismatch(expected, actual),
		)
	}
}

func apiGoldenUpdateCommand() string {
	return apiGoldenUpdateEnv + "=1 go test ./internal/backend -run TestAPIGoldenResponses"
}

func marshalAPIGolden(t *testing.T, responses map[string]interface{}) []byte {
	t.Helper()
	ordered := make(map[string]interface{}, len(responses))
	keys := make([]string, 0, len(responses))
	for key := range responses {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ordered[key] = responses[key]
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(ordered); err != nil {
		t.Fatalf("marshal API golden responses: %v", err)
	}
	return buffer.Bytes()
}

func apiGoldenMismatch(expected []byte, actual []byte) string {
	return fmt.Sprintf("--- expected\n%s\n--- actual\n%s", expected, actual)
}
