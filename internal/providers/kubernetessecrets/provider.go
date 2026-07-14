// Package kubernetessecrets provides the Kubernetes Secret destination provider.
package kubernetessecrets

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/endpointguard"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/providerutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// ProviderType is the stable destination type used by associations.
	ProviderType = "k8s"

	// ConfigKeyNamespace configures the target Kubernetes namespace.
	ConfigKeyNamespace = "namespace"
	// ConfigKeyAuthMode selects Kubernetes credential behavior.
	ConfigKeyAuthMode = "auth_mode"
	// ConfigKeyKubeconfigPath configures a kubeconfig file for local or external clusters.
	ConfigKeyKubeconfigPath = "kubeconfig_path"
	// ConfigKeyKubeContext selects a kubeconfig context.
	ConfigKeyKubeContext = "context"
	// ConfigKeyAPIServer configures the Kubernetes API server for token auth.
	ConfigKeyAPIServer = "api_server"
	// ConfigKeyAllowPrivateAPIServer allows token auth api_server on local or private networks.
	ConfigKeyAllowPrivateAPIServer = "allow_private_api_server"
	// ConfigKeyToken configures the Kubernetes bearer token for token auth.
	ConfigKeyToken = "token"
	// ConfigKeyCACertPEM configures the Kubernetes API CA bundle for token auth.
	ConfigKeyCACertPEM = "ca_cert_pem"
	// ConfigKeyTLSServerName configures the Kubernetes API TLS server name for token auth.
	ConfigKeyTLSServerName = "tls_server_name"

	// AuthModeInCluster uses the mounted Kubernetes service account.
	AuthModeInCluster = "in_cluster"
	// AuthModeKubeconfig loads credentials from a kubeconfig file.
	AuthModeKubeconfig = "kubeconfig"
	// AuthModeToken uses an explicitly configured Kubernetes bearer token.
	AuthModeToken = "token"

	secretMaxBytes        = 1024 * 1024
	dataKeyPayload        = "payload"
	healthCheckSecretName = "openbao-secret-sync-health-check"
	defaultRequestTimeout = 30 * time.Second

	labelManaged = "openbao.adfinis.com/managed"

	annotationAssociationID  = "openbao.adfinis.com/association-id"
	annotationSourcePath     = "openbao.adfinis.com/source-path"
	annotationSourceVersion  = "openbao.adfinis.com/source-version"
	annotationObjectID       = "openbao.adfinis.com/object-id"
	annotationPayloadSHA256  = "openbao.adfinis.com/payload-sha256"
	annotationFormat         = "openbao.adfinis.com/format"
	annotationDataKeys       = "openbao.adfinis.com/data-keys"
	annotationPluginInstance = "openbao.adfinis.com/plugin-instance"
	annotationRestoreEpoch   = "openbao.adfinis.com/restore-epoch"
)

var providerHelpers = providerutil.New(ProviderType)

type clientFactory func(context.Context, providers.DestinationConfig) (kubernetes.Interface, error)

// Provider is the Kubernetes Secret provider.
type Provider struct {
	client        kubernetes.Interface
	clientFactory clientFactory
}

type destinationRuntime struct {
	client  kubernetes.Interface
	options kubernetesDestinationOptions
}

// New returns a provider using in-cluster or kubeconfig client construction.
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
		SupportsDataMap:             true,
		MaxPayloadBytes:             secretMaxBytes,
	}
}

func (Provider) ValidateConfig(_ context.Context, cfg providers.DestinationConfig) error {
	_, err := kubernetesDestinationOptionsFromConfig(cfg)
	return err
}

func (Provider) NormalizeAssociationConfig(
	_ context.Context,
	_ providers.DestinationConfig,
	cfg providers.AssociationConfig,
) (providers.AssociationConfig, error) {
	if len(cfg.Config) > 0 {
		return providers.AssociationConfig{}, providerHelpers.ValidationError(
			"k8s does not support association configuration",
		)
	}
	return providers.AssociationConfig{Config: map[string]string{}}, nil
}

func (p Provider) OpenDestination(
	ctx context.Context,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	options, err := kubernetesDestinationOptionsFromConfig(cfg)
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
	_, err := r.secretClient().Get(ctx, healthCheckSecretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "k8s health check failed",
			ErrorClass: classifyKubernetesError(err),
		}, nil
	}
	return &providers.HealthResult{Healthy: true}, nil
}

