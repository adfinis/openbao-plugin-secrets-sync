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

func TestDestinationLifecycle(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	createFakeDestination(t, b, storage, "primary")
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/primary", nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["name"]; got != "primary" {
		t.Fatalf("destination name = %v, want primary", got)
	}
	if _, ok := readResp.Data["sensitive_config"]; !ok {
		t.Fatal("destination read must include redacted sensitive_config")
	}

	listResp := handleRequest(t, b, storage, logical.ListOperation, "destinations/fake", nil)
	assertNoErrorResponse(t, listResp)
	keys := listResp.Data["keys"].([]string)
	if len(keys) != 1 || keys[0] != "primary" {
		t.Fatalf("destination keys = %v, want [primary]", keys)
	}

	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "destinations/fake/primary", nil)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected destination delete error: %v", deleteResp.Error())
	}
	readDeletedResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/primary", nil)
	if readDeletedResp != nil {
		t.Fatalf("deleted destination response = %#v, want nil", readDeletedResp)
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
	if got := writeMetadata["sync_state"]; got != string(domain.SyncStateUnknown) {
		t.Fatalf("sync state = %v, want %s", got, domain.SyncStateUnknown)
	}
	assertOperationIDs(t, writeMetadata, 0)

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
	if got := queueResp.Data["pending"]; got != 0 {
		t.Fatalf("pending queue count = %v, want 0", got)
	}

	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	if got := statusResp.Data["state"]; got != string(domain.SyncStateUnknown) {
		t.Fatalf("status state = %v, want %s", got, domain.SyncStateUnknown)
	}
	if got := statusResp.Data["version"]; got != 1 {
		t.Fatalf("status version = %v, want 1", got)
	}
}

func TestMetadataReadListAndSoftDelete(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/api", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "api",
		},
	})
	assertNoErrorResponse(t, resp)

	listResp := handleRequest(t, b, storage, logical.ListOperation, "metadata/app", nil)
	assertNoErrorResponse(t, listResp)
	keys := listResp.Data["keys"].([]string)
	if !hasKey(keys, "db") || !hasKey(keys, "api") {
		t.Fatalf("metadata keys = %v, want db and api", keys)
	}

	metadataResp := handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	if got := metadataResp.Data["current_version"]; got != 1 {
		t.Fatalf("current version = %v, want 1", got)
	}

	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected data delete error: %v", deleteResp.Error())
	}
	readDeletedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	if readDeletedResp != nil {
		t.Fatalf("soft-deleted data response = %#v, want nil", readDeletedResp)
	}
	metadataResp = handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if versions["1"].DeletionTime == "" {
		t.Fatal("metadata version deletion_time must be set after soft delete")
	}
}

func TestUndeleteAndDestroyVersions(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected data delete error: %v", deleteResp.Error())
	}

	undeleteResp := handleRequest(t, b, storage, logical.UpdateOperation, "undelete/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if undeleteResp != nil && undeleteResp.IsError() {
		t.Fatalf("unexpected undelete error: %v", undeleteResp.Error())
	}
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if got := payload["password"]; got != "initial" {
		t.Fatalf("password = %v, want initial", got)
	}

	destroyResp := handleRequest(t, b, storage, logical.UpdateOperation, "destroy/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if destroyResp != nil && destroyResp.IsError() {
		t.Fatalf("unexpected destroy error: %v", destroyResp.Error())
	}
	readDestroyedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	if readDestroyedResp != nil {
		t.Fatalf("destroyed data response = %#v, want nil", readDestroyedResp)
	}
	metadataResp := handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if !versions["1"].Destroyed {
		t.Fatal("metadata version destroyed flag must be set after destroy")
	}

	undeleteDestroyedResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"undelete/app/db",
		map[string]interface{}{"versions": []int{1}},
	)
	if undeleteDestroyedResp != nil && undeleteDestroyedResp.IsError() {
		t.Fatalf("unexpected undelete destroyed error: %v", undeleteDestroyedResp.Error())
	}
	readStillDestroyedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	if readStillDestroyedResp != nil {
		t.Fatalf("undeleted destroyed data response = %#v, want nil", readStillDestroyedResp)
	}
}

