package backend

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/fake"
	"github.com/openbao/openbao/sdk/v2/logical"
)

type backendTestEnv struct {
	t       *testing.T
	b       *secretSyncBackend
	storage logical.Storage
}

const testMountUUID = "00000000-0000-4000-8000-000000000001"

func newBackendTestEnv(t *testing.T) *backendTestEnv {
	t.Helper()
	return &backendTestEnv{
		t:       t,
		b:       newBackendForTest(&logical.BackendConfig{}),
		storage: &logical.InmemStorage{},
	}
}

func newBackendForTest(conf *logical.BackendConfig) *secretSyncBackend {
	mountUUID := backendUUIDFromConfig(conf)
	if mountUUID == "" {
		mountUUID = testMountUUID
	}
	providerSet := append(productionProviders(), fake.Provider{})
	return backendWithProviders(mountUUID, providerSet...)
}

func (env *backendTestEnv) handle(
	operation logical.Operation,
	path string,
	data map[string]interface{},
) *logical.Response {
	env.t.Helper()
	return handleRequest(env.t, env.b, env.storage, operation, path, data)
}

func (env *backendTestEnv) read(path string, data ...map[string]interface{}) *logical.Response {
	env.t.Helper()
	return env.handle(logical.ReadOperation, path, optionalRequestData(data))
}

func (env *backendTestEnv) update(path string, data ...map[string]interface{}) *logical.Response {
	env.t.Helper()
	return env.handle(logical.UpdateOperation, path, optionalRequestData(data))
}

func (env *backendTestEnv) delete(path string, data ...map[string]interface{}) *logical.Response {
	env.t.Helper()
	return env.handle(logical.DeleteOperation, path, optionalRequestData(data))
}

func (env *backendTestEnv) list(path string, data ...map[string]interface{}) *logical.Response {
	env.t.Helper()
	return env.handle(logical.ListOperation, path, optionalRequestData(data))
}

func optionalRequestData(data []map[string]interface{}) map[string]interface{} {
	if len(data) == 0 {
		return nil
	}
	return data[0]
}

func (env *backendTestEnv) acknowledgeRestoreGuard() {
	env.t.Helper()
	acknowledgeRestoreGuard(env.t, env.b, env.storage)
}

func (env *backendTestEnv) runPeriodicAllowed(label string) {
	env.t.Helper()
	runPeriodicAllowed(env.t, env.b, env.storage, label)
}

func (env *backendTestEnv) writeAppDBSecret(password string) *logical.Response {
	env.t.Helper()
	return writeAppDBSecret(env.t, env.b, env.storage, password)
}

func (env *backendTestEnv) writeAppDBSecretData(data map[string]interface{}) *logical.Response {
	env.t.Helper()
	return writeAppDBSecretData(env.t, env.b, env.storage, data)
}

func (env *backendTestEnv) writeAppDBSecretDataNoAssert(data map[string]interface{}) *logical.Response {
	env.t.Helper()
	return writeAppDBSecretDataNoAssert(env.t, env.b, env.storage, data)
}

func (env *backendTestEnv) createFakeDestination(name string) {
	env.t.Helper()
	createFakeDestination(env.t, env.b, env.storage, name)
}

func (env *backendTestEnv) createDefaultConstrainedFakeDestination() {
	env.t.Helper()
	createDefaultConstrainedFakeDestination(env.t, env.b, env.storage)
}

func (env *backendTestEnv) createDefaultFakeAssociation() *logical.Response {
	env.t.Helper()
	return createDefaultFakeAssociation(env.t, env.b, env.storage)
}

func (env *backendTestEnv) createFakeAssociationForPath(path string) *logical.Response {
	env.t.Helper()
	return createFakeAssociationForPath(env.t, env.b, env.storage, path)
}

func (env *backendTestEnv) createFakeSecretKeyAssociation(deleteMode string) *logical.Response {
	env.t.Helper()
	return createFakeSecretKeyAssociation(env.t, env.b, env.storage, deleteMode)
}