func (r destinationRuntime) Plan(ctx context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	secretClient := r.secretClient()
	if err := validateSecretName(req.ResolvedName); err != nil {
		return providerHelpers.BlockedPlan(providers.ErrorClassValidation), nil
	}
	if err := validateDataMapKeys(req.DataMapKeys); err != nil {
		return &providers.PlanResult{
			Action:     providers.PlanActionBlocked,
			ErrorClass: providers.ErrorClassValidation,
			Message:    err.Error(),
		}, nil
	}
	secret, err := secretClient.Get(ctx, req.ResolvedName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
	}
	if err != nil {
		return providerHelpers.BlockedPlan(classifyKubernetesError(err)), nil
	}
	if !ownedByRequest(secret, req.OwnershipIdentity()) {
		return &providers.PlanResult{
			Action:     providers.PlanActionConflict,
			ErrorClass: providers.ErrorClassCollision,
			Message:    "k8s secret exists but is not owned by this association",
		}, nil
	}
	if remoteSourceVersionNewer(secret.Annotations, req.SourceVersion) {
		return &providers.PlanResult{
			Action:     providers.PlanActionBlocked,
			ErrorClass: providers.ErrorClassDrift,
			Message:    "k8s secret has newer managed source version",
		}, nil
	}
	if req.DataMap {
		managedKeys, err := managedDataKeysForMutation(secret)
		if err != nil {
			return &providers.PlanResult{
				Action:     providers.PlanActionBlocked,
				ErrorClass: providers.ErrorClassDrift,
				Message:    "k8s data key metadata is invalid",
			}, nil
		}
		unmanagedConflicts := unmanagedDataKeyConflicts(secret, managedKeys, req.DataMapKeys)
		if len(unmanagedConflicts) > 0 {
			return &providers.PlanResult{
				Action:     providers.PlanActionConflict,
				ErrorClass: providers.ErrorClassOwnership,
				Message:    "k8s data key exists but is not owned by this association",
			}, nil
		}
	}
	payloadMatches := payloadMatchesRequest(secret, req.DataMap, req.PayloadSHA256)
	if payloadMatches && managedMetadataMatches(
		secret,
		req.SourceVersion,
		req.PayloadSHA256,
		req.Format,
		req.DataMap,
		req.DataMapKeys,
	) {
		return &providers.PlanResult{Action: providers.PlanActionNoop}, nil
	}
	if !payloadMatches && isImmutable(secret) {
		return &providers.PlanResult{
			Action:     providers.PlanActionBlocked,
			ErrorClass: providers.ErrorClassValidation,
			Message:    "k8s secret is immutable",
		}, nil
	}
	return &providers.PlanResult{Action: providers.PlanActionUpdate}, nil
}

func (r destinationRuntime) Upsert(ctx context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	secretClient := r.secretClient()
	if err := validateSecretName(req.ResolvedName); err != nil {
		return nil, err
	}
	if len(req.Payload) > secretMaxBytes {
		return nil, providerHelpers.ProviderError(providers.ErrorClassCapacity)
	}
	if len(req.DataMap) > 0 {
		return r.upsertDataMap(ctx, req)
	}
	secret, err := secretClient.Get(ctx, req.ResolvedName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return createSecret(ctx, secretClient, r.options.namespace, req)
	}
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	if !ownedByRequest(secret, req.OwnershipIdentity()) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(secret.Annotations, req.SourceVersion) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassDrift)
	}
	payloadMatches := payloadMatchesRequest(secret, false, req.PayloadSHA256)
	if payloadMatches && managedMetadataMatches(
		secret,
		req.SourceVersion,
		req.PayloadSHA256,
		req.Format,
		false,
		nil,
	) {
		return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
	}
	if !payloadMatches && isImmutable(secret) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassValidation)
	}
	updated := secret.DeepCopy()
	if !payloadMatches {
		updated.Type = corev1.SecretTypeOpaque
		updated.Data = map[string][]byte{dataKeyPayload: req.Payload}
	}
	applyOwnershipMetadata(updated, req.OwnershipIdentity(), req.SourceVersion, req.PayloadSHA256, req.Format)
	removeDataMapMetadata(updated)
	result, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: result.ResourceVersion}, nil
}

