package backend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/helper/consts"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestQueueDrainCancelsClaimedStaleUpsertAfterClaimExpiry(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	staleOperationID := operationIDsFromResponse(t, associationResp)[0]
	staleOperation := assertOutboxOperation(t, env.storage, staleOperationID, 1, outboxStatePending)
	staleOperation.ClaimOwner = "worker-stale"
	staleOperation.ClaimExpiresTime = nowUTC().Add(time.Hour).Format(timeFormatRFC3339)
	staleOperation.ClaimAttempt = 1
	if err := putOutbox(context.Background(), env.storage, *staleOperation); err != nil {
		t.Fatalf("write claimed stale operation: %v", err)
	}

	rotatedResp := env.writeAppDBSecret("rotated")
	rotatedOperationID := requireSingleOperationID(
		t,
		operationIDsFromMetadata(t, rotatedResp.Data["metadata"].(map[string]interface{})),
		"rotated write",
	)
	assertOutboxOperation(t, env.storage, staleOperationID, 1, outboxStatePending)
	assertOutboxOperation(t, env.storage, rotatedOperationID, 2, outboxStatePending)

	staleOperation, err := getOutbox(context.Background(), env.storage, staleOperationID)
	if err != nil {
		t.Fatalf("read stale operation: %v", err)
	}
	staleOperation.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	staleOperation.NotBefore = outboxDueZeroTime
	if err := putOutbox(context.Background(), env.storage, *staleOperation); err != nil {
		t.Fatalf("expire stale operation claim: %v", err)
	}

	env.acknowledgeRestoreGuard()
	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	assertResponseValue(t, drainResp, "processed", 1)
	assertOutboxMissing(t, env.storage, staleOperationID)
	assertOutboxOperation(t, env.storage, rotatedOperationID, 2, outboxStatePending)
	status, err := getStatus(
		context.Background(),
		env.storage,
		"app/db",
		associationIDFromResponse(t, associationResp),
		syncObjectIDSecretPath,
	)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want nil before rotated operation dispatch", status)
	}
}

func TestQueueCapacityRejectsWriteBeforeVersionCommit(t *testing.T) {
	env := newBackendTestEnv(t)

	env.update("config", map[string]interface{}{
		"queue_capacity": 1,
		"restore_guard":  true,
	})
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.enableAppDBSourceSync()
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoErrorResponse(t, associationResp)
	now := nowUTC().Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, outboxRecord{
		ID:             "op-unrelated-1",
		Type:           outbox.OperationTypeUpsert,
		Path:           "other/db",
		Version:        1,
		AssociationID:  "assoc-unrelated",
		ObjectID:       syncObjectIDSecretPath,
		DestinationRef: "fake/default",
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
	}); err != nil {
		t.Fatalf("write unrelated outbox operation: %v", err)
	}
	associationID := associationIDFromResponse(t, associationResp)
	association, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	if association == nil {
		t.Fatal("association must exist")
	}
	association.Enabled = true
	if err := putAssociation(context.Background(), env.storage, *association); err != nil {
		t.Fatalf("enable association fixture: %v", err)
	}

	secondResp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
	})
	if !secondResp.IsError() {
		t.Fatal("write must fail when queue is full")
	}

	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("blocked write committed version = %v, want 1", got)
	}
}

func TestQueueCapacityZeroBlocksEnqueues(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createDefaultFakeAssociation()
	env.runPeriodicAllowed("periodic")

	configResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": 0,
	})
	if configResp != nil && configResp.IsError() {
		t.Fatalf("unexpected config write error: %v", configResp.Error())
	}

	blockedResp := env.writeAppDBSecretDataNoAssert(map[string]interface{}{
		"password": "blocked",
	})
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("write with zero queue capacity response = %#v, want error", blockedResp)
	}
	assertHintContains(t, blockedResp.Data, "Queue capacity is exhausted")
	assertNextActionCommand(t, blockedResp.Data, "read_queue", "bao read <mount>/queue")
	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("blocked write committed version = %v, want 1", got)
	}
	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertResponseValue(t, queueResp, "capacity", 0)
}

func TestQueueOperationReadCancelAndPrune(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]

	readResp := env.read("queue/" + operationID)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "state", outboxStatePending)

	cancelResp := env.update("queue/" + operationID + "/cancel")
	assertNoErrorResponse(t, cancelResp)
	assertResponseValue(t, cancelResp, "state", outboxStateCanceled)
	assertOutboxMissing(t, env.storage, operationID)
	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertResponseValue(t, queueResp, "pending", 0)

	retryResp := env.update("queue/" + operationID + "/retry")
	if retryResp != nil {
		t.Fatalf("retry pruned operation response = %#v, want nil", retryResp)
	}
}

