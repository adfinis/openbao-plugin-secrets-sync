//go:build e2e

package kinde2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/providers/kubernetessecrets"
	"github.com/openbao/openbao/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	pluginName       = "openbao-plugin-secrets-sync"
	pluginVersion    = "v0.0.0-dev"
	mountPath        = "secret-sync"
	rootToken        = "root"
	defaultContext   = "kind-openbao-secret-sync-e2e"
	defaultNamespace = "openbao-secret-sync-e2e"
	dataKeyPayload   = "payload"
	testPollInterval = 500 * time.Millisecond
	testTimeout      = 45 * time.Second
)

func TestOpenBaoPluginSyncsToKubernetesSecrets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baoClient := setupMountedOpenBao(t, ctx)
	namespace := env("E2E_KIND_NAMESPACE", defaultNamespace)
	kubeClient := newKubernetesClient(t)
	remoteName := fmt.Sprintf("openbao-secret-sync-e2e-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		deleteKubernetesSecret(ctx, kubeClient, namespace, remoteName)
	})

	writeKubernetesDestination(t, baoClient, namespace)
	assertDestinationValid(t, baoClient)
	assertDestinationHealthy(t, baoClient)
	acknowledgeRestoreGuard(t, baoClient)
	write(t, baoClient, mountPath+"/metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"syncable": "true",
		},
	})
	writeSource(t, baoClient, "initial")

	plan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := plan.Data["action"]; got != "create" {
		t.Fatalf("plan action = %v, want create", got)
	}

	association := write(t, baoClient, mountPath+"/associations/app/db", associationRequest(remoteName))
	if ids := stringSlice(t, association.Data["sync_operation_ids"]); len(ids) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", ids)
	}
	drainQueue(t, baoClient, 1)
	assertKubernetesSecret(t, ctx, kubeClient, namespace, remoteName, "initial", "1")
	assertStatus(t, baoClient, "SYNCED")
	assertReconcilePlan(t, baoClient, "SYNCED")
	assertReconcileApply(t, baoClient, "SYNCED")

	noOpPlan := write(t, baoClient, mountPath+"/associations/app/db/plan", associationRequest(remoteName))
	if got := noOpPlan.Data["action"]; got != "noop" {
		t.Fatalf("noop plan action = %v, want noop", got)
	}

	writeSource(t, baoClient, "updated")
	drainQueue(t, baoClient, 1)
	assertKubernetesSecret(t, ctx, kubeClient, namespace, remoteName, "updated", "2")

	deleteSecret := deletePath(t, baoClient, mountPath+"/data/app/db")
	if ids := metadataOperationIDs(t, deleteSecret); len(ids) != 1 {
		t.Fatalf("delete sync_operation_ids = %v, want one operation", ids)
	}
	drainQueue(t, baoClient, 1)
	assertKubernetesSecretMissing(t, ctx, kubeClient, namespace, remoteName)
	assertStatus(t, baoClient, "REMOTE_MISSING")
}

func TestOpenBaoPluginReportsKubernetesPolicyFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baoClient := setupMountedOpenBao(t, ctx)
	kubeClient := newKubernetesClient(t)
	namespace := fmt.Sprintf("openbao-secret-sync-denied-%d", time.Now().UnixNano()%1_000_000)
	createNamespace(t, ctx, kubeClient, namespace)

	writeKubernetesDestination(t, baoClient, namespace)
	assertDestinationValid(t, baoClient)
	assertDestinationUnhealthy(t, baoClient, "authz")
	acknowledgeRestoreGuard(t, baoClient)
	write(t, baoClient, mountPath+"/metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"syncable": "true",
		},
	})
	writeSource(t, baoClient, "initial")

	remoteName := fmt.Sprintf("openbao-secret-sync-authz-%d", time.Now().UnixNano())
	association := write(t, baoClient, mountPath+"/associations/app/db", associationRequest(remoteName))
	if ids := stringSlice(t, association.Data["sync_operation_ids"]); len(ids) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", ids)
	}
	drainQueue(t, baoClient, 1)
	assertStatusDetails(t, baoClient, "DESTINATION_POLICY_ERROR", "authz")
}

