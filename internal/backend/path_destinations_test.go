package backend

import (
	"context"
	"fmt"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/kubernetessecrets"
)

const testKubernetesCACertPEM = `-----BEGIN CERTIFICATE-----
MIIDIzCCAgugAwIBAgIUE5FUmToiQv3bNaxE1dI9jJj8bsIwDQYJKoZIhvcNAQEL
BQAwITEfMB0GA1UEAwwWa3ViZXJuZXRlcy5kZWZhdWx0LnN2YzAeFw0yNjA3MDIx
NjE0MDdaFw0yNjA3MDMxNjE0MDdaMCExHzAdBgNVBAMMFmt1YmVybmV0ZXMuZGVm
YXVsdC5zdmMwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQCwKLu09rvV
i57nXOwK7W6iDMM1X606UQrfTb/T5pyjFE1g6DajO/QMZqC4n1+b86uDwpiCjobb
p+Iu3FaMHEJyoejKQbd2d6VvEjfHHCAson/XZEWgCVwk03L7YCu55zYzaC4tyc/u
5hsdgnJGvi6TGpxFvhRUkMQcnAwsUBZ7YVV1pc6B81UTWkGYQdo1mIdqHcx1ngQ3
A7bjmfEtPxVM51DEYc9DJSCmbmXShXAs1kdc424mhBng4hNzAv6hLLFL9DqD6Wmn
KEcAFi4SrG3IxsC3i8ph1uRQrgZX5ASVj+gwNPhp4937AYU+kJjg2VOY3iQQkOYo
+fy4dIch/zuNAgMBAAGjUzBRMB0GA1UdDgQWBBTxoSmRj83w0WIpKC2cpjZ1U8qI
aDAfBgNVHSMEGDAWgBTxoSmRj83w0WIpKC2cpjZ1U8qIaDAPBgNVHRMBAf8EBTAD
AQH/MA0GCSqGSIb3DQEBCwUAA4IBAQCt16lTmsc2/nHqI0Zi77AxPN+XfXdm+oW7
bdUeOzL1ZhwvXbcbXRzV19mnRM3oAkYQIA5+XDNN63AMm3QQ0sdC9exya+mbGokz
dh4uSM/A2qc5e08acV9VkRD8aPMBjdYXuKmfeCAkVq3y86EOEYe0Uh+sBVfU2Q+a
1G+M56JnByoz+zAwI4yUMfqJ5tGvUsB99DuWWzSAtgNKC2mtV9rG7OhEi2hAx42T
ONdZhbrc5TmwV7TpNa0pSjVsBOjaavQSGw9UN3p4oXoSKaZoFVYN8bbarZM19g5v
T459I6FRYgo1Ut0HO2F/8edsZ5cAIgn4gVlqDQkMvWK1zNlp59CF
-----END CERTIFICATE-----`

func TestDestinationValidateSupportsRead(t *testing.T) {
	env := newBackendTestEnv(t)
	env.createFakeDestination("default")

	resp := env.read("destinations/fake/default/validate")
	assertNoErrorResponse(t, resp)
	assertResponseValue(t, resp, "valid", true)
}

func TestDestinationCheckReportsReady(t *testing.T) {
	env := newBackendTestEnv(t)

	env.createFakeDestination("primary")

	readyResp := env.read("destinations/fake/primary/check")
	assertNoErrorResponse(t, readyResp)
	assertResponseValue(t, readyResp, "ready", true)
	assertResponseValue(t, readyResp, "valid", true)
	assertResponseValue(t, readyResp, "healthy", true)
	assertResponseValue(t, readyResp, "health_checked", true)
	assertStringSlice(t, readyResp.Data["blockers"].([]string), []string{})
}

