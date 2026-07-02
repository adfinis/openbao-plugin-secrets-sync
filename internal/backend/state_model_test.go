package backend

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	modelSourcePath   = "app/db"
	modelResolvedName = "model/app/db"
	modelSecretCanary = "secret-canary-model"
)

type coreStateModel struct {
	version         int
	sourceAvailable bool
	pending         int
	state           domain.SyncState
}

func TestCoreStateModelSourceAssociationQueueLifecycle(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}
	createFakeDestination(t, b, storage, "default")

	model := coreStateModel{}
	associationID := ""
	steps := []struct {
		name string
		run  func(t *testing.T)
		want coreStateModel
	}{
		{
			name: "write initial source without association",
			run: func(t *testing.T) {
				resp := writeAppDBSecret(t, b, storage, modelSecretCanary+"-v1")
				metadata := resp.Data["metadata"].(map[string]interface{})
				assertOperationIDs(t, metadata, 0)
			},
			want: coreStateModel{
				version:         1,
				sourceAvailable: true,
				state:           domain.SyncStateNoAssociation,
			},
		},
		{
			name: "create association enqueues current version",
			run: func(t *testing.T) {
				markAppDBSyncable(t, b, storage)
				resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
					"destination_type": providerTypeFake,
					"destination_name": "default",
					"resolved_name":    modelResolvedName,
					"granularity":      syncGranularitySecretPath,
					"format":           defaultAssociationFormat,
					"delete_mode":      deleteModeDelete,
				})
				associationID = associationIDFromResponse(t, resp)
				if ids := operationIDsFromResponse(t, resp); len(ids) != 1 {
					t.Fatalf("association operation IDs = %v, want one", ids)
				}
			},
			want: coreStateModel{
				version:         1,
				sourceAvailable: true,
				pending:         1,
				state:           domain.SyncStatePending,
			},
		},
		{
			name: "drain creates synced status",
			run: func(t *testing.T) {
				drainCoreModelQueue(t, b, storage, 1)
			},
			want: coreStateModel{
				version:         1,
				sourceAvailable: true,
				state:           domain.SyncStateSynced,
			},
		},
		{
			name: "write update enqueues new version",
			run: func(t *testing.T) {
				resp := writeAppDBSecret(t, b, storage, modelSecretCanary+"-v2")
				metadata := resp.Data["metadata"].(map[string]interface{})
				assertOperationIDs(t, metadata, 1)
			},
			want: coreStateModel{
				version:         2,
				sourceAvailable: true,
				pending:         1,
				state:           domain.SyncStatePending,
			},
		},
		{
			name: "disable cancels queued version",
			run: func(t *testing.T) {
				resp := handleRequest(
					t,
					b,
					storage,
					logical.UpdateOperation,
					"associations/app/db/"+associationID+"/disable",
					nil,
				)
				assertNoErrorResponse(t, resp)
				if ids := stringSliceFromResponse(t, resp, "canceled_operation_ids"); len(ids) != 1 {
					t.Fatalf("canceled operation IDs = %v, want one", ids)
				}
			},
			want: coreStateModel{
				version:         2,
				sourceAvailable: true,
				state:           domain.SyncStateDisabled,
			},
		},
		{
			name: "enable requeues current version",
			run: func(t *testing.T) {
				resp := handleRequest(
					t,
					b,
					storage,
					logical.UpdateOperation,
					"associations/app/db/"+associationID+"/enable",
					nil,
				)
				if ids := operationIDsFromResponse(t, resp); len(ids) != 1 {
					t.Fatalf("enable operation IDs = %v, want one", ids)
				}
			},
			want: coreStateModel{
				version:         2,
				sourceAvailable: true,
				pending:         1,
				state:           domain.SyncStatePending,
			},
		},
		{
			name: "drain update returns synced",
			run: func(t *testing.T) {
				drainCoreModelQueue(t, b, storage, 1)
			},
			want: coreStateModel{
				version:         2,
				sourceAvailable: true,
				state:           domain.SyncStateSynced,
			},
		},
		{
			name: "manual sync requeues current version",
			run: func(t *testing.T) {
				resp := handleRequest(
					t,
					b,
					storage,
					logical.UpdateOperation,
					"associations/app/db/"+associationID+"/sync",
					nil,
				)
				if ids := operationIDsFromResponse(t, resp); len(ids) != 1 {
					t.Fatalf("manual sync operation IDs = %v, want one", ids)
				}
			},
			want: coreStateModel{
				version:         2,
				sourceAvailable: true,
				pending:         1,
				state:           domain.SyncStatePending,
			},
		},
		{
			name: "source delete replaces queued upsert with delete",
			run: func(t *testing.T) {
				resp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
				assertNoErrorResponse(t, resp)
				metadata := resp.Data["metadata"].(map[string]interface{})
				if ids := operationIDsFromMetadata(t, metadata); len(ids) != 1 {
					t.Fatalf("delete operation IDs = %v, want one", ids)
				}
			},
			want: coreStateModel{
				version: 2,
				pending: 1,
				state:   domain.SyncStatePending,
			},
		},
		{
			name: "drain delete records remote missing",
			run: func(t *testing.T) {
				drainCoreModelQueue(t, b, storage, 1)
			},
			want: coreStateModel{
				version: 2,
				state:   domain.SyncStateRemoteMissing,
			},
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.run(t)
			model = step.want
			assertCoreStateModel(t, b, storage, model)
		})
	}
}

