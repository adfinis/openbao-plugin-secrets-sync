package awssecretsmanager

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/providertest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/smithy-go"
)

const (
	testDestinationName = "prod"
	testResolvedName    = "prod/app/db"
	testAssociationID   = "assoc-1"
	testSourcePath      = "app/db"
	testObjectID        = "secret-path"
	testPayloadSHAOld   = "sha256:old"
	testPayloadSHANew   = "sha256:new"
	testRegion          = "eu-central-1"
	testEndpointURL     = "http://localhost:4566"
	testRoleARN         = "arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync"
	testPluginInstance  = "inst-test"
	testRestoreEpoch    = "epoch-test"
)

func TestProviderConformance(t *testing.T) {
	client := &mockSecretsManagerClient{
		listSecretsOutput:    &secretsmanager.ListSecretsOutput{},
		describeOutput:       ownedDescribeOutput(testPayloadSHAOld),
		putSecretValueOutput: &secretsmanager.PutSecretValueOutput{VersionId: aws.String("version-2")},
		tagResourceOutput:    &secretsmanager.TagResourceOutput{},
		deleteSecretOutput:   &secretsmanager.DeleteSecretOutput{ARN: aws.String("arn:aws:secretsmanager:test")},
	}
	providertest.Run(t, providertest.Harness{
		Provider:         Provider{client: client},
		ValidDestination: providers.DestinationConfig{Name: testDestinationName},
		RequiredCapabilities: providertest.CapabilityExpectations{
			MetadataReadback:    true,
			SecretPath:          true,
			UpdateIfOwned:       true,
			DeleteIfOwned:       true,
			PayloadHashMetadata: true,
			MinPayloadBytes:     secretValueMaxBytes,
		},
		ValidationError: &providertest.ValidationErrorCase{
			Destination: providers.DestinationConfig{Name: ""},
			ErrorClass:  providers.ErrorClassValidation,
		},
		HealthCase: &providertest.HealthCase{
			Destination: providers.DestinationConfig{Name: testDestinationName},
			Healthy:     true,
		},
		PlanCases: []providertest.PlanCase{
			{
				Name:       "update",
				Request:    defaultPlanRequest(testPayloadSHANew),
				Action:     providers.PlanActionUpdate,
				ErrorClass: "",
			},
		},
		UpsertSuccess: &providertest.UpsertCase{
			Request:       defaultUpsertRequest(),
			RemoteVersion: "version-2",
		},
		DeleteSuccess: &providertest.DeleteCase{
			Request:       defaultDeleteRequest(),
			RemoteVersion: "arn:aws:secretsmanager:test",
		},
		ReadStateCase: &providertest.ReadStateCase{
			Request:        defaultReadStateRequest(),
			Exists:         true,
			OwnershipKnown: true,
			Owned:          true,
			PayloadSHA256:  testPayloadSHAOld,
			SourceVersion:  1,
			RemoteVersion:  "current-version",
		},
		Maturity: awsMaturityMatrix(),
	})
}

