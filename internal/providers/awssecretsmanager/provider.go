// Package awssecretsmanager provides the AWS Secrets Manager destination provider.
package awssecretsmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
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
	// ConfigKeyRoleARN configures the role ARN for assume-role auth.
	ConfigKeyRoleARN = "role_arn"
	// ConfigKeyExternalID configures the optional external ID for assume-role auth.
	ConfigKeyExternalID = "external_id"
	// ConfigKeySessionName configures the optional role session name for assume-role auth.
	ConfigKeySessionName = "session_name"
	// ConfigKeyAccessKeyID configures the static AWS access key ID. Static auth is intentionally unsupported for now.
	ConfigKeyAccessKeyID = "access_key_id"
	// ConfigKeySecretAccessKey configures the static AWS secret access key.
	// Static auth is intentionally unsupported for now.
	ConfigKeySecretAccessKey = "secret_access_key"
	// ConfigKeySessionToken configures the optional static AWS session token.
	// Static auth is intentionally unsupported for now.
	ConfigKeySessionToken = "session_token"

	// AuthModeDefault uses the AWS SDK default credential chain.
	AuthModeDefault = "default"
	// AuthModeAssumeRole uses the default credential chain and then assumes a configured role.
	AuthModeAssumeRole = "assume_role"
	// AuthModeStatic is reserved for non-default static credential auth.
	AuthModeStatic = "static"

	// EndpointPolicyLocal allows development endpoints such as LocalStack.
	EndpointPolicyLocal = "local"
	// EndpointPolicyPrivate allows explicitly configured HTTPS private endpoints.
	EndpointPolicyPrivate = "private"

	// AWS Secrets Manager caps the encrypted secret value at 65,536 bytes.
	secretValueMaxBytes = 65536

	defaultDeleteRecoveryWindowDays = 7
	defaultHTTPTimeout              = 30 * time.Second
	endpointResolutionTimeout       = 5 * time.Second

	tagManaged        = "openbao-sync"
	tagAssociationID  = "openbao-sync-association"
	tagSourcePath     = "openbao-sync-path"
	tagSourceVersion  = "openbao-sync-version"
	tagObjectID       = "openbao-sync-object"
	tagPayloadSHA256  = "openbao-sync-payload-sha256"
	tagPluginInstance = "openbao-sync-plugin-instance"
	tagRestoreEpoch   = "openbao-sync-restore-epoch"
)