func TestDestinationCheckReportsDelegatedModeUnconstrained(t *testing.T) {
	env := newBackendTestEnv(t)

	cfgResp := env.update(configPath, map[string]interface{}{
		"require_source_opt_in": true,
		"delegated_mode":        true,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.createFakeDestination("primary")

	readyResp := env.read("destinations/fake/primary/check")
	assertNoErrorResponse(t, readyResp)
	assertResponseValue(t, readyResp, "ready", false)
	assertResponseValue(t, readyResp, "valid", true)
	assertResponseValue(t, readyResp, "healthy", true)
	assertStringSlice(t, readyResp.Data["blockers"].([]string), []string{destinationUnconstrainedBlocker})

	updateResp := env.update(
		"destinations/fake/primary",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "app",
			destinationAllowedResolvedNamePrefixesField: "prod/app/",
		},
	)
	if updateResp != nil && updateResp.IsError() {
		t.Fatalf("unexpected destination constraint update error: %v", updateResp.Error())
	}
	constrainedResp := env.read("destinations/fake/primary/check")
	assertNoErrorResponse(t, constrainedResp)
	assertResponseValue(t, constrainedResp, "ready", true)
	assertStringSlice(t, constrainedResp.Data["blockers"].([]string), []string{})
}

func TestDestinationCheckReportsValidationFailure(t *testing.T) {
	env := newBackendTestEnv(t)

	now := nowUTC().Format(timeFormatRFC3339)
	if err := putDestination(context.Background(), env.storage, destinationRecord{
		Type:        providerTypeFake,
		Name:        "invalid",
		CreatedTime: now,
		UpdatedTime: now,
	}); err != nil {
		t.Fatalf("write invalid destination fixture: %v", err)
	}
	invalidResp := env.read("destinations/fake/invalid/check")
	assertNoErrorResponse(t, invalidResp)
	assertResponseValue(t, invalidResp, "ready", false)
	assertResponseValue(t, invalidResp, "valid", false)
	assertResponseValue(t, invalidResp, "health_checked", false)
	assertResponseValue(t, invalidResp, "validation_error_class", string(providers.ErrorClassValidation))
	assertStringSlice(t, invalidResp.Data["blockers"].([]string), []string{"validation_failed"})
}

func TestDestinationCheckReportsHealthFailure(t *testing.T) {
	env := newBackendTestEnv(t)

	env.createFakeDestination("unhealthy")
	unhealthyResp := env.read("destinations/fake/unhealthy/check")
	assertNoErrorResponse(t, unhealthyResp)
	assertResponseValue(t, unhealthyResp, "ready", false)
	assertResponseValue(t, unhealthyResp, "valid", true)
	assertResponseValue(t, unhealthyResp, "healthy", false)
	assertResponseValue(t, unhealthyResp, "health_error_class", string(providers.ErrorClassUnavailable))
	assertStringSlice(t, unhealthyResp.Data["blockers"].([]string), []string{"health_failed"})
}

func TestDestinationCheckReportsDisabled(t *testing.T) {
	env := newBackendTestEnv(t)

	disabledResp := env.update(
		"destinations/fake/disabled",
		map[string]interface{}{
			"description": "disabled destination",
			"disabled":    true,
		},
	)
	if disabledResp != nil && disabledResp.IsError() {
		t.Fatalf("unexpected disabled destination write error: %v", disabledResp.Error())
	}
	disabledCheckResp := env.read("destinations/fake/disabled/check")
	assertNoErrorResponse(t, disabledCheckResp)
	assertResponseValue(t, disabledCheckResp, "ready", false)
	assertResponseValue(t, disabledCheckResp, "valid", true)
	assertResponseValue(t, disabledCheckResp, "health_checked", false)
	assertStringSlice(t, disabledCheckResp.Data["blockers"].([]string), []string{"destination_disabled"})
}

func TestDestinationWriteValidatesProviderConfig(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("destinations/fake/invalid")
	if resp == nil || !resp.IsError() {
		t.Fatalf("invalid destination write response = %#v, want error", resp)
	}
	record, err := getDestination(context.Background(), env.storage, providerTypeFake, "invalid")
	if err != nil {
		t.Fatalf("read invalid destination: %v", err)
	}
	if record != nil {
		t.Fatalf("invalid destination was stored: %#v", record)
	}
}

func TestDestinationWriteRejectsCrossProviderConfig(t *testing.T) {
	env := newBackendTestEnv(t)

	resp := env.update("destinations/k8s/prod", map[string]interface{}{
		kubernetessecrets.ConfigKeyNamespace: "apps",
		gitlab.ConfigKeyToken:                "glpat-secret",
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("cross-provider destination response = %#v, want error", resp)
	}
	record, err := getDestination(context.Background(), env.storage, kubernetessecrets.ProviderType, "prod")
	if err != nil {
		t.Fatalf("read rejected k8s destination: %v", err)
	}
	if record != nil {
		t.Fatalf("cross-provider destination was stored: %#v", record)
	}
}

func TestDestinationLifecycle(t *testing.T) {
	env := newBackendTestEnv(t)

	env.createFakeDestination("primary")
	readResp := env.read("destinations/fake/primary")
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "name", "primary")
	if _, ok := readResp.Data["sensitive_config"]; !ok {
		t.Fatal("destination read must include redacted sensitive_config")
	}

	listResp := env.list("destinations/fake")
	assertNoErrorResponse(t, listResp)
	keys := listResp.Data["keys"].([]string)
	if len(keys) != 1 || keys[0] != "primary" {
		t.Fatalf("destination keys = %v, want [primary]", keys)
	}

	deleteResp := env.delete("destinations/fake/primary")
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected destination delete error: %v", deleteResp.Error())
	}
	readDeletedResp := env.read("destinations/fake/primary")
	if readDeletedResp != nil {
		t.Fatalf("deleted destination response = %#v, want nil", readDeletedResp)
	}
}

