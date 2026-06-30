// Package awssecretsmanager provides the AWS Secrets Manager destination provider.
package awssecretsmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/smithy-go"
)

const (
	// ProviderType is the stable destination type used by associations.
	ProviderType = "aws-sm"

	// AWS Secrets Manager caps the encrypted secret value at 65,536 bytes.
	secretValueMaxBytes = 65536

	defaultDeleteRecoveryWindowDays = 7

	tagManaged       = "openbao-sync"
	tagAssociationID = "openbao-sync-association"
	tagSourcePath    = "openbao-sync-path"
	tagSourceVersion = "openbao-sync-version"
	tagObjectID      = "openbao-sync-object"
	tagPayloadSHA256 = "openbao-sync-payload-sha256"
)

type secretsManagerClient interface {
	DescribeSecret(
		context.Context,
		*secretsmanager.DescribeSecretInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.DescribeSecretOutput, error)
	CreateSecret(
		context.Context,
		*secretsmanager.CreateSecretInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(
		context.Context,
		*secretsmanager.PutSecretValueInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.PutSecretValueOutput, error)
	DeleteSecret(
		context.Context,
		*secretsmanager.DeleteSecretInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.DeleteSecretOutput, error)
	TagResource(
		context.Context,
		*secretsmanager.TagResourceInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.TagResourceOutput, error)
	ListSecrets(
		context.Context,
		*secretsmanager.ListSecretsInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.ListSecretsOutput, error)
}

type clientFactory func(context.Context) (secretsManagerClient, error)

// Provider is the AWS Secrets Manager provider.
type Provider struct {
	client        secretsManagerClient
	clientFactory clientFactory
}

// New returns a provider using the AWS SDK default configuration chain.
func New() Provider {
	return Provider{clientFactory: defaultClientFactory}
}

func (Provider) Type() string {
	return ProviderType
}

func (Provider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsValueReadback:       false,
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		SupportsSecretKey:           false,
		MaxPayloadBytes:             secretValueMaxBytes,
	}
}

func (Provider) Validate(_ context.Context, cfg providers.DestinationConfig) error {
	if strings.TrimSpace(cfg.Name) == "" {
		return &providers.Error{Class: providers.ErrorClassValidation, Message: "aws-sm destination name must not be empty"}
	}
	return nil
}

func (p Provider) Plan(ctx context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	client, err := p.clientFor(ctx)
	if err != nil {
		return blockedPlan(providers.ErrorClassInternal), nil
	}
	describe, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
	}
	if err != nil {
		return blockedPlan(classifyAWSError(err)), nil
	}
	if describe.DeletedDate != nil || !ownedByRequest(describe.Tags, ownershipIdentityFromPlan(req)) {
		return &providers.PlanResult{
			Action:     providers.PlanActionConflict,
			ErrorClass: providers.ErrorClassCollision,
			Message:    "aws-sm secret exists but is not owned by this association",
		}, nil
	}
	if remoteSourceVersionNewer(describe.Tags, req.SourceVersion) {
		return &providers.PlanResult{
			Action:     providers.PlanActionBlocked,
			ErrorClass: providers.ErrorClassDrift,
			Message:    "aws-sm secret has newer managed source version",
		}, nil
	}
	if tagValue(describe.Tags, tagPayloadSHA256) == req.PayloadSHA256 {
		return &providers.PlanResult{Action: providers.PlanActionNoop}, nil
	}
	return &providers.PlanResult{Action: providers.PlanActionUpdate}, nil
}

