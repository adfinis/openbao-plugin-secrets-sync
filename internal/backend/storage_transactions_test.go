package backend

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	log "github.com/hashicorp/go-hclog"
	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/openbao/openbao/sdk/v2/physical"
	"github.com/openbao/openbao/sdk/v2/physical/inmem"
)

type countingTransactionalStorage struct {
	logical.TransactionalStorage

	writeTransactions atomic.Int32
}

func (s *countingTransactionalStorage) BeginTx(ctx context.Context) (logical.Transaction, error) {
	s.writeTransactions.Add(1)
	return s.TransactionalStorage.BeginTx(ctx)
}

type commitFailureTransactionalStorage struct {
	logical.TransactionalStorage
}

func (s commitFailureTransactionalStorage) BeginTx(ctx context.Context) (logical.Transaction, error) {
	tx, err := s.TransactionalStorage.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return commitFailureTransaction{Transaction: tx}, nil
}

type commitFailureTransaction struct {
	logical.Transaction
}

func (commitFailureTransaction) Commit(context.Context) error {
	return physical.ErrTransactionCommitFailure
}

var errInjectedTransactionBegin = errors.New("injected transaction begin failure")

type beginFailureTransactionalStorage struct {
	logical.TransactionalStorage
}

func (beginFailureTransactionalStorage) BeginTx(context.Context) (logical.Transaction, error) {
	return nil, errInjectedTransactionBegin
}

type conflictOnCommitTransactionalStorage struct {
	logical.TransactionalStorage

	beforeCommit func(context.Context) error
}

func (s conflictOnCommitTransactionalStorage) BeginTx(ctx context.Context) (logical.Transaction, error) {
	tx, err := s.TransactionalStorage.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return conflictOnCommitTransaction{
		Transaction:  tx,
		beforeCommit: s.beforeCommit,
	}, nil
}

type conflictOnCommitTransaction struct {
	logical.Transaction

	beforeCommit func(context.Context) error
}

func (tx conflictOnCommitTransaction) Commit(ctx context.Context) error {
	if err := tx.beforeCommit(ctx); err != nil {
		return err
	}
	return tx.Transaction.Commit(ctx)
}

func TestSourceMutationsUseNativeTransactions(t *testing.T) {
	tests := []struct {
		name             string
		prepare          func(*backendTestEnv)
		operation        logical.Operation
		path             string
		data             map[string]interface{}
		wantTransactions int32
	}{
		{
			name:      "data write",
			operation: logical.UpdateOperation,
			path:      "data/app/db",
			data: map[string]interface{}{
				"data": map[string]interface{}{"password": "initial"},
			},
			wantTransactions: 1,
		},
		{
			name: "data delete",
			prepare: func(env *backendTestEnv) {
				env.writeAppDBSecret("initial")
			},
			operation:        logical.DeleteOperation,
			path:             "data/app/db",
			wantTransactions: 1,
		},
		{
			name: "version delete",
			prepare: func(env *backendTestEnv) {
				env.writeAppDBSecret("initial")
				env.writeAppDBSecret("rotated")
			},
			operation: logical.UpdateOperation,
			path:      "delete/app/db",
			data: map[string]interface{}{
				"versions": []int{1, 2},
			},
			wantTransactions: 2,
		},
		{
			name: "version undelete",
			prepare: func(env *backendTestEnv) {
				env.writeAppDBSecret("initial")
				assertNoErrorResponse(t, env.delete("data/app/db"))
			},
			operation: logical.UpdateOperation,
			path:      "undelete/app/db",
			data: map[string]interface{}{
				"versions": []int{1},
			},
			wantTransactions: 1,
		},
		{
			name: "version destroy",
			prepare: func(env *backendTestEnv) {
				env.writeAppDBSecret("initial")
			},
			operation: logical.UpdateOperation,
			path:      "destroy/app/db",
			data: map[string]interface{}{
				"versions": []int{1},
			},
			wantTransactions: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, transactional := newTransactionalBackendTestEnv(t)
			if test.prepare != nil {
				test.prepare(env)
			}
			counting := &countingTransactionalStorage{TransactionalStorage: transactional}

			resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
				Operation: test.operation,
				Path:      test.path,
				Storage:   counting,
				Data:      test.data,
			})
			if err != nil {
				t.Fatalf("mutation failed: %v", err)
			}
			if resp != nil && resp.IsError() {
				t.Fatalf("mutation response: %v", resp.Error())
			}
			if got := counting.writeTransactions.Load(); got != test.wantTransactions {
				t.Fatalf("write transactions = %d, want %d", got, test.wantTransactions)
			}
		})
	}
}

