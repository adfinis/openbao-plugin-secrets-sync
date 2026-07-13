// Package awssecretsmanager provides the AWS Secrets Manager destination provider.
package awssecretsmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/endpointguard"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/providerutil"
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
	// ConfigKeyEndpointPolicy explicitly opts in to custom endpoint behavior.
	ConfigKeyEndpointPolicy = "endpoint_policy"
	// ConfigKeyAuthMode selects AWS credential behavior.
	ConfigKeyAuthMode = "auth_mode"
	// ConfigKeyRoleARN configures the role ARN for STS role auth.
	ConfigKeyRoleARN = "role_arn"
	// ConfigKeyExternalID configures the optional external ID for assume-role auth.
	ConfigKeyExternalID = "external_id"
	// ConfigKeySessionName configures the optional STS role session name.
	ConfigKeySessionName = "session_name"
	// ConfigKeyWebIdentityTokenFile configures the projected OIDC token file for web-identity auth.
	ConfigKeyWebIdentityTokenFile = "web_identity_token_file"
	// ConfigKeyDeleteRecoveryWindowDays configures AWS Secrets Manager scheduled-delete recovery days.
	ConfigKeyDeleteRecoveryWindowDays = "delete_recovery_window_days"
	// ConfigKeyValueDriftDetection opts in to GetSecretValue-based drift checks.
	ConfigKeyValueDriftDetection = "value_drift_detection"

	// AuthModeDefault uses the AWS SDK default credential chain.
	AuthModeDefault = "default"
	// AuthModeAssumeRole uses the default credential chain and then assumes a configured role.
	AuthModeAssumeRole = "assume_role"
	// AuthModeWebIdentity uses an OIDC token file to call STS AssumeRoleWithWebIdentity.
	AuthModeWebIdentity = "web_identity"

	// EndpointPolicyLocal allows development endpoints such as LocalStack.
	EndpointPolicyLocal = "local"
	// EndpointPolicyPrivate allows explicitly configured HTTPS private endpoints.
	EndpointPolicyPrivate = "private"

	// AWS Secrets Manager caps the encrypted secret value at 65,536 bytes.
	secretValueMaxBytes = 65536

	defaultDeleteRecoveryWindowDays = 7
	minDeleteRecoveryWindowDays     = 7
	maxDeleteRecoveryWindowDays     = 30
	defaultHTTPTimeout              = 30 * time.Second

	tagManaged        = "openbao-sync"
	tagAssociationID  = "openbao-sync-association"
	tagSourcePath     = "openbao-sync-path"
	tagSourceVersion  = "openbao-sync-version"
	tagObjectID       = "openbao-sync-object"
	tagPayloadSHA256  = "openbao-sync-payload-sha256"
	tagPluginInstance = "openbao-sync-plugin-instance"
	tagRestoreEpoch   = "openbao-sync-restore-epoch"
)

var providerHelpers = providerutil.New(ProviderType)

type secretsManagerClient interface {
	DescribeSecret(
		context.Context,
		*secretsmanager.DescribeSecretInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.DescribeSecretOutput, error)
	GetSecretValue(
		context.Context,
		*secretsmanager.GetSecretValueInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.GetSecretValueOutput, error)
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
	RestoreSecret(
		context.Context,
		*secretsmanager.RestoreSecretInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.RestoreSecretOutput, error)
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

type destinationRuntime struct {
	client  secretsManagerClient
	options awsDestinationOptions
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
		SupportsValueReadback:       true,
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		SupportsSecretKey:           false,
		MaxPayloadBytes:             secretValueMaxBytes,
	}
}

func (Provider) ValidateConfig(_ context.Context, cfg providers.DestinationConfig) error {
	if strings.TrimSpace(cfg.Name) == "" {
		return &providers.Error{Class: providers.ErrorClassValidation, Message: "aws-sm destination name must not be empty"}
	}
	if _, err := awsDestinationOptionsFromConfig(cfg); err != nil {
		return err
	}
	return nil
}

