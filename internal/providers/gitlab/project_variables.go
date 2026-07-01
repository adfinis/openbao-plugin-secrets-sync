// Package gitlab provides the GitLab project CI/CD variable destination provider.
package gitlab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
)

const (
	// ProviderType is the stable destination type used by associations.
	ProviderType = "gitlab"

	// ConfigKeyBaseURL configures the GitLab instance URL. It defaults to https://gitlab.com.
	ConfigKeyBaseURL = "base_url"
	// ConfigKeyProjectID configures the GitLab project id or URL-encoded path.
	ConfigKeyProjectID = "project_id"
	// ConfigKeyEnvironmentScope configures the GitLab variable environment scope.
	ConfigKeyEnvironmentScope = "environment_scope"
	// ConfigKeyProtected configures whether variables are protected.
	ConfigKeyProtected = "protected"
	// ConfigKeyMasked configures whether variables are masked.
	ConfigKeyMasked = "masked"
	// ConfigKeyHidden configures whether variables are created as masked and hidden.
	ConfigKeyHidden = "hidden"
	// ConfigKeyVariableRaw configures whether variable reference expansion is disabled.
	ConfigKeyVariableRaw = "variable_raw"
	// ConfigKeyVariableType configures the GitLab variable type.
	ConfigKeyVariableType = "variable_type"
	// ConfigKeyAllowInsecureHTTP allows non-local http GitLab URLs for local test networks.
	ConfigKeyAllowInsecureHTTP = "allow_insecure_http"
	// ConfigKeyToken configures the GitLab API token.
	ConfigKeyToken = "token"

	VariableTypeEnvVar = "env_var"
	VariableTypeFile   = "file"

	defaultBaseURL          = "https://gitlab.com"
	defaultEnvironmentScope = "*"
	defaultVariableType     = VariableTypeEnvVar

	variableValueMaxBytes       = 10000
	variableKeyMaxBytes         = 255
	variableDescriptionMaxBytes = 255

	metadataManagedBy        = "openbao-secret-sync"
	metadataManagedByCompact = "1"
)

type projectVariableClient interface {
	GetProject(context.Context, gitlabDestinationOptions) error
	GetVariable(context.Context, gitlabDestinationOptions, string) (*gitlabVariable, error)
	CreateVariable(context.Context, gitlabDestinationOptions, gitlabVariableInput) (*gitlabVariable, error)
	UpdateVariable(context.Context, gitlabDestinationOptions, string, gitlabVariableInput) (*gitlabVariable, error)
	DeleteVariable(context.Context, gitlabDestinationOptions, string) error
}

type clientFactory func(context.Context, providers.DestinationConfig) (projectVariableClient, error)

// Provider is the GitLab project variable provider.
type Provider struct {
	client        projectVariableClient
	clientFactory clientFactory
}

// New returns a provider using the GitLab HTTP API.
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
		SupportsSecretKey:           true,
		MaxPayloadBytes:             variableValueMaxBytes,
	}
}

func (Provider) Validate(_ context.Context, cfg providers.DestinationConfig) error {
	if strings.TrimSpace(cfg.Name) == "" {
		return validationError("gitlab destination name must not be empty")
	}
	_, err := gitlabDestinationOptionsFromConfig(cfg)
	return err
}

