package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/adfinis/openbao-secret-sync/internal/payload"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/providertest"
)

const (
	testDestinationName = "prod"
	testProjectID       = "group/project"
	testToken           = "glpat-test"
	testEnvScope        = "production"
	testResolvedName    = "APP_PASSWORD"
	testAssociationID   = "assoc-1"
	testSourcePath      = "app/db"
	testObjectID        = "APP_PASSWORD"
	testPayloadSHAOld   = "sha256:old"
	testPayloadSHANew   = "sha256:new"
	testPluginInstance  = "inst-test"
	testRestoreEpoch    = "epoch-test"
)

func TestProviderConformance(t *testing.T) {
	client := newMemoryProjectVariableClient()
	providertest.Run(t, providertest.Harness{
		Provider:         Provider{client: client},
		ValidDestination: defaultDestinationConfig(),
		RequiredCapabilities: providertest.CapabilityExpectations{
			MetadataReadback:    true,
			SecretPath:          true,
			SecretKey:           true,
			UpdateIfOwned:       true,
			DeleteIfOwned:       true,
			PayloadHashMetadata: true,
			MinPayloadBytes:     variableValueMaxBytes,
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
			Name: "project-variable",
			CreatePlan: providertest.PlanCase{
				Name:    "create",
				Request: defaultPlanRequest(testPayloadSHAOld, 1),
				Action:  providers.PlanActionCreate,
			},
			Create: providertest.UpsertCase{
				Request:       defaultUpsertRequest(testPayloadSHAOld, []byte("old"), 1),
				RemoteVersion: testPayloadSHAOld,
			},
			StateAfterCreate: providertest.ReadStateCase{
				Request:        defaultReadStateRequest(testPayloadSHAOld, 1),
				Exists:         true,
				OwnershipKnown: true,
				Owned:          true,
				PayloadSHA256:  testPayloadSHAOld,
				SourceVersion:  1,
				RemoteVersion:  testPayloadSHAOld,
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
				Request:       defaultUpsertRequest(testPayloadSHANew, []byte("new"), 2),
				RemoteVersion: testPayloadSHANew,
			},
			StateAfterUpdate: providertest.ReadStateCase{
				Request:        defaultReadStateRequest(testPayloadSHANew, 2),
				Exists:         true,
				OwnershipKnown: true,
				Owned:          true,
				PayloadSHA256:  testPayloadSHANew,
				SourceVersion:  2,
				RemoteVersion:  testPayloadSHANew,
			},
			Delete: providertest.DeleteCase{
				Request:       defaultDeleteRequest(2),
				RemoteVersion: testPayloadSHANew,
			},
			StateAfterDelete: providertest.ReadStateCase{
				Request: defaultReadStateRequest(testPayloadSHANew, 2),
			},
		},
		Maturity: gitlabMaturityMatrix(),
	})
}