func TestQueueDrainProcessesDueOperations(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	env.acknowledgeRestoreGuard()

	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	assertResponseValue(t, drainResp, "processed", 1)
	queue := drainResp.Data["queue"].(map[string]interface{})
	if got := queue["pending"]; got != 0 {
		t.Fatalf("pending = %v, want 0", got)
	}
	assertOutboxMissing(t, env.storage, operationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestQueueDrainSkipsUnexpiredCurrentOwnerClaim(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	operation, err := getOutbox(context.Background(), env.storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	claimOwner, err := env.b.outboxClaimOwner(context.Background(), env.storage)
	if err != nil {
		t.Fatalf("claim owner: %v", err)
	}
	operation.ClaimOwner = claimOwner
	operation.ClaimExpiresTime = nowUTC().Add(time.Hour).Format(timeFormatRFC3339)
	operation.ClaimAttempt = 1
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("write claimed outbox operation: %v", err)
	}

	env.acknowledgeRestoreGuard()
	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	assertResponseValue(t, drainResp, "processed", 0)
	queue := drainResp.Data["queue"].(map[string]interface{})
	if got := queue["claimed"]; got != 1 {
		t.Fatalf("claimed = %v, want 1", got)
	}
	operation = assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if operation.ClaimOwner != claimOwner {
		t.Fatalf("claim_owner = %q, want %s", operation.ClaimOwner, claimOwner)
	}

	cancelResp := env.update("queue/" + operationID + "/cancel")
	if cancelResp == nil || !cancelResp.IsError() {
		t.Fatalf("cancel claimed operation response = %#v, want error", cancelResp)
	}
}

func TestQueueDrainPreservesUnexpiredOtherOwnerClaim(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	operation, err := getOutbox(context.Background(), env.storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	operation.ClaimOwner = "inst-old/worker-old"
	operation.ClaimExpiresTime = nowUTC().Add(time.Hour).Format(timeFormatRFC3339)
	operation.ClaimAttempt = 1
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("write claimed outbox operation: %v", err)
	}

	env.acknowledgeRestoreGuard()
	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	assertResponseValue(t, drainResp, "processed", 0)
	queue := drainResp.Data["queue"].(map[string]interface{})
	if got := queue["claimed"]; got != 1 {
		t.Fatalf("claimed = %v, want 1", got)
	}
	stored := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if got := stored.ClaimOwner; got != operation.ClaimOwner {
		t.Fatalf("claim owner = %q, want %q", got, operation.ClaimOwner)
	}
	if got := stored.ClaimExpiresTime; got != operation.ClaimExpiresTime {
		t.Fatalf("claim expiry = %q, want %q", got, operation.ClaimExpiresTime)
	}
	status, err := getStatus(
		context.Background(),
		env.storage,
		"app/db",
		associationIDFromResponse(t, associationResp),
		syncObjectIDSecretPath,
	)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want nil while operation remains claimed", status)
	}
}

func TestQueueOperationRetryRejectsClaimedOperation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/rate-limit/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.runPeriodicAllowed("periodic")
	claimOperationFixture(t, env.storage, operationID)

	retryResp := env.update("queue/" + operationID + "/retry")
	if retryResp == nil || !retryResp.IsError() {
		t.Fatalf("retry claimed operation response = %#v, want error", retryResp)
	}
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if operation.ClaimOwner == "" {
		t.Fatal("operation claim must remain active")
	}
}

func TestQueueDrainReclaimsExpiredClaimAndClearsIt(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	operation, err := getOutbox(context.Background(), env.storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	operation.ClaimOwner = "worker-stale"
	operation.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	operation.ClaimAttempt = 1
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("write expired claimed outbox operation: %v", err)
	}

	env.acknowledgeRestoreGuard()
	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	assertResponseValue(t, drainResp, "processed", 1)
	assertOutboxMissing(t, env.storage, operationID)

	readResp := env.read("queue/" + operationID)
	if readResp != nil {
		t.Fatalf("read pruned operation response = %#v, want nil", readResp)
	}
}

func TestQueueDrainClearsClaimAfterRetryableFailure(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/rate-limit/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.runPeriodicAllowed("periodic")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if operation.ClaimOwner != "" || operation.ClaimExpiresTime != "" || operation.ClaimAttempt != 0 {
		t.Fatalf("claim fields after retryable failure = %#v, want cleared", operation)
	}
}

func TestQueueDrainSkipsFutureRetryWaitOperation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/rate-limit/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.runPeriodicAllowed("periodic")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	assertFutureNotBefore(t, operation.NotBefore)

	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	assertResponseValue(t, drainResp, "processed", 0)
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
}

