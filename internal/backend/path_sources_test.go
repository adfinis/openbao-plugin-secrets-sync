package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
)

func TestSourceEnableDisableUpdatesSourceSyncState(t *testing.T) {
	env := newBackendTestEnv(t)

	metadataResp := env.update("metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"owner": "team-a",
		},
	})
	assertNoErrorResponse(t, metadataResp)

	enableResp := env.update("sources/app/db/enable")
	assertNoErrorResponse(t, enableResp)
	assertResponseValue(t, enableResp, "path", modelSourcePath)
	assertResponseValue(t, enableResp, "source_sync_enabled", true)
	assertResponseValue(t, enableResp, "changed", true)
	assertResponseValue(t, enableResp, "sync_state", string(domain.SyncStateNoAssociation))
	assertOperationIDs(t, enableResp.Data, 0)
	metadata := enableResp.Data["metadata"].(map[string]interface{})
	if got := metadata["source_sync_enabled"]; got != true {
		t.Fatalf("metadata.source_sync_enabled = %v, want true", got)
	}
	customMetadata := metadata["custom_metadata"].(map[string]string)
	if got := customMetadata["owner"]; got != "team-a" {
		t.Fatalf("custom_metadata.owner = %v, want team-a", got)
	}

	secondResp := env.update("sources/app/db/enable")
	assertNoErrorResponse(t, secondResp)
	assertResponseValue(t, secondResp, "changed", false)

	disableResp := env.update("sources/app/db/disable")
	assertNoErrorResponse(t, disableResp)
	assertResponseValue(t, disableResp, "source_sync_enabled", false)
	assertResponseValue(t, disableResp, "changed", true)

	secondDisableResp := env.update("sources/app/db/disable")
	assertNoErrorResponse(t, secondDisableResp)
	assertResponseValue(t, secondDisableResp, "changed", false)
}

func TestSourceEnableRequeuesCurrentVersionAfterHardenedTransition(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createDefaultConstrainedFakeDestination()
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

	cfgResp := env.update("config", map[string]interface{}{
		"security_posture": securityPostureHardened,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.runPeriodicAllowed("periodic after hardened posture enabled")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateValidationError)

	enableResp := env.update("sources/app/db/enable")
	assertNoErrorResponse(t, enableResp)
	assertResponseValue(t, enableResp, "changed", true)
	assertResponseValue(t, enableResp, "sync_state", string(domain.SyncStatePending))
	requeuedOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, enableResp), "source enable")
	if requeuedOperationID != operationID {
		t.Fatalf("requeued operation ID = %q, want original deterministic ID %q", requeuedOperationID, operationID)
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)

	env.runPeriodicAllowed("periodic after source sync enabled")
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestSourceEnableQueueCapacityFailureRollsBackState(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createDefaultConstrainedFakeDestination()
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

	cfgResp := env.update("config", map[string]interface{}{
		"security_posture": securityPostureHardened,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.runPeriodicAllowed("periodic after hardened posture enabled")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)

	capacityResp := env.update("config", map[string]interface{}{
		"queue_capacity": 0,
	})
	if capacityResp != nil && capacityResp.IsError() {
		t.Fatalf("unexpected queue capacity write error: %v", capacityResp.Error())
	}
	enableResp := env.update("sources/app/db/enable")
	if enableResp == nil || !enableResp.IsError() {
		t.Fatalf("source enable response = %#v, want queue capacity error", enableResp)
	}
	assertHintContains(t, enableResp.Data, "Queue capacity is exhausted")

	metadata, err := getMetadata(context.Background(), env.storage, modelSourcePath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if metadata == nil {
		t.Fatal("metadata is nil")
	}
	if metadata.SourceSyncEnabled {
		t.Fatal("source sync remained enabled after queue admission failed")
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
}

func TestSourceCheckReportsReadiness(t *testing.T) {
	env := newBackendTestEnv(t)

	initialResp := env.read("sources/app/db/check")
	assertNoErrorResponse(t, initialResp)
	assertResponseValue(t, initialResp, "ready", false)
	assertResponseValue(t, initialResp, "source_sync_required", false)
	assertResponseValue(t, initialResp, "source_sync_enabled", false)
	assertResponseValue(t, initialResp, "current_version", 0)
	assertStringSlice(t, initialResp.Data["blockers"].([]string), []string{
		"source_missing",
	})

	env.writeAppDBSecret("secret")
	writtenResp := env.read("sources/app/db/check")
	assertNoErrorResponse(t, writtenResp)
	assertResponseValue(t, writtenResp, "ready", true)
	assertResponseValue(t, writtenResp, "current_version", 1)
	assertResponseValue(t, writtenResp, "current_version_available", true)
	assertStringSlice(t, writtenResp.Data["blockers"].([]string), []string{})

	env.createFakeDestination("default")
	enableResp := env.update("sources/app/db/enable")
	assertNoErrorResponse(t, enableResp)
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
	})
	assertNoErrorResponse(t, associationResp)

	readyResp := env.read("sources/app/db/check")
	assertNoErrorResponse(t, readyResp)
	assertResponseValue(t, readyResp, "ready", true)
	assertResponseValue(t, readyResp, "source_sync_enabled", true)
	assertResponseValue(t, readyResp, "association_count", 1)
	assertResponseValue(t, readyResp, "enabled_association_count", 1)
	assertResponseValue(t, readyResp, "queued_operations", 1)
	assertStringSlice(t, readyResp.Data["blockers"].([]string), []string{})
}

func TestSourceCheckReportsHardenedOptInBlocker(t *testing.T) {
	env := newBackendTestEnv(t)

	cfgResp := env.update("config", map[string]interface{}{
		"security_posture": securityPostureHardened,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}

	initialResp := env.read("sources/app/db/check")
	assertNoErrorResponse(t, initialResp)
	assertResponseValue(t, initialResp, "source_sync_required", true)
	assertResponseValue(t, initialResp, "source_sync_enabled", false)
	assertStringSlice(t, initialResp.Data["blockers"].([]string), []string{
		"source_missing",
		"source_sync_not_enabled",
	})

	env.writeAppDBSecret("secret")
	writtenResp := env.read("sources/app/db/check")
	assertNoErrorResponse(t, writtenResp)
	assertResponseValue(t, writtenResp, "ready", false)
	assertStringSlice(t, writtenResp.Data["blockers"].([]string), []string{"source_sync_not_enabled"})

	enableResp := env.update("sources/app/db/enable")
	assertNoErrorResponse(t, enableResp)
	readyResp := env.read("sources/app/db/check")
	assertNoErrorResponse(t, readyResp)
	assertResponseValue(t, readyResp, "ready", true)
	assertResponseValue(t, readyResp, "source_sync_enabled", true)
	assertStringSlice(t, readyResp.Data["blockers"].([]string), []string{})
}

func TestSourceDeleteRejectsClaimedUpsert(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	claimOperationFixture(t, env.storage, operationID)

	deleteResp := env.delete("data/app/db")
	if deleteResp == nil || !deleteResp.IsError() {
		t.Fatalf("delete claimed upsert response = %#v, want error", deleteResp)
	}
	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["deletion_time"]; got != "" {
		t.Fatalf("deletion_time = %v, want empty", got)
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
}