func TestValidateDestinationConfig(t *testing.T) {
	tests := []struct {
		name       string
		config     map[string]string
		errorClass providers.ErrorClass
	}{
		{
			name: "default auth",
			config: map[string]string{
				ConfigKeyRegion:         testRegion,
				ConfigKeyEndpointURL:    testEndpointURL,
				ConfigKeyEndpointPolicy: EndpointPolicyLocal,
			},
		},
		{
			name: "private endpoint",
			config: map[string]string{
				ConfigKeyRegion:         testRegion,
				ConfigKeyEndpointURL:    "https://10.0.0.5",
				ConfigKeyEndpointPolicy: EndpointPolicyPrivate,
			},
		},
		{
			name: "assume role auth",
			config: map[string]string{
				ConfigKeyAuthMode:    AuthModeAssumeRole,
				ConfigKeyRegion:      testRegion,
				ConfigKeyRoleARN:     testRoleARN,
				ConfigKeyExternalID:  "tenant-1",
				ConfigKeySessionName: "openbao-sync",
			},
		},
		{
			name: "unsupported static auth",
			config: map[string]string{
				ConfigKeyAuthMode: AuthModeStatic,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "static fields require static auth",
			config: map[string]string{
				ConfigKeyAccessKeyID: "AKIATEST",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "assume role missing role arn",
			config: map[string]string{
				ConfigKeyAuthMode: AuthModeAssumeRole,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "default auth rejects role fields",
			config: map[string]string{
				ConfigKeyRoleARN: testRoleARN,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "invalid endpoint scheme",
			config: map[string]string{
				ConfigKeyEndpointURL:    "ftp://localhost",
				ConfigKeyEndpointPolicy: EndpointPolicyLocal,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "endpoint requires explicit policy",
			config: map[string]string{
				ConfigKeyEndpointURL: testEndpointURL,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "endpoint policy requires endpoint",
			config: map[string]string{
				ConfigKeyEndpointPolicy: EndpointPolicyLocal,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "endpoint userinfo rejected",
			config: map[string]string{
				ConfigKeyEndpointURL:    "https://user@example.com",
				ConfigKeyEndpointPolicy: EndpointPolicyPrivate,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "private endpoint rejects http",
			config: map[string]string{
				ConfigKeyEndpointURL:    "http://10.0.0.5",
				ConfigKeyEndpointPolicy: EndpointPolicyPrivate,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "private endpoint rejects loopback",
			config: map[string]string{
				ConfigKeyEndpointURL:    "https://127.0.0.1:4566",
				ConfigKeyEndpointPolicy: EndpointPolicyPrivate,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "custom delete recovery window",
			config: map[string]string{
				ConfigKeyRegion:                   testRegion,
				ConfigKeyDeleteRecoveryWindowDays: "30",
			},
		},
		{
			name: "invalid delete recovery window",
			config: map[string]string{
				ConfigKeyDeleteRecoveryWindowDays: "six",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "delete recovery window below AWS minimum",
			config: map[string]string{
				ConfigKeyDeleteRecoveryWindowDays: "6",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "delete recovery window above AWS maximum",
			config: map[string]string{
				ConfigKeyDeleteRecoveryWindowDays: "31",
			},
			errorClass: providers.ErrorClassValidation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Provider{}).ValidateConfig(context.Background(), providers.DestinationConfig{
				Name:   testDestinationName,
				Config: tt.config,
			})
			if tt.errorClass == "" {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				return
			}
			assertProviderErrorClass(t, err, tt.errorClass)
		})
	}
}

func TestValidatePrivateEndpointResolution(t *testing.T) {
	tests := []struct {
		name       string
		options    awsDestinationOptions
		resolve    endpointResolver
		errorClass providers.ErrorClass
	}{
		{
			name: "private hostname resolves to routable address",
			options: awsDestinationOptions{
				endpointURL:    "https://vpce-1234567890abcdef.secretsmanager.eu-central-1.vpce.amazonaws.com",
				endpointPolicy: EndpointPolicyPrivate,
			},
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
			},
		},
		{
			name: "private hostname rejects loopback resolution",
			options: awsDestinationOptions{
				endpointURL:    "https://vpce-1234567890abcdef.secretsmanager.eu-central-1.vpce.amazonaws.com",
				endpointPolicy: EndpointPolicyPrivate,
			},
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "private hostname DNS failure is unavailable",
			options: awsDestinationOptions{
				endpointURL:    "https://vpce-1234567890abcdef.secretsmanager.eu-central-1.vpce.amazonaws.com",
				endpointPolicy: EndpointPolicyPrivate,
			},
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return nil, errors.New("lookup failed")
			},
			errorClass: providers.ErrorClassUnavailable,
		},
		{
			name: "local policy skips DNS guard",
			options: awsDestinationOptions{
				endpointURL:    testEndpointURL,
				endpointPolicy: EndpointPolicyLocal,
			},
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEndpointResolution(context.Background(), tt.options, tt.resolve)
			if tt.errorClass == "" {
				if err != nil {
					t.Fatalf("validate endpoint resolution: %v", err)
				}
				return
			}
			assertProviderErrorClass(t, err, tt.errorClass)
		})
	}
}

func TestDefaultAWSHTTPClientIsBoundedAndProxyFree(t *testing.T) {
	client := defaultAWSHTTPClient()
	if client.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %s, want %s", client.Timeout, defaultHTTPTimeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default AWS HTTP client must not use ambient proxy configuration")
	}
}

func TestPlanActions(t *testing.T) {
	tests := []struct {
		name           string
		describeOutput *secretsmanager.DescribeSecretOutput
		describeError  error
		payloadSHA256  string
		action         string
		errorClass     providers.ErrorClass
	}{
		{
			name:          "create missing secret",
			describeError: apiError("ResourceNotFoundException"),
			action:        providers.PlanActionCreate,
		},
		{
			name:           "noop owned matching hash",
			describeOutput: ownedDescribeOutput("sha256:same"),
			payloadSHA256:  "sha256:same",
			action:         providers.PlanActionNoop,
		},
		{
			name:           "update owned different hash",
			describeOutput: ownedDescribeOutput(testPayloadSHAOld),
			payloadSHA256:  testPayloadSHANew,
			action:         providers.PlanActionUpdate,
		},
		{
			name:           "update owned matching hash with stale metadata",
			describeOutput: ownedDescribeOutputAtVersion(testPayloadSHANew, 0),
			payloadSHA256:  testPayloadSHANew,
			action:         providers.PlanActionUpdate,
		},
		{
			name:           "update owned scheduled delete",
			describeOutput: ownedDeletedDescribeOutput(testPayloadSHANew),
			payloadSHA256:  testPayloadSHANew,
			action:         providers.PlanActionUpdate,
		},
		{
			name:           "blocked newer remote source version",
			describeOutput: ownedDescribeOutputAtVersion(testPayloadSHAOld, 2),
			payloadSHA256:  testPayloadSHANew,
			action:         providers.PlanActionBlocked,
			errorClass:     providers.ErrorClassDrift,
		},
		{
			name:           "conflict unowned secret",
			describeOutput: &secretsmanager.DescribeSecretOutput{Name: aws.String(testResolvedName)},
			payloadSHA256:  testPayloadSHANew,
			action:         providers.PlanActionConflict,
			errorClass:     providers.ErrorClassCollision,
		},
		{
			name: "conflict unowned scheduled delete",
			describeOutput: &secretsmanager.DescribeSecretOutput{
				Name:        aws.String(testResolvedName),
				DeletedDate: aws.Time(time.Unix(1700000000, 0)),
			},
			payloadSHA256: testPayloadSHANew,
			action:        providers.PlanActionConflict,
			errorClass:    providers.ErrorClassCollision,
		},
		{
			name:          "blocked rate limit",
			describeError: apiError("ThrottlingException"),
			action:        providers.PlanActionBlocked,
			errorClass:    providers.ErrorClassRateLimit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockSecretsManagerClient{
				describeOutput: tt.describeOutput,
				describeError:  tt.describeError,
			}
			result, err := runtimeWithClient(t, client).Plan(context.Background(), defaultPlanRequest(tt.payloadSHA256))
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if result.Action != tt.action {
				t.Fatalf("action = %s, want %s", result.Action, tt.action)
			}
			if result.ErrorClass != tt.errorClass {
				t.Fatalf("error class = %s, want %s", result.ErrorClass, tt.errorClass)
			}
		})
	}
}

func TestReadStateReportsRemoteMetadata(t *testing.T) {
	tests := []struct {
		name           string
		describeOutput *secretsmanager.DescribeSecretOutput
		describeError  error
		exists         bool
		ownershipKnown bool
		owned          bool
		payloadSHA256  string
		sourceVersion  int
		remoteVersion  string
		errorClass     providers.ErrorClass
	}{
		{
			name:           "owned existing secret",
			describeOutput: ownedDescribeOutputAtVersion(testPayloadSHAOld, 2),
			exists:         true,
			ownershipKnown: true,
			owned:          true,
			payloadSHA256:  testPayloadSHAOld,
			sourceVersion:  2,
			remoteVersion:  "current-version",
		},
		{
			name:          "missing secret",
			describeError: apiError("ResourceNotFoundException"),
			exists:        false,
		},
		{
			name: "unowned existing secret",
			describeOutput: &secretsmanager.DescribeSecretOutput{
				Name: aws.String(testResolvedName),
			},
			exists:         true,
			ownershipKnown: true,
			owned:          false,
		},
		{
			name: "deleted secret",
			describeOutput: &secretsmanager.DescribeSecretOutput{
				Name:        aws.String(testResolvedName),
				DeletedDate: aws.Time(time.Unix(1700000000, 0)),
			},
			exists: false,
		},
		{
			name:          "auth failure",
			describeError: apiError("UnrecognizedClientException"),
			errorClass:    providers.ErrorClassAuthn,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockSecretsManagerClient{
				describeOutput: tt.describeOutput,
				describeError:  tt.describeError,
			}
			state, err := runtimeWithClient(t, client).ReadState(context.Background(), defaultReadStateRequest())
			if tt.errorClass != "" {
				assertProviderErrorClass(t, err, tt.errorClass)
				return
			}
			if err != nil {
				t.Fatalf("read state: %v", err)
			}
			if state.Exists != tt.exists {
				t.Fatalf("exists = %v, want %v", state.Exists, tt.exists)
			}
			if state.OwnershipKnown != tt.ownershipKnown {
				t.Fatalf("ownership known = %v, want %v", state.OwnershipKnown, tt.ownershipKnown)
			}
			if state.Owned != tt.owned {
				t.Fatalf("owned = %v, want %v", state.Owned, tt.owned)
			}
			if state.PayloadSHA256 != tt.payloadSHA256 {
				t.Fatalf("payload sha = %q, want %q", state.PayloadSHA256, tt.payloadSHA256)
			}
			if state.SourceVersion != tt.sourceVersion {
				t.Fatalf("source version = %d, want %d", state.SourceVersion, tt.sourceVersion)
			}
			if state.RemoteVersion != tt.remoteVersion {
				t.Fatalf("remote version = %q, want %q", state.RemoteVersion, tt.remoteVersion)
			}
		})
	}
}

func TestUpsertCreatesMissingSecretWithOwnershipTags(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeError:      apiError("ResourceNotFoundException"),
		createSecretOutput: &secretsmanager.CreateSecretOutput{VersionId: aws.String("created-version")},
	}
	result, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.RemoteVersion != "created-version" {
		t.Fatalf("remote version = %s, want created-version", result.RemoteVersion)
	}
	if client.createSecretInput == nil {
		t.Fatal("CreateSecret input must be captured")
	}
	if got := aws.ToString(client.createSecretInput.Name); got != testResolvedName {
		t.Fatalf("create name = %s, want %s", got, testResolvedName)
	}
	if got := aws.ToString(client.createSecretInput.SecretString); got != `{"password":"secret"}` {
		t.Fatalf("secret string = %s, want payload", got)
	}
	assertTag(t, client.createSecretInput.Tags, tagAssociationID, testAssociationID)
	assertTag(t, client.createSecretInput.Tags, tagSourcePath, testSourcePath)
	assertTag(t, client.createSecretInput.Tags, tagObjectID, testObjectID)
	assertTag(t, client.createSecretInput.Tags, tagPayloadSHA256, testPayloadSHANew)
	assertTag(t, client.createSecretInput.Tags, tagPluginInstance, testPluginInstance)
	assertTag(t, client.createSecretInput.Tags, tagRestoreEpoch, testRestoreEpoch)
}

func TestUpsertUpdatesOwnedSecretAndTagsHash(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput:       ownedDescribeOutput(testPayloadSHAOld),
		putSecretValueOutput: &secretsmanager.PutSecretValueOutput{VersionId: aws.String("updated-version")},
		tagResourceOutput:    &secretsmanager.TagResourceOutput{},
	}
	result, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.RemoteVersion != "updated-version" {
		t.Fatalf("remote version = %s, want updated-version", result.RemoteVersion)
	}
	if client.putSecretValueInput == nil {
		t.Fatal("PutSecretValue input must be captured")
	}
	if got := aws.ToString(client.putSecretValueInput.SecretId); got != testResolvedName {
		t.Fatalf("secret id = %s, want %s", got, testResolvedName)
	}
	if client.tagResourceInput == nil {
		t.Fatal("TagResource input must be captured")
	}
	assertTag(t, client.tagResourceInput.Tags, tagPayloadSHA256, testPayloadSHANew)
	assertTag(t, client.tagResourceInput.Tags, tagPluginInstance, testPluginInstance)
	assertTag(t, client.tagResourceInput.Tags, tagRestoreEpoch, testRestoreEpoch)
}

func TestUpsertRestoresOwnedScheduledDeleteBeforeUpdate(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput:       ownedDeletedDescribeOutput(testPayloadSHAOld),
		restoreSecretOutput:  &secretsmanager.RestoreSecretOutput{ARN: aws.String("arn:aws:secretsmanager:test")},
		putSecretValueOutput: &secretsmanager.PutSecretValueOutput{VersionId: aws.String("restored-version")},
		tagResourceOutput:    &secretsmanager.TagResourceOutput{},
	}
	result, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.RemoteVersion != "restored-version" {
		t.Fatalf("remote version = %s, want restored-version", result.RemoteVersion)
	}
	if client.restoreSecretInput == nil {
		t.Fatal("RestoreSecret input must be captured")
	}
	if got := aws.ToString(client.restoreSecretInput.SecretId); got != testResolvedName {
		t.Fatalf("restore secret id = %s, want %s", got, testResolvedName)
	}
	if client.putSecretValueInput == nil {
		t.Fatal("PutSecretValue must be called after restoring scheduled-delete secret")
	}
	if client.tagResourceInput == nil {
		t.Fatal("TagResource must refresh ownership metadata after restoring scheduled-delete secret")
	}
	assertTag(t, client.tagResourceInput.Tags, tagSourceVersion, "1")
	assertTag(t, client.tagResourceInput.Tags, tagPayloadSHA256, testPayloadSHANew)
}

