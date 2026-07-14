package backend

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestConfigDefaults(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}
	req := &logical.Request{
		Operation: logical.ReadOperation,
		Path:      configPath,
		Storage:   storage,
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if resp == nil {
		t.Fatal("response must not be nil")
	}
	assertResponseValue(t, resp, "restore_guard", false)
	if got := resp.Data["restore_guard_acknowledged_time"]; got == "" {
		t.Fatal("restore_guard_acknowledged_time must be set for fresh mounts")
	}
	assertResponseValue(t, resp, "security_posture", securityPostureStandard)
	assertResponseValue(t, resp, "drift_repair", driftRepairOff)
	assertResponseValue(t, resp, "drift_reconcile_interval", defaultDriftInterval)
	assertResponseValue(t, resp, "drift_reconcile_batch", defaultDriftBatch)
	assertResponseValue(t, resp, "event_dispatch_enabled", true)
	assertResponseValue(t, resp, "event_dispatch_max_operations", defaultEventDispatchMaxOperations)
	assertResponseValue(t, resp, "storage_schema_version", currentStorageSchema)
	assertResponseValue(t, resp, "storage_schema_min_compatible_version", minSupportedStorageSchema)
	if got, ok := resp.Data["plugin_instance_id"].(string); !ok || !strings.HasPrefix(got, "inst-") {
		t.Fatalf("plugin_instance_id = %v, want inst-*", resp.Data["plugin_instance_id"])
	}
	if got, ok := resp.Data["restore_epoch"].(string); !ok || !strings.HasPrefix(got, "epoch-") {
		t.Fatalf("restore_epoch = %v, want epoch-*", resp.Data["restore_epoch"])
	}
}

func TestConfigInitializesDefaultsForExistingStorageWithoutConfig(t *testing.T) {
	env := newBackendTestEnv(t)
	metadata := newMetadataRecord()
	if err := putMetadata(context.Background(), env.storage, "app/db", metadata); err != nil {
		t.Fatalf("write existing metadata fixture: %v", err)
	}

	resp := env.read(configPath)
	assertNoErrorResponse(t, resp)
	assertResponseValue(t, resp, "restore_guard", false)
	if got := resp.Data["restore_guard_acknowledged_time"]; got == "" {
		t.Fatal("restore_guard_acknowledged_time must be set")
	}
	assertResponseValue(t, resp, "security_posture", securityPostureStandard)
	assertResponseValue(t, resp, "queue_capacity", defaultQueueCapacity)
	assertResponseValue(t, resp, "drift_repair", driftRepairOff)
	assertResponseValue(t, resp, "drift_reconcile_interval", defaultDriftInterval)
	assertResponseValue(t, resp, "drift_reconcile_batch", defaultDriftBatch)
	assertResponseValue(t, resp, "event_dispatch_enabled", true)
	assertResponseValue(t, resp, "event_dispatch_max_operations", defaultEventDispatchMaxOperations)
}