func (r destinationRuntime) Delete(ctx context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	secretClient := r.secretClient()
	if err := validateSecretName(req.ResolvedName); err != nil {
		return nil, err
	}
	secret, err := secretClient.Get(ctx, req.ResolvedName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &providers.SyncResult{RemoteVersion: "missing"}, nil
	}
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	if !ownedByRequest(secret, req.OwnershipIdentity()) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(secret.Annotations, req.SourceVersion) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassDrift)
	}
	if req.DataMap {
		return r.deleteDataMap(ctx, secret, req)
	}
	if err := secretClient.Delete(ctx, req.ResolvedName, deleteOptionsForSecret(secret)); err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
}

func (r destinationRuntime) ReadState(
	ctx context.Context,
	req providers.ReadStateRequest,
) (*providers.RemoteState, error) {
	secretClient := r.secretClient()
	if err := validateSecretName(req.ResolvedName); err != nil {
		return nil, err
	}
	secret, err := secretClient.Get(ctx, req.ResolvedName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &providers.RemoteState{Exists: false}, nil
	}
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	sourceVersion, _ := strconv.Atoi(annotationValue(secret.Annotations, annotationSourceVersion))
	return &providers.RemoteState{
		Exists:         true,
		OwnershipKnown: req.OwnershipIdentity().Complete(),
		Owned:          ownedByRequest(secret, req.OwnershipIdentity()),
		PayloadSHA256:  payloadSHA256ForMode(secret, req.DataMap),
		SourceVersion:  sourceVersion,
		RemoteVersion:  secret.ResourceVersion,
		Verification:   providers.RemoteStateVerificationValue,
	}, nil
}

func (r destinationRuntime) secretClient() typedcorev1.SecretInterface {
	return r.client.CoreV1().Secrets(r.options.namespace)
}

func (destinationRuntime) Close(context.Context) error {
	return nil
}

func (p Provider) clientFor(ctx context.Context, cfg providers.DestinationConfig) (kubernetes.Interface, error) {
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
) (kubernetes.Interface, error) {
	return defaultClientFactoryWithResolver(ctx, providerConfig, nil)
}

func defaultClientFactoryWithResolver(
	ctx context.Context,
	providerConfig providers.DestinationConfig,
	resolver endpointguard.Resolver,
) (kubernetes.Interface, error) {
	options, err := kubernetesDestinationOptionsFromConfig(providerConfig)
	if err != nil {
		return nil, err
	}
	var restConfig *rest.Config
	switch options.authMode {
	case AuthModeInCluster:
		restConfig, err = rest.InClusterConfig()
	case AuthModeKubeconfig:
		loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: options.kubeconfigPath}
		overrides := &clientcmd.ConfigOverrides{CurrentContext: options.kubeContext}
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			overrides,
		).ClientConfig()
	case AuthModeToken:
		if err := validateAPIServerResolution(ctx, options, resolver); err != nil {
			return nil, err
		}
		restConfig = restConfigForToken(options)
	default:
		err = providerHelpers.ValidationError("k8s auth_mode must be in_cluster, kubeconfig, or token")
	}
	if err != nil {
		return nil, err
	}
	hardenRESTConfig(restConfig, options, resolver)
	return kubernetes.NewForConfig(restConfig)
}

type kubernetesDestinationOptions struct {
	namespace       string
	authMode        string
	kubeconfigPath  string
	kubeContext     string
	apiServer       string
	allowPrivateAPI bool
	allowPrivateSet bool
	token           string
	caCertPEM       string
	tlsServerName   string
}