func TestOpenBaoPluginReportsKubernetesOwnershipLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baoClient := setupMountedOpenBao(t, ctx)
	namespace := env("E2E_KIND_NAMESPACE", defaultNamespace)
	kubeClient := newKubernetesClient(t)
	remoteName := fmt.Sprintf("openbao-secret-sync-owner-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		deleteKubernetesSecret(ctx, kubeClient, namespace, remoteName)
	})

	writeKubernetesDestination(t, baoClient, namespace)
	acknowledgeRestoreGuard(t, baoClient)
	write(t, baoClient, mountPath+"/metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"syncable": "true",
		},
	})
	writeSource(t, baoClient, "initial")
	write(t, baoClient, mountPath+"/associations/app/db", associationRequest(remoteName))
	drainQueue(t, baoClient, 1)
	assertKubernetesSecret(t, ctx, kubeClient, namespace, remoteName, "initial", "1")

	secret := getKubernetesSecret(t, ctx, kubeClient, namespace, remoteName)
	secret.Labels["openbao.adfinis.com/managed"] = "false"
	updateKubernetesSecret(t, ctx, kubeClient, namespace, secret)

	writeSource(t, baoClient, "updated")
	drainQueue(t, baoClient, 1)
	assertStatusDetails(t, baoClient, "REMOTE_OWNERSHIP_LOST", "ownership")
	assertKubernetesSecretPayload(t, ctx, kubeClient, namespace, remoteName, "initial")
}

func TestOpenBaoPluginReportsKubernetesImmutableSecret(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baoClient := setupMountedOpenBao(t, ctx)
	namespace := env("E2E_KIND_NAMESPACE", defaultNamespace)
	kubeClient := newKubernetesClient(t)
	remoteName := fmt.Sprintf("openbao-secret-sync-immutable-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		deleteKubernetesSecret(ctx, kubeClient, namespace, remoteName)
	})

	writeKubernetesDestination(t, baoClient, namespace)
	acknowledgeRestoreGuard(t, baoClient)
	write(t, baoClient, mountPath+"/metadata/app/db", map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"syncable": "true",
		},
	})
	writeSource(t, baoClient, "initial")
	write(t, baoClient, mountPath+"/associations/app/db", associationRequest(remoteName))
	drainQueue(t, baoClient, 1)
	assertKubernetesSecret(t, ctx, kubeClient, namespace, remoteName, "initial", "1")

	secret := getKubernetesSecret(t, ctx, kubeClient, namespace, remoteName)
	immutable := true
	secret.Immutable = &immutable
	updateKubernetesSecret(t, ctx, kubeClient, namespace, secret)

	writeSource(t, baoClient, "updated")
	drainQueue(t, baoClient, 1)
	assertStatusDetails(t, baoClient, "VALIDATION_ERROR", "validation")
	assertKubernetesSecretPayload(t, ctx, kubeClient, namespace, remoteName, "initial")
}

func setupMountedOpenBao(t *testing.T, ctx context.Context) *api.Client {
	t.Helper()
	baoClient := newOpenBaoClient(t)
	waitForOpenBao(t, ctx, baoClient)
	registerPlugin(t, baoClient)
	mountPlugin(t, baoClient)
	return baoClient
}

func writeKubernetesDestination(t *testing.T, client *api.Client, namespace string) {
	t.Helper()
	write(t, client, mountPath+"/destinations/k8s/prod", map[string]interface{}{
		kubernetessecrets.ConfigKeyNamespace: namespace,
		kubernetessecrets.ConfigKeyAuthMode:  kubernetessecrets.AuthModeInCluster,
	})
}