func (Provider) NormalizeAssociationConfig(
	_ context.Context,
	_ providers.DestinationConfig,
	cfg providers.AssociationConfig,
) (providers.AssociationConfig, error) {
	if len(cfg.Config) > 0 {
		return providers.AssociationConfig{}, providerHelpers.ValidationError(
			"aws-sm does not support association configuration",
		)
	}
	return providers.AssociationConfig{Config: map[string]string{}}, nil
}

func (p Provider) OpenDestination(
	ctx context.Context,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	options, err := awsDestinationOptionsFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := p.clientFor(ctx, cfg)
	if err != nil {
		return nil, providerHelpers.ProviderError(providerHelpers.SetupErrorClass(err))
	}
	return destinationRuntime{client: client, options: options}, nil
}

func (r destinationRuntime) Health(ctx context.Context) (*providers.HealthResult, error) {
	if _, err := r.client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{MaxResults: aws.Int32(1)}); err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "aws-sm health check failed",
			ErrorClass: classifyAWSError(err),
		}, nil
	}
	return &providers.HealthResult{Healthy: true}, nil
}

func (r destinationRuntime) Plan(ctx context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	describe, err := r.client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return &providers.PlanResult{
			Action:  providers.PlanActionCreate,
			Message: "aws-sm secret is missing and will be created",
		}, nil
	}
	if err != nil {
		return providerHelpers.BlockedPlan(classifyAWSError(err)), nil
	}
	if !ownedByRequest(describe.Tags, req.OwnershipIdentity()) {
		message := "aws-sm secret exists but is not owned by this association"
		if describe.DeletedDate != nil {
			message = "aws-sm secret is scheduled for deletion but is not owned by this association"
		}
		return &providers.PlanResult{
			Action:     providers.PlanActionConflict,
			ErrorClass: providers.ErrorClassCollision,
			Message:    message,
		}, nil
	}
	if remoteSourceVersionNewer(describe.Tags, req.SourceVersion) {
		return &providers.PlanResult{
			Action:     providers.PlanActionBlocked,
			ErrorClass: providers.ErrorClassDrift,
			Message:    "aws-sm secret has newer managed source version",
		}, nil
	}
	if describe.DeletedDate != nil {
		return &providers.PlanResult{
			Action:  providers.PlanActionUpdate,
			Message: "aws-sm secret is scheduled for deletion and will be restored before upsert",
		}, nil
	}
	payloadMatches, err := r.payloadMatchesRequest(ctx, describe.Tags, req.ResolvedName, req.PayloadSHA256)
	if err != nil {
		return providerHelpers.BlockedPlan(classifyAWSError(err)), nil
	}
	if payloadMatches {
		if tagValue(describe.Tags, tagPayloadSHA256) == req.PayloadSHA256 &&
			remoteSourceVersionMatches(describe.Tags, req.SourceVersion) {
			return &providers.PlanResult{
				Action:  providers.PlanActionNoop,
				Message: "aws-sm secret already matches desired payload and metadata",
			}, nil
		}
		return &providers.PlanResult{
			Action:  providers.PlanActionUpdate,
			Message: "aws-sm secret metadata differs and will be refreshed",
		}, nil
	}
	return &providers.PlanResult{
		Action:  providers.PlanActionUpdate,
		Message: "aws-sm secret payload differs and will be updated",
	}, nil
}