func (env *backendTestEnv) createFakeDeleteModeAssociation() *logical.Response {
	env.t.Helper()
	return createFakeDeleteModeAssociation(env.t, env.b, env.storage)
}

func (env *backendTestEnv) createFakeAssociationWithResolvedName(resolvedName string) *logical.Response {
	env.t.Helper()
	return createFakeAssociationWithResolvedName(env.t, env.b, env.storage, resolvedName)
}

func (env *backendTestEnv) planDefaultFakeAssociation(resolvedName string) *logical.Response {
	env.t.Helper()
	return planDefaultFakeAssociation(env.t, env.b, env.storage, resolvedName)
}

func (env *backendTestEnv) enableAppDBSourceSync() {
	env.t.Helper()
	enableAppDBSourceSync(env.t, env.b, env.storage)
}

func (env *backendTestEnv) enableSourceSync(path string) {
	env.t.Helper()
	enableSourceSync(env.t, env.b, env.storage, path)
}

func assertStoredAWSDestinationConfig(t *testing.T, storage logical.Storage) {
	t.Helper()
	storedDestination, err := getDestination(context.Background(), storage, awssecretsmanager.ProviderType, "prod")
	if err != nil {
		t.Fatalf("read stored destination: %v", err)
	}
	if _, ok := storedDestination.Config[awssecretsmanager.ConfigKeyExternalID]; ok {
		t.Fatal("external_id must not be stored in non-sensitive destination config")
	}
	storedSensitiveConfig, err := getDestinationSensitiveConfig(
		context.Background(),
		storage,
		awssecretsmanager.ProviderType,
		"prod",
	)
	if err != nil {
		t.Fatalf("read stored sensitive config: %v", err)
	}
	if storedSensitiveConfig == nil {
		t.Fatal("sensitive destination config must be stored separately")
	}
	if got := storedSensitiveConfig.Config[awssecretsmanager.ConfigKeyExternalID]; got != "tenant-1" {
		t.Fatalf("stored external_id = %q, want tenant-1", got)
	}
}

func assertNoStoredAWSDestination(t *testing.T, storage logical.Storage) {
	t.Helper()
	storedDestination, err := getDestination(context.Background(), storage, awssecretsmanager.ProviderType, "prod")
	if err != nil {
		t.Fatalf("read stored destination: %v", err)
	}
	if storedDestination != nil {
		t.Fatalf("stored destination = %#v, want nil", storedDestination)
	}
	storedSensitiveConfig, err := getDestinationSensitiveConfig(
		context.Background(),
		storage,
		awssecretsmanager.ProviderType,
		"prod",
	)
	if err != nil {
		t.Fatalf("read stored sensitive config: %v", err)
	}
	if storedSensitiveConfig != nil {
		t.Fatalf("stored sensitive config = %#v, want nil", storedSensitiveConfig)
	}
}

func assertStringMapValue(t *testing.T, values map[string]string, key string, expected string) {
	t.Helper()
	if got := values[key]; got != expected {
		t.Fatalf("%s = %v, want %s", key, got, expected)
	}
}

func assertInterfaceMapValue(t *testing.T, values map[string]interface{}, key string, expected string) {
	t.Helper()
	if got := values[key]; got != expected {
		t.Fatalf("%s = %v, want %s", key, got, expected)
	}
}

func assertReadAWSDestinationConfig(t *testing.T, b *secretSyncBackend, storage logical.Storage) {
	t.Helper()
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/aws-sm/prod", nil)
	assertNoErrorResponse(t, readResp)
	config := readResp.Data["config"].(map[string]interface{})
	if got := config[awssecretsmanager.ConfigKeyRegion]; got != "eu-central-1" {
		t.Fatalf("aws destination region = %v, want eu-central-1", got)
	}
	if got := config[awssecretsmanager.ConfigKeyAuthMode]; got != awssecretsmanager.AuthModeAssumeRole {
		t.Fatalf("aws auth_mode = %v, want %s", got, awssecretsmanager.AuthModeAssumeRole)
	}
	if _, ok := config[awssecretsmanager.ConfigKeyExternalID]; ok {
		t.Fatal("read config must not include external_id")
	}
	sensitiveConfig := readResp.Data["sensitive_config"].(map[string]interface{})
	if got := sensitiveConfig["redacted"]; got != true {
		t.Fatalf("sensitive_config redacted = %v, want true", got)
	}
	if got := sensitiveConfig["configured"]; got != true {
		t.Fatalf("sensitive_config configured = %v, want true", got)
	}
	keys, ok := sensitiveConfig["keys"].([]string)
	if !ok {
		t.Fatalf("sensitive keys = %T, want []string", sensitiveConfig["keys"])
	}
	if len(keys) != 1 || keys[0] != awssecretsmanager.ConfigKeyExternalID {
		t.Fatalf("sensitive keys = %v, want [%s]", keys, awssecretsmanager.ConfigKeyExternalID)
	}
}

