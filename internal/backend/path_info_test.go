package backend

import (
	"context"
	"testing"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestInfoResponse(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.read("info")
	assertNoErrorResponse(t, resp)
	assertResponseValue(t, resp, "plugin_version", "0.0.0-dev")

	defaults, ok := resp.Data["defaults"].(map[string]interface{})
	if !ok {
		t.Fatalf("defaults = %T, want map", resp.Data["defaults"])
	}
	associationDefaults, ok := defaults["association"].(map[string]interface{})
	if !ok {
		t.Fatalf("defaults.association = %T, want map", defaults["association"])
	}
	if got := associationDefaults["granularity"]; got != syncGranularitySecretPath {
		t.Fatalf("defaults.association.granularity = %v, want %s", got, syncGranularitySecretPath)
	}
	if got := associationDefaults["format"]; got != defaultAssociationFormat {
		t.Fatalf("defaults.association.format = %v, want %s", got, defaultAssociationFormat)
	}
	if got := associationDefaults["data_mapping"]; got != defaultDataMapping {
		t.Fatalf("defaults.association.data_mapping = %v, want %s", got, defaultDataMapping)
	}
	if got := associationDefaults["delete_mode"]; got != defaultDeleteMode {
		t.Fatalf("defaults.association.delete_mode = %v, want %s", got, defaultDeleteMode)
	}
	if got := associationDefaults["enabled"]; got != true {
		t.Fatalf("defaults.association.enabled = %v, want true", got)
	}

	providers, ok := resp.Data["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers = %T, want map", resp.Data["providers"])
	}
	for _, providerType := range []string{"aws-sm", "fake", "gitlab", "k8s"} {
		if _, ok := providers[providerType]; !ok {
			t.Fatalf("providers missing %s: %#v", providerType, providers)
		}
	}
	fakeProvider, ok := providers["fake"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers.fake = %T, want map", providers["fake"])
	}
	capabilities, ok := fakeProvider["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers.fake.capabilities = %T, want map", fakeProvider["capabilities"])
	}
	if got := capabilities["supports_secret_key"]; got != true {
		t.Fatalf("fake supports_secret_key = %v, want true", got)
	}
	if got := capabilities["max_payload_bytes"]; got != 1024*1024 {
		t.Fatalf("fake max_payload_bytes = %v, want %d", got, 1024*1024)
	}
}

func TestProductionInfoResponseOmitsFakeProvider(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	resp := handleRequest(t, b, &logical.InmemStorage{}, logical.ReadOperation, "info", nil)
	assertNoErrorResponse(t, resp)

	providers, ok := resp.Data["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers = %T, want map", resp.Data["providers"])
	}
	for _, providerType := range []string{"aws-sm", "gitlab", "k8s"} {
		if _, ok := providers[providerType]; !ok {
			t.Fatalf("providers missing %s: %#v", providerType, providers)
		}
	}
	if _, ok := providers["fake"]; ok {
		t.Fatalf("production providers include fake: %#v", providers)
	}
}

func TestProductionBackendRejectsFakeDestination(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "destinations/fake/default",
		Storage:   storage,
		Data: map[string]interface{}{
			"description": "test destination",
		},
	})
	if err != nil {
		t.Fatalf("write fake destination returned backend error: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("write fake destination response = %#v, want error", resp)
	}
}
