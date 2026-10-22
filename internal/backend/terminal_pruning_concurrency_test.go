package backend

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/openbao/openbao/sdk/v2/physical"
)

const (
	terminalPruneTargetID           = "a-target"
	terminalPruneGateID             = "z-gate"
	terminalPruneNativeID           = "op-native"
	terminalPruneTransactionSuccess = "success"
)

func TestTerminalPruningBoundsLockedStorageWork(t *testing.T) {
	for _, backlog := range []int{40, 2000} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("backlog=%d/changed=%t", backlog, changed), func(t *testing.T) {
				storage := &logical.InmemStorage{}
				b := &secretSyncBackend{}
				now := nowUTC()
				for index := range backlog {
					putTerminalOutboxFixture(t, storage, fmt.Sprintf("op-%04d", index),
						now.Add(-terminalOutboxRetention-time.Hour))
				}
				counts := &terminalPruneLockWork{backend: b}
				probe := &terminalPruneProbeStorage{Storage: storage, observe: counts.observe}
				rewritten := false
				probe.afterGet = func(key string) {
					if changed && !rewritten && key == outboxStorageKey(fmt.Sprintf("op-%04d", backlog-1)) {
						// Replace the discovered records before candidate revalidation.
						rewritten = true
						for index := range backlog {
							markOutboxTerminal(t, storage, fmt.Sprintf("op-%04d", index), now)
						}
					}
				}
				wantDeleted := 5
				if changed {
					wantDeleted = 0
				}
				deleted, err := b.pruneTerminalOutboxRecordsLimit(context.Background(), probe, now, 5, 3, 2)
				if err != nil || deleted != wantDeleted {
					t.Fatalf("prune = %d, %v; want %d, nil", deleted, err, wantDeleted)
				}
				if counts.lockedLists != 0 || counts.unlockedReads < backlog ||
					counts.lockedReads > 2*(3+2) || counts.unlockedDeletes != 0 {
					t.Fatalf("storage work: locked lists=%d, unlocked reads=%d, locked reads=%d, unlocked deletes=%d",
						counts.lockedLists, counts.unlockedReads, counts.lockedReads, counts.unlockedDeletes)
				}
			})
		}
	}
}

func TestTerminalPruningAllowsQueueChangesDuringDiscovery(t *testing.T) {
	for _, action := range []string{"retry", "cancel", "refresh terminal", "active claim"} {
		t.Run(action, func(t *testing.T) {
			env := newBackendTestEnv(t)
			now := nowUTC()
			putTerminalOutboxFixture(t, env.storage, terminalPruneTargetID, now.Add(-terminalOutboxRetention-time.Hour))
			putTerminalOutboxFixture(t, env.storage, terminalPruneGateID, now)
			if action == "active claim" {
				record := assertOutboxOperation(t, env.storage, terminalPruneTargetID, 1, outboxStateFailedTerminal)
				record.ClaimOwner = "worker"
				record.ClaimExpiresTime = now.Add(time.Hour).Format(timeFormatRFC3339)
				if err := putOutbox(context.Background(), env.storage, *record); err != nil {
					t.Fatal(err)
				}
			}
			blocked, release := make(chan struct{}), make(chan struct{})
			var blockOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			probe := &terminalPruneProbeStorage{Storage: env.storage}
			probe.afterGet = func(key string) {
				if key == outboxStorageKey(terminalPruneGateID) {
					blockOnce.Do(func() {
						close(blocked)
						<-release
					})
				}
			}
			done := make(chan error, 1)
			go func() { done <- env.b.pruneTerminalOutboxRecords(context.Background(), probe, now) }()
			select {
			case <-blocked:
			case <-time.After(5 * time.Second):
				t.Fatal("pruning did not reach the discovery gate")
			}
			if !env.b.enqueueMu.TryLock() {
				t.Fatal("discovery holds enqueueMu and blocks queue actions")
			}
			env.b.enqueueMu.Unlock()
			switch action {
			case "retry", "cancel":
				assertNoErrorResponse(t, env.update("queue/"+terminalPruneTargetID+"/"+action))
			case "refresh terminal":
				markOutboxTerminal(t, env.storage, terminalPruneTargetID, now)
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pruning did not finish after discovery resumed")
			}
			assertTerminalPruneQueueChange(t, env.storage, action)
		})
	}
}

