package backend

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
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
	assertStringIDs(t, dueIDs)

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

	processed, err := newBackendForTest(&logical.BackendConfig{}).processDueOutboxLimit(
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

func TestOutboxIndexCandidatesMustMatchCanonicalRecord(t *testing.T) {
	storage := &logical.InmemStorage{}
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	record := indexTestOutboxRecord(now)
	record.State = outboxStateRetryWait
	record.NotBefore = future.Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), storage, record); err != nil {
		t.Fatalf("write canonical outbox: %v", err)
	}

	putStorageJSON(t, storage, outboxByDueStorageKey(now.Format(timeFormatRFC3339), record.ID), record.ID)
	putStorageJSON(t, storage, outboxByPathStorageKey("other/path", record.ID), record.ID)
	putStorageJSON(t, storage, outboxByStateStorageKey(outboxStatePending, record.ID), record.ID)

	dueIDs, err := listDueOutboxIDs(context.Background(), storage, now)
	if err != nil {
		t.Fatalf("list due outbox IDs: %v", err)
	}
	assertStringIDs(t, dueIDs)

	nextDue, err := nextFutureOutboxDueTime(context.Background(), storage, now)
	if err != nil {
		t.Fatalf("read next future outbox time: %v", err)
	}
	if nextDue == nil || !nextDue.Equal(future) {
		t.Fatalf("next due time = %v, want %v", nextDue, future)
	}

	pendingIDs, err := listOutboxIDsForState(context.Background(), storage, outboxStatePending)
	if err != nil {
		t.Fatalf("list pending IDs: %v", err)
	}
	assertStringIDs(t, pendingIDs)
	retryIDs, err := listOutboxIDsForState(context.Background(), storage, outboxStateRetryWait)
	if err != nil {
		t.Fatalf("list retry-wait IDs: %v", err)
	}
	assertStringIDs(t, retryIDs, record.ID)
	otherPathIDs, err := listOutboxIDsForPath(context.Background(), storage, "other/path")
	if err != nil {
		t.Fatalf("list other-path IDs: %v", err)
	}
	assertStringIDs(t, otherPathIDs)

	summary, err := readQueueSummary(context.Background(), storage, now)
	if err != nil {
		t.Fatalf("read queue summary: %v", err)
	}
	if summary.Pending != 0 || summary.RetryWait != 1 || summary.Terminal != 0 {
		t.Fatalf("queue summary = %#v, want one retry-wait operation", summary)
	}

	processed, err := newBackendForTest(&logical.BackendConfig{}).processDueOutboxLimit(
		context.Background(),
		storage,
		now,
		10,
		observability.OperationPeriodic,
	)
	if err != nil {
		t.Fatalf("process stale due candidate: %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed operations = %d, want 0", processed)
	}
}

func TestOutboxPutCommitsCanonicalRecordAfterIndexes(t *testing.T) {
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	record := indexTestOutboxRecord(now)
	for _, failKey := range append(outboxIndexKeys(record), outboxStorageKey(record.ID)) {
		t.Run(failKey, func(t *testing.T) {
			storage := &logical.InmemStorage{}
			failing := failPutStorage{Storage: storage, failKey: failKey}
			if err := putOutbox(context.Background(), failing, record); !errors.Is(err, errInjectedStoragePut) {
				t.Fatalf("put outbox error = %v, want injected failure", err)
			}
			stored, err := getOutbox(context.Background(), storage, record.ID)
			if err != nil {
				t.Fatalf("read outbox: %v", err)
			}
			if stored != nil {
				t.Fatalf("canonical outbox was committed after write failure: %#v", stored)
			}
		})
	}
}

