package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/openbao/openbao/sdk/v2/helper/consts"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestPeriodicProcessesFakeOutbox(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	association := associationResp.Data["association"].(map[string]interface{})
	associationID := association["id"]
	assertResponseValue(t, associationResp, "association_id", associationID)
	assertResponseValue(t, associationResp, "destination_ref", "fake/default")

	env.runPeriodicAllowed("periodic")

	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertResponseValue(t, queueResp, "pending", 0)

	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	assertResponseValue(t, statusResp, "state", string(domain.SyncStateSynced))
	assertResponseValue(t, statusResp, "association_id", associationID)
	assertResponseValue(t, statusResp, "destination_ref", "fake/default")
	assertResponseValue(t, statusResp, "last_operation_id", operationID)
	assertSyncedStatusObject(t, statusResp.Data["objects"], operationID)

	assertOutboxMissing(t, env.storage, operationID)
}

func TestPeriodicLimitsProcessedOperations(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)
	for _, operationID := range operationIDsFromResponse(t, associationResp) {
		operation, err := getOutbox(context.Background(), env.storage, operationID)
		if err != nil {
			t.Fatalf("read initial operation: %v", err)
		}
		if operation != nil {
			if err := deleteOutbox(context.Background(), env.storage, *operation); err != nil {
				t.Fatalf("delete initial operation: %v", err)
			}
		}
	}

	now := nowUTC().Format(timeFormatRFC3339)
	for index := 0; index < defaultPeriodicMaxOperations+1; index++ {
		if err := putOutbox(context.Background(), env.storage, outboxRecord{
			ID:             fmt.Sprintf("op-periodic-%03d", index),
			Type:           outbox.OperationTypeUpsert,
			Path:           "app/db",
			Version:        1,
			AssociationID:  associationID,
			ObjectID:       syncObjectIDSecretPath,
			DestinationRef: "fake/default",
			State:          outboxStatePending,
			NotBefore:      now,
			CreatedTime:    now,
			UpdatedTime:    now,
		}); err != nil {
			t.Fatalf("write periodic operation %d: %v", index, err)
		}
	}

	env.runPeriodicAllowed("bounded periodic")
	ids, err := listQueuedOutboxIDs(context.Background(), env.storage)
	if err != nil {
		t.Fatalf("list queued outbox IDs: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("queued IDs after bounded periodic = %v, want one remaining", ids)
	}
}

func TestPeriodicDropsUnsupportedOutboxOperation(t *testing.T) {
	env := newBackendTestEnv(t)
	now := nowUTC().Format(timeFormatRFC3339)
	record := outboxRecord{
		ID:             "op-empty-object",
		Type:           outbox.OperationTypeUpsert,
		Path:           "app/db",
		Version:        1,
		AssociationID:  "assoc-invalid",
		DestinationRef: "fake/default",
		State:          outboxStatePending,
		NotBefore:      now,
		CreatedTime:    now,
		UpdatedTime:    now,
	}
	if err := putOutbox(context.Background(), env.storage, record); err != nil {
		t.Fatalf("write unsupported operation: %v", err)
	}

	env.runPeriodicAllowed("periodic invalid operation cleanup")
	assertOutboxMissing(t, env.storage, record.ID)
	assertQueueCount(t, env.b, env.storage, "pending", 0)
}

func TestPeriodicHonorsRestoreGuard(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	rearmResp := env.update("config", map[string]interface{}{
		"restore_guard": true,
	})
	if rearmResp != nil && rearmResp.IsError() {
		t.Fatalf("unexpected restore guard rearm error: %v", rearmResp.Error())
	}

	if err := env.b.periodic(context.Background(), &logical.Request{Storage: env.storage}); err != nil {
		t.Fatalf("periodic with restore guard: %v", err)
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	assertQueueCount(t, env.b, env.storage, "pending", 1)

	env.runPeriodicAllowed("periodic after restore guard acknowledgement")
	assertOutboxMissing(t, env.storage, operationID)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateSynced)
}

