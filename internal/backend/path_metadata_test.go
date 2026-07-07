package backend

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMetadataReadListAndSoftDelete(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	resp := env.update("data/app/api", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "api",
		},
	})
	assertNoErrorResponse(t, resp)

	listResp := env.list("metadata/app")
	assertNoErrorResponse(t, listResp)
	keys := listResp.Data["keys"].([]string)
	if !hasKey(keys, "db") || !hasKey(keys, "api") {
		t.Fatalf("metadata keys = %v, want db and api", keys)
	}

	metadataResp := env.read("metadata/app/db")
	assertNoErrorResponse(t, metadataResp)
	assertResponseValue(t, metadataResp, "current_version", 1)

	deleteResp := env.delete("data/app/db")
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected data delete error: %v", deleteResp.Error())
	}
	readDeletedResp := env.read("data/app/db")
	if readDeletedResp != nil {
		t.Fatalf("soft-deleted data response = %#v, want nil", readDeletedResp)
	}
	metadataResp = env.read("metadata/app/db")
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if versions["1"].DeletionTime == "" {
		t.Fatal("metadata version deletion_time must be set after soft delete")
	}
}

func TestMetadataListPagination(t *testing.T) {
	env := newBackendTestEnv(t)

	for _, path := range []string{"app/api", "app/cache", "app/db", "shared/db", "team/db"} {
		env.markSourceSyncable(path)
	}

	assertListKeys(t,
		env.list("metadata", map[string]interface{}{
			"limit": 2,
		}),
		[]string{"app/", "shared/"},
	)
	assertListKeys(t,
		env.list("metadata", map[string]interface{}{
			"after": "b",
			"limit": 1,
		}),
		[]string{"shared/"},
	)
	assertListKeys(t,
		env.list("metadata/app", map[string]interface{}{
			"limit": 2,
		}),
		[]string{"api", "cache"},
	)
	assertListKeys(t,
		env.list("metadata/app", map[string]interface{}{
			"after": "api",
			"limit": 1,
		}),
		[]string{"cache"},
	)
	assertListKeys(t,
		env.list("metadata/app", map[string]interface{}{
			"limit": -1,
		}),
		[]string{"api", "cache", "db"},
	)
}

func TestMetadataDeleteRequiresAssociationRemoval(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)

	blockedResp := env.delete("metadata/app/db")
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("metadata delete with association response = %#v, want error", blockedResp)
	}

	deleteAssociationResp := env.delete(
		"associations/app/db/"+associationID,
		nil,
	)
	if deleteAssociationResp != nil && deleteAssociationResp.IsError() {
		t.Fatalf("unexpected association delete error: %v", deleteAssociationResp.Error())
	}

	deleteMetadataResp := env.delete("metadata/app/db")
	if deleteMetadataResp != nil && deleteMetadataResp.IsError() {
		t.Fatalf("unexpected metadata delete error: %v", deleteMetadataResp.Error())
	}
	readResp := env.read("metadata/app/db")
	if readResp != nil {
		t.Fatalf("deleted metadata response = %#v, want nil", readResp)
	}
}

func TestMetadataDeleteRecreateRotatesOperationIDs(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	firstAssociationResp := env.createDefaultFakeAssociation()
	firstAssociationID := associationIDFromResponse(t, firstAssociationResp)
	firstGeneration := sourceGeneration(t, env.storage)
	firstOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, firstAssociationResp), "first association")

	deleteAssociationResp := env.delete(
		"associations/app/db/"+firstAssociationID,
		nil,
	)
	if deleteAssociationResp != nil && deleteAssociationResp.IsError() {
		t.Fatalf("unexpected association delete error: %v", deleteAssociationResp.Error())
	}
	deleteMetadataResp := env.delete("metadata/app/db")
	if deleteMetadataResp != nil && deleteMetadataResp.IsError() {
		t.Fatalf("unexpected metadata delete error: %v", deleteMetadataResp.Error())
	}

	env.writeAppDBSecret("recreated")
	secondAssociationResp := env.createDefaultFakeAssociation()
	secondGeneration := sourceGeneration(t, env.storage)
	secondOperationID := requireSingleOperationID(
		t,
		operationIDsFromResponse(t, secondAssociationResp),
		"second association",
	)

	if secondGeneration == firstGeneration {
		t.Fatalf("metadata generation was reused: %s", secondGeneration)
	}
	if secondOperationID == firstOperationID {
		t.Fatalf("operation ID was reused after metadata delete: %s", secondOperationID)
	}
}