func associationRequest(remoteName string) map[string]interface{} {
	return map[string]interface{}{
		"destination_type": "k8s",
		"destination_name": "prod",
		"resolved_name":    remoteName,
		"granularity":      "secret-path",
		"format":           "json",
		"delete_mode":      "delete",
	}
}

func writeSource(t *testing.T, client *api.Client, password string) {
	t.Helper()
	write(t, client, mountPath+"/data/app/db", map[string]interface{}{
		"data": map[string]interface{}{
			"password": password,
		},
	})
}

func newOpenBaoClient(t *testing.T) *api.Client {
	t.Helper()
	config := api.DefaultConfig()
	config.Address = env("E2E_KIND_OPENBAO_ADDR", "http://127.0.0.1:18202")
	client, err := api.NewClient(config)
	if err != nil {
		t.Fatalf("create OpenBao client: %v", err)
	}
	client.SetToken(rootToken)
	return client
}

func newKubernetesClient(t *testing.T) kubernetes.Interface {
	t.Helper()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig := os.Getenv("E2E_KIND_KUBECONFIG"); kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{
		CurrentContext: env("E2E_KIND_CONTEXT", defaultContext),
	}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		overrides,
	).ClientConfig()
	if err != nil {
		t.Fatalf("load Kubernetes config: %v", err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("create Kubernetes client: %v", err)
	}
	return client
}

func waitForOpenBao(t *testing.T, ctx context.Context, client *api.Client) {
	t.Helper()
	waitFor(t, ctx, func() error {
		health, err := client.Sys().Health()
		if err != nil {
			return err
		}
		if !health.Initialized || health.Sealed {
			return fmt.Errorf("openbao not ready: initialized=%v sealed=%v", health.Initialized, health.Sealed)
		}
		return nil
	})
}

func registerPlugin(t *testing.T, client *api.Client) {
	t.Helper()
	pluginPath := env("E2E_PLUGIN_PATH", "../../../bin/e2e/"+pluginName)
	absPluginPath, err := filepath.Abs(pluginPath)
	if err != nil {
		t.Fatalf("resolve plugin path: %v", err)
	}
	pluginBytes, err := os.ReadFile(absPluginPath)
	if err != nil {
		t.Fatalf("read plugin binary %s: %v", absPluginPath, err)
	}
	sum := sha256.Sum256(pluginBytes)
	if err := client.Sys().RegisterPlugin(&api.RegisterPluginInput{
		Name:    pluginName,
		Type:    api.PluginTypeSecrets,
		Command: pluginName,
		SHA256:  hex.EncodeToString(sum[:]),
		Version: pluginVersion,
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
}

func mountPlugin(t *testing.T, client *api.Client) {
	t.Helper()
	_ = client.Sys().Unmount(mountPath)
	if err := client.Sys().Mount(mountPath, &api.MountInput{
		Type:       "plugin",
		PluginName: pluginName,
		Config: api.MountConfigInput{
			PluginVersion: pluginVersion,
		},
	}); err != nil {
		t.Fatalf("mount plugin: %v", err)
	}
}

func acknowledgeRestoreGuard(t *testing.T, client *api.Client) {
	t.Helper()
	write(t, client, mountPath+"/config/restore-guard/acknowledge", map[string]interface{}{})
}

func assertDestinationValid(t *testing.T, client *api.Client) {
	t.Helper()
	secret := write(t, client, mountPath+"/destinations/k8s/prod/validate", map[string]interface{}{})
	if got := secret.Data["valid"]; got != true {
		t.Fatalf("destination valid = %v, want true", got)
	}
}

func assertDestinationHealthy(t *testing.T, client *api.Client) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/destinations/k8s/prod/health")
	if err != nil {
		t.Fatalf("read destination health: %v", err)
	}
	if secret == nil {
		t.Fatal("destination health response is nil")
	}
	if got := secret.Data["healthy"]; got != true {
		t.Fatalf("destination healthy = %v, want true", got)
	}
}

func assertDestinationUnhealthy(t *testing.T, client *api.Client, expectedErrorClass string) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/destinations/k8s/prod/health")
	if err != nil {
		t.Fatalf("read destination health: %v", err)
	}
	if secret == nil {
		t.Fatal("destination health response is nil")
	}
	if got := secret.Data["healthy"]; got != false {
		t.Fatalf("destination healthy = %v, want false", got)
	}
	if got := secret.Data["error_class"]; got != expectedErrorClass {
		t.Fatalf("destination error_class = %v, want %s", got, expectedErrorClass)
	}
}