func TestPeriodicSkipsUnsafeReplicationNode(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]
	env.acknowledgeRestoreGuard()

	if err := env.b.Setup(context.Background(), &logical.BackendConfig{
		System: &logical.StaticSystemView{
			ReplicationStateVal: consts.ReplicationPerformanceSecondary,
		},
	}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	if err := env.b.periodic(context.Background(), &logical.Request{Storage: env.storage}); err != nil {
		t.Fatalf("periodic on unsafe replication node: %v", err)
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
}

func TestPeriodicRejectsPayloadOverProviderLimit(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": strings.Repeat("x", 1024*1024) + "secret-canary",
		},
	})
	assertNoErrorResponse(t, resp)
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.runPeriodicAllowed("periodic")

	operation, err := getOutbox(context.Background(), env.storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil || operation.State != outboxStateFailedTerminal {
		t.Fatalf("outbox operation = %#v, want terminal failure", operation)
	}
	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 {
		t.Fatalf("status objects length = %d, want 1", len(objects))
	}
	object := objects[0]
	if got := object["last_error_class"]; got != string(providers.ErrorClassCapacity) {
		t.Fatalf("last_error_class = %v, want %s", got, providers.ErrorClassCapacity)
	}
	if got := object["state"]; got != string(domain.SyncStateQueueBlocked) {
		t.Fatalf("state = %v, want %s", got, domain.SyncStateQueueBlocked)
	}
	if strings.Contains(object["last_error"].(string), "secret-canary") {
		t.Fatalf("last_error contains secret canary: %s", object["last_error"])
	}
	assertNoPayloadHash(t, object)
}

func TestPeriodicRejectsPayloadOverAWSProviderLimit(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": strings.Repeat("x", 70*1024) + "secret-canary",
		},
	})
	assertNoErrorResponse(t, resp)
	resp = env.update("destinations/aws-sm/prod", map[string]interface{}{
		"description":                             "aws production",
		awssecretsmanager.ConfigKeyRegion:         "us-east-1",
		awssecretsmanager.ConfigKeyEndpointURL:    "http://localhost:4566",
		awssecretsmanager.ConfigKeyEndpointPolicy: awssecretsmanager.EndpointPolicyLocal,
		awssecretsmanager.ConfigKeyAuthMode:       awssecretsmanager.AuthModeDefault,
	})
	if resp != nil && resp.IsError() {
		t.Fatalf("unexpected destination write error: %v", resp.Error())
	}
	env.markAppDBSyncable()
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination_type": awssecretsmanager.ProviderType,
		"destination_name": "prod",
		"resolved_name":    "openbao-plugin-secrets-sync/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.runPeriodicAllowed("periodic")

	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 {
		t.Fatalf("status objects length = %d, want 1", len(objects))
	}
	object := objects[0]
	if got := object["last_error_class"]; got != string(providers.ErrorClassCapacity) {
		t.Fatalf("last_error_class = %v, want %s", got, providers.ErrorClassCapacity)
	}
	if got := object["state"]; got != string(domain.SyncStateQueueBlocked) {
		t.Fatalf("state = %v, want %s", got, domain.SyncStateQueueBlocked)
	}
	if strings.Contains(object["last_error"].(string), "secret-canary") {
		t.Fatalf("last_error contains secret canary: %s", object["last_error"])
	}
	assertNoPayloadHash(t, object)
}

func TestPeriodicRetriesTransientProviderErrors(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/unavailable/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.runPeriodicAllowed("periodic")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	assertFutureNotBefore(t, operation.NotBefore)
	assertQueueCount(t, env.b, env.storage, "retry_wait", 1)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassUnavailable)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateDestinationUnavailable)

	for range 2 {
		operation = runDueRetry(t, env.b, env.storage, *operation)
	}
	operation = assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	if got := operation.Attempts; got != maxAutomaticRetryAttempts {
		t.Fatalf("attempts = %d, want %d", got, maxAutomaticRetryAttempts)
	}
}

func TestPeriodicLeavesClaimedOperationOnDispatchContextCancellation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/context-canceled/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.acknowledgeRestoreGuard()
	err := env.b.periodic(context.Background(), &logical.Request{Storage: env.storage})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("periodic error = %v, want context.Canceled", err)
	}

	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if got := operation.Attempts; got != 0 {
		t.Fatalf("attempts = %d, want 0", got)
	}
	if operation.ClaimOwner == "" {
		t.Fatal("claim owner must remain set")
	}
	if operation.ClaimExpiresTime == "" {
		t.Fatal("claim expiry must remain set")
	}
	if got := operation.ClaimAttempt; got != 1 {
		t.Fatalf("claim attempt = %d, want 1", got)
	}
	status, err := getStatus(
		context.Background(),
		env.storage,
		"app/db",
		associationIDFromResponse(t, associationResp),
		syncObjectIDSecretPath,
	)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want nil", status)
	}
}