func kubernetesDestinationOptionsFromConfig(
	cfg providers.DestinationConfig,
) (kubernetesDestinationOptions, error) {
	options := kubernetesDestinationOptions{
		namespace:       providerHelpers.ConfigValue(cfg, ConfigKeyNamespace),
		authMode:        normalizedAuthMode(cfg),
		kubeconfigPath:  providerHelpers.ConfigValue(cfg, ConfigKeyKubeconfigPath),
		kubeContext:     providerHelpers.ConfigValue(cfg, ConfigKeyKubeContext),
		apiServer:       providerHelpers.ConfigValue(cfg, ConfigKeyAPIServer),
		allowPrivateSet: providerHelpers.ConfigValue(cfg, ConfigKeyAllowPrivateAPIServer) != "",
		token:           providerHelpers.ConfigValue(cfg, ConfigKeyToken),
		caCertPEM:       providerHelpers.ConfigValue(cfg, ConfigKeyCACertPEM),
		tlsServerName:   providerHelpers.ConfigValue(cfg, ConfigKeyTLSServerName),
	}
	var err error
	if options.allowPrivateAPI, err = providerHelpers.BoolConfigValue(
		cfg,
		ConfigKeyAllowPrivateAPIServer,
		false,
	); err != nil {
		return kubernetesDestinationOptions{}, err
	}
	if cfg.Name == "" {
		return kubernetesDestinationOptions{}, providerHelpers.ValidationError("k8s destination name must not be empty")
	}
	if err := validateNamespace(options.namespace); err != nil {
		return kubernetesDestinationOptions{}, err
	}
	if err := options.validateAuthMode(); err != nil {
		return kubernetesDestinationOptions{}, err
	}
	return options, nil
}

func (options kubernetesDestinationOptions) validateAuthMode() error {
	switch options.authMode {
	case AuthModeInCluster:
		return options.validateInClusterAuth()
	case AuthModeKubeconfig:
		return options.validateKubeconfigAuth()
	case AuthModeToken:
		return options.validateTokenAuth()
	default:
		return providerHelpers.ValidationError("k8s auth_mode must be in_cluster, kubeconfig, or token")
	}
}

func (options kubernetesDestinationOptions) validateInClusterAuth() error {
	if options.kubeconfigPath != "" || options.kubeContext != "" || options.hasTokenAuthConfig() {
		return providerHelpers.ValidationError("k8s kubeconfig or token fields require matching auth_mode")
	}
	return nil
}

func (options kubernetesDestinationOptions) validateKubeconfigAuth() error {
	if options.kubeconfigPath == "" {
		return providerHelpers.ValidationError("k8s auth_mode kubeconfig requires kubeconfig_path")
	}
	if options.hasTokenAuthConfig() {
		return providerHelpers.ValidationError("k8s token fields require auth_mode token")
	}
	return nil
}

func (options kubernetesDestinationOptions) validateTokenAuth() error {
	if options.kubeconfigPath != "" || options.kubeContext != "" {
		return providerHelpers.ValidationError("k8s kubeconfig fields require auth_mode kubeconfig")
	}
	if options.apiServer == "" {
		return providerHelpers.ValidationError("k8s auth_mode token requires api_server")
	}
	if err := validateAPIServer(options.apiServer, options.allowPrivateAPI); err != nil {
		return err
	}
	if options.token == "" {
		return providerHelpers.ValidationError("k8s auth_mode token requires token")
	}
	return validateCACertPEM(options.caCertPEM)
}

func normalizedAuthMode(cfg providers.DestinationConfig) string {
	authMode := providerHelpers.ConfigValue(cfg, ConfigKeyAuthMode)
	if authMode != "" {
		return authMode
	}
	if providerHelpers.ConfigValue(cfg, ConfigKeyAPIServer) != "" ||
		providerHelpers.ConfigValue(cfg, ConfigKeyToken) != "" {
		return AuthModeToken
	}
	if providerHelpers.ConfigValue(cfg, ConfigKeyKubeconfigPath) != "" {
		return AuthModeKubeconfig
	}
	return AuthModeInCluster
}

func (options kubernetesDestinationOptions) hasTokenAuthConfig() bool {
	return options.apiServer != "" ||
		options.allowPrivateSet ||
		options.token != "" ||
		options.caCertPEM != "" ||
		options.tlsServerName != ""
}

func restConfigForToken(options kubernetesDestinationOptions) *rest.Config {
	return &rest.Config{
		Host:        options.apiServer,
		BearerToken: options.token,
		TLSClientConfig: rest.TLSClientConfig{
			CAData:     []byte(options.caCertPEM),
			ServerName: options.tlsServerName,
		},
	}
}