func (r destinationRuntime) Upsert(ctx context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	if len(req.Payload) > secretValueMaxBytes {
		return nil, providerHelpers.ProviderError(providers.ErrorClassCapacity)
	}
	describe, err := r.client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return createSecret(ctx, r.client, req)
	}
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	if !ownedByRequest(describe.Tags, req.OwnershipIdentity()) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(describe.Tags, req.SourceVersion) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassDrift)
	}
	if describe.DeletedDate != nil {
		if _, err := r.client.RestoreSecret(ctx, &secretsmanager.RestoreSecretInput{
			SecretId: aws.String(req.ResolvedName),
		}); err != nil {
			return nil, providerHelpers.ProviderError(classifyAWSError(err))
		}
	}
	valueMatches, err := r.payloadMatchesRequest(ctx, describe.Tags, req.ResolvedName, req.PayloadSHA256)
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	if valueMatches {
		if tagValue(describe.Tags, tagPayloadSHA256) != req.PayloadSHA256 ||
			!remoteSourceVersionMatches(describe.Tags, req.SourceVersion) {
			if _, err := r.client.TagResource(ctx, &secretsmanager.TagResourceInput{
				SecretId: aws.String(req.ResolvedName),
				Tags:     ownershipTagsFromUpsert(req),
			}); err != nil {
				return nil, providerHelpers.ProviderError(classifyAWSError(err))
			}
		}
		return &providers.SyncResult{RemoteVersion: currentVersionID(describe)}, nil
	}
	payload := string(req.Payload)
	result, err := r.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:           aws.String(req.ResolvedName),
		SecretString:       aws.String(payload),
		ClientRequestToken: aws.String(mutationIdempotencyToken("put", req)),
	})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	if _, err := r.client.TagResource(ctx, &secretsmanager.TagResourceInput{
		SecretId: aws.String(req.ResolvedName),
		Tags:     ownershipTagsFromUpsert(req),
	}); err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	return &providers.SyncResult{RemoteVersion: aws.ToString(result.VersionId)}, nil
}

func (r destinationRuntime) Delete(ctx context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	describe, err := r.client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return &providers.SyncResult{RemoteVersion: "missing"}, nil
	}
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	if !ownedByRequest(describe.Tags, req.OwnershipIdentity()) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassOwnership)
	}
	if describe.DeletedDate != nil {
		return &providers.SyncResult{RemoteVersion: "scheduled"}, nil
	}
	if remoteSourceVersionNewer(describe.Tags, req.SourceVersion) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassDrift)
	}
	result, err := r.client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:             aws.String(req.ResolvedName),
		RecoveryWindowInDays: aws.Int64(int64(r.options.deleteRecoveryWindowDays)),
	})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	return &providers.SyncResult{RemoteVersion: aws.ToString(result.ARN)}, nil
}

func (r destinationRuntime) ReadState(
	ctx context.Context,
	req providers.ReadStateRequest,
) (*providers.RemoteState, error) {
	describe, err := r.client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(req.ResolvedName),
	})
	if isResourceNotFound(err) {
		return &providers.RemoteState{Exists: false}, nil
	}
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	if describe.DeletedDate != nil {
		return &providers.RemoteState{Exists: false}, nil
	}
	owned := ownedByRequest(describe.Tags, req.OwnershipIdentity())
	payloadSHA256 := tagValue(describe.Tags, tagPayloadSHA256)
	verification := providers.RemoteStateVerificationMetadata
	if owned && r.options.valueDriftDetection {
		var readErr error
		payloadSHA256, readErr = remotePayloadSHA256(ctx, r.client, req.ResolvedName)
		if readErr != nil {
			return nil, providerHelpers.ProviderError(classifyAWSError(readErr))
		}
		verification = providers.RemoteStateVerificationValue
	}
	sourceVersion, _ := strconv.Atoi(tagValue(describe.Tags, tagSourceVersion))
	return &providers.RemoteState{
		Exists:         true,
		OwnershipKnown: req.OwnershipIdentity().Complete(),
		Owned:          owned,
		PayloadSHA256:  payloadSHA256,
		SourceVersion:  sourceVersion,
		RemoteVersion:  currentVersionID(describe),
		Verification:   verification,
	}, nil
}

func (destinationRuntime) Close(context.Context) error {
	return nil
}

func (r destinationRuntime) payloadMatchesRequest(
	ctx context.Context,
	tags []smtypes.Tag,
	resolvedName string,
	payloadSHA256 string,
) (bool, error) {
	if !r.options.valueDriftDetection {
		return tagValue(tags, tagPayloadSHA256) == payloadSHA256, nil
	}
	livePayloadSHA256, err := remotePayloadSHA256(ctx, r.client, resolvedName)
	if err != nil {
		return false, err
	}
	return livePayloadSHA256 == payloadSHA256, nil
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
	return defaultClientFactoryWithResolver(ctx, providerConfig, nil)
}

