package backend

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestOutboxIndexesDoNotAuthorizeMissingOperations(t *testing.T) {
	storage := &logical.InmemStorage{}
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	dueTime := now.Format(timeFormatRFC3339)

	putStorageJSON(t, storage, outboxByDueStorageKey(dueTime, "op-missing"), "op-missing")
	putStorageJSON(t, storage, outboxByPathStorageKey("app/db", "op-missing"), "op-missing")
	putStorageJSON(t, storage, outboxByStateStorageKey(outboxStatePending, "op-missing"), "op-missing")

	dueIDs, err := listDueOutboxIDs(context.Background(), storage, now)
	if err != nil {
		t.Fatalf("list due outbox IDs: %v", err)
	}
	assertStringIDs(t, dueIDs, "op-missing")

	queuedIDs, err := listQueuedOutboxIDsForPath(context.Background(), storage, "app/db")
	if err != nil {
		t.Fatalf("list queued outbox IDs for path: %v", err)
	}
	assertStringIDs(t, queuedIDs)

	versions, err := queuedUpsertVersionsForPath(context.Background(), storage, "app/db")
	if err != nil {
		t.Fatalf("list queued upsert versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("queued versions = %v, want empty", versions)
	}

	processed, err := Backend(&logical.BackendConfig{}).processDueOutboxLimit(
		context.Background(),
		storage,
		now,
		10,
		observability.OperationPeriodic,
	)
	if err != nil {
		t.Fatalf("process due outbox: %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed operations = %d, want 0", processed)
	}
}

func TestAssociationIndexesReturnOnlyLivePrimaryRecords(t *testing.T) {
	storage := &logical.InmemStorage{}
	now := "2026-07-06T10:00:00Z"
	live := associationRecord{
		ID:              "assoc-live",
		Path:            "app/db",
		DestinationType: "fake",
		DestinationName: "default",
		DestinationRef:  "fake/default",
		ResolvedName:    "prod/app/db",
		Granularity:     syncGranularitySecretPath,
		Format:          defaultAssociationFormat,
		DeleteMode:      defaultDeleteMode,
		CreatedTime:     now,
		UpdatedTime:     now,
	}
	if err := putAssociation(context.Background(), storage, live); err != nil {
		t.Fatalf("write live association: %v", err)
	}
	moved := live
	moved.ID = "assoc-moved"
	moved.DestinationName = "other"
	moved.DestinationRef = "fake/other"
	moved.ResolvedName = "prod/other"
	if err := putAssociation(context.Background(), storage, moved); err != nil {
		t.Fatalf("write moved association: %v", err)
	}

	putStorageJSON(t, storage, associationByDestinationStorageKey("fake", "default", "assoc-missing"), "app/missing")
	putStorageJSON(t, storage, associationByDestinationStorageKey("fake", "default", moved.ID), moved.Path)
	putStorageJSON(t, storage, associationNameStorageKey("fake/default", "prod/missing", "assoc-missing"), "app/missing")
	putStorageJSON(t, storage, associationNameStorageKey("fake/default", "prod/old", live.ID), live.Path)

	destinationIDs, err := listAssociationIDsForDestination(context.Background(), storage, "fake", "default")
	if err != nil {
		t.Fatalf("list destination association IDs: %v", err)
	}
	assertStringIDs(t, destinationIDs, live.ID)

	reservationIDs, err := listAssociationNameReservationIDs(
		context.Background(),
		storage,
		"fake/default",
		live.ResolvedName,
	)
	if err != nil {
		t.Fatalf("list live reservation IDs: %v", err)
	}
	assertStringIDs(t, reservationIDs, live.ID)

	missingReservationIDs, err := listAssociationNameReservationIDs(
		context.Background(),
		storage,
		"fake/default",
		"prod/missing",
	)
	if err != nil {
		t.Fatalf("list missing reservation IDs: %v", err)
	}
	assertStringIDs(t, missingReservationIDs)

	oldReservationIDs, err := listAssociationNameReservationIDs(context.Background(), storage, "fake/default", "prod/old")
	if err != nil {
		t.Fatalf("list stale reservation IDs: %v", err)
	}
	assertStringIDs(t, oldReservationIDs)
}

func TestAssociationPutCommitsPrimaryAfterIndexes(t *testing.T) {
	storage := &logical.InmemStorage{}
	now := "2026-07-06T10:00:00Z"
	record := associationRecord{
		ID:              "assoc-write-order",
		Path:            "app/db",
		DestinationType: "fake",
		DestinationName: "default",
		DestinationRef:  "fake/default",
		ResolvedName:    "prod/app/db",
		Granularity:     syncGranularitySecretPath,
		Format:          defaultAssociationFormat,
		DeleteMode:      defaultDeleteMode,
		CreatedTime:     now,
		UpdatedTime:     now,
	}
	failing := failPutStorage{
		Storage: storage,
		failKey: associationNameStorageKey(record.DestinationRef, record.ResolvedName, record.ID),
	}
	if err := putAssociation(context.Background(), failing, record); !errors.Is(err, errInjectedStoragePut) {
		t.Fatalf("put association error = %v, want injected failure", err)
	}
	stored, err := getAssociation(context.Background(), storage, record.Path, record.ID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	if stored != nil {
		t.Fatalf("association primary was committed after index failure: %#v", stored)
	}
	destinationIDs, err := listAssociationIDsForDestination(
		context.Background(),
		storage,
		record.DestinationType,
		record.DestinationName,
	)
	if err != nil {
		t.Fatalf("list destination association IDs: %v", err)
	}
	assertStringIDs(t, destinationIDs)
}

func putStorageJSON(t *testing.T, storage logical.Storage, key string, value interface{}) {
	t.Helper()
	entry, err := logical.StorageEntryJSON(key, value)
	if err != nil {
		t.Fatalf("build storage entry %q: %v", key, err)
	}
	if err := storage.Put(context.Background(), entry); err != nil {
		t.Fatalf("put storage entry %q: %v", key, err)
	}
}

var errInjectedStoragePut = errors.New("injected storage put failure")

type failPutStorage struct {
	logical.Storage

	failKey string
}

func (storage failPutStorage) Put(ctx context.Context, entry *logical.StorageEntry) error {
	if entry.Key == storage.failKey {
		return errInjectedStoragePut
	}
	return storage.Storage.Put(ctx, entry)
}

func assertStringIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if got == nil {
		got = []string{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
}
