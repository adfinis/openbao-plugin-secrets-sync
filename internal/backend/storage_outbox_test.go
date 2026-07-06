package backend

import (
	"context"
	"sort"
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

func TestOutboxStateStringsAreStorageKeyEnums(t *testing.T) {
	for _, testCase := range []struct {
		name string
		got  string
		want string
	}{
		{name: "pending", got: outboxStatePending, want: "pending"},
		{name: "retry wait", got: outboxStateRetryWait, want: "retry_wait"},
		{name: "failed terminal", got: outboxStateFailedTerminal, want: "failed_terminal"},
		{name: "canceled", got: outboxStateCanceled, want: "canceled"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.want {
				t.Fatalf("outbox state = %q, want frozen storage key enum %q", testCase.got, testCase.want)
			}
		})
	}
}

func TestOutboxDueIndexUsesLexicalUTCSeconds(t *testing.T) {
	times := []time.Time{
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
		time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC),
		time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC),
	}
	keys := make([]string, 0, len(times))
	for _, at := range times {
		keys = append(keys, outboxDueIndexTime(outboxRecord{
			State:     outboxStatePending,
			NotBefore: at.Format(timeFormatRFC3339),
		}))
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for i := range keys {
		if keys[i] != sorted[i] {
			t.Fatalf("due index keys sort out of time order: got %v sorted %v", keys, sorted)
		}
	}

	if got := outboxDueIndexTime(outboxRecord{State: outboxStatePending}); got != outboxDueZeroTime {
		t.Fatalf("empty not_before due index = %q, want zero-time sentinel", got)
	}
}