func TestUpsertRestoresOwnedScheduledDeleteWithMatchingPayload(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput:      ownedDeletedDescribeOutputAtVersion(testPayloadSHANew, 0),
		restoreSecretOutput: &secretsmanager.RestoreSecretOutput{ARN: aws.String("arn:aws:secretsmanager:test")},
		tagResourceOutput:   &secretsmanager.TagResourceOutput{},
	}
	result, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.RemoteVersion != "current-version" {
		t.Fatalf("remote version = %s, want current-version", result.RemoteVersion)
	}
	if client.restoreSecretInput == nil {
		t.Fatal("RestoreSecret input must be captured")
	}
	if client.putSecretValueInput != nil {
		t.Fatal("PutSecretValue must not be called when restored payload already matches")
	}
	if client.tagResourceInput == nil {
		t.Fatal("TagResource must refresh stale source metadata")
	}
	assertTag(t, client.tagResourceInput.Tags, tagSourceVersion, "1")
}

func TestUpsertRejectsUnownedSecret(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: &secretsmanager.DescribeSecretOutput{Name: aws.String(testResolvedName)},
	}
	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassOwnership)
	if client.putSecretValueInput != nil {
		t.Fatal("PutSecretValue must not be called for unowned secret")
	}
}

func TestUpsertRejectsUnownedScheduledDelete(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: &secretsmanager.DescribeSecretOutput{
			Name:        aws.String(testResolvedName),
			DeletedDate: aws.Time(time.Unix(1700000000, 0)),
		},
	}
	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassOwnership)
	if client.restoreSecretInput != nil {
		t.Fatal("RestoreSecret must not be called for unowned scheduled-delete secret")
	}
	if client.putSecretValueInput != nil {
		t.Fatal("PutSecretValue must not be called for unowned scheduled-delete secret")
	}
}

