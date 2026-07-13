package kubernetessecrets

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	payloadpkg "github.com/adfinis/openbao-plugin-secrets-sync/internal/payload"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/providertest"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testDestinationName = "cluster"
	testNamespace       = "apps"
	testResolvedName    = "app-db"
	testAssociationID   = "assoc-1"
	testSourcePath      = "app/db"
	testObjectID        = "secret-path"
	testPayloadSHAOld   = "sha256:6a81041dee1ed86a0d590b7d8c555c789cd4de82fbfa5b4e6881f4ebba1b6f41"
	testPayloadSHANew   = "sha256:4cc28eb0fcebad7dbacc0970586ee420fd24ef182cf76c955e833f3da4a5ad3d"
	testPayloadNew      = `{"password":"new"}`
	testPluginInstance  = "inst-test"
	testRestoreEpoch    = "epoch-test"
)

var testCACertPEM = mustTestCACertPEM()

func mustTestCACertPEM() string {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		panic(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "kubernetes.default.svc"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})))
}

var secretsResource = schema.GroupResource{Resource: "secrets"}

func TestProviderConformance(t *testing.T) {
	client := fake.NewSimpleClientset()
	providertest.Run(t, providertest.Harness{
		Provider:         Provider{client: client},
		ValidDestination: defaultDestinationConfig(),
		RequiredCapabilities: providertest.CapabilityExpectations{
			ValueReadback:       true,
			MetadataReadback:    true,
			SecretPath:          true,
			DataMap:             true,
			UpdateIfOwned:       true,
			DeleteIfOwned:       true,
			PayloadHashMetadata: true,
			MinPayloadBytes:     secretMaxBytes,
		},
		ValidationError: &providertest.ValidationErrorCase{
			Destination: providers.DestinationConfig{Name: testDestinationName},
			ErrorClass:  providers.ErrorClassValidation,
		},
		HealthCase: &providertest.HealthCase{
			Destination: defaultDestinationConfig(),
			Healthy:     true,
		},
		Lifecycle: &providertest.LifecycleCase{
			Name: "secret-path",
			CreatePlan: providertest.PlanCase{
				Name:    "create",
				Request: defaultPlanRequest(testPayloadSHAOld, 1),
				Action:  providers.PlanActionCreate,
			},
			Create: providertest.UpsertCase{
				Request: defaultUpsertRequest(testPayloadSHAOld, []byte(`{"password":"old"}`), 1),
			},
			StateAfterCreate: providertest.ReadStateCase{
				Request:        defaultReadStateRequest(testPayloadSHAOld, 1),
				Exists:         true,
				OwnershipKnown: true,
				Owned:          true,
				PayloadSHA256:  testPayloadSHAOld,
				SourceVersion:  1,
			},
			NoopPlan: providertest.PlanCase{
				Name:    "noop",
				Request: defaultPlanRequest(testPayloadSHAOld, 1),
				Action:  providers.PlanActionNoop,
			},
			UpdatePlan: providertest.PlanCase{
				Name:    "update",
				Request: defaultPlanRequest(testPayloadSHANew, 2),
				Action:  providers.PlanActionUpdate,
			},
			Update: providertest.UpsertCase{
				Request: defaultUpsertRequest(testPayloadSHANew, []byte(testPayloadNew), 2),
			},
			StateAfterUpdate: providertest.ReadStateCase{
				Request:        defaultReadStateRequest(testPayloadSHANew, 2),
				Exists:         true,
				OwnershipKnown: true,
				Owned:          true,
				PayloadSHA256:  testPayloadSHANew,
				SourceVersion:  2,
			},
			Delete: providertest.DeleteCase{
				Request: defaultDeleteRequest(2),
			},
			StateAfterDelete: providertest.ReadStateCase{
				Request: defaultReadStateRequest(testPayloadSHANew, 2),
			},
		},
		Maturity: kubernetesMaturityMatrix(),
		Idempotency: &providertest.IdempotencyCase{
			Name:          "same-request",
			UpsertRequest: defaultUpsertRequest(testPayloadSHAOld, []byte(`{"password":"old"}`), 1),
			StateAfterUpsert: &providertest.ReadStateCase{
				Request:        defaultReadStateRequest(testPayloadSHAOld, 1),
				Exists:         true,
				OwnershipKnown: true,
				Owned:          true,
				PayloadSHA256:  testPayloadSHAOld,
				SourceVersion:  1,
			},
			DeleteRequest: defaultDeleteRequest(1),
			StateAfterDelete: &providertest.ReadStateCase{
				Request: defaultReadStateRequest(testPayloadSHAOld, 1),
			},
			ExpectMutationResult: true,
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
			name: "in cluster",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
			},
		},
		{
			name: "kubeconfig inferred from path",
			config: map[string]string{
				ConfigKeyNamespace:      testNamespace,
				ConfigKeyKubeconfigPath: "/tmp/kubeconfig",
				ConfigKeyKubeContext:    "dev",
			},
		},
		{
			name: "kubeconfig explicit",
			config: map[string]string{
				ConfigKeyNamespace:      testNamespace,
				ConfigKeyAuthMode:       AuthModeKubeconfig,
				ConfigKeyKubeconfigPath: "/tmp/kubeconfig",
			},
		},
		{
			name: "token explicit",
			config: map[string]string{
				ConfigKeyNamespace:     testNamespace,
				ConfigKeyAuthMode:      AuthModeToken,
				ConfigKeyAPIServer:     "https://kubernetes.example.com",
				ConfigKeyToken:         "bearer-token",
				ConfigKeyCACertPEM:     testCACertPEM,
				ConfigKeyTLSServerName: "kubernetes.default.svc",
			},
		},
		{
			name: "token inferred from api server",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAPIServer: "https://kubernetes.example.com",
				ConfigKeyToken:     "bearer-token",
			},
		},
		{
			name: "token private api server rejected without opt in",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  AuthModeToken,
				ConfigKeyAPIServer: "https://10.0.0.10",
				ConfigKeyToken:     "bearer-token",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "token private api server with explicit opt in",
			config: map[string]string{
				ConfigKeyNamespace:             testNamespace,
				ConfigKeyAuthMode:              AuthModeToken,
				ConfigKeyAPIServer:             "https://10.0.0.10",
				ConfigKeyAllowPrivateAPIServer: "true",
				ConfigKeyToken:                 "bearer-token",
			},
		},
		{
			name: "in cluster rejects private api server opt in",
			config: map[string]string{
				ConfigKeyNamespace:             testNamespace,
				ConfigKeyAuthMode:              AuthModeInCluster,
				ConfigKeyAllowPrivateAPIServer: "true",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "invalid private api server opt in",
			config: map[string]string{
				ConfigKeyNamespace:             testNamespace,
				ConfigKeyAuthMode:              AuthModeToken,
				ConfigKeyAPIServer:             "https://kubernetes.example.com",
				ConfigKeyToken:                 "bearer-token",
				ConfigKeyAllowPrivateAPIServer: "sometimes",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name:       "missing namespace",
			config:     map[string]string{},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "invalid namespace",
			config: map[string]string{
				ConfigKeyNamespace: "Invalid",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "in cluster rejects kubeconfig",
			config: map[string]string{
				ConfigKeyNamespace:      testNamespace,
				ConfigKeyAuthMode:       AuthModeInCluster,
				ConfigKeyKubeconfigPath: "/tmp/kubeconfig",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "kubeconfig requires path",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  AuthModeKubeconfig,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "unknown auth mode",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  "static-token",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "token requires api server",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  AuthModeToken,
				ConfigKeyToken:     "bearer-token",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "token requires https api server",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  AuthModeToken,
				ConfigKeyAPIServer: "http://kubernetes.example.com",
				ConfigKeyToken:     "bearer-token",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "token requires token",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  AuthModeToken,
				ConfigKeyAPIServer: "https://kubernetes.example.com",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "token rejects kubeconfig",
			config: map[string]string{
				ConfigKeyNamespace:      testNamespace,
				ConfigKeyAuthMode:       AuthModeToken,
				ConfigKeyAPIServer:      "https://kubernetes.example.com",
				ConfigKeyToken:          "bearer-token",
				ConfigKeyKubeconfigPath: "/tmp/kubeconfig",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "kubeconfig rejects token fields",
			config: map[string]string{
				ConfigKeyNamespace:      testNamespace,
				ConfigKeyAuthMode:       AuthModeKubeconfig,
				ConfigKeyKubeconfigPath: "/tmp/kubeconfig",
				ConfigKeyToken:          "bearer-token",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "token rejects invalid ca cert",
			config: map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  AuthModeToken,
				ConfigKeyAPIServer: "https://kubernetes.example.com",
				ConfigKeyToken:     "bearer-token",
				ConfigKeyCACertPEM: "not a certificate",
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

func TestValidateAPIServerResolution(t *testing.T) {
	tests := []struct {
		name       string
		overrides  map[string]string
		resolve    func(context.Context, string, string) ([]netip.Addr, error)
		errorClass providers.ErrorClass
	}{
		{
			name: "public DNS allowed",
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
			},
		},
		{
			name: "private DNS rejected",
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.0.0.10")}, nil
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "link local DNS rejected",
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "DNS error unavailable",
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return nil, errors.New("lookup failed")
			},
			errorClass: providers.ErrorClassUnavailable,
		},
		{
			name: "private opt in skips DNS guard",
			overrides: map[string]string{
				ConfigKeyAllowPrivateAPIServer: "true",
			},
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]string{
				ConfigKeyNamespace: testNamespace,
				ConfigKeyAuthMode:  AuthModeToken,
				ConfigKeyAPIServer: "https://kubernetes.example.com",
				ConfigKeyToken:     "bearer-token",
			}
			for key, value := range tt.overrides {
				config[key] = value
			}
			options, err := kubernetesDestinationOptionsFromConfig(providers.DestinationConfig{
				Name:   testDestinationName,
				Config: config,
			})
			if err != nil {
				t.Fatalf("options from config: %v", err)
			}
			err = validateAPIServerResolution(context.Background(), options, tt.resolve)
			if tt.errorClass == "" {
				if err != nil {
					t.Fatalf("validate api server resolution: %v", err)
				}
				return
			}
			assertProviderErrorClass(t, err, tt.errorClass)
		})
	}
}

func TestTokenRESTConfig(t *testing.T) {
	options, err := kubernetesDestinationOptionsFromConfig(providers.DestinationConfig{
		Name: testDestinationName,
		Config: map[string]string{
			ConfigKeyNamespace:     testNamespace,
			ConfigKeyAuthMode:      AuthModeToken,
			ConfigKeyAPIServer:     "https://kubernetes.example.com",
			ConfigKeyToken:         "bearer-token",
			ConfigKeyCACertPEM:     testCACertPEM,
			ConfigKeyTLSServerName: "kubernetes.default.svc",
		},
	})
	if err != nil {
		t.Fatalf("options from config: %v", err)
	}

	restConfig := restConfigForToken(options)
	if restConfig.Host != "https://kubernetes.example.com" {
		t.Fatalf("host = %s, want https://kubernetes.example.com", restConfig.Host)
	}
	if restConfig.BearerToken != "bearer-token" {
		t.Fatalf("bearer token = %q, want configured token", restConfig.BearerToken)
	}
	if string(restConfig.CAData) != testCACertPEM {
		t.Fatal("CAData does not match configured ca_cert_pem")
	}
	if restConfig.ServerName != "kubernetes.default.svc" {
		t.Fatalf("server name = %s, want kubernetes.default.svc", restConfig.ServerName)
	}
	hardenRESTConfig(restConfig, options, nil)
	if restConfig.Timeout != defaultRequestTimeout {
		t.Fatalf("timeout = %s, want %s", restConfig.Timeout, defaultRequestTimeout)
	}
	if restConfig.Dial == nil {
		t.Fatal("public token api server must revalidate DNS when dialing")
	}
	if restConfig.Proxy == nil {
		t.Fatal("public token api server must disable ambient proxy resolution")
	}
	proxyURL, err := restConfig.Proxy(nil)
	if err != nil || proxyURL != nil {
		t.Fatalf("proxy = %v, %v, want no proxy", proxyURL, err)
	}
}

func TestTokenRESTConfigRejectsDNSRebindingAtDial(t *testing.T) {
	resolverCalls := 0
	resolver := func(context.Context, string, string) ([]netip.Addr, error) {
		resolverCalls++
		if resolverCalls == 1 {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	options := kubernetesDestinationOptions{
		authMode:  AuthModeToken,
		apiServer: "https://kubernetes.example.com",
	}
	if err := validateAPIServerResolution(context.Background(), options, resolver); err != nil {
		t.Fatalf("validate api server resolution: %v", err)
	}
	restConfig := restConfigForToken(options)
	hardenRESTConfig(restConfig, options, resolver)
	_, err := restConfig.Dial(context.Background(), "tcp", "kubernetes.example.com:443")
	if err == nil || !strings.Contains(err.Error(), "endpoint address is not allowed") {
		t.Fatalf("dial error = %v, want disallowed rebound address", err)
	}
	if resolverCalls != 2 {
		t.Fatalf("resolver calls = %d, want validation and dial lookups", resolverCalls)
	}
}

func TestRESTConfigHardeningPreservesExplicitTimeoutAndPrivateOptIn(t *testing.T) {
	explicitTimeout := 5 * time.Second
	restConfig := &rest.Config{Timeout: explicitTimeout}
	options := kubernetesDestinationOptions{authMode: AuthModeToken, allowPrivateAPI: true}
	hardenRESTConfig(restConfig, options, nil)
	if restConfig.Timeout != explicitTimeout {
		t.Fatalf("timeout = %s, want configured %s", restConfig.Timeout, explicitTimeout)
	}
	if restConfig.Dial != nil {
		t.Fatal("private api server opt in must allow the standard dialer")
	}
	if restConfig.Proxy != nil {
		t.Fatal("private api server opt in must preserve proxy configuration")
	}
}

func TestPlanActions(t *testing.T) {
	tests := []struct {
		name       string
		secret     *corev1.Secret
		request    providers.PlanRequest
		action     string
		errorClass providers.ErrorClass
	}{
		{
			name:    "create missing secret",
			request: defaultPlanRequest(testPayloadSHANew, 1),
			action:  providers.PlanActionCreate,
		},
		{
			name:    "noop owned matching hash",
			secret:  ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew)),
			request: defaultPlanRequest(testPayloadSHANew, 1),
			action:  providers.PlanActionNoop,
		},
		{
			name:    "update owned different hash",
			secret:  ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`)),
			request: defaultPlanRequest(testPayloadSHANew, 1),
			action:  providers.PlanActionUpdate,
		},
		{
			name:    "update matching payload with stale source metadata",
			secret:  ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew)),
			request: defaultPlanRequest(testPayloadSHANew, 2),
			action:  providers.PlanActionUpdate,
		},
		{
			name: "update matching payload with unexpected data key",
			secret: func() *corev1.Secret {
				secret := ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew))
				secret.Data["foreign"] = []byte("remove")
				return secret
			}(),
			request: defaultPlanRequest(testPayloadSHANew, 1),
			action:  providers.PlanActionUpdate,
		},
		{
			name:       "blocked newer remote source version",
			secret:     ownedSecret(testPayloadSHAOld, 2, []byte(`{"password":"old"}`)),
			request:    defaultPlanRequest(testPayloadSHANew, 1),
			action:     providers.PlanActionBlocked,
			errorClass: providers.ErrorClassDrift,
		},
		{
			name:       "blocked immutable secret",
			secret:     immutableSecret(ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`))),
			request:    defaultPlanRequest(testPayloadSHANew, 1),
			action:     providers.PlanActionBlocked,
			errorClass: providers.ErrorClassValidation,
		},
		{
			name:       "conflict unowned secret",
			secret:     unownedSecret(),
			request:    defaultPlanRequest(testPayloadSHANew, 1),
			action:     providers.PlanActionConflict,
			errorClass: providers.ErrorClassCollision,
		},
		{
			name:       "blocked invalid resolved name",
			request:    planRequest("app/db", testPayloadSHANew, 1),
			action:     providers.PlanActionBlocked,
			errorClass: providers.ErrorClassValidation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			if tt.secret != nil {
				client = fake.NewSimpleClientset(tt.secret)
			}
			result, err := runtimeWithClient(t, client).Plan(context.Background(), tt.request)
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

func TestPlanUpdatesWhenSecretDataDriftsWithMatchingMetadata(t *testing.T) {
	secret := ownedSecret(testPayloadSHANew, 1, []byte(`{"password":"old"}`))
	client := fake.NewSimpleClientset(secret)

	result, err := runtimeWithClient(t, client).Plan(context.Background(), defaultPlanRequest(testPayloadSHANew, 1))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Action != providers.PlanActionUpdate {
		t.Fatalf("action = %s, want %s", result.Action, providers.PlanActionUpdate)
	}
}

func TestPlanUpdatesDataMapWithMatchingPayloadAndStaleMetadata(t *testing.T) {
	payload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
	})
	secret := ownedDataMapSecret(payload.SHA256, payload.Data, dataMapKeys(payload.Data))
	client := fake.NewSimpleClientset(secret)
	request := defaultPlanRequest(payload.SHA256, 2)
	request.Format = payload.Format
	request.DataMap = true
	request.DataMapKeys = dataMapKeys(payload.Data)

	result, err := runtimeWithClient(t, client).Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("plan data map: %v", err)
	}
	if result.Action != providers.PlanActionUpdate {
		t.Fatalf("action = %s, want %s", result.Action, providers.PlanActionUpdate)
	}
}

func TestUpsertCreatesSecretWithOwnershipMetadata(t *testing.T) {
	client := fake.NewSimpleClientset()
	result, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest(
		testPayloadSHANew,
		[]byte(`{"password":"secret"}`),
		1,
	))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result == nil {
		t.Fatal("sync result must not be nil")
	}
	secret, err := client.CoreV1().Secrets(testNamespace).Get(context.Background(), testResolvedName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got := string(secret.Data[dataKeyPayload]); got != `{"password":"secret"}` {
		t.Fatalf("secret payload = %s, want canonical payload", got)
	}
	if got := secret.Type; got != corev1.SecretTypeOpaque {
		t.Fatalf("secret type = %s, want Opaque", got)
	}
	assertLabel(t, secret, labelManaged, "true")
	assertAnnotation(t, secret, annotationAssociationID, testAssociationID)
	assertAnnotation(t, secret, annotationSourcePath, testSourcePath)
	assertAnnotation(t, secret, annotationSourceVersion, "1")
	assertAnnotation(t, secret, annotationObjectID, testObjectID)
	assertAnnotation(t, secret, annotationPayloadSHA256, testPayloadSHANew)
	assertAnnotation(t, secret, annotationPluginInstance, testPluginInstance)
	assertAnnotation(t, secret, annotationRestoreEpoch, testRestoreEpoch)
}

func TestUpsertUpdatesOwnedSecretAndPreservesForeignMetadata(t *testing.T) {
	secret := ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`))
	secret.Labels["app"] = "demo"
	secret.Annotations["example.com/owner"] = "team-a"
	client := fake.NewSimpleClientset(secret)

	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest(
		testPayloadSHANew,
		[]byte(testPayloadNew),
		1,
	))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	updated, err := client.CoreV1().Secrets(testNamespace).Get(
		context.Background(),
		testResolvedName,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got := string(updated.Data[dataKeyPayload]); got != testPayloadNew {
		t.Fatalf("secret payload = %s, want updated payload", got)
	}
	assertLabel(t, updated, "app", "demo")
	assertAnnotation(t, updated, "example.com/owner", "team-a")
	assertAnnotation(t, updated, annotationPayloadSHA256, testPayloadSHANew)
}

func TestUpsertRepairsSecretDataDriftWithMatchingMetadata(t *testing.T) {
	secret := ownedSecret(testPayloadSHANew, 1, []byte(`{"password":"old"}`))
	client := fake.NewSimpleClientset(secret)

	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest(
		testPayloadSHANew,
		[]byte(testPayloadNew),
		1,
	))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	updated, err := client.CoreV1().Secrets(testNamespace).Get(
		context.Background(),
		testResolvedName,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got := string(updated.Data[dataKeyPayload]); got != testPayloadNew {
		t.Fatalf("secret payload = %s, want updated payload", got)
	}
}

func TestUpsertRefreshesMetadataForMatchingPayload(t *testing.T) {
	secret := ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew))
	secret.Annotations[annotationPayloadSHA256] = testPayloadSHAOld
	client := fake.NewSimpleClientset(secret)

	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest(
		testPayloadSHANew,
		[]byte(testPayloadNew),
		2,
	))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	updated, err := client.CoreV1().Secrets(testNamespace).Get(
		context.Background(),
		testResolvedName,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	assertAnnotation(t, updated, annotationSourceVersion, "2")
	assertAnnotation(t, updated, annotationPayloadSHA256, testPayloadSHANew)
	if got := string(updated.Data[dataKeyPayload]); got != testPayloadNew {
		t.Fatalf("secret payload = %s, want unchanged payload", got)
	}
}

func TestUpsertRefreshesImmutableSecretMetadataForMatchingPayload(t *testing.T) {
	secret := immutableSecret(ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew)))
	client := fake.NewSimpleClientset(secret)

	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest(
		testPayloadSHANew,
		[]byte(testPayloadNew),
		2,
	))
	if err != nil {
		t.Fatalf("upsert immutable metadata: %v", err)
	}
	updated, err := client.CoreV1().Secrets(testNamespace).Get(
		context.Background(),
		testResolvedName,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	assertAnnotation(t, updated, annotationSourceVersion, "2")
}

func TestUpsertMatchingPayloadRemovesUnexpectedDataKeys(t *testing.T) {
	secret := ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew))
	secret.Data["foreign"] = []byte("remove")
	client := fake.NewSimpleClientset(secret)

	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest(
		testPayloadSHANew,
		[]byte(testPayloadNew),
		1,
	))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	updated, err := client.CoreV1().Secrets(testNamespace).Get(
		context.Background(),
		testResolvedName,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if len(updated.Data) != 1 || string(updated.Data[dataKeyPayload]) != testPayloadNew {
		t.Fatalf("secret data = %#v, want only managed payload", updated.Data)
	}
}

func TestUpsertCreatesSecretDataMapWithOwnershipMetadata(t *testing.T) {
	client := fake.NewSimpleClientset()
	payload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
	})
	request := defaultDataMapUpsertRequest(payload, 1)

	_, err := runtimeWithClient(t, client).Upsert(context.Background(), request)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	secret, err := client.CoreV1().Secrets(testNamespace).Get(context.Background(), testResolvedName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got := string(secret.Data["username"]); got != "app" {
		t.Fatalf("username = %q, want app", got)
	}
	if got := string(secret.Data["password"]); got != "secret" {
		t.Fatalf("password = %q, want secret", got)
	}
	if _, ok := secret.Data[dataKeyPayload]; ok {
		t.Fatal("data-map secret must not write legacy payload key")
	}
	assertAnnotation(t, secret, annotationFormat, payloadpkg.FormatDataMap)
	assertAnnotation(t, secret, annotationPayloadSHA256, payload.SHA256)
	assertDataKeysAnnotation(t, secret, []string{"password", "username"})
}

func TestUpsertDataMapRemovesStaleManagedKeysAndPreservesForeignKeys(t *testing.T) {
	initialPayload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("old"),
		"password": []byte("old-secret"),
	})
	secret := ownedDataMapSecret(initialPayload.SHA256, map[string][]byte{
		"username": []byte("old"),
		"password": []byte("old-secret"),
		"tls.crt":  []byte("foreign"),
	}, []string{"password", "username"})
	client := fake.NewSimpleClientset(secret)

	nextPayload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("new"),
		"token":    []byte("rotated"),
	})
	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultDataMapUpsertRequest(nextPayload, 2))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := client.CoreV1().Secrets(testNamespace).Get(context.Background(), testResolvedName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got := string(updated.Data["username"]); got != "new" {
		t.Fatalf("username = %q, want new", got)
	}
	if got := string(updated.Data["token"]); got != "rotated" {
		t.Fatalf("token = %q, want rotated", got)
	}
	if _, ok := updated.Data["password"]; ok {
		t.Fatal("stale managed password key must be removed")
	}
	if got := string(updated.Data["tls.crt"]); got != "foreign" {
		t.Fatalf("foreign key = %q, want foreign", got)
	}
	assertDataKeysAnnotation(t, updated, []string{"token", "username"})
}

func TestUpsertDataMapRefreshesMetadataForMatchingPayload(t *testing.T) {
	payload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
	})
	secret := ownedDataMapSecret(payload.SHA256, payload.Data, dataMapKeys(payload.Data))
	client := fake.NewSimpleClientset(secret)

	_, err := runtimeWithClient(t, client).Upsert(
		context.Background(),
		defaultDataMapUpsertRequest(payload, 2),
	)
	if err != nil {
		t.Fatalf("upsert data map: %v", err)
	}
	updated, err := client.CoreV1().Secrets(testNamespace).Get(
		context.Background(),
		testResolvedName,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	assertAnnotation(t, updated, annotationSourceVersion, "2")
	assertDataKeysAnnotation(t, updated, []string{"password", "username"})
}

func TestUpsertDataMapRejectsUnmanagedDataKeyConflict(t *testing.T) {
	initialPayload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("old"),
	})
	secret := ownedDataMapSecret(initialPayload.SHA256, map[string][]byte{
		"username": []byte("old"),
		"foreign":  []byte("keep"),
	}, []string{"username"})
	client := fake.NewSimpleClientset(secret)
	nextPayload := mustDataMapPayload(t, map[string][]byte{
		"foreign": []byte("overwrite"),
	})

	_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultDataMapUpsertRequest(nextPayload, 2))
	assertProviderErrorClass(t, err, providers.ErrorClassOwnership)

	unchanged, getErr := client.CoreV1().Secrets(testNamespace).Get(
		context.Background(),
		testResolvedName,
		metav1.GetOptions{},
	)
	if getErr != nil {
		t.Fatalf("get secret: %v", getErr)
	}
	if got := string(unchanged.Data["foreign"]); got != "keep" {
		t.Fatalf("foreign key = %q, want keep", got)
	}
}

func TestDeleteDataMapRemovesManagedKeysAndPreservesForeignKeys(t *testing.T) {
	payload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
	})
	secret := ownedDataMapSecret(payload.SHA256, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
		"tls.crt":  []byte("foreign"),
	}, []string{"password", "username"})
	client := fake.NewSimpleClientset(secret)
	request := defaultDeleteRequest(1)
	request.DataMap = true

	_, err := runtimeWithClient(t, client).Delete(context.Background(), request)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	updated, err := client.CoreV1().Secrets(testNamespace).Get(context.Background(), testResolvedName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if len(updated.Data) != 1 || string(updated.Data["tls.crt"]) != "foreign" {
		t.Fatalf("data after delete = %#v, want only foreign key", updated.Data)
	}
	if updated.Labels[labelManaged] != "" {
		t.Fatal("managed label must be removed when preserving foreign keys")
	}
	if got := updated.Annotations[annotationAssociationID]; got != "" {
		t.Fatalf("association annotation = %q, want removed", got)
	}
}

func TestDeleteDataMapUsesPreconditionsWhenDeletingSecret(t *testing.T) {
	payload := mustDataMapPayload(t, map[string][]byte{"password": []byte("secret")})
	secret := ownedDataMapSecret(payload.SHA256, payload.Data, dataMapKeys(payload.Data))
	secret.UID = "uid-data-map"
	client := fake.NewSimpleClientset(secret)
	client.PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		deleteAction := action.(k8stesting.DeleteAction)
		preconditions := deleteAction.GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || string(*preconditions.UID) != "uid-data-map" {
			t.Fatalf("delete preconditions = %#v, want data-map UID", preconditions)
		}
		if preconditions.ResourceVersion == nil || *preconditions.ResourceVersion != "rv-1" {
			t.Fatalf("delete preconditions = %#v, want data-map resource version", preconditions)
		}
		return false, nil, nil
	})
	request := defaultDeleteRequest(1)
	request.DataMap = true

	if _, err := runtimeWithClient(t, client).Delete(context.Background(), request); err != nil {
		t.Fatalf("delete data map: %v", err)
	}
}

func TestUpsertRejectsUnsafeRemoteState(t *testing.T) {
	tests := []struct {
		name       string
		secret     *corev1.Secret
		errorClass providers.ErrorClass
	}{
		{
			name:       "unowned",
			secret:     unownedSecret(),
			errorClass: providers.ErrorClassOwnership,
		},
		{
			name:       "newer remote source version",
			secret:     ownedSecret(testPayloadSHAOld, 2, []byte(`{"password":"old"}`)),
			errorClass: providers.ErrorClassDrift,
		},
		{
			name:       "immutable",
			secret:     immutableSecret(ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`))),
			errorClass: providers.ErrorClassValidation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.secret)
			_, err := runtimeWithClient(t, client).Upsert(context.Background(), defaultUpsertRequest(
				testPayloadSHANew,
				[]byte(testPayloadNew),
				1,
			))
			assertProviderErrorClass(t, err, tt.errorClass)
		})
	}
}