func hardenRESTConfig(
	restConfig *rest.Config,
	options kubernetesDestinationOptions,
	resolver endpointguard.Resolver,
) {
	if restConfig.Timeout <= 0 {
		restConfig.Timeout = defaultRequestTimeout
	}
	if options.authMode != AuthModeToken || options.allowPrivateAPI {
		return
	}
	restConfig.Dial = endpointguard.GuardedDialContext(
		resolver,
		func(addr netip.Addr) bool { return !endpointguard.IsRestrictedAddr(addr) },
	)
	// A proxy would resolve api_server outside the guarded dial path.
	restConfig.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
}

func validateNamespace(namespace string) error {
	if namespace == "" {
		return providerHelpers.ValidationError("k8s namespace must not be empty")
	}
	if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
		return providerHelpers.ValidationError(fmt.Sprintf("k8s namespace is invalid: %s", strings.Join(errs, "; ")))
	}
	return nil
}

func validateAPIServer(apiServer string, allowPrivateAPI bool) error {
	parsed, err := url.Parse(apiServer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return providerHelpers.ValidationError("k8s api_server must be an absolute URL")
	}
	if parsed.Scheme != "https" {
		return providerHelpers.ValidationError("k8s api_server must use https")
	}
	host := endpointguard.NormalizeHost(parsed.Hostname())
	if host == "" {
		return providerHelpers.ValidationError("k8s api_server must include a host")
	}
	if !allowPrivateAPI && endpointguard.IsRestrictedHost(host) {
		return providerHelpers.ValidationError(
			"k8s api_server requires allow_private_api_server=true for localhost, private, link-local, " +
				"multicast, or unspecified hosts",
		)
	}
	return nil
}

func validateAPIServerResolution(
	ctx context.Context,
	options kubernetesDestinationOptions,
	resolver endpointguard.Resolver,
) error {
	if options.authMode != AuthModeToken || options.allowPrivateAPI {
		return nil
	}
	parsed, err := url.Parse(options.apiServer)
	if err != nil {
		return providerHelpers.ValidationError("k8s api_server must be an absolute URL")
	}
	host := endpointguard.NormalizeHost(parsed.Hostname())
	if host == "" {
		return providerHelpers.ValidationError("k8s api_server must include a host")
	}
	if _, ok := endpointguard.ParseAddr(host); ok {
		return nil
	}
	addrs, err := endpointguard.LookupNetIP(ctx, host, resolver)
	if err != nil {
		return &providers.Error{
			Class:   providers.ErrorClassUnavailable,
			Message: "k8s api_server DNS lookup failed",
		}
	}
	if len(addrs) == 0 {
		return &providers.Error{
			Class:   providers.ErrorClassUnavailable,
			Message: "k8s api_server DNS lookup returned no addresses",
		}
	}
	for _, addr := range addrs {
		if endpointguard.IsRestrictedAddr(addr) {
			return providerHelpers.ValidationError(
				"k8s api_server DNS must not resolve to localhost, private, link-local, multicast, or unspecified addresses",
			)
		}
	}
	return nil
}

func validateCACertPEM(caCertPEM string) error {
	if caCertPEM == "" {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caCertPEM)) {
		return providerHelpers.ValidationError("k8s ca_cert_pem must contain at least one PEM certificate")
	}
	return nil
}

func validateSecretName(name string) error {
	if name == "" {
		return providerHelpers.ValidationError("k8s resolved secret name must not be empty")
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return providerHelpers.ValidationError(fmt.Sprintf(
			"k8s resolved secret name is invalid: %s",
			strings.Join(errs, "; "),
		))
	}
	return nil
}

func createSecret(
	ctx context.Context,
	secretClient typedcorev1.SecretInterface,
	namespace string,
	req providers.UpsertRequest,
) (*providers.SyncResult, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.ResolvedName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{dataKeyPayload: req.Payload},
	}
	applyOwnershipMetadata(secret, req.OwnershipIdentity(), req.SourceVersion, req.PayloadSHA256, req.Format)
	result, err := secretClient.Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: result.ResourceVersion}, nil
}

