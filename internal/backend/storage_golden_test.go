package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"testing"

	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	storageGoldenUpdateEnv = "UPDATE_STORAGE_GOLDEN"
	storageGoldenFile      = "testdata/storage_golden/layout.json"
)

type storageGoldenEntry struct {
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

func TestStorageGoldenLayout(t *testing.T) {
	env := newBackendTestEnv(t)

	assertNoStorageGoldenError(t, env.update(configPath, map[string]interface{}{
		"event_dispatch_enabled": false,
		"queue_capacity":         10,
	}))
	assertNoErrorResponse(t, env.writeAppDBSecretData(map[string]interface{}{
		"setting":  "enabled",
		"username": "app",
	}))
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

	env.acknowledgeRestoreGuard()
	assertNoErrorResponse(t, env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	}))
	assertNoErrorResponse(t, env.writeAppDBSecretData(map[string]interface{}{
		"setting":  "rotated",
		"username": "app",
	}))

	assertStorageGolden(t, captureStorageGolden(t, env.storage))
}

func assertNoStorageGoldenError(t *testing.T, resp *logical.Response) {
	t.Helper()
	if resp != nil && resp.IsError() {
		t.Fatalf("unexpected error response: %v", resp.Error())
	}
}

func captureStorageGolden(t *testing.T, storage logical.Storage) []storageGoldenEntry {
	t.Helper()

	keys, err := logical.CollectKeys(context.Background(), storage)
	if err != nil {
		t.Fatalf("collect storage keys: %v", err)
	}
	sort.Strings(keys)

	entries := make([]storageGoldenEntry, 0, len(keys))
	for _, key := range keys {
		entry, err := storage.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("read storage key %q: %v", key, err)
		}
		if entry == nil {
			continue
		}
		entries = append(entries, storageGoldenEntry{
			Key:   canonicalStorageGoldenKey(key),
			Value: canonicalStorageGoldenValue(t, entry.Value),
		})
	}
	return entries
}

func canonicalStorageGoldenValue(t *testing.T, raw []byte) interface{} {
	t.Helper()

	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode storage value: %v", err)
	}
	return canonicalAPIGoldenDecoded(decoded, "")
}

func canonicalStorageGoldenKey(key string) string {
	for _, replacement := range storageGoldenKeyReplacements {
		key = replacement.pattern.ReplaceAllString(key, replacement.value)
	}
	return key
}

var storageGoldenKeyReplacements = []struct {
	pattern *regexp.Regexp
	value   string
}{
	{pattern: regexp.MustCompile(`epoch-[0-9a-f]{32}`), value: "<restore-epoch>"},
	{pattern: regexp.MustCompile(`gen-[0-9a-f]{32}`), value: "<generation>"},
	{pattern: regexp.MustCompile(`assoc-[0-9a-f]{32}`), value: "<association-id>"},
	{pattern: regexp.MustCompile(`op-[0-9a-f]{16}-[0-9]+`), value: "<operation-id>"},
	{pattern: regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z`), value: "<timestamp>"},
}

func assertStorageGolden(t *testing.T, entries []storageGoldenEntry) {
	t.Helper()
	assertGoldenFixture(t, goldenFixture{
		file:          storageGoldenFile,
		updateEnv:     storageGoldenUpdateEnv,
		updateCommand: storageGoldenUpdateCommand(),
		description:   "storage golden layout",
	}, marshalStorageGolden(t, entries))
}

func storageGoldenUpdateCommand() string {
	return storageGoldenUpdateEnv + "=1 go test ./internal/backend -run TestStorageGoldenLayout"
}

func marshalStorageGolden(t *testing.T, entries []storageGoldenEntry) []byte {
	t.Helper()

	buffer := bytes.Buffer{}
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(entries); err != nil {
		t.Fatalf("marshal storage golden layout: %v", err)
	}
	return buffer.Bytes()
}