func handleRequest(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	operation logical.Operation,
	path string,
	data map[string]interface{},
) *logical.Response {
	t.Helper()
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: operation,
		Path:      path,
		Storage:   storage,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("%s %s: %v", operation, path, err)
	}
	return resp
}

func acknowledgeRestoreGuard(t *testing.T, b logical.Backend, storage logical.Storage) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "config/restore-guard/acknowledge", nil)
	assertNoErrorResponse(t, resp)
	if got := resp.Data["restore_guard"]; got != false {
		t.Fatalf("restore_guard = %v, want false", got)
	}
	if got := resp.Data["restore_guard_acknowledged_time"]; got == "" {
		t.Fatal("restore_guard_acknowledged_time must be set")
	}
}

func runPeriodicAllowed(t *testing.T, b *secretSyncBackend, storage logical.Storage, label string) {
	t.Helper()
	acknowledgeRestoreGuard(t, b, storage)
	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func assertNoErrorResponse(t *testing.T, resp *logical.Response) {
	t.Helper()
	if resp == nil {
		t.Fatal("response must not be nil")
	}
	if resp.IsError() {
		t.Fatalf("unexpected error response: %v", resp.Error())
	}
}

func assertListKeys(t *testing.T, resp *logical.Response, expected []string) {
	t.Helper()
	assertNoErrorResponse(t, resp)
	if len(expected) == 0 {
		if _, ok := resp.Data["keys"]; !ok {
			return
		}
	}
	rawKeys, ok := resp.Data["keys"]
	if !ok {
		t.Fatalf("list response keys missing, want %v", expected)
	}
	keys, ok := rawKeys.([]string)
	if !ok {
		t.Fatalf("list response keys = %T, want []string", rawKeys)
	}
	if len(keys) != len(expected) {
		t.Fatalf("list response keys = %v, want %v", keys, expected)
	}
	for index, expectedKey := range expected {
		if keys[index] != expectedKey {
			t.Fatalf("list response keys = %v, want %v", keys, expected)
		}
	}
}

func assertPrunedEnqueueIntentAndOutbox(
	t *testing.T,
	storage logical.Storage,
	path string,
	version int,
	metadata map[string]interface{},
) {
	t.Helper()
	operationIDs := operationIDsFromMetadata(t, metadata)
	intent, err := getEnqueueIntent(context.Background(), storage, path, version)
	if err != nil {
		t.Fatalf("read enqueue intent: %v", err)
	}
	if intent != nil {
		t.Fatalf("enqueue intent = %#v, want pruned", intent)
	}
	operation, err := getOutbox(context.Background(), storage, operationIDs[0])
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil {
		t.Fatal("outbox operation must exist")
	}
	if got := operation.ObjectID; got != syncObjectIDSecretPath {
		t.Fatalf("outbox object ID = %v, want %s", got, syncObjectIDSecretPath)
	}
}

func claimOperationFixture(t *testing.T, storage logical.Storage, operationID string) {
	t.Helper()
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read operation to claim: %v", err)
	}
	if operation == nil {
		t.Fatalf("operation %s must exist", operationID)
	}
	operation.ClaimOwner = "worker-active"
	operation.ClaimExpiresTime = nowUTC().Add(time.Hour).Format(timeFormatRFC3339)
	operation.ClaimAttempt = operation.Attempts + 1
	if err := putOutbox(context.Background(), storage, *operation); err != nil {
		t.Fatalf("write claimed operation: %v", err)
	}
}