func TestValidateDestinationConfig(t *testing.T) {
	tests := []struct {
		name       string
		config     map[string]string
		errorClass providers.ErrorClass
	}{
		{
			name: "minimal",
			config: map[string]string{
				ConfigKeyProjectID: testProjectID,
				ConfigKeyToken:     testToken,
			},
		},
		{
			name: "self managed https",
			config: map[string]string{
				ConfigKeyBaseURL:   "https://gitlab.example.com",
				ConfigKeyProjectID: testProjectID,
				ConfigKeyToken:     testToken,
			},
		},
		{
			name: "local http",
			config: map[string]string{
				ConfigKeyBaseURL:   "http://127.0.0.1:8080",
				ConfigKeyProjectID: testProjectID,
				ConfigKeyToken:     testToken,
			},
		},
		{
			name: "options",
			config: map[string]string{
				ConfigKeyProjectID:        testProjectID,
				ConfigKeyToken:            testToken,
				ConfigKeyEnvironmentScope: testEnvScope,
				ConfigKeyProtected:        "true",
				ConfigKeyMasked:           "true",
				ConfigKeyHidden:           "false",
				ConfigKeyVariableRaw:      "true",
				ConfigKeyVariableType:     VariableTypeFile,
			},
		},
		{
			name: "missing project",
			config: map[string]string{
				ConfigKeyToken: testToken,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "missing token",
			config: map[string]string{
				ConfigKeyProjectID: testProjectID,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "remote http rejected",
			config: map[string]string{
				ConfigKeyBaseURL:   "http://gitlab.example.com",
				ConfigKeyProjectID: testProjectID,
				ConfigKeyToken:     testToken,
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "remote http with explicit insecure opt in",
			config: map[string]string{
				ConfigKeyBaseURL:           "http://gitlab",
				ConfigKeyProjectID:         testProjectID,
				ConfigKeyToken:             testToken,
				ConfigKeyAllowInsecureHTTP: "true",
			},
		},
		{
			name: "invalid insecure http opt in",
			config: map[string]string{
				ConfigKeyProjectID:         testProjectID,
				ConfigKeyToken:             testToken,
				ConfigKeyAllowInsecureHTTP: "sometimes",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "invalid bool",
			config: map[string]string{
				ConfigKeyProjectID: testProjectID,
				ConfigKeyToken:     testToken,
				ConfigKeyMasked:    "sometimes",
			},
			errorClass: providers.ErrorClassValidation,
		},
		{
			name: "invalid variable type",
			config: map[string]string{
				ConfigKeyProjectID:    testProjectID,
				ConfigKeyToken:        testToken,
				ConfigKeyVariableType: "docker",
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
			assertProviderErrorClass(t, err, tt.errorClass)
		})
	}
}

func TestPlanDetectsConflictAndDrift(t *testing.T) {
	tests := []struct {
		name       string
		variable   *gitlabVariable
		sourceSHA  string
		sourceVer  int
		action     string
		errorClass providers.ErrorClass
	}{
		{
			name:       "unowned",
			variable:   &gitlabVariable{Key: testResolvedName, Description: "created by humans"},
			sourceSHA:  testPayloadSHAOld,
			sourceVer:  1,
			action:     providers.PlanActionConflict,
			errorClass: providers.ErrorClassCollision,
		},
		{
			name: "newer remote version",
			variable: ownedVariable(variableMetadata{
				ManagedBy:     metadataManagedBy,
				AssociationID: testAssociationID,
				SourcePath:    testSourcePath,
				ObjectID:      testObjectID,
				SourceVersion: 3,
				PayloadSHA256: testPayloadSHAOld,
			}),
			sourceSHA:  testPayloadSHANew,
			sourceVer:  2,
			action:     providers.PlanActionBlocked,
			errorClass: providers.ErrorClassDrift,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMemoryProjectVariableClient()
			client.variables[variableStorageKey(testResolvedName, testEnvScope)] = tt.variable
			plan, err := (Provider{client: client}).Plan(context.Background(), defaultPlanRequest(tt.sourceSHA, tt.sourceVer))
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if plan.Action != tt.action {
				t.Fatalf("action = %s, want %s", plan.Action, tt.action)
			}
			if plan.ErrorClass != tt.errorClass {
				t.Fatalf("error class = %s, want %s", plan.ErrorClass, tt.errorClass)
			}
		})
	}
}

func TestProviderRejectsInvalidVariableKey(t *testing.T) {
	_, err := (Provider{client: newMemoryProjectVariableClient()}).Upsert(
		context.Background(),
		providers.UpsertRequest{
			Destination:   defaultDestinationConfig(),
			ResolvedName:  "app/password",
			Format:        payload.FormatRaw,
			Payload:       []byte("secret"),
			PayloadSHA256: testPayloadSHAOld,
			SourcePath:    testSourcePath,
			SourceVersion: 1,
			AssociationID: testAssociationID,
			ObjectID:      testObjectID,
		},
	)
	assertProviderErrorClass(t, err, providers.ErrorClassValidation)
}

func TestHTTPClientProjectVariableRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("PRIVATE-TOKEN"); got != testToken {
			t.Fatalf("private token header = %q, want %q", got, testToken)
		}
		if got := r.URL.Path; got != "/api/v4/projects/group/project/variables/APP_PASSWORD" {
			t.Fatalf("path = %s", got)
		}
		if got := r.URL.RawPath; got != "/api/v4/projects/group%2Fproject/variables/APP_PASSWORD" {
			t.Fatalf("raw path = %s", got)
		}
		if got := r.URL.Query().Get("filter[environment_scope]"); got != testEnvScope {
			t.Fatalf("environment scope filter = %q, want %q", got, testEnvScope)
		}
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("value"); got != "secret" {
			t.Fatalf("form value = %q, want secret", got)
		}
		if got := r.Form.Get("raw"); got != "true" {
			t.Fatalf("form raw = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode(gitlabVariable{
			Key:              testResolvedName,
			EnvironmentScope: testEnvScope,
			Description:      r.Form.Get("description"),
		})
	}))
	defer server.Close()

	options := defaultOptions()
	options.baseURL = server.URL
	client := httpProjectVariableClient{client: server.Client()}
	variable, err := client.UpdateVariable(context.Background(), options, testResolvedName, gitlabVariableInput{
		Key:              testResolvedName,
		Value:            "secret",
		EnvironmentScope: testEnvScope,
		VariableRaw:      true,
		VariableType:     VariableTypeEnvVar,
		Description:      "owned",
	})
	if err != nil {
		t.Fatalf("update variable: %v", err)
	}
	if variable.Key != testResolvedName {
		t.Fatalf("variable key = %s, want %s", variable.Key, testResolvedName)
	}
}

