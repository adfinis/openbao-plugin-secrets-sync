package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestConfigDefaults(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	req := &logical.Request{
		Operation: logical.ReadOperation,
		Path:      configPath,
		Storage:   &logical.InmemStorage{},
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if resp == nil {
		t.Fatal("response must not be nil")
	}
	assertResponseValue(t, resp, "restore_guard", true)
	assertResponseValue(t, resp, "restore_guard_acknowledged_time", "")
	assertResponseValue(t, resp, "storage_schema_version", currentStorageSchema)
	assertResponseValue(t, resp, "storage_schema_min_compatible_version", minSupportedStorageSchema)
	if got, ok := resp.Data["plugin_instance_id"].(string); !ok || !strings.HasPrefix(got, "inst-") {
		t.Fatalf("plugin_instance_id = %v, want inst-*", resp.Data["plugin_instance_id"])
	}
	if got, ok := resp.Data["restore_epoch"].(string); !ok || !strings.HasPrefix(got, "epoch-") {
		t.Fatalf("restore_epoch = %v, want epoch-*", resp.Data["restore_epoch"])
	}
}

func TestConfigWriteMergesDefaultsAndValidatesQueueCapacity(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": 12,
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected config write error: %v", writeResp.Error())
	}

	readResp := env.read(configPath)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "queue_capacity", 12)
	assertResponseValue(t, readResp, "restore_guard", true)

	zeroResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": 0,
	})
	if zeroResp != nil && zeroResp.IsError() {
		t.Fatalf("unexpected zero queue_capacity error: %v", zeroResp.Error())
	}
	readZeroResp := env.read(configPath)
	assertNoErrorResponse(t, readZeroResp)
	assertResponseValue(t, readZeroResp, "queue_capacity", 0)

	negativeResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": -1,
	})
	if negativeResp == nil || !negativeResp.IsError() {
		t.Fatalf("negative queue_capacity response = %#v, want error", negativeResp)
	}
}

func TestConfigRestoreGuardAcknowledge(t *testing.T) {
	env := newBackendTestEnv(t)

	initialResp := env.read(configPath)
	assertNoErrorResponse(t, initialResp)
	initialEpoch := initialResp.Data["restore_epoch"].(string)

	ackResp := env.update("config/restore-guard/acknowledge")
	assertNoErrorResponse(t, ackResp)
	assertResponseValue(t, ackResp, "restore_guard", false)
	if got := ackResp.Data["restore_guard_acknowledged_time"]; got == "" {
		t.Fatal("restore_guard_acknowledged_time must be set")
	}
	ackEpoch := ackResp.Data["restore_epoch"].(string)
	if ackEpoch == "" || ackEpoch == initialEpoch {
		t.Fatalf("restore_epoch after acknowledgement = %q, want new epoch distinct from %q", ackEpoch, initialEpoch)
	}

	readResp := env.read(configPath)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "restore_guard", false)
	assertResponseValue(t, readResp, "restore_guard_acknowledged_time", ackResp.Data["restore_guard_acknowledged_time"])
	assertResponseValue(t, readResp, "restore_epoch", ackEpoch)

	repeatedAckResp := env.update("config/restore-guard/acknowledge")
	assertNoErrorResponse(t, repeatedAckResp)
	assertResponseValue(t, repeatedAckResp, "restore_epoch", ackEpoch)

	rearmResp := env.update(configPath, map[string]interface{}{
		"restore_guard": true,
	})
	if rearmResp != nil && rearmResp.IsError() {
		t.Fatalf("unexpected restore guard rearm error: %v", rearmResp.Error())
	}
	readResp = env.read(configPath)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "restore_guard", true)
	assertResponseValue(t, readResp, "restore_guard_acknowledged_time", "")
}