func TestDestinationDeleteIgnoresDanglingAssociationIndex(t *testing.T) {
	env := newBackendTestEnv(t)

	env.createFakeDestination("primary")
	putStorageJSON(
		t,
		env.storage,
		associationByDestinationStorageKey("fake", "primary", "assoc-missing"),
		"app/missing",
	)

	deleteResp := env.delete("destinations/fake/primary")
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected destination delete error: %v", deleteResp.Error())
	}
	readDeletedResp := env.read("destinations/fake/primary")
	if readDeletedResp != nil {
		t.Fatalf("deleted destination response = %#v, want nil", readDeletedResp)
	}
}

func TestDestinationListPagination(t *testing.T) {
	env := newBackendTestEnv(t)

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		env.createFakeDestination(name)
	}

	assertListKeys(t,
		env.list("destinations/fake"),
		[]string{"alpha", "bravo", "charlie"},
	)
	assertListKeys(t,
		env.list("destinations/fake", map[string]interface{}{
			"limit": 2,
		}),
		[]string{"alpha", "bravo"},
	)
	assertListKeys(t,
		env.list("destinations/fake", map[string]interface{}{
			"after": "alpha",
			"limit": 1,
		}),
		[]string{"bravo"},
	)
	assertListKeys(t,
		env.list("destinations/fake", map[string]interface{}{
			"after": "alpha-missing",
			"limit": 1,
		}),
		[]string{"bravo"},
	)
	assertListKeys(t,
		env.list("destinations/fake", map[string]interface{}{
			"limit": 0,
		}),
		[]string{"alpha", "bravo", "charlie"},
	)
}