func TestDefaultClientFactoryUsesHardenedHTTPClient(t *testing.T) {
	client, err := defaultClientFactory(context.Background(), defaultDestinationConfig())
	if err != nil {
		t.Fatalf("default client factory: %v", err)
	}
	httpClient, ok := client.(httpProjectVariableClient)
	if !ok {
		t.Fatalf("client type = %T, want httpProjectVariableClient", client)
	}
	if httpClient.client == nil {
		t.Fatal("http client must be set")
	}
	if httpClient.client.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %s, want %s", httpClient.client.Timeout, defaultHTTPTimeout)
	}
	if httpClient.client.CheckRedirect == nil {
		t.Fatal("redirect policy must be set")
	}
	transport, ok := httpClient.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", httpClient.client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default GitLab HTTP client must not use ambient proxy configuration")
	}
}

func TestHTTPClientDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()

	options := defaultOptions()
	options.baseURL = server.URL
	client := httpProjectVariableClient{client: defaultGitLabHTTPClient()}
	err := client.GetProject(context.Background(), options)
	var httpErr gitlabHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T %[1]v, want gitlabHTTPError", err)
	}
	if httpErr.statusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", httpErr.statusCode, http.StatusFound)
	}
}

func TestHTTPClientLimitsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", gitlabResponseMaxBytes)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer server.Close()

	options := defaultOptions()
	options.baseURL = server.URL
	client := httpProjectVariableClient{client: defaultGitLabHTTPClient()}
	_, err := client.GetVariable(context.Background(), options, testResolvedName)
	if err == nil {
		t.Fatal("expected limited response decode error")
	}
	if got := classifyGitLabError(err); got != providers.ErrorClassUnavailable {
		t.Fatalf("error class = %s, want %s", got, providers.ErrorClassUnavailable)
	}
}

