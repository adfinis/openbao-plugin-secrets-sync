package backend

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/adfinis/openbao-secret-sync/internal/outbox"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-secret-sync/internal/providers/gitlab"
	"github.com/adfinis/openbao-secret-sync/internal/providers/kubernetessecrets"
	"github.com/openbao/openbao/sdk/v2/helper/consts"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestFactoryCreatesLogicalBackend(t *testing.T) {
	b, err := Factory(context.Background(), &logical.BackendConfig{})
	if err != nil {
		t.Fatalf("factory returned error: %v", err)
	}
	if b == nil {
		t.Fatal("backend must not be nil")
	}
}

func TestConfigDefaults(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	req := &logical.Request{
		Operation: logical.ReadOperation,
		Path:      configPath,
		Storage:   &logical.InmemStorage{},
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if resp == nil {
		t.Fatal("response must not be nil")
	}
	if got := resp.Data["restore_guard"]; got != true {
		t.Fatalf("restore_guard default = %v, want true", got)
	}
	if got := resp.Data["restore_guard_acknowledged_time"]; got != "" {
		t.Fatalf("restore_guard_acknowledged_time = %v, want empty", got)
	}
	if got := resp.Data["storage_schema_version"]; got != currentStorageSchema {
		t.Fatalf("storage_schema_version = %v, want %d", got, currentStorageSchema)
	}
	if got := resp.Data["storage_schema_min_compatible_version"]; got != minSupportedStorageSchema {
		t.Fatalf("storage_schema_min_compatible_version = %v, want %d", got, minSupportedStorageSchema)
	}
	if got, ok := resp.Data["plugin_instance_id"].(string); !ok || !strings.HasPrefix(got, "inst-") {
		t.Fatalf("plugin_instance_id = %v, want inst-*", resp.Data["plugin_instance_id"])
	}
	if got, ok := resp.Data["restore_epoch"].(string); !ok || !strings.HasPrefix(got, "epoch-") {
		t.Fatalf("restore_epoch = %v, want epoch-*", resp.Data["restore_epoch"])
	}
}

func TestConfigWriteMergesDefaultsAndValidatesQueueCapacity(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"queue_capacity": 12,
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected config write error: %v", writeResp.Error())
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, configPath, nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["queue_capacity"]; got != 12 {
		t.Fatalf("queue_capacity = %v, want 12", got)
	}
	if got := readResp.Data["restore_guard"]; got != true {
		t.Fatalf("restore_guard = %v, want true", got)
	}

	negativeResp := handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"queue_capacity": -1,
	})
	if negativeResp == nil || !negativeResp.IsError() {
		t.Fatalf("negative queue_capacity response = %#v, want error", negativeResp)
	}
}

func TestConfigRestoreGuardAcknowledge(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	initialResp := handleRequest(t, b, storage, logical.ReadOperation, configPath, nil)
	assertNoErrorResponse(t, initialResp)
	initialEpoch := initialResp.Data["restore_epoch"].(string)

	ackResp := handleRequest(t, b, storage, logical.UpdateOperation, "config/restore-guard/acknowledge", nil)
	assertNoErrorResponse(t, ackResp)
	if got := ackResp.Data["restore_guard"]; got != false {
		t.Fatalf("restore_guard = %v, want false", got)
	}
	if got := ackResp.Data["restore_guard_acknowledged_time"]; got == "" {
		t.Fatal("restore_guard_acknowledged_time must be set")
	}
	ackEpoch := ackResp.Data["restore_epoch"].(string)
	if ackEpoch == "" || ackEpoch == initialEpoch {
		t.Fatalf("restore_epoch after acknowledgement = %q, want new epoch distinct from %q", ackEpoch, initialEpoch)
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, configPath, nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["restore_guard"]; got != false {
		t.Fatalf("read restore_guard = %v, want false", got)
	}
	if got := readResp.Data["restore_guard_acknowledged_time"]; got != ackResp.Data["restore_guard_acknowledged_time"] {
		t.Fatalf("read acknowledged time = %v, want %v", got, ackResp.Data["restore_guard_acknowledged_time"])
	}
	if got := readResp.Data["restore_epoch"]; got != ackEpoch {
		t.Fatalf("read restore_epoch = %v, want %s", got, ackEpoch)
	}

	repeatedAckResp := handleRequest(t, b, storage, logical.UpdateOperation, "config/restore-guard/acknowledge", nil)
	assertNoErrorResponse(t, repeatedAckResp)
	if got := repeatedAckResp.Data["restore_epoch"]; got != ackEpoch {
		t.Fatalf("repeated acknowledgement restore_epoch = %v, want unchanged %s", got, ackEpoch)
	}

	rearmResp := handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"restore_guard": true,
	})
	if rearmResp != nil && rearmResp.IsError() {
		t.Fatalf("unexpected restore guard rearm error: %v", rearmResp.Error())
	}
	readResp = handleRequest(t, b, storage, logical.ReadOperation, configPath, nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["restore_guard"]; got != true {
		t.Fatalf("rearmed restore_guard = %v, want true", got)
	}
	if got := readResp.Data["restore_guard_acknowledged_time"]; got != "" {
		t.Fatalf("rearmed acknowledged time = %v, want empty", got)
	}
}

func TestSourceEnableMarksPathSyncable(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	metadataResp := handleRequest(t, b, storage, logical.UpdateOperation, "metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"owner": "team-a",
		},
	})
	assertNoErrorResponse(t, metadataResp)

	enableResp := handleRequest(t, b, storage, logical.UpdateOperation, "sources/app/db/enable", nil)
	assertNoErrorResponse(t, enableResp)
	if got := enableResp.Data["path"]; got != "app/db" {
		t.Fatalf("source path = %v, want app/db", got)
	}
	if got := enableResp.Data["syncable"]; got != true {
		t.Fatalf("syncable = %v, want true", got)
	}
	if got := enableResp.Data["changed"]; got != true {
		t.Fatalf("changed = %v, want true", got)
	}
	metadata := enableResp.Data["metadata"].(map[string]interface{})
	customMetadata := metadata["custom_metadata"].(map[string]string)
	if got := customMetadata["owner"]; got != "team-a" {
		t.Fatalf("custom_metadata.owner = %v, want team-a", got)
	}
	if got := customMetadata[sourceMetadataKeySyncable]; got != sourceMetadataValueTrue {
		t.Fatalf("custom_metadata.syncable = %v, want true", got)
	}

	secondResp := handleRequest(t, b, storage, logical.UpdateOperation, "sources/app/db/enable", nil)
	assertNoErrorResponse(t, secondResp)
	if got := secondResp.Data["changed"]; got != false {
		t.Fatalf("second changed = %v, want false", got)
	}
}

func TestDestinationValidateSupportsRead(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}
	createFakeDestination(t, b, storage, "default")

	resp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/default/validate", nil)
	assertNoErrorResponse(t, resp)
	if got := resp.Data["valid"]; got != true {
		t.Fatalf("valid = %v, want true", got)
	}
}

func TestAssociationCreateUsesSafeDefaults(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}
	createFakeDestination(t, b, storage, "default")
	writeAppDBSecret(t, b, storage, "secret")

	enableResp := handleRequest(t, b, storage, logical.UpdateOperation, "sources/app/db/enable", nil)
	assertNoErrorResponse(t, enableResp)

	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
	})
	assertNoErrorResponse(t, resp)
	if got := resp.Data["resolved_name"]; got != "app/db" {
		t.Fatalf("resolved_name = %v, want app/db", got)
	}
	if got := resp.Data["granularity"]; got != syncGranularitySecretPath {
		t.Fatalf("granularity = %v, want %s", got, syncGranularitySecretPath)
	}
	if got := resp.Data["format"]; got != defaultAssociationFormat {
		t.Fatalf("format = %v, want %s", got, defaultAssociationFormat)
	}
	if got := resp.Data["delete_mode"]; got != deleteModeRetain {
		t.Fatalf("delete_mode = %v, want %s", got, deleteModeRetain)
	}
	if got := resp.Data["enabled"]; got != true {
		t.Fatalf("enabled = %v, want true", got)
	}
	operationIDs := operationIDsFromResponse(t, resp)
	if len(operationIDs) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", operationIDs)
	}
}

func TestIncompatibleStorageSchemaFailsClosed(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}
	entry, err := logical.StorageEntryJSON(storageSchemaKey, storageSchemaRecord{
		Version:              currentStorageSchema + 1,
		MinCompatibleVersion: currentStorageSchema + 1,
		CreatedTime:          "2026-07-01T00:00:00Z",
		UpdatedTime:          "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("encode schema: %v", err)
	}
	if err := storage.Put(context.Background(), entry); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "data/app/db",
		Storage:   storage,
		Data: map[string]interface{}{
			"data": map[string]interface{}{"password": "secret"},
		},
	})
	if err != nil {
		t.Fatalf("write with incompatible schema returned backend error: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("write with incompatible schema response = %#v, want logical error", resp)
	}
	if !strings.Contains(resp.Error().Error(), "incompatible storage schema") {
		t.Fatalf("schema error = %q, want incompatible storage schema", resp.Error().Error())
	}
	entry, err = storage.Get(context.Background(), metadataStorageKey("app/db"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if entry != nil {
		t.Fatal("source metadata must not be written when schema is incompatible")
	}
}

func TestDestinationLifecycle(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	createFakeDestination(t, b, storage, "primary")
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/primary", nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["name"]; got != "primary" {
		t.Fatalf("destination name = %v, want primary", got)
	}
	if _, ok := readResp.Data["sensitive_config"]; !ok {
		t.Fatal("destination read must include redacted sensitive_config")
	}

	listResp := handleRequest(t, b, storage, logical.ListOperation, "destinations/fake", nil)
	assertNoErrorResponse(t, listResp)
	keys := listResp.Data["keys"].([]string)
	if len(keys) != 1 || keys[0] != "primary" {
		t.Fatalf("destination keys = %v, want [primary]", keys)
	}

	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "destinations/fake/primary", nil)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected destination delete error: %v", deleteResp.Error())
	}
	readDeletedResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/primary", nil)
	if readDeletedResp != nil {
		t.Fatalf("deleted destination response = %#v, want nil", readDeletedResp)
	}
}