func assertOperationIDs(t *testing.T, metadata map[string]interface{}, expected int) {
	t.Helper()
	operationIDs := operationIDsFromMetadata(t, metadata)
	if len(operationIDs) != expected {
		t.Fatalf("operation IDs = %v, want %d entries", operationIDs, expected)
	}
}

func assertAssociationEnabled(t *testing.T, resp *logical.Response, want bool) {
	t.Helper()
	assertNoErrorResponse(t, resp)
	if got := resp.Data["enabled"]; got != want {
		t.Fatalf("association enabled = %v, want %v", got, want)
	}
}

func assertStringSlice(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("string slice = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("string slice = %v, want %v", got, want)
		}
	}
}

func assertStringSet(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("string set = %v, want %v", got, want)
	}
	counts := make(map[string]int, len(want))
	for _, value := range want {
		counts[value]++
	}
	for _, value := range got {
		counts[value]--
	}
	for value, count := range counts {
		if count != 0 {
			t.Fatalf("string set = %v, want %v; count for %q = %d", got, want, value, count)
		}
	}
}

func assertNoPayloadHash(t *testing.T, object map[string]interface{}) {
	t.Helper()
	if _, ok := object["payload_sha256"]; ok {
		t.Fatalf("payload_sha256 must not be exposed in response object: %#v", object)
	}
	if _, ok := object["remote_payload_sha256"]; ok {
		t.Fatalf("remote_payload_sha256 must not be exposed in response object: %#v", object)
	}
}

func assertResponseValue[T comparable](t *testing.T, resp *logical.Response, key string, want T) {
	t.Helper()
	if got := resp.Data[key]; got != want {
		t.Fatalf("%s = %v, want %v", key, got, want)
	}
}

func assertHintContains(t *testing.T, data map[string]interface{}, want string) { //nolint:forbidigo
	t.Helper()
	data = diagnosticTestData(data)
	hint, ok := data["hint"].(string)
	if !ok {
		t.Fatalf("hint = %T, want string", data["hint"])
	}
	if !strings.Contains(hint, want) {
		t.Fatalf("hint = %q, want substring %q", hint, want)
	}
}

func assertNextActionCommand( //nolint:forbidigo
	t *testing.T,
	data map[string]interface{},
	action string,
	command string,
) {
	t.Helper()
	data = diagnosticTestData(data)
	actions, ok := data["next_actions"].([]map[string]interface{})
	if !ok {
		t.Fatalf("next_actions = %T, want []map[string]interface{}", data["next_actions"])
	}
	for _, candidate := range actions {
		if candidate["action"] == action {
			if got := candidate["command"]; got != command {
				t.Fatalf("%s command = %v, want %s", action, got, command)
			}
			return
		}
	}
	t.Fatalf("next_actions = %#v, want action %q", actions, action)
}

func diagnosticTestData(data map[string]interface{}) map[string]interface{} { //nolint:forbidigo
	if _, ok := data["hint"]; ok {
		return data
	}
	nested, ok := data["data"].(map[string]interface{})
	if !ok {
		return data
	}
	return nested
}

func assertQueueCount(t *testing.T, b logical.Backend, storage logical.Storage, key string, want int) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, resp)
	if got := resp.Data[key]; got != want {
		t.Fatalf("%s queue count = %v, want %d", key, got, want)
	}
}

func assertDisabledStatusObject(t *testing.T, b logical.Backend, storage logical.Storage, version int) {
	t.Helper()
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 || objects[0]["state"] != string(domain.SyncStateDisabled) {
		t.Fatalf("status objects = %#v, want disabled object", objects)
	}
	if got := objects[0]["version"]; got != version {
		t.Fatalf("disabled status version = %v, want %d", got, version)
	}
}