func gitlabMaturityMatrix() *providertest.MaturityMatrix {
	return &providertest.MaturityMatrix{
		OwnershipLoss: []providertest.MaturityCase{
			{
				Name:            "upsert-unowned-variable",
				Provider:        Provider{client: gitlabClientWithVariable(unownedVariable())},
				Operation:       providertest.OperationUpsert,
				UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte("new"), 2),
				ErrorClass:      providers.ErrorClassOwnership,
				NoResultOnError: true,
			},
		},
		AuthFailure: providertest.MaturityCase{
			Name: "get-unauthorized",
			Provider: Provider{client: gitlabClientWithError(
				"get",
				gitlabHTTPError{statusCode: http.StatusUnauthorized},
			)},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte("new"), 2),
			ErrorClass:      providers.ErrorClassAuthn,
			NoResultOnError: true,
		},
		Throttling: providertest.MaturityCase{
			Name: "create-rate-limited",
			Provider: Provider{client: gitlabClientWithError(
				"create",
				gitlabHTTPError{statusCode: http.StatusTooManyRequests},
			)},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte("new"), 2),
			ErrorClass:      providers.ErrorClassRateLimit,
			NoResultOnError: true,
		},
		PayloadLimit: providertest.MaturityCase{
			Name:            "oversized-payload",
			Provider:        Provider{client: newMemoryProjectVariableClient()},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   oversizedGitLabUpsertRequest(),
			ErrorClass:      providers.ErrorClassCapacity,
			NoResultOnError: true,
		},
		PartialSuccess: providertest.PartialSuccessCase{
			Name: "single-variable-update",
			Mode: providertest.PartialSuccessAtomic,
			Case: providertest.MaturityCase{
				Provider: Provider{client: gitlabClientWithVariable(ownedVariable(variableMetadata{
					ManagedBy:     metadataManagedBy,
					AssociationID: testAssociationID,
					SourcePath:    testSourcePath,
					ObjectID:      testObjectID,
					SourceVersion: 1,
					PayloadSHA256: testPayloadSHAOld,
				}))},
				Operation:     providertest.OperationUpsert,
				UpsertRequest: defaultUpsertRequest(testPayloadSHANew, []byte("new"), 2),
				RemoteVersion: testPayloadSHANew,
			},
		},
		StaleRemoteState: providertest.MaturityCase{
			Name: "newer-remote-source-version",
			Provider: Provider{client: gitlabClientWithVariable(ownedVariable(variableMetadata{
				ManagedBy:     metadataManagedBy,
				AssociationID: testAssociationID,
				SourcePath:    testSourcePath,
				ObjectID:      testObjectID,
				SourceVersion: 3,
				PayloadSHA256: testPayloadSHAOld,
			}))},
			Operation:       providertest.OperationUpsert,
			UpsertRequest:   defaultUpsertRequest(testPayloadSHANew, []byte("new"), 2),
			ErrorClass:      providers.ErrorClassDrift,
			NoResultOnError: true,
		},
		DeleteSemantics: []providertest.MaturityCase{
			{
				Name:          "missing-delete-is-idempotent",
				Provider:      Provider{client: newMemoryProjectVariableClient()},
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultDeleteRequest(1),
				RemoteVersion: "missing",
			},
			{
				Name: "owned-delete-removes-variable",
				Provider: Provider{client: gitlabClientWithVariable(ownedVariable(variableMetadata{
					ManagedBy:     metadataManagedBy,
					AssociationID: testAssociationID,
					SourcePath:    testSourcePath,
					ObjectID:      testObjectID,
					SourceVersion: 1,
					PayloadSHA256: testPayloadSHAOld,
				}))},
				Operation:     providertest.OperationDelete,
				DeleteRequest: defaultDeleteRequest(1),
				RemoteVersion: testPayloadSHAOld,
			},
		},
	}
}

func defaultDestinationConfig() providers.DestinationConfig {
	return providers.DestinationConfig{
		Name: testDestinationName,
		Config: map[string]string{
			ConfigKeyProjectID:        testProjectID,
			ConfigKeyToken:            testToken,
			ConfigKeyEnvironmentScope: testEnvScope,
		},
	}
}

func defaultOptions() gitlabDestinationOptions {
	options, err := gitlabDestinationOptionsFromConfig(defaultDestinationConfig())
	if err != nil {
		panic(err)
	}
	return options
}