func TestDestinationPolicyPrefixesNormalizeAndRead(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "team/api, app ,team/api",
			destinationAllowedResolvedNamePrefixesField: "prod/app/, team/api",
		},
	)
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/restricted", nil)
	assertNoErrorResponse(t, readResp)
	sourcePrefixes, ok := readResp.Data["allowed_source_path_prefixes"].([]string)
	if !ok {
		t.Fatalf("allowed_source_path_prefixes = %T, want []string", readResp.Data["allowed_source_path_prefixes"])
	}
	assertStringSlice(t, sourcePrefixes, []string{"app", "team/api"})
	namePrefixes, ok := readResp.Data["allowed_resolved_name_prefixes"].([]string)
	if !ok {
		t.Fatalf("allowed_resolved_name_prefixes = %T, want []string", readResp.Data["allowed_resolved_name_prefixes"])
	}
	assertStringSlice(t, namePrefixes, []string{"prod/app/", "team/api"})
}

func TestAWSDestinationConfigLifecycle(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/aws-sm/prod", map[string]interface{}{
		"description":                             "aws production",
		awssecretsmanager.ConfigKeyRegion:         "eu-central-1",
		awssecretsmanager.ConfigKeyEndpointURL:    "http://localhost:4566",
		awssecretsmanager.ConfigKeyEndpointPolicy: awssecretsmanager.EndpointPolicyLocal,
		awssecretsmanager.ConfigKeyAuthMode:       awssecretsmanager.AuthModeAssumeRole,
		awssecretsmanager.ConfigKeyRoleARN:        "arn:aws:iam::123456789012:role/openbao-secret-sync",
		awssecretsmanager.ConfigKeyExternalID:     "tenant-1",
		awssecretsmanager.ConfigKeySessionName:    "openbao-sync",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	assertStoredAWSDestinationConfig(t, storage)
	assertReadAWSDestinationConfig(t, b, storage)

	validateResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/aws-sm/prod/validate", nil)
	assertNoErrorResponse(t, validateResp)
	if got := validateResp.Data["valid"]; got != true {
		t.Fatalf("aws validation valid = %v, want true", got)
	}
}

func TestKubernetesDestinationConfigLifecycle(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/k8s/prod", map[string]interface{}{
		"description":                          "kubernetes production",
		kubernetessecrets.ConfigKeyNamespace:   "apps",
		kubernetessecrets.ConfigKeyAuthMode:    kubernetessecrets.AuthModeInCluster,
		kubernetessecrets.ConfigKeyKubeContext: "",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/k8s/prod", nil)
	assertNoErrorResponse(t, readResp)
	config := readResp.Data["config"].(map[string]interface{})
	if got := config[kubernetessecrets.ConfigKeyNamespace]; got != "apps" {
		t.Fatalf("k8s destination namespace = %v, want apps", got)
	}
	if got := config[kubernetessecrets.ConfigKeyAuthMode]; got != kubernetessecrets.AuthModeInCluster {
		t.Fatalf("k8s auth_mode = %v, want %s", got, kubernetessecrets.AuthModeInCluster)
	}
	sensitiveConfig := readResp.Data["sensitive_config"].(map[string]interface{})
	if got := sensitiveConfig["configured"]; got != false {
		t.Fatalf("k8s sensitive_config configured = %v, want false", got)
	}

	validateResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/k8s/prod/validate", nil)
	assertNoErrorResponse(t, validateResp)
	if got := validateResp.Data["valid"]; got != true {
		t.Fatalf("k8s validation valid = %v, want true", got)
	}
	capabilities := validateResp.Data["capabilities"].(map[string]interface{})
	if got := capabilities["supports_value_readback"]; got != true {
		t.Fatalf("k8s supports_value_readback = %v, want true", got)
	}
}

func TestGitLabDestinationConfigLifecycle(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/gitlab/prod", map[string]interface{}{
		"description":                     "gitlab production",
		gitlab.ConfigKeyBaseURL:           "https://gitlab.example.com",
		gitlab.ConfigKeyProjectID:         "platform/app",
		gitlab.ConfigKeyEnvironmentScope:  "production",
		gitlab.ConfigKeyProtected:         "true",
		gitlab.ConfigKeyMasked:            "false",
		gitlab.ConfigKeyHidden:            "false",
		gitlab.ConfigKeyVariableRaw:       "true",
		gitlab.ConfigKeyVariableType:      gitlab.VariableTypeEnvVar,
		gitlab.ConfigKeyAllowInsecureHTTP: fmt.Sprint(true),
		gitlab.ConfigKeyToken:             "glpat-secret",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	storedDestination, err := getDestination(context.Background(), storage, gitlab.ProviderType, "prod")
	if err != nil {
		t.Fatalf("read stored gitlab destination: %v", err)
	}
	if _, ok := storedDestination.Config[gitlab.ConfigKeyToken]; ok {
		t.Fatal("gitlab token must not be stored in destination config")
	}
	if got := storedDestination.Config[gitlab.ConfigKeyProjectID]; got != "platform/app" {
		t.Fatalf("gitlab project_id = %v, want platform/app", got)
	}
	assertStringMapValue(t, storedDestination.Config, gitlab.ConfigKeyAllowInsecureHTTP, fmt.Sprint(true))
	storedSensitiveConfig, err := getDestinationSensitiveConfig(
		context.Background(),
		storage,
		gitlab.ProviderType,
		"prod",
	)
	if err != nil {
		t.Fatalf("read gitlab sensitive config: %v", err)
	}
	if got := storedSensitiveConfig.Config[gitlab.ConfigKeyToken]; got != "glpat-secret" {
		t.Fatalf("stored gitlab token = %v, want glpat-secret", got)
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/gitlab/prod", nil)
	assertNoErrorResponse(t, readResp)
	config := readResp.Data["config"].(map[string]interface{})
	if _, ok := config[gitlab.ConfigKeyToken]; ok {
		t.Fatal("gitlab token must not be returned in config")
	}
	assertInterfaceMapValue(t, config, gitlab.ConfigKeyAllowInsecureHTTP, fmt.Sprint(true))
	sensitiveConfig := readResp.Data["sensitive_config"].(map[string]interface{})
	if got := sensitiveConfig["configured"]; got != true {
		t.Fatalf("gitlab sensitive_config configured = %v, want true", got)
	}
	keys := sensitiveConfig["keys"].([]string)
	if len(keys) != 1 || keys[0] != gitlab.ConfigKeyToken {
		t.Fatalf("gitlab sensitive keys = %v, want [%s]", keys, gitlab.ConfigKeyToken)
	}

	validateResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/gitlab/prod/validate", nil)
	assertNoErrorResponse(t, validateResp)
	if got := validateResp.Data["valid"]; got != true {
		t.Fatalf("gitlab validation valid = %v, want true", got)
	}
	capabilities := validateResp.Data["capabilities"].(map[string]interface{})
	if got := capabilities["supports_secret_key"]; got != true {
		t.Fatalf("gitlab supports_secret_key = %v, want true", got)
	}
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

func TestDestinationSensitiveConfigDeletion(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/aws-sm/prod", map[string]interface{}{
		awssecretsmanager.ConfigKeyAuthMode:   awssecretsmanager.AuthModeAssumeRole,
		awssecretsmanager.ConfigKeyRoleARN:    "arn:aws:iam::123456789012:role/openbao-secret-sync",
		awssecretsmanager.ConfigKeyExternalID: "tenant-1",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	clearResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/aws-sm/prod", map[string]interface{}{
		awssecretsmanager.ConfigKeyExternalID: "",
	})
	if clearResp != nil && clearResp.IsError() {
		t.Fatalf("unexpected destination update error: %v", clearResp.Error())
	}
	sensitiveConfig, err := getDestinationSensitiveConfig(
		context.Background(),
		storage,
		awssecretsmanager.ProviderType,
		"prod",
	)
	if err != nil {
		t.Fatalf("read stored sensitive config: %v", err)
	}
	if sensitiveConfig != nil {
		t.Fatalf("sensitive config after clear = %#v, want nil", sensitiveConfig)
	}
}

func TestDestinationWriteMigratesSensitiveKeysFromLegacyConfig(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}
	if err := putDestination(context.Background(), storage, destinationRecord{
		Type: awssecretsmanager.ProviderType,
		Name: "prod",
		Config: map[string]string{
			awssecretsmanager.ConfigKeyAuthMode:   awssecretsmanager.AuthModeAssumeRole,
			awssecretsmanager.ConfigKeyRoleARN:    "arn:aws:iam::123456789012:role/openbao-secret-sync",
			awssecretsmanager.ConfigKeyExternalID: "tenant-legacy",
		},
	}); err != nil {
		t.Fatalf("write legacy destination: %v", err)
	}

	updateResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/aws-sm/prod", map[string]interface{}{
		"description": "migrated",
	})
	if updateResp != nil && updateResp.IsError() {
		t.Fatalf("unexpected destination update error: %v", updateResp.Error())
	}
	storedDestination, err := getDestination(context.Background(), storage, awssecretsmanager.ProviderType, "prod")
	if err != nil {
		t.Fatalf("read stored destination: %v", err)
	}
	if _, ok := storedDestination.Config[awssecretsmanager.ConfigKeyExternalID]; ok {
		t.Fatal("legacy external_id must be removed from non-sensitive destination config")
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
	if got := storedSensitiveConfig.Config[awssecretsmanager.ConfigKeyExternalID]; got != "tenant-legacy" {
		t.Fatalf("migrated external_id = %q, want tenant-legacy", got)
	}
}

func TestDestinationConfigResponseFiltersSensitiveKeys(t *testing.T) {
	response := destinationConfigResponse(map[string]string{
		awssecretsmanager.ConfigKeyRegion:          "eu-central-1",
		awssecretsmanager.ConfigKeyExternalID:      "tenant-1",
		awssecretsmanager.ConfigKeySecretAccessKey: "secret",
	})
	if got := response[awssecretsmanager.ConfigKeyRegion]; got != "eu-central-1" {
		t.Fatalf("region = %v, want eu-central-1", got)
	}
	if _, ok := response[awssecretsmanager.ConfigKeyExternalID]; ok {
		t.Fatal("response must not include external_id")
	}
	if _, ok := response[awssecretsmanager.ConfigKeySecretAccessKey]; ok {
		t.Fatal("response must not include secret_access_key")
	}
}