func TestCoreStateModelProviderFailureInvariants(t *testing.T) {
	testCases := []struct {
		name           string
		resolvedName   string
		errorClass     providers.ErrorClass
		state          domain.SyncState
		operationState string
		retryable      bool
	}{
		{
			name:           "authn",
			resolvedName:   "prod/authn/app/db",
			errorClass:     providers.ErrorClassAuthn,
			state:          domain.SyncStateDestinationAuthError,
			operationState: outboxStateFailedTerminal,
		},
		{
			name:           "authz",
			resolvedName:   "prod/authz/app/db",
			errorClass:     providers.ErrorClassAuthz,
			state:          domain.SyncStateDestinationPolicyError,
			operationState: outboxStateFailedTerminal,
		},
		{
			name:           "ownership",
			resolvedName:   "prod/ownership/app/db",
			errorClass:     providers.ErrorClassOwnership,
			state:          domain.SyncStateRemoteOwnershipLost,
			operationState: outboxStateFailedTerminal,
		},
		{
			name:           "drift",
			resolvedName:   "prod/drift-newer/app/db",
			errorClass:     providers.ErrorClassDrift,
			state:          domain.SyncStateDrifted,
			operationState: outboxStateFailedTerminal,
		},
		{
			name:           "rate-limit",
			resolvedName:   "prod/rate-limit/app/db",
			errorClass:     providers.ErrorClassRateLimit,
			state:          domain.SyncStateDestinationRateLimited,
			operationState: outboxStateRetryWait,
			retryable:      true,
		},
		{
			name:           "unavailable",
			resolvedName:   "prod/unavailable/app/db",
			errorClass:     providers.ErrorClassUnavailable,
			state:          domain.SyncStateDestinationUnavailable,
			operationState: outboxStateRetryWait,
			retryable:      true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			b := Backend(&logical.BackendConfig{})
			storage := &logical.InmemStorage{}

			writeAppDBSecret(t, b, storage, modelSecretCanary+"-"+testCase.name)
			createFakeDestination(t, b, storage, "default")
			associationResp := createFakeAssociationWithResolvedName(t, b, storage, testCase.resolvedName)
			operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")

			acknowledgeRestoreGuard(t, b, storage)
			drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
				"max_operations": 1,
			})
			assertNoErrorResponse(t, drainResp)
			if got := drainResp.Data["processed"]; got != 1 {
				t.Fatalf("processed = %v, want 1", got)
			}

			operation := assertOutboxOperation(t, storage, operationID, 1, testCase.operationState)
			if operation.Attempts != 1 {
				t.Fatalf("attempts = %d, want 1", operation.Attempts)
			}
			if testCase.retryable {
				assertFutureNotBefore(t, operation.NotBefore)
				if operation.ClaimOwner != "" || operation.ClaimExpiresTime != "" || operation.ClaimAttempt != 0 {
					t.Fatalf("claim fields after retryable failure = %#v, want cleared", operation)
				}
			} else if operation.NotBefore != "" {
				t.Fatalf("not_before = %q, want empty for terminal failure", operation.NotBefore)
			}

			sourceState := testCase.state
			if testCase.retryable {
				sourceState = domain.SyncStatePending
			}
			assertCoreStateModel(t, b, storage, coreStateModel{
				version:         1,
				sourceAvailable: true,
				pending:         boolToInt(testCase.retryable),
				state:           sourceState,
			})
			assertStatusModelFailure(t, b, storage, testCase.errorClass, testCase.state)
		})
	}
}