func (p Provider) Plan(ctx context.Context, req providers.PlanRequest) (*providers.PlanResult, error) {
	client, options, err := p.clientAndOptions(ctx, req.Destination)
	if err != nil {
		return blockedPlan(setupErrorClass(err)), nil
	}
	if err := validateVariableKey(req.ResolvedName); err != nil {
		return blockedPlan(providers.ErrorClassValidation), nil
	}
	variable, err := client.GetVariable(ctx, options, req.ResolvedName)
	if isGitLabNotFound(err) {
		return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
	}
	if err != nil {
		return blockedPlan(classifyGitLabError(err)), nil
	}
	metadata, owned := ownershipMetadata(variable)
	if !ownedByRequest(metadata, owned, ownershipIdentityFromPlan(req)) {
		return &providers.PlanResult{
			Action:     providers.PlanActionConflict,
			ErrorClass: providers.ErrorClassCollision,
			Message:    "gitlab variable exists but is not owned by this association",
		}, nil
	}
	if remoteSourceVersionNewer(metadata, req.SourceVersion) {
		return &providers.PlanResult{
			Action:     providers.PlanActionBlocked,
			ErrorClass: providers.ErrorClassDrift,
			Message:    "gitlab variable has newer managed source version",
		}, nil
	}
	if metadata.PayloadSHA256 == req.PayloadSHA256 {
		return &providers.PlanResult{Action: providers.PlanActionNoop}, nil
	}
	return &providers.PlanResult{Action: providers.PlanActionUpdate}, nil
}

func (p Provider) Upsert(ctx context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	client, options, err := p.clientAndOptions(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
	}
	if err := validateVariableKey(req.ResolvedName); err != nil {
		return nil, err
	}
	if len(req.Payload) > variableValueMaxBytes {
		return nil, providerError(providers.ErrorClassCapacity)
	}
	input := variableInputFromUpsert(options, req)
	variable, err := client.GetVariable(ctx, options, req.ResolvedName)
	if isGitLabNotFound(err) {
		created, createErr := client.CreateVariable(ctx, options, input)
		if createErr != nil {
			return nil, providerError(classifyGitLabError(createErr))
		}
		return &providers.SyncResult{RemoteVersion: remoteVersion(created)}, nil
	}
	if err != nil {
		return nil, providerError(classifyGitLabError(err))
	}
	metadata, owned := ownershipMetadata(variable)
	if !ownedByRequest(metadata, owned, ownershipIdentityFromUpsert(req)) {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(metadata, req.SourceVersion) {
		return nil, providerError(providers.ErrorClassDrift)
	}
	if metadata.PayloadSHA256 == req.PayloadSHA256 {
		return &providers.SyncResult{RemoteVersion: remoteVersion(variable)}, nil
	}
	updated, err := client.UpdateVariable(ctx, options, req.ResolvedName, input)
	if err != nil {
		return nil, providerError(classifyGitLabError(err))
	}
	return &providers.SyncResult{RemoteVersion: remoteVersion(updated)}, nil
}

func (p Provider) Delete(ctx context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	client, options, err := p.clientAndOptions(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
	}
	if err := validateVariableKey(req.ResolvedName); err != nil {
		return nil, err
	}
	variable, err := client.GetVariable(ctx, options, req.ResolvedName)
	if isGitLabNotFound(err) {
		return &providers.SyncResult{RemoteVersion: "missing"}, nil
	}
	if err != nil {
		return nil, providerError(classifyGitLabError(err))
	}
	metadata, owned := ownershipMetadata(variable)
	if !ownedByRequest(metadata, owned, ownershipIdentityFromDelete(req)) {
		return nil, providerError(providers.ErrorClassOwnership)
	}
	if remoteSourceVersionNewer(metadata, req.SourceVersion) {
		return nil, providerError(providers.ErrorClassDrift)
	}
	if err := client.DeleteVariable(ctx, options, req.ResolvedName); err != nil {
		return nil, providerError(classifyGitLabError(err))
	}
	return &providers.SyncResult{RemoteVersion: remoteVersion(variable)}, nil
}

func (p Provider) ReadState(ctx context.Context, req providers.ReadStateRequest) (*providers.RemoteState, error) {
	client, options, err := p.clientAndOptions(ctx, req.Destination)
	if err != nil {
		return nil, providerError(setupErrorClass(err))
	}
	if err := validateVariableKey(req.ResolvedName); err != nil {
		return nil, err
	}
	variable, err := client.GetVariable(ctx, options, req.ResolvedName)
	if isGitLabNotFound(err) {
		return &providers.RemoteState{Exists: false}, nil
	}
	if err != nil {
		return nil, providerError(classifyGitLabError(err))
	}
	metadata, owned := ownershipMetadata(variable)
	return &providers.RemoteState{
		Exists:         true,
		OwnershipKnown: true,
		Owned:          ownedByRequest(metadata, owned, ownershipIdentityFromReadState(req)),
		PayloadSHA256:  metadata.PayloadSHA256,
		SourceVersion:  metadata.SourceVersion,
		RemoteVersion:  remoteVersion(variable),
	}, nil
}

func (p Provider) Health(ctx context.Context, cfg providers.DestinationConfig) (*providers.HealthResult, error) {
	client, options, err := p.clientAndOptions(ctx, cfg)
	if err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "gitlab client initialization failed",
			ErrorClass: setupErrorClass(err),
		}, nil
	}
	if err := client.GetProject(ctx, options); err != nil {
		return &providers.HealthResult{
			Healthy:    false,
			Message:    "gitlab health check failed",
			ErrorClass: classifyGitLabError(err),
		}, nil
	}
	return &providers.HealthResult{Healthy: true}, nil
}

