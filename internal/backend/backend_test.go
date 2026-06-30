package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
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
	if got := resp.Data["restore_guard"]; got != true {
		t.Fatalf("restore_guard default = %v, want true", got)
	}
}

func TestConfigWriteMergesDefaultsAndValidatesQueueCapacity(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"queue_capacity": 12,
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected config write error: %v", writeResp.Error())
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, configPath, nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["queue_capacity"]; got != 12 {
		t.Fatalf("queue_capacity = %v, want 12", got)
	}
	if got := readResp.Data["restore_guard"]; got != true {
		t.Fatalf("restore_guard = %v, want true", got)
	}

	negativeResp := handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"queue_capacity": -1,
	})
	if negativeResp == nil || !negativeResp.IsError() {
		t.Fatalf("negative queue_capacity response = %#v, want error", negativeResp)
	}
}

func TestDataWriteReadAndQueueStatus(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"username": "app",
			"password": "secret",
		},
	})
	assertNoErrorResponse(t, writeResp)
	writeMetadata := writeResp.Data["metadata"].(map[string]interface{})
	if got := writeMetadata["version"]; got != 1 {
		t.Fatalf("write version = %v, want 1", got)
	}
	if got := writeMetadata["sync_state"]; got != string(domain.SyncStatePending) {
		t.Fatalf("sync state = %v, want %s", got, domain.SyncStatePending)
	}
	assertCompleteEnqueueIntent(t, storage, "app/db", 1, writeMetadata)

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if got := payload["username"]; got != "app" {
		t.Fatalf("username = %v, want app", got)
	}
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("read version = %v, want 1", got)
	}

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 1 {
		t.Fatalf("pending queue count = %v, want 1", got)
	}

	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	if got := statusResp.Data["state"]; got != string(domain.SyncStatePending) {
		t.Fatalf("status state = %v, want %s", got, domain.SyncStatePending)
	}
	if got := statusResp.Data["version"]; got != 1 {
		t.Fatalf("status version = %v, want 1", got)
	}
}

func TestDataWriteCAS(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	firstResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "initial",
		},
		"options": map[string]interface{}{
			"cas": 0,
		},
	})
	assertNoErrorResponse(t, firstResp)

	secondResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
		"options": map[string]interface{}{
			"cas": 0,
		},
	})
	if !secondResp.IsError() {
		t.Fatal("second write with cas=0 must fail")
	}

	thirdResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "rotated",
		},
		"options": map[string]interface{}{
			"cas": 1,
		},
	})
	assertNoErrorResponse(t, thirdResp)
	metadata := thirdResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 2 {
		t.Fatalf("third write version = %v, want 2", got)
	}
}

func TestQueueCapacityRejectsWriteBeforeVersionCommit(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	handleRequest(t, b, storage, logical.UpdateOperation, "config", map[string]interface{}{
		"queue_capacity": 1,
		"restore_guard":  true,
	})
	firstResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "initial",
		},
	})
	assertNoErrorResponse(t, firstResp)

	secondResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/api", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
	})
	if !secondResp.IsError() {
		t.Fatal("write must fail when queue is full")
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/api", nil)
	if readResp != nil {
		t.Fatalf("blocked write committed data: %#v", readResp.Data)
	}
}

func TestPeriodicProcessesFakeOutbox(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := writeTestSecret(t, b, storage, "data/app/db", "initial")
	writeMetadata := writeResp.Data["metadata"].(map[string]interface{})
	operationID := writeMetadata["sync_operation_id"].(string)

	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic: %v", err)
	}

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 0 {
		t.Fatalf("pending queue count = %v, want 0", got)
	}

	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	if got := statusResp.Data["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("status state = %v, want %s", got, domain.SyncStateSynced)
	}
	assertSyncedStatusObject(t, statusResp.Data["objects"], operationID)

	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil || operation.State != outboxStateSucceeded {
		t.Fatalf("outbox operation = %#v, want succeeded", operation)
	}
}

func TestPeriodicHonorsDisabledConfig(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeTestSecret(t, b, storage, "data/app/db", "initial")
	handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"disabled": true,
	})

	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic: %v", err)
	}

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 1 {
		t.Fatalf("pending queue count = %v, want 1", got)
	}
}

func TestQueueCapacityCountsQueuedOperationsOnly(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"queue_capacity": 1,
	})
	writeTestSecret(t, b, storage, "data/app/db", "initial")
	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic: %v", err)
	}

	secondResp := writeTestSecret(t, b, storage, "data/app/api", "allowed")
	metadata := secondResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 1 {
		t.Fatalf("second write version = %v, want 1", got)
	}
}

func handleRequest(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	operation logical.Operation,
	path string,
	data map[string]interface{},
) *logical.Response {
	t.Helper()
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: operation,
		Path:      path,
		Storage:   storage,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("%s %s: %v", operation, path, err)
	}
	return resp
}

func assertNoErrorResponse(t *testing.T, resp *logical.Response) {
	t.Helper()
	if resp == nil {
		t.Fatal("response must not be nil")
	}
	if resp.IsError() {
		t.Fatalf("unexpected error response: %v", resp.Error())
	}
}

func assertCompleteEnqueueIntent(
	t *testing.T,
	storage logical.Storage,
	path string,
	version int,
	metadata map[string]interface{},
) {
	t.Helper()
	operationID, ok := metadata["sync_operation_id"].(string)
	if !ok || operationID == "" {
		t.Fatalf("sync_operation_id = %v, want non-empty string", metadata["sync_operation_id"])
	}
	intent, err := getEnqueueIntent(context.Background(), storage, path, version)
	if err != nil {
		t.Fatalf("read enqueue intent: %v", err)
	}
	if intent == nil || !intent.Complete {
		t.Fatalf("enqueue intent = %#v, want complete intent", intent)
	}
	if got := intent.OperationIDs; len(got) != 1 || got[0] != operationID {
		t.Fatalf("enqueue intent operation IDs = %v, want [%s]", got, operationID)
	}
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil {
		t.Fatal("outbox operation must exist")
	}
	if got := operation.ObjectID; got != syncObjectIDSecretPath {
		t.Fatalf("outbox object ID = %v, want %s", got, syncObjectIDSecretPath)
	}
}

func writeTestSecret(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	path string,
	password string,
) *logical.Response {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, path, map[string]interface{}{
		"data": map[string]interface{}{
			"password": password,
		},
	})
	assertNoErrorResponse(t, resp)
	return resp
}

func assertSyncedStatusObject(t *testing.T, raw interface{}, operationID string) { //nolint:forbidigo
	t.Helper()
	objects, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("objects = %T, want []map[string]interface{}", raw)
	}
	if len(objects) != 1 {
		t.Fatalf("objects length = %d, want 1", len(objects))
	}
	object := objects[0]
	if got := object["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("object state = %v, want %s", got, domain.SyncStateSynced)
	}
	if got := object["last_operation_id"]; got != operationID {
		t.Fatalf("object last operation ID = %v, want %s", got, operationID)
	}
	if got := object["remote_version"]; got != "fake" {
		t.Fatalf("object remote version = %v, want fake", got)
	}
}