func defaultClientFactoryWithResolver(
	ctx context.Context,
	providerConfig providers.DestinationConfig,
	resolver endpointguard.Resolver,
) (secretsManagerClient, error) {
	options, err := awsDestinationOptionsFromConfig(providerConfig)
	if err != nil {
		return nil, err
	}
	if err := validateEndpointResolution(ctx, options, resolver); err != nil {
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
	// STS calls use the bounded default client. The custom endpoint address
	// policy applies only to the Secrets Manager client created below.
	cfg.HTTPClient = defaultAWSHTTPClient()
	switch options.authMode {
	case AuthModeAssumeRole:
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
	case AuthModeWebIdentity:
		stsClient := sts.NewFromConfig(cfg)
		webIdentityProvider := stscreds.NewWebIdentityRoleProvider(
			stsClient,
			options.roleARN,
			stscreds.IdentityTokenFile(options.webIdentityTokenFile),
			func(webIdentityOptions *stscreds.WebIdentityRoleOptions) {
				if options.sessionName != "" {
					webIdentityOptions.RoleSessionName = options.sessionName
				}
			},
		)
		cfg.Credentials = aws.NewCredentialsCache(webIdentityProvider)
	}
	cfg.HTTPClient = awsHTTPClientForOptions(options, resolver)
	clientOptions := []func(*secretsmanager.Options){}
	if options.endpointURL != "" {
		clientOptions = append(clientOptions, func(secretsManagerOptions *secretsmanager.Options) {
			secretsManagerOptions.BaseEndpoint = aws.String(options.endpointURL)
		})
	}
	return secretsmanager.NewFromConfig(cfg, clientOptions...), nil
}

func defaultAWSHTTPClient() *http.Client {
	transport, _ := http.DefaultTransport.(*http.Transport)
	if transport != nil {
		transport = transport.Clone()
		transport.Proxy = nil
	}
	return &http.Client{
		Timeout:   defaultHTTPTimeout,
		Transport: transport,
	}
}

func awsHTTPClientForOptions(options awsDestinationOptions, resolver endpointguard.Resolver) *http.Client {
	client := defaultAWSHTTPClient()
	if options.endpointURL == "" {
		return client
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return client
	}
	var allowed endpointguard.AddrAllowed
	switch options.endpointPolicy {
	case EndpointPolicyLocal:
		allowed = func(addr netip.Addr) bool {
			addr = addr.Unmap()
			return addr.IsLoopback() || addr.IsPrivate()
		}
	case EndpointPolicyPrivate:
		allowed = func(addr netip.Addr) bool { return !isUnsafeEndpointAddr(addr) }
	default:
		return client
	}
	transport.DialContext = endpointguard.GuardedDialContext(resolver, allowed)
	return client
}

type awsDestinationOptions struct {
	region                   string
	endpointURL              string
	endpointPolicy           string
	authMode                 string
	roleARN                  string
	externalID               string
	sessionName              string
	webIdentityTokenFile     string
	deleteRecoveryWindowDays int
	valueDriftDetection      bool
}

func awsDestinationOptionsFromConfig(cfg providers.DestinationConfig) (awsDestinationOptions, error) {
	options := awsDestinationOptions{
		region:                   providerHelpers.ConfigValue(cfg, ConfigKeyRegion),
		endpointURL:              providerHelpers.ConfigValue(cfg, ConfigKeyEndpointURL),
		endpointPolicy:           providerHelpers.ConfigValue(cfg, ConfigKeyEndpointPolicy),
		authMode:                 normalizedAuthMode(cfg),
		roleARN:                  providerHelpers.ConfigValue(cfg, ConfigKeyRoleARN),
		externalID:               providerHelpers.ConfigValue(cfg, ConfigKeyExternalID),
		sessionName:              providerHelpers.ConfigValue(cfg, ConfigKeySessionName),
		webIdentityTokenFile:     providerHelpers.ConfigValue(cfg, ConfigKeyWebIdentityTokenFile),
		deleteRecoveryWindowDays: defaultDeleteRecoveryWindowDays,
	}
	var err error
	if options.deleteRecoveryWindowDays, err = deleteRecoveryWindowDaysFromConfig(cfg); err != nil {
		return awsDestinationOptions{}, err
	}
	if options.valueDriftDetection, err = providerHelpers.BoolConfigValue(
		cfg,
		ConfigKeyValueDriftDetection,
		false,
	); err != nil {
		return awsDestinationOptions{}, err
	}
	if err := validateEndpointOptions(options); err != nil {
		return awsDestinationOptions{}, err
	}
	if err := validateAuthOptions(options); err != nil {
		return awsDestinationOptions{}, err
	}
	return options, nil
}

func validateEndpointOptions(options awsDestinationOptions) error {
	if options.endpointURL != "" {
		if options.endpointPolicy == "" {
			return providerHelpers.ValidationError("aws-sm endpoint_url requires endpoint_policy")
		}
		if err := validateEndpointURL(options.endpointURL, options.endpointPolicy); err != nil {
			return err
		}
	} else if options.endpointPolicy != "" {
		return providerHelpers.ValidationError("aws-sm endpoint_policy requires endpoint_url")
	}
	return nil
}

func validateAuthOptions(options awsDestinationOptions) error {
	switch options.authMode {
	case AuthModeDefault:
		return validateDefaultAuthOptions(options)
	case AuthModeAssumeRole:
		return validateAssumeRoleAuthOptions(options)
	case AuthModeWebIdentity:
		return validateWebIdentityAuthOptions(options)
	default:
		return providerHelpers.ValidationError(
			"aws-sm auth_mode must be default, assume_role, or web_identity",
		)
	}
}

func validateDefaultAuthOptions(options awsDestinationOptions) error {
	if options.roleARN != "" ||
		options.externalID != "" ||
		options.sessionName != "" ||
		options.webIdentityTokenFile != "" {
		return providerHelpers.ValidationError("aws-sm auth fields require auth_mode assume_role or web_identity")
	}
	return nil
}

func validateAssumeRoleAuthOptions(options awsDestinationOptions) error {
	if options.roleARN == "" {
		return providerHelpers.ValidationError("aws-sm auth_mode assume_role requires role_arn")
	}
	if !isLikelyRoleARN(options.roleARN) {
		return providerHelpers.ValidationError("aws-sm role_arn must be an IAM role ARN")
	}
	if options.webIdentityTokenFile != "" {
		return providerHelpers.ValidationError("aws-sm web_identity_token_file requires auth_mode web_identity")
	}
	return nil
}

func validateWebIdentityAuthOptions(options awsDestinationOptions) error {
	if options.roleARN == "" {
		return providerHelpers.ValidationError("aws-sm auth_mode web_identity requires role_arn")
	}
	if !isLikelyRoleARN(options.roleARN) {
		return providerHelpers.ValidationError("aws-sm role_arn must be an IAM role ARN")
	}
	if options.webIdentityTokenFile == "" {
		return providerHelpers.ValidationError("aws-sm auth_mode web_identity requires web_identity_token_file")
	}
	if !filepath.IsAbs(options.webIdentityTokenFile) {
		return providerHelpers.ValidationError("aws-sm web_identity_token_file must be an absolute path")
	}
	if options.externalID != "" {
		return providerHelpers.ValidationError("aws-sm external_id is only supported with auth_mode assume_role")
	}
	return nil
}

func deleteRecoveryWindowDaysFromConfig(cfg providers.DestinationConfig) (int, error) {
	value := providerHelpers.ConfigValue(cfg, ConfigKeyDeleteRecoveryWindowDays)
	if value == "" {
		return defaultDeleteRecoveryWindowDays, nil
	}
	days, err := strconv.Atoi(value)
	if err != nil {
		return 0, providerHelpers.ValidationError("aws-sm delete_recovery_window_days must be an integer")
	}
	if days < minDeleteRecoveryWindowDays || days > maxDeleteRecoveryWindowDays {
		return 0, providerHelpers.ValidationError("aws-sm delete_recovery_window_days must be between 7 and 30")
	}
	return days, nil
}

func normalizedAuthMode(cfg providers.DestinationConfig) string {
	authMode := providerHelpers.ConfigValue(cfg, ConfigKeyAuthMode)
	if authMode == "" {
		return AuthModeDefault
	}
	return authMode
}

func validateEndpointURL(rawEndpoint string, endpointPolicy string) error {
	parsedEndpoint, err := url.Parse(rawEndpoint)
	if err != nil {
		return providerHelpers.ValidationError("aws-sm endpoint_url must be a valid URL")
	}
	if parsedEndpoint.Host == "" || parsedEndpoint.User != nil ||
		parsedEndpoint.RawQuery != "" || parsedEndpoint.Fragment != "" {
		return providerHelpers.ValidationError("aws-sm endpoint_url must include a host and no userinfo, query, or fragment")
	}
	host := endpointguard.NormalizeHost(parsedEndpoint.Hostname())
	if host == "" {
		return providerHelpers.ValidationError("aws-sm endpoint_url must include a host")
	}
	switch endpointPolicy {
	case EndpointPolicyLocal:
		return validateLocalEndpointURL(parsedEndpoint.Scheme, host)
	case EndpointPolicyPrivate:
		return validatePrivateEndpointURL(parsedEndpoint.Scheme, host)
	default:
		return providerHelpers.ValidationError("aws-sm endpoint_policy must be local or private")
	}
}

func validateEndpointResolution(
	ctx context.Context,
	options awsDestinationOptions,
	resolver endpointguard.Resolver,
) error {
	if options.endpointURL == "" || options.endpointPolicy != EndpointPolicyPrivate {
		return nil
	}
	parsedEndpoint, err := url.Parse(options.endpointURL)
	if err != nil {
		return providerHelpers.ValidationError("aws-sm endpoint_url must be a valid URL")
	}
	host := endpointguard.NormalizeHost(parsedEndpoint.Hostname())
	if host == "" {
		return providerHelpers.ValidationError("aws-sm endpoint_url must include a host")
	}
	if addr, ok := endpointguard.ParseAddr(host); ok {
		if isUnsafeEndpointAddr(addr) {
			return providerHelpers.ValidationError(
				"aws-sm private endpoint_url DNS must not resolve to loopback, link-local, multicast, or unspecified addresses",
			)
		}
		return nil
	}
	addrs, err := endpointguard.LookupNetIP(ctx, host, resolver)
	if err != nil {
		return &providers.Error{
			Class:   providers.ErrorClassUnavailable,
			Message: "aws-sm private endpoint_url DNS lookup failed",
		}
	}
	if len(addrs) == 0 {
		return &providers.Error{
			Class:   providers.ErrorClassUnavailable,
			Message: "aws-sm private endpoint_url DNS lookup returned no addresses",
		}
	}
	for _, addr := range addrs {
		if isUnsafeEndpointAddr(addr) {
			return providerHelpers.ValidationError(
				"aws-sm private endpoint_url DNS must not resolve to loopback, link-local, multicast, or unspecified addresses",
			)
		}
	}
	return nil
}

func validateLocalEndpointURL(scheme string, host string) error {
	if scheme != "https" && scheme != "http" {
		return providerHelpers.ValidationError("aws-sm local endpoint_url must use http or https")
	}
	if !isLocalEndpointHost(host) {
		return providerHelpers.ValidationError("aws-sm local endpoint_url host must be localhost, loopback, or localstack")
	}
	return nil
}

func validatePrivateEndpointURL(scheme string, host string) error {
	if scheme != "https" {
		return providerHelpers.ValidationError("aws-sm private endpoint_url must use https")
	}
	if isLocalEndpointHost(host) {
		return providerHelpers.ValidationError("aws-sm private endpoint_url must not target local development hosts")
	}
	if addr, ok := endpointguard.ParseAddr(host); ok && isUnsafeEndpointAddr(addr) {
		return providerHelpers.ValidationError(
			"aws-sm private endpoint_url must not target loopback, link-local, multicast, or unspecified addresses",
		)
	}
	return nil
}

func isLocalEndpointHost(host string) bool {
	if endpointguard.NormalizeHost(host) == "localstack" {
		return true
	}
	return endpointguard.IsLocalHost(host)
}

func isUnsafeEndpointAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
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

func createSecret(
	ctx context.Context,
	client secretsManagerClient,
	req providers.UpsertRequest,
) (*providers.SyncResult, error) {
	payload := string(req.Payload)
	result, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:               aws.String(req.ResolvedName),
		SecretString:       aws.String(payload),
		ClientRequestToken: aws.String(mutationIdempotencyToken("create", req)),
		Tags:               ownershipTagsFromUpsert(req),
	})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyAWSError(err))
	}
	return &providers.SyncResult{RemoteVersion: aws.ToString(result.VersionId)}, nil
}