func TestDestinationValidateAndHealth(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	createFakeDestination(t, b, storage, "primary")
	createFakeDestination(t, b, storage, "invalid")
	createFakeDestination(t, b, storage, "unhealthy")

	validateResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/fake/primary/validate", nil)
	assertNoErrorResponse(t, validateResp)
	if got := validateResp.Data["valid"]; got != true {
		t.Fatalf("valid = %v, want true", got)
	}
	if _, ok := validateResp.Data["capabilities"]; !ok {
		t.Fatal("validate response must include capabilities")
	}

	invalidResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/fake/invalid/validate", nil)
	assertNoErrorResponse(t, invalidResp)
	if got := invalidResp.Data["valid"]; got != false {
		t.Fatalf("invalid valid = %v, want false", got)
	}
	if got := invalidResp.Data["error_class"]; got != string(providers.ErrorClassValidation) {
		t.Fatalf("invalid error_class = %v, want %s", got, providers.ErrorClassValidation)
	}

	healthResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/primary/health", nil)
	assertNoErrorResponse(t, healthResp)
	if got := healthResp.Data["healthy"]; got != true {
		t.Fatalf("healthy = %v, want true", got)
	}

	unhealthyResp := handleRequest(t, b, storage, logical.ReadOperation, "destinations/fake/unhealthy/health", nil)
	assertNoErrorResponse(t, unhealthyResp)
	if got := unhealthyResp.Data["healthy"]; got != false {
		t.Fatalf("unhealthy healthy = %v, want false", got)
	}
	if got := unhealthyResp.Data["error_class"]; got != string(providers.ErrorClassUnavailable) {
		t.Fatalf("unhealthy error_class = %v, want %s", got, providers.ErrorClassUnavailable)
	}
}

func TestDataWriteReadAndQueueStatus(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
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

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if got := payload["username"]; got != "app" {
		t.Fatalf("username = %v, want app", got)
	}
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("read version = %v, want 1", got)
	}

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 0 {
		t.Fatalf("pending queue count = %v, want 0", got)
	}

	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	if got := statusResp.Data["state"]; got != string(domain.SyncStateNoAssociation) {
		t.Fatalf("status state = %v, want %s", got, domain.SyncStateNoAssociation)
	}
	if got := statusResp.Data["version"]; got != 1 {
		t.Fatalf("status version = %v, want 1", got)
	}
}

func TestMetadataReadListAndSoftDelete(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/api", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "api",
		},
	})
	assertNoErrorResponse(t, resp)

	listResp := handleRequest(t, b, storage, logical.ListOperation, "metadata/app", nil)
	assertNoErrorResponse(t, listResp)
	keys := listResp.Data["keys"].([]string)
	if !hasKey(keys, "db") || !hasKey(keys, "api") {
		t.Fatalf("metadata keys = %v, want db and api", keys)
	}

	metadataResp := handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	if got := metadataResp.Data["current_version"]; got != 1 {
		t.Fatalf("current version = %v, want 1", got)
	}

	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected data delete error: %v", deleteResp.Error())
	}
	readDeletedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	if readDeletedResp != nil {
		t.Fatalf("soft-deleted data response = %#v, want nil", readDeletedResp)
	}
	metadataResp = handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if versions["1"].DeletionTime == "" {
		t.Fatal("metadata version deletion_time must be set after soft delete")
	}
}

func TestUndeleteAndDestroyVersions(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected data delete error: %v", deleteResp.Error())
	}

	undeleteResp := handleRequest(t, b, storage, logical.UpdateOperation, "undelete/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if undeleteResp != nil && undeleteResp.IsError() {
		t.Fatalf("unexpected undelete error: %v", undeleteResp.Error())
	}
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readResp)
	payload := readResp.Data["data"].(secretPayload)
	if got := payload["password"]; got != "initial" {
		t.Fatalf("password = %v, want initial", got)
	}

	destroyResp := handleRequest(t, b, storage, logical.UpdateOperation, "destroy/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if destroyResp != nil && destroyResp.IsError() {
		t.Fatalf("unexpected destroy error: %v", destroyResp.Error())
	}
	readDestroyedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	if readDestroyedResp != nil {
		t.Fatalf("destroyed data response = %#v, want nil", readDestroyedResp)
	}
	metadataResp := handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if !versions["1"].Destroyed {
		t.Fatal("metadata version destroyed flag must be set after destroy")
	}

	undeleteDestroyedResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"undelete/app/db",
		map[string]interface{}{"versions": []int{1}},
	)
	if undeleteDestroyedResp != nil && undeleteDestroyedResp.IsError() {
		t.Fatalf("unexpected undelete destroyed error: %v", undeleteDestroyedResp.Error())
	}
	readStillDestroyedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	if readStillDestroyedResp != nil {
		t.Fatalf("undeleted destroyed data response = %#v, want nil", readStillDestroyedResp)
	}
}

func TestDeleteVersionsSoftDeletesSelectedVersions(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	secondResp := writeAppDBSecret(t, b, storage, "rotated")
	secondMetadata := secondResp.Data["metadata"].(map[string]interface{})
	if got := secondMetadata["version"]; got != 2 {
		t.Fatalf("second write version = %v, want 2", got)
	}

	deleteResp := handleRequest(t, b, storage, logical.UpdateOperation, "delete/app/db", map[string]interface{}{
		"versions": []int{1},
	})
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected version delete error: %v", deleteResp.Error())
	}
	readDeletedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", map[string]interface{}{
		"version": 1,
	})
	if readDeletedResp != nil {
		t.Fatalf("deleted version response = %#v, want nil", readDeletedResp)
	}
	readLatestResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readLatestResp)
	readMetadata := readLatestResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 2 {
		t.Fatalf("latest version = %v, want 2", got)
	}

	metadataResp := handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if versions["1"].DeletionTime == "" {
		t.Fatal("metadata version deletion_time must be set after version delete")
	}
}

func TestDataDeleteRetainModeCancelsQueuedUpsert(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	upsertOperationID := operationIDsFromResponse(t, associationResp)[0]

	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	assertNoErrorResponse(t, deleteResp)
	metadata := deleteResp.Data["metadata"].(map[string]interface{})
	assertOperationIDs(t, metadata, 0)
	assertOutboxOperation(t, storage, upsertOperationID, 1, outboxStateCanceled)
	assertQueueCount(t, b, storage, "pending", 0)
	assertQueueCount(t, b, storage, "canceled", 1)

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	if readResp != nil {
		t.Fatalf("deleted source response = %#v, want nil", readResp)
	}
}

func TestDataDeleteDeleteModeQueuesRemoteDelete(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createFakeAssociationWithDeleteMode(t, b, storage, deleteModeDelete)
	runPeriodicAllowed(t, b, storage, "periodic upsert")

	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	assertNoErrorResponse(t, deleteResp)
	deleteOperationID := requireSingleOperationID(
		t,
		operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{})),
		"delete",
	)
	deleteOperation := assertOutboxOperation(t, storage, deleteOperationID, 1, outboxStatePending)
	if got := deleteOperation.Type; got != outbox.OperationTypeDelete {
		t.Fatalf("delete operation type = %s, want %s", got, outbox.OperationTypeDelete)
	}

	runPeriodicAllowed(t, b, storage, "periodic delete")
	deleteOperation = assertOutboxOperation(t, storage, deleteOperationID, 1, outboxStateSucceeded)
	if got := deleteOperation.Type; got != outbox.OperationTypeDelete {
		t.Fatalf("succeeded delete operation type = %s, want %s", got, outbox.OperationTypeDelete)
	}
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	objects := statusResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 || objects[0]["state"] != string(domain.SyncStateRemoteMissing) {
		t.Fatalf("status objects = %#v, want remote missing object", objects)
	}
	if got := objects[0]["last_operation_id"]; got != deleteOperationID {
		t.Fatalf("delete last operation ID = %v, want %s", got, deleteOperationID)
	}
	if got := objects[0]["remote_version"]; got != "deleted" {
		t.Fatalf("delete remote version = %v, want deleted", got)
	}
}

func TestMetadataDeleteRequiresAssociationRemoval(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	associationID := associationIDFromResponse(t, associationResp)

	blockedResp := handleRequest(t, b, storage, logical.DeleteOperation, "metadata/app/db", nil)
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("metadata delete with association response = %#v, want error", blockedResp)
	}

	deleteAssociationResp := handleRequest(
		t,
		b,
		storage,
		logical.DeleteOperation,
		"associations/app/db/"+associationID,
		nil,
	)
	if deleteAssociationResp != nil && deleteAssociationResp.IsError() {
		t.Fatalf("unexpected association delete error: %v", deleteAssociationResp.Error())
	}

	deleteMetadataResp := handleRequest(t, b, storage, logical.DeleteOperation, "metadata/app/db", nil)
	if deleteMetadataResp != nil && deleteMetadataResp.IsError() {
		t.Fatalf("unexpected metadata delete error: %v", deleteMetadataResp.Error())
	}
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	if readResp != nil {
		t.Fatalf("deleted metadata response = %#v, want nil", readResp)
	}
}