func TestOutboxUpdateFailurePreservesCanonicalIndexView(t *testing.T) {
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	existing := indexTestOutboxRecord(now)
	updated := existing
	updated.State = outboxStateRetryWait
	updated.NotBefore = future.Format(timeFormatRFC3339)
	updated.UpdatedTime = updated.NotBefore

	for _, failKey := range append(outboxIndexKeys(updated), outboxStorageKey(updated.ID)) {
		t.Run(failKey, func(t *testing.T) {
			storage := &logical.InmemStorage{}
			if err := putOutbox(context.Background(), storage, existing); err != nil {
				t.Fatalf("write existing outbox: %v", err)
			}
			failing := failPutStorage{Storage: storage, failKey: failKey}
			if err := putOutbox(context.Background(), failing, updated); !errors.Is(err, errInjectedStoragePut) {
				t.Fatalf("update outbox error = %v, want injected failure", err)
			}
			stored := assertOutboxOperation(t, storage, existing.ID, existing.Version, outboxStatePending)
			if stored.NotBefore != existing.NotBefore {
				t.Fatalf("stored not_before = %q, want %q", stored.NotBefore, existing.NotBefore)
			}
			pendingIDs, err := listOutboxIDsForState(context.Background(), storage, outboxStatePending)
			if err != nil {
				t.Fatalf("list pending IDs: %v", err)
			}
			assertStringIDs(t, pendingIDs, existing.ID)
			retryIDs, err := listOutboxIDsForState(context.Background(), storage, outboxStateRetryWait)
			if err != nil {
				t.Fatalf("list retry IDs: %v", err)
			}
			assertStringIDs(t, retryIDs)
			dueIDs, err := listDueOutboxIDs(context.Background(), storage, now)
			if err != nil {
				t.Fatalf("list due IDs: %v", err)
			}
			assertStringIDs(t, dueIDs, existing.ID)
		})
	}
}

func TestOutboxUpdateCleanupFailureLeavesOnlyHarmlessStaleIndexes(t *testing.T) {
	storage := &logical.InmemStorage{}
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	existing := indexTestOutboxRecord(now)
	if err := putOutbox(context.Background(), storage, existing); err != nil {
		t.Fatalf("write existing outbox: %v", err)
	}
	updated := existing
	updated.State = outboxStateRetryWait
	updated.NotBefore = future.Format(timeFormatRFC3339)
	updated.UpdatedTime = updated.NotBefore
	failing := failDeleteStorage{
		Storage: storage,
		failKey: outboxByStateStorageKey(existing.State, existing.ID),
	}
	if err := putOutbox(context.Background(), failing, updated); !errors.Is(err, errInjectedStorageDelete) {
		t.Fatalf("update outbox error = %v, want injected delete failure", err)
	}

	assertOutboxOperation(t, storage, updated.ID, updated.Version, outboxStateRetryWait)
	pendingIDs, err := listOutboxIDsForState(context.Background(), storage, outboxStatePending)
	if err != nil {
		t.Fatalf("list pending IDs: %v", err)
	}
	assertStringIDs(t, pendingIDs)
	retryIDs, err := listOutboxIDsForState(context.Background(), storage, outboxStateRetryWait)
	if err != nil {
		t.Fatalf("list retry IDs: %v", err)
	}
	assertStringIDs(t, retryIDs, updated.ID)
	dueIDs, err := listDueOutboxIDs(context.Background(), storage, now)
	if err != nil {
		t.Fatalf("list stale due IDs: %v", err)
	}
	assertStringIDs(t, dueIDs)
}

func indexTestOutboxRecord(now time.Time) outboxRecord {
	nowString := now.Format(timeFormatRFC3339)
	return outboxRecord{
		ID:             "op-index-test",
		Type:           outbox.OperationTypeUpsert,
		Path:           "app/db",
		Version:        1,
		AssociationID:  "assoc-index-test",
		ObjectID:       syncObjectIDSecretPath,
		DestinationRef: "fake/default",
		State:          outboxStatePending,
		NotBefore:      nowString,
		CreatedTime:    nowString,
		UpdatedTime:    nowString,
		IdempotencyKey: "index-test",
		Trigger:        outboxTriggerUser,
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

var (
	errInjectedStoragePut    = errors.New("injected storage put failure")
	errInjectedStorageDelete = errors.New("injected storage delete failure")
)

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

type failDeleteStorage struct {
	logical.Storage

	failKey string
}

func (storage failDeleteStorage) Delete(ctx context.Context, key string) error {
	if key == storage.failKey {
		return errInjectedStorageDelete
	}
	return storage.Storage.Delete(ctx, key)
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