func TestConfigDecodesMissingOptionalFieldsAsDefaults(t *testing.T) {
	env := newBackendTestEnv(t)
	entry, err := logical.StorageEntryJSON(configPath, map[string]interface{}{
		"restore_guard":  false,
		"queue_capacity": 0,
	})
	if err != nil {
		t.Fatalf("build config entry: %v", err)
	}
	if err := env.storage.Put(context.Background(), entry); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}

	resp := env.read(configPath)
	assertNoErrorResponse(t, resp)
	assertResponseValue(t, resp, "restore_guard", false)
	assertResponseValue(t, resp, "security_posture", securityPostureStandard)
	assertResponseValue(t, resp, "queue_capacity", 0)
	assertResponseValue(t, resp, "drift_repair", driftRepairOff)
	assertResponseValue(t, resp, "drift_reconcile_interval", defaultDriftInterval)
	assertResponseValue(t, resp, "drift_reconcile_batch", defaultDriftBatch)
	assertResponseValue(t, resp, "event_dispatch_enabled", true)
	assertResponseValue(t, resp, "event_dispatch_max_operations", defaultEventDispatchMaxOperations)

	cfg, err := readGlobalConfig(context.Background(), env.storage)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if cfg.QueueCapacity != 0 {
		t.Fatalf("queue_capacity = %d, want 0", cfg.QueueCapacity)
	}
	if !cfg.EventDispatchEnabled {
		t.Fatal("missing event_dispatch_enabled must decode as true")
	}
	if cfg.EventDispatchMaxOperations != defaultEventDispatchMaxOperations {
		t.Fatalf(
			"event_dispatch_max_operations = %d, want %d",
			cfg.EventDispatchMaxOperations,
			defaultEventDispatchMaxOperations,
		)
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
	assertResponseValue(t, readResp, "restore_guard", false)
	assertResponseValue(t, readResp, "security_posture", securityPostureStandard)
	assertResponseValue(t, readResp, "drift_repair", driftRepairOff)
	assertResponseValue(t, readResp, "drift_reconcile_interval", defaultDriftInterval)
	assertResponseValue(t, readResp, "drift_reconcile_batch", defaultDriftBatch)
	assertResponseValue(t, readResp, "event_dispatch_enabled", true)
	assertResponseValue(t, readResp, "event_dispatch_max_operations", defaultEventDispatchMaxOperations)

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

func TestConcurrentConfigWritesPreservePartialUpdates(t *testing.T) {
	env := newBackendTestEnv(t)
	env.read(configPath)
	storage := delayedConfigReadStorage{
		Storage: env.storage,
		delay:   25 * time.Millisecond,
	}
	requests := []*logical.Request{
		{
			Operation: logical.UpdateOperation,
			Path:      configPath,
			Storage:   storage,
			Data:      map[string]interface{}{"disabled": true},
		},
		{
			Operation: logical.UpdateOperation,
			Path:      configPath,
			Storage:   storage,
			Data:      map[string]interface{}{"queue_capacity": 17},
		},
	}
	start := make(chan struct{})
	results := make(chan error, len(requests))
	var ready sync.WaitGroup
	ready.Add(len(requests))
	for _, req := range requests {
		go func() {
			ready.Done()
			<-start
			resp, err := env.b.Backend.HandleRequest(context.Background(), req)
			if err == nil && resp != nil && resp.IsError() {
				err = resp.Error()
			}
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	for range requests {
		if err := <-results; err != nil {
			t.Fatalf("concurrent config write: %v", err)
		}
	}

	cfg, err := readGlobalConfig(context.Background(), env.storage)
	if err != nil {
		t.Fatalf("read config after concurrent writes: %v", err)
	}
	if !cfg.Disabled {
		t.Fatal("concurrent config write lost disabled=true")
	}
	if got := cfg.QueueCapacity; got != 17 {
		t.Fatalf("queue_capacity after concurrent writes = %d, want 17", got)
	}
}

type delayedConfigReadStorage struct {
	logical.Storage

	delay time.Duration
}

func (storage delayedConfigReadStorage) Get(ctx context.Context, key string) (*logical.StorageEntry, error) {
	entry, err := storage.Storage.Get(ctx, key)
	if err == nil && key == configPath {
		time.Sleep(storage.delay)
	}
	return entry, err
}

func TestConfigWriteRejectsUnknownFields(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update(configPath, map[string]interface{}{
		"unexpected": true,
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("unknown config field response = %#v, want error", resp)
	}
	if !strings.Contains(resp.Error().Error(), `unknown config field "unexpected"`) {
		t.Fatalf("unknown config field error = %q", resp.Error().Error())
	}
}

func TestConfigWriteSecurityPosture(t *testing.T) {
	env := newBackendTestEnv(t)

	hardenResp := env.update(configPath, map[string]interface{}{
		"security_posture": securityPostureHardened,
	})
	if hardenResp != nil && hardenResp.IsError() {
		t.Fatalf("unexpected hardened posture config write error: %v", hardenResp.Error())
	}
	readResp := env.read(configPath)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "security_posture", securityPostureHardened)

	relaxResp := env.update(configPath, map[string]interface{}{
		"security_posture": securityPostureStandard,
	})
	if relaxResp != nil && relaxResp.IsError() {
		t.Fatalf("unexpected standard posture config write error: %v", relaxResp.Error())
	}
	relaxedReadResp := env.read(configPath)
	assertNoErrorResponse(t, relaxedReadResp)
	assertResponseValue(t, relaxedReadResp, "security_posture", securityPostureStandard)
}

func TestConfigWriteRejectsUnknownSecurityPosture(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update(configPath, map[string]interface{}{
		"security_posture": "strict",
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("unknown security posture response = %#v, want error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "security_posture must be standard or hardened") {
		t.Fatalf("unknown security posture error = %q", resp.Error().Error())
	}
}

func TestConfigWriteValidatesEventDispatch(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update(configPath, map[string]interface{}{
		"event_dispatch_enabled":        false,
		"event_dispatch_max_operations": 3,
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected event dispatch config write error: %v", writeResp.Error())
	}
	readResp := env.read(configPath)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "event_dispatch_enabled", false)
	assertResponseValue(t, readResp, "event_dispatch_max_operations", 3)

	zeroResp := env.update(configPath, map[string]interface{}{
		"event_dispatch_max_operations": 0,
	})
	if zeroResp == nil || !zeroResp.IsError() {
		t.Fatalf("zero event_dispatch_max_operations response = %#v, want error", zeroResp)
	}
}

func TestConfigWriteValidatesDriftRepair(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update(configPath, map[string]interface{}{
		"drift_repair":             driftRepairRepair,
		"drift_reconcile_interval": "2h",
		"drift_reconcile_batch":    3,
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected drift config write error: %v", writeResp.Error())
	}
	readResp := env.read(configPath)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "drift_repair", driftRepairRepair)
	assertResponseValue(t, readResp, "drift_reconcile_interval", "2h")
	assertResponseValue(t, readResp, "drift_reconcile_batch", 3)

	invalidModeResp := env.update(configPath, map[string]interface{}{
		"drift_repair": "overwrite",
	})
	if invalidModeResp == nil || !invalidModeResp.IsError() {
		t.Fatalf("invalid drift_repair response = %#v, want error", invalidModeResp)
	}
	shortIntervalResp := env.update(configPath, map[string]interface{}{
		"drift_reconcile_interval": "30s",
	})
	if shortIntervalResp == nil || !shortIntervalResp.IsError() {
		t.Fatalf("short drift_reconcile_interval response = %#v, want error", shortIntervalResp)
	}
	zeroBatchResp := env.update(configPath, map[string]interface{}{
		"drift_reconcile_batch": 0,
	})
	if zeroBatchResp == nil || !zeroBatchResp.IsError() {
		t.Fatalf("zero drift_reconcile_batch response = %#v, want error", zeroBatchResp)
	}
}

func TestConfigRestoreGuardAcknowledge(t *testing.T) {
	env := newBackendTestEnv(t)

	initialResp := env.read(configPath)
	assertNoErrorResponse(t, initialResp)
	initialEpoch := initialResp.Data["restore_epoch"].(string)
	assertResponseValue(t, initialResp, "restore_guard", false)

	ackResp := env.update("config/restore-guard/acknowledge")
	assertNoErrorResponse(t, ackResp)
	assertResponseValue(t, ackResp, "restore_guard", false)
	if got := ackResp.Data["restore_guard_acknowledged_time"]; got == "" {
		t.Fatal("restore_guard_acknowledged_time must be set")
	}
	ackEpoch := ackResp.Data["restore_epoch"].(string)
	if ackEpoch != initialEpoch {
		t.Fatalf("restore_epoch after fresh-mount acknowledgement = %q, want unchanged %q", ackEpoch, initialEpoch)
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

	ackResp = env.update("config/restore-guard/acknowledge")
	assertNoErrorResponse(t, ackResp)
	ackEpoch = ackResp.Data["restore_epoch"].(string)
	if ackEpoch == "" || ackEpoch == initialEpoch {
		t.Fatalf("restore_epoch after guarded acknowledgement = %q, want new epoch distinct from %q", ackEpoch, initialEpoch)
	}
}