func (p Provider) clientAndOptions(
	ctx context.Context,
	cfg providers.DestinationConfig,
) (projectVariableClient, gitlabDestinationOptions, error) {
	options, err := gitlabDestinationOptionsFromConfig(cfg)
	if err != nil {
		return nil, gitlabDestinationOptions{}, err
	}
	if p.client != nil {
		return p.client, options, nil
	}
	factory := p.clientFactory
	if factory == nil {
		factory = defaultClientFactory
	}
	client, err := factory(ctx, cfg)
	if err != nil {
		return nil, gitlabDestinationOptions{}, err
	}
	return client, options, nil
}

func defaultClientFactory(_ context.Context, _ providers.DestinationConfig) (projectVariableClient, error) {
	return httpProjectVariableClient{client: http.DefaultClient}, nil
}

type gitlabDestinationOptions struct {
	baseURL           string
	projectID         string
	environmentScope  string
	protected         bool
	masked            bool
	hidden            bool
	variableRaw       bool
	variableType      string
	allowInsecureHTTP bool
	token             string
}

func gitlabDestinationOptionsFromConfig(cfg providers.DestinationConfig) (gitlabDestinationOptions, error) {
	options := gitlabDestinationOptions{
		baseURL:          stringDefault(configValue(cfg, ConfigKeyBaseURL), defaultBaseURL),
		projectID:        configValue(cfg, ConfigKeyProjectID),
		environmentScope: stringDefault(configValue(cfg, ConfigKeyEnvironmentScope), defaultEnvironmentScope),
		variableRaw:      true,
		variableType:     stringDefault(configValue(cfg, ConfigKeyVariableType), defaultVariableType),
		token:            configValue(cfg, ConfigKeyToken),
	}
	var err error
	if options.protected, err = boolConfigValue(cfg, ConfigKeyProtected, false); err != nil {
		return gitlabDestinationOptions{}, err
	}
	if options.masked, err = boolConfigValue(cfg, ConfigKeyMasked, false); err != nil {
		return gitlabDestinationOptions{}, err
	}
	if options.hidden, err = boolConfigValue(cfg, ConfigKeyHidden, false); err != nil {
		return gitlabDestinationOptions{}, err
	}
	if options.variableRaw, err = boolConfigValue(cfg, ConfigKeyVariableRaw, true); err != nil {
		return gitlabDestinationOptions{}, err
	}
	if options.allowInsecureHTTP, err = boolConfigValue(cfg, ConfigKeyAllowInsecureHTTP, false); err != nil {
		return gitlabDestinationOptions{}, err
	}
	if err := validateBaseURL(options.baseURL, options.allowInsecureHTTP); err != nil {
		return gitlabDestinationOptions{}, err
	}
	if options.projectID == "" {
		return gitlabDestinationOptions{}, validationError("gitlab project_id must not be empty")
	}
	if options.token == "" {
		return gitlabDestinationOptions{}, validationError("gitlab token must not be empty")
	}
	if options.environmentScope == "" {
		return gitlabDestinationOptions{}, validationError("gitlab environment_scope must not be empty")
	}
	if options.variableType != VariableTypeEnvVar && options.variableType != VariableTypeFile {
		return gitlabDestinationOptions{}, validationError("gitlab variable_type must be env_var or file")
	}
	return options, nil
}

