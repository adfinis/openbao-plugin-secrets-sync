package backend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestQueueCancelPurgesTerminalOperation(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	markOutboxTerminal(t, env.storage, operationID, nowUTC())

	cancelResp := env.update("queue/" + operationID + "/cancel")
	assertNoErrorResponse(t, cancelResp)
	assertResponseValue(t, cancelResp, "state", outboxStateCanceled)
	assertOutboxMissing(t, env.storage, operationID)
	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertResponseValue(t, queueResp, "terminal", 0)
}

func TestTerminalOutboxPruningBoundsCountAndAgeWork(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		storage := &logical.InmemStorage{}
		now := nowUTC()
		for index := range 3 {
			putTerminalOutboxFixture(t, storage, fmt.Sprintf("op-count-%d", index), now.Add(time.Duration(index)*time.Minute))
		}

		deleted, err := pruneTerminalOutboxRecordsLimit(context.Background(), storage, now.Add(time.Hour), 2, 100)
		if err != nil {
			t.Fatalf("prune terminal count: %v", err)
		}
		if deleted != 1 {
			t.Fatalf("deleted terminal records = %d, want 1", deleted)
		}
		assertOutboxMissing(t, storage, "op-count-0")
		assertOutboxOperation(t, storage, "op-count-1", 1, outboxStateFailedTerminal)
		assertOutboxOperation(t, storage, "op-count-2", 1, outboxStateFailedTerminal)
	})

	t.Run("age batch", func(t *testing.T) {
		storage := &logical.InmemStorage{}
		now := nowUTC()
		for index := range 3 {
			putTerminalOutboxFixture(
				t,
				storage,
				fmt.Sprintf("op-age-%d", index),
				now.Add(-terminalOutboxRetention-time.Duration(index+1)*time.Hour),
			)
		}

		deleted, err := pruneTerminalOutboxRecordsLimit(context.Background(), storage, now, 10, 2)
		if err != nil {
			t.Fatalf("prune terminal age: %v", err)
		}
		if deleted != 2 {
			t.Fatalf("deleted expired terminal records = %d, want 2", deleted)
		}
		ids, err := listOutboxIDsForState(context.Background(), storage, outboxStateFailedTerminal)
		if err != nil {
			t.Fatalf("list retained terminal records: %v", err)
		}
		if len(ids) != 1 {
			t.Fatalf("retained terminal records = %v, want one", ids)
		}
	})
}

func TestPeriodicPrunesExpiredTerminalOperationWhileDisabled(t *testing.T) {
	env := newBackendTestEnv(t)
	putTerminalOutboxFixture(
		t,
		env.storage,
		"op-expired-disabled",
		nowUTC().Add(-terminalOutboxRetention-time.Hour),
	)
	resp := env.update(configPath, map[string]interface{}{"disabled": true})
	if resp != nil && resp.IsError() {
		t.Fatalf("disable config: %v", resp.Error())
	}

	if err := env.b.periodic(context.Background(), &logical.Request{Storage: env.storage}); err != nil {
		t.Fatalf("disabled periodic cleanup: %v", err)
	}
	assertOutboxMissing(t, env.storage, "op-expired-disabled")
}

func TestAssociationAndSourceDeletionPurgeTerminalHistory(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	markOutboxTerminal(t, env.storage, operationID, nowUTC())

	deleteResp := env.delete("associations/app/db/" + associationID)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("delete association: %v", deleteResp.Error())
	}
	assertOutboxMissing(t, env.storage, operationID)
	putTerminalOutboxFixture(t, env.storage, "op-orphaned-source", nowUTC())

	deleteMetadataResp := env.delete("metadata/app/db")
	if deleteMetadataResp != nil && deleteMetadataResp.IsError() {
		t.Fatalf("delete source metadata: %v", deleteMetadataResp.Error())
	}
	assertOutboxMissing(t, env.storage, "op-orphaned-source")
}

func putTerminalOutboxFixture(t *testing.T, storage logical.Storage, id string, terminalTime time.Time) {
	t.Helper()
	timestamp := terminalTime.Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), storage, outboxRecord{
		ID:             id,
		Type:           outbox.OperationTypeUpsert,
		Path:           "app/db",
		Version:        1,
		AssociationID:  "assoc-terminal",
		ObjectID:       syncObjectIDSecretPath,
		DestinationRef: "fake/default",
		State:          outboxStateFailedTerminal,
		CreatedTime:    timestamp,
		UpdatedTime:    timestamp,
		IdempotencyKey: id,
	}); err != nil {
		t.Fatalf("write terminal outbox fixture %s: %v", id, err)
	}
}

func markOutboxTerminal(t *testing.T, storage logical.Storage, id string, terminalTime time.Time) {
	t.Helper()
	record, err := getOutbox(context.Background(), storage, id)
	if err != nil {
		t.Fatalf("read outbox operation %s: %v", id, err)
	}
	if record == nil {
		t.Fatalf("outbox operation %s must exist", id)
	}
	record.State = outboxStateFailedTerminal
	record.NotBefore = ""
	record.UpdatedTime = terminalTime.Format(timeFormatRFC3339)
	clearOutboxClaim(record)
	if err := putOutbox(context.Background(), storage, *record); err != nil {
		t.Fatalf("mark outbox operation %s terminal: %v", id, err)
	}
}
