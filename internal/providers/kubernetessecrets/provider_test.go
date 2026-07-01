package kubernetessecrets

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/providertest"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testDestinationName = "cluster"
	testNamespace       = "apps"
	testResolvedName    = "app-db"
	testAssociationID   = "assoc-1"
	testSourcePath      = "app/db"
	testObjectID        = "secret-path"
	testPayloadSHAOld   = "sha256:old"
	testPayloadSHANew   = "sha256:new"
	testPluginInstance  = "inst-test"
	testRestoreEpoch    = "epoch-test"
)

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
				Request: defaultUpsertRequest(testPayloadSHANew, []byte(`{"password":"new"}`), 2),
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
			secret:  ownedSecret(testPayloadSHANew, 1, []byte(`{"password":"new"}`)),
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
			result, err := (Provider{client: client}).Plan(context.Background(), tt.request)
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

func TestUpsertCreatesSecretWithOwnershipMetadata(t *testing.T) {
	client := fake.NewSimpleClientset()
	result, err := (Provider{client: client}).Upsert(context.Background(), defaultUpsertRequest(
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

	_, err := (Provider{client: client}).Upsert(context.Background(), defaultUpsertRequest(
		testPayloadSHANew,
		[]byte(`{"password":"new"}`),
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
	if got := string(updated.Data[dataKeyPayload]); got != `{"password":"new"}` {
		t.Fatalf("secret payload = %s, want updated payload", got)
	}
	assertLabel(t, updated, "app", "demo")
	assertAnnotation(t, updated, "example.com/owner", "team-a")
	assertAnnotation(t, updated, annotationPayloadSHA256, testPayloadSHANew)
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
			_, err := (Provider{client: client}).Upsert(context.Background(), defaultUpsertRequest(
				testPayloadSHANew,
				[]byte(`{"password":"new"}`),
				1,
			))
			assertProviderErrorClass(t, err, tt.errorClass)
		})
	}
}

func TestOwnedByRequestRejectsRuntimeIdentityMismatch(t *testing.T) {
	request := defaultUpsertRequest(testPayloadSHANew, []byte(`{"password":"new"}`), 1)
	secret := ownedSecret(testPayloadSHANew, 1, []byte(`{"password":"new"}`))
	secret.Annotations[annotationPluginInstance] = testPluginInstance
	secret.Annotations[annotationRestoreEpoch] = testRestoreEpoch
	if !ownedByRequest(secret, ownershipIdentityFromUpsert(request)) {
		t.Fatal("ownedByRequest returned false for matching runtime identity")
	}
	secret.Annotations[annotationPluginInstance] = "inst-other"
	if ownedByRequest(secret, ownershipIdentityFromUpsert(request)) {
		t.Fatal("ownedByRequest returned true for mismatched plugin instance")
	}
}

func TestDeleteUsesOwnershipMetadata(t *testing.T) {
	client := fake.NewSimpleClientset(ownedSecret(testPayloadSHAOld, 1, []byte(`{"password":"old"}`)))
	_, err := (Provider{client: client}).Delete(context.Background(), defaultDeleteRequest(1))
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
			_, err := (Provider{client: client}).Delete(context.Background(), defaultDeleteRequest(1))
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

	state, err := (Provider{client: client}).ReadState(context.Background(), defaultReadStateRequest("", 1))
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

func TestHealthClassifiesKubernetesFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(secretsResource, "", errors.New("denied"))
	})
	result, err := (Provider{client: client}).Health(context.Background(), defaultDestinationConfig())
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
				UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(`{"password":"new"}`), 1),
				ErrorClass:      providers.ErrorClassOwnership,
				NoResultOnError: true,
			},
		},
		AuthFailure: providertest.MaturityCase{
			Name:            "get-unauthorized",
			Provider:        Provider{client: clientWithGetError(apierrors.NewUnauthorized("bad token"))},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(`{"password":"new"}`), 1),
			ErrorClass:      providers.ErrorClassAuthn,
			NoResultOnError: true,
		},
		Throttling: providertest.MaturityCase{
			Name: "get-rate-limited",
			Provider: Provider{client: clientWithGetError(
				apierrors.NewTooManyRequests("slow down", 1),
			)},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(`{"password":"new"}`), 1),
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
				UpsertRequest: defaultUpsertRequest(testPayloadSHANew, []byte(`{"password":"new"}`), 1),
				RemoteVersion: "rv-1",
			},
		},
		StaleRemoteState: providertest.MaturityCase{
			Name: "newer-remote-source-version",
			Provider: Provider{client: fake.NewSimpleClientset(
				ownedSecret(testPayloadSHAOld, 2, []byte(`{"password":"old"}`)),
			)},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte(`{"password":"new"}`), 1),
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
		Destination:   defaultDestinationConfig(),
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
		Destination:   defaultDestinationConfig(),
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

func defaultDeleteRequest(sourceVersion int) providers.DeleteRequest {
	return providers.DeleteRequest{
		Destination:   defaultDestinationConfig(),
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		SourcePath:    testSourcePath,
		SourceVersion: sourceVersion,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultReadStateRequest(payloadSHA256 string, sourceVersion int) providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Destination:   defaultDestinationConfig(),
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		PayloadSHA256: payloadSHA256,
		SourcePath:    testSourcePath,
		SourceVersion: sourceVersion,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
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