func TestMetadataWriteEnforcesCASRequiredAndCustomMetadata(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	metadataResp := env.update("metadata/app/db", map[string]interface{}{
		"cas_required": true,
		"custom_metadata": map[string]interface{}{
			sourceMetadataKeySyncable: sourceMetadataValueTrue,
			"owner":                   "platform",
		},
	})
	assertNoErrorResponse(t, metadataResp)
	assertResponseValue(t, metadataResp, "cas_required", true)
	customMetadata := metadataResp.Data["custom_metadata"].(map[string]string)
	if got := customMetadata[sourceMetadataKeySyncable]; got != sourceMetadataValueTrue {
		t.Fatalf("custom_metadata.syncable = %v, want true", got)
	}

	blockedResp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
	})
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("write without CAS response = %#v, want error", blockedResp)
	}

	allowedResp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "rotated",
		},
		"options": map[string]interface{}{
			"cas": 1,
		},
	})
	assertNoErrorResponse(t, allowedResp)
	allowedMetadata := allowedResp.Data["metadata"].(map[string]interface{})
	if got := allowedMetadata["version"]; got != 2 {
		t.Fatalf("allowed write version = %v, want 2", got)
	}
}

func TestMetadataWriteRejectsNonZeroDeleteVersionAfter(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("metadata/app/db", map[string]interface{}{
		"delete_version_after": "1h",
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("delete_version_after response = %#v, want error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "delete_version_after is not enforced yet") {
		t.Fatalf("delete_version_after error = %q", resp.Error().Error())
	}
}

func TestMetadataMaxVersionsPrunesOldVersions(t *testing.T) {
	env := newBackendTestEnv(t)

	metadataResp := env.update("metadata/app/db", map[string]interface{}{
		"max_versions": 2,
	})
	assertNoErrorResponse(t, metadataResp)

	env.writeAppDBSecret("one")
	env.writeAppDBSecret("two")
	env.writeAppDBSecret("three")

	readPrunedResp := env.read("data/app/db", map[string]interface{}{
		"version": 1,
	})
	if readPrunedResp != nil {
		t.Fatalf("pruned version response = %#v, want nil", readPrunedResp)
	}
	metadataResp = env.read("metadata/app/db")
	assertNoErrorResponse(t, metadataResp)
	assertResponseValue(t, metadataResp, "current_version", 3)
	assertResponseValue(t, metadataResp, "oldest_version", 2)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if _, ok := versions["1"]; ok {
		t.Fatalf("metadata versions = %v, want version 1 pruned", versions)
	}
}

func TestMetadataMaxVersionsKeepsQueuedSourceVersions(t *testing.T) {
	env := newBackendTestEnv(t)

	metadataResp := env.update("metadata/app/db", map[string]interface{}{
		"max_versions": 1,
	})
	assertNoErrorResponse(t, metadataResp)
	env.writeAppDBSecret("one")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	operation.ClaimOwner = "worker-active"
	operation.ClaimExpiresTime = nowUTC().Add(time.Hour).Format(timeFormatRFC3339)
	operation.ClaimAttempt = 1
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("write claimed operation: %v", err)
	}

	env.writeAppDBSecret("two")

	version, err := getVersion(context.Background(), env.storage, "app/db", 1)
	if err != nil {
		t.Fatalf("read protected version: %v", err)
	}
	if version == nil {
		t.Fatal("version 1 must be kept while a queued upsert references it")
	}
	metadata, err := getMetadata(context.Background(), env.storage, "app/db")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if _, ok := metadata.Versions["1"]; !ok {
		t.Fatalf("metadata versions = %v, want protected version 1", metadata.Versions)
	}
}