func TestPeriodicLeavesClaimedOperationWhenCanceledProviderRedactsCause(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.providerRegistry = providers.MustNewRegistry(contextCanceledProvider{cancel: cancel})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	destinationResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"destinations/ctxcancel/default",
		nil,
	)
	if destinationResp != nil && destinationResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", destinationResp.Error())
	}
	markAppDBSyncable(t, b, storage)
	associationResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": "ctxcancel",
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
	assertNoErrorResponse(t, associationResp)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	acknowledgeRestoreGuard(t, b, storage)
	err := b.periodic(ctx, &logical.Request{Storage: storage})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("periodic error = %v, want context.Canceled", err)
	}

	operation := assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)
	if got := operation.Attempts; got != 0 {
		t.Fatalf("attempts = %d, want 0", got)
	}
	if operation.ClaimOwner == "" {
		t.Fatal("claim owner must remain set")
	}
	if operation.ClaimExpiresTime == "" {
		t.Fatal("claim expiry must remain set")
	}
	if got := operation.ClaimAttempt; got != 1 {
		t.Fatalf("claim attempt = %d, want 1", got)
	}
	status, err := getStatus(
		context.Background(),
		storage,
		"app/db",
		associationIDFromResponse(t, associationResp),
		syncObjectIDSecretPath,
	)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want nil", status)
	}
}

func TestIsDispatchContextCanceledTreatsRedactedProviderErrorAsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !isDispatchContextCanceled(ctx, &providers.Error{Class: providers.ErrorClassUnavailable, Message: "redacted"}) {
		t.Fatal("canceled context with redacted provider error must be treated as cancellation")
	}
}

func TestPeriodicRetriesRateLimitProviderErrors(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createFakeAssociationWithResolvedName("prod/rate-limit/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	env.runPeriodicAllowed("periodic")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	assertFutureNotBefore(t, operation.NotBefore)
	assertQueueCount(t, env.b, env.storage, "retry_wait", 1)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassRateLimit)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateDestinationRateLimited)
}

func TestPeriodicMapsProviderMutationErrorClasses(t *testing.T) {
	testCases := []struct {
		name         string
		resolvedName string
		errorClass   providers.ErrorClass
		state        domain.SyncState
	}{
		{
			name:         "authn",
			resolvedName: "prod/authn/app/db",
			errorClass:   providers.ErrorClassAuthn,
			state:        domain.SyncStateDestinationAuthError,
		},
		{
			name:         "authz",
			resolvedName: "prod/authz/app/db",
			errorClass:   providers.ErrorClassAuthz,
			state:        domain.SyncStateDestinationPolicyError,
		},
		{
			name:         "ownership",
			resolvedName: "prod/ownership/app/db",
			errorClass:   providers.ErrorClassOwnership,
			state:        domain.SyncStateRemoteOwnershipLost,
		},
		{
			name:         "collision",
			resolvedName: "prod/collision/app/db",
			errorClass:   providers.ErrorClassCollision,
			state:        domain.SyncStateDrifted,
		},
		{
			name:         "validation",
			resolvedName: "prod/validation/app/db",
			errorClass:   providers.ErrorClassValidation,
			state:        domain.SyncStateValidationError,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newBackendTestEnv(t)

			env.writeAppDBSecret("initial")
			env.createFakeDestination("default")
			associationResp := env.createFakeAssociationWithResolvedName(testCase.resolvedName)
			operationID := operationIDsFromResponse(t, associationResp)[0]

			env.runPeriodicAllowed("periodic")
			operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
			if got := operation.Attempts; got != 1 {
				t.Fatalf("attempts = %d, want 1", got)
			}
			assertStatusObjectErrorClass(t, env.b, env.storage, testCase.errorClass)
			assertStatusObjectState(t, env.b, env.storage, testCase.state)
		})
	}
}

func TestPeriodicHonorsDisabledConfig(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.createDefaultFakeAssociation()
	env.update(configPath, map[string]interface{}{
		"disabled": true,
	})

	env.runPeriodicAllowed("periodic")

	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertResponseValue(t, queueResp, "pending", 1)
}
