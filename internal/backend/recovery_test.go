package backend

import (
	"context"
	"fmt"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestPeriodicRecoversIncompleteEnqueueIntent(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createDefaultFakeAssociation()
	env.runPeriodicAllowed("periodic")

	secondResp := env.writeAppDBSecret("rotated")
	metadata := secondResp.Data["metadata"].(map[string]interface{})
	operationID := operationIDsFromMetadata(t, metadata)[0]
	operation, err := getOutbox(context.Background(), env.storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil {
		t.Fatal("outbox operation must exist before simulated loss")
	}
	if err := deleteOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("delete outbox operation: %v", err)
	}
	intent := newEnqueueIntentRecord(
		"app/db",
		sourceGeneration(t, env.storage),
		2,
		[]outboxRecord{*operation},
		operation.CreatedTime,
	)
	if err := putEnqueueIntent(context.Background(), env.storage, intent); err != nil {
		t.Fatalf("write incomplete enqueue intent: %v", err)
	}

	env.runPeriodicAllowed("periodic recovery")
	assertOutboxMissing(t, env.storage, operationID)
	intentRecord, err := getEnqueueIntent(context.Background(), env.storage, "app/db", 2)
	if err != nil {
		t.Fatalf("read recovered enqueue intent: %v", err)
	}
	if intentRecord != nil {
		t.Fatalf("recovered enqueue intent = %#v, want pruned", intentRecord)
	}
}

func TestRecoveryRestoresDeleteIntentAfterSourceDelete(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createFakeDeleteModeAssociation()
	env.runPeriodicAllowed("periodic upsert")
	deleteResp := env.delete("data/app/db")
	assertNoErrorResponse(t, deleteResp)
	deleteOperationID := operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{}))[0]
	operation, err := getOutbox(context.Background(), env.storage, deleteOperationID)
	if err != nil {
		t.Fatalf("read delete operation: %v", err)
	}
	if operation == nil {
		t.Fatal("delete operation must exist before simulated loss")
	}
	if err := deleteOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("delete outbox operation: %v", err)
	}
	intent := newEnqueueIntentRecord(
		"app/db",
		sourceGeneration(t, env.storage),
		1,
		[]outboxRecord{*operation},
		operation.CreatedTime,
	)
	if err := putEnqueueIntent(context.Background(), env.storage, intent); err != nil {
		t.Fatalf("write incomplete delete enqueue intent: %v", err)
	}

	if err := recoverIncompleteEnqueueIntents(context.Background(), env.storage, nowUTC()); err != nil {
		t.Fatalf("recover delete enqueue intent: %v", err)
	}
	recovered := assertOutboxOperation(t, env.storage, deleteOperationID, 1, outboxStatePending)
	if got := recovered.Type; got != outbox.OperationTypeDelete {
		t.Fatalf("recovered operation type = %s, want %s", got, outbox.OperationTypeDelete)
	}
}

func TestRecoveryCompletesIntentWithoutCommittedVersion(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)
	association, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	now := nowUTC().Format(timeFormatRFC3339)
	generation := sourceGeneration(t, env.storage)
	operation := newAssociationOutboxRecord(*association, generation, 99, syncObjectIDSecretPath, now)
	intent := newEnqueueIntentRecord("app/db", generation, 99, []outboxRecord{operation}, now)
	if err := putEnqueueIntent(context.Background(), env.storage, intent); err != nil {
		t.Fatalf("write enqueue intent: %v", err)
	}

	if err := recoverIncompleteEnqueueIntents(context.Background(), env.storage, nowUTC()); err != nil {
		t.Fatalf("recover incomplete enqueue intents: %v", err)
	}
	recoveredIntent, err := getEnqueueIntent(context.Background(), env.storage, "app/db", 99)
	if err != nil {
		t.Fatalf("read recovered enqueue intent: %v", err)
	}
	if recoveredIntent != nil {
		t.Fatalf("recovered enqueue intent = %#v, want pruned", recoveredIntent)
	}
	recoveredOperation, err := getOutbox(context.Background(), env.storage, operation.ID)
	if err != nil {
		t.Fatalf("read recovered operation: %v", err)
	}
	if recoveredOperation != nil {
		t.Fatalf("recovered operation = %#v, want nil without committed version", recoveredOperation)
	}
}

func TestRecoveryPrunesCompletedEnqueueIntent(t *testing.T) {
	storage := &logical.InmemStorage{}
	now := nowUTC().Format(timeFormatRFC3339)
	intent := newEnqueueIntentRecord("app/db", "gen-test", 1, nil, now)
	intent.Complete = true
	intent.CompletedTime = now
	if err := putEnqueueIntent(context.Background(), storage, intent); err != nil {
		t.Fatalf("write completed enqueue intent: %v", err)
	}

	if err := recoverIncompleteEnqueueIntents(context.Background(), storage, nowUTC()); err != nil {
		t.Fatalf("recover incomplete enqueue intents: %v", err)
	}
	prunedIntent, err := getEnqueueIntent(context.Background(), storage, "app/db", 1)
	if err != nil {
		t.Fatalf("read pruned enqueue intent: %v", err)
	}
	if prunedIntent != nil {
		t.Fatalf("enqueue intent = %#v, want pruned", prunedIntent)
	}
}

func TestPeriodicLimitsRecoveredEnqueueIntents(t *testing.T) {
	env := newBackendTestEnv(t)
	now := nowUTC().Format(timeFormatRFC3339)
	for index := 0; index < defaultPeriodicRecoveryMaxIntents+1; index++ {
		intent := newEnqueueIntentRecord(fmt.Sprintf("app/db-%03d", index), "gen-test", 1, nil, now)
		intent.Complete = true
		intent.CompletedTime = now
		if err := putEnqueueIntent(context.Background(), env.storage, intent); err != nil {
			t.Fatalf("write completed enqueue intent %d: %v", index, err)
		}
	}

	env.runPeriodicAllowed("bounded periodic recovery")
	intents, err := listEnqueueIntents(context.Background(), env.storage)
	if err != nil {
		t.Fatalf("list enqueue intents: %v", err)
	}
	if len(intents) != 1 {
		t.Fatalf("enqueue intents after bounded periodic = %d, want 1", len(intents))
	}
}
