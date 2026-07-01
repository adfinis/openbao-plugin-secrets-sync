// Package kubernetessecrets provides the Kubernetes Secret destination provider.
package kubernetessecrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

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

	// AuthModeInCluster uses the mounted Kubernetes service account.
	AuthModeInCluster = "in_cluster"
	// AuthModeKubeconfig loads credentials from a kubeconfig file.
	AuthModeKubeconfig = "kubeconfig"

	secretMaxBytes = 1024 * 1024
	dataKeyPayload = "payload"

	labelManaged = "openbao.adfinis.com/managed"

	annotationAssociationID  = "openbao.adfinis.com/association-id"
	annotationSourcePath     = "openbao.adfinis.com/source-path"
	annotationSourceVersion  = "openbao.adfinis.com/source-version"
	annotationObjectID       = "openbao.adfinis.com/object-id"
	annotationPayloadSHA256  = "openbao.adfinis.com/payload-sha256"
	annotationFormat         = "openbao.adfinis.com/format"
	annotationPluginInstance = "openbao.adfinis.com/plugin-instance"
	annotationRestoreEpoch   = "openbao.adfinis.com/restore-epoch"
)

type clientFactory func(context.Context, providers.DestinationConfig) (kubernetes.Interface, error)

// Provider is the Kubernetes Secret provider.
type Provider struct {
	client        kubernetes.Interface
	clientFactory clientFactory
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
		MaxPayloadBytes:             secretMaxBytes,
	}
}

func (Provider) Validate(_ context.Context, cfg providers.DestinationConfig) error {
	_, err := kubernetesDestinationOptionsFromConfig(cfg)
	return err
}

func (p Provider) Plan(ctx context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	_, secretClient, err := p.secretClientFor(ctx, req.Destination)
	if err != nil {
		return blockedPlan(setupErrorClass(err)), nil
	}
	if err := validateSecretName(req.ResolvedName); err != nil {
		return blockedPlan(providers.ErrorClassValidation), nil
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
	if payloadSHA256(secret) == req.PayloadSHA256 {
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

func (p Provider) Upsert(ctx context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	options, secretClient, err := p.secretClientFor(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
	}
	if err := validateSecretName(req.ResolvedName); err != nil {
		return nil, err
	}
	if len(req.Payload) > secretMaxBytes {
		return nil, providerError(providers.ErrorClassCapacity)
	}
	secret, err := secretClient.Get(ctx, req.ResolvedName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return createSecret(ctx, secretClient, options.namespace, req)
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
	if payloadSHA256(secret) == req.PayloadSHA256 {
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

func (p Provider) Delete(ctx context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	_, secretClient, err := p.secretClientFor(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
	}
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
	if err := secretClient.Delete(ctx, req.ResolvedName, metav1.DeleteOptions{}); err != nil {
		return nil, providerError(classifyKubernetesError(err))
	}
	return &providers.SyncResult{RemoteVersion: secret.ResourceVersion}, nil
}

func (p Provider) ReadState(ctx context.Context, req providers.ReadStateRequest) (*providers.RemoteState, error) {
	_, secretClient, err := p.secretClientFor(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
	}
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
		PayloadSHA256:  payloadSHA256(secret),
		SourceVersion:  sourceVersion,
		RemoteVersion:  secret.ResourceVersion,
	}, nil
}

func (p Provider) Health(ctx context.Context, cfg providers.DestinationConfig) (*providers.HealthResult, error) {
	_, secretClient, err := p.secretClientFor(ctx, cfg)
	if err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "k8s client initialization failed",
			ErrorClass: setupErrorClass(err),
		}, nil
	}
	if _, err := secretClient.List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "k8s health check failed",
			ErrorClass: classifyKubernetesError(err),
		}, nil
	}
	return &providers.HealthResult{Healthy: true}, nil
}

func (p Provider) secretClientFor(
	ctx context.Context,
	cfg providers.DestinationConfig,
) (kubernetesDestinationOptions, typedcorev1.SecretInterface, error) {
	options, err := kubernetesDestinationOptionsFromConfig(cfg)
	if err != nil {
		return kubernetesDestinationOptions{}, nil, err
	}
	client, err := p.clientFor(ctx, cfg)
	if err != nil {
		return kubernetesDestinationOptions{}, nil, err
	}
	return options, client.CoreV1().Secrets(options.namespace), nil
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
	default:
		err = validationError("k8s auth_mode must be in_cluster or kubeconfig")
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
}

func kubernetesDestinationOptionsFromConfig(
	cfg providers.DestinationConfig,
) (kubernetesDestinationOptions, error) {
	options := kubernetesDestinationOptions{
		namespace:      configValue(cfg, ConfigKeyNamespace),
		authMode:       normalizedAuthMode(cfg),
		kubeconfigPath: configValue(cfg, ConfigKeyKubeconfigPath),
		kubeContext:    configValue(cfg, ConfigKeyKubeContext),
	}
	if cfg.Name == "" {
		return kubernetesDestinationOptions{}, validationError("k8s destination name must not be empty")
	}
	if err := validateNamespace(options.namespace); err != nil {
		return kubernetesDestinationOptions{}, err
	}
	switch options.authMode {
	case AuthModeInCluster:
		if options.kubeconfigPath != "" || options.kubeContext != "" {
			return kubernetesDestinationOptions{}, validationError(
				"k8s kubeconfig fields require auth_mode kubeconfig",
			)
		}
	case AuthModeKubeconfig:
		if options.kubeconfigPath == "" {
			return kubernetesDestinationOptions{}, validationError(
				"k8s auth_mode kubeconfig requires kubeconfig_path",
			)
		}
	default:
		return kubernetesDestinationOptions{}, validationError("k8s auth_mode must be in_cluster or kubeconfig")
	}
	return options, nil
}

func normalizedAuthMode(cfg providers.DestinationConfig) string {
	authMode := configValue(cfg, ConfigKeyAuthMode)
	if authMode != "" {
		return authMode
	}
	if configValue(cfg, ConfigKeyKubeconfigPath) != "" {
		return AuthModeKubeconfig
	}
	return AuthModeInCluster
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

func payloadSHA256(secret *corev1.Secret) string {
	if value := annotationValue(secret.Annotations, annotationPayloadSHA256); value != "" {
		return value
	}
	if secret.Data == nil {
		return ""
	}
	payload, ok := secret.Data[dataKeyPayload]
	if !ok {
		return ""
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
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