func TestMetadataDeleteRequiresAssociationRemoval(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	associationID := associationIDFromResponse(t, associationResp)

	blockedResp := handleRequest(t, b, storage, logical.DeleteOperation, "metadata/app/db", nil)
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("metadata delete with association response = %#v, want error", blockedResp)
	}

	deleteAssociationResp := handleRequest(
		t,
		b,
		storage,
		logical.DeleteOperation,
		"associations/app/db/"+associationID,
		nil,
	)
	if deleteAssociationResp != nil && deleteAssociationResp.IsError() {
		t.Fatalf("unexpected association delete error: %v", deleteAssociationResp.Error())
	}

	deleteMetadataResp := handleRequest(t, b, storage, logical.DeleteOperation, "metadata/app/db", nil)
	if deleteMetadataResp != nil && deleteMetadataResp.IsError() {
		t.Fatalf("unexpected metadata delete error: %v", deleteMetadataResp.Error())
	}
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	if readResp != nil {
		t.Fatalf("deleted metadata response = %#v, want nil", readResp)
	}
}

func TestAssociationCreateQueuesCurrentVersion(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationIDs := operationIDsFromResponse(t, associationResp)
	if len(operationIDs) != 1 {
		t.Fatalf("association operation IDs = %v, want one operation", operationIDs)
	}

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 1 {
		t.Fatalf("pending queue count = %v, want 1", got)
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "associations/app/db", nil)
	assertNoErrorResponse(t, readResp)
	associations := readResp.Data["associations"].([]map[string]interface{})
	if len(associations) != 1 {
		t.Fatalf("associations length = %d, want 1", len(associations))
	}
	if got := associations[0]["resolved_name"]; got != "prod/app/db" {
		t.Fatalf("resolved name = %v, want prod/app/db", got)
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
	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)

	secondResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
	})
	if !secondResp.IsError() {
		t.Fatal("write must fail when queue is full")
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readResp)
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("blocked write committed version = %v, want 1", got)
	}
}

func TestPeriodicProcessesFakeOutbox(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]

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

func TestQueueOperationReadCancelAndRetry(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "queue/"+operationID, nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["state"]; got != outboxStatePending {
		t.Fatalf("operation state = %v, want %s", got, outboxStatePending)
	}

	cancelResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/cancel", nil)
	assertNoErrorResponse(t, cancelResp)
	if got := cancelResp.Data["state"]; got != outboxStateCanceled {
		t.Fatalf("canceled operation state = %v, want %s", got, outboxStateCanceled)
	}
	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 0 {
		t.Fatalf("pending queue count = %v, want 0", got)
	}
	if got := queueResp.Data["canceled"]; got != 1 {
		t.Fatalf("canceled queue count = %v, want 1", got)
	}

	retryResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/retry", nil)
	assertNoErrorResponse(t, retryResp)
	if got := retryResp.Data["state"]; got != outboxStatePending {
		t.Fatalf("retried operation state = %v, want %s", got, outboxStatePending)
	}
}

func TestQueueOperationRejectsRetryAfterSuccess(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic: %v", err)
	}

	retryResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/retry", nil)
	if retryResp == nil || !retryResp.IsError() {
		t.Fatalf("retry succeeded operation response = %#v, want error", retryResp)
	}
	cancelResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/cancel", nil)
	if cancelResp == nil || !cancelResp.IsError() {
		t.Fatalf("cancel succeeded operation response = %#v, want error", cancelResp)
	}
}

func TestPeriodicRecoversIncompleteEnqueueIntent(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)
	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic: %v", err)
	}

	secondResp := writeAppDBSecret(t, b, storage, "rotated")
	metadata := secondResp.Data["metadata"].(map[string]interface{})
	operationID := operationIDsFromMetadata(t, metadata)[0]
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil {
		t.Fatal("outbox operation must exist before simulated loss")
	}
	if err := deleteOutbox(context.Background(), storage, *operation); err != nil {
		t.Fatalf("delete outbox operation: %v", err)
	}
	intent, err := getEnqueueIntent(context.Background(), storage, "app/db", 2)
	if err != nil {
		t.Fatalf("read enqueue intent: %v", err)
	}
	intent.Complete = false
	intent.CompletedTime = ""
	if err := putEnqueueIntent(context.Background(), storage, *intent); err != nil {
		t.Fatalf("write incomplete enqueue intent: %v", err)
	}

	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic recovery: %v", err)
	}
	recovered, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read recovered outbox operation: %v", err)
	}
	if recovered == nil || recovered.State != outboxStateSucceeded {
		t.Fatalf("recovered operation = %#v, want succeeded operation", recovered)
	}
	intent, err = getEnqueueIntent(context.Background(), storage, "app/db", 2)
	if err != nil {
		t.Fatalf("read recovered enqueue intent: %v", err)
	}
	if intent == nil || !intent.Complete {
		t.Fatalf("recovered enqueue intent = %#v, want complete", intent)
	}
}

