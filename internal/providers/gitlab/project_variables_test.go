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
		if got := r.URL.Path; got != "/api/v4/projects/group%2Fproject/variables/APP_PASSWORD" {
			t.Fatalf("path = %s", got)
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
		ResolvedName:  testResolvedName,
		PayloadSHA256: payloadSHA256,
		SourcePath:    testSourcePath,
		SourceVersion: version,
		AssociationID: testAssociationID,
		ObjectID:      testObjectID,
	}
}

func ownedVariable(metadata variableMetadata) *gitlabVariable {
	return &gitlabVariable{
		Key:              testResolvedName,
		EnvironmentScope: testEnvScope,
		Description:      metadataDescription(metadata),
	}
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
}