func objectsByIDFromRaw(t *testing.T, raw interface{}) map[string]map[string]interface{} { //nolint:forbidigo
	t.Helper()
	objects, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("objects = %T, want []map[string]interface{}", raw)
	}
	byID := make(map[string]map[string]interface{}, len(objects)) //nolint:forbidigo
	for _, object := range objects {
		objectID, ok := object["object_id"].(string)
		if !ok || objectID == "" {
			t.Fatalf("object id = %v, want non-empty string", object["object_id"])
		}
		if _, exists := byID[objectID]; exists {
			t.Fatalf("duplicate object id %q in %#v", objectID, objects)
		}
		byID[objectID] = object
	}
	return byID
}

func assertPlanObject(
	t *testing.T,
	objects map[string]map[string]interface{}, //nolint:forbidigo
	objectID string,
	resolvedName string,
) {
	t.Helper()
	object, ok := objects[objectID]
	if !ok {
		t.Fatalf("plan object %q missing in %#v", objectID, objects)
	}
	if got := object["resolved_name"]; got != resolvedName {
		t.Fatalf("%s resolved_name = %v, want %s", objectID, got, resolvedName)
	}
	if got := object["action"]; got != providers.PlanActionCreate {
		t.Fatalf("%s action = %v, want %s", objectID, got, providers.PlanActionCreate)
	}
	assertNoPayloadHash(t, object)
	if got := object["payload_bytes"].(int); got <= 0 {
		t.Fatalf("%s payload_bytes = %d, want positive", objectID, got)
	}
}

func assertOperationObjectIDs(
	t *testing.T,
	storage logical.Storage,
	operationIDs []string,
	version int,
	state string,
	wantObjectIDs []string,
) {
	t.Helper()
	if len(operationIDs) != len(wantObjectIDs) {
		t.Fatalf("operation IDs = %v, want %d entries", operationIDs, len(wantObjectIDs))
	}
	got := make(map[string]struct{}, len(operationIDs))
	for _, operationID := range operationIDs {
		operation := assertOutboxOperation(t, storage, operationID, version, state)
		got[operation.ObjectID] = struct{}{}
	}
	for _, wantObjectID := range wantObjectIDs {
		if _, ok := got[wantObjectID]; !ok {
			t.Fatalf("operation object IDs = %v, missing %s", got, wantObjectID)
		}
	}
	if len(got) != len(wantObjectIDs) {
		t.Fatalf("operation object IDs = %v, want %v", got, wantObjectIDs)
	}
}

func assertSecretKeySyncedStatusObject(
	t *testing.T,
	objects map[string]map[string]interface{}, //nolint:forbidigo
	objectID string,
	associationID string,
	resolvedName string,
	operationID string,
) {
	t.Helper()
	object, ok := objects[objectID]
	if !ok {
		t.Fatalf("status object %q missing in %#v", objectID, objects)
	}
	if got := object["association_id"]; got != associationID {
		t.Fatalf("%s association_id = %v, want %s", objectID, got, associationID)
	}
	if got := object["resolved_name"]; got != resolvedName {
		t.Fatalf("%s resolved_name = %v, want %s", objectID, got, resolvedName)
	}
	if got := object["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("%s state = %v, want %s", objectID, got, domain.SyncStateSynced)
	}
	if got := object["last_operation_id"]; got != operationID {
		t.Fatalf("%s last_operation_id = %v, want %s", objectID, got, operationID)
	}
	if got := object["remote_version"]; got != "fake" {
		t.Fatalf("%s remote_version = %v, want fake", objectID, got)
	}
	assertNoPayloadHash(t, object)
}

func assertStatusObjectErrorClass(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	want providers.ErrorClass,
) {
	t.Helper()
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if got := objects[0]["last_error_class"]; got != string(want) {
		t.Fatalf("last_error_class = %v, want %s", got, want)
	}
}

func assertStatusObjectState(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	want domain.SyncState,
) {
	t.Helper()
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if got := objects[0]["state"]; got != string(want) {
		t.Fatalf("state = %v, want %s", got, want)
	}
}

