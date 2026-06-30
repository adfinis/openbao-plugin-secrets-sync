package awssecretsmanager

import (
	"context"
	"strconv"
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/providertest"
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
	testRoleARN         = "arn:aws:iam::123456789012:role/openbao-secret-sync"
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
			Request: providers.ReadStateRequest{ResolvedName: testResolvedName},
			Exists:  true,
		},
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Provider{}).Validate(context.Background(), providers.DestinationConfig{
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
			result, err := (Provider{client: client}).Plan(context.Background(), defaultPlanRequest(tt.payloadSHA256))
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

func TestUpsertCreatesMissingSecretWithOwnershipTags(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeError:      apiError("ResourceNotFoundException"),
		createSecretOutput: &secretsmanager.CreateSecretOutput{VersionId: aws.String("created-version")},
	}
	result, err := (Provider{client: client}).Upsert(context.Background(), defaultUpsertRequest())
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
}

func TestUpsertUpdatesOwnedSecretAndTagsHash(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput:       ownedDescribeOutput(testPayloadSHAOld),
		putSecretValueOutput: &secretsmanager.PutSecretValueOutput{VersionId: aws.String("updated-version")},
		tagResourceOutput:    &secretsmanager.TagResourceOutput{},
	}
	result, err := (Provider{client: client}).Upsert(context.Background(), defaultUpsertRequest())
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
}

func TestUpsertRejectsUnownedSecret(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: &secretsmanager.DescribeSecretOutput{Name: aws.String(testResolvedName)},
	}
	_, err := (Provider{client: client}).Upsert(context.Background(), defaultUpsertRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassOwnership)
}

func TestUpsertRejectsNewerRemoteSourceVersion(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: ownedDescribeOutputAtVersion(testPayloadSHAOld, 2),
	}
	_, err := (Provider{client: client}).Upsert(context.Background(), defaultUpsertRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassDrift)
	if client.putSecretValueInput != nil {
		t.Fatal("PutSecretValue must not be called for newer remote source version")
	}
}

func TestDeleteUsesOwnedRecoveryWindow(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput:     ownedDescribeOutput(testPayloadSHAOld),
		deleteSecretOutput: &secretsmanager.DeleteSecretOutput{ARN: aws.String("arn:aws:secretsmanager:test")},
	}
	result, err := (Provider{client: client}).Delete(context.Background(), defaultDeleteRequest())
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
	if got := aws.ToInt64(client.deleteSecretInput.RecoveryWindowInDays); got != defaultDeleteRecoveryWindowDays {
		t.Fatalf("recovery window = %d, want %d", got, defaultDeleteRecoveryWindowDays)
	}
}

func TestDeleteRejectsNewerRemoteSourceVersion(t *testing.T) {
	client := &mockSecretsManagerClient{
		describeOutput: ownedDescribeOutputAtVersion(testPayloadSHAOld, 2),
	}
	_, err := (Provider{client: client}).Delete(context.Background(), defaultDeleteRequest())
	assertProviderErrorClass(t, err, providers.ErrorClassDrift)
	if client.deleteSecretInput != nil {
		t.Fatal("DeleteSecret must not be called for newer remote source version")
	}
}

func TestHealthClassifiesAWSFailure(t *testing.T) {
	client := &mockSecretsManagerClient{listSecretsError: apiError("AccessDeniedException")}
	result, err := (Provider{client: client}).Health(
		context.Background(),
		providers.DestinationConfig{Name: testDestinationName},
	)
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
	result, err := provider.Health(context.Background(), providers.DestinationConfig{Name: testDestinationName})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if result.Healthy {
		t.Fatal("health must be unhealthy on validation failure")
	}
	if result.ErrorClass != providers.ErrorClassValidation {
		t.Fatalf("health error class = %s, want %s", result.ErrorClass, providers.ErrorClassValidation)
	}
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
	result, err := provider.Health(context.Background(), providers.DestinationConfig{
		Name: testDestinationName,
		Config: map[string]string{
			ConfigKeyRegion: testRegion,
		},
	})
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
	tests := map[string]providers.ErrorClass{
		"ThrottlingException":         providers.ErrorClassRateLimit,
		"TooManyRequestsException":    providers.ErrorClassRateLimit,
		"InternalServiceError":        providers.ErrorClassUnavailable,
		"ServiceUnavailableException": providers.ErrorClassUnavailable,
		"UnrecognizedClientException": providers.ErrorClassAuthn,
		"InvalidSignatureException":   providers.ErrorClassAuthn,
		"AccessDeniedException":       providers.ErrorClassAuthz,
		"InvalidParameterException":   providers.ErrorClassValidation,
		"ResourceExistsException":     providers.ErrorClassCollision,
		"SomethingUnexpected":         providers.ErrorClassInternal,
	}
	for code, expected := range tests {
		t.Run(code, func(t *testing.T) {
			if got := classifyAWSError(apiError(code)); got != expected {
				t.Fatalf("classify = %s, want %s", got, expected)
			}
		})
	}
}

func defaultPlanRequest(payloadSHA256 string) providers.PlanRequest {
	return providers.PlanRequest{
		Destination:   defaultDestinationConfig(),
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
		Destination:   defaultDestinationConfig(),
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

func defaultDeleteRequest() providers.DeleteRequest {
	return providers.DeleteRequest{
		Destination:   defaultDestinationConfig(),
		ResolvedName:  testResolvedName,
		SourcePath:    testSourcePath,
		SourceVersion: 1,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
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