func TestOwnedByRequestRejectsRuntimeIdentityMismatch(t *testing.T) {
	request := defaultUpsertRequest()
	identity := ownershipIdentityFromUpsert(request)
	tags := ownershipTagsFromUpsert(request)
	if !ownedByRequest(tags, identity) {
		t.Fatalf("ownedByRequest returned false for matching runtime identity")
	}
	for index := range tags {
		if aws.ToString(tags[index].Key) == tagPluginInstance {
			tags[index].Value = aws.String("inst-other")
		}
	}
	if ownedByRequest(tags, identity) {
		t.Fatal("ownedByRequest returned true for mismatched plugin instance")
	}
}

func TestUpsertRejectsNewerRemoteSourceVersion(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: ownedDescribeOutputAtVersion(testPayloadSHAOld, 2),
	}
	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassDrift)
	if client.putSecretValueInput != nil {
		t.Fatal("PutSecretValue must not be called for newer remote source version")
	}
}

func TestUpsertClassifiesAWSMutationFailures(t *testing.T) {
	tests := []struct {
		name          string
		client        *mockSecretsManagerClient
		expectedClass providers.ErrorClass
		expectCreate  bool
		expectPut     bool
		expectTag     bool
	}{
		{
			name: "describe auth failure",
			client: &mockSecretsManagerClient{
				describeError: apiError("UnrecognizedClientException"),
			},
			expectedClass: providers.ErrorClassAuthn,
		},
		{
			name: "create throttled",
			client: &mockSecretsManagerClient{
				describeError:     apiError("ResourceNotFoundException"),
				createSecretError: apiError("ThrottlingException"),
			},
			expectedClass: providers.ErrorClassRateLimit,
			expectCreate:  true,
		},
		{
			name: "put throttled",
			client: &mockSecretsManagerClient{
				describeOutput:      ownedDescribeOutput(testPayloadSHAOld),
				putSecretValueError: apiError("TooManyRequestsException"),
			},
			expectedClass: providers.ErrorClassRateLimit,
			expectPut:     true,
		},
		{
			name: "tag authorization failure",
			client: &mockSecretsManagerClient{
				describeOutput:       ownedDescribeOutput(testPayloadSHAOld),
				putSecretValueOutput: &secretsmanager.PutSecretValueOutput{VersionId: aws.String("version-2")},
				tagResourceError:     apiError("AccessDeniedException"),
			},
			expectedClass: providers.ErrorClassAuthz,
			expectPut:     true,
			expectTag:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runtimeWithClient(t, tt.client).Upsert(context.Background(), defaultUpsertRequest())
			assertProviderErrorClass(t, err, tt.expectedClass)
			if got := tt.client.createSecretInput != nil; got != tt.expectCreate {
				t.Fatalf("CreateSecret called = %v, want %v", got, tt.expectCreate)
			}
			if got := tt.client.putSecretValueInput != nil; got != tt.expectPut {
				t.Fatalf("PutSecretValue called = %v, want %v", got, tt.expectPut)
			}
			if got := tt.client.tagResourceInput != nil; got != tt.expectTag {
				t.Fatalf("TagResource called = %v, want %v", got, tt.expectTag)
			}
		})
	}
}

