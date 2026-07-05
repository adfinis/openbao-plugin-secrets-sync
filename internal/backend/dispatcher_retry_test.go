package backend

import (
	"fmt"
	"testing"
	"time"
)

func TestRetryDelayAddsDeterministicOperationJitter(t *testing.T) {
	record := outboxRecord{ID: "operation-a", Attempts: 1}

	first := retryDelay(record)
	second := retryDelay(record)
	if first != second {
		t.Fatalf("retry delay changed for same operation: %s then %s", first, second)
	}
	assertRetryDelayWithinJitter(t, first, retryBaseDelay)
}

func TestRetryDelaySpreadsBatch(t *testing.T) {
	delays := make(map[time.Duration]struct{})
	for i := 0; i < 32; i++ {
		delay := retryDelay(outboxRecord{
			ID:       fmt.Sprintf("operation-%02d", i),
			Attempts: 1,
		})
		assertRetryDelayWithinJitter(t, delay, retryBaseDelay)
		delays[delay] = struct{}{}
	}
	if len(delays) < 2 {
		t.Fatalf("retry delay produced %d unique values, want at least 2", len(delays))
	}
}

func TestRetryDelayCapsJitteredDelay(t *testing.T) {
	delay := retryDelay(outboxRecord{ID: "operation-max", Attempts: 20})
	if delay != retryMaxDelay {
		t.Fatalf("retry delay = %s, want %s", delay, retryMaxDelay)
	}
}

func assertRetryDelayWithinJitter(t *testing.T, delay time.Duration, base time.Duration) {
	t.Helper()

	max := base + base/retryJitterDivisor
	if delay < base || delay > max {
		t.Fatalf("retry delay = %s, want between %s and %s", delay, base, max)
	}
}