func remotePayloadSHA256(
	ctx context.Context,
	client secretsManagerClient,
	resolvedName string,
) (string, error) {
	value, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(resolvedName),
	})
	if err != nil {
		return "", err
	}
	return secretValuePayloadSHA256(value), nil
}

func secretValuePayloadSHA256(value *secretsmanager.GetSecretValueOutput) string {
	if value == nil {
		return payloadSHA256(nil)
	}
	if value.SecretString != nil {
		return payloadSHA256([]byte(aws.ToString(value.SecretString)))
	}
	return payloadSHA256(value.SecretBinary)
}

func payloadSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ownershipTagsFromUpsert(req providers.UpsertRequest) []smtypes.Tag {
	tags := []smtypes.Tag{
		tag(tagManaged, "true"),
		tag(tagAssociationID, req.AssociationID),
		tag(tagSourcePath, req.SourcePath),
		tag(tagSourceVersion, strconv.Itoa(req.SourceVersion)),
		tag(tagObjectID, req.ObjectID),
		tag(tagPayloadSHA256, req.PayloadSHA256),
	}
	if req.Runtime.PluginInstanceID != "" {
		tags = append(tags, tag(tagPluginInstance, req.Runtime.PluginInstanceID))
	}
	if req.Runtime.RestoreEpoch != "" {
		tags = append(tags, tag(tagRestoreEpoch, req.Runtime.RestoreEpoch))
	}
	return tags
}

