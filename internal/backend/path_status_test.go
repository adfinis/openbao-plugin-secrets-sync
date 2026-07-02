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

func TestStatusWritePreservesDriftBookkeeping(t *testing.T) {
	storage := &logical.InmemStorage{}
	existing := statusRecord{
		Path:                  "app/db",
		Version:               1,
		AssociationID:         "assoc-test",
		ObjectID:              syncObjectIDSecretPath,
		DestinationRef:        "fake/default",
		ResolvedName:          "prod/app/db",
		State:                 string(domain.SyncStateDrifted),
		Verification:          providers.RemoteStateVerificationValue,
		LastReconcileTime:     "2026-07-01T10:00:00Z",
		LastDriftDetectedTime: "2026-07-01T10:00:00Z",
		LastRepairTime:        "2026-07-01T10:05:00Z",
		RepairCount:           2,
		UpdatedTime:           "2026-07-01T10:05:00Z",
	}
	if err := putStatus(context.Background(), storage, existing); err != nil {
		t.Fatalf("write existing status: %v", err)
	}

	if err := putStatus(context.Background(), storage, statusRecord{
		Path:            existing.Path,
		Version:         existing.Version,
		AssociationID:   existing.AssociationID,
		ObjectID:        existing.ObjectID,
		DestinationRef:  existing.DestinationRef,
		ResolvedName:    existing.ResolvedName,
		State:           string(domain.SyncStateSynced),
		LastOperationID: "op-success",
		UpdatedTime:     "2026-07-01T10:10:00Z",
	}); err != nil {
		t.Fatalf("write fresh status: %v", err)
	}

	status, err := getStatus(context.Background(), storage, existing.Path, existing.AssociationID, existing.ObjectID)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status == nil {
		t.Fatal("status must exist")
	}
	if got := status.Verification; got != existing.Verification {
		t.Fatalf("verification = %s, want %s", got, existing.Verification)
	}
	if got := status.LastReconcileTime; got != existing.LastReconcileTime {
		t.Fatalf("last_reconcile_time = %s, want %s", got, existing.LastReconcileTime)
	}
	if got := status.LastDriftDetectedTime; got != existing.LastDriftDetectedTime {
		t.Fatalf("last_drift_detected_time = %s, want %s", got, existing.LastDriftDetectedTime)
	}
	if got := status.LastRepairTime; got != existing.LastRepairTime {
		t.Fatalf("last_repair_time = %s, want %s", got, existing.LastRepairTime)
	}
	if got := status.RepairCount; got != existing.RepairCount {
		t.Fatalf("repair_count = %d, want %d", got, existing.RepairCount)
	}
	if got := status.LastOperationID; got != "op-success" {
		t.Fatalf("last_operation_id = %s, want op-success", got)
	}
}
