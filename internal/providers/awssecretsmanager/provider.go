// Package awssecretsmanager provides the AWS Secrets Manager destination provider.
package awssecretsmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

const (
	// ProviderType is the stable destination type used by associations.
	ProviderType = "aws-sm"

	// ConfigKeyRegion configures the AWS region. If omitted, the SDK default chain may supply it.
	ConfigKeyRegion = "region"
	// ConfigKeyEndpointURL configures a custom Secrets Manager endpoint, primarily for localstack.
	ConfigKeyEndpointURL = "endpoint_url"
	// ConfigKeyAuthMode selects AWS credential behavior.
	ConfigKeyAuthMode = "auth_mode"
	// ConfigKeyRoleARN configures the role ARN for assume-role auth.
	ConfigKeyRoleARN = "role_arn"
	// ConfigKeyExternalID configures the optional external ID for assume-role auth.
	ConfigKeyExternalID = "external_id"
	// ConfigKeySessionName configures the optional role session name for assume-role auth.
	ConfigKeySessionName = "session_name"

	// AuthModeDefault uses the AWS SDK default credential chain.
	AuthModeDefault = "default"
	// AuthModeAssumeRole uses the default credential chain and then assumes a configured role.
	AuthModeAssumeRole = "assume_role"

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

type clientFactory func(context.Context, providers.DestinationConfig) (secretsManagerClient, error)

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
	if _, err := awsDestinationOptionsFromConfig(cfg); err != nil {
		return err
	}
	return nil
}

func (p Provider) Plan(ctx context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	client, err := p.clientFor(ctx, req.Destination)
	if err != nil {
		return blockedPlan(setupErrorClass(err)), nil
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
	client, err := p.clientFor(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
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
	client, err := p.clientFor(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
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
	client, err := p.clientFor(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
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

func (p Provider) Health(ctx context.Context, cfg providers.DestinationConfig) (*providers.HealthResult, error) {
	client, err := p.clientFor(ctx, cfg)
	if err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "aws-sm client initialization failed",
			ErrorClass: setupErrorClass(err),
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

func (p Provider) clientFor(ctx context.Context, cfg providers.DestinationConfig) (secretsManagerClient, error) {
	if p.client != nil {
		return p.client, nil
	}
	factory := p.clientFactory
	if factory == nil {
		factory = defaultClientFactory
	}
	return factory(ctx, cfg)
}

func defaultClientFactory(
	ctx context.Context,
	providerConfig providers.DestinationConfig,
) (secretsManagerClient, error) {
	options, err := awsDestinationOptionsFromConfig(providerConfig)
	if err != nil {
		return nil, err
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{}
	if options.region != "" {
		loadOptions = append(loadOptions, awsconfig.WithRegion(options.region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, err
	}
	if options.authMode == AuthModeAssumeRole {
		stsClient := sts.NewFromConfig(cfg)
		assumeRoleProvider := stscreds.NewAssumeRoleProvider(
			stsClient,
			options.roleARN,
			func(assumeOptions *stscreds.AssumeRoleOptions) {
				if options.externalID != "" {
					assumeOptions.ExternalID = aws.String(options.externalID)
				}
				if options.sessionName != "" {
					assumeOptions.RoleSessionName = options.sessionName
				}
			},
		)
		cfg.Credentials = aws.NewCredentialsCache(assumeRoleProvider)
	}
	clientOptions := []func(*secretsmanager.Options){}
	if options.endpointURL != "" {
		clientOptions = append(clientOptions, func(secretsManagerOptions *secretsmanager.Options) {
			secretsManagerOptions.BaseEndpoint = aws.String(options.endpointURL)
		})
	}
	return secretsmanager.NewFromConfig(cfg, clientOptions...), nil
}

type awsDestinationOptions struct {
	region      string
	endpointURL string
	authMode    string
	roleARN     string
	externalID  string
	sessionName string
}

func awsDestinationOptionsFromConfig(cfg providers.DestinationConfig) (awsDestinationOptions, error) {
	options := awsDestinationOptions{
		region:      configValue(cfg, ConfigKeyRegion),
		endpointURL: configValue(cfg, ConfigKeyEndpointURL),
		authMode:    normalizedAuthMode(cfg),
		roleARN:     configValue(cfg, ConfigKeyRoleARN),
		externalID:  configValue(cfg, ConfigKeyExternalID),
		sessionName: configValue(cfg, ConfigKeySessionName),
	}
	if options.endpointURL != "" {
		if err := validateEndpointURL(options.endpointURL); err != nil {
			return awsDestinationOptions{}, err
		}
	}
	switch options.authMode {
	case AuthModeDefault:
		if options.roleARN != "" || options.externalID != "" || options.sessionName != "" {
			return awsDestinationOptions{}, validationError("aws-sm role fields require auth_mode assume_role")
		}
	case AuthModeAssumeRole:
		if options.roleARN == "" {
			return awsDestinationOptions{}, validationError("aws-sm auth_mode assume_role requires role_arn")
		}
		if !isLikelyRoleARN(options.roleARN) {
			return awsDestinationOptions{}, validationError("aws-sm role_arn must be an IAM role ARN")
		}
	default:
		return awsDestinationOptions{}, validationError(
			"aws-sm auth_mode must be default or assume_role",
		)
	}
	return options, nil
}

func normalizedAuthMode(cfg providers.DestinationConfig) string {
	authMode := configValue(cfg, ConfigKeyAuthMode)
	if authMode == "" {
		return AuthModeDefault
	}
	return authMode
}

func configValue(cfg providers.DestinationConfig, key string) string {
	if cfg.Config == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Config[key])
}

func validateEndpointURL(rawEndpoint string) error {
	parsedEndpoint, err := url.Parse(rawEndpoint)
	if err != nil {
		return validationError("aws-sm endpoint_url must be a valid URL")
	}
	if parsedEndpoint.Scheme != "https" && parsedEndpoint.Scheme != "http" {
		return validationError("aws-sm endpoint_url must use http or https")
	}
	if parsedEndpoint.Host == "" || parsedEndpoint.User != nil {
		return validationError("aws-sm endpoint_url must include a host and no userinfo")
	}
	return nil
}

func isLikelyRoleARN(roleARN string) bool {
	parts := strings.SplitN(roleARN, ":", 6)
	if len(parts) != 6 {
		return false
	}
	return strings.HasPrefix(roleARN, "arn:") &&
		parts[2] == "iam" &&
		parts[4] != "" &&
		strings.HasPrefix(parts[5], "role/")
}

func validationError(message string) error {
	return &providers.Error{Class: providers.ErrorClassValidation, Message: message}
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

func setupErrorClass(err error) providers.ErrorClass {
	var providerError *providers.Error
	if errors.As(err, &providerError) && providerError.Class != "" {
		return providerError.Class
	}
	return providers.ErrorClassInternal
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