func TestMetadataWriteEnforcesCASRequiredAndCustomMetadata(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	metadataResp := handleRequest(t, b, storage, logical.UpdateOperation, "metadata/app/db", map[string]interface{}{
		"cas_required": true,
		"custom_metadata": map[string]interface{}{
			sourceMetadataKeySyncable: sourceMetadataValueTrue,
			"owner":                   "platform",
		},
	})
	assertNoErrorResponse(t, metadataResp)
	if got := metadataResp.Data["cas_required"]; got != true {
		t.Fatalf("cas_required = %v, want true", got)
	}
	customMetadata := metadataResp.Data["custom_metadata"].(map[string]string)
	if got := customMetadata[sourceMetadataKeySyncable]; got != sourceMetadataValueTrue {
		t.Fatalf("custom_metadata.syncable = %v, want true", got)
	}

	blockedResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
	})
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("write without CAS response = %#v, want error", blockedResp)
	}

	allowedResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "rotated",
		},
		"options": map[string]interface{}{
			"cas": 1,
		},
	})
	assertNoErrorResponse(t, allowedResp)
	allowedMetadata := allowedResp.Data["metadata"].(map[string]interface{})
	if got := allowedMetadata["version"]; got != 2 {
		t.Fatalf("allowed write version = %v, want 2", got)
	}
}

func TestMetadataMaxVersionsPrunesOldVersions(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	metadataResp := handleRequest(t, b, storage, logical.UpdateOperation, "metadata/app/db", map[string]interface{}{
		"max_versions": 2,
	})
	assertNoErrorResponse(t, metadataResp)

	writeAppDBSecret(t, b, storage, "one")
	writeAppDBSecret(t, b, storage, "two")
	writeAppDBSecret(t, b, storage, "three")

	readPrunedResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", map[string]interface{}{
		"version": 1,
	})
	if readPrunedResp != nil {
		t.Fatalf("pruned version response = %#v, want nil", readPrunedResp)
	}
	metadataResp = handleRequest(t, b, storage, logical.ReadOperation, "metadata/app/db", nil)
	assertNoErrorResponse(t, metadataResp)
	if got := metadataResp.Data["current_version"]; got != 3 {
		t.Fatalf("current_version = %v, want 3", got)
	}
	if got := metadataResp.Data["oldest_version"]; got != 2 {
		t.Fatalf("oldest_version = %v, want 2", got)
	}
	versions := metadataResp.Data["versions"].(map[string]versionMetadata)
	if _, ok := versions["1"]; ok {
		t.Fatalf("metadata versions = %v, want version 1 pruned", versions)
	}
}

func TestAssociationCreateQueuesCurrentVersion(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationIDs := operationIDsFromResponse(t, associationResp)
	if len(operationIDs) != 1 {
		t.Fatalf("association operation IDs = %v, want one operation", operationIDs)
	}

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 1 {
		t.Fatalf("pending queue count = %v, want 1", got)
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "associations/app/db", nil)
	assertNoErrorResponse(t, readResp)
	associations := readResp.Data["associations"].([]map[string]interface{})
	if len(associations) != 1 {
		t.Fatalf("associations length = %d, want 1", len(associations))
	}
	if got := associations[0]["resolved_name"]; got != "prod/app/db" {
		t.Fatalf("resolved name = %v, want prod/app/db", got)
	}
}

func TestAssociationSecretKeyQueuesAndSyncsPerSourceKey(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	createFakeDestination(t, b, storage, "default")
	markAppDBSyncable(t, b, storage)

	planResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db/plan", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"name_template":    "prod/{{ path }}/{{ key }}",
		"granularity":      syncGranularitySecretKey,
		"format":           defaultAssociationFormat,
	})
	assertNoErrorResponse(t, planResp)
	if got := planResp.Data["action"]; got != providers.PlanActionCreate {
		t.Fatalf("secret-key plan action = %v, want %s", got, providers.PlanActionCreate)
	}
	if got := planResp.Data["granularity"]; got != syncGranularitySecretKey {
		t.Fatalf("secret-key plan granularity = %v, want %s", got, syncGranularitySecretKey)
	}
	planObjects := objectsByIDFromRaw(t, planResp.Data["objects"])
	assertPlanObject(t, planObjects, "password", "prod/app/db/password")
	assertPlanObject(t, planObjects, "username", "prod/app/db/username")
	if strings.Contains(fmt.Sprint(planResp.Data), "initial") || strings.Contains(fmt.Sprint(planResp.Data), "appuser") {
		t.Fatalf("secret-key plan response contains secret value: %#v", planResp.Data)
	}

	associationResp := createFakeSecretKeyAssociation(t, b, storage, deleteModeRetain)
	associationID := associationIDFromResponse(t, associationResp)
	operationIDs := operationIDsFromResponse(t, associationResp)
	if len(operationIDs) != 2 {
		t.Fatalf("secret-key association operation IDs = %v, want two operations", operationIDs)
	}
	assertOperationObjectIDs(t, storage, operationIDs, 1, outboxStatePending, []string{"password", "username"})

	runPeriodicAllowed(t, b, storage, "periodic")
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	if got := statusResp.Data["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("secret-key status state = %v, want %s", got, domain.SyncStateSynced)
	}
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	assertSecretKeySyncedStatusObject(
		t,
		statusObjects,
		"password",
		associationID,
		"prod/app/db/password",
		operationIDs[0],
	)
	assertSecretKeySyncedStatusObject(
		t,
		statusObjects,
		"username",
		associationID,
		"prod/app/db/username",
		operationIDs[1],
	)

	updateResp := writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "rotated",
		"username": "appuser",
	})
	updateMetadata := updateResp.Data["metadata"].(map[string]interface{})
	updateOperationIDs := operationIDsFromMetadata(t, updateMetadata)
	if len(updateOperationIDs) != 2 {
		t.Fatalf("secret-key update operation IDs = %v, want two operations", updateOperationIDs)
	}
	assertOperationObjectIDs(t, storage, updateOperationIDs, 2, outboxStatePending, []string{"password", "username"})
}

func TestAssociationSecretKeyRawFormat(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"PASSWORD": "initial",
	})
	createFakeDestination(t, b, storage, "default")
	markAppDBSyncable(t, b, storage)

	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db/plan", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"name_template":    "{{ key }}",
		"granularity":      syncGranularitySecretKey,
		"format":           rawAssociationFormat,
	})
	assertNoErrorResponse(t, resp)
	objects := objectsByIDFromRaw(t, resp.Data["objects"])
	object := objects["PASSWORD"]
	if got := object["payload_bytes"]; got != len("initial") {
		t.Fatalf("raw payload bytes = %v, want %d", got, len("initial"))
	}
	if got := object["payload_sha256"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("raw payload sha = %q, want sha256 prefix", got)
	}
}

func TestAssociationRawFormatRequiresSecretKey(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "initial",
	})
	createFakeDestination(t, b, storage, "default")
	markAppDBSyncable(t, b, storage)

	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncGranularitySecretPath,
		"format":           rawAssociationFormat,
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("raw secret-path association response = %#v, want error", resp)
	}
}

func TestAssociationGitLabSecretKeyRawFormat(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"APP_PASSWORD": "initial",
	})
	writeResp := handleRequest(t, b, storage, logical.UpdateOperation, "destinations/gitlab/prod", map[string]interface{}{
		gitlab.ConfigKeyProjectID:        "platform/app",
		gitlab.ConfigKeyEnvironmentScope: "production",
		gitlab.ConfigKeyToken:            "glpat-secret",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected gitlab destination write error: %v", writeResp.Error())
	}
	markAppDBSyncable(t, b, storage)

	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": gitlab.ProviderType,
		"destination_name": "prod",
		"name_template":    "{{ key }}",
		"granularity":      syncGranularitySecretKey,
		"format":           rawAssociationFormat,
		"delete_mode":      deleteModeRetain,
	})
	assertNoErrorResponse(t, resp)
	operationIDs := operationIDsFromResponse(t, resp)
	if len(operationIDs) != 1 {
		t.Fatalf("gitlab operation IDs = %v, want one operation", operationIDs)
	}
	operation := assertOutboxOperation(t, storage, operationIDs[0], 1, outboxStatePending)
	if got := operation.ObjectID; got != "APP_PASSWORD" {
		t.Fatalf("gitlab object ID = %s, want APP_PASSWORD", got)
	}
	association := resp.Data["association"].(map[string]interface{})
	if got := association["destination_ref"]; got != "gitlab/prod" {
		t.Fatalf("gitlab destination_ref = %v, want gitlab/prod", got)
	}
	if got := association["format"]; got != rawAssociationFormat {
		t.Fatalf("gitlab association format = %v, want %s", got, rawAssociationFormat)
	}
}

func TestAssociationSecretKeyDeleteModeQueuesPerSourceKeyDeletes(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	createFakeDestination(t, b, storage, "default")
	createFakeSecretKeyAssociation(t, b, storage, deleteModeDelete)
	runPeriodicAllowed(t, b, storage, "periodic upsert")

	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	assertNoErrorResponse(t, deleteResp)
	deleteOperationIDs := operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{}))
	if len(deleteOperationIDs) != 2 {
		t.Fatalf("secret-key delete operation IDs = %v, want two operations", deleteOperationIDs)
	}
	for _, operationID := range deleteOperationIDs {
		operation := assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)
		if got := operation.Type; got != outbox.OperationTypeDelete {
			t.Fatalf("secret-key delete operation type = %s, want %s", got, outbox.OperationTypeDelete)
		}
	}
	assertOperationObjectIDs(t, storage, deleteOperationIDs, 1, outboxStatePending, []string{"password", "username"})

	runPeriodicAllowed(t, b, storage, "periodic delete")
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := statusObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateRemoteMissing) {
			t.Fatalf("%s status state = %v, want %s", objectID, got, domain.SyncStateRemoteMissing)
		}
		if got := object["remote_version"]; got != "deleted" {
			t.Fatalf("%s remote version = %v, want deleted", objectID, got)
		}
	}
}