func (p Provider) Upsert(ctx context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	client, err := p.clientFor(ctx)
	if err != nil {
		return nil, providerError(providers.ErrorClassInternal)
	}
	describe, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return createSecret(ctx, client, req)
	}
	if err != nil {
		return nil, providerError(classifyAWSError(err))
	}
	if describe.DeletedDate != nil || !ownedByRequest(describe.Tags, ownershipIdentityFromUpsert(req)) {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(describe.Tags, req.SourceVersion) {
		return nil, providerError(providers.ErrorClassDrift)
	}
	if tagValue(describe.Tags, tagPayloadSHA256) == req.PayloadSHA256 {
		return &providers.SyncResult{RemoteVersion: currentVersionID(describe)}, nil
	}
	payload := string(req.Payload)
	result, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:           aws.String(req.ResolvedName),
		SecretString:       aws.String(payload),
		ClientRequestToken: aws.String(idempotencyToken("put", req.ResolvedName, req.PayloadSHA256)),
	})
	if err != nil {
		return nil, providerError(classifyAWSError(err))
	}
	if _, err := client.TagResource(ctx, &secretsmanager.TagResourceInput{
		SecretId: aws.String(req.ResolvedName),
		Tags:     ownershipTagsFromUpsert(req),
	}); err != nil {
		return nil, providerError(classifyAWSError(err))
	}
	return &providers.SyncResult{RemoteVersion: aws.ToString(result.VersionId)}, nil
}

func (p Provider) Delete(ctx context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	client, err := p.clientFor(ctx)
	if err != nil {
		return nil, providerError(providers.ErrorClassInternal)
	}
	describe, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return &providers.SyncResult{RemoteVersion: "missing"}, nil
	}
	if err != nil {
		return nil, providerError(classifyAWSError(err))
	}
	if describe.DeletedDate != nil {
		return &providers.SyncResult{RemoteVersion: "scheduled"}, nil
	}
	if !ownedByRequest(describe.Tags, ownershipIdentityFromDelete(req)) {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(describe.Tags, req.SourceVersion) {
		return nil, providerError(providers.ErrorClassDrift)
	}
	result, err := client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:             aws.String(req.ResolvedName),
		RecoveryWindowInDays: aws.Int64(defaultDeleteRecoveryWindowDays),
	})
	if err != nil {
		return nil, providerError(classifyAWSError(err))
	}
	return &providers.SyncResult{RemoteVersion: aws.ToString(result.ARN)}, nil
}

func (p Provider) ReadState(ctx context.Context, req providers.ReadStateRequest) (*providers.RemoteState, error) {
	client, err := p.clientFor(ctx)
	if err != nil {
		return nil, providerError(providers.ErrorClassInternal)
	}
	describe, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return &providers.RemoteState{Exists: false}, nil
	}
	if err != nil {
		return nil, providerError(classifyAWSError(err))
	}
	return &providers.RemoteState{Exists: describe.DeletedDate == nil}, nil
}

func (p Provider) Health(ctx context.Context, _ providers.DestinationConfig) (*providers.HealthResult, error) {
	client, err := p.clientFor(ctx)
	if err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "aws-sm client initialization failed",
			ErrorClass: providers.ErrorClassInternal,
		}, nil
	}
	if _, err := client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{MaxResults: aws.Int32(1)}); err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "aws-sm health check failed",
			ErrorClass: classifyAWSError(err),
		}, nil
	}
	return &providers.HealthResult{Healthy: true}, nil
}

func (p Provider) clientFor(ctx context.Context) (secretsManagerClient, error) {
	if p.client != nil {
		return p.client, nil
	}
	factory := p.clientFactory
	if factory == nil {
		factory = defaultClientFactory
	}
	return factory(ctx)
}

func defaultClientFactory(ctx context.Context) (secretsManagerClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return secretsmanager.NewFromConfig(cfg), nil
}

func blockedPlan(errorClass providers.ErrorClass) *providers.PlanResult {
	return &providers.PlanResult{
		Action:     providers.PlanActionBlocked,
		ErrorClass: errorClass,
		Message:    "aws-sm provider plan failed",
	}
}

func createSecret(
	ctx context.Context,
	client secretsManagerClient,
	req providers.UpsertRequest,
) (*providers.SyncResult, error) {
	payload := string(req.Payload)
	result, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:               aws.String(req.ResolvedName),
		SecretString:       aws.String(payload),
		ClientRequestToken: aws.String(idempotencyToken("create", req.ResolvedName, req.PayloadSHA256)),
		Tags:               ownershipTagsFromUpsert(req),
	})
	if err != nil {
		return nil, providerError(classifyAWSError(err))
	}
	return &providers.SyncResult{RemoteVersion: aws.ToString(result.VersionId)}, nil
}