func configValue(cfg providers.DestinationConfig, key string) string {
	if cfg.Config == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Config[key])
}

func stringDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func boolConfigValue(cfg providers.DestinationConfig, key string, fallback bool) (bool, error) {
	value := configValue(cfg, key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, validationError(fmt.Sprintf("gitlab %s must be true or false", key))
	}
	return parsed, nil
}

func validateBaseURL(rawBaseURL string, allowInsecureHTTP bool) error {
	parsed, err := url.Parse(rawBaseURL)
	if err != nil {
		return validationError("gitlab base_url must be a valid URL")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return validationError("gitlab base_url must include a host and no userinfo, query, or fragment")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLocalHost(parsed.Hostname()) || allowInsecureHTTP {
			return nil
		}
		return validationError("gitlab http base_url requires allow_insecure_http=true unless it targets localhost")
	default:
		return validationError("gitlab base_url must use http or https")
	}
}

func isLocalHost(host string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if normalized == "localhost" || strings.HasSuffix(normalized, ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(normalized)
	return err == nil && addr.IsLoopback()
}

func validateVariableKey(key string) error {
	if key == "" {
		return validationError("gitlab variable key must not be empty")
	}
	if len(key) > variableKeyMaxBytes {
		return validationError("gitlab variable key exceeds 255 characters")
	}
	for _, char := range key {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return validationError("gitlab variable key may contain only letters, digits, and underscore")
	}
	return nil
}

type ownershipIdentity struct {
	AssociationID string
	SourcePath    string
	ObjectID      string
}

func ownershipIdentityFromPlan(req providers.PlanRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID: req.AssociationID,
		SourcePath:    req.SourcePath,
		ObjectID:      req.ObjectID,
	}
}

func ownershipIdentityFromUpsert(req providers.UpsertRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID: req.AssociationID,
		SourcePath:    req.SourcePath,
		ObjectID:      req.ObjectID,
	}
}

func ownershipIdentityFromDelete(req providers.DeleteRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID: req.AssociationID,
		SourcePath:    req.SourcePath,
		ObjectID:      req.ObjectID,
	}
}

func ownershipIdentityFromReadState(req providers.ReadStateRequest) ownershipIdentity {
	return ownershipIdentity{
		AssociationID: req.AssociationID,
		SourcePath:    req.SourcePath,
		ObjectID:      req.ObjectID,
	}
}

type variableMetadata struct {
	ManagedBy      string `json:"managed_by"`
	AssociationID  string `json:"association_id"`
	SourcePath     string `json:"source_path"`
	SourcePathHash string `json:"-"`
	ObjectID       string `json:"object_id"`
	ObjectIDHash   string `json:"-"`
	SourceVersion  int    `json:"source_version"`
	PayloadSHA256  string `json:"payload_sha256"`
	PayloadFormat  string `json:"payload_format"`
	EnvironmentRef string `json:"environment_scope"`
}

type variableMetadataWire struct {
	ManagedBy      string `json:"m"`
	AssociationID  string `json:"a"`
	SourcePath     string `json:"p,omitempty"`
	SourcePathHash string `json:"ph,omitempty"`
	ObjectID       string `json:"o,omitempty"`
	ObjectIDHash   string `json:"oh,omitempty"`
	SourceVersion  int    `json:"v"`
	PayloadSHA256  string `json:"h"`
	PayloadFormat  string `json:"f"`
}