func TestDeleteUsesOwnedRecoveryWindow(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput:     ownedDescribeOutput(testPayloadSHAOld),
		deleteSecretOutput: &secretsmanager.DeleteSecretOutput{ARN: aws.String("arn:aws:secretsmanager:test")},
	}
	request := defaultDeleteRequest()
	cfg := defaultDestinationConfig()
	cfg.Config[ConfigKeyDeleteRecoveryWindowDays] = "14"
	result, err := runtimeWithDestination(t, Provider{client: client}, cfg).Delete(context.Background(), request)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.RemoteVersion != "arn:aws:secretsmanager:test" {
		t.Fatalf("remote version = %s, want arn", result.RemoteVersion)
	}
	if client.deleteSecretInput == nil {
		t.Fatal("DeleteSecret input must be captured")
	}
	if got := aws.ToString(client.deleteSecretInput.SecretId); got != testResolvedName {
		t.Fatalf("delete secret id = %s, want %s", got, testResolvedName)
	}
	if got := aws.ToInt64(client.deleteSecretInput.RecoveryWindowInDays); got != 14 {
		t.Fatalf("recovery window = %d, want 14", got)
	}
}

func TestDeleteRejectsUnownedSecret(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: &secretsmanager.DescribeSecretOutput{Name: aws.String(testResolvedName)},
	}
	_, err := runtimeWithClient(t, client).Delete(context.Background(), defaultDeleteRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassOwnership)
	if client.deleteSecretInput != nil {
		t.Fatal("DeleteSecret must not be called for unowned secret")
	}
}

func TestDeleteRejectsUnownedScheduledDelete(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: &secretsmanager.DescribeSecretOutput{
			Name:        aws.String(testResolvedName),
			DeletedDate: aws.Time(time.Unix(1700000000, 0)),
		},
	}
	_, err := runtimeWithClient(t, client).Delete(context.Background(), defaultDeleteRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassOwnership)
	if client.deleteSecretInput != nil {
		t.Fatal("DeleteSecret must not be called for unowned scheduled-delete secret")
	}
}

func TestDeleteRejectsNewerRemoteSourceVersion(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: ownedDescribeOutputAtVersion(testPayloadSHAOld, 2),
	}
	_, err := runtimeWithClient(t, client).Delete(context.Background(), defaultDeleteRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassDrift)
	if client.deleteSecretInput != nil {
		t.Fatal("DeleteSecret must not be called for newer remote source version")
	}
}

