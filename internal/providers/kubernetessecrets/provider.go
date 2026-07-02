// Package kubernetessecrets provides the Kubernetes Secret destination provider.
package kubernetessecrets

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
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

	secretMaxBytes = 1024 * 1024
	dataKeyPayload = "payload"

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
		return nil, providerError(setupErrorClass(err))
	}
	return destinationRuntime{client: client, options: options}, nil
}

func (r destinationRuntime) Health(ctx context.Context) (*providers.HealthResult, error) {
	if _, err := r.secretClient().List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
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
		return blockedPlan(providers.ErrorClassValidation), nil
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
		return blockedPlan(classifyKubernetesError(err)), nil
	}
	if !ownedByRequest(secret, ownershipIdentityFromPlan(req)) {
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
	if payloadSHA256ForMode(secret, req.DataMap) == req.PayloadSHA256 {
		return &providers.PlanResult{Action: providers.PlanActionNoop}, nil
	}
	if isImmutable(secret) {
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
		return nil, providerError(providers.ErrorClassCapacity)
	}
	if len(req.DataMap) > 0 {
		return r.upsertDataMap(ctx, req)
	}
	secret, err := secretClient.Get(ctx, req.ResolvedName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return createSecret(ctx, secretClient, r.options.namespace, req)
	}
	if err != nil {
		return nil, providerError(classifyKubernetesError(err))
	}
	if !ownedByRequest(secret, ownershipIdentityFromUpsert(req)) {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(secret.Annotations, req.SourceVersion) {
		return nil, providerError(providers.ErrorClassDrift)
	}
	if payloadSHA256ForMode(secret, false) == req.PayloadSHA256 {
		return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
	}
	if isImmutable(secret) {
		return nil, providerError(providers.ErrorClassValidation)
	}
	updated := secret.DeepCopy()
	updated.Type = corev1.SecretTypeOpaque
	updated.Data = map[string][]byte{dataKeyPayload: req.Payload}
	applyOwnershipMetadata(updated, ownershipIdentityFromUpsert(req), req.SourceVersion, req.PayloadSHA256, req.Format)
	result, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, providerError(classifyKubernetesError(err))
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
		return nil, providerError(classifyKubernetesError(err))
	}
	if !ownedByRequest(secret, ownershipIdentityFromDelete(req)) {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(secret.Annotations, req.SourceVersion) {
		return nil, providerError(providers.ErrorClassDrift)
	}
	if req.DataMap {
		return r.deleteDataMap(ctx, secret, req)
	}
	if err := secretClient.Delete(ctx, req.ResolvedName, metav1.DeleteOptions{}); err != nil {
		return nil, providerError(classifyKubernetesError(err))
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
		return nil, providerError(classifyKubernetesError(err))
	}
	sourceVersion, _ := strconv.Atoi(annotationValue(secret.Annotations, annotationSourceVersion))
	return &providers.RemoteState{
		Exists:         true,
		OwnershipKnown: hasOwnershipIdentityFromReadState(req),
		Owned:          ownedByRequest(secret, ownershipIdentityFromReadState(req)),
		PayloadSHA256:  payloadSHA256ForMode(secret, req.DataMap),
		SourceVersion:  sourceVersion,
		RemoteVersion:  secret.ResourceVersion,
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
	_ context.Context,
	providerConfig providers.DestinationConfig,
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
		restConfig = restConfigForToken(options)
	default:
		err = validationError("k8s auth_mode must be in_cluster, kubeconfig, or token")
	}
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restConfig)
}

type kubernetesDestinationOptions struct {
	namespace      string
	authMode       string
	kubeconfigPath string
	kubeContext    string
	apiServer      string
	token          string
	caCertPEM      string
	tlsServerName  string
}

func kubernetesDestinationOptionsFromConfig(
	cfg providers.DestinationConfig,
) (kubernetesDestinationOptions, error) {
	options := kubernetesDestinationOptions{
		namespace:      configValue(cfg, ConfigKeyNamespace),
		authMode:       normalizedAuthMode(cfg),
		kubeconfigPath: configValue(cfg, ConfigKeyKubeconfigPath),
		kubeContext:    configValue(cfg, ConfigKeyKubeContext),
		apiServer:      configValue(cfg, ConfigKeyAPIServer),
		token:          configValue(cfg, ConfigKeyToken),
		caCertPEM:      configValue(cfg, ConfigKeyCACertPEM),
		tlsServerName:  configValue(cfg, ConfigKeyTLSServerName),
	}
	if cfg.Name == "" {
		return kubernetesDestinationOptions{}, validationError("k8s destination name must not be empty")
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
		return validationError("k8s auth_mode must be in_cluster, kubeconfig, or token")
	}
}

func (options kubernetesDestinationOptions) validateInClusterAuth() error {
	if options.kubeconfigPath != "" || options.kubeContext != "" || options.hasTokenAuthConfig() {
		return validationError("k8s kubeconfig or token fields require matching auth_mode")
	}
	return nil
}

func (options kubernetesDestinationOptions) validateKubeconfigAuth() error {
	if options.kubeconfigPath == "" {
		return validationError("k8s auth_mode kubeconfig requires kubeconfig_path")
	}
	if options.hasTokenAuthConfig() {
		return validationError("k8s token fields require auth_mode token")
	}
	return nil
}

func (options kubernetesDestinationOptions) validateTokenAuth() error {
	if options.kubeconfigPath != "" || options.kubeContext != "" {
		return validationError("k8s kubeconfig fields require auth_mode kubeconfig")
	}
	if options.apiServer == "" {
		return validationError("k8s auth_mode token requires api_server")
	}
	if err := validateAPIServer(options.apiServer); err != nil {
		return err
	}
	if options.token == "" {
		return validationError("k8s auth_mode token requires token")
	}
	return validateCACertPEM(options.caCertPEM)
}

