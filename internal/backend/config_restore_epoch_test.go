package backend

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/openbao/openbao/sdk/v2/physical"
)

func TestRejectedConfigWritePreservesRestoreEpoch(t *testing.T) {
	invalidFields := map[string]interface{}{
		"queue_capacity": -1, "security_posture": "strict", "drift_repair": "invalid",
		"drift_reconcile_interval": "30s", "drift_reconcile_batch": 0,
		"event_dispatch_max_operations": 0, "unknown_field": true,
	}
	for field, value := range invalidFields {
		t.Run(field, func(t *testing.T) {
			env := newBackendTestEnv(t)
			env.read(configPath)
			rearmRestoreGuard(t, env)
			before := snapshotLogicalStorage(t, env.storage)
			dispatchCh := captureEventDispatchSignals(t, env.b)
			response := env.update(configPath, map[string]interface{}{
				"restore_guard": false, "disabled": true, field: value,
			})
			if response == nil || !response.IsError() {
				t.Fatalf("invalid config response = %#v, want validation error", response)
			}
			if !reflect.DeepEqual(snapshotLogicalStorage(t, env.storage), before) {
				t.Fatal("rejected config write changed persistent state")
			}
			select {
			case <-dispatchCh:
				t.Fatal("rejected config write signaled event dispatch")
			default:
			}
		})
	}
}

func TestRestoreGuardTransitionRollsBackFailedTransaction(t *testing.T) {
	for _, path := range []string{configPath, "config/restore-guard/acknowledge"} {
		t.Run(path, func(t *testing.T) {
			env, transactional := newTransactionalBackendTestEnv(t)
			env.read(configPath)
			rearmRestoreGuard(t, env)
			before := snapshotLogicalStorage(t, env.storage)
			dispatchCh := captureEventDispatchSignals(t, env.b)
			_, err := env.b.HandleRequest(context.Background(), &logical.Request{
				Operation: logical.UpdateOperation, Path: path,
				Storage: commitFailureTransactionalStorage{TransactionalStorage: transactional},
				Data:    map[string]interface{}{"restore_guard": false},
			})
			if !errors.Is(err, physical.ErrTransactionCommitFailure) {
				t.Fatalf("config error = %v, want transaction commit failure", err)
			}
			if !reflect.DeepEqual(snapshotLogicalStorage(t, env.storage), before) {
				t.Fatal("failed config transaction changed persistent state")
			}
			select {
			case <-dispatchCh:
				t.Fatal("failed config transaction signaled event dispatch")
			default:
			}
		})
	}
}

func TestConfigWriteRotatesRestoreEpochOnce(t *testing.T) {
	env := newBackendTestEnv(t)
	before := env.read(configPath).Data["restore_epoch"]
	rearmRestoreGuard(t, env)
	write := func() {
		t.Helper()
		response := env.update(configPath, map[string]interface{}{"restore_guard": false})
		if response != nil && response.IsError() {
			t.Fatalf("clear restore guard: %v", response.Error())
		}
	}
	write()
	after := env.read(configPath)
	assertResponseValue(t, after, "restore_guard", false)
	if after.Data["restore_epoch"] == before {
		t.Fatal("successful guard acknowledgement did not rotate the epoch")
	}
	write()
	assertResponseValue(t, env.read(configPath), "restore_epoch", after.Data["restore_epoch"])
}

func TestRestoreGuardStorageFailureKeepsGuardActive(t *testing.T) {
	for _, path := range []string{configPath, "config/restore-guard/acknowledge"} {
		for _, key := range []string{restoreEpochKey, configPath} {
			t.Run(path+"/"+key, func(t *testing.T) {
				env := newBackendTestEnv(t)
				before := env.read(configPath).Data["restore_epoch"]
				rearmRestoreGuard(t, env)
				_, err := env.b.HandleRequest(context.Background(), &logical.Request{
					Operation: logical.UpdateOperation, Path: path,
					Storage: configWriteFailureStorage{Storage: env.storage, key: key},
					Data:    map[string]interface{}{"restore_guard": false},
				})
				if !errors.Is(err, errConfigWriteFailure) {
					t.Fatalf("config error = %v, want injected storage failure", err)
				}
				after := env.read(configPath)
				assertResponseValue(t, after, "restore_guard", true)
				if key == restoreEpochKey {
					assertResponseValue(t, after, "restore_epoch", before)
				}
			})
		}
	}
}

var errConfigWriteFailure = errors.New("injected config write failure")

type configWriteFailureStorage struct {
	logical.Storage
	key string
}

func (storage configWriteFailureStorage) Put(ctx context.Context, entry *logical.StorageEntry) error {
	if entry.Key == storage.key {
		return errConfigWriteFailure
	}
	return storage.Storage.Put(ctx, entry)
}

func rearmRestoreGuard(t *testing.T, env *backendTestEnv) {
	t.Helper()
	response := env.update(configPath, map[string]interface{}{"restore_guard": true})
	if response != nil && response.IsError() {
		t.Fatalf("rearm restore guard: %v", response.Error())
	}
}