func TestDestinationPolicyPrefixesNormalizeAndRead(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update(
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "team/api, app ,team/api",
			destinationAllowedResolvedNamePrefixesField: "prod/app/, team/api",
		},
	)
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	readResp := env.read("destinations/fake/restricted")
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
	env := newBackendTestEnv(t)

	writeResp := env.update("destinations/aws-sm/prod", map[string]interface{}{
		"description":                                       "aws production",
		awssecretsmanager.ConfigKeyRegion:                   "eu-central-1",
		awssecretsmanager.ConfigKeyEndpointURL:              "http://localhost:4566",
		awssecretsmanager.ConfigKeyEndpointPolicy:           awssecretsmanager.EndpointPolicyLocal,
		awssecretsmanager.ConfigKeyAuthMode:                 awssecretsmanager.AuthModeAssumeRole,
		awssecretsmanager.ConfigKeyRoleARN:                  "arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync",
		awssecretsmanager.ConfigKeyExternalID:               "tenant-1",
		awssecretsmanager.ConfigKeySessionName:              "openbao-sync",
		awssecretsmanager.ConfigKeyDeleteRecoveryWindowDays: "14",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	assertStoredAWSDestinationConfig(t, env.storage)
	assertReadAWSDestinationConfig(t, env.b, env.storage)

	validateResp := env.update("destinations/aws-sm/prod/validate")
	assertNoErrorResponse(t, validateResp)
	assertResponseValue(t, validateResp, "valid", true)
}

func TestAWSWebIdentityDestinationConfigLifecycle(t *testing.T) {
	env := newBackendTestEnv(t)

	identityFile := "/var/run/openbao/aws-web-identity.jwt"
	writeResp := env.update("destinations/aws-sm/prod", map[string]interface{}{
		"description":                                   "aws production",
		awssecretsmanager.ConfigKeyRegion:               "eu-central-1",
		awssecretsmanager.ConfigKeyAuthMode:             awssecretsmanager.AuthModeWebIdentity,
		awssecretsmanager.ConfigKeyRoleARN:              "arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync",
		awssecretsmanager.ConfigKeyWebIdentityTokenFile: identityFile,
		awssecretsmanager.ConfigKeySessionName:          "openbao-sync",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	storedDestination, err := getDestination(context.Background(), env.storage, awssecretsmanager.ProviderType, "prod")
	if err != nil {
		t.Fatalf("read stored destination: %v", err)
	}
	if got := storedDestination.Config[awssecretsmanager.ConfigKeyWebIdentityTokenFile]; got != identityFile {
		t.Fatalf("stored web_identity_token_file = %q, want %q", got, identityFile)
	}
	if _, ok := storedDestination.Config[awssecretsmanager.ConfigKeyExternalID]; ok {
		t.Fatal("web_identity destination must not store external_id")
	}

	readResp := env.read("destinations/aws-sm/prod")
	assertNoErrorResponse(t, readResp)
	config := readResp.Data["config"].(map[string]interface{})
	if got := config[awssecretsmanager.ConfigKeyAuthMode]; got != awssecretsmanager.AuthModeWebIdentity {
		t.Fatalf("aws auth_mode = %v, want %s", got, awssecretsmanager.AuthModeWebIdentity)
	}
	if got := config[awssecretsmanager.ConfigKeyWebIdentityTokenFile]; got != identityFile {
		t.Fatalf("read web_identity_token_file = %v, want %s", got, identityFile)
	}
	sensitiveConfig := readResp.Data["sensitive_config"].(map[string]interface{})
	if got := sensitiveConfig["configured"]; got != false {
		t.Fatalf("sensitive_config configured = %v, want false", got)
	}

	validateResp := env.update("destinations/aws-sm/prod/validate")
	assertNoErrorResponse(t, validateResp)
	assertResponseValue(t, validateResp, "valid", true)
}

func TestKubernetesDestinationConfigLifecycle(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update("destinations/k8s/prod", map[string]interface{}{
		"description":                          "kubernetes production",
		kubernetessecrets.ConfigKeyNamespace:   "apps",
		kubernetessecrets.ConfigKeyAuthMode:    kubernetessecrets.AuthModeInCluster,
		kubernetessecrets.ConfigKeyKubeContext: "",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	readResp := env.read("destinations/k8s/prod")
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

	validateResp := env.update("destinations/k8s/prod/validate")
	assertNoErrorResponse(t, validateResp)
	assertResponseValue(t, validateResp, "valid", true)
	capabilities := validateResp.Data["capabilities"].(map[string]interface{})
	if got := capabilities["supports_value_readback"]; got != true {
		t.Fatalf("k8s supports_value_readback = %v, want true", got)
	}
	if got := capabilities["supports_data_map"]; got != true {
		t.Fatalf("k8s supports_data_map = %v, want true", got)
	}
}

func TestKubernetesTokenDestinationConfigLifecycle(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update("destinations/k8s/prod", map[string]interface{}{
		"description":                            "kubernetes production",
		kubernetessecrets.ConfigKeyNamespace:     "apps",
		kubernetessecrets.ConfigKeyAuthMode:      kubernetessecrets.AuthModeToken,
		kubernetessecrets.ConfigKeyAPIServer:     "https://kubernetes.example.com",
		kubernetessecrets.ConfigKeyToken:         "bearer-token",
		kubernetessecrets.ConfigKeyCACertPEM:     testKubernetesCACertPEM,
		kubernetessecrets.ConfigKeyTLSServerName: "kubernetes.default.svc",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	storedDestination, err := getDestination(context.Background(), env.storage, kubernetessecrets.ProviderType, "prod")
	if err != nil {
		t.Fatalf("read stored k8s destination: %v", err)
	}
	if _, ok := storedDestination.Config[kubernetessecrets.ConfigKeyToken]; ok {
		t.Fatal("k8s token must not be stored in destination config")
	}
	if got := storedDestination.Config[kubernetessecrets.ConfigKeyAPIServer]; got != "https://kubernetes.example.com" {
		t.Fatalf("k8s api_server = %v, want https://kubernetes.example.com", got)
	}

	storedSensitiveConfig, err := getDestinationSensitiveConfig(
		context.Background(),
		env.storage,
		kubernetessecrets.ProviderType,
		"prod",
	)
	if err != nil {
		t.Fatalf("read k8s sensitive config: %v", err)
	}
	if got := storedSensitiveConfig.Config[kubernetessecrets.ConfigKeyToken]; got != "bearer-token" {
		t.Fatalf("stored k8s token = %v, want bearer-token", got)
	}

	readResp := env.read("destinations/k8s/prod")
	assertNoErrorResponse(t, readResp)
	config := readResp.Data["config"].(map[string]interface{})
	if _, ok := config[kubernetessecrets.ConfigKeyToken]; ok {
		t.Fatal("k8s token must not be returned in config")
	}
	assertInterfaceMapValue(t, config, kubernetessecrets.ConfigKeyAPIServer, "https://kubernetes.example.com")
	sensitiveConfig := readResp.Data["sensitive_config"].(map[string]interface{})
	if got := sensitiveConfig["configured"]; got != true {
		t.Fatalf("k8s sensitive_config configured = %v, want true", got)
	}
	keys := sensitiveConfig["keys"].([]string)
	if len(keys) != 1 || keys[0] != kubernetessecrets.ConfigKeyToken {
		t.Fatalf("k8s sensitive keys = %v, want [%s]", keys, kubernetessecrets.ConfigKeyToken)
	}

	validateResp := env.update("destinations/k8s/prod/validate")
	assertNoErrorResponse(t, validateResp)
	assertResponseValue(t, validateResp, "valid", true)
}

func TestGitLabDestinationConfigLifecycle(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update("destinations/gitlab/prod", map[string]interface{}{
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

	storedDestination, err := getDestination(context.Background(), env.storage, gitlab.ProviderType, "prod")
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
		env.storage,
		gitlab.ProviderType,
		"prod",
	)
	if err != nil {
		t.Fatalf("read gitlab sensitive config: %v", err)
	}
	if got := storedSensitiveConfig.Config[gitlab.ConfigKeyToken]; got != "glpat-secret" {
		t.Fatalf("stored gitlab token = %v, want glpat-secret", got)
	}

	readResp := env.read("destinations/gitlab/prod")
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

	validateResp := env.update("destinations/gitlab/prod/validate")
	assertNoErrorResponse(t, validateResp)
	assertResponseValue(t, validateResp, "valid", true)
	capabilities := validateResp.Data["capabilities"].(map[string]interface{})
	if got := capabilities["supports_secret_key"]; got != true {
		t.Fatalf("gitlab supports_secret_key = %v, want true", got)
	}
}

func TestDestinationSensitiveConfigDeletion(t *testing.T) {
	env := newBackendTestEnv(t)

	writeResp := env.update("destinations/aws-sm/prod", map[string]interface{}{
		awssecretsmanager.ConfigKeyAuthMode:   awssecretsmanager.AuthModeAssumeRole,
		awssecretsmanager.ConfigKeyRoleARN:    "arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync",
		awssecretsmanager.ConfigKeyExternalID: "tenant-1",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	clearResp := env.update("destinations/aws-sm/prod", map[string]interface{}{
		awssecretsmanager.ConfigKeyExternalID: "",
	})
	if clearResp != nil && clearResp.IsError() {
		t.Fatalf("unexpected destination update error: %v", clearResp.Error())
	}
	sensitiveConfig, err := getDestinationSensitiveConfig(
		context.Background(),
		env.storage,
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

func TestDestinationConfigResponseFiltersSensitiveKeys(t *testing.T) {
	response := destinationConfigResponse(awssecretsmanager.ProviderType, map[string]string{
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

	k8sResponse := destinationConfigResponse(kubernetessecrets.ProviderType, map[string]string{
		kubernetessecrets.ConfigKeyAPIServer: "https://kubernetes.example.com",
		kubernetessecrets.ConfigKeyToken:     "bearer-token",
	})
	if got := k8sResponse[kubernetessecrets.ConfigKeyAPIServer]; got != "https://kubernetes.example.com" {
		t.Fatalf("api_server = %v, want https://kubernetes.example.com", got)
	}
	if _, ok := k8sResponse[kubernetessecrets.ConfigKeyToken]; ok {
		t.Fatal("response must not include k8s token")
	}
}

func TestDestinationValidateAndHealth(t *testing.T) {
	env := newBackendTestEnv(t)

	env.createFakeDestination("primary")
	now := nowUTC().Format(timeFormatRFC3339)
	if err := putDestination(context.Background(), env.storage, destinationRecord{
		Type:        providerTypeFake,
		Name:        "invalid",
		CreatedTime: now,
		UpdatedTime: now,
	}); err != nil {
		t.Fatalf("write invalid destination fixture: %v", err)
	}
	env.createFakeDestination("unhealthy")

	validateResp := env.update("destinations/fake/primary/validate")
	assertNoErrorResponse(t, validateResp)
	assertResponseValue(t, validateResp, "valid", true)
	if _, ok := validateResp.Data["capabilities"]; !ok {
		t.Fatal("validate response must include capabilities")
	}

	invalidResp := env.update("destinations/fake/invalid/validate")
	assertNoErrorResponse(t, invalidResp)
	assertResponseValue(t, invalidResp, "valid", false)
	assertResponseValue(t, invalidResp, "error_class", string(providers.ErrorClassValidation))

	healthResp := env.read("destinations/fake/primary/health")
	assertNoErrorResponse(t, healthResp)
	assertResponseValue(t, healthResp, "healthy", true)

	unhealthyResp := env.read("destinations/fake/unhealthy/health")
	assertNoErrorResponse(t, unhealthyResp)
	assertResponseValue(t, unhealthyResp, "healthy", false)
	assertResponseValue(t, unhealthyResp, "error_class", string(providers.ErrorClassUnavailable))
}