func variableInputFromUpsert(options gitlabDestinationOptions, req providers.UpsertRequest) gitlabVariableInput {
	metadata := variableMetadata{
		ManagedBy:      metadataManagedBy,
		AssociationID:  req.AssociationID,
		SourcePath:     req.SourcePath,
		ObjectID:       req.ObjectID,
		SourceVersion:  req.SourceVersion,
		PayloadSHA256:  req.PayloadSHA256,
		PayloadFormat:  req.Format,
		EnvironmentRef: options.environmentScope,
	}
	return gitlabVariableInput{
		Key:              req.ResolvedName,
		Value:            string(req.Payload),
		EnvironmentScope: options.environmentScope,
		Protected:        options.protected,
		Masked:           options.masked,
		Hidden:           options.hidden,
		VariableRaw:      options.variableRaw,
		VariableType:     options.variableType,
		Description:      metadataDescription(metadata),
	}
}

func metadataDescription(metadata variableMetadata) string {
	wire := variableMetadataWire{
		ManagedBy:     metadataManagedByCompact,
		AssociationID: metadata.AssociationID,
		SourcePath:    metadata.SourcePath,
		ObjectID:      metadata.ObjectID,
		SourceVersion: metadata.SourceVersion,
		PayloadSHA256: metadata.PayloadSHA256,
		PayloadFormat: metadata.PayloadFormat,
	}
	payload := mustMarshalMetadata(wire)
	if len(payload) <= variableDescriptionMaxBytes {
		return payload
	}
	wire.ObjectID = ""
	wire.ObjectIDHash = metadataIdentityHash(metadata.ObjectID)
	payload = mustMarshalMetadata(wire)
	if len(payload) <= variableDescriptionMaxBytes {
		return payload
	}
	wire.SourcePath = ""
	wire.SourcePathHash = metadataIdentityHash(metadata.SourcePath)
	return mustMarshalMetadata(wire)
}

func mustMarshalMetadata(metadata variableMetadataWire) string {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return ""
	}
	return string(payload)
}

func ownershipMetadata(variable *gitlabVariable) (variableMetadata, bool) {
	if variable == nil {
		return variableMetadata{}, false
	}
	var wire variableMetadataWire
	if err := json.Unmarshal([]byte(variable.Description), &wire); err == nil && wire.ManagedBy != "" {
		metadata := variableMetadata{
			ManagedBy:      metadataManagedBy,
			AssociationID:  wire.AssociationID,
			SourcePath:     wire.SourcePath,
			SourcePathHash: wire.SourcePathHash,
			ObjectID:       wire.ObjectID,
			ObjectIDHash:   wire.ObjectIDHash,
			SourceVersion:  wire.SourceVersion,
			PayloadSHA256:  wire.PayloadSHA256,
			PayloadFormat:  wire.PayloadFormat,
		}
		return metadata, wire.ManagedBy == metadataManagedByCompact || wire.ManagedBy == metadataManagedBy
	}
	var metadata variableMetadata
	if err := json.Unmarshal([]byte(variable.Description), &metadata); err != nil {
		return variableMetadata{}, false
	}
	return metadata, metadata.ManagedBy == metadataManagedBy
}

func ownedByRequest(metadata variableMetadata, metadataOwned bool, identity ownershipIdentity) bool {
	if !metadataOwned {
		return false
	}
	sourcePathMatches := metadata.SourcePath == identity.SourcePath ||
		(metadata.SourcePathHash != "" && metadata.SourcePathHash == metadataIdentityHash(identity.SourcePath))
	objectIDMatches := metadata.ObjectID == identity.ObjectID ||
		(metadata.ObjectIDHash != "" && metadata.ObjectIDHash == metadataIdentityHash(identity.ObjectID))
	return metadata.AssociationID == identity.AssociationID &&
		sourcePathMatches &&
		objectIDMatches &&
		identity.AssociationID != "" &&
		identity.SourcePath != "" &&
		identity.ObjectID != ""
}

func metadataIdentityHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func remoteSourceVersionNewer(metadata variableMetadata, sourceVersion int) bool {
	return sourceVersion > 0 && metadata.SourceVersion > sourceVersion
}

func remoteVersion(variable *gitlabVariable) string {
	if variable == nil {
		return ""
	}
	metadata, owned := ownershipMetadata(variable)
	if owned && metadata.PayloadSHA256 != "" {
		return metadata.PayloadSHA256
	}
	sum := sha256.Sum256([]byte(variable.Key + ":" + variable.EnvironmentScope + ":" + variable.Description))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validationError(message string) error {
	return &providers.Error{Class: providers.ErrorClassValidation, Message: message}
}

func blockedPlan(errorClass providers.ErrorClass) *providers.PlanResult {
	return &providers.PlanResult{
		Action:     providers.PlanActionBlocked,
		ErrorClass: errorClass,
		Message:    "gitlab provider plan failed",
	}
}

func providerError(errorClass providers.ErrorClass) error {
	return &providers.Error{Class: errorClass, Message: "gitlab request failed"}
}

func setupErrorClass(err error) providers.ErrorClass {
	var providerError *providers.Error
	if errors.As(err, &providerError) && providerError.Class != "" {
		return providerError.Class
	}
	return providers.ErrorClassInternal
}

type gitlabVariable struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	EnvironmentScope string `json:"environment_scope"`
	Protected        bool   `json:"protected"`
	Masked           bool   `json:"masked"`
	VariableRaw      bool   `json:"raw"`
	VariableType     string `json:"variable_type"`
	Description      string `json:"description"`
}

type gitlabVariableInput struct {
	Key              string
	Value            string
	EnvironmentScope string
	Protected        bool
	Masked           bool
	Hidden           bool
	VariableRaw      bool
	VariableType     string
	Description      string
}

type httpProjectVariableClient struct {
	client *http.Client
}

