package backend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/helper/consts"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestEventDispatchProcessesAssociationEnqueue(t *testing.T) {
	env := newEventDispatchTestEnv(t, nil)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

	waitForOutboxMissing(t, env.storage, operationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestEventDispatchProcessesSourceWriteEnqueue(t *testing.T) {
	env := newEventDispatchTestEnv(t, nil)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	initialOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	waitForOutboxMissing(t, env.storage, initialOperationID)

	writeResp := env.writeAppDBSecret("rotated")
	metadata := writeResp.Data["metadata"].(map[string]interface{})
	operationID := requireSingleOperationID(t, operationIDsFromMetadata(t, metadata), "source write")

	waitForOutboxMissing(t, env.storage, operationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestEventDispatchDrainsBurstPastPerPassLimit(t *testing.T) {
	env := newEventDispatchTestEnv(t, nil)
	env.update(configPath, map[string]interface{}{
		"event_dispatch_enabled":        false,
		"event_dispatch_max_operations": 3,
	})
	env.createFakeDestination("default")

	operationIDs := make([]string, 0, 8)
	for i := range 8 {
		path := fmt.Sprintf("app/burst-%02d", i)
		associationResp := env.createFakeAssociationForPath(path)
		operationIDs = append(operationIDs, requireSingleOperationID(
			t,
			operationIDsFromResponse(t, associationResp),
			path,
		))
	}

	env.update(configPath, map[string]interface{}{
		"event_dispatch_enabled": true,
	})
	for _, operationID := range operationIDs {
		waitForOutboxMissing(t, env.storage, operationID)
	}
	assertQueueCount(t, env.b, env.storage, "pending", 0)
	assertQueueCount(t, env.b, env.storage, "retry_wait", 0)
}

func TestEventDispatchWakesWhenRetryBecomesDue(t *testing.T) {
	env := newEventDispatchTestEnv(t, nil)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/unavailable/app/db")
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

	waitForCondition(t, "initial retry-wait operation", func() bool {
		record, err := getOutbox(context.Background(), env.storage, operationID)
		if err != nil {
			t.Fatalf("read outbox operation: %v", err)
		}
		return record != nil && record.State == outboxStateRetryWait && record.Attempts == 1
	})
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassUnavailable)

	operation.NotBefore = time.Now().UTC().Add(50 * time.Millisecond).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("shorten retry wait: %v", err)
	}
	env.b.signalEventDispatch()

	waitForCondition(t, "event retry attempt", func() bool {
		record, err := getOutbox(context.Background(), env.storage, operationID)
		if err != nil {
			t.Fatalf("read outbox operation: %v", err)
		}
		return record != nil && record.Attempts >= 2
	})
	operation = assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if got := operation.Attempts; got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestEventDispatchHonorsDisabledConfigAndResumes(t *testing.T) {
	env := newEventDispatchTestEnv(t, nil)
	env.update(configPath, map[string]interface{}{
		"disabled": true,
	})

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	assertOutboxStillPendingAfterSettle(t, env.storage, operationID)

	env.update(configPath, map[string]interface{}{
		"disabled": false,
	})
	waitForOutboxMissing(t, env.storage, operationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestEventDispatchHonorsRestoreGuardAndResumesOnAcknowledge(t *testing.T) {
	env := newEventDispatchTestEnv(t, nil)
	env.update(configPath, map[string]interface{}{
		"restore_guard": true,
	})

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	assertOutboxStillPendingAfterSettle(t, env.storage, operationID)

	env.update("config/restore-guard/acknowledge")
	waitForOutboxMissing(t, env.storage, operationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestEventDispatchCanBeDisabledAndReEnabled(t *testing.T) {
	env := newEventDispatchTestEnv(t, nil)
	env.update(configPath, map[string]interface{}{
		"event_dispatch_enabled": false,
	})

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	assertOutboxStillPendingAfterSettle(t, env.storage, operationID)

	env.update(configPath, map[string]interface{}{
		"event_dispatch_enabled": true,
	})
	waitForOutboxMissing(t, env.storage, operationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestEventDispatchSkipsUnsafeReplicationNode(t *testing.T) {
	env := newEventDispatchTestEnv(t, &logical.BackendConfig{
		System: &logical.StaticSystemView{
			ReplicationStateVal: consts.ReplicationPerformanceSecondary,
		},
	})

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	assertOutboxStillPendingAfterSettle(t, env.storage, operationID)
}

func newEventDispatchTestEnv(t *testing.T, config *logical.BackendConfig) *backendTestEnv {
	t.Helper()
	env := newBackendTestEnv(t)
	if config == nil {
		config = &logical.BackendConfig{}
	}
	if config.StorageView == nil {
		config.StorageView = env.storage
	}
	if err := env.b.Setup(context.Background(), config); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	if err := env.b.Initialize(context.Background(), &logical.InitializationRequest{
		Storage: env.storage,
	}); err != nil {
		t.Fatalf("initialize backend: %v", err)
	}
	t.Cleanup(func() {
		env.b.Cleanup(context.Background())
	})
	return env
}

func waitForOutboxMissing(t *testing.T, storage logical.Storage, operationID string) {
	t.Helper()
	waitForCondition(t, "outbox operation to be pruned", func() bool {
		record, err := getOutbox(context.Background(), storage, operationID)
		if err != nil {
			t.Fatalf("read outbox operation: %v", err)
		}
		return record == nil
	})
}

func assertOutboxStillPendingAfterSettle(t *testing.T, storage logical.Storage, operationID string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)
}

func waitForCondition(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}