func assertOutboxOperation(
	t *testing.T,
	storage logical.Storage,
	operationID string,
	version int,
	state string,
) *outboxRecord {
	t.Helper()
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil {
		t.Fatalf("outbox operation %s must exist", operationID)
	}
	if got := operation.Version; got != version {
		t.Fatalf("outbox operation version = %d, want %d", got, version)
	}
	if got := operation.State; got != state {
		t.Fatalf("outbox operation state = %s, want %s", got, state)
	}
	return operation
}

func assertOutboxMissing(t *testing.T, storage logical.Storage, operationID string) {
	t.Helper()
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation != nil {
		t.Fatalf("outbox operation %s = %#v, want pruned", operationID, operation)
	}
}

func assertOutboxStateIndexed(t *testing.T, storage logical.Storage, state string, operationID string, want bool) {
	t.Helper()
	entry, err := storage.Get(context.Background(), outboxByStateStorageKey(state, operationID))
	if err != nil {
		t.Fatalf("read outbox state index: %v", err)
	}
	if got := entry != nil; got != want {
		t.Fatalf("outbox state index %s/%s exists = %t, want %t", state, operationID, got, want)
	}
}

func assertOutboxDueIndexed(t *testing.T, storage logical.Storage, dueTime string, operationID string, want bool) {
	t.Helper()
	entry, err := storage.Get(context.Background(), outboxByDueStorageKey(dueTime, operationID))
	if err != nil {
		t.Fatalf("read outbox due index: %v", err)
	}
	if got := entry != nil; got != want {
		t.Fatalf("outbox due index %s/%s exists = %t, want %t", dueTime, operationID, got, want)
	}
}

func assertFutureNotBefore(t *testing.T, raw string) {
	t.Helper()
	notBefore, err := time.Parse(timeFormatRFC3339, raw)
	if err != nil {
		t.Fatalf("parse not_before: %v", err)
	}
	if !notBefore.After(nowUTC()) {
		t.Fatalf("not_before = %s, want future retry", raw)
	}
}

func runDueRetry(
	t *testing.T,
	b *secretSyncBackend,
	storage logical.Storage,
	operation outboxRecord,
) *outboxRecord {
	t.Helper()
	operation.NotBefore = nowUTC().Add(-time.Second).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), storage, operation); err != nil {
		t.Fatalf("write due retry operation: %v", err)
	}
	runPeriodicAllowed(t, b, storage, "periodic retry")
	updated, err := getOutbox(context.Background(), storage, operation.ID)
	if err != nil {
		t.Fatalf("read retry operation: %v", err)
	}
	if updated == nil {
		t.Fatalf("retry operation %s must exist", operation.ID)
	}
	return updated
}

func canceledOperationIDsFromResponse(t *testing.T, resp *logical.Response) []string {
	t.Helper()
	const key = "canceled_operation_ids"
	rawIDs, ok := resp.Data[key].([]string)
	if !ok {
		t.Fatalf("%s = %T, want []string", key, resp.Data[key])
	}
	return rawIDs
}

func requireSingleOperationID(t *testing.T, operationIDs []string, label string) string {
	t.Helper()
	if len(operationIDs) != 1 {
		t.Fatalf("%s operation IDs = %v, want one operation", label, operationIDs)
	}
	return operationIDs[0]
}

func sourceGeneration(t *testing.T, storage logical.Storage) string {
	t.Helper()
	path := "app/db"
	metadata, err := getMetadata(context.Background(), storage, path)
	if err != nil {
		t.Fatalf("read source metadata: %v", err)
	}
	if metadata == nil {
		t.Fatalf("source metadata %s must exist", path)
	}
	if metadata.Generation == "" {
		t.Fatalf("source metadata %s generation must be set", path)
	}
	return metadata.Generation
}

func operationIDsFromMetadata(t *testing.T, metadata map[string]interface{}) []string {
	t.Helper()
	rawIDs, ok := metadata["sync_operation_ids"].([]string)
	if !ok {
		t.Fatalf("sync_operation_ids = %T, want []string", metadata["sync_operation_ids"])
	}
	return rawIDs
}

func operationIDsFromResponse(t *testing.T, resp *logical.Response) []string {
	t.Helper()
	assertNoErrorResponse(t, resp)
	rawIDs, ok := resp.Data["sync_operation_ids"].([]string)
	if !ok {
		t.Fatalf("sync_operation_ids = %T, want []string", resp.Data["sync_operation_ids"])
	}
	return rawIDs
}

