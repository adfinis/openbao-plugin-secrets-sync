package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestFactoryCreatesLogicalBackend(t *testing.T) {
	b, err := Factory(context.Background(), &logical.BackendConfig{})
	if err != nil {
		t.Fatalf("factory returned error: %v", err)
	}
	if b == nil {
		t.Fatal("backend must not be nil")
	}
}

func TestStorageSchemaCompatibilityRules(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		record    storageSchemaRecord
		current   int
		min       int
		errorText string
	}{
		{
			name: "current schema",
			record: storageSchemaRecord{
				Version:              currentStorageSchema,
				MinCompatibleVersion: minSupportedStorageSchema,
			},
			current: currentStorageSchema,
			min:     minSupportedStorageSchema,
		},
		{
			name: "future compatible schema",
			record: storageSchemaRecord{
				Version:              currentStorageSchema + 1,
				MinCompatibleVersion: currentStorageSchema,
			},
			current: currentStorageSchema,
			min:     minSupportedStorageSchema,
		},
		{
			name: "future incompatible schema",
			record: storageSchemaRecord{
				Version:              currentStorageSchema + 1,
				MinCompatibleVersion: currentStorageSchema + 1,
			},
			current:   currentStorageSchema,
			min:       minSupportedStorageSchema,
			errorText: "requires plugin schema",
		},
		{
			name: "older than storage floor",
			record: storageSchemaRecord{
				Version:              1,
				MinCompatibleVersion: 1,
			},
			current:   2,
			min:       2,
			errorText: "older than minimum supported schema",
		},
		{
			name: "invalid zero schema",
			record: storageSchemaRecord{
				Version:              0,
				MinCompatibleVersion: 0,
			},
			current:   currentStorageSchema,
			min:       minSupportedStorageSchema,
			errorText: "must be greater than zero",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateStorageSchemaVersions(testCase.record, testCase.current, testCase.min)
			if testCase.errorText == "" {
				if err != nil {
					t.Fatalf("validateStorageSchema() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateStorageSchema() = nil, want error")
			}
			if !strings.Contains(err.Error(), testCase.errorText) {
				t.Fatalf("schema error = %q, want substring %q", err.Error(), testCase.errorText)
			}
			if !isStorageSchemaCompatibilityError(err) {
				t.Fatalf("schema error type = %T, want storage compatibility error", err)
			}
		})
	}
}

func TestEnsureStorageSchemaBackfillsLegacyMinCompatibleVersion(t *testing.T) {
	env := newBackendTestEnv(t)
	entry, err := logical.StorageEntryJSON(storageSchemaKey, storageSchemaRecord{
		Version:     currentStorageSchema,
		CreatedTime: "2026-07-01T00:00:00Z",
		UpdatedTime: "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("encode schema: %v", err)
	}
	if err := env.storage.Put(context.Background(), entry); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	record, err := ensureStorageSchema(context.Background(), env.storage, "2026-07-02T00:00:00Z")
	if err != nil {
		t.Fatalf("ensureStorageSchema(): %v", err)
	}
	if got := record.MinCompatibleVersion; got != currentStorageSchema {
		t.Fatalf("min compatible version = %d, want %d", got, currentStorageSchema)
	}
}

func TestIncompatibleStorageSchemaFailsClosed(t *testing.T) {
	env := newBackendTestEnv(t)
	writeIncompatibleStorageSchema(t, env.storage)

	resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "data/app/db",
		Storage:   env.storage,
		Data: map[string]interface{}{
			"data": map[string]interface{}{"password": "secret"},
		},
	})
	if err != nil {
		t.Fatalf("write with incompatible schema returned backend error: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("write with incompatible schema response = %#v, want logical error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "incompatible storage schema") {
		t.Fatalf("schema error = %q, want incompatible storage schema", resp.Error().Error())
	}
	entry, err := env.storage.Get(context.Background(), metadataStorageKey("app/db"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if entry != nil {
		t.Fatal("source metadata must not be written when schema is incompatible")
	}
}

func TestIncompatibleStorageSchemaBlocksQueueAndBackgroundMutation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	writeIncompatibleStorageSchema(t, env.storage)

	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	if drainResp == nil || !drainResp.IsError() {
		t.Fatalf("queue drain response = %#v, want schema error", drainResp)
	}
	if !strings.Contains(drainResp.Error().Error(), "incompatible storage schema") {
		t.Fatalf("queue drain error = %q, want incompatible storage schema", drainResp.Error().Error())
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)

	if err := env.b.periodic(context.Background(), &logical.Request{Storage: env.storage}); err == nil {
		t.Fatal("periodic error = nil, want incompatible schema error")
	} else if !strings.Contains(err.Error(), "incompatible storage schema") {
		t.Fatalf("periodic error = %q, want incompatible storage schema", err.Error())
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)

	processed, limit, ok := env.b.runEventDispatchPass(context.Background(), env.storage)
	if ok || processed != 0 || limit != 0 {
		t.Fatalf("event dispatch result = processed:%d limit:%d ok:%t, want blocked", processed, limit, ok)
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
}

func writeIncompatibleStorageSchema(t *testing.T, storage logical.Storage) {
	t.Helper()
	entry, err := logical.StorageEntryJSON(storageSchemaKey, storageSchemaRecord{
		Version:              currentStorageSchema + 1,
		MinCompatibleVersion: currentStorageSchema + 1,
		CreatedTime:          "2026-07-01T00:00:00Z",
		UpdatedTime:          "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("encode schema: %v", err)
	}
	if err := storage.Put(context.Background(), entry); err != nil {
		t.Fatalf("write schema: %v", err)
	}
}

func TestNormalizeSourcePathRejectsReservedSegments(t *testing.T) {
	for _, input := range []string{
		"app/versions/5/x",
		"versions",
		"team/plan",
		"team/disable",
		"team/enable",
		"team/sync",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeSourcePath(input); err == nil {
				t.Fatalf("normalizeSourcePath(%q) succeeded, want error", input)
			}
		})
	}
	for _, input := range []string{
		"app/versions2/5/x",
		"plan/team",
		"disable/team",
		"enable/team",
		"sync/team",
		"team/plans",
		"team/disabled",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeSourcePath(input); err != nil {
				t.Fatalf("normalizeSourcePath(%q): %v", input, err)
			}
		})
	}
}

func TestSecretPathObjectIDIsStorageKeySentinel(t *testing.T) {
	if syncObjectIDSecretPath != "secret-path" {
		t.Fatalf("secret-path object ID = %q, want frozen storage key sentinel %q", syncObjectIDSecretPath, "secret-path")
	}
}

func TestResolvedNameAllowedUsesSegmentBoundary(t *testing.T) {
	prefixes := []string{"prod/app", "shared/team/"}
	for _, name := range []string{
		"prod/app",
		"prod/app/db",
		"/prod/app/db",
		"shared/team",
		"shared/team/api",
	} {
		t.Run("allow "+name, func(t *testing.T) {
			if !resolvedNameAllowed(name, prefixes) {
				t.Fatalf("resolvedNameAllowed(%q) = false, want true", name)
			}
		})
	}
	for _, name := range []string{
		"prod/apple-secrets",
		"prod/application/db",
		"shared/team-alpha",
	} {
		t.Run("deny "+name, func(t *testing.T) {
			if resolvedNameAllowed(name, prefixes) {
				t.Fatalf("resolvedNameAllowed(%q) = true, want false", name)
			}
		})
	}
}