func TestAssociationSecretKeyValidation(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "initial",
	})
	createFakeDestination(t, b, storage, "default")
	markAppDBSyncable(t, b, storage)

	resolvedNameResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db",
		map[string]interface{}{
			"destination_type": providerTypeFake,
			"destination_name": "default",
			"resolved_name":    "prod/app/db/password",
			"name_template":    "prod/{{ path }}/{{ key }}",
			"granularity":      syncGranularitySecretKey,
			"format":           defaultAssociationFormat,
		},
	)
	if resolvedNameResp == nil || !resolvedNameResp.IsError() {
		t.Fatalf("secret-key resolved_name response = %#v, want error", resolvedNameResp)
	}

	missingKeyResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"name_template":    "prod/static",
		"granularity":      syncGranularitySecretKey,
		"format":           defaultAssociationFormat,
	})
	if missingKeyResp == nil || !missingKeyResp.IsError() {
		t.Fatalf("secret-key template without key response = %#v, want error", missingKeyResp)
	}

	kubernetesResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"destinations/k8s/prod",
		map[string]interface{}{
			"description":                          "kubernetes production",
			kubernetessecrets.ConfigKeyNamespace:   "apps",
			kubernetessecrets.ConfigKeyAuthMode:    kubernetessecrets.AuthModeInCluster,
			kubernetessecrets.ConfigKeyKubeContext: "",
		},
	)
	if kubernetesResp != nil && kubernetesResp.IsError() {
		t.Fatalf("unexpected kubernetes destination write error: %v", kubernetesResp.Error())
	}
	unsupportedResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": kubernetessecrets.ProviderType,
		"destination_name": "prod",
		"name_template":    "prod/{{ path }}/{{ key }}",
		"granularity":      syncGranularitySecretKey,
		"format":           defaultAssociationFormat,
	})
	if unsupportedResp == nil || !unsupportedResp.IsError() {
		t.Fatalf("secret-key unsupported provider response = %#v, want error", unsupportedResp)
	}
}

func TestAssociationSecretKeyRejectsUnsupportedSourceKey(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "initial",
	})
	createFakeDestination(t, b, storage, "default")
	createFakeSecretKeyAssociation(t, b, storage, deleteModeRetain)

	blockedResp := writeAppDBSecretDataNoAssert(t, b, storage, map[string]interface{}{
		"bad/key":  "blocked",
		"password": "rotated",
	})
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("secret-key unsupported key write response = %#v, want error", blockedResp)
	}
	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readResp)
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("blocked secret-key write committed version = %v, want 1", got)
	}
}

func TestAssociationSecretKeyReconcileAppliesPerSourceKeyStatus(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "secret-canary",
		"username": "appuser",
	})
	createFakeDestination(t, b, storage, "default")
	associationResp := createFakeSecretKeyAssociation(t, b, storage, deleteModeRetain)
	associationID := associationIDFromResponse(t, associationResp)

	resp := handleRequest(t, b, storage, logical.UpdateOperation, "reconcile/app/db", nil)
	assertNoErrorResponse(t, resp)
	if got := resp.Data["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("secret-key reconcile state = %v, want %s", got, domain.SyncStateSynced)
	}
	reconcileObjects := objectsByIDFromRaw(t, resp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := reconcileObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateSynced) {
			t.Fatalf("%s reconcile state = %v, want %s", objectID, got, domain.SyncStateSynced)
		}
		if got := object["association_id"]; got != associationID {
			t.Fatalf("%s reconcile association_id = %v, want %s", objectID, got, associationID)
		}
	}
	if strings.Contains(fmt.Sprint(resp.Data), "secret-canary") || strings.Contains(fmt.Sprint(resp.Data), "appuser") {
		t.Fatalf("secret-key reconcile response contains secret value: %#v", resp.Data)
	}

	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := statusObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateSynced) {
			t.Fatalf("%s status state = %v, want %s", objectID, got, domain.SyncStateSynced)
		}
	}
}

func TestAssociationSecretKeyDisableMarksPerSourceKeyStatus(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	createFakeDestination(t, b, storage, "default")
	associationResp := createFakeSecretKeyAssociation(t, b, storage, deleteModeRetain)
	associationID := associationIDFromResponse(t, associationResp)
	operationIDs := operationIDsFromResponse(t, associationResp)

	disableResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db/"+associationID+"/disable",
		nil,
	)
	assertNoErrorResponse(t, disableResp)
	assertAssociationEnabled(t, disableResp, false)
	assertStringSlice(t, stringSliceFromResponse(t, disableResp, "canceled_operation_ids"), operationIDs)

	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := statusObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateDisabled) {
			t.Fatalf("%s status state = %v, want %s", objectID, got, domain.SyncStateDisabled)
		}
	}
	if _, ok := statusObjects[syncObjectIDSecretPath]; ok {
		t.Fatalf("secret-key status must not include %s object: %#v", syncObjectIDSecretPath, statusObjects)
	}
}

func TestAssociationRequiresSyncableMetadata(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")

	blockedResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("association without syncable metadata response = %#v, want error", blockedResp)
	}

	markAppDBSyncable(t, b, storage)
	allowedResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
	assertNoErrorResponse(t, allowedResp)
}

func TestAssociationDestinationPolicyConstraints(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	markAppDBSyncable(t, b, storage)
	writeResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "team/",
			destinationAllowedResolvedNamePrefixesField: "prod/app/",
		},
	)
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	sourceBlockedResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db",
		map[string]interface{}{
			"destination_type": providerTypeFake,
			"destination_name": "restricted",
			"resolved_name":    "prod/app/db",
			"granularity":      syncObjectIDSecretPath,
			"format":           defaultAssociationFormat,
		},
	)
	if sourceBlockedResp == nil || !sourceBlockedResp.IsError() {
		t.Fatalf("source policy response = %#v, want error", sourceBlockedResp)
	}
	if !strings.Contains(sourceBlockedResp.Error().Error(), "does not allow source path") {
		t.Fatalf("source policy error = %q", sourceBlockedResp.Error().Error())
	}

	updateResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "app",
			destinationAllowedResolvedNamePrefixesField: "prod/app/",
		},
	)
	if updateResp != nil && updateResp.IsError() {
		t.Fatalf("unexpected destination update error: %v", updateResp.Error())
	}
	nameBlockedPlan := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db/plan",
		map[string]interface{}{
			"destination_type": providerTypeFake,
			"destination_name": "restricted",
			"resolved_name":    "prod/other/db",
			"granularity":      syncObjectIDSecretPath,
			"format":           defaultAssociationFormat,
		},
	)
	assertNoErrorResponse(t, nameBlockedPlan)
	if got := nameBlockedPlan.Data["source_eligible"]; got != true {
		t.Fatalf("name policy source_eligible = %v, want true", got)
	}
	if got := nameBlockedPlan.Data["action"]; got != providers.PlanActionBlocked {
		t.Fatalf("name policy action = %v, want %s", got, providers.PlanActionBlocked)
	}
	if got := nameBlockedPlan.Data["error_class"]; got != string(providers.ErrorClassValidation) {
		t.Fatalf("name policy error_class = %v, want %s", got, providers.ErrorClassValidation)
	}
	if !strings.Contains(nameBlockedPlan.Data["message"].(string), "does not allow resolved name") {
		t.Fatalf("name policy message = %q", nameBlockedPlan.Data["message"])
	}

	nameBlockedWrite := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db",
		map[string]interface{}{
			"destination_type": providerTypeFake,
			"destination_name": "restricted",
			"resolved_name":    "prod/other/db",
			"granularity":      syncObjectIDSecretPath,
			"format":           defaultAssociationFormat,
		},
	)
	if nameBlockedWrite == nil || !nameBlockedWrite.IsError() {
		t.Fatalf("name policy write response = %#v, want error", nameBlockedWrite)
	}

	allowedResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "restricted",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
	assertNoErrorResponse(t, allowedResp)
}

func TestDispatchHonorsTightenedDestinationPolicy(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	markAppDBSyncable(t, b, storage)
	writeResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "app",
			destinationAllowedResolvedNamePrefixesField: "prod/app/",
		},
	)
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}
	associationResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "restricted",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
	operationID := operationIDsFromResponse(t, associationResp)[0]

	tightenResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "app",
			destinationAllowedResolvedNamePrefixesField: "other/",
		},
	)
	if tightenResp != nil && tightenResp.IsError() {
		t.Fatalf("unexpected destination tighten error: %v", tightenResp.Error())
	}
	runPeriodicAllowed(t, b, storage, "periodic after destination policy tightened")
	assertOutboxOperation(t, storage, operationID, 1, outboxStateFailedTerminal)
	assertStatusObjectErrorClass(t, b, storage, providers.ErrorClassValidation)
	assertStatusObjectState(t, b, storage, domain.SyncStateValidationError)
}

func TestAssociationPlan(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")

	blockedResp := planDefaultFakeAssociation(t, b, storage, "prod/app/db")
	assertNoErrorResponse(t, blockedResp)
	if got := blockedResp.Data["action"]; got != providers.PlanActionBlocked {
		t.Fatalf("blocked action = %v, want %s", got, providers.PlanActionBlocked)
	}
	if got := blockedResp.Data["source_eligible"]; got != false {
		t.Fatalf("blocked source_eligible = %v, want false", got)
	}
	if got := blockedResp.Data["error_class"]; got != string(providers.ErrorClassValidation) {
		t.Fatalf("blocked error_class = %v, want %s", got, providers.ErrorClassValidation)
	}

	markAppDBSyncable(t, b, storage)
	createResp := planDefaultFakeAssociation(t, b, storage, "prod/app/db")
	assertNoErrorResponse(t, createResp)
	if got := createResp.Data["action"]; got != providers.PlanActionCreate {
		t.Fatalf("create action = %v, want %s", got, providers.PlanActionCreate)
	}
	if got := createResp.Data["source_eligible"]; got != true {
		t.Fatalf("create source_eligible = %v, want true", got)
	}
	if got := createResp.Data["payload_sha256"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("payload_sha256 = %q, want sha256 prefix", got)
	}
	if got := createResp.Data["payload_bytes"].(int); got <= 0 {
		t.Fatalf("payload_bytes = %d, want positive", got)
	}
	if strings.Contains(fmt.Sprint(createResp.Data), "initial") {
		t.Fatalf("plan response contains secret value: %#v", createResp.Data)
	}

	conflictResp := planDefaultFakeAssociation(t, b, storage, "prod/conflict/app/db")
	assertNoErrorResponse(t, conflictResp)
	if got := conflictResp.Data["action"]; got != providers.PlanActionConflict {
		t.Fatalf("conflict action = %v, want %s", got, providers.PlanActionConflict)
	}
	if got := conflictResp.Data["error_class"]; got != string(providers.ErrorClassCollision) {
		t.Fatalf("conflict error_class = %v, want %s", got, providers.ErrorClassCollision)
	}
}