func write(t *testing.T, client *api.Client, path string, data map[string]interface{}) *api.Secret {
	t.Helper()
	secret, err := client.Logical().Write(path, data)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if secret == nil {
		return &api.Secret{Data: map[string]interface{}{}}
	}
	return secret
}

func deletePath(t *testing.T, client *api.Client, path string) *api.Secret {
	t.Helper()
	secret, err := client.Logical().Delete(path)
	if err != nil {
		t.Fatalf("delete %s: %v", path, err)
	}
	if secret == nil {
		return &api.Secret{Data: map[string]interface{}{}}
	}
	return secret
}

func drainQueue(t *testing.T, client *api.Client, expectedProcessed int) {
	t.Helper()
	secret := write(t, client, mountPath+"/queue/drain", map[string]interface{}{
		"max_operations": expectedProcessed,
	})
	if got := intFromSecret(t, secret.Data["processed"]); got != expectedProcessed {
		t.Fatalf("queue drain processed = %d, want %d", got, expectedProcessed)
	}
}

func assertKubernetesSecret(
	t *testing.T,
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	name string,
	expectedPassword string,
	expectedSourceVersion string,
) {
	t.Helper()
	waitFor(t, ctx, func() error {
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		payload, ok := secret.Data[dataKeyPayload]
		if !ok {
			return errors.New("secret data.payload is missing")
		}
		var data map[string]string
		if err := json.Unmarshal(payload, &data); err != nil {
			return err
		}
		if data["password"] != expectedPassword {
			return fmt.Errorf("password = %q, want %q", data["password"], expectedPassword)
		}
		expectedSHA := "sha256:" + sha256Hex(payload)
		expectedAnnotations := map[string]string{
			"openbao.adfinis.com/source-path":    "app/db",
			"openbao.adfinis.com/source-version": expectedSourceVersion,
			"openbao.adfinis.com/object-id":      "secret-path",
			"openbao.adfinis.com/payload-sha256": expectedSHA,
			"openbao.adfinis.com/format":         "json",
		}
		if got := secret.Labels["openbao.adfinis.com/managed"]; got != "true" {
			return fmt.Errorf("managed label = %q, want true", got)
		}
		for key, expected := range expectedAnnotations {
			if got := secret.Annotations[key]; got != expected {
				return fmt.Errorf("annotation %s = %q, want %q", key, got, expected)
			}
		}
		if got := secret.Annotations["openbao.adfinis.com/association-id"]; got == "" {
			return errors.New("association-id annotation is missing")
		}
		return nil
	})
}

func assertKubernetesSecretPayload(
	t *testing.T,
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	name string,
	expectedPassword string,
) {
	t.Helper()
	waitFor(t, ctx, func() error {
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		payload, ok := secret.Data[dataKeyPayload]
		if !ok {
			return errors.New("secret data.payload is missing")
		}
		var data map[string]string
		if err := json.Unmarshal(payload, &data); err != nil {
			return err
		}
		if data["password"] != expectedPassword {
			return fmt.Errorf("password = %q, want %q", data["password"], expectedPassword)
		}
		return nil
	})
}

func assertKubernetesSecretMissing(
	t *testing.T,
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	name string,
) {
	t.Helper()
	waitFor(t, ctx, func() error {
		_, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.New("secret still exists")
	})
}