type ownershipIdentity struct {
	AssociationID string
	SourcePath    string
	ObjectID      string
}

func ownershipIdentityFromPlan(req providers.PlanRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID: req.AssociationID,
		SourcePath:    req.SourcePath,
		ObjectID:      req.ObjectID,
	}
}

func ownershipIdentityFromUpsert(req providers.UpsertRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID: req.AssociationID,
		SourcePath:    req.SourcePath,
		ObjectID:      req.ObjectID,
	}
}

func ownershipIdentityFromDelete(req providers.DeleteRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID: req.AssociationID,
		SourcePath:    req.SourcePath,
		ObjectID:      req.ObjectID,
	}
}

func ownershipTagsFromUpsert(req providers.UpsertRequest) []smtypes.Tag {
	return []smtypes.Tag{
		tag(tagManaged, "true"),
		tag(tagAssociationID, req.AssociationID),
		tag(tagSourcePath, req.SourcePath),
		tag(tagSourceVersion, strconv.Itoa(req.SourceVersion)),
		tag(tagObjectID, req.ObjectID),
		tag(tagPayloadSHA256, req.PayloadSHA256),
	}
}

func ownedByRequest(tags []smtypes.Tag, identity ownershipIdentity) bool {
	if tagValue(tags, tagManaged) != "true" {
		return false
	}
	return requiredTagMatches(tags, tagAssociationID, identity.AssociationID) &&
		requiredTagMatches(tags, tagSourcePath, identity.SourcePath) &&
		requiredTagMatches(tags, tagObjectID, identity.ObjectID)
}

func requiredTagMatches(tags []smtypes.Tag, key string, expected string) bool {
	return expected != "" && tagValue(tags, key) == expected
}

func remoteSourceVersionNewer(tags []smtypes.Tag, sourceVersion int) bool {
	if sourceVersion <= 0 {
		return false
	}
	remoteVersion, err := strconv.Atoi(tagValue(tags, tagSourceVersion))
	if err != nil {
		return false
	}
	return remoteVersion > sourceVersion
}

func tag(key string, value string) smtypes.Tag {
	return smtypes.Tag{Key: aws.String(key), Value: aws.String(value)}
}

func tagValue(tags []smtypes.Tag, key string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

func currentVersionID(describe *secretsmanager.DescribeSecretOutput) string {
	for versionID, stages := range describe.VersionIdsToStages {
		for _, stage := range stages {
			if stage == "AWSCURRENT" {
				return versionID
			}
		}
	}
	return ""
}

func idempotencyToken(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(sum[:16])
}

func isResourceNotFound(err error) bool {
	var apiError smithy.APIError
	return errors.As(err, &apiError) && apiError.ErrorCode() == "ResourceNotFoundException"
}

func providerError(errorClass providers.ErrorClass) error {
	return &providers.Error{Class: errorClass, Message: "aws-sm request failed"}
}

func classifyAWSError(err error) providers.ErrorClass {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return providers.ErrorClassInternal
	}
	switch apiError.ErrorCode() {
	case "ThrottlingException", "TooManyRequestsException", "LimitExceededException":
		return providers.ErrorClassRateLimit
	case "InternalServiceError", "ServiceUnavailableException", "RequestTimeout", "RequestTimeoutException":
		return providers.ErrorClassUnavailable
	case "UnrecognizedClientException", "InvalidSignatureException":
		return providers.ErrorClassAuthn
	case "AccessDeniedException":
		return providers.ErrorClassAuthz
	case "InvalidParameterException", "InvalidRequestException", "MalformedPolicyDocumentException",
		"PreconditionNotMetException", "ResourceNotFoundException":
		return providers.ErrorClassValidation
	case "ResourceExistsException":
		return providers.ErrorClassCollision
	default:
		return providers.ErrorClassInternal
	}
}