func TestDeleteClassifiesAWSMutationFailures(t *testing.T) {
	tests := []struct {
		name          string
		client        *mockSecretsManagerClient
		expectedClass providers.ErrorClass
		expectDelete  bool
	}{
		{
			name: "describe authorization failure",
			client: &mockSecretsManagerClient{
				describeError: apiError("AccessDeniedException"),
			},
			expectedClass: providers.ErrorClassAuthz,
		},
		{
			name: "delete throttled",
			client: &mockSecretsManagerClient{
				describeOutput:    ownedDescribeOutput(testPayloadSHAOld),
				deleteSecretError: apiError("ThrottlingException"),
			},
			expectedClass: providers.ErrorClassRateLimit,
			expectDelete:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runtimeWithClient(t, tt.client).Delete(context.Background(), defaultDeleteRequest())
			assertProviderErrorClass(t, err, tt.expectedClass)
			if got := tt.client.deleteSecretInput != nil; got != tt.expectDelete {
				t.Fatalf("DeleteSecret called = %v, want %v", got, tt.expectDelete)
			}
		})
	}
}

func TestHealthClassifiesAWSFailure(t *testing.T) {
	client := &mockSecretsManagerClient{listSecretsError: apiError("AccessDeniedException")}
	result, err := runtimeWithClient(t, client).Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if result.Healthy {
		t.Fatal("health must be unhealthy on AWS failure")
	}
	if result.ErrorClass != providers.ErrorClassAuthz {
		t.Fatalf("health error class = %s, want %s", result.ErrorClass, providers.ErrorClassAuthz)
	}
}

func TestHealthClassifiesDestinationValidationFailure(t *testing.T) {
	provider := Provider{
		clientFactory: func(context.Context, providers.DestinationConfig) (secretsManagerClient, error) {
			return nil, validationError("invalid destination config")
		},
	}
	_, err := provider.OpenDestination(context.Background(), providers.DestinationConfig{Name: testDestinationName})
	assertProviderErrorClass(t, err, providers.ErrorClassValidation)
}

func TestHealthPassesDestinationConfigToFactory(t *testing.T) {
	client := &mockSecretsManagerClient{listSecretsOutput: &secretsmanager.ListSecretsOutput{}}
	var capturedConfig providers.DestinationConfig
	provider := Provider{
		clientFactory: func(_ context.Context, cfg providers.DestinationConfig) (secretsManagerClient, error) {
			capturedConfig = cfg
			return client, nil
		},
	}
	runtime := runtimeWithDestination(t, provider, providers.DestinationConfig{
		Name: testDestinationName,
		Config: map[string]string{
			ConfigKeyRegion: testRegion,
		},
	})
	result, err := runtime.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !result.Healthy {
		t.Fatalf("health healthy = %v, want true", result.Healthy)
	}
	if capturedConfig.Config[ConfigKeyRegion] != testRegion {
		t.Fatalf("captured region = %q, want %q", capturedConfig.Config[ConfigKeyRegion], testRegion)
	}
}

func TestErrorClassification(t *testing.T) {
	tests := map[string]struct {
		err      error
		expected providers.ErrorClass
	}{
		"ThrottlingException": {
			err:      apiError("ThrottlingException"),
			expected: providers.ErrorClassRateLimit,
		},
		"TooManyRequestsException": {
			err:      apiError("TooManyRequestsException"),
			expected: providers.ErrorClassRateLimit,
		},
		"InternalServiceError": {
			err:      apiError("InternalServiceError"),
			expected: providers.ErrorClassUnavailable,
		},
		"ServiceUnavailableException": {
			err:      apiError("ServiceUnavailableException"),
			expected: providers.ErrorClassUnavailable,
		},
		"context deadline exceeded": {
			err:      context.DeadlineExceeded,
			expected: providers.ErrorClassUnavailable,
		},
		"UnrecognizedClientException": {
			err:      apiError("UnrecognizedClientException"),
			expected: providers.ErrorClassAuthn,
		},
		"InvalidSignatureException": {
			err:      apiError("InvalidSignatureException"),
			expected: providers.ErrorClassAuthn,
		},
		"AccessDeniedException": {
			err:      apiError("AccessDeniedException"),
			expected: providers.ErrorClassAuthz,
		},
		"InvalidParameterException": {
			err:      apiError("InvalidParameterException"),
			expected: providers.ErrorClassValidation,
		},
		"ResourceExistsException": {
			err:      apiError("ResourceExistsException"),
			expected: providers.ErrorClassCollision,
		},
		"SomethingUnexpected": {
			err:      apiError("SomethingUnexpected"),
			expected: providers.ErrorClassInternal,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := classifyAWSError(tt.err); got != tt.expected {
				t.Fatalf("classify = %s, want %s", got, tt.expected)
			}
		})
	}
}