func TestSourceMutationCommitFailureRollsBackWithoutDispatch(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*backendTestEnv)
		operation logical.Operation
		path      string
		data      map[string]interface{}
	}{
		{
			name: "data write",
			prepare: func(env *backendTestEnv) {
				prepareSyncedDeleteModeSource(t, env)
			},
			operation: logical.UpdateOperation,
			path:      "data/app/db",
			data: map[string]interface{}{
				"data": map[string]interface{}{"password": "rotated"},
			},
		},
		{
			name: "data delete",
			prepare: func(env *backendTestEnv) {
				prepareSyncedDeleteModeSource(t, env)
			},
			operation: logical.DeleteOperation,
			path:      "data/app/db",
		},
		{
			name: "version delete",
			prepare: func(env *backendTestEnv) {
				prepareSyncedDeleteModeSource(t, env)
			},
			operation: logical.UpdateOperation,
			path:      "delete/app/db",
			data:      map[string]interface{}{"versions": []int{1}},
		},
		{
			name: "version undelete",
			prepare: func(env *backendTestEnv) {
				prepareSyncedDeleteModeSource(t, env)
				assertNoErrorResponse(t, env.delete("data/app/db"))
				env.runPeriodicAllowed("prepare remote delete")
			},
			operation: logical.UpdateOperation,
			path:      "undelete/app/db",
			data:      map[string]interface{}{"versions": []int{1}},
		},
		{
			name: "version destroy",
			prepare: func(env *backendTestEnv) {
				prepareSyncedDeleteModeSource(t, env)
			},
			operation: logical.UpdateOperation,
			path:      "destroy/app/db",
			data:      map[string]interface{}{"versions": []int{1}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, transactional := newTransactionalBackendTestEnv(t)
			test.prepare(env)
			before := snapshotLogicalStorage(t, env.storage)
			dispatchCh := captureEventDispatchSignals(t, env.b)

			resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
				Operation: test.operation,
				Path:      test.path,
				Storage: commitFailureTransactionalStorage{
					TransactionalStorage: transactional,
				},
				Data: test.data,
			})
			if !errors.Is(err, physical.ErrTransactionCommitFailure) {
				t.Fatalf("mutation response = %#v, error = %v, want commit failure", resp, err)
			}
			after := snapshotLogicalStorage(t, env.storage)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("storage changed after failed commit\nbefore: %#v\nafter:  %#v", before, after)
			}
			select {
			case <-dispatchCh:
				t.Fatal("failed transaction signaled event dispatch")
			default:
			}
		})
	}
}

func TestSourceMutationBeginFailureDoesNotMutateStorage(t *testing.T) {
	env, transactional := newTransactionalBackendTestEnv(t)
	if _, err := ensureRuntimeState(context.Background(), env.storage); err != nil {
		t.Fatalf("initialize runtime state: %v", err)
	}
	before := snapshotLogicalStorage(t, env.storage)

	resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "data/app/db",
		Storage: beginFailureTransactionalStorage{
			TransactionalStorage: transactional,
		},
		Data: map[string]interface{}{
			"data": map[string]interface{}{"password": "initial"},
		},
	})
	if !errors.Is(err, errInjectedTransactionBegin) {
		t.Fatalf("mutation response = %#v, error = %v, want begin failure", resp, err)
	}
	if got := snapshotLogicalStorage(t, env.storage); !reflect.DeepEqual(got, before) {
		t.Fatalf("storage changed after begin failure\nbefore: %#v\nafter:  %#v", before, got)
	}
}

func TestSourceMutationConflictReturnsNativeCommitFailure(t *testing.T) {
	env, transactional := newTransactionalBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	conflictingStorage := conflictOnCommitTransactionalStorage{
		TransactionalStorage: transactional,
		beforeCommit: func(ctx context.Context) error {
			metadata, err := getMetadata(ctx, env.storage, "app/db")
			if err != nil {
				return err
			}
			metadata.UpdatedTime = "2026-07-19T00:00:00Z"
			return putMetadata(ctx, env.storage, "app/db", *metadata)
		},
	}

	resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "data/app/db",
		Storage:   conflictingStorage,
		Data: map[string]interface{}{
			"data": map[string]interface{}{"password": "rotated"},
		},
	})
	if !errors.Is(err, physical.ErrTransactionCommitFailure) {
		t.Fatalf("mutation response = %#v, error = %v, want native commit failure", resp, err)
	}
	metadata, err := getMetadata(context.Background(), env.storage, "app/db")
	if err != nil {
		t.Fatalf("read metadata after conflict: %v", err)
	}
	if got := metadata.CurrentVersion; got != 1 {
		t.Fatalf("current version after conflict = %d, want 1", got)
	}
	version, err := getVersion(context.Background(), env.storage, "app/db", 2)
	if err != nil {
		t.Fatalf("read uncommitted version: %v", err)
	}
	if version != nil {
		t.Fatalf("version 2 after conflict = %#v, want nil", version)
	}
}

func newTransactionalBackendTestEnv(t *testing.T) (*backendTestEnv, logical.TransactionalStorage) {
	t.Helper()
	physicalStorage, err := inmem.NewInmem(map[string]string{}, log.NewNullLogger())
	if err != nil {
		t.Fatalf("create transactional physical storage: %v", err)
	}
	storage := logical.NewLogicalStorage(physicalStorage)
	transactional, ok := storage.(logical.TransactionalStorage)
	if !ok {
		t.Fatal("logical storage does not expose transactional storage")
	}
	return &backendTestEnv{
		t:       t,
		b:       newBackendForTest(&logical.BackendConfig{}),
		storage: storage,
	}, transactional
}

func prepareSyncedDeleteModeSource(t *testing.T, env *backendTestEnv) {
	t.Helper()
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createFakeDeleteModeAssociation()
	env.runPeriodicAllowed("prepare synced source")
}

func snapshotLogicalStorage(t *testing.T, storage logical.Storage) map[string][]byte {
	t.Helper()
	keys, err := logical.CollectKeys(context.Background(), storage)
	if err != nil {
		t.Fatalf("collect storage keys: %v", err)
	}
	snapshot := make(map[string][]byte, len(keys))
	for _, key := range keys {
		entry, err := storage.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("read storage key %q: %v", key, err)
		}
		if entry != nil {
			snapshot[key] = append([]byte(nil), entry.Value...)
		}
	}
	return snapshot
}

func captureEventDispatchSignals(t *testing.T, b *secretSyncBackend) <-chan eventDispatchSignal {
	t.Helper()
	b.eventDispatchMu.Lock()
	b.eventDispatchCh = make(chan eventDispatchSignal, 1)
	dispatchCh := b.eventDispatchCh
	b.eventDispatchMu.Unlock()
	t.Cleanup(func() {
		b.eventDispatchMu.Lock()
		b.eventDispatchCh = nil
		b.eventDispatchMu.Unlock()
	})
	return dispatchCh
}
