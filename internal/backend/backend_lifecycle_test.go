package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestBackendStartsStorageLifecycleDuringInitialize(t *testing.T) {
	env := newBackendTestEnv(t)
	config := &logical.BackendConfig{StorageView: env.storage}
	if err := env.b.Setup(context.Background(), config); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	t.Cleanup(func() {
		env.b.Cleanup(context.Background())
	})

	if env.b.eventDispatchCh != nil {
		t.Fatal("setup must not start the event dispatcher")
	}
	if entry, err := env.storage.Get(context.Background(), storageSchemaKey); err != nil {
		t.Fatalf("read schema after setup: %v", err)
	} else if entry != nil {
		t.Fatal("setup must not initialize runtime storage")
	}

	if err := env.b.Initialize(context.Background(), &logical.InitializationRequest{
		Storage: env.storage,
	}); err != nil {
		t.Fatalf("initialize backend: %v", err)
	}
	if env.b.eventDispatchCh == nil {
		t.Fatal("initialize must start the event dispatcher")
	}
	for _, key := range []string{storageSchemaKey, pluginInstanceKey, restoreEpochKey, configPath} {
		entry, err := env.storage.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("read initialized key %q: %v", key, err)
		}
		if entry == nil {
			t.Fatalf("initialized key %q is missing", key)
		}
	}
}

func TestBackendInitializeFailsClosedBeforeStartingDispatcher(t *testing.T) {
	env := newBackendTestEnv(t)
	writeIncompatibleStorageSchema(t, env.storage)
	if err := env.b.Setup(context.Background(), &logical.BackendConfig{StorageView: env.storage}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}

	err := env.b.Initialize(context.Background(), &logical.InitializationRequest{Storage: env.storage})
	if err == nil {
		t.Fatal("initialize error = nil, want incompatible schema error")
	}
	if !strings.Contains(err.Error(), "incompatible storage schema") {
		t.Fatalf("initialize error = %q, want incompatible storage schema", err.Error())
	}
	if env.b.eventDispatchCh != nil {
		t.Fatal("failed initialization must not start the event dispatcher")
	}
}

func TestBackendInitializeWithoutStorageIsNoOp(t *testing.T) {
	b := Backend(nil)
	if err := b.Initialize(context.Background(), nil); err != nil {
		t.Fatalf("nil initialization request: %v", err)
	}
	if err := b.Initialize(context.Background(), &logical.InitializationRequest{}); err != nil {
		t.Fatalf("initialization without storage: %v", err)
	}
	if b.eventDispatchCh != nil {
		t.Fatal("initialization without storage must not start the event dispatcher")
	}
}