type endpointResolver func(context.Context, string, string) ([]netip.Addr, error)

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
	if len(req.Payload) > secretValueMaxBytes {
		return nil, providerError(providers.ErrorClassCapacity)
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
	if describe.DeletedDate != nil {
		return &providers.RemoteState{Exists: false}, nil
	}
	sourceVersion, _ := strconv.Atoi(tagValue(describe.Tags, tagSourceVersion))
	return &providers.RemoteState{
		Exists:         true,
		OwnershipKnown: hasOwnershipIdentityFromReadState(req),
		Owned:          ownedByRequest(describe.Tags, ownershipIdentityFromReadState(req)),
		PayloadSHA256:  tagValue(describe.Tags, tagPayloadSHA256),
		SourceVersion:  sourceVersion,
		RemoteVersion:  currentVersionID(describe),
	}, nil
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
	if err := validateEndpointResolution(ctx, options, net.DefaultResolver.LookupNetIP); err != nil {
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
	cfg.HTTPClient = defaultAWSHTTPClient()
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

type awsDestinationOptions struct {
	region          string
	endpointURL     string
	endpointPolicy  string
	authMode        string
	roleARN         string
	externalID      string
	sessionName     string
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
}

func awsDestinationOptionsFromConfig(cfg providers.DestinationConfig) (awsDestinationOptions, error) {
	options := awsDestinationOptions{
		region:          configValue(cfg, ConfigKeyRegion),
		endpointURL:     configValue(cfg, ConfigKeyEndpointURL),
		endpointPolicy:  configValue(cfg, ConfigKeyEndpointPolicy),
		authMode:        normalizedAuthMode(cfg),
		roleARN:         configValue(cfg, ConfigKeyRoleARN),
		externalID:      configValue(cfg, ConfigKeyExternalID),
		sessionName:     configValue(cfg, ConfigKeySessionName),
		accessKeyID:     configValue(cfg, ConfigKeyAccessKeyID),
		secretAccessKey: configValue(cfg, ConfigKeySecretAccessKey),
		sessionToken:    configValue(cfg, ConfigKeySessionToken),
	}
	if options.endpointURL != "" {
		if options.endpointPolicy == "" {
			return awsDestinationOptions{}, validationError("aws-sm endpoint_url requires endpoint_policy")
		}
		if err := validateEndpointURL(options.endpointURL, options.endpointPolicy); err != nil {
			return awsDestinationOptions{}, err
		}
	} else if options.endpointPolicy != "" {
		return awsDestinationOptions{}, validationError("aws-sm endpoint_policy requires endpoint_url")
	}
	switch options.authMode {
	case AuthModeDefault:
		if options.roleARN != "" || options.externalID != "" || options.sessionName != "" {
			return awsDestinationOptions{}, validationError("aws-sm role fields require auth_mode assume_role")
		}
		if options.hasStaticCredentials() {
			return awsDestinationOptions{}, validationError("aws-sm static credential fields require auth_mode static")
		}
	case AuthModeAssumeRole:
		if options.roleARN == "" {
			return awsDestinationOptions{}, validationError("aws-sm auth_mode assume_role requires role_arn")
		}
		if !isLikelyRoleARN(options.roleARN) {
			return awsDestinationOptions{}, validationError("aws-sm role_arn must be an IAM role ARN")
		}
		if options.hasStaticCredentials() {
			return awsDestinationOptions{}, validationError("aws-sm static credential fields require auth_mode static")
		}
	case AuthModeStatic:
		return awsDestinationOptions{}, validationError("aws-sm auth_mode static is not supported yet")
	default:
		return awsDestinationOptions{}, validationError(
			"aws-sm auth_mode must be default, assume_role, or static",
		)
	}
	return options, nil
}

func (options awsDestinationOptions) hasStaticCredentials() bool {
	return options.accessKeyID != "" || options.secretAccessKey != "" || options.sessionToken != ""
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

func validateEndpointURL(rawEndpoint string, endpointPolicy string) error {
	parsedEndpoint, err := url.Parse(rawEndpoint)
	if err != nil {
		return validationError("aws-sm endpoint_url must be a valid URL")
	}
	if parsedEndpoint.Host == "" || parsedEndpoint.User != nil ||
		parsedEndpoint.RawQuery != "" || parsedEndpoint.Fragment != "" {
		return validationError("aws-sm endpoint_url must include a host and no userinfo, query, or fragment")
	}
	host := normalizeEndpointHost(parsedEndpoint.Hostname())
	if host == "" {
		return validationError("aws-sm endpoint_url must include a host")
	}
	switch endpointPolicy {
	case EndpointPolicyLocal:
		return validateLocalEndpointURL(parsedEndpoint.Scheme, host)
	case EndpointPolicyPrivate:
		return validatePrivateEndpointURL(parsedEndpoint.Scheme, host)
	default:
		return validationError("aws-sm endpoint_policy must be local or private")
	}
}

func validateEndpointResolution(
	ctx context.Context,
	options awsDestinationOptions,
	resolver endpointResolver,
) error {
	if options.endpointURL == "" || options.endpointPolicy != EndpointPolicyPrivate {
		return nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver.LookupNetIP
	}
	parsedEndpoint, err := url.Parse(options.endpointURL)
	if err != nil {
		return validationError("aws-sm endpoint_url must be a valid URL")
	}
	host := normalizeEndpointHost(parsedEndpoint.Hostname())
	if host == "" {
		return validationError("aws-sm endpoint_url must include a host")
	}
	if addr, ok := parseEndpointAddr(host); ok {
		if isUnsafeEndpointAddr(addr) {
			return validationError(
				"aws-sm private endpoint_url DNS must not resolve to loopback, link-local, multicast, or unspecified addresses",
			)
		}
		return nil
	}
	resolveCtx, cancel := context.WithTimeout(ctx, endpointResolutionTimeout)
	defer cancel()
	addrs, err := resolver(resolveCtx, "ip", host)
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
			return validationError(
				"aws-sm private endpoint_url DNS must not resolve to loopback, link-local, multicast, or unspecified addresses",
			)
		}
	}
	return nil
}

func validateLocalEndpointURL(scheme string, host string) error {
	if scheme != "https" && scheme != "http" {
		return validationError("aws-sm local endpoint_url must use http or https")
	}
	if !isLocalEndpointHost(host) {
		return validationError("aws-sm local endpoint_url host must be localhost, loopback, or localstack")
	}
	return nil
}

func validatePrivateEndpointURL(scheme string, host string) error {
	if scheme != "https" {
		return validationError("aws-sm private endpoint_url must use https")
	}
	if isLocalEndpointHost(host) {
		return validationError("aws-sm private endpoint_url must not target local development hosts")
	}
	if addr, ok := parseEndpointAddr(host); ok && isUnsafeEndpointAddr(addr) {
		return validationError(
			"aws-sm private endpoint_url must not target loopback, link-local, multicast, or unspecified addresses",
		)
	}
	return nil
}

func normalizeEndpointHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func isLocalEndpointHost(host string) bool {
	if host == "localhost" || host == "localstack" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	addr, ok := parseEndpointAddr(host)
	return ok && addr.IsLoopback()
}

func parseEndpointAddr(host string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}

func isUnsafeEndpointAddr(addr netip.Addr) bool {
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
	AssociationID    string
	SourcePath       string
	ObjectID         string
	PluginInstanceID string
	RestoreEpoch     string
}

func ownershipIdentityFromPlan(req providers.PlanRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID:    req.AssociationID,
		SourcePath:       req.SourcePath,
		ObjectID:         req.ObjectID,
		PluginInstanceID: req.Runtime.PluginInstanceID,
		RestoreEpoch:     req.Runtime.RestoreEpoch,
	}
}

func ownershipIdentityFromUpsert(req providers.UpsertRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID:    req.AssociationID,
		SourcePath:       req.SourcePath,
		ObjectID:         req.ObjectID,
		PluginInstanceID: req.Runtime.PluginInstanceID,
		RestoreEpoch:     req.Runtime.RestoreEpoch,
	}
}

func ownershipIdentityFromDelete(req providers.DeleteRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID:    req.AssociationID,
		SourcePath:       req.SourcePath,
		ObjectID:         req.ObjectID,
		PluginInstanceID: req.Runtime.PluginInstanceID,
		RestoreEpoch:     req.Runtime.RestoreEpoch,
	}
}

func ownershipIdentityFromReadState(req providers.ReadStateRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID:    req.AssociationID,
		SourcePath:       req.SourcePath,
		ObjectID:         req.ObjectID,
		PluginInstanceID: req.Runtime.PluginInstanceID,
		RestoreEpoch:     req.Runtime.RestoreEpoch,
	}
}

func hasOwnershipIdentityFromReadState(req providers.ReadStateRequest) bool {
	return req.AssociationID != "" && req.SourcePath != "" && req.ObjectID != ""
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

func ownedByRequest(tags []smtypes.Tag, identity ownershipIdentity) bool {
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