func (r destinationRuntime) upsertDataMap(
	ctx context.Context,
	req providers.UpsertRequest,
) (*providers.SyncResult, error) {
	if err := validateDataMap(req.DataMap); err != nil {
		return nil, err
	}
	secretClient := r.secretClient()
	secret, err := secretClient.Get(ctx, req.ResolvedName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return createDataMapSecret(ctx, secretClient, r.options.namespace, req)
	}
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	if !ownedByRequest(secret, req.OwnershipIdentity()) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(secret.Annotations, req.SourceVersion) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassDrift)
	}
	desiredKeys := dataMapKeys(req.DataMap)
	payloadMatches := payloadMatchesRequest(secret, true, req.PayloadSHA256)
	if payloadMatches && managedMetadataMatches(
		secret,
		req.SourceVersion,
		req.PayloadSHA256,
		req.Format,
		true,
		desiredKeys,
	) {
		return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
	}
	if !payloadMatches && isImmutable(secret) {
		return nil, providerHelpers.ProviderError(providers.ErrorClassValidation)
	}
	managedKeys, err := managedDataKeysForMutation(secret)
	if err != nil {
		return nil, providerHelpers.ProviderError(providers.ErrorClassDrift)
	}
	unmanagedConflicts := unmanagedDataKeyConflicts(secret, managedKeys, dataMapKeys(req.DataMap))
	if len(unmanagedConflicts) > 0 {
		return nil, providerHelpers.ProviderError(providers.ErrorClassOwnership)
	}
	updated := secret.DeepCopy()
	updated.Type = corev1.SecretTypeOpaque
	updated.Data = mergedDataMap(secret.Data, managedKeys, req.DataMap)
	applyOwnershipMetadata(updated, req.OwnershipIdentity(), req.SourceVersion, req.PayloadSHA256, req.Format)
	applyDataMapMetadata(updated, desiredKeys)
	result, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: result.ResourceVersion}, nil
}

func createDataMapSecret(
	ctx context.Context,
	secretClient typedcorev1.SecretInterface,
	namespace string,
	req providers.UpsertRequest,
) (*providers.SyncResult, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.ResolvedName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: copyDataMap(req.DataMap),
	}
	applyOwnershipMetadata(secret, req.OwnershipIdentity(), req.SourceVersion, req.PayloadSHA256, req.Format)
	applyDataMapMetadata(secret, dataMapKeys(req.DataMap))
	result, err := secretClient.Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: result.ResourceVersion}, nil
}

func (r destinationRuntime) deleteDataMap(
	ctx context.Context,
	secret *corev1.Secret,
	req providers.DeleteRequest,
) (*providers.SyncResult, error) {
	managedKeys, err := managedDataKeysForMutation(secret)
	if err != nil {
		return nil, providerHelpers.ProviderError(providers.ErrorClassDrift)
	}
	secretClient := r.secretClient()
	updatedData := mergedDataMap(secret.Data, managedKeys, nil)
	if len(updatedData) == 0 {
		if err := secretClient.Delete(ctx, req.ResolvedName, deleteOptionsForSecret(secret)); err != nil {
			return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
		}
		return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
	}
	updated := secret.DeepCopy()
	updated.Data = updatedData
	removeOwnershipMetadata(updated)
	result, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, providerHelpers.ProviderError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: result.ResourceVersion}, nil
}

func applyOwnershipMetadata(
	secret *corev1.Secret,
	identity providers.RequestIdentity,
	sourceVersion int,
	payloadSHA256 string,
	format string,
) {
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Labels[labelManaged] = "true"
	secret.Annotations[annotationAssociationID] = identity.AssociationID
	secret.Annotations[annotationSourcePath] = identity.SourcePath
	secret.Annotations[annotationSourceVersion] = strconv.Itoa(sourceVersion)
	secret.Annotations[annotationObjectID] = identity.ObjectID
	secret.Annotations[annotationPayloadSHA256] = payloadSHA256
	secret.Annotations[annotationFormat] = format
	if identity.PluginInstanceID != "" {
		secret.Annotations[annotationPluginInstance] = identity.PluginInstanceID
	}
	if identity.RestoreEpoch != "" {
		secret.Annotations[annotationRestoreEpoch] = identity.RestoreEpoch
	}
}

func applyDataMapMetadata(secret *corev1.Secret, dataKeys []string) {
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	payload, err := json.Marshal(dataKeys)
	if err != nil {
		return
	}
	secret.Annotations[annotationDataKeys] = string(payload)
}

func removeDataMapMetadata(secret *corev1.Secret) {
	delete(secret.Annotations, annotationDataKeys)
}