func TestQueueDrainHonorsRestoreGuard(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	rearmResp := env.update("config", map[string]interface{}{
		"restore_guard": true,
	})
	if rearmResp != nil && rearmResp.IsError() {
		t.Fatalf("unexpected restore guard rearm error: %v", rearmResp.Error())
	}

	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	if drainResp == nil || !drainResp.IsError() {
		t.Fatalf("drain restore guard response = %#v, want error", drainResp)
	}
	assertHintContains(t, drainResp.Data, "Restore guard is active")
	assertNextActionCommand(
		t,
		drainResp.Data,
		"acknowledge_restore_guard",
		"bao write -force <mount>/config/restore-guard/acknowledge",
	)
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)

	env.acknowledgeRestoreGuard()
	drainResp = env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	assertResponseValue(t, drainResp, "processed", 1)
}

func TestQueueDrainRejectsUnsafeReplicationNode(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	env.acknowledgeRestoreGuard()

	if err := env.b.Setup(context.Background(), &logical.BackendConfig{
		System: &logical.StaticSystemView{
			ReplicationStateVal: consts.ReplicationPerformanceSecondary,
		},
	}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	drainResp := env.update("queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	if drainResp == nil || !drainResp.IsError() {
		t.Fatalf("drain unsafe replication response = %#v, want error", drainResp)
	}
	if !strings.Contains(drainResp.Error().Error(), remoteMutationUnsafeError) {
		t.Fatalf("drain unsafe replication error = %q, want %q", drainResp.Error().Error(), remoteMutationUnsafeError)
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
}

func TestQueueDrainForwardsPerformanceStandbyBeforeCallback(t *testing.T) {
	env := newBackendTestEnv(t)
	recorder := &recordingObserver{}
	env.b.observer = recorder

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	env.acknowledgeRestoreGuard()

	if err := env.b.Setup(context.Background(), &logical.BackendConfig{
		System: &logical.StaticSystemView{
			ReplicationStateVal: consts.ReplicationPerformanceStandby,
		},
	}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "queue/drain",
		Storage:   env.storage,
		Data:      map[string]interface{}{"max_operations": 1},
	})
	if resp != nil {
		t.Fatalf("queue drain response = %#v, want nil forwarding response", resp)
	}
	if !errors.Is(err, logical.ErrReadOnly) {
		t.Fatalf("queue drain error = %v, want %v", err, logical.ErrReadOnly)
	}
	if len(recorder.remoteMutationBlocked) != 0 {
		t.Fatalf(
			"remote mutation blocked events = %#v, want none before drain callback",
			recorder.remoteMutationBlocked,
		)
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
}

func TestQueueSummaryOldestAge(t *testing.T) {
	env := newBackendTestEnv(t)

	configResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": 2,
	})
	if configResp != nil && configResp.IsError() {
		t.Fatalf("unexpected config write error: %v", configResp.Error())
	}
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	operation, err := getOutbox(context.Background(), env.storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	operation.CreatedTime = nowUTC().Add(-2 * time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("write old outbox operation: %v", err)
	}

	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["oldest_age_seconds"].(int); got < 120 {
		t.Fatalf("oldest_age_seconds = %v, want at least 120", got)
	}
	assertResponseValue(t, queueResp, "capacity", 2)
	assertResponseValue(t, queueResp, "utilization", 0.5)
}

func TestQueueDrainHonorsDisabledConfig(t *testing.T) {
	env := newBackendTestEnv(t)

	configResp := env.update(configPath, map[string]interface{}{
		"disabled": true,
	})
	if configResp != nil && configResp.IsError() {
		t.Fatalf("unexpected config write error: %v", configResp.Error())
	}

	drainResp := env.update("queue/drain")
	if drainResp == nil || !drainResp.IsError() {
		t.Fatalf("drain disabled response = %#v, want error", drainResp)
	}
}

func TestQueueOperationPrunesAfterSuccess(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	env.runPeriodicAllowed("periodic")

	assertOutboxMissing(t, env.storage, operationID)
	readResp := env.read("queue/" + operationID)
	if readResp != nil {
		t.Fatalf("read pruned operation response = %#v, want nil", readResp)
	}
	retryResp := env.update("queue/" + operationID + "/retry")
	if retryResp != nil {
		t.Fatalf("retry pruned operation response = %#v, want nil", retryResp)
	}
	cancelResp := env.update("queue/" + operationID + "/cancel")
	if cancelResp != nil {
		t.Fatalf("cancel pruned operation response = %#v, want nil", cancelResp)
	}
}

func TestQueueCapacityCountsQueuedOperationsOnly(t *testing.T) {
	env := newBackendTestEnv(t)

	env.update(configPath, map[string]interface{}{
		"queue_capacity": 1,
	})
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createDefaultFakeAssociation()
	env.runPeriodicAllowed("periodic")

	secondResp := env.writeAppDBSecret("allowed")
	metadata := secondResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 2 {
		t.Fatalf("second write version = %v, want 2", got)
	}
	assertPrunedEnqueueIntentAndOutbox(t, env.storage, "app/db", 2, metadata)
}