func TestReconcilePlanDoesNotPersistStatus(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "secret-canary")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	associationID := associationIDFromResponse(t, associationResp)

	planResp := handleRequest(t, b, storage, logical.ReadOperation, "reconcile/app/db/plan", nil)
	assertNoErrorResponse(t, planResp)
	if got := planResp.Data["applied"]; got != false {
		t.Fatalf("applied = %v, want false", got)
	}
	if got := planResp.Data["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("reconcile plan state = %v, want %s", got, domain.SyncStateSynced)
	}
	objects := planResp.Data["objects"].([]map[string]interface{})
	if len(objects) != 1 {
		t.Fatalf("reconcile plan objects = %d, want 1", len(objects))
	}
	if got := objects[0]["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("reconcile object state = %v, want %s", got, domain.SyncStateSynced)
	}
	if strings.Contains(fmt.Sprint(planResp.Data), "secret-canary") {
		t.Fatalf("reconcile plan response contains secret value: %#v", planResp.Data)
	}
	status, err := getStatus(context.Background(), storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want nil after reconcile plan", status)
	}
}

func TestReconcileApplyMapsReadStateToStatus(t *testing.T) {
	testCases := []struct {
		name         string
		resolvedName string
		state        domain.SyncState
		errorClass   providers.ErrorClass
	}{
		{
			name:         "synced",
			resolvedName: "prod/app/db",
			state:        domain.SyncStateSynced,
		},
		{
			name:         "missing",
			resolvedName: "prod/missing/app/db",
			state:        domain.SyncStateRemoteMissing,
		},
		{
			name:         "ownership",
			resolvedName: "prod/ownership/app/db",
			state:        domain.SyncStateRemoteOwnershipLost,
			errorClass:   providers.ErrorClassOwnership,
		},
		{
			name:         "drift",
			resolvedName: "prod/drift/app/db",
			state:        domain.SyncStateDrifted,
			errorClass:   providers.ErrorClassDrift,
		},
		{
			name:         "authn",
			resolvedName: "prod/authn/app/db",
			state:        domain.SyncStateDestinationAuthError,
			errorClass:   providers.ErrorClassAuthn,
		},
		{
			name:         "rate-limit",
			resolvedName: "prod/rate-limit/app/db",
			state:        domain.SyncStateDestinationRateLimited,
			errorClass:   providers.ErrorClassRateLimit,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			b := Backend(&logical.BackendConfig{})
			storage := &logical.InmemStorage{}

			writeAppDBSecret(t, b, storage, "secret-canary")
			createFakeDestination(t, b, storage, "default")
			associationResp := createFakeAssociationWithResolvedName(t, b, storage, testCase.resolvedName)
			associationID := associationIDFromResponse(t, associationResp)

			resp := handleRequest(t, b, storage, logical.UpdateOperation, "reconcile/app/db", nil)
			assertNoErrorResponse(t, resp)
			if got := resp.Data["applied"]; got != true {
				t.Fatalf("applied = %v, want true", got)
			}
			objects := resp.Data["objects"].([]map[string]interface{})
			if len(objects) != 1 {
				t.Fatalf("reconcile objects = %d, want 1", len(objects))
			}
			if got := objects[0]["state"]; got != string(testCase.state) {
				t.Fatalf("reconcile object state = %v, want %s", got, testCase.state)
			}
			if got := objects[0]["error_class"]; got != string(testCase.errorClass) {
				t.Fatalf("reconcile error class = %v, want %s", got, testCase.errorClass)
			}
			if strings.Contains(fmt.Sprint(resp.Data), "secret-canary") {
				t.Fatalf("reconcile response contains secret value: %#v", resp.Data)
			}

			status, err := getStatus(context.Background(), storage, "app/db", associationID, syncObjectIDSecretPath)
			if err != nil {
				t.Fatalf("read status: %v", err)
			}
			if status == nil {
				t.Fatal("status must be written")
			}
			if got := status.State; got != string(testCase.state) {
				t.Fatalf("status state = %v, want %s", got, testCase.state)
			}
			if got := status.LastErrorClass; got != string(testCase.errorClass) {
				t.Fatalf("status last error class = %v, want %s", got, testCase.errorClass)
			}
			if strings.Contains(fmt.Sprint(status), "secret-canary") {
				t.Fatalf("status contains secret value: %#v", status)
			}
		})
	}
}

func TestAssociationDisableEnableAndManualSync(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	associationID := associationIDFromResponse(t, associationResp)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	disableResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db/"+associationID+"/disable",
		nil,
	)
	assertNoErrorResponse(t, disableResp)
	assertAssociationEnabled(t, disableResp, false)
	assertStringSlice(t, stringSliceFromResponse(t, disableResp, "canceled_operation_ids"), []string{operationID})
	assertQueueCount(t, b, storage, "canceled", 1)
	assertDisabledStatusObject(t, b, storage, 1)

	disabledSyncResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db/"+associationID+"/sync",
		nil,
	)
	if disabledSyncResp == nil || !disabledSyncResp.IsError() {
		t.Fatalf("sync disabled association response = %#v, want error", disabledSyncResp)
	}

	secondResp := writeAppDBSecret(t, b, storage, "rotated")
	secondMetadata := secondResp.Data["metadata"].(map[string]interface{})
	assertOperationIDs(t, secondMetadata, 0)

	enableResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db/"+associationID+"/enable",
		nil,
	)
	assertNoErrorResponse(t, enableResp)
	assertAssociationEnabled(t, enableResp, true)
	enableOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, enableResp), "enable")
	assertOutboxOperation(t, storage, enableOperationID, 2, outboxStatePending)
	runPeriodicAllowed(t, b, storage, "periodic")

	syncResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db/"+associationID+"/sync",
		nil,
	)
	assertNoErrorResponse(t, syncResp)
	syncOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, syncResp), "manual sync")
	assertStringSlice(t, []string{syncOperationID}, []string{enableOperationID})
	assertOutboxOperation(t, storage, syncOperationID, 2, outboxStatePending)
}

func TestAssociationEnableRequiresSyncableMetadata(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
		"enabled":          false,
	})
	assertNoErrorResponse(t, resp)
	associationID := associationIDFromResponse(t, resp)

	enableResp := handleRequest(
		t,
		b,
		storage,
		logical.UpdateOperation,
		"associations/app/db/"+associationID+"/enable",
		nil,
	)
	if enableResp == nil || !enableResp.IsError() {
		t.Fatalf("enable without syncable metadata response = %#v, want error", enableResp)
	}
}

func TestDataWriteCAS(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	firstResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "initial",
		},
		"options": map[string]interface{}{
			"cas": 0,
		},
	})
	assertNoErrorResponse(t, firstResp)

	secondResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
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

	thirdResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
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

func TestQueueCapacityRejectsWriteBeforeVersionCommit(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	handleRequest(t, b, storage, logical.UpdateOperation, "config", map[string]interface{}{
		"queue_capacity": 1,
		"restore_guard":  true,
	})
	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)

	secondResp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "blocked",
		},
	})
	if !secondResp.IsError() {
		t.Fatal("write must fail when queue is full")
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "data/app/db", nil)
	assertNoErrorResponse(t, readResp)
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("blocked write committed version = %v, want 1", got)
	}
}

func TestPeriodicProcessesFakeOutbox(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	association := associationResp.Data["association"].(map[string]interface{})
	associationID := association["id"]
	if got := associationResp.Data["association_id"]; got != associationID {
		t.Fatalf("association_id = %v, want %v", got, associationID)
	}
	if got := associationResp.Data["destination_ref"]; got != "fake/default" {
		t.Fatalf("destination_ref = %v, want fake/default", got)
	}

	runPeriodicAllowed(t, b, storage, "periodic")

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 0 {
		t.Fatalf("pending queue count = %v, want 0", got)
	}

	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
	assertNoErrorResponse(t, statusResp)
	if got := statusResp.Data["state"]; got != string(domain.SyncStateSynced) {
		t.Fatalf("status state = %v, want %s", got, domain.SyncStateSynced)
	}
	if got := statusResp.Data["association_id"]; got != associationID {
		t.Fatalf("status association_id = %v, want %v", got, associationID)
	}
	if got := statusResp.Data["destination_ref"]; got != "fake/default" {
		t.Fatalf("status destination_ref = %v, want fake/default", got)
	}
	if got := statusResp.Data["last_operation_id"]; got != operationID {
		t.Fatalf("status last_operation_id = %v, want %s", got, operationID)
	}
	assertSyncedStatusObject(t, statusResp.Data["objects"], operationID)

	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil || operation.State != outboxStateSucceeded {
		t.Fatalf("outbox operation = %#v, want succeeded", operation)
	}
}

func TestPeriodicHonorsRestoreGuard(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic with restore guard: %v", err)
	}
	assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)
	assertQueueCount(t, b, storage, "pending", 1)

	runPeriodicAllowed(t, b, storage, "periodic after restore guard acknowledgement")
	assertOutboxOperation(t, storage, operationID, 1, outboxStateSucceeded)
}

func TestPeriodicSkipsUnsafeReplicationNode(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	acknowledgeRestoreGuard(t, b, storage)

	if err := b.Setup(context.Background(), &logical.BackendConfig{
		System: &logical.StaticSystemView{
			ReplicationStateVal: consts.ReplicationPerformanceSecondary,
		},
	}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	if err := b.periodic(context.Background(), &logical.Request{Storage: storage}); err != nil {
		t.Fatalf("periodic on unsafe replication node: %v", err)
	}
	assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)
}

func TestPeriodicRejectsPayloadOverProviderLimit(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	resp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": strings.Repeat("x", 1024*1024) + "secret-canary",
		},
	})
	assertNoErrorResponse(t, resp)
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	runPeriodicAllowed(t, b, storage, "periodic")

	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil || operation.State != outboxStateFailedTerminal {
		t.Fatalf("outbox operation = %#v, want terminal failure", operation)
	}
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
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
	if got := object["payload_sha256"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("payload_sha256 = %q, want sha256 prefix", got)
	}
}