func removeOwnershipMetadata(secret *corev1.Secret) {
	for _, key := range []string{
		annotationAssociationID,
		annotationSourcePath,
		annotationSourceVersion,
		annotationObjectID,
		annotationPayloadSHA256,
		annotationFormat,
		annotationDataKeys,
		annotationPluginInstance,
		annotationRestoreEpoch,
	} {
		delete(secret.Annotations, key)
	}
	delete(secret.Labels, labelManaged)
	if len(secret.Annotations) == 0 {
		secret.Annotations = nil
	}
	if len(secret.Labels) == 0 {
		secret.Labels = nil
	}
}

func ownedByRequest(secret *corev1.Secret, identity providers.RequestIdentity) bool {
	if secret == nil || secret.Labels[labelManaged] != "true" {
		return false
	}
	return requiredAnnotationMatches(secret.Annotations, annotationAssociationID, identity.AssociationID) &&
		requiredAnnotationMatches(secret.Annotations, annotationSourcePath, identity.SourcePath) &&
		requiredAnnotationMatches(secret.Annotations, annotationObjectID, identity.ObjectID) &&
		runtimeAnnotationMatches(secret.Annotations, annotationPluginInstance, identity.PluginInstanceID) &&
		runtimeAnnotationMatches(secret.Annotations, annotationRestoreEpoch, identity.RestoreEpoch)
}

func requiredAnnotationMatches(annotations map[string]string, key string, expected string) bool {
	return expected != "" && annotationValue(annotations, key) == expected
}

func runtimeAnnotationMatches(annotations map[string]string, key string, expected string) bool {
	if expected == "" {
		return true
	}
	return annotationValue(annotations, key) == expected
}

func annotationValue(annotations map[string]string, key string) string {
	if annotations == nil {
		return ""
	}
	return annotations[key]
}

func remoteSourceVersionNewer(annotations map[string]string, sourceVersion int) bool {
	if sourceVersion <= 0 {
		return false
	}
	remoteVersion, err := strconv.Atoi(annotationValue(annotations, annotationSourceVersion))
	if err != nil {
		return false
	}
	return remoteVersion > sourceVersion
}

func payloadSHA256ForMode(secret *corev1.Secret, dataMap bool) string {
	if dataMap {
		return dataMapPayloadSHA256(secret)
	}
	return payloadSHA256(secret)
}

func payloadMatchesRequest(secret *corev1.Secret, dataMap bool, payloadSHA256 string) bool {
	if payloadSHA256ForMode(secret, dataMap) != payloadSHA256 {
		return false
	}
	if dataMap {
		return true
	}
	return len(secret.Data) == 1
}