func TestRecoveryCompletesIntentWithoutCommittedVersion(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	associationID := associationIDFromResponse(t, associationResp)
	association, err := getAssociation(context.Background(), storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	now := nowUTC().Format(timeFormatRFC3339)
	operation := newAssociationOutboxRecord(*association, 99, now)
	intent := newEnqueueIntentRecord("app/db", 99, []outboxRecord{operation}, now)
	if err := putEnqueueIntent(context.Background(), storage, intent); err != nil {
		t.Fatalf("write enqueue intent: %v", err)
	}

	if err := recoverIncompleteEnqueueIntents(context.Background(), storage, nowUTC()); err != nil {
		t.Fatalf("recover incomplete enqueue intents: %v", err)
	}
	recoveredIntent, err := getEnqueueIntent(context.Background(), storage, "app/db", 99)
	if err != nil {
		t.Fatalf("read recovered enqueue intent: %v", err)
	}
	if recoveredIntent == nil || !recoveredIntent.Complete {
		t.Fatalf("recovered enqueue intent = %#v, want complete", recoveredIntent)
	}
	recoveredOperation, err := getOutbox(context.Background(), storage, operation.ID)
	if err != nil {
		t.Fatalf("read recovered operation: %v", err)
	}
	if recoveredOperation != nil {
		t.Fatalf("recovered operation = %#v, want nil without committed version", recoveredOperation)
	}
}

func TestPeriodicHonorsDisabledConfig(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)
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
	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)
	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic: %v", err)
	}

	secondResp := writeAppDBSecret(t, b, storage, "allowed")
	metadata := secondResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 2 {
		t.Fatalf("second write version = %v, want 2", got)
	}
	assertCompleteEnqueueIntent(t, storage, "app/db", 2, metadata)
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
	operationIDs := operationIDsFromMetadata(t, metadata)
	intent, err := getEnqueueIntent(context.Background(), storage, path, version)
	if err != nil {
		t.Fatalf("read enqueue intent: %v", err)
	}
	if intent == nil || !intent.Complete {
		t.Fatalf("enqueue intent = %#v, want complete intent", intent)
	}
	if got := intent.OperationIDs; len(got) != len(operationIDs) || got[0] != operationIDs[0] {
		t.Fatalf("enqueue intent operation IDs = %v, want %v", got, operationIDs)
	}
	operation, err := getOutbox(context.Background(), storage, operationIDs[0])
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

func assertOperationIDs(t *testing.T, metadata map[string]interface{}, expected int) {
	t.Helper()
	operationIDs := operationIDsFromMetadata(t, metadata)
	if len(operationIDs) != expected {
		t.Fatalf("operation IDs = %v, want %d entries", operationIDs, expected)
	}
}

func operationIDsFromMetadata(t *testing.T, metadata map[string]interface{}) []string {
	t.Helper()
	rawIDs, ok := metadata["sync_operation_ids"].([]string)
	if !ok {
		t.Fatalf("sync_operation_ids = %T, want []string", metadata["sync_operation_ids"])
	}
	return rawIDs
}

func operationIDsFromResponse(t *testing.T, resp *logical.Response) []string {
	t.Helper()
	assertNoErrorResponse(t, resp)
	rawIDs, ok := resp.Data["sync_operation_ids"].([]string)
	if !ok {
		t.Fatalf("sync_operation_ids = %T, want []string", resp.Data["sync_operation_ids"])
	}
	return rawIDs
}

func associationIDFromResponse(t *testing.T, resp *logical.Response) string {
	t.Helper()
	assertNoErrorResponse(t, resp)
	association, ok := resp.Data["association"].(map[string]interface{})
	if !ok {
		t.Fatalf("association = %T, want map[string]interface{}", resp.Data["association"])
	}
	id, ok := association["id"].(string)
	if !ok || id == "" {
		t.Fatalf("association id = %v, want non-empty string", association["id"])
	}
	return id
}

func hasKey(keys []string, expected string) bool {
	for _, key := range keys {
		if key == expected {
			return true
		}
	}
	return false
}

func writeAppDBSecret(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	password string,
) *logical.Response {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": password,
		},
	})
	assertNoErrorResponse(t, resp)
	return resp
}

func createFakeDestination(t *testing.T, b logical.Backend, storage logical.Storage, name string) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/fake/"+name, map[string]interface{}{
		"description": "test destination",
	})
	if resp != nil && resp.IsError() {
		t.Fatalf("unexpected destination write error: %v", resp.Error())
	}
}

func createDefaultFakeAssociation(t *testing.T, b logical.Backend, storage logical.Storage) *logical.Response {
	t.Helper()
	return handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
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