func TestPeriodicRejectsPayloadOverAWSProviderLimit(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	resp := handleRequest(t, b, storage, logical.UpdateOperation, "data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": strings.Repeat("x", 70*1024) + "secret-canary",
		},
	})
	assertNoErrorResponse(t, resp)
	resp = handleRequest(t, b, storage, logical.UpdateOperation, "destinations/aws-sm/prod", map[string]interface{}{
		"description":                             "aws production",
		awssecretsmanager.ConfigKeyRegion:         "us-east-1",
		awssecretsmanager.ConfigKeyEndpointURL:    "http://localhost:4566",
		awssecretsmanager.ConfigKeyEndpointPolicy: awssecretsmanager.EndpointPolicyLocal,
		awssecretsmanager.ConfigKeyAuthMode:       awssecretsmanager.AuthModeDefault,
	})
	if resp != nil && resp.IsError() {
		t.Fatalf("unexpected destination write error: %v", resp.Error())
	}
	markAppDBSyncable(t, b, storage)
	associationResp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": awssecretsmanager.ProviderType,
		"destination_name": "prod",
		"resolved_name":    "openbao-secret-sync/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
	operationID := operationIDsFromResponse(t, associationResp)[0]

	runPeriodicAllowed(t, b, storage, "periodic")

	operation := assertOutboxOperation(t, storage, operationID, 1, outboxStateFailedTerminal)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	statusResp := handleRequest(t, b, storage, logical.ReadOperation, "status/app/db", nil)
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
	if got := object["payload_sha256"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("payload_sha256 = %q, want sha256 prefix", got)
	}
}

func TestPeriodicRetriesTransientProviderErrors(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createFakeAssociationWithResolvedName(t, b, storage, "prod/unavailable/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	runPeriodicAllowed(t, b, storage, "periodic")
	operation := assertOutboxOperation(t, storage, operationID, 1, outboxStateRetryWait)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	assertFutureNotBefore(t, operation.NotBefore)
	assertQueueCount(t, b, storage, "retry_wait", 1)
	assertStatusObjectErrorClass(t, b, storage, providers.ErrorClassUnavailable)
	assertStatusObjectState(t, b, storage, domain.SyncStateDestinationUnavailable)

	for range 2 {
		operation = runDueRetry(t, b, storage, *operation)
	}
	operation = assertOutboxOperation(t, storage, operationID, 1, outboxStateFailedTerminal)
	if got := operation.Attempts; got != maxAutomaticRetryAttempts {
		t.Fatalf("attempts = %d, want %d", got, maxAutomaticRetryAttempts)
	}
}

func TestPeriodicRetriesRateLimitProviderErrors(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createFakeAssociationWithResolvedName(t, b, storage, "prod/rate-limit/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	runPeriodicAllowed(t, b, storage, "periodic")
	operation := assertOutboxOperation(t, storage, operationID, 1, outboxStateRetryWait)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	assertFutureNotBefore(t, operation.NotBefore)
	assertQueueCount(t, b, storage, "retry_wait", 1)
	assertStatusObjectErrorClass(t, b, storage, providers.ErrorClassRateLimit)
	assertStatusObjectState(t, b, storage, domain.SyncStateDestinationRateLimited)
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
			b := Backend(&logical.BackendConfig{})
			storage := &logical.InmemStorage{}

			writeAppDBSecret(t, b, storage, "initial")
			createFakeDestination(t, b, storage, "default")
			associationResp := createFakeAssociationWithResolvedName(t, b, storage, testCase.resolvedName)
			operationID := operationIDsFromResponse(t, associationResp)[0]

			runPeriodicAllowed(t, b, storage, "periodic")
			operation := assertOutboxOperation(t, storage, operationID, 1, outboxStateFailedTerminal)
			if got := operation.Attempts; got != 1 {
				t.Fatalf("attempts = %d, want 1", got)
			}
			assertStatusObjectErrorClass(t, b, storage, testCase.errorClass)
			assertStatusObjectState(t, b, storage, testCase.state)
		})
	}
}

func TestQueueOperationReadCancelAndRetry(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "queue/"+operationID, nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["state"]; got != outboxStatePending {
		t.Fatalf("operation state = %v, want %s", got, outboxStatePending)
	}

	cancelResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/cancel", nil)
	assertNoErrorResponse(t, cancelResp)
	if got := cancelResp.Data["state"]; got != outboxStateCanceled {
		t.Fatalf("canceled operation state = %v, want %s", got, outboxStateCanceled)
	}
	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 0 {
		t.Fatalf("pending queue count = %v, want 0", got)
	}
	if got := queueResp.Data["canceled"]; got != 1 {
		t.Fatalf("canceled queue count = %v, want 1", got)
	}

	retryResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/retry", nil)
	assertNoErrorResponse(t, retryResp)
	if got := retryResp.Data["state"]; got != outboxStatePending {
		t.Fatalf("retried operation state = %v, want %s", got, outboxStatePending)
	}
}

func TestQueueDrainProcessesDueOperations(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	acknowledgeRestoreGuard(t, b, storage)

	drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	if got := drainResp.Data["processed"]; got != 1 {
		t.Fatalf("processed = %v, want 1", got)
	}
	if got := drainResp.Data["queue_pending"]; got != 0 {
		t.Fatalf("queue_pending = %v, want 0", got)
	}
	queue := drainResp.Data["queue"].(map[string]interface{})
	if got := queue["pending"]; got != 0 {
		t.Fatalf("pending = %v, want 0", got)
	}
	assertOutboxOperation(t, storage, operationID, 1, outboxStateSucceeded)
}

func TestQueueDrainSkipsUnexpiredClaim(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	operation.ClaimOwner = "worker-other"
	operation.ClaimExpiresTime = nowUTC().Add(time.Hour).Format(timeFormatRFC3339)
	operation.ClaimAttempt = 1
	if err := putOutbox(context.Background(), storage, *operation); err != nil {
		t.Fatalf("write claimed outbox operation: %v", err)
	}

	acknowledgeRestoreGuard(t, b, storage)
	drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	if got := drainResp.Data["processed"]; got != 0 {
		t.Fatalf("processed = %v, want 0", got)
	}
	if got := drainResp.Data["queue_claimed"]; got != 1 {
		t.Fatalf("queue_claimed = %v, want 1", got)
	}
	operation = assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)
	if operation.ClaimOwner != "worker-other" {
		t.Fatalf("claim_owner = %q, want worker-other", operation.ClaimOwner)
	}

	cancelResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/cancel", nil)
	if cancelResp == nil || !cancelResp.IsError() {
		t.Fatalf("cancel claimed operation response = %#v, want error", cancelResp)
	}
}

func TestQueueDrainReclaimsExpiredClaimAndClearsIt(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	operation.ClaimOwner = "worker-stale"
	operation.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	operation.ClaimAttempt = 1
	if err := putOutbox(context.Background(), storage, *operation); err != nil {
		t.Fatalf("write expired claimed outbox operation: %v", err)
	}

	acknowledgeRestoreGuard(t, b, storage)
	drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	if got := drainResp.Data["processed"]; got != 1 {
		t.Fatalf("processed = %v, want 1", got)
	}
	operation = assertOutboxOperation(t, storage, operationID, 1, outboxStateSucceeded)
	if operation.ClaimOwner != "" || operation.ClaimExpiresTime != "" || operation.ClaimAttempt != 0 {
		t.Fatalf("claim fields after success = %#v, want cleared", operation)
	}

	readResp := handleRequest(t, b, storage, logical.ReadOperation, "queue/"+operationID, nil)
	assertNoErrorResponse(t, readResp)
	if got := readResp.Data["claimed"]; got != false {
		t.Fatalf("claimed response = %v, want false", got)
	}
}

func TestQueueDrainClearsClaimAfterRetryableFailure(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createFakeAssociationWithResolvedName(t, b, storage, "prod/rate-limit/app/db")
	operationID := operationIDsFromResponse(t, associationResp)[0]

	runPeriodicAllowed(t, b, storage, "periodic")
	operation := assertOutboxOperation(t, storage, operationID, 1, outboxStateRetryWait)
	if operation.ClaimOwner != "" || operation.ClaimExpiresTime != "" || operation.ClaimAttempt != 0 {
		t.Fatalf("claim fields after retryable failure = %#v, want cleared", operation)
	}
}

func TestQueueDrainHonorsRestoreGuard(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	if drainResp == nil || !drainResp.IsError() {
		t.Fatalf("drain restore guard response = %#v, want error", drainResp)
	}
	assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)

	acknowledgeRestoreGuard(t, b, storage)
	drainResp = handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	assertNoErrorResponse(t, drainResp)
	if got := drainResp.Data["processed"]; got != 1 {
		t.Fatalf("processed after acknowledge = %v, want 1", got)
	}
}

func TestQueueDrainRejectsUnsafeReplicationNode(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	acknowledgeRestoreGuard(t, b, storage)

	if err := b.Setup(context.Background(), &logical.BackendConfig{
		System: &logical.StaticSystemView{
			ReplicationStateVal: consts.ReplicationPerformanceSecondary,
		},
	}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}
	drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", map[string]interface{}{
		"max_operations": 1,
	})
	if drainResp == nil || !drainResp.IsError() {
		t.Fatalf("drain unsafe replication response = %#v, want error", drainResp)
	}
	if !strings.Contains(drainResp.Error().Error(), remoteMutationUnsafeError) {
		t.Fatalf("drain unsafe replication error = %q, want %q", drainResp.Error().Error(), remoteMutationUnsafeError)
	}
	assertOutboxOperation(t, storage, operationID, 1, outboxStatePending)
}

func TestQueueSummaryOldestAge(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	operation.CreatedTime = nowUTC().Add(-2 * time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), storage, *operation); err != nil {
		t.Fatalf("write old outbox operation: %v", err)
	}

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["oldest_age_seconds"].(int); got < 120 {
		t.Fatalf("oldest_age_seconds = %v, want at least 120", got)
	}
}