func normalizedAuthMode(cfg providers.DestinationConfig) string {
	authMode := configValue(cfg, ConfigKeyAuthMode)
	if authMode != "" {
		return authMode
	}
	if configValue(cfg, ConfigKeyAPIServer) != "" || configValue(cfg, ConfigKeyToken) != "" {
		return AuthModeToken
	}
	if configValue(cfg, ConfigKeyKubeconfigPath) != "" {
		return AuthModeKubeconfig
	}
	return AuthModeInCluster
}

func (options kubernetesDestinationOptions) hasTokenAuthConfig() bool {
	return options.apiServer != "" ||
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

func configValue(cfg providers.DestinationConfig, key string) string {
	if cfg.Config == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Config[key])
}

func validateNamespace(namespace string) error {
	if namespace == "" {
		return validationError("k8s namespace must not be empty")
	}
	if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
		return validationError(fmt.Sprintf("k8s namespace is invalid: %s", strings.Join(errs, "; ")))
	}
	return nil
}

func validateAPIServer(apiServer string) error {
	parsed, err := url.Parse(apiServer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return validationError("k8s api_server must be an absolute URL")
	}
	if parsed.Scheme != "https" {
		return validationError("k8s api_server must use https")
	}
	return nil
}

func validateCACertPEM(caCertPEM string) error {
	if caCertPEM == "" {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caCertPEM)) {
		return validationError("k8s ca_cert_pem must contain at least one PEM certificate")
	}
	return nil
}

func validateSecretName(name string) error {
	if name == "" {
		return validationError("k8s resolved secret name must not be empty")
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return validationError(fmt.Sprintf("k8s resolved secret name is invalid: %s", strings.Join(errs, "; ")))
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
	applyOwnershipMetadata(secret, ownershipIdentityFromUpsert(req), req.SourceVersion, req.PayloadSHA256, req.Format)
	result, err := secretClient.Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, providerError(classifyKubernetesError(err))
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
		return nil, providerError(classifyKubernetesError(err))
	}
	if !ownedByRequest(secret, ownershipIdentityFromUpsert(req)) {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(secret.Annotations, req.SourceVersion) {
		return nil, providerError(providers.ErrorClassDrift)
	}
	if payloadSHA256ForMode(secret, true) == req.PayloadSHA256 {
		return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
	}
	if isImmutable(secret) {
		return nil, providerError(providers.ErrorClassValidation)
	}
	managedKeys, err := managedDataKeysForMutation(secret)
	if err != nil {
		return nil, providerError(providers.ErrorClassDrift)
	}
	unmanagedConflicts := unmanagedDataKeyConflicts(secret, managedKeys, dataMapKeys(req.DataMap))
	if len(unmanagedConflicts) > 0 {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	updated := secret.DeepCopy()
	updated.Type = corev1.SecretTypeOpaque
	updated.Data = mergedDataMap(secret.Data, managedKeys, req.DataMap)
	applyOwnershipMetadata(updated, ownershipIdentityFromUpsert(req), req.SourceVersion, req.PayloadSHA256, req.Format)
	applyDataMapMetadata(updated, dataMapKeys(req.DataMap))
	result, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, providerError(classifyKubernetesError(err))
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
	applyOwnershipMetadata(secret, ownershipIdentityFromUpsert(req), req.SourceVersion, req.PayloadSHA256, req.Format)
	applyDataMapMetadata(secret, dataMapKeys(req.DataMap))
	result, err := secretClient.Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, providerError(classifyKubernetesError(err))
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
		return nil, providerError(providers.ErrorClassDrift)
	}
	secretClient := r.secretClient()
	updatedData := mergedDataMap(secret.Data, managedKeys, nil)
	if len(updatedData) == 0 {
		if err := secretClient.Delete(ctx, req.ResolvedName, metav1.DeleteOptions{}); err != nil {
			return nil, providerError(classifyKubernetesError(err))
		}
		return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
	}
	updated := secret.DeepCopy()
	updated.Data = updatedData
	removeOwnershipMetadata(updated)
	result, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, providerError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: result.ResourceVersion}, nil
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

func applyOwnershipMetadata(
	secret *corev1.Secret,
	identity ownershipIdentity,
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

func ownedByRequest(secret *corev1.Secret, identity ownershipIdentity) bool {
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

func payloadSHA256(secret *corev1.Secret) string {
	if secret.Data == nil {
		return ""
	}
	payload, ok := secret.Data[dataKeyPayload]
	if !ok {
		return ""
	}
	return payloadSHA256Bytes(payload)
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
		return validationError("k8s data-map payload must contain at least one key")
	}
	return validateDataMapKeys(dataMapKeys(data))
}

func validateDataMapKeys(keys []string) error {
	for _, key := range keys {
		if errs := validation.IsConfigMapKey(key); len(errs) > 0 {
			return validationError(fmt.Sprintf("k8s data key %q is invalid: %s", key, strings.Join(errs, "; ")))
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

func validationError(message string) error {
	return &providers.Error{Class: providers.ErrorClassValidation, Message: message}
}

func blockedPlan(errorClass providers.ErrorClass) *providers.PlanResult {
	return &providers.PlanResult{
		Action:     providers.PlanActionBlocked,
		ErrorClass: errorClass,
		Message:    "k8s provider plan failed",
	}
}

func providerError(errorClass providers.ErrorClass) error {
	return &providers.Error{Class: errorClass, Message: "k8s request failed"}
}

func setupErrorClass(err error) providers.ErrorClass {
	var providerError *providers.Error
	if errors.As(err, &providerError) && providerError.Class != "" {
		return providerError.Class
	}
	return providers.ErrorClassInternal
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