func getKubernetesSecret(
	t *testing.T,
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	name string,
) *corev1.Secret {
	t.Helper()
	secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Kubernetes secret %s/%s: %v", namespace, name, err)
	}
	return secret
}

func updateKubernetesSecret(
	t *testing.T,
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	secret *corev1.Secret,
) {
	t.Helper()
	if _, err := client.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update Kubernetes secret %s/%s: %v", namespace, secret.Name, err)
	}
}

func createNamespace(
	t *testing.T,
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
) {
	t.Helper()
	_, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", namespace, err)
	}
	t.Cleanup(func() {
		_ = client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	})
}

func assertStatus(t *testing.T, client *api.Client, expectedState string) {
	t.Helper()
	secret := readStatus(t, client)
	if got := secret.Data["state"]; got != expectedState {
		t.Fatalf("status state = %v, want %s", got, expectedState)
	}
}

func assertStatusDetails(t *testing.T, client *api.Client, expectedState string, expectedErrorClass string) {
	t.Helper()
	secret := readStatus(t, client)
	if got := secret.Data["state"]; got != expectedState {
		t.Fatalf("status state = %v, want %s", got, expectedState)
	}
	if got := secret.Data["last_error_class"]; got != expectedErrorClass {
		t.Fatalf("status last_error_class = %v, want %s", got, expectedErrorClass)
	}
}

func readStatus(t *testing.T, client *api.Client) *api.Secret {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/status/app/db")
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if secret == nil {
		t.Fatal("status response is nil")
	}
	return secret
}

func assertReconcilePlan(t *testing.T, client *api.Client, expectedState string) {
	t.Helper()
	secret, err := client.Logical().Read(mountPath + "/reconcile/app/db/plan")
	if err != nil {
		t.Fatalf("read reconcile plan: %v", err)
	}
	assertReconcileState(t, secret, expectedState)
}

func assertReconcileApply(t *testing.T, client *api.Client, expectedState string) {
	t.Helper()
	secret := write(t, client, mountPath+"/reconcile/app/db", map[string]interface{}{})
	assertReconcileState(t, secret, expectedState)
}

func assertReconcileState(t *testing.T, secret *api.Secret, expectedState string) {
	t.Helper()
	if secret == nil {
		t.Fatal("reconcile response is nil")
	}
	if got := secret.Data["state"]; got != expectedState {
		t.Fatalf("reconcile state = %v, want %s", got, expectedState)
	}
}

func deleteKubernetesSecret(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	name string,
) {
	_ = client.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func metadataOperationIDs(t *testing.T, secret *api.Secret) []string {
	t.Helper()
	metadata, ok := secret.Data["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata = %T, want map", secret.Data["metadata"])
	}
	return stringSlice(t, metadata["sync_operation_ids"])
}

func stringSlice(t *testing.T, raw interface{}) []string {
	t.Helper()
	values, ok := raw.([]interface{})
	if ok {
		result := make([]string, 0, len(values))
		for _, value := range values {
			result = append(result, fmt.Sprint(value))
		}
		return result
	}
	strings, ok := raw.([]string)
	if !ok {
		t.Fatalf("value = %T, want string slice", raw)
	}
	return strings
}

func intFromSecret(t *testing.T, raw interface{}) int {
	t.Helper()
	switch value := raw.(type) {
	case int:
		return value
	case json.Number:
		result, err := value.Int64()
		if err != nil {
			t.Fatalf("parse json number: %v", err)
		}
		return int(result)
	case float64:
		return int(value)
	default:
		t.Fatalf("value = %T, want number", raw)
		return 0
	}
}

func waitFor(t *testing.T, ctx context.Context, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled while waiting: %v", ctx.Err())
		case <-time.After(testPollInterval):
		}
	}
	t.Fatalf("condition not met after %s: %v", testTimeout, lastErr)
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func env(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