func ownedByRequest(tags []smtypes.Tag, identity providers.RequestIdentity) bool {
	if tagValue(tags, tagManaged) != "true" {
		return false
	}
	return requiredTagMatches(tags, tagAssociationID, identity.AssociationID) &&
		requiredTagMatches(tags, tagSourcePath, identity.SourcePath) &&
		requiredTagMatches(tags, tagObjectID, identity.ObjectID) &&
		runtimeTagMatches(tags, tagPluginInstance, identity.PluginInstanceID) &&
		runtimeTagMatches(tags, tagRestoreEpoch, identity.RestoreEpoch)
}

func requiredTagMatches(tags []smtypes.Tag, key string, expected string) bool {
	return expected != "" && tagValue(tags, key) == expected
}

func runtimeTagMatches(tags []smtypes.Tag, key string, expected string) bool {
	if expected == "" {
		return true
	}
	return tagValue(tags, key) == expected
}

func remoteSourceVersionNewer(tags []smtypes.Tag, sourceVersion int) bool {
	if sourceVersion <= 0 {
		return false
	}
	remoteVersion, ok := remoteSourceVersion(tags)
	if !ok {
		return false
	}
	return remoteVersion > sourceVersion
}

func remoteSourceVersionMatches(tags []smtypes.Tag, sourceVersion int) bool {
	if sourceVersion <= 0 {
		return true
	}
	remoteVersion, ok := remoteSourceVersion(tags)
	return ok && remoteVersion == sourceVersion
}

func remoteSourceVersion(tags []smtypes.Tag) (int, bool) {
	remoteVersion, err := strconv.Atoi(tagValue(tags, tagSourceVersion))
	if err != nil {
		return 0, false
	}
	return remoteVersion, true
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

func mutationIdempotencyToken(operation string, req providers.UpsertRequest) string {
	if req.IdempotencyKey != "" {
		return idempotencyToken(operation, req.ResolvedName, req.IdempotencyKey)
	}
	return idempotencyToken(operation, req.ResolvedName, req.PayloadSHA256)
}

func isResourceNotFound(err error) bool {
	var apiError smithy.APIError
	return errors.As(err, &apiError) && apiError.ErrorCode() == "ResourceNotFoundException"
}

func classifyAWSError(err error) providers.ErrorClass {
	if errorClass, ok := providers.ClassifyTransportError(err); ok {
		return errorClass
	}
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
