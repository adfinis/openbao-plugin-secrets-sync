package backend

import (
	"context"
	"testing"

	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

const testAppUsername = "app"

func TestAssociationDataMapDispatchesSourceKeys(t *testing.T) {
	env := newBackendTestEnv(t)
	provider := &capturingDataMapProvider{}
	env.b.providerRegistry = providers.MustNewRegistry(provider)

	env.writeAppDBSecretData(map[string]interface{}{
		"username": testAppUsername,
		"password": "secret",
	})
	env.enableAppDBSourceSync()
	resp := env.update("destinations/data-map/prod")
	assertNilOrNoErrorResponse(t, resp)

	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":       destinationRef("data-map", "prod"),
		"resolved_name":     "app-db",
		"data_mapping":      dataMappingSourceKeys,
		"data_key_template": "APP_{{ key }}",
		"delete_mode":       deleteModeDelete,
	})
	assertNilOrNoErrorResponse(t, associationResp)
	assertResponseValue(t, associationResp, "data_mapping", dataMappingSourceKeys)
	assertResponseValue(t, associationResp, "data_key_template", "APP_{{ key }}")
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

	env.acknowledgeRestoreGuard()
	drainResp := env.update("queue/drain", map[string]interface{}{"max_operations": 1})
	assertNoErrorResponse(t, drainResp)

	if provider.lastUpsert == nil {
		t.Fatal("provider upsert was not called")
	}
	request := *provider.lastUpsert
	if request.Format != payloadpkg.FormatDataMap {
		t.Fatalf("format = %s, want %s", request.Format, payloadpkg.FormatDataMap)
	}
	if got := string(request.DataMap["APP_username"]); got != testAppUsername {
		t.Fatalf("APP_username = %q, want %s", got, testAppUsername)
	}
	if got := string(request.DataMap["APP_password"]); got != "secret" {
		t.Fatalf("APP_password = %q, want secret", got)
	}
	if request.PayloadSHA256 == "" || len(request.Payload) == 0 {
		t.Fatal("data-map dispatch must include canonical payload bytes and hash")
	}

	status, err := getStatus(context.Background(), env.storage, "app/db", request.AssociationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status == nil || status.LastOperationID != operationID {
		t.Fatalf("status = %#v, want operation %s", status, operationID)
	}
}

func TestAssociationDataMapRequiresProviderCapability(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecretData(map[string]interface{}{"username": testAppUsername})
	env.enableAppDBSourceSync()
	env.createFakeDestination("default")

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":       destinationRef(providerTypeFake, "default"),
		"resolved_name":     "app-db",
		"data_mapping":      dataMappingSourceKeys,
		"data_key_template": defaultDataKeyTemplate,
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("association response = %#v, want provider capability error", resp)
	}
}

func TestAssociationDataMapRejectsUnsupportedSourceValues(t *testing.T) {
	env := newBackendTestEnv(t)
	provider := &capturingDataMapProvider{}
	env.b.providerRegistry = providers.MustNewRegistry(provider)

	env.writeAppDBSecretData(map[string]interface{}{
		"config": map[string]interface{}{"nested": "value"},
	})
	env.enableAppDBSourceSync()
	resp := env.update("destinations/data-map/prod")
	assertNilOrNoErrorResponse(t, resp)

	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":       destinationRef("data-map", "prod"),
		"resolved_name":     "app-db",
		"data_mapping":      dataMappingSourceKeys,
		"data_key_template": defaultDataKeyTemplate,
	})
	if associationResp == nil || !associationResp.IsError() {
		t.Fatalf("association response = %#v, want source value validation error", associationResp)
	}
}

type capturingDataMapProvider struct {
	lastUpsert *providers.UpsertRequest
}

func (p *capturingDataMapProvider) Type() string {
	return "data-map"
}

func (p *capturingDataMapProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		SupportsDataMap:             true,
		MaxPayloadBytes:             1024 * 1024,
	}
}

func (*capturingDataMapProvider) ValidateConfig(context.Context, providers.DestinationConfig) error {
	return nil
}

func (*capturingDataMapProvider) NormalizeAssociationConfig(
	context.Context,
	providers.DestinationConfig,
	providers.AssociationConfig,
) (providers.AssociationConfig, error) {
	return providers.AssociationConfig{Config: map[string]string{}}, nil
}

func (p *capturingDataMapProvider) OpenDestination(
	context.Context,
	providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	return capturingDataMapRuntime{provider: p}, nil
}

type capturingDataMapRuntime struct {
	provider *capturingDataMapProvider
}

func (capturingDataMapRuntime) Health(context.Context) (*providers.HealthResult, error) {
	return &providers.HealthResult{Healthy: true}, nil
}

func (capturingDataMapRuntime) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
}

func (r capturingDataMapRuntime) Upsert(_ context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	copyReq := req
	copyReq.Payload = append([]byte(nil), req.Payload...)
	copyReq.DataMap = copyByteMap(req.DataMap)
	r.provider.lastUpsert = &copyReq
	return &providers.SyncResult{RemoteVersion: "rv-data-map"}, nil
}

func (capturingDataMapRuntime) Delete(context.Context, providers.DeleteRequest) (*providers.SyncResult, error) {
	return &providers.SyncResult{RemoteVersion: "rv-delete"}, nil
}

func (capturingDataMapRuntime) ReadState(context.Context, providers.ReadStateRequest) (*providers.RemoteState, error) {
	return &providers.RemoteState{Exists: false}, nil
}

func (capturingDataMapRuntime) Close(context.Context) error {
	return nil
}

func copyByteMap(input map[string][]byte) map[string][]byte {
	output := make(map[string][]byte, len(input))
	for key, value := range input {
		output[key] = append([]byte(nil), value...)
	}
	return output
}
