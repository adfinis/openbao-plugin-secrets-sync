package backend

import (
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
)

func TestUndeleteAndDestroyVersions(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	deleteResp := env.delete("data/app/db")
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected data delete error: %v", deleteResp.Error())
	}

	undeleteResp := env.update("undelete/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if undeleteResp != nil && undeleteResp.IsError() {
		t.Fatalf("unexpected undelete error: %v", undeleteResp.Error())
	}
	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if got := payload["password"]; got != "initial" {
		t.Fatalf("password = %v, want initial", got)
	}

	destroyResp := env.update("destroy/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if destroyResp != nil && destroyResp.IsError() {
		t.Fatalf("unexpected destroy error: %v", destroyResp.Error())
	}
	readDestroyedResp := env.read("data/app/db")
	if readDestroyedResp != nil {
		t.Fatalf("destroyed data response = %#v, want nil", readDestroyedResp)
	}
	metadataResp := env.read("metadata/app/db")
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if !versions["1"].Destroyed {
		t.Fatal("metadata version destroyed flag must be set after destroy")
	}

	undeleteDestroyedResp := env.update(
		"undelete/app/db",
		map[string]interface{}{"versions": []int{1}},
	)
	if undeleteDestroyedResp != nil && undeleteDestroyedResp.IsError() {
		t.Fatalf("unexpected undelete destroyed error: %v", undeleteDestroyedResp.Error())
	}
	readStillDestroyedResp := env.read("data/app/db")
	if readStillDestroyedResp != nil {
		t.Fatalf("undeleted destroyed data response = %#v, want nil", readStillDestroyedResp)
	}
}

func TestDeleteVersionsSoftDeletesSelectedVersions(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	secondResp := env.writeAppDBSecret("rotated")
	secondMetadata := secondResp.Data["metadata"].(map[string]interface{})
	if got := secondMetadata["version"]; got != 2 {
		t.Fatalf("second write version = %v, want 2", got)
	}

	deleteResp := env.update("delete/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected version delete error: %v", deleteResp.Error())
	}
	readDeletedResp := env.read("data/app/db", map[string]interface{}{
		"version": 1,
	})
	if readDeletedResp != nil {
		t.Fatalf("deleted version response = %#v, want nil", readDeletedResp)
	}
	readLatestResp := env.read("data/app/db")
	assertNoErrorResponse(t, readLatestResp)
	readMetadata := readLatestResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 2 {
		t.Fatalf("latest version = %v, want 2", got)
	}

	metadataResp := env.read("metadata/app/db")
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if versions["1"].DeletionTime == "" {
		t.Fatal("metadata version deletion_time must be set after version delete")
	}
}

func TestCurrentVersionMutationQueuesRemoteDelete(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path string
	}{
		{name: "delete", path: "delete/app/db"},
		{name: "destroy", path: "destroy/app/db"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := newBackendTestEnv(t)

			env.writeAppDBSecret("initial")
			env.createFakeDestination("default")
			associationResp := env.createFakeDeleteModeAssociation()
			associationID := associationIDFromResponse(t, associationResp)
			generation := sourceGeneration(t, env.storage)
			upsertOperationID := operationIDsFromResponse(t, associationResp)[0]
			deleteOperationID := newOperationID(
				generation,
				"app/db",
				1,
				associationID,
				syncObjectIDSecretPath,
				outbox.OperationTypeDelete,
			)

			mutationResp := env.update(testCase.path, map[string]interface{}{
				"versions": []int{1},
			})
			if mutationResp != nil && mutationResp.IsError() {
				t.Fatalf("unexpected %s response: %v", testCase.name, mutationResp.Error())
			}

			assertOutboxMissing(t, env.storage, upsertOperationID)
			deleteOperation := assertOutboxOperation(t, env.storage, deleteOperationID, 1, outboxStatePending)
			if got := deleteOperation.Type; got != outbox.OperationTypeDelete {
				t.Fatalf("operation type = %s, want %s", got, outbox.OperationTypeDelete)
			}
			readResp := env.read("data/app/db")
			if readResp != nil {
				t.Fatalf("mutated current version response = %#v, want nil", readResp)
			}
		})
	}
}

func TestUndeleteCurrentVersionQueuesUpsertAfterRemoteDelete(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeDeleteModeAssociation()
	upsertOperationID := operationIDsFromResponse(t, associationResp)[0]
	env.runPeriodicAllowed("periodic upsert")

	deleteResp := env.delete("data/app/db")
	assertNoErrorResponse(t, deleteResp)
	deleteOperationID := operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{}))[0]
	env.runPeriodicAllowed("periodic delete")
	assertOutboxMissing(t, env.storage, deleteOperationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateRemoteMissing)

	undeleteResp := env.update("undelete/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if undeleteResp != nil && undeleteResp.IsError() {
		t.Fatalf("unexpected undelete response: %v", undeleteResp.Error())
	}
	assertOutboxOperation(t, env.storage, upsertOperationID, 1, outboxStatePending)

	env.runPeriodicAllowed("periodic undelete upsert")
	assertOutboxMissing(t, env.storage, upsertOperationID)
	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 || objects[0]["state"] != string(domain.SyncStateSynced) {
		t.Fatalf("status objects = %#v, want synced object", objects)
	}
}

func TestDataDeleteRetainModeCancelsQueuedUpsert(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	upsertOperationID := operationIDsFromResponse(t, associationResp)[0]

	deleteResp := env.delete("data/app/db")
	assertNoErrorResponse(t, deleteResp)
	metadata := deleteResp.Data["metadata"].(map[string]interface{})
	assertOperationIDs(t, metadata, 0)
	assertOutboxMissing(t, env.storage, upsertOperationID)
	assertQueueCount(t, env.b, env.storage, "pending", 0)

	readResp := env.read("data/app/db")
	if readResp != nil {
		t.Fatalf("deleted source response = %#v, want nil", readResp)
	}
}

func TestDataDeleteDeleteModeQueuesRemoteDelete(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createFakeDeleteModeAssociation()
	env.runPeriodicAllowed("periodic upsert")

	deleteResp := env.delete("data/app/db")
	assertNoErrorResponse(t, deleteResp)
	deleteOperationID := requireSingleOperationID(
		t,
		operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{})),
		"delete",
	)
	deleteOperation := assertOutboxOperation(t, env.storage, deleteOperationID, 1, outboxStatePending)
	if got := deleteOperation.Type; got != outbox.OperationTypeDelete {
		t.Fatalf("delete operation type = %s, want %s", got, outbox.OperationTypeDelete)
	}

	env.runPeriodicAllowed("periodic delete")
	assertOutboxMissing(t, env.storage, deleteOperationID)
	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 || objects[0]["state"] != string(domain.SyncStateRemoteMissing) {
		t.Fatalf("status objects = %#v, want remote missing object", objects)
	}
	if got := objects[0]["last_operation_id"]; got != deleteOperationID {
		t.Fatalf("delete last operation ID = %v, want %s", got, deleteOperationID)
	}
	if got := objects[0]["remote_version"]; got != "deleted" {
		t.Fatalf("delete remote version = %v, want deleted", got)
	}
}