func (c httpProjectVariableClient) GetProject(ctx context.Context, options gitlabDestinationOptions) error {
	req, err := c.newRequest(ctx, options, http.MethodGet, "/projects/"+url.PathEscape(options.projectID), nil, nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

func (c httpProjectVariableClient) GetVariable(
	ctx context.Context,
	options gitlabDestinationOptions,
	key string,
) (*gitlabVariable, error) {
	query := environmentScopeQuery(options)
	req, err := c.newRequest(ctx, options, http.MethodGet, variablePath(options.projectID, key), query, nil)
	if err != nil {
		return nil, err
	}
	var variable gitlabVariable
	if err := c.do(req, &variable); err != nil {
		return nil, err
	}
	return &variable, nil
}

func (c httpProjectVariableClient) CreateVariable(
	ctx context.Context,
	options gitlabDestinationOptions,
	input gitlabVariableInput,
) (*gitlabVariable, error) {
	req, err := c.newRequest(ctx, options, http.MethodPost, variablesPath(options.projectID), nil, input.form())
	if err != nil {
		return nil, err
	}
	var variable gitlabVariable
	if err := c.do(req, &variable); err != nil {
		return nil, err
	}
	return &variable, nil
}

func (c httpProjectVariableClient) UpdateVariable(
	ctx context.Context,
	options gitlabDestinationOptions,
	key string,
	input gitlabVariableInput,
) (*gitlabVariable, error) {
	req, err := c.newRequest(
		ctx,
		options,
		http.MethodPut,
		variablePath(options.projectID, key),
		environmentScopeQuery(options),
		input.form(),
	)
	if err != nil {
		return nil, err
	}
	var variable gitlabVariable
	if err := c.do(req, &variable); err != nil {
		return nil, err
	}
	return &variable, nil
}

func (c httpProjectVariableClient) DeleteVariable(
	ctx context.Context,
	options gitlabDestinationOptions,
	key string,
) error {
	req, err := c.newRequest(
		ctx,
		options,
		http.MethodDelete,
		variablePath(options.projectID, key),
		environmentScopeQuery(options),
		nil,
	)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

func (input gitlabVariableInput) form() url.Values {
	values := url.Values{}
	values.Set("key", input.Key)
	values.Set("value", input.Value)
	values.Set("environment_scope", input.EnvironmentScope)
	values.Set("protected", strconv.FormatBool(input.Protected))
	values.Set("masked", strconv.FormatBool(input.Masked))
	values.Set("raw", strconv.FormatBool(input.VariableRaw))
	values.Set("variable_type", input.VariableType)
	values.Set("description", input.Description)
	if input.Hidden {
		values.Set("masked_and_hidden", "true")
	}
	return values
}

func (c httpProjectVariableClient) newRequest(
	ctx context.Context,
	options gitlabDestinationOptions,
	method string,
	apiPath string,
	query url.Values,
	form url.Values,
) (*http.Request, error) {
	parsed, err := url.Parse(options.baseURL)
	if err != nil {
		return nil, validationError("gitlab base_url must be a valid URL")
	}
	unescapedAPIPath, err := url.PathUnescape(apiPath)
	if err != nil {
		return nil, validationError("gitlab api path must be valid URL path escaping")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	escapedBasePath := strings.TrimRight(parsed.EscapedPath(), "/")
	parsed.Path = basePath + "/api/v4" + unescapedAPIPath
	parsed.RawPath = escapedBasePath + "/api/v4" + apiPath
	parsed.RawQuery = query.Encode()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", options.token)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req, nil
}

func (c httpProjectVariableClient) do(req *http.Request, output interface{}) error {
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req) //nolint:gosec // GitLab base_url is operator configured and validated.
	if err != nil {
		return gitlabRequestError{err: err}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, resp.Body)
		return gitlabHTTPError{statusCode: resp.StatusCode}
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(output); err != nil {
		return gitlabRequestError{err: err}
	}
	return nil
}

func variablesPath(projectID string) string {
	return "/projects/" + url.PathEscape(projectID) + "/variables"
}

func variablePath(projectID string, key string) string {
	return variablesPath(projectID) + "/" + url.PathEscape(key)
}

func environmentScopeQuery(options gitlabDestinationOptions) url.Values {
	query := url.Values{}
	query.Set("filter[environment_scope]", options.environmentScope)
	return query
}

type gitlabHTTPError struct {
	statusCode int
}

func (e gitlabHTTPError) Error() string {
	return fmt.Sprintf("gitlab request failed with HTTP status %d", e.statusCode)
}

type gitlabRequestError struct {
	err error
}

func (e gitlabRequestError) Error() string {
	if e.err == nil {
		return "gitlab request failed"
	}
	return e.err.Error()
}

func isGitLabNotFound(err error) bool {
	var httpErr gitlabHTTPError
	return errors.As(err, &httpErr) && httpErr.statusCode == http.StatusNotFound
}

func classifyGitLabError(err error) providers.ErrorClass {
	var providerError *providers.Error
	if errors.As(err, &providerError) && providerError.Class != "" {
		return providerError.Class
	}
	var requestErr gitlabRequestError
	if errors.As(err, &requestErr) {
		return providers.ErrorClassUnavailable
	}
	var httpErr gitlabHTTPError
	if !errors.As(err, &httpErr) {
		return providers.ErrorClassInternal
	}
	switch httpErr.statusCode {
	case http.StatusTooManyRequests:
		return providers.ErrorClassRateLimit
	case http.StatusUnauthorized:
		return providers.ErrorClassAuthn
	case http.StatusForbidden:
		return providers.ErrorClassAuthz
	case http.StatusConflict:
		return providers.ErrorClassCollision
	case http.StatusRequestEntityTooLarge:
		return providers.ErrorClassCapacity
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return providers.ErrorClassValidation
	}
	if httpErr.statusCode >= http.StatusInternalServerError || httpErr.statusCode == http.StatusRequestTimeout {
		return providers.ErrorClassUnavailable
	}
	return providers.ErrorClassInternal
}