func awsMaturityMatrix() *providertest.MaturityMatrix {
	return &providertest.MaturityMatrix{
		OwnershipLoss: []providertest.MaturityCase{
			{
				Name: "upsert-unowned-secret",
				Provider: Provider{client: &mockSecretsManagerClient{
					describeOutput: &secretsmanager.DescribeSecretOutput{Name: aws.String(testResolvedName)},
				}},
				Operation:       providertest.OperationUpsert,
				UpsertRequest:   defaultUpsertRequest(),
				ErrorClass:      providers.ErrorClassOwnership,
				NoResultOnError: true,
			},
		},
		AuthFailure: providertest.MaturityCase{
			Name: "read-state-authn",
			Provider: Provider{client: &mockSecretsManagerClient{
				describeError: apiError("UnrecognizedClientException"),
			}},
			Operation:        providertest.OperationReadState,
			ReadStateRequest: defaultReadStateRequest(),
			ErrorClass:       providers.ErrorClassAuthn,
		},
		Throttling: providertest.MaturityCase{
			Name: "create-throttled",
			Provider: Provider{client: &mockSecretsManagerClient{
				describeError:     apiError("ResourceNotFoundException"),
				createSecretError: apiError("ThrottlingException"),
			}},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(),
			ErrorClass:      providers.ErrorClassRateLimit,
			NoResultOnError: true,
		},
		PayloadLimit: providertest.MaturityCase{
			Name:            "oversized-payload",
			Provider:        Provider{client: &mockSecretsManagerClient{}},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   oversizedAWSUpsertRequest(),
			ErrorClass:      providers.ErrorClassCapacity,
			NoResultOnError: true,
		},
		PartialSuccess: providertest.PartialSuccessCase{
			Name: "put-succeeds-tag-fails",
			Mode: providertest.PartialSuccessClassifiedFailure,
			Case: providertest.MaturityCase{
				Provider: Provider{client: &mockSecretsManagerClient{
					describeOutput:       ownedDescribeOutput(testPayloadSHAOld),
					putSecretValueOutput: &secretsmanager.PutSecretValueOutput{VersionId: aws.String("version-2")},
					tagResourceError:     apiError("AccessDeniedException"),
				}},
				Operation:       providertest.OperationUpsert,
				UpsertRequest:   defaultUpsertRequest(),
				ErrorClass:      providers.ErrorClassAuthz,
				NoResultOnError: true,
			},
		},
		StaleRemoteState: providertest.MaturityCase{
			Name: "newer-remote-source-version",
			Provider: Provider{client: &mockSecretsManagerClient{
				describeOutput: ownedDescribeOutputAtVersion(testPayloadSHAOld, 2),
			}},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(),
			ErrorClass:      providers.ErrorClassDrift,
			NoResultOnError: true,
		},
		DeleteSemantics: []providertest.MaturityCase{
			{
				Name: "missing-delete-is-idempotent",
				Provider: Provider{client: &mockSecretsManagerClient{
					describeError: apiError("ResourceNotFoundException"),
				}},
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultDeleteRequest(),
				RemoteVersion: "missing",
			},
			{
				Name: "owned-delete-uses-recovery-window",
				Provider: Provider{client: &mockSecretsManagerClient{
					describeOutput:     ownedDescribeOutput(testPayloadSHAOld),
					deleteSecretOutput: &secretsmanager.DeleteSecretOutput{ARN: aws.String("arn:aws:secretsmanager:test")},
				}},
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultDeleteRequest(),
				RemoteVersion: "arn:aws:secretsmanager:test",
			},
		},
	}
}