func drainCoreModelQueue(t *testing.T, b *secretSyncBackend, storage logical.Storage, wantProcessed int) {
	t.Helper()
	acknowledgeRestoreGuard(t, b, storage)
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 10,
	})
	assertNoErrorResponse(t, resp)
	if got := resp.Data["processed"]; got != wantProcessed {
		t.Fatalf("processed = %v, want %d", got, wantProcessed)
	}
}

func assertCoreStateModel(t *testing.T, b logical.Backend, storage logical.Storage, model coreStateModel) {
	t.Helper()
	assertCoreMetadataModel(t, storage, model)
	assertCoreQueueModel(t, storage, model)
	assertCoreStatusModel(t, b, storage, model)
}

func assertCoreMetadataModel(t *testing.T, storage logical.Storage, model coreStateModel) {
	t.Helper()
	metadata, err := getMetadata(context.Background(), storage, modelSourcePath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if model.version == 0 {
		if metadata != nil {
			t.Fatalf("metadata = %#v, want nil", metadata)
		}
		return
	}
	if metadata == nil {
		t.Fatal("metadata must exist")
	}
	if metadata.CurrentVersion != model.version {
		t.Fatalf("current version = %d, want %d", metadata.CurrentVersion, model.version)
	}
	version, err := getVersion(context.Background(), storage, modelSourcePath, model.version)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version == nil {
		t.Fatalf("version %d must exist", model.version)
	}
	versionAvailable := !version.Destroyed && version.DeletionTime == ""
	if versionAvailable != model.sourceAvailable {
		t.Fatalf("version available = %v, want %v: %#v", versionAvailable, model.sourceAvailable, version)
	}
}

func assertCoreQueueModel(t *testing.T, storage logical.Storage, model coreStateModel) {
	t.Helper()
	ids, err := listQueuedOutboxIDsForPath(context.Background(), storage, modelSourcePath)
	if err != nil {
		t.Fatalf("list queued outbox: %v", err)
	}
	if len(ids) != model.pending {
		t.Fatalf("queued operations = %v, want %d", ids, model.pending)
	}
	for _, id := range ids {
		record, err := getOutbox(context.Background(), storage, id)
		if err != nil {
			t.Fatalf("read queued operation %s: %v", id, err)
		}
		assertQueuedOperationReferencesAvailableSourceState(t, storage, *record)
	}
}

func assertQueuedOperationReferencesAvailableSourceState(
	t *testing.T,
	storage logical.Storage,
	record outboxRecord,
) {
	t.Helper()
	version, err := getVersion(context.Background(), storage, record.Path, record.Version)
	if err != nil {
		t.Fatalf("read queued operation version: %v", err)
	}
	versionAvailable := version != nil && !version.Destroyed && version.DeletionTime == ""
	switch record.Type {
	case outbox.OperationTypeUpsert:
		if !versionAvailable {
			t.Fatalf("queued upsert references unavailable source version: %#v version=%#v", record, version)
		}
	case outbox.OperationTypeDelete:
		if versionAvailable {
			t.Fatalf("queued delete references available source version: %#v version=%#v", record, version)
		}
	default:
		t.Fatalf("queued operation type = %s, want upsert or delete", record.Type)
	}
}

func assertCoreStatusModel(t *testing.T, b logical.Backend, storage logical.Storage, model coreStateModel) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, resp)
	if got := resp.Data["state"]; got != string(model.state) {
		t.Fatalf("status state = %v, want %s: %#v", got, model.state, resp.Data)
	}
	if model.version > 0 {
		if got := resp.Data["version"]; got != model.version {
			t.Fatalf("status version = %v, want %d", got, model.version)
		}
	}
	if strings.Contains(fmt.Sprint(resp.Data), modelSecretCanary) {
		t.Fatalf("status leaks secret canary: %#v", resp.Data)
	}
}

func assertStatusModelFailure(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	errorClass providers.ErrorClass,
	state domain.SyncState,
) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, resp)
	objects := resp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 {
		t.Fatalf("status objects length = %d, want 1", len(objects))
	}
	object := objects[0]
	if got := object["last_error_class"]; got != string(errorClass) {
		t.Fatalf("last_error_class = %v, want %s", got, errorClass)
	}
	if got := object["state"]; got != string(state) {
		t.Fatalf("object state = %v, want %s", got, state)
	}
	if strings.Contains(fmt.Sprint(object), modelSecretCanary) {
		t.Fatalf("status object leaks secret canary: %#v", object)
	}
	assertNoPayloadHash(t, object)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
