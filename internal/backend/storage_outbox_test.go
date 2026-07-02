package backend

import (
	"context"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestOutboxIndexesTrackPutAndDelete(t *testing.T) {
	storage := &logical.InmemStorage{}
	nowTime := nowUTC()
	now := nowTime.Format(timeFormatRFC3339)
	future := nowTime.Add(time.Minute).Format(timeFormatRFC3339)
	record := outboxRecord{
		ID:          "op_state_index",
		Type:        outbox.OperationTypeUpsert,
		Path:        "app/db",
		Version:     1,
		State:       outboxStatePending,
		NotBefore:   now,
		CreatedTime: now,
		UpdatedTime: now,
	}
	if err := putOutbox(context.Background(), storage, record); err != nil {
		t.Fatalf("write pending outbox: %v", err)
	}
	assertOutboxStateIndexed(t, storage, outboxStatePending, record.ID, true)
	assertOutboxStateIndexed(t, storage, outboxStateRetryWait, record.ID, false)
	assertOutboxDueIndexed(t, storage, now, record.ID, true)
	assertOutboxDueIndexed(t, storage, future, record.ID, false)

	record.State = outboxStateRetryWait
	record.NotBefore = future
	record.UpdatedTime = now
	if err := putOutbox(context.Background(), storage, record); err != nil {
		t.Fatalf("write retry-wait outbox: %v", err)
	}
	assertOutboxStateIndexed(t, storage, outboxStatePending, record.ID, false)
	assertOutboxStateIndexed(t, storage, outboxStateRetryWait, record.ID, true)
	assertOutboxDueIndexed(t, storage, now, record.ID, false)
	assertOutboxDueIndexed(t, storage, future, record.ID, true)

	if err := deleteOutbox(context.Background(), storage, record); err != nil {
		t.Fatalf("delete outbox: %v", err)
	}
	assertOutboxMissing(t, storage, record.ID)
	assertOutboxStateIndexed(t, storage, outboxStateRetryWait, record.ID, false)
	assertOutboxDueIndexed(t, storage, future, record.ID, false)

	record.State = outboxStatePending
	record.NotBefore = now
	if err := putOutbox(context.Background(), storage, record); err != nil {
		t.Fatalf("rewrite pending outbox: %v", err)
	}
	assertOutboxStateIndexed(t, storage, outboxStatePending, record.ID, true)
	assertOutboxDueIndexed(t, storage, now, record.ID, true)

	partial := record
	partial.State = ""
	partial.NotBefore = ""
	if err := deleteOutbox(context.Background(), storage, partial); err != nil {
		t.Fatalf("delete outbox with partial caller copy: %v", err)
	}
	assertOutboxMissing(t, storage, record.ID)
	assertOutboxStateIndexed(t, storage, outboxStatePending, record.ID, false)
	assertOutboxDueIndexed(t, storage, now, record.ID, false)
}