func defaultPlanRequest(payloadSHA256 string) providers.PlanRequest {
	return providers.PlanRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		PayloadSHA256: payloadSHA256,
		PayloadBytes:  21,
		SourcePath:    testSourcePath,
		SourceVersion: 1,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultUpsertRequest() providers.UpsertRequest {
	return providers.UpsertRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		Format:        "json",
		Payload:       []byte(`{"password":"secret"}`),
		PayloadSHA256: testPayloadSHANew,
		SourcePath:    testSourcePath,
		SourceVersion: 1,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func oversizedAWSUpsertRequest() providers.UpsertRequest {
	request := defaultUpsertRequest()
	request.Payload = make([]byte, secretValueMaxBytes+1)
	return request
}

func defaultDeleteRequest() providers.DeleteRequest {
	return providers.DeleteRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		SourcePath:    testSourcePath,
		SourceVersion: 1,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultReadStateRequest() providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		PayloadSHA256: testPayloadSHANew,
		SourcePath:    testSourcePath,
		SourceVersion: 1,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func runtimeWithClient(t *testing.T, client secretsManagerClient) providers.DestinationRuntime {
	t.Helper()
	return runtimeWithDestination(t, Provider{client: client}, defaultDestinationConfig())
}

func runtimeWithDestination(
	t *testing.T,
	provider Provider,
	destination providers.DestinationConfig,
) providers.DestinationRuntime {
	t.Helper()
	runtime, err := provider.OpenDestination(context.Background(), destination)
	if err != nil {
		t.Fatalf("open destination runtime: %v", err)
	}
	if runtime == nil {
		t.Fatal("destination runtime must not be nil")
	}
	return runtime
}

func defaultRuntimeIdentity() providers.RuntimeIdentity {
	return providers.RuntimeIdentity{
		PluginInstanceID: testPluginInstance,
		RestoreEpoch:     testRestoreEpoch,
	}
}

func defaultDestinationConfig() providers.DestinationConfig {
	return providers.DestinationConfig{
		Name: testDestinationName,
		Config: map[string]string{
			ConfigKeyRegion: testRegion,
		},
	}
}

func ownedDescribeOutput(payloadSHA256 string) *secretsmanager.DescribeSecretOutput {
	return ownedDescribeOutputAtVersion(payloadSHA256, 1)
}

func ownedDeletedDescribeOutput(payloadSHA256 string) *secretsmanager.DescribeSecretOutput {
	return ownedDeletedDescribeOutputAtVersion(payloadSHA256, 1)
}

func ownedDeletedDescribeOutputAtVersion(
	payloadSHA256 string,
	sourceVersion int,
) *secretsmanager.DescribeSecretOutput {
	output := ownedDescribeOutputAtVersion(payloadSHA256, sourceVersion)
	output.DeletedDate = aws.Time(time.Unix(1700000000, 0))
	return output
}

func ownedDescribeOutputAtVersion(
	payloadSHA256 string,
	sourceVersion int,
) *secretsmanager.DescribeSecretOutput {
	return &secretsmanager.DescribeSecretOutput{
		Name: aws.String(testResolvedName),
		Tags: []smtypes.Tag{
			tag(tagManaged, "true"),
			tag(tagAssociationID, testAssociationID),
			tag(tagSourcePath, testSourcePath),
			tag(tagSourceVersion, strconv.Itoa(sourceVersion)),
			tag(tagObjectID, testObjectID),
			tag(tagPayloadSHA256, payloadSHA256),
			tag(tagPluginInstance, testPluginInstance),
			tag(tagRestoreEpoch, testRestoreEpoch),
		},
		VersionIdsToStages: map[string][]string{
			"current-version": {"AWSCURRENT"},
		},
	}
}

func assertProviderErrorClass(t *testing.T, err error, expected providers.ErrorClass) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", expected)
	}
	providerError, ok := err.(*providers.Error)
	if !ok {
		t.Fatalf("error = %T, want *providers.Error", err)
	}
	if providerError.Class != expected {
		t.Fatalf("error class = %s, want %s", providerError.Class, expected)
	}
}

func assertTag(t *testing.T, tags []smtypes.Tag, key string, expected string) {
	t.Helper()
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			if got := aws.ToString(tag.Value); got != expected {
				t.Fatalf("tag %s = %s, want %s", key, got, expected)
			}
			return
		}
	}
	t.Fatalf("tag %s not found in %#v", key, tags)
}

func apiError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code}
}

type mockSecretsManagerClient struct {
	describeOutput *secretsmanager.DescribeSecretOutput
	describeError  error

	createSecretInput  *secretsmanager.CreateSecretInput
	createSecretOutput *secretsmanager.CreateSecretOutput
	createSecretError  error

	putSecretValueInput  *secretsmanager.PutSecretValueInput
	putSecretValueOutput *secretsmanager.PutSecretValueOutput
	putSecretValueError  error

	deleteSecretInput  *secretsmanager.DeleteSecretInput
	deleteSecretOutput *secretsmanager.DeleteSecretOutput
	deleteSecretError  error

	restoreSecretInput  *secretsmanager.RestoreSecretInput
	restoreSecretOutput *secretsmanager.RestoreSecretOutput
	restoreSecretError  error

	tagResourceInput  *secretsmanager.TagResourceInput
	tagResourceOutput *secretsmanager.TagResourceOutput
	tagResourceError  error

	listSecretsOutput *secretsmanager.ListSecretsOutput
	listSecretsError  error
}

func (m *mockSecretsManagerClient) DescribeSecret(
	context.Context,
	*secretsmanager.DescribeSecretInput,
	...func(*secretsmanager.Options),
) (*secretsmanager.DescribeSecretOutput, error) {
	return m.describeOutput, m.describeError
}

func (m *mockSecretsManagerClient) CreateSecret(
	_ context.Context,
	input *secretsmanager.CreateSecretInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.CreateSecretOutput, error) {
	m.createSecretInput = input
	return m.createSecretOutput, m.createSecretError
}

func (m *mockSecretsManagerClient) PutSecretValue(
	_ context.Context,
	input *secretsmanager.PutSecretValueInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.PutSecretValueOutput, error) {
	m.putSecretValueInput = input
	return m.putSecretValueOutput, m.putSecretValueError
}

func (m *mockSecretsManagerClient) DeleteSecret(
	_ context.Context,
	input *secretsmanager.DeleteSecretInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.DeleteSecretOutput, error) {
	m.deleteSecretInput = input
	return m.deleteSecretOutput, m.deleteSecretError
}

func (m *mockSecretsManagerClient) RestoreSecret(
	_ context.Context,
	input *secretsmanager.RestoreSecretInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.RestoreSecretOutput, error) {
	m.restoreSecretInput = input
	return m.restoreSecretOutput, m.restoreSecretError
}

func (m *mockSecretsManagerClient) TagResource(
	_ context.Context,
	input *secretsmanager.TagResourceInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.TagResourceOutput, error) {
	m.tagResourceInput = input
	return m.tagResourceOutput, m.tagResourceError
}

func (m *mockSecretsManagerClient) ListSecrets(
	context.Context,
	*secretsmanager.ListSecretsInput,
	...func(*secretsmanager.Options),
) (*secretsmanager.ListSecretsOutput, error) {
	return m.listSecretsOutput, m.listSecretsError
}
