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

func TestIncompatibleStorageSchemaFailsClosed(t *testing.T) {
	env := newBackendTestEnv(t)
	entry, err := logical.StorageEntryJSON(storageSchemaKey, storageSchemaRecord{
		Version:              currentStorageSchema + 1,
		MinCompatibleVersion: currentStorageSchema + 1,
		CreatedTime:          "2026-07-01T00:00:00Z",
		UpdatedTime:          "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("encode schema: %v", err)
	}
	if err := env.storage.Put(context.Background(), entry); err != nil {
		t.Fatalf("write schema: %v", err)
	}

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
	entry, err = env.storage.Get(context.Background(), metadataStorageKey("app/db"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if entry != nil {
		t.Fatal("source metadata must not be written when schema is incompatible")
	}
}

func TestNormalizeSourcePathRejectsReservedSegments(t *testing.T) {
	for _, input := range []string{
		"app/versions/5/x",
		"versions",
		"team/plan",
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
		"team/plans",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeSourcePath(input); err != nil {
				t.Fatalf("normalizeSourcePath(%q): %v", input, err)
			}
		})
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