func assertTerminalPruneQueueChange(t *testing.T, storage logical.Storage, action string) {
	t.Helper()
	switch action {
	case "cancel":
		assertOutboxMissing(t, storage, terminalPruneTargetID)
		assertOutboxStateIndexed(t, storage, outboxStateFailedTerminal, terminalPruneTargetID, false)
	case "retry":
		record := assertOutboxOperation(t, storage, terminalPruneTargetID, 1, outboxStatePending)
		assertOutboxStateIndexed(t, storage, outboxStatePending, record.ID, true)
		assertOutboxDueIndexed(t, storage, record.NotBefore, record.ID, true)
	default:
		assertOutboxOperation(t, storage, terminalPruneTargetID, 1, outboxStateFailedTerminal)
		assertOutboxStateIndexed(t, storage, outboxStateFailedTerminal, terminalPruneTargetID, true)
	}
}

func TestTerminalPruningUsesNativeTransactions(t *testing.T) {
	for _, mode := range []string{terminalPruneTransactionSuccess, "commit failure", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			env, transactional := newTransactionalBackendTestEnv(t)
			now := nowUTC()
			putTerminalOutboxFixture(t, env.storage, terminalPruneNativeID, now.Add(-terminalOutboxRetention-time.Hour))
			before := snapshotLogicalStorage(t, env.storage)
			var storage logical.Storage = commitFailureTransactionalStorage{TransactionalStorage: transactional}
			switch mode {
			case terminalPruneTransactionSuccess:
				storage = transactional
			case "conflict":
				storage = conflictOnCommitTransactionalStorage{
					TransactionalStorage: transactional,
					beforeCommit: func(context.Context) error {
						markOutboxTerminal(t, env.storage, terminalPruneNativeID, now)
						return nil
					},
				}
			}
			deleted, err := env.b.pruneTerminalOutboxRecordsLimit(context.Background(), storage, now, 1000, 1, 1)
			if mode == terminalPruneTransactionSuccess {
				if deleted != 1 || err != nil {
					t.Fatalf("prune = %d, %v; want one, nil", deleted, err)
				}
				assertOutboxMissing(t, env.storage, terminalPruneNativeID)
				assertOutboxStateIndexed(t, env.storage, outboxStateFailedTerminal, terminalPruneNativeID, false)
				return
			}
			if deleted != 0 || !errors.Is(err, physical.ErrTransactionCommitFailure) {
				t.Fatalf("prune = %d, %v; want zero and a transaction commit failure", deleted, err)
			}
			if mode == "commit failure" && !reflect.DeepEqual(snapshotLogicalStorage(t, env.storage), before) {
				t.Fatal("failed transaction changed canonical records or indexes")
			}
			assertOutboxOperation(t, env.storage, terminalPruneNativeID, 1, outboxStateFailedTerminal)
			assertOutboxStateIndexed(t, env.storage, outboxStateFailedTerminal, terminalPruneNativeID, true)
		})
	}
}

type terminalPruneProbeStorage struct {
	logical.Storage
	observe  func(string)
	afterGet func(string)
}

func (s *terminalPruneProbeStorage) Get(ctx context.Context, key string) (*logical.StorageEntry, error) {
	if s.observe != nil {
		s.observe("get")
	}
	entry, err := s.Storage.Get(ctx, key)
	if s.afterGet != nil {
		s.afterGet(key)
	}
	return entry, err
}

func (s *terminalPruneProbeStorage) List(ctx context.Context, prefix string) ([]string, error) {
	if s.observe != nil {
		s.observe("list")
	}
	return s.Storage.List(ctx, prefix)
}

func (s *terminalPruneProbeStorage) Delete(ctx context.Context, key string) error {
	if s.observe != nil {
		s.observe("delete")
	}
	return s.Storage.Delete(ctx, key)
}

// terminalPruneLockWork measures storage work while only pruning is running.
// TryLock can therefore distinguish pruning's locked and unlocked phases.
type terminalPruneLockWork struct {
	backend         *secretSyncBackend
	unlockedReads   int
	lockedReads     int
	lockedLists     int
	unlockedDeletes int
}

func (p *terminalPruneLockWork) observe(operation string) {
	unlocked := p.backend.enqueueMu.TryLock()
	if unlocked {
		p.backend.enqueueMu.Unlock()
	}
	switch operation {
	case "get":
		if unlocked {
			p.unlockedReads++
		} else {
			p.lockedReads++
		}
	case "list":
		if !unlocked {
			p.lockedLists++
		}
	case "delete":
		if unlocked {
			p.unlockedDeletes++
		}
	}
}
