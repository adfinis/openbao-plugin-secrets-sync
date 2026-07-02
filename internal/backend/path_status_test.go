package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestStatusWriteIgnoresOlderOperationVersion(t *testing.T) {
	storage := &logical.InmemStorage{}
	associationID := "assoc-test"
	if err := putStatus(context.Background(), storage, statusRecord{
		Path:            "app/db",
		Version:         2,
		AssociationID:   associationID,
		ObjectID:        syncObjectIDSecretPath,
		DestinationRef:  "fake/default",
		ResolvedName:    "prod/app/db",
		State:           string(domain.SyncStateSynced),
		LastOperationID: "op-new",
		UpdatedTime:     nowUTC().Format(timeFormatRFC3339),
	}); err != nil {
		t.Fatalf("write current status: %v", err)
	}

	staleOperation := outboxRecord{
		ID:             "op-stale",
		Type:           outbox.OperationTypeUpsert,
		Path:           "app/db",
		Version:        1,
		AssociationID:  associationID,
		ObjectID:       syncObjectIDSecretPath,
		DestinationRef: "fake/default",
		State:          outboxStatePending,
	}
	if err := markOperationFailed(
		context.Background(),
		storage,
		staleOperation,
		operationFailure{
			class:        providers.ErrorClassDrift,
			message:      "stale remote drift",
			resolvedName: "prod/app/db",
		},
		nowUTC(),
	); err != nil {
		t.Fatalf("mark stale operation failed: %v", err)
	}

	status, err := getStatus(context.Background(), storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status == nil {
		t.Fatal("status must exist")
	}
	if got := status.Version; got != 2 {
		t.Fatalf("status version = %d, want 2", got)
	}
	if got := status.State; got != string(domain.SyncStateSynced) {
		t.Fatalf("status state = %s, want %s", got, domain.SyncStateSynced)
	}
	if got := status.LastOperationID; got != "op-new" {
		t.Fatalf("last operation = %s, want op-new", got)
	}
	assertOutboxOperation(t, storage, staleOperation.ID, 1, outboxStateFailedTerminal)
}
