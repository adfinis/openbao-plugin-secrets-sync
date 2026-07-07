package backend

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestDataWriteReadAndQueueStatus(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"username": "app",
			"password": "secret",
		},
	})
	assertNoErrorResponse(t, writeResp)
	writeMetadata := writeResp.Data["metadata"].(map[string]interface{})
	if got := writeMetadata["version"]; got != 1 {
		t.Fatalf("write version = %v, want 1", got)
	}
	if got := writeMetadata["sync_state"]; got != string(domain.SyncStateNoAssociation) {
		t.Fatalf("sync state = %v, want %s", got, domain.SyncStateNoAssociation)
	}
	assertOperationIDs(t, writeMetadata, 0)

	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if got := payload["username"]; got != "app" {
		t.Fatalf("username = %v, want app", got)
	}
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("read version = %v, want 1", got)
	}

	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertResponseValue(t, queueResp, "pending", 0)

	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	assertResponseValue(t, statusResp, "state", string(domain.SyncStateNoAssociation))
	assertResponseValue(t, statusResp, "version", 1)
}

func TestDataWriteShorthandPayload(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update("data/app/db", map[string]interface{}{
		"username": "app",
		"password": "secret",
	})
	assertNoErrorResponse(t, writeResp)

	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if got := payload["username"]; got != "app" {
		t.Fatalf("username = %v, want app", got)
	}
	if got := payload["password"]; got != "secret" {
		t.Fatalf("password = %v, want secret", got)
	}
	if _, ok := payload["path"]; ok {
		t.Fatalf("path must not be stored as source payload: %#v", payload)
	}
}

func TestDataWriteShorthandRejectsReservedVersionPayloadKey(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("data/app/db", map[string]interface{}{
		"password": "secret",
		"version":  5,
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("reserved shorthand response = %#v, want error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "reserved data write field version") {
		t.Fatalf("reserved shorthand error = %q", resp.Error().Error())
	}
}

func TestDataWriteShorthandRejectsReservedOptionsPayloadKey(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("data/app/db", map[string]interface{}{
		"password": "secret",
		"options": map[string]interface{}{
			"cas": 0,
		},
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("reserved shorthand response = %#v, want error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "reserved data write field options") {
		t.Fatalf("reserved shorthand error = %q", resp.Error().Error())
	}
}

func TestDataWriteHonorsStrictSourceOptInBeforeEnqueue(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, associationResp)
	assertOperationIDs(t, associationResp.Data, 1)

	cfgResp := env.update("config", map[string]interface{}{
		"require_source_opt_in": true,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	writeResp := env.writeAppDBSecret("rotated")
	writeMetadata := writeResp.Data["metadata"].(map[string]interface{})
	if got := writeMetadata["version"]; got != 2 {
		t.Fatalf("strict write version = %v, want 2", got)
	}
	assertOperationIDs(t, writeMetadata, 0)
}

func TestDataWriteCAS(t *testing.T) {
	env := newBackendTestEnv(t)

	firstResp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "initial",
		},
		"options": map[string]interface{}{
			"cas": 0,
		},
	})
	assertNoErrorResponse(t, firstResp)

	secondResp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
		"options": map[string]interface{}{
			"cas": 0,
		},
	})
	if !secondResp.IsError() {
		t.Fatal("second write with cas=0 must fail")
	}

	thirdResp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "rotated",
		},
		"options": map[string]interface{}{
			"cas": 1,
		},
	})
	assertNoErrorResponse(t, thirdResp)
	metadata := thirdResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 2 {
		t.Fatalf("third write version = %v, want 2", got)
	}
}

func TestDataWriteShorthandCAS(t *testing.T) {
	env := newBackendTestEnv(t)

	firstResp := env.update("data/app/db", map[string]interface{}{
		"password": "initial",
		"cas":      0,
	})
	assertNoErrorResponse(t, firstResp)

	secondResp := env.update("data/app/db", map[string]interface{}{
		"password": "blocked",
		"cas":      0,
	})
	if secondResp == nil || !secondResp.IsError() {
		t.Fatalf("second write with cas=0 response = %#v, want error", secondResp)
	}

	thirdResp := env.update("data/app/db", map[string]interface{}{
		"password": "rotated",
		"cas":      "1",
	})
	assertNoErrorResponse(t, thirdResp)
	metadata := thirdResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 2 {
		t.Fatalf("third write version = %v, want 2", got)
	}

	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if _, ok := payload["cas"]; ok {
		t.Fatalf("cas alias must not be stored as source payload: %#v", payload)
	}
	if got := payload["password"]; got != "rotated" {
		t.Fatalf("password = %v, want rotated", got)
	}
}

func TestDataWriteWrappedPayloadAllowsMatchingCASAlias(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "initial",
		},
		"options": map[string]interface{}{
			"cas": 0,
		},
		"cas": 0,
	})
	assertNoErrorResponse(t, resp)
}

func TestDataWriteRejectsMixedWrappedAndShorthandPayload(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "wrapped",
		},
		"username": "app",
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("mixed write response = %#v, want error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "cannot mix wrapped data with top-level source payload fields username") {
		t.Fatalf("mixed write error = %q", resp.Error().Error())
	}
}

func TestDataWriteRejectsConflictingCASForms(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "initial",
		},
		"options": map[string]interface{}{
			"cas": 0,
		},
		"cas": 1,
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("conflicting CAS response = %#v, want error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "cas and options.cas must match") {
		t.Fatalf("conflicting CAS error = %q", resp.Error().Error())
	}
}

func TestConcurrentDataWritesPreserveVersions(t *testing.T) {
	env := newBackendTestEnv(t)
	const writers = 32
	resp := env.update("metadata/app/db", map[string]interface{}{
		"max_versions": writers,
	})
	assertNoErrorResponse(t, resp)

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
				Operation: logical.UpdateOperation,
				Path:      "data/app/db",
				Storage:   env.storage,
				Data: map[string]interface{}{
					"data": map[string]interface{}{
						"password": fmt.Sprintf("secret-%02d", index),
					},
				},
			})
			if err != nil {
				errs <- err
				return
			}
			if resp == nil {
				errs <- fmt.Errorf("write %d returned nil response", index)
				return
			}
			if resp.IsError() {
				errs <- fmt.Errorf("write %d returned error response: %v", index, resp.Error())
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	metadata, err := getMetadata(context.Background(), env.storage, "app/db")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if metadata == nil {
		t.Fatal("metadata must exist")
	}
	if got := metadata.CurrentVersion; got != writers {
		t.Fatalf("current version = %d, want %d", got, writers)
	}
	for version := 1; version <= writers; version++ {
		record, err := getVersion(context.Background(), env.storage, "app/db", version)
		if err != nil {
			t.Fatalf("read version %d: %v", version, err)
		}
		if record == nil {
			t.Fatalf("version %d is missing", version)
		}
	}
}

func TestDataWriteSupersedesStaleQueuedUpserts(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	staleOperationID := operationIDsFromResponse(t, associationResp)[0]

	rotatedResp := env.writeAppDBSecret("rotated")
	rotatedMetadata := rotatedResp.Data["metadata"].(map[string]interface{})
	rotatedOperationID := requireSingleOperationID(
		t,
		operationIDsFromMetadata(t, rotatedMetadata),
		"rotated write",
	)

	assertOutboxMissing(t, env.storage, staleOperationID)
	assertOutboxOperation(t, env.storage, rotatedOperationID, 2, outboxStatePending)
	assertQueueCount(t, env.b, env.storage, "pending", 1)
}