func managedMetadataMatches(
	secret *corev1.Secret,
	sourceVersion int,
	payloadSHA256 string,
	format string,
	dataMap bool,
	desiredDataKeys []string,
) bool {
	if secret.Type != corev1.SecretTypeOpaque ||
		annotationValue(secret.Annotations, annotationSourceVersion) != strconv.Itoa(sourceVersion) ||
		annotationValue(secret.Annotations, annotationPayloadSHA256) != payloadSHA256 ||
		annotationValue(secret.Annotations, annotationFormat) != format {
		return false
	}
	if !dataMap {
		return annotationValue(secret.Annotations, annotationDataKeys) == ""
	}
	managedKeys, err := managedDataKeys(secret)
	desiredKeys := append([]string(nil), desiredDataKeys...)
	sort.Strings(desiredKeys)
	return err == nil && equalStrings(managedKeys, desiredKeys)
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func deleteOptionsForSecret(secret *corev1.Secret) metav1.DeleteOptions {
	if secret == nil {
		return metav1.DeleteOptions{}
	}
	preconditions := &metav1.Preconditions{}
	if secret.UID != "" {
		uid := secret.UID
		preconditions.UID = &uid
	}
	if secret.ResourceVersion != "" {
		resourceVersion := secret.ResourceVersion
		preconditions.ResourceVersion = &resourceVersion
	}
	if preconditions.UID == nil && preconditions.ResourceVersion == nil {
		return metav1.DeleteOptions{}
	}
	return metav1.DeleteOptions{Preconditions: preconditions}
}

func payloadSHA256(secret *corev1.Secret) string {
	if len(secret.Data) == 0 {
		return ""
	}
	payload, ok := secret.Data[dataKeyPayload]
	if ok && len(secret.Data) == 1 {
		return payloadSHA256Bytes(payload)
	}
	dataMapPayload, err := payloadpkg.BuildDataMap(secret.Data)
	if err != nil {
		return ""
	}
	return dataMapPayload.SHA256
}

func dataMapPayloadSHA256(secret *corev1.Secret) string {
	keys, err := managedDataKeys(secret)
	if err != nil || len(keys) == 0 {
		return ""
	}
	data := map[string][]byte{}
	for _, key := range keys {
		value, ok := secret.Data[key]
		if !ok {
			continue
		}
		data[key] = value
	}
	if len(data) == 0 {
		return ""
	}
	payload, err := payloadpkg.BuildDataMap(data)
	if err != nil {
		return ""
	}
	return payload.SHA256
}

func payloadSHA256Bytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateDataMap(data map[string][]byte) error {
	if len(data) == 0 {
		return providerHelpers.ValidationError("k8s data-map payload must contain at least one key")
	}
	return validateDataMapKeys(dataMapKeys(data))
}

func validateDataMapKeys(keys []string) error {
	for _, key := range keys {
		if errs := validation.IsConfigMapKey(key); len(errs) > 0 {
			return providerHelpers.ValidationError(fmt.Sprintf("k8s data key %q is invalid: %s", key, strings.Join(errs, "; ")))
		}
	}
	return nil
}

func managedDataKeys(secret *corev1.Secret) ([]string, error) {
	value := annotationValue(secret.Annotations, annotationDataKeys)
	if value == "" {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(value), &keys); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

func managedDataKeysForMutation(secret *corev1.Secret) ([]string, error) {
	keys, err := managedDataKeys(secret)
	if err != nil || len(keys) > 0 {
		return keys, err
	}
	if annotationValue(secret.Annotations, annotationFormat) == payloadpkg.FormatDataMap {
		return nil, fmt.Errorf("managed data keys metadata is missing")
	}
	if annotationValue(secret.Annotations, annotationFormat) != payloadpkg.FormatDataMap {
		if _, ok := secret.Data[dataKeyPayload]; ok {
			return []string{dataKeyPayload}, nil
		}
	}
	return nil, nil
}

func unmanagedDataKeyConflicts(secret *corev1.Secret, managedKeys []string, desiredKeys []string) []string {
	managed := stringSet(managedKeys)
	conflicts := []string{}
	for _, key := range desiredKeys {
		if _, exists := secret.Data[key]; !exists {
			continue
		}
		if _, owned := managed[key]; owned {
			continue
		}
		conflicts = append(conflicts, key)
	}
	return conflicts
}

func mergedDataMap(existing map[string][]byte, managedKeys []string, desired map[string][]byte) map[string][]byte {
	merged := copyDataMap(existing)
	for _, key := range managedKeys {
		delete(merged, key)
	}
	for key, value := range desired {
		merged[key] = append([]byte(nil), value...)
	}
	return merged
}

func copyDataMap(input map[string][]byte) map[string][]byte {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string][]byte, len(input))
	for key, value := range input {
		output[key] = append([]byte(nil), value...)
	}
	return output
}

func dataMapKeys(data map[string][]byte) []string {
	if len(data) == 0 {
		return nil
	}
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func isImmutable(secret *corev1.Secret) bool {
	return secret.Immutable != nil && *secret.Immutable
}

func classifyKubernetesError(err error) providers.ErrorClass {
	if errorClass, ok := providers.ClassifyTransportError(err); ok {
		return errorClass
	}
	switch {
	case apierrors.IsTooManyRequests(err):
		return providers.ErrorClassRateLimit
	case apierrors.IsUnauthorized(err):
		return providers.ErrorClassAuthn
	case apierrors.IsForbidden(err):
		return providers.ErrorClassAuthz
	case apierrors.IsServerTimeout(err), apierrors.IsTimeout(err), apierrors.IsServiceUnavailable(err):
		return providers.ErrorClassUnavailable
	case apierrors.IsAlreadyExists(err):
		return providers.ErrorClassCollision
	case apierrors.IsConflict(err):
		return providers.ErrorClassDrift
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err), apierrors.IsNotFound(err):
		return providers.ErrorClassValidation
	case apierrors.IsRequestEntityTooLargeError(err):
		return providers.ErrorClassCapacity
	default:
		return providers.ErrorClassInternal
	}
}