func associationIDFromResponse(t *testing.T, resp *logical.Response) string {
	t.Helper()
	assertNoErrorResponse(t, resp)
	id, ok := resp.Data["association_id"].(string)
	if !ok || id == "" {
		t.Fatalf("association_id = %v, want non-empty string", resp.Data["association_id"])
	}
	return id
}

func hasKey(keys []string, expected string) bool {
	for _, key := range keys {
		if key == expected {
			return true
		}
	}
	return false
}

func writeAppDBSecret(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	password string,
) *logical.Response {
	t.Helper()
	return writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": password,
	})
}

func writeAppDBSecretData(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	data map[string]interface{},
) *logical.Response {
	t.Helper()
	resp := writeAppDBSecretDataNoAssert(t, b, storage, data)
	assertNoErrorResponse(t, resp)
	return resp
}

func writeAppDBSecretDataNoAssert(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	data map[string]interface{},
) *logical.Response {
	t.Helper()
	return handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": data,
	})
}

func createFakeDestination(t *testing.T, b logical.Backend, storage logical.Storage, name string) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/fake/"+name, map[string]interface{}{
		"description": "test destination",
	})
	if resp != nil && resp.IsError() {
		t.Fatalf("unexpected destination write error: %v", resp.Error())
	}
}

func createDefaultConstrainedFakeDestination(t *testing.T, b logical.Backend, storage logical.Storage) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/fake/default", map[string]interface{}{
		"description": "test destination",
		destinationAllowedSourcePathPrefixesField:   "app",
		destinationAllowedResolvedNamePrefixesField: "prod/app/",
	})
	if resp != nil && resp.IsError() {
		t.Fatalf("unexpected constrained destination write error: %v", resp.Error())
	}
}

func createDefaultFakeAssociation(t *testing.T, b logical.Backend, storage logical.Storage) *logical.Response {
	t.Helper()
	enableAppDBSourceSync(t, b, storage)
	return handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
}

func createFakeAssociationForPath(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	path string,
) *logical.Response {
	t.Helper()
	enableSourceSync(t, b, storage, path)
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "data/"+path, map[string]interface{}{
		"data": map[string]interface{}{
			"password": path,
		},
	})
	assertNoErrorResponse(t, resp)
	resp = handleRequest(t, b, storage, logical.UpdateOperation, "associations/"+path, map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/" + path,
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, resp)
	return resp
}

func createFakeSecretKeyAssociation(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	deleteMode string,
) *logical.Response {
	t.Helper()
	enableAppDBSourceSync(t, b, storage)
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
		"delete_mode":   deleteMode,
	})
	assertNoErrorResponse(t, resp)
	return resp
}

func createFakeDeleteModeAssociation(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
) *logical.Response {
	t.Helper()
	enableAppDBSourceSync(t, b, storage)
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
		"delete_mode":   deleteModeDelete,
	})
	assertNoErrorResponse(t, resp)
	return resp
}

func createFakeAssociationWithResolvedName(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	resolvedName string,
) *logical.Response {
	t.Helper()
	enableAppDBSourceSync(t, b, storage)
	return handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": resolvedName,
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
}

func planDefaultFakeAssociation(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	resolvedName string,
) *logical.Response {
	t.Helper()
	return handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db/plan", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": resolvedName,
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
}

type recordingObserver struct {
	operations            []observability.OperationEvent
	driftRepairs          []observability.DriftRepairEvent
	remoteMutationBlocked []observability.RemoteMutationBlockedEvent
}

func (*recordingObserver) QueueDepth(context.Context, string, int) {}

func (r *recordingObserver) Operation(_ context.Context, event observability.OperationEvent) {
	r.operations = append(r.operations, event)
}

func (*recordingObserver) ProviderRequest(context.Context, observability.ProviderRequestEvent) {}

func (*recordingObserver) ReadinessCheck(context.Context, observability.ReadinessCheckEvent) {}