func defaultPlanRequest(payloadSHA256 string, version int) providers.PlanRequest {
	return providers.PlanRequest{
		Destination:   defaultDestinationConfig(),
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		Format:        payload.FormatRaw,
		PayloadSHA256: payloadSHA256,
		PayloadBytes:  3,
		SourcePath:    testSourcePath,
		SourceVersion: version,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultUpsertRequest(payloadSHA256 string, value []byte, version int) providers.UpsertRequest {
	return providers.UpsertRequest{
		Destination:   defaultDestinationConfig(),
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		Format:        payload.FormatRaw,
		Payload:       value,
		PayloadSHA256: payloadSHA256,
		SourcePath:    testSourcePath,
		SourceVersion: version,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultDeleteRequest(version int) providers.DeleteRequest {
	return providers.DeleteRequest{
		Destination:   defaultDestinationConfig(),
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		SourcePath:    testSourcePath,
		SourceVersion: version,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func defaultReadStateRequest(payloadSHA256 string, version int) providers.ReadStateRequest {
	return providers.ReadStateRequest{
		Destination:   defaultDestinationConfig(),
		Runtime:       defaultRuntimeIdentity(),
		ResolvedName:  testResolvedName,
		PayloadSHA256: payloadSHA256,
		SourcePath:    testSourcePath,
		SourceVersion: version,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func oversizedGitLabUpsertRequest() providers.UpsertRequest {
	request := defaultUpsertRequest(testPayloadSHANew, []byte("new"), 2)
	request.Payload = make([]byte, variableValueMaxBytes+1)
	return request
}

func defaultRuntimeIdentity() providers.RuntimeIdentity {
	return providers.RuntimeIdentity{
		PluginInstanceID: testPluginInstance,
		RestoreEpoch:     testRestoreEpoch,
	}
}

func ownedVariable(metadata variableMetadata) *gitlabVariable {
	if metadata.PluginInstanceID == "" {
		metadata.PluginInstanceID = testPluginInstance
	}
	if metadata.RestoreEpoch == "" {
		metadata.RestoreEpoch = testRestoreEpoch
	}
	return &gitlabVariable{
		Key:              testResolvedName,
		EnvironmentScope: testEnvScope,
		Description:      metadataDescription(metadata),
	}
}

func unownedVariable() *gitlabVariable {
	return &gitlabVariable{
		Key:              testResolvedName,
		EnvironmentScope: testEnvScope,
		Description:      "created outside openbao-secret-sync",
	}
}

func gitlabClientWithVariable(variable *gitlabVariable) *memoryProjectVariableClient {
	client := newMemoryProjectVariableClient()
	client.variables[variableStorageKey(variable.Key, variable.EnvironmentScope)] = variable
	return client
}

func gitlabClientWithError(operation string, err error) *memoryProjectVariableClient {
	client := newMemoryProjectVariableClient()
	client.errors[operation] = err
	return client
}

type memoryProjectVariableClient struct {
	variables map[string]*gitlabVariable
	errors    map[string]error
}

func newMemoryProjectVariableClient() *memoryProjectVariableClient {
	return &memoryProjectVariableClient{
		variables: map[string]*gitlabVariable{},
		errors:    map[string]error{},
	}
}

func (c *memoryProjectVariableClient) GetProject(context.Context, gitlabDestinationOptions) error {
	return c.errors["project"]
}

func (c *memoryProjectVariableClient) GetVariable(
	_ context.Context,
	options gitlabDestinationOptions,
	key string,
) (*gitlabVariable, error) {
	if err := c.errors["get"]; err != nil {
		return nil, err
	}
	variable, ok := c.variables[variableStorageKey(key, options.environmentScope)]
	if !ok {
		return nil, gitlabHTTPError{statusCode: http.StatusNotFound}
	}
	copy := *variable
	return &copy, nil
}

func (c *memoryProjectVariableClient) CreateVariable(
	_ context.Context,
	options gitlabDestinationOptions,
	input gitlabVariableInput,
) (*gitlabVariable, error) {
	if err := c.errors["create"]; err != nil {
		return nil, err
	}
	key := variableStorageKey(input.Key, options.environmentScope)
	if _, exists := c.variables[key]; exists {
		return nil, gitlabHTTPError{statusCode: http.StatusConflict}
	}
	variable := variableFromInput(input)
	c.variables[key] = variable
	copy := *variable
	return &copy, nil
}

func (c *memoryProjectVariableClient) UpdateVariable(
	_ context.Context,
	options gitlabDestinationOptions,
	key string,
	input gitlabVariableInput,
) (*gitlabVariable, error) {
	if err := c.errors["update"]; err != nil {
		return nil, err
	}
	storageKey := variableStorageKey(key, options.environmentScope)
	if _, exists := c.variables[storageKey]; !exists {
		return nil, gitlabHTTPError{statusCode: http.StatusNotFound}
	}
	variable := variableFromInput(input)
	c.variables[storageKey] = variable
	copy := *variable
	return &copy, nil
}

func (c *memoryProjectVariableClient) DeleteVariable(
	_ context.Context,
	options gitlabDestinationOptions,
	key string,
) error {
	if err := c.errors["delete"]; err != nil {
		return err
	}
	storageKey := variableStorageKey(key, options.environmentScope)
	if _, exists := c.variables[storageKey]; !exists {
		return gitlabHTTPError{statusCode: http.StatusNotFound}
	}
	delete(c.variables, storageKey)
	return nil
}

func variableFromInput(input gitlabVariableInput) *gitlabVariable {
	return &gitlabVariable{
		Key:              input.Key,
		Value:            input.Value,
		EnvironmentScope: input.EnvironmentScope,
		Protected:        input.Protected,
		Masked:           input.Masked,
		VariableRaw:      input.VariableRaw,
		VariableType:     input.VariableType,
		Description:      input.Description,
	}
}

func variableStorageKey(key string, environmentScope string) string {
	return key + "\x00" + environmentScope
}

func assertProviderErrorClass(t *testing.T, err error, expected providers.ErrorClass) {
	t.Helper()
	if expected == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("error = nil, want class %s", expected)
	}
	if !strings.Contains(err.Error(), "gitlab") && !strings.Contains(err.Error(), string(expected)) {
		t.Fatalf("error = %v, want gitlab provider error", err)
	}
	var providerError *providers.Error
	if !errors.As(err, &providerError) || providerError.Class != expected {
		t.Fatalf("error = %v, want class %s", err, expected)
	}
}

func TestVariableFormOmitsSecretFromDescription(t *testing.T) {
	input := variableInputFromUpsert(defaultOptions(), defaultUpsertRequest(testPayloadSHAOld, []byte("secret-canary"), 1))
	form := input.form()
	if strings.Contains(form.Get("description"), "secret-canary") {
		t.Fatalf("description contains secret value: %s", form.Get("description"))
	}
	parsed, err := url.ParseQuery(form.Encode())
	if err != nil {
		t.Fatalf("parse encoded form: %v", err)
	}
	if got := parsed.Get("value"); got != "secret-canary" {
		t.Fatalf("encoded value = %q, want secret-canary", got)
	}
	metadata, owned := ownershipMetadata(&gitlabVariable{Description: form.Get("description")})
	if !owned {
		t.Fatalf("description metadata is not owned: %s", form.Get("description"))
	}
	if metadata.PluginInstanceID != testPluginInstance {
		t.Fatalf("plugin instance = %s, want %s", metadata.PluginInstanceID, testPluginInstance)
	}
	if metadata.RestoreEpoch != testRestoreEpoch {
		t.Fatalf("restore epoch = %s, want %s", metadata.RestoreEpoch, testRestoreEpoch)
	}
}

func TestOwnedByRequestRejectsRuntimeIdentityMismatch(t *testing.T) {
	request := defaultUpsertRequest(testPayloadSHANew, []byte("secret"), 2)
	metadata, owned := ownershipMetadata(ownedVariable(variableMetadata{
		ManagedBy:        metadataManagedBy,
		AssociationID:    request.AssociationID,
		SourcePath:       request.SourcePath,
		ObjectID:         request.ObjectID,
		PluginInstanceID: request.Runtime.PluginInstanceID,
		RestoreEpoch:     request.Runtime.RestoreEpoch,
		SourceVersion:    request.SourceVersion,
		PayloadSHA256:    request.PayloadSHA256,
		PayloadFormat:    request.Format,
	}))
	if !ownedByRequest(metadata, owned, ownershipIdentityFromUpsert(request)) {
		t.Fatal("ownedByRequest returned false for matching runtime identity")
	}
	metadata.PluginInstanceID = "inst-other"
	if ownedByRequest(metadata, owned, ownershipIdentityFromUpsert(request)) {
		t.Fatal("ownedByRequest returned true for mismatched plugin instance")
	}
}

func TestVariableMetadataDescriptionFitsGitLabLimit(t *testing.T) {
	request := defaultUpsertRequest(testPayloadSHANew, []byte("secret"), 2)
	request.ObjectID = strings.Repeat("A", variableKeyMaxBytes)
	request.ResolvedName = request.ObjectID
	request.SourcePath = strings.Repeat("path/", 50)

	input := variableInputFromUpsert(defaultOptions(), request)
	if len(input.Description) > variableDescriptionMaxBytes {
		t.Fatalf(
			"description length = %d, want <= %d: %s",
			len(input.Description),
			variableDescriptionMaxBytes,
			input.Description,
		)
	}

	metadata, owned := ownershipMetadata(&gitlabVariable{Description: input.Description})
	if !ownedByRequest(metadata, owned, ownershipIdentityFromUpsert(request)) {
		t.Fatalf("metadata does not match request identity: %#v", metadata)
	}
	if metadata.PayloadSHA256 != testPayloadSHANew {
		t.Fatalf("payload sha = %s, want %s", metadata.PayloadSHA256, testPayloadSHANew)
	}
	if metadata.PluginInstanceID != testPluginInstance {
		t.Fatalf("plugin instance = %s, want %s", metadata.PluginInstanceID, testPluginInstance)
	}
	if metadata.RestoreEpoch != testRestoreEpoch {
		t.Fatalf("restore epoch = %s, want %s", metadata.RestoreEpoch, testRestoreEpoch)
	}
}