func TestOwnedByRequestRejectsRuntimeIdentityMismatch(t *testing.T) {
	request := defaultUpsertRequest(testPayloadSHANew, []byte(testPayloadNew), 1)
	secret := ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew))
	secret.Annotations[annotationPluginInstance] = testPluginInstance
	secret.Annotations[annotationRestoreEpoch] = testRestoreEpoch
	if !ownedByRequest(secret, request.OwnershipIdentity()) {
		t.Fatal("ownedByRequest returned false for matching runtime identity")
	}
	secret.Annotations[annotationPluginInstance] = "inst-other"
	if ownedByRequest(secret, request.OwnershipIdentity()) {
		t.Fatal("ownedByRequest returned true for mismatched plugin instance")
	}
}

func TestDeleteUsesOwnershipMetadata(t *testing.T) {
	secret := ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`))
	secret.UID = "uid-1"
	client := fake.NewSimpleClientset(secret)
	client.PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		deleteAction, ok := action.(k8stesting.DeleteAction)
		if !ok {
			t.Fatalf("delete action = %T, want DeleteAction", action)
		}
		preconditions := deleteAction.GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || string(*preconditions.UID) != "uid-1" {
			t.Fatalf("delete UID precondition = %#v, want uid-1", preconditions)
		}
		if preconditions.ResourceVersion == nil || *preconditions.ResourceVersion != "rv-1" {
			t.Fatalf("delete resource version precondition = %#v, want rv-1", preconditions)
		}
		return false, nil, nil
	})
	_, err := runtimeWithClient(t, client).Delete(context.Background(), defaultDeleteRequest(1))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = client.CoreV1().Secrets(testNamespace).Get(context.Background(), testResolvedName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("secret exists after delete, err=%v", err)
	}
}

func TestDeleteRejectsUnsafeRemoteState(t *testing.T) {
	tests := []struct {
		name       string
		secret     *corev1.Secret
		errorClass providers.ErrorClass
	}{
		{
			name:       "unowned",
			secret:     unownedSecret(),
			errorClass: providers.ErrorClassOwnership,
		},
		{
			name:       "newer remote source version",
			secret:     ownedSecret(testPayloadSHAOld, 2, []byte(`{"password":"old"}`)),
			errorClass: providers.ErrorClassDrift,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.secret)
			_, err := runtimeWithClient(t, client).Delete(context.Background(), defaultDeleteRequest(1))
			assertProviderErrorClass(t, err, tt.errorClass)
			if _, getErr := client.CoreV1().Secrets(testNamespace).Get(
				context.Background(),
				testResolvedName,
				metav1.GetOptions{},
			); getErr != nil {
				t.Fatalf("secret should remain after rejected delete: %v", getErr)
			}
		})
	}
}

func TestReadStateReportsRemoteMetadataAndPayloadHash(t *testing.T) {
	secret := ownedSecret("", 1, []byte(`{"password":"secret"}`))
	delete(secret.Annotations, annotationPayloadSHA256)
	client := fake.NewSimpleClientset(secret)

	state, err := runtimeWithClient(t, client).ReadState(context.Background(), defaultReadStateRequest("", 1))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !state.Exists || !state.Owned || !state.OwnershipKnown {
		t.Fatalf("state ownership = exists %v owned %v known %v", state.Exists, state.Owned, state.OwnershipKnown)
	}
	if state.PayloadSHA256 == "" || state.PayloadSHA256 == testPayloadSHAOld {
		t.Fatalf("payload sha = %q, want hash computed from data", state.PayloadSHA256)
	}
	if state.SourceVersion != 1 {
		t.Fatalf("source version = %d, want 1", state.SourceVersion)
	}
}

func TestReadStateReportsSecretDataDriftDespiteMatchingMetadata(t *testing.T) {
	secret := ownedSecret(testPayloadSHANew, 1, []byte(`{"password":"old"}`))
	client := fake.NewSimpleClientset(secret)

	state, err := runtimeWithClient(t, client).ReadState(
		context.Background(),
		defaultReadStateRequest(testPayloadSHANew, 1),
	)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.PayloadSHA256 != testPayloadSHAOld {
		t.Fatalf("payload sha = %q, want %q", state.PayloadSHA256, testPayloadSHAOld)
	}
}

func TestReadStateReportsUnexpectedPayloadModeDataKeyAsDrift(t *testing.T) {
	secret := ownedSecret(testPayloadSHANew, 1, []byte(testPayloadNew))
	secret.Data["foreign"] = []byte("drift")
	client := fake.NewSimpleClientset(secret)

	state, err := runtimeWithClient(t, client).ReadState(
		context.Background(),
		defaultReadStateRequest(testPayloadSHANew, 1),
	)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.PayloadSHA256 == "" || state.PayloadSHA256 == testPayloadSHANew {
		t.Fatalf("payload sha = %q, want whole-secret drift hash", state.PayloadSHA256)
	}
}

func TestReadStateReportsDataMapPayloadHashFromManagedKeys(t *testing.T) {
	payload := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
	})
	secret := ownedDataMapSecret(payload.SHA256, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
		"foreign":  []byte("ignored"),
	}, []string{"password", "username"})
	delete(secret.Annotations, annotationPayloadSHA256)
	client := fake.NewSimpleClientset(secret)
	request := defaultReadStateRequest("", 1)
	request.DataMap = true

	state, err := runtimeWithClient(t, client).ReadState(context.Background(), request)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.PayloadSHA256 != payload.SHA256 {
		t.Fatalf("payload sha = %q, want %q", state.PayloadSHA256, payload.SHA256)
	}
}

func TestReadStateReportsDataMapDriftDespiteMatchingMetadata(t *testing.T) {
	desired := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("secret"),
	})
	drifted := mustDataMapPayload(t, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("drifted"),
	})
	secret := ownedDataMapSecret(desired.SHA256, map[string][]byte{
		"username": []byte("app"),
		"password": []byte("drifted"),
	}, []string{"password", "username"})
	client := fake.NewSimpleClientset(secret)
	request := defaultReadStateRequest(desired.SHA256, 1)
	request.DataMap = true

	state, err := runtimeWithClient(t, client).ReadState(context.Background(), request)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.PayloadSHA256 != drifted.SHA256 {
		t.Fatalf("payload sha = %q, want %q", state.PayloadSHA256, drifted.SHA256)
	}
}

func TestHealthClassifiesKubernetesFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(secretsResource, "", errors.New("denied"))
	})
	result, err := runtimeWithClient(t, client).Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if result.Healthy {
		t.Fatal("health must be unhealthy on Kubernetes failure")
	}
	if result.ErrorClass != providers.ErrorClassAuthz {
		t.Fatalf("health error class = %s, want %s", result.ErrorClass, providers.ErrorClassAuthz)
	}
}

func TestHealthUsesGetWithoutSecretListPermission(t *testing.T) {
	client := fake.NewSimpleClientset()
	result, err := runtimeWithClient(t, client).Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !result.Healthy {
		t.Fatalf("health result = %#v, want healthy missing probe secret", result)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "list" {
			t.Fatalf("health action = %#v, must not list secrets", action)
		}
	}
}

func TestErrorClassification(t *testing.T) {
	tests := map[string]struct {
		err           error
		expectedClass providers.ErrorClass
	}{
		"too many requests": {
			err:           apierrors.NewTooManyRequests("slow down", 1),
			expectedClass: providers.ErrorClassRateLimit,
		},
		"unauthorized": {
			err:           apierrors.NewUnauthorized("bad token"),
			expectedClass: providers.ErrorClassAuthn,
		},
		"forbidden": {
			err:           apierrors.NewForbidden(secretsResource, testResolvedName, errors.New("denied")),
			expectedClass: providers.ErrorClassAuthz,
		},
		"server timeout": {
			err:           apierrors.NewServerTimeout(secretsResource, "get", 1),
			expectedClass: providers.ErrorClassUnavailable,
		},
		"context deadline exceeded": {
			err:           context.DeadlineExceeded,
			expectedClass: providers.ErrorClassUnavailable,
		},
		"already exists": {
			err:           apierrors.NewAlreadyExists(secretsResource, testResolvedName),
			expectedClass: providers.ErrorClassCollision,
		},
		"conflict": {
			err:           apierrors.NewConflict(secretsResource, testResolvedName, errors.New("conflict")),
			expectedClass: providers.ErrorClassDrift,
		},
		"invalid": {
			err: apierrors.NewInvalid(
				schema.GroupKind{Kind: "Secret"},
				testResolvedName,
				field.ErrorList{field.Invalid(field.NewPath("metadata", "name"), "bad", "invalid")},
			),
			expectedClass: providers.ErrorClassValidation,
		},
		"payload too large": {
			err:           apierrors.NewRequestEntityTooLargeError("large"),
			expectedClass: providers.ErrorClassCapacity,
		},
		"unknown": {
			err:           errors.New("unknown"),
			expectedClass: providers.ErrorClassInternal,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := classifyKubernetesError(tt.err); got != tt.expectedClass {
				t.Fatalf("classify = %s, want %s", got, tt.expectedClass)
			}
		})
	}
}

func kubernetesMaturityMatrix() *providertest.MaturityMatrix {
	return &providertest.MaturityMatrix{
		OwnershipLoss: []providertest.MaturityCase{
			{
				Name:            "upsert-unowned-secret",
				Provider:        Provider{client: fake.NewSimpleClientset(unownedSecret())},
				Operation:       providertest.OperationUpsert,
				UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(testPayloadNew), 1),
				ErrorClass:      providers.ErrorClassOwnership,
				NoResultOnError: true,
			},
		},
		AuthFailure: providertest.MaturityCase{
			Name:            "get-unauthorized",
			Provider:        Provider{client: clientWithGetError(apierrors.NewUnauthorized("bad token"))},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(testPayloadNew), 1),
			ErrorClass:      providers.ErrorClassAuthn,
			NoResultOnError: true,
		},
		Throttling: providertest.MaturityCase{
			Name: "get-rate-limited",
			Provider: Provider{client: clientWithGetError(
				apierrors.NewTooManyRequests("slow down", 1),
			)},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(testPayloadNew), 1),
			ErrorClass:      providers.ErrorClassRateLimit,
			NoResultOnError: true,
		},
		PayloadLimit: providertest.MaturityCase{
			Name:            "oversized-payload",
			Provider:        Provider{client: fake.NewSimpleClientset()},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   oversizedKubernetesUpsertRequest(),
			ErrorClass:      providers.ErrorClassCapacity,
			NoResultOnError: true,
		},
		PartialSuccess: providertest.PartialSuccessCase{
			Name: "single-secret-update",
			Mode: providertest.PartialSuccessAtomic,
			Case: providertest.MaturityCase{
				Provider: Provider{client: fake.NewSimpleClientset(
					ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`)),
				)},
				Operation:     providertest.OperationUpsert,
				UpsertRequest: defaultUpsertRequest(testPayloadSHANew, []byte(testPayloadNew), 1),
				RemoteVersion: "rv-1",
			},
		},
		StaleRemoteState: providertest.MaturityCase{
			Name: "newer-remote-source-version",
			Provider: Provider{client: fake.NewSimpleClientset(
				ownedSecret(testPayloadSHAOld, 2, []byte(`{"password":"old"}`)),
			)},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(testPayloadNew), 1),
			ErrorClass:      providers.ErrorClassDrift,
			NoResultOnError: true,
		},
		DeleteSemantics: []providertest.MaturityCase{
			{
				Name:          "missing-delete-is-idempotent",
				Provider:      Provider{client: fake.NewSimpleClientset()},
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultDeleteRequest(1),
				RemoteVersion: "missing",
			},
			{
				Name: "owned-delete-removes-secret",
				Provider: Provider{client: fake.NewSimpleClientset(
					ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`)),
				)},
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultDeleteRequest(1),
				RemoteVersion: "rv-1",
			},
		},
	}
}

func clientWithGetError(err error) *fake.Clientset {
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
	return client
}

func oversizedKubernetesUpsertRequest() providers.UpsertRequest {
	return defaultUpsertRequest(testPayloadSHANew, make([]byte, secretMaxBytes+1), 1)
}

func defaultDestinationConfig() providers.DestinationConfig {
	return providers.DestinationConfig{
		Name: testDestinationName,
		Config: map[string]string{
			ConfigKeyNamespace: testNamespace,
			ConfigKeyAuthMode:  AuthModeInCluster,
		},
	}
}

func defaultPlanRequest(payloadSHA256 string, sourceVersion int) providers.PlanRequest {
	return planRequest(testResolvedName, payloadSHA256, sourceVersion)
}

func planRequest(resolvedName string, payloadSHA256 string, sourceVersion int) providers.PlanRequest {
	return providers.PlanRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  resolvedName,
		Format:        "json",
		PayloadSHA256: payloadSHA256,
		PayloadBytes:  21,
		SourcePath:    testSourcePath,
		SourceVersion: sourceVersion,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultUpsertRequest(payloadSHA256 string, payload []byte, sourceVersion int) providers.UpsertRequest {
	return providers.UpsertRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		Format:        "json",
		Payload:       payload,
		PayloadSHA256: payloadSHA256,
		SourcePath:    testSourcePath,
		SourceVersion: sourceVersion,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultDataMapUpsertRequest(payload payloadpkg.CanonicalPayload, sourceVersion int) providers.UpsertRequest {
	request := defaultUpsertRequest(payload.SHA256, payload.Bytes, sourceVersion)
	request.Format = payload.Format
	request.DataMap = payload.Data
	return request
}

func defaultDeleteRequest(sourceVersion int) providers.DeleteRequest {
	return providers.DeleteRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		SourcePath:    testSourcePath,
		SourceVersion: sourceVersion,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func mustDataMapPayload(t *testing.T, data map[string][]byte) payloadpkg.CanonicalPayload {
	t.Helper()
	payload, err := payloadpkg.BuildDataMap(data)
	if err != nil {
		t.Fatalf("build data-map payload: %v", err)
	}
	return payload
}

func defaultReadStateRequest(payloadSHA256 string, sourceVersion int) providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		PayloadSHA256: payloadSHA256,
		SourcePath:    testSourcePath,
		SourceVersion: sourceVersion,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func runtimeWithClient(t *testing.T, client kubernetes.Interface) providers.DestinationRuntime {
	t.Helper()
	destinationRuntime, err := (Provider{client: client}).OpenDestination(context.Background(), defaultDestinationConfig())
	if err != nil {
		t.Fatalf("open destination runtime: %v", err)
	}
	if destinationRuntime == nil {
		t.Fatal("destination runtime must not be nil")
	}
	return destinationRuntime
}

func defaultRuntimeIdentity() providers.RuntimeIdentity {
	return providers.RuntimeIdentity{
		PluginInstanceID: testPluginInstance,
		RestoreEpoch:     testRestoreEpoch,
	}
}

func ownedSecret(payloadSHA256 string, sourceVersion int, payload []byte) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testResolvedName,
			Namespace: testNamespace,
			Labels: map[string]string{
				labelManaged: "true",
			},
			Annotations: map[string]string{
				annotationAssociationID:  testAssociationID,
				annotationSourcePath:     testSourcePath,
				annotationSourceVersion:  "1",
				annotationObjectID:       testObjectID,
				annotationPayloadSHA256:  payloadSHA256,
				annotationFormat:         "json",
				annotationPluginInstance: testPluginInstance,
				annotationRestoreEpoch:   testRestoreEpoch,
			},
			ResourceVersion: "rv-1",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{dataKeyPayload: payload},
	}
	secret.Annotations[annotationSourceVersion] = strconv.Itoa(sourceVersion)
	return secret
}

func ownedDataMapSecret(
	payloadSHA256 string,
	data map[string][]byte,
	managedKeys []string,
) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testResolvedName,
			Namespace: testNamespace,
			Labels: map[string]string{
				labelManaged: "true",
			},
			Annotations: map[string]string{
				annotationAssociationID:  testAssociationID,
				annotationSourcePath:     testSourcePath,
				annotationSourceVersion:  "1",
				annotationObjectID:       testObjectID,
				annotationPayloadSHA256:  payloadSHA256,
				annotationFormat:         payloadpkg.FormatDataMap,
				annotationPluginInstance: testPluginInstance,
				annotationRestoreEpoch:   testRestoreEpoch,
			},
			ResourceVersion: "rv-1",
		},
		Type: corev1.SecretTypeOpaque,
		Data: copyDataMap(data),
	}
	applyDataMapMetadata(secret, managedKeys)
	return secret
}

func unownedSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testResolvedName,
			Namespace: testNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{dataKeyPayload: []byte(`{"password":"other"}`)},
	}
}

func immutableSecret(secret *corev1.Secret) *corev1.Secret {
	immutable := true
	secret.Immutable = &immutable
	return secret
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

func assertLabel(t *testing.T, secret *corev1.Secret, key string, expected string) {
	t.Helper()
	if got := secret.Labels[key]; got != expected {
		t.Fatalf("label %s = %s, want %s", key, got, expected)
	}
}

func assertAnnotation(t *testing.T, secret *corev1.Secret, key string, expected string) {
	t.Helper()
	if got := secret.Annotations[key]; got != expected {
		t.Fatalf("annotation %s = %s, want %s", key, got, expected)
	}
}

func assertDataKeysAnnotation(t *testing.T, secret *corev1.Secret, expected []string) {
	t.Helper()
	var got []string
	if err := json.Unmarshal([]byte(secret.Annotations[annotationDataKeys]), &got); err != nil {
		t.Fatalf("parse data keys annotation: %v", err)
	}
	assertStringSlice(t, got, expected)
}

func assertStringSlice(t *testing.T, got []string, expected []string) {
	t.Helper()
	if len(got) != len(expected) {
		t.Fatalf("slice = %v, want %v", got, expected)
	}
	for index := range got {
		if got[index] != expected[index] {
			t.Fatalf("slice = %v, want %v", got, expected)
		}
	}
}