func (r *recordingObserver) RemoteMutationBlocked(
	_ context.Context,
	event observability.RemoteMutationBlockedEvent,
) {
	r.remoteMutationBlocked = append(r.remoteMutationBlocked, event)
}

func (*recordingObserver) ReconcileRun(context.Context, observability.ReconcileRunEvent) {}

func (r *recordingObserver) DriftRepair(_ context.Context, event observability.DriftRepairEvent) {
	r.driftRepairs = append(r.driftRepairs, event)
}

func (*recordingObserver) QueueCapacity(context.Context, observability.QueueCapacityEvent) {}

func (*recordingObserver) RestoreGuardActive(context.Context, bool) {}

type contextCanceledProvider struct {
	cancel context.CancelFunc
}

func (contextCanceledProvider) Type() string {
	return "ctxcancel"
}

func (contextCanceledProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsValueReadback:       true,
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		MaxPayloadBytes:             1024 * 1024,
	}
}

func (contextCanceledProvider) ValidateConfig(context.Context, providers.DestinationConfig) error {
	return nil
}

func (contextCanceledProvider) NormalizeAssociationConfig(
	context.Context,
	providers.DestinationConfig,
	providers.AssociationConfig,
) (providers.AssociationConfig, error) {
	return providers.AssociationConfig{Config: map[string]string{}}, nil
}

func (p contextCanceledProvider) OpenDestination(
	context.Context,
	providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	return contextCanceledRuntime(p), nil
}

type contextCanceledRuntime struct {
	cancel context.CancelFunc
}

func (contextCanceledRuntime) Health(context.Context) (*providers.HealthResult, error) {
	return &providers.HealthResult{Healthy: true}, nil
}

func (contextCanceledRuntime) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
}

func (r contextCanceledRuntime) Upsert(ctx context.Context, _ providers.UpsertRequest) (*providers.SyncResult, error) {
	if r.cancel != nil {
		r.cancel()
	}
	if ctx.Err() != nil {
		return nil, &providers.Error{
			Class:   providers.ErrorClassUnavailable,
			Message: "redacted provider request failed",
		}
	}
	return &providers.SyncResult{RemoteVersion: "ctxcancel"}, nil
}

func (r contextCanceledRuntime) Delete(ctx context.Context, _ providers.DeleteRequest) (*providers.SyncResult, error) {
	if r.cancel != nil {
		r.cancel()
	}
	if ctx.Err() != nil {
		return nil, &providers.Error{
			Class:   providers.ErrorClassUnavailable,
			Message: "redacted provider request failed",
		}
	}
	return &providers.SyncResult{RemoteVersion: "ctxcancel-deleted"}, nil
}

func (contextCanceledRuntime) ReadState(context.Context, providers.ReadStateRequest) (*providers.RemoteState, error) {
	return &providers.RemoteState{Exists: true, OwnershipKnown: true, Owned: true}, nil
}

func (contextCanceledRuntime) Close(context.Context) error {
	return nil
}

func enableAppDBSourceSync(t *testing.T, b logical.Backend, storage logical.Storage) {
	t.Helper()
	enableSourceSync(t, b, storage, "app/db")
}

func enableSourceSync(t *testing.T, b logical.Backend, storage logical.Storage, path string) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "sources/"+path+"/enable", nil)
	assertNoErrorResponse(t, resp)
}

func assertSyncedStatusObject(t *testing.T, raw interface{}, operationID string) { //nolint:forbidigo
	t.Helper()
	objects, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("objects = %T, want []map[string]interface{}", raw)
	}
	if len(objects) != 1 {
		t.Fatalf("objects length = %d, want 1", len(objects))
	}
	object := objects[0]
	if got := object["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("object state = %v, want %s", got, domain.SyncStateSynced)
	}
	if got := object["last_operation_id"]; got != operationID {
		t.Fatalf("object last operation ID = %v, want %s", got, operationID)
	}
	if got := object["remote_version"]; got != "fake" {
		t.Fatalf("object remote version = %v, want fake", got)
	}
	assertNoPayloadHash(t, object)
}