func TestQueueDrainHonorsDisabledConfig(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	configResp := handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"disabled": true,
	})
	if configResp != nil && configResp.IsError() {
		t.Fatalf("unexpected config write error: %v", configResp.Error())
	}

	drainResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/drain", nil)
	if drainResp == nil || !drainResp.IsError() {
		t.Fatalf("drain disabled response = %#v, want error", drainResp)
	}
}

func TestQueueOperationRejectsRetryAfterSuccess(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	operationID := operationIDsFromResponse(t, associationResp)[0]
	runPeriodicAllowed(t, b, storage, "periodic")

	retryResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/retry", nil)
	if retryResp == nil || !retryResp.IsError() {
		t.Fatalf("retry succeeded operation response = %#v, want error", retryResp)
	}
	cancelResp := handleRequest(t, b, storage, logical.UpdateOperation, "queue/"+operationID+"/cancel", nil)
	if cancelResp == nil || !cancelResp.IsError() {
		t.Fatalf("cancel succeeded operation response = %#v, want error", cancelResp)
	}
}

func TestPeriodicRecoversIncompleteEnqueueIntent(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)
	runPeriodicAllowed(t, b, storage, "periodic")

	secondResp := writeAppDBSecret(t, b, storage, "rotated")
	metadata := secondResp.Data["metadata"].(map[string]interface{})
	operationID := operationIDsFromMetadata(t, metadata)[0]
	operation, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read outbox operation: %v", err)
	}
	if operation == nil {
		t.Fatal("outbox operation must exist before simulated loss")
	}
	if err := deleteOutbox(context.Background(), storage, *operation); err != nil {
		t.Fatalf("delete outbox operation: %v", err)
	}
	intent, err := getEnqueueIntent(context.Background(), storage, "app/db", 2)
	if err != nil {
		t.Fatalf("read enqueue intent: %v", err)
	}
	intent.Complete = false
	intent.CompletedTime = ""
	if err := putEnqueueIntent(context.Background(), storage, *intent); err != nil {
		t.Fatalf("write incomplete enqueue intent: %v", err)
	}

	runPeriodicAllowed(t, b, storage, "periodic recovery")
	recovered, err := getOutbox(context.Background(), storage, operationID)
	if err != nil {
		t.Fatalf("read recovered outbox operation: %v", err)
	}
	if recovered == nil || recovered.State != outboxStateSucceeded {
		t.Fatalf("recovered operation = %#v, want succeeded operation", recovered)
	}
	intent, err = getEnqueueIntent(context.Background(), storage, "app/db", 2)
	if err != nil {
		t.Fatalf("read recovered enqueue intent: %v", err)
	}
	if intent == nil || !intent.Complete {
		t.Fatalf("recovered enqueue intent = %#v, want complete", intent)
	}
}

func TestRecoveryRestoresDeleteIntentAfterSourceDelete(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createFakeAssociationWithDeleteMode(t, b, storage, deleteModeDelete)
	runPeriodicAllowed(t, b, storage, "periodic upsert")
	deleteResp := handleRequest(t, b, storage, logical.DeleteOperation, "data/app/db", nil)
	assertNoErrorResponse(t, deleteResp)
	deleteOperationID := operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{}))[0]
	operation, err := getOutbox(context.Background(), storage, deleteOperationID)
	if err != nil {
		t.Fatalf("read delete operation: %v", err)
	}
	if operation == nil {
		t.Fatal("delete operation must exist before simulated loss")
	}
	if err := deleteOutbox(context.Background(), storage, *operation); err != nil {
		t.Fatalf("delete outbox operation: %v", err)
	}
	intent, err := getEnqueueIntent(context.Background(), storage, "app/db", 1)
	if err != nil {
		t.Fatalf("read delete enqueue intent: %v", err)
	}
	intent.Complete = false
	intent.CompletedTime = ""
	if err := putEnqueueIntent(context.Background(), storage, *intent); err != nil {
		t.Fatalf("write incomplete delete enqueue intent: %v", err)
	}

	if err := recoverIncompleteEnqueueIntents(context.Background(), storage, nowUTC()); err != nil {
		t.Fatalf("recover delete enqueue intent: %v", err)
	}
	recovered := assertOutboxOperation(t, storage, deleteOperationID, 1, outboxStatePending)
	if got := recovered.Type; got != outbox.OperationTypeDelete {
		t.Fatalf("recovered operation type = %s, want %s", got, outbox.OperationTypeDelete)
	}
}

func TestRecoveryCompletesIntentWithoutCommittedVersion(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	associationResp := createDefaultFakeAssociation(t, b, storage)
	associationID := associationIDFromResponse(t, associationResp)
	association, err := getAssociation(context.Background(), storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	now := nowUTC().Format(timeFormatRFC3339)
	operation := newAssociationOutboxRecord(*association, 99, syncObjectIDSecretPath, now)
	intent := newEnqueueIntentRecord("app/db", 99, []outboxRecord{operation}, now)
	if err := putEnqueueIntent(context.Background(), storage, intent); err != nil {
		t.Fatalf("write enqueue intent: %v", err)
	}

	if err := recoverIncompleteEnqueueIntents(context.Background(), storage, nowUTC()); err != nil {
		t.Fatalf("recover incomplete enqueue intents: %v", err)
	}
	recoveredIntent, err := getEnqueueIntent(context.Background(), storage, "app/db", 99)
	if err != nil {
		t.Fatalf("read recovered enqueue intent: %v", err)
	}
	if recoveredIntent == nil || !recoveredIntent.Complete {
		t.Fatalf("recovered enqueue intent = %#v, want complete", recoveredIntent)
	}
	recoveredOperation, err := getOutbox(context.Background(), storage, operation.ID)
	if err != nil {
		t.Fatalf("read recovered operation: %v", err)
	}
	if recoveredOperation != nil {
		t.Fatalf("recovered operation = %#v, want nil without committed version", recoveredOperation)
	}
}

func TestPeriodicHonorsDisabledConfig(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)
	handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"disabled": true,
	})

	runPeriodicAllowed(t, b, storage, "periodic")

	queueResp := handleRequest(t, b, storage, logical.ReadOperation, "queue", nil)
	assertNoErrorResponse(t, queueResp)
	if got := queueResp.Data["pending"]; got != 1 {
		t.Fatalf("pending queue count = %v, want 1", got)
	}
}

func TestQueueCapacityCountsQueuedOperationsOnly(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	storage := &logical.InmemStorage{}

	handleRequest(t, b, storage, logical.UpdateOperation, configPath, map[string]interface{}{
		"queue_capacity": 1,
	})
	writeAppDBSecret(t, b, storage, "initial")
	createFakeDestination(t, b, storage, "default")
	createDefaultFakeAssociation(t, b, storage)
	runPeriodicAllowed(t, b, storage, "periodic")

	secondResp := writeAppDBSecret(t, b, storage, "allowed")
	metadata := secondResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 2 {
		t.Fatalf("second write version = %v, want 2", got)
	}
	assertCompleteEnqueueIntent(t, storage, "app/db", 2, metadata)
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

func assertCompleteEnqueueIntent(
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
	if intent == nil || !intent.Complete {
		t.Fatalf("enqueue intent = %#v, want complete intent", intent)
	}
	if got := intent.OperationIDs; len(got) != len(operationIDs) || got[0] != operationIDs[0] {
		t.Fatalf("enqueue intent operation IDs = %v, want %v", got, operationIDs)
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
	association, ok := resp.Data["association"].(map[string]interface{})
	if !ok {
		t.Fatalf("association = %T, want map[string]interface{}", resp.Data["association"])
	}
	if got := association["enabled"]; got != want {
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
	if got := object["payload_sha256"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("%s payload_sha256 = %q, want sha256 prefix", objectID, got)
	}
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
	if got := object["payload_sha256"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("%s payload_sha256 = %q, want sha256 prefix", objectID, got)
	}
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

func stringSliceFromResponse(t *testing.T, resp *logical.Response, key string) []string {
	t.Helper()
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
	association, ok := resp.Data["association"].(map[string]interface{})
	if !ok {
		t.Fatalf("association = %T, want map[string]interface{}", resp.Data["association"])
	}
	id, ok := association["id"].(string)
	if !ok || id == "" {
		t.Fatalf("association id = %v, want non-empty string", association["id"])
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

func createDefaultFakeAssociation(t *testing.T, b logical.Backend, storage logical.Storage) *logical.Response {
	t.Helper()
	markAppDBSyncable(t, b, storage)
	return handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
}

func createFakeSecretKeyAssociation(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	deleteMode string,
) *logical.Response {
	t.Helper()
	markAppDBSyncable(t, b, storage)
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"name_template":    "prod/{{ path }}/{{ key }}",
		"granularity":      syncGranularitySecretKey,
		"format":           defaultAssociationFormat,
		"delete_mode":      deleteMode,
	})
	assertNoErrorResponse(t, resp)
	return resp
}

func createFakeAssociationWithDeleteMode(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	deleteMode string,
) {
	t.Helper()
	markAppDBSyncable(t, b, storage)
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
		"delete_mode":      deleteMode,
	})
	assertNoErrorResponse(t, resp)
}

func createFakeAssociationWithResolvedName(
	t *testing.T,
	b logical.Backend,
	storage logical.Storage,
	resolvedName string,
) *logical.Response {
	t.Helper()
	markAppDBSyncable(t, b, storage)
	return handleRequest(t, b, storage, logical.UpdateOperation, "associations/app/db", map[string]interface{}{
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    resolvedName,
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
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
		"destination_type": providerTypeFake,
		"destination_name": "default",
		"resolved_name":    resolvedName,
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
	})
}

func markAppDBSyncable(t *testing.T, b logical.Backend, storage logical.Storage) {
	t.Helper()
	resp := handleRequest(t, b, storage, logical.UpdateOperation, "metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			sourceMetadataKeySyncable: sourceMetadataValueTrue,
		},
	})
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
	if got := object["payload_sha256"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("object payload_sha256 = %q, want sha256 prefix", got)
	}
}
