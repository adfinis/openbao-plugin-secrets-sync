package backend

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/awssecretsmanager"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/kubernetessecrets"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	destinationAllowedSourcePathPrefixesField   = "allowed_source_path_prefixes"
	destinationAllowedResolvedNamePrefixesField = "allowed_resolved_name_prefixes"
)

var destinationConfigFieldKeys = []string{
	awssecretsmanager.ConfigKeyRegion,
	awssecretsmanager.ConfigKeyEndpointURL,
	awssecretsmanager.ConfigKeyEndpointPolicy,
	awssecretsmanager.ConfigKeyAuthMode,
	awssecretsmanager.ConfigKeyRoleARN,
	awssecretsmanager.ConfigKeySessionName,
	awssecretsmanager.ConfigKeyWebIdentityTokenFile,
	awssecretsmanager.ConfigKeyValueDriftDetection,
	gitlab.ConfigKeyBaseURL,
	gitlab.ConfigKeyProjectID,
	gitlab.ConfigKeyAllowInsecureHTTP,
	gitlab.ConfigKeyAllowPrivateNetwork,
	kubernetessecrets.ConfigKeyNamespace,
	kubernetessecrets.ConfigKeyKubeconfigPath,
	kubernetessecrets.ConfigKeyKubeContext,
	kubernetessecrets.ConfigKeyAPIServer,
	kubernetessecrets.ConfigKeyAllowPrivateAPIServer,
	kubernetessecrets.ConfigKeyCACertPEM,
	kubernetessecrets.ConfigKeyTLSServerName,
}

var destinationConfigFieldKeysByType = map[string][]string{
	awssecretsmanager.ProviderType: {
		awssecretsmanager.ConfigKeyRegion,
		awssecretsmanager.ConfigKeyEndpointURL,
		awssecretsmanager.ConfigKeyEndpointPolicy,
		awssecretsmanager.ConfigKeyAuthMode,
		awssecretsmanager.ConfigKeyRoleARN,
		awssecretsmanager.ConfigKeySessionName,
		awssecretsmanager.ConfigKeyWebIdentityTokenFile,
		awssecretsmanager.ConfigKeyValueDriftDetection,
	},
	gitlab.ProviderType: {
		gitlab.ConfigKeyBaseURL,
		gitlab.ConfigKeyProjectID,
		gitlab.ConfigKeyAllowInsecureHTTP,
		gitlab.ConfigKeyAllowPrivateNetwork,
	},
	kubernetessecrets.ProviderType: {
		kubernetessecrets.ConfigKeyNamespace,
		kubernetessecrets.ConfigKeyKubeconfigPath,
		kubernetessecrets.ConfigKeyKubeContext,
		kubernetessecrets.ConfigKeyAuthMode,
		kubernetessecrets.ConfigKeyAPIServer,
		kubernetessecrets.ConfigKeyAllowPrivateAPIServer,
		kubernetessecrets.ConfigKeyCACertPEM,
		kubernetessecrets.ConfigKeyTLSServerName,
	},
}

var destinationSensitiveConfigFieldKeysByType = map[string][]string{
	awssecretsmanager.ProviderType: {
		awssecretsmanager.ConfigKeyExternalID,
	},
	gitlab.ProviderType: {
		gitlab.ConfigKeyToken,
	},
	kubernetessecrets.ProviderType: {
		kubernetessecrets.ConfigKeyToken,
	},
}

var destinationSensitiveConfigFieldKeys = allDestinationSensitiveConfigFieldKeys()

func pathDestinations(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "destinations/" + framework.GenericNameRegex("type") + "/?",
			Fields: withPaginationFields(map[string]*framework.FieldSchema{
				"type": {
					Type:        framework.TypeString,
					Description: "Destination provider type.",
				},
			}),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback:  b.pathDestinationList,
					Summary:   "List configured destinations for a provider type.",
					Responses: apiListResponse(),
				},
			},
			HelpSynopsis:    "List destinations.",
			HelpDescription: "Lists configured destination names for a provider type.",
		},
		{
			Pattern: "destinations/" + framework.GenericNameRegex("type") + "/" +
				framework.GenericNameRegex("name") + "/check",
			Fields: destinationIdentityFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback:  b.pathDestinationCheck,
					Summary:   "Check destination readiness.",
					Responses: apiDestinationCheckResponse(),
				},
			},
			HelpSynopsis: "Check destination readiness.",
			HelpDescription: "Runs provider validation and, when enabled and valid, provider health checks " +
				"for a configured destination without mutating remote state.",
		},
		{
			Pattern: "destinations/" + framework.GenericNameRegex("type") + "/" +
				framework.GenericNameRegex("name") + "/validate",
			Fields: destinationIdentityFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback:  b.pathDestinationValidate,
					Summary:   "Validate a configured destination.",
					Responses: apiDestinationValidationResponse(),
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback:  b.pathDestinationValidate,
					Summary:   "Validate a configured destination.",
					Responses: apiDestinationValidationResponse(),
				},
			},
			HelpSynopsis:    "Validate a destination.",
			HelpDescription: "Runs provider validation for a configured destination without mutating remote state.",
		},
		{
			Pattern: "destinations/" + framework.GenericNameRegex("type") + "/" +
				framework.GenericNameRegex("name") + "/health",
			Fields: destinationIdentityFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback:  b.pathDestinationHealth,
					Summary:   "Check destination health.",
					Responses: apiDestinationHealthResponse(),
				},
			},
			HelpSynopsis:    "Check destination health.",
			HelpDescription: "Runs provider health checks for a configured destination without mutating remote state.",
		},
		{
			Pattern: "destinations/" + framework.GenericNameRegex("type") + "/" + framework.GenericNameRegex("name"),
			Fields:  destinationRequestFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.CreateOperation: &framework.PathOperation{
					Callback:  b.pathDestinationWrite,
					Summary:   "Create a destination.",
					Responses: apiNoContentResponse("Destination created."),
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback:  b.pathDestinationWrite,
					Summary:   "Create or update a destination.",
					Responses: apiNoContentResponse("Destination created or updated."),
				},
				logical.ReadOperation: &framework.PathOperation{
					Callback:  b.pathDestinationRead,
					Summary:   "Read a destination.",
					Responses: apiDestinationResponse(),
				},
				logical.DeleteOperation: &framework.PathOperation{
					Callback:  b.pathDestinationDelete,
					Summary:   "Delete a destination.",
					Responses: apiNoContentResponse("Destination deleted."),
				},
			},
			ExistenceCheck:  destinationExistenceCheck,
			HelpSynopsis:    "Manage destinations.",
			HelpDescription: "Stores destination configuration for supported providers.",
		},
	}
}

func destinationRequestFields() map[string]*framework.FieldSchema {
	fields := destinationIdentityFields()
	fields["description"] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Human-readable destination description.",
	}
	fields["disabled"] = &framework.FieldSchema{
		Type:        framework.TypeBool,
		Description: "Disable dispatch for associations using this destination.",
	}
	fields[destinationAllowedSourcePathPrefixesField] = &framework.FieldSchema{
		Type: framework.TypeCommaStringSlice,
		Description: "Optional comma-separated source path prefixes allowed to use this destination. " +
			"Empty allows any source path.",
	}
	fields[destinationAllowedResolvedNamePrefixesField] = &framework.FieldSchema{
		Type: framework.TypeCommaStringSlice,
		Description: "Optional comma-separated resolved remote name prefixes allowed for this destination. " +
			"Empty allows any resolved name.",
	}
	fields[awssecretsmanager.ConfigKeyRegion] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "AWS region for aws-sm destinations. If omitted, the AWS SDK may load it from the runtime environment.",
	}
	fields[awssecretsmanager.ConfigKeyEndpointURL] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Custom AWS Secrets Manager endpoint URL. Requires endpoint_policy.",
	}
	fields[awssecretsmanager.ConfigKeyEndpointPolicy] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Custom endpoint policy for aws-sm destinations: local or private.",
	}
	fields[awssecretsmanager.ConfigKeyAuthMode] = &framework.FieldSchema{
		Type: framework.TypeString,
		Description: "Provider auth mode. aws-sm: default, assume_role, or web_identity. " +
			"k8s: in_cluster, kubeconfig, or token.",
	}
	fields[awssecretsmanager.ConfigKeyRoleARN] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "IAM role ARN for aws-sm assume_role and web_identity destinations.",
	}
	fields[awssecretsmanager.ConfigKeyExternalID] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "External ID passed to STS for aws-sm assume_role destinations.",
		DisplayAttrs: &framework.DisplayAttributes{
			Sensitive: true,
		},
	}
	fields[awssecretsmanager.ConfigKeySessionName] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Optional STS session name for aws-sm assume_role and web_identity destinations.",
	}
	fields[awssecretsmanager.ConfigKeyWebIdentityTokenFile] = &framework.FieldSchema{
		Type: framework.TypeString,
		Description: "Absolute token file path for aws-sm web_identity destinations. " +
			"The file must be readable by the OpenBao plugin process.",
	}
	fields[awssecretsmanager.ConfigKeyValueDriftDetection] = &framework.FieldSchema{
		Type: framework.TypeBool,
		Description: "Opt in to AWS GetSecretValue checks for explicit plan, upsert, and read-state " +
			"value drift detection. Defaults to false.",
	}
	fields[gitlab.ConfigKeyBaseURL] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab instance base URL for gitlab destinations. Defaults to https://gitlab.com.",
	}
	fields[gitlab.ConfigKeyProjectID] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab project ID or path for gitlab project variable destinations.",
	}
	fields[gitlab.ConfigKeyAllowInsecureHTTP] = &framework.FieldSchema{
		Type: framework.TypeBool,
		Description: "Allow non-local http GitLab base URLs for local Docker or private test networks. " +
			"Defaults to false.",
	}
	fields[gitlab.ConfigKeyAllowPrivateNetwork] = &framework.FieldSchema{
		Type: framework.TypeBool,
		Description: "Allow GitLab base URLs that target localhost, private, link-local, multicast, " +
			"or unspecified networks. Defaults to false.",
	}
	fields[gitlab.ConfigKeyToken] = &framework.FieldSchema{
		Type: framework.TypeString,
		Description: "Provider API token. GitLab uses this for project variable management; " +
			"k8s uses it as a bearer token when auth_mode=token.",
		DisplayAttrs: &framework.DisplayAttributes{
			Sensitive: true,
		},
	}
	fields[kubernetessecrets.ConfigKeyNamespace] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Kubernetes namespace for k8s destinations.",
	}
	fields[kubernetessecrets.ConfigKeyKubeconfigPath] = &framework.FieldSchema{
		Type: framework.TypeString,
		Description: "Kubeconfig path for k8s destinations using auth_mode kubeconfig. " +
			"Prefer in-cluster auth for production OpenBao deployments.",
	}
	fields[kubernetessecrets.ConfigKeyKubeContext] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Optional kubeconfig context for k8s destinations using auth_mode kubeconfig.",
	}
	fields[kubernetessecrets.ConfigKeyAPIServer] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Kubernetes API server URL for k8s destinations using auth_mode token.",
	}
	fields[kubernetessecrets.ConfigKeyAllowPrivateAPIServer] = &framework.FieldSchema{
		Type: framework.TypeBool,
		Description: "Allow token auth api_server values that target localhost, private, link-local, " +
			"multicast, or unspecified networks. Defaults to false.",
	}
	fields[kubernetessecrets.ConfigKeyCACertPEM] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Optional PEM CA bundle for k8s destinations using auth_mode token.",
	}
	fields[kubernetessecrets.ConfigKeyTLSServerName] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Optional TLS server name override for k8s destinations using auth_mode token.",
	}
	return fields
}

func destinationIdentityFields() map[string]*framework.FieldSchema {
	return map[string]*framework.FieldSchema{
		"type": {
			Type:        framework.TypeString,
			Description: "Destination provider type.",
		},
		"name": {
			Type:        framework.TypeString,
			Description: "Destination name.",
		},
	}
}

func (b *secretSyncBackend) pathDestinationWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	destinationType := data.Get("type").(string)
	name := data.Get("name").(string)
	if err := b.validateDestinationType(destinationType); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if err := validateDestinationWriteFields(data); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	provider, err := b.providerRegistry.MustGet(destinationType)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockDestination(destinationRef(destinationType, name))
	defer unlock()

	now := nowUTC().Format(timeFormatRFC3339)
	record, sensitiveConfig, sensitiveCreatedTime, response, err := destinationWriteRecordFromFieldData(
		ctx,
		req.Storage,
		destinationType,
		name,
		data,
		now,
	)
	if response != nil || err != nil {
		return response, err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if response := b.validateDestinationWrite(ctx, provider, record, sensitiveConfig, cfg); response != nil {
		return response, nil
	}
	if err := storeDestinationWrite(ctx, req.Storage, record, sensitiveConfig, sensitiveCreatedTime, now); err != nil {
		return nil, err
	}
	b.invalidateDestinationRuntime(ctx, destinationRef(record.Type, record.Name))
	return nil, nil
}

func validateDestinationWriteFields(data *framework.FieldData) error {
	unsupportedFields := []string{}
	for field := range data.Raw {
		if _, ok := data.Schema[field]; !ok {
			unsupportedFields = append(unsupportedFields, field)
		}
	}
	if len(unsupportedFields) == 0 {
		return nil
	}
	sort.Strings(unsupportedFields)
	if len(unsupportedFields) == 1 {
		return fmt.Errorf("unsupported destination field %q", unsupportedFields[0])
	}
	return fmt.Errorf("unsupported destination fields %q", strings.Join(unsupportedFields, ", "))
}

func destinationWriteRecordFromFieldData(
	ctx context.Context,
	storage logical.Storage,
	destinationType string,
	name string,
	data *framework.FieldData,
	now string,
) (destinationRecord, map[string]string, string, *logical.Response, error) {
	existing, err := getDestination(ctx, storage, destinationType, name)
	if err != nil {
		return destinationRecord{}, nil, "", nil, err
	}
	var existingSensitive *destinationSensitiveRecord
	if existing != nil {
		existingSensitive, err = getDestinationSensitiveConfigForRecord(ctx, storage, *existing)
		if err != nil {
			return destinationRecord{}, nil, "", nil, err
		}
	}
	config, err := destinationConfigFromFieldData(destinationType, existing, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	sensitiveConfig, err := destinationSensitiveConfigFromFieldData(destinationType, existingSensitive, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	removeSensitiveConfigKeys(destinationType, config)
	allowedSourcePrefixes, err := destinationSourcePathPrefixesFromFieldData(existing, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	allowedNamePrefixes, err := destinationResolvedNamePrefixesFromFieldData(existing, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	description := data.Get("description").(string)
	disabled := data.Get("disabled").(bool)
	if existing != nil {
		if _, ok := data.Raw["description"]; !ok {
			description = existing.Description
		}
		if _, ok := data.Raw["disabled"]; !ok {
			disabled = existing.Disabled
		}
	}
	record := destinationRecord{
		Type:                        destinationType,
		Name:                        name,
		Description:                 description,
		Disabled:                    disabled,
		Config:                      config,
		AllowedSourcePathPrefixes:   allowedSourcePrefixes,
		AllowedResolvedNamePrefixes: allowedNamePrefixes,
		CreatedTime:                 now,
		UpdatedTime:                 now,
	}
	if existing != nil {
		record.CreatedTime = existing.CreatedTime
		record.SensitiveConfigVersion = existing.SensitiveConfigVersion
	}
	sensitiveCreatedTime := ""
	if existingSensitive != nil {
		sensitiveCreatedTime = existingSensitive.CreatedTime
	}
	return record, sensitiveConfig, sensitiveCreatedTime, nil, nil
}

func (b *secretSyncBackend) validateDestinationWrite(
	ctx context.Context,
	provider providers.Provider,
	record destinationRecord,
	sensitiveConfig map[string]string,
	cfg globalConfig,
) *logical.Response {
	if cfg.SecurityPosture == securityPostureHardened && !destinationHasDelegationConstraints(record) {
		return logical.ErrorResponse(
			"security_posture=hardened requires destination %s to set "+
				"allowed_source_path_prefixes and allowed_resolved_name_prefixes",
			destinationRef(record.Type, record.Name),
		)
	}
	resolvedConfig := destinationConfigFromParts(record, sensitiveConfig)
	providerStart := time.Now()
	validationErr := provider.ValidateConfig(ctx, resolvedConfig)
	b.recordProviderRequest(
		ctx,
		provider.Type(),
		observability.OperationValidate,
		validationErr,
		time.Since(providerStart),
	)
	if validationErr != nil {
		return logical.ErrorResponse("destination validation failed: %s", validationErr.Error())
	}
	return nil
}

func storeDestinationWrite(
	ctx context.Context,
	storage logical.Storage,
	record destinationRecord,
	sensitiveConfig map[string]string,
	sensitiveCreatedTime string,
	now string,
) error {
	previousSensitiveVersion := record.SensitiveConfigVersion
	nextSensitiveVersion := destinationSensitiveNone
	if len(sensitiveConfig) == 0 {
		record.SensitiveConfigVersion = nextSensitiveVersion
	} else {
		var err error
		nextSensitiveVersion, err = newRuntimeID("destination-sensitive")
		if err != nil {
			return err
		}
		sensitiveRecord := destinationSensitiveRecord{
			Type:        record.Type,
			Name:        record.Name,
			Config:      sensitiveConfig,
			CreatedTime: now,
			UpdatedTime: now,
		}
		if sensitiveCreatedTime != "" {
			sensitiveRecord.CreatedTime = sensitiveCreatedTime
		}
		if err := putDestinationSensitiveConfig(ctx, storage, nextSensitiveVersion, sensitiveRecord); err != nil {
			return err
		}
		record.SensitiveConfigVersion = nextSensitiveVersion
	}
	if err := putDestination(ctx, storage, record); err != nil {
		if nextSensitiveVersion != destinationSensitiveNone {
			_ = deleteDestinationSensitiveConfigVersion(
				ctx,
				storage,
				record.Type,
				record.Name,
				nextSensitiveVersion,
			)
		}
		return err
	}
	return deleteDestinationSensitiveConfigVersion(
		ctx,
		storage,
		record.Type,
		record.Name,
		previousSensitiveVersion,
	)
}

func (b *secretSyncBackend) pathDestinationValidate(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, provider, response, err := b.destinationProviderFromRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	resolvedConfig, err := destinationConfig(ctx, req.Storage, *record)
	if err != nil {
		return nil, err
	}
	providerStart := time.Now()
	providerErr := provider.ValidateConfig(ctx, resolvedConfig)
	b.recordProviderRequest(ctx, provider.Type(), observability.OperationValidate, providerErr, time.Since(providerStart))
	if providerErr != nil {
		return &logical.Response{Data: newResponseData(
			responseField("valid", false),
			responseField("destination", destinationRefResponse(*record)),
			responseField("error_class", string(providerErrorClass(providerErr))),
			responseField("error", providerErr.Error()),
		)}, nil
	}
	return &logical.Response{Data: newResponseData(
		responseField("valid", true),
		responseField("destination", destinationRefResponse(*record)),
		responseField("capabilities", capabilitiesResponse(provider.Capabilities())),
	)}, nil
}

func (b *secretSyncBackend) pathDestinationCheck(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, provider, response, err := b.destinationProviderFromRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	resolvedConfig, err := destinationConfig(ctx, req.Storage, *record)
	if err != nil {
		return nil, err
	}
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	blockers := []string{}
	if record.Disabled {
		blockers = append(blockers, "destination_disabled")
	}
	blockers = append(blockers, destinationDelegationConstraintBlockers(*record, cfg)...)
	valid := true
	validationErrorClass := ""
	validationMessage := ""
	providerStart := time.Now()
	if providerErr := provider.ValidateConfig(ctx, resolvedConfig); providerErr != nil {
		b.recordProviderRequest(
			ctx,
			provider.Type(),
			observability.OperationValidate,
			providerErr,
			time.Since(providerStart),
		)
		valid = false
		validationErrorClass = string(providerErrorClass(providerErr))
		validationMessage = providerErr.Error()
		blockers = append(blockers, "validation_failed")
	} else {
		b.recordProviderRequest(
			ctx,
			provider.Type(),
			observability.OperationValidate,
			nil,
			time.Since(providerStart),
		)
	}
	healthChecked := false
	healthy := false
	healthErrorClass := ""
	healthMessage := ""
	if valid && !record.Disabled {
		healthChecked = true
		providerStart = time.Now()
		runtime, releaseRuntime, providerErr := b.destinationRuntime(ctx, provider, *record, resolvedConfig)
		var result *providers.HealthResult
		if providerErr == nil {
			defer releaseRuntime(ctx)
			result, providerErr = runtime.Health(ctx)
		}
		b.recordProviderHealthRequest(ctx, provider.Type(), result, providerErr, time.Since(providerStart))
		if providerErr != nil {
			healthErrorClass = string(providerErrorClass(providerErr))
			healthMessage = providerErr.Error()
			blockers = append(blockers, "health_failed")
		} else {
			if result == nil {
				result = &providers.HealthResult{}
			}
			healthy = result.Healthy
			healthErrorClass = string(result.ErrorClass)
			healthMessage = result.Message
			if !result.Healthy {
				blockers = append(blockers, "health_failed")
			}
		}
	}
	b.recordReadinessCheck(ctx, observability.CheckDestination, record.Type, blockers)
	return &logical.Response{Data: newResponseData(
		responseField("ready", len(blockers) == 0),
		responseField("valid", valid),
		responseField("healthy", healthy),
		responseField("health_checked", healthChecked),
		responseField("disabled", record.Disabled),
		responseField("destination", destinationRefResponse(*record)),
		responseField("capabilities", capabilitiesResponse(provider.Capabilities())),
		responseField("blockers", blockers),
		responseField("validation_error_class", validationErrorClass),
		responseField("validation_message", validationMessage),
		responseField("health_error_class", healthErrorClass),
		responseField("health_message", healthMessage),
	)}, nil
}

func (b *secretSyncBackend) pathDestinationHealth(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, provider, response, err := b.destinationProviderFromRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	resolvedConfig, err := destinationConfig(ctx, req.Storage, *record)
	if err != nil {
		return nil, err
	}
	providerStart := time.Now()
	runtime, releaseRuntime, providerErr := b.destinationRuntime(ctx, provider, *record, resolvedConfig)
	var result *providers.HealthResult
	if providerErr == nil {
		defer releaseRuntime(ctx)
		result, providerErr = runtime.Health(ctx)
	}
	b.recordProviderHealthRequest(ctx, provider.Type(), result, providerErr, time.Since(providerStart))
	if providerErr != nil {
		return &logical.Response{Data: newResponseData(
			responseField("healthy", false),
			responseField("destination", destinationRefResponse(*record)),
			responseField("error_class", string(providerErrorClass(providerErr))),
			responseField("message", providerErr.Error()),
		)}, nil
	}
	if result == nil {
		result = &providers.HealthResult{}
	}
	return &logical.Response{Data: newResponseData(
		responseField("healthy", result.Healthy),
		responseField("destination", destinationRefResponse(*record)),
		responseField("disabled", record.Disabled),
		responseField("error_class", string(result.ErrorClass)),
		responseField("message", result.Message),
	)}, nil
}

func (b *secretSyncBackend) pathDestinationRead(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, err := getDestination(ctx, req.Storage, data.Get("type").(string), data.Get("name").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	provider, err := b.providerRegistry.MustGet(record.Type)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	sensitiveRecord, err := getDestinationSensitiveConfigForRecord(ctx, req.Storage, *record)
	if err != nil {
		return nil, err
	}
	return &logical.Response{Data: destinationResponse(*record, sensitiveRecord, provider.Capabilities())}, nil
}

func (b *secretSyncBackend) destinationProviderFromRequest(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*destinationRecord, providers.Provider, *logical.Response, error) {
	destinationType := data.Get("type").(string)
	name := data.Get("name").(string)
	if err := b.validateDestinationType(destinationType); err != nil {
		return nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	record, err := getDestination(ctx, req.Storage, destinationType, name)
	if err != nil {
		return nil, nil, nil, err
	}
	if record == nil {
		return nil, nil, logical.ErrorResponse("destination does not exist"), nil
	}
	provider, err := b.providerRegistry.MustGet(record.Type)
	if err != nil {
		return nil, nil, logical.ErrorResponse(err.Error()), nil
	}
	return record, provider, nil, nil
}

func (b *secretSyncBackend) pathDestinationList(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	destinationType := data.Get("type").(string)
	if err := b.validateDestinationType(destinationType); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	names, err := listDestinationNamesPage(ctx, req.Storage, destinationType, listPaginationFromFieldData(data))
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(names), nil
}

func (b *secretSyncBackend) pathDestinationDelete(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	destinationType := data.Get("type").(string)
	name := data.Get("name").(string)
	if err := b.validateDestinationType(destinationType); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockDestination(destinationRef(destinationType, name))
	defer unlock()
	associationIDs, err := listAssociationIDsForDestination(ctx, req.Storage, destinationType, name)
	if err != nil {
		return nil, err
	}
	if len(associationIDs) > 0 {
		return logical.ErrorResponse("destination has associations and cannot be deleted"), nil
	}
	if err := deleteDestination(ctx, req.Storage, destinationType, name); err != nil {
		return nil, err
	}
	if err := deleteDestinationSensitiveConfig(ctx, req.Storage, destinationType, name); err != nil {
		return nil, err
	}
	b.invalidateDestinationRuntime(ctx, destinationRef(destinationType, name))
	return nil, nil
}

func (b *secretSyncBackend) validateDestinationType(destinationType string) error {
	_, err := b.providerRegistry.MustGet(destinationType)
	return err
}

func destinationResponse(
	record destinationRecord,
	sensitiveRecord *destinationSensitiveRecord,
	capabilities providers.Capabilities,
) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("type", record.Type),
		responseField("name", record.Name),
		responseField("description", record.Description),
		responseField("disabled", record.Disabled),
		responseField("allowed_source_path_prefixes", responseStringSlice(record.AllowedSourcePathPrefixes)),
		responseField("allowed_resolved_name_prefixes", responseStringSlice(record.AllowedResolvedNamePrefixes)),
		responseField("config", destinationConfigResponse(record.Type, record.Config)),
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
		responseField("sensitive_config", destinationSensitiveConfigResponse(sensitiveRecord)),
		responseField("capabilities", capabilitiesResponse(capabilities)),
	)
}

func destinationRefResponse(record destinationRecord) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("type", record.Type),
		responseField("name", record.Name),
	)
}

func destinationConfig(
	ctx context.Context,
	storage logical.Storage,
	record destinationRecord,
) (providers.DestinationConfig, error) {
	config := copyStringMap(record.Config)
	sensitiveRecord, err := getDestinationSensitiveConfigForRecord(ctx, storage, record)
	if err != nil {
		return providers.DestinationConfig{}, err
	}
	if sensitiveRecord != nil {
		for key, value := range sensitiveRecord.Config {
			config[key] = value
		}
	}
	return providers.DestinationConfig{
		Name:   record.Name,
		Config: config,
	}, nil
}

func destinationConfigFromParts(
	record destinationRecord,
	sensitiveConfig map[string]string,
) providers.DestinationConfig {
	config := copyStringMap(record.Config)
	for key, value := range sensitiveConfig {
		config[key] = value
	}
	return providers.DestinationConfig{
		Name:   record.Name,
		Config: config,
	}
}

func destinationConfigFromFieldData(
	destinationType string,
	existing *destinationRecord,
	data *framework.FieldData,
) (map[string]string, error) {
	var existingConfig map[string]string
	if existing != nil {
		existingConfig = existing.Config
	}
	return providerConfigMapFromFieldData(
		destinationType,
		existingConfig,
		destinationConfigFieldKeysForType(destinationType),
		destinationConfigFieldKeys,
		data,
	)
}

func destinationSensitiveConfigFromFieldData(
	destinationType string,
	existing *destinationSensitiveRecord,
	data *framework.FieldData,
) (map[string]string, error) {
	var existingConfig map[string]string
	if existing != nil {
		existingConfig = existing.Config
	}
	return providerConfigMapFromFieldData(
		destinationType,
		existingConfig,
		destinationSensitiveConfigFieldKeysForType(destinationType),
		destinationSensitiveConfigFieldKeys,
		data,
	)
}

func providerConfigMapFromFieldData(
	destinationType string,
	existing map[string]string,
	providerKeys []string,
	fieldKeys []string,
	data *framework.FieldData,
) (map[string]string, error) {
	config := map[string]string{}
	if existing != nil {
		for _, key := range providerKeys {
			if value := strings.TrimSpace(existing[key]); value != "" {
				config[key] = value
			}
		}
	}
	allowedKeys := stringSet(providerKeys)
	for _, key := range fieldKeys {
		if _, ok := data.Raw[key]; !ok {
			continue
		}
		value := data.Get(key)
		stringValue, err := destinationConfigStringValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if _, allowed := allowedKeys[key]; !allowed {
			if stringValue == "" {
				continue
			}
			return nil, fmt.Errorf("%s is not supported for destination type %s", key, destinationType)
		}
		if stringValue == "" {
			delete(config, key)
			continue
		}
		config[key] = stringValue
	}
	return config, nil
}

func destinationConfigStringValue(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	default:
		return "", fmt.Errorf("unsupported destination config value type %T", value)
	}
}

func destinationConfigFieldKeysForType(destinationType string) []string {
	return destinationConfigFieldKeysByType[destinationType]
}

func destinationSensitiveConfigFieldKeysForType(destinationType string) []string {
	return destinationSensitiveConfigFieldKeysByType[destinationType]
}

func allDestinationSensitiveConfigFieldKeys() []string {
	keys := []string{}
	for _, providerKeys := range destinationSensitiveConfigFieldKeysByType {
		keys = append(keys, providerKeys...)
	}
	return uniqueSortedStrings(keys)
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func destinationSourcePathPrefixesFromFieldData(
	existing *destinationRecord,
	data *framework.FieldData,
) ([]string, error) {
	if prefixes, ok, err := stringSliceFromFieldData(data, destinationAllowedSourcePathPrefixesField); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return normalizeSourcePathPrefixes(prefixes)
	}
	if existing == nil {
		return nil, nil
	}
	return copyStringSlice(existing.AllowedSourcePathPrefixes), nil
}

func destinationResolvedNamePrefixesFromFieldData(
	existing *destinationRecord,
	data *framework.FieldData,
) ([]string, error) {
	if prefixes, ok, err := stringSliceFromFieldData(data, destinationAllowedResolvedNamePrefixesField); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return normalizeResolvedNamePrefixes(prefixes)
	}
	if existing == nil {
		return nil, nil
	}
	return copyStringSlice(existing.AllowedResolvedNamePrefixes), nil
}

func stringSliceFromFieldData(data *framework.FieldData, key string) ([]string, bool, error) {
	value, ok := data.GetOk(key)
	if !ok {
		return nil, false, nil
	}
	switch typed := value.(type) {
	case []string:
		return typed, true, nil
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			stringValue, ok := item.(string)
			if !ok {
				return nil, true, fmt.Errorf("%s must contain only strings", key)
			}
			values = append(values, stringValue)
		}
		return values, true, nil
	case string:
		return strings.Split(typed, ","), true, nil
	default:
		return nil, true, fmt.Errorf("%s must be a string list", key)
	}
}

func normalizeSourcePathPrefixes(prefixes []string) ([]string, error) {
	normalized := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		path, err := normalizeSourcePath(prefix)
		if err != nil {
			return nil, fmt.Errorf("%s contains invalid prefix %q: %w", destinationAllowedSourcePathPrefixesField, prefix, err)
		}
		normalized = append(normalized, path)
	}
	return uniqueSortedStrings(normalized), nil
}

func normalizeResolvedNamePrefixes(prefixes []string) ([]string, error) {
	normalized := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimLeft(strings.TrimSpace(prefix), "/")
		if prefix == "" {
			continue
		}
		normalized = append(normalized, prefix)
	}
	return uniqueSortedStrings(normalized), nil
}

func removeSensitiveConfigKeys(destinationType string, config map[string]string) {
	for _, key := range destinationSensitiveConfigFieldKeysForType(destinationType) {
		delete(config, key)
	}
}

func destinationConfigResponse(
	destinationType string,
	config map[string]string,
) map[string]interface{} { //nolint:forbidigo
	publicConfig := copyStringMap(config)
	removeSensitiveConfigKeys(destinationType, publicConfig)
	response := make(map[string]interface{}, len(publicConfig)) //nolint:forbidigo
	for key, value := range publicConfig {
		response[key] = value
	}
	return response
}

func destinationSensitiveConfigResponse(
	sensitiveRecord *destinationSensitiveRecord,
) map[string]interface{} { //nolint:forbidigo
	keys := []string{}
	if sensitiveRecord != nil {
		keys = sortedStringMapKeys(sensitiveRecord.Config)
	}
	return newResponseData(
		responseField("redacted", true),
		responseField("configured", len(keys) > 0),
		responseField("keys", keys),
	)
}

func sortedStringMapKeys(input map[string]string) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func copyStringSlice(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	output := make([]string, len(input))
	copy(output, input)
	return output
}

func responseStringSlice(input []string) []string {
	if len(input) == 0 {
		return []string{}
	}
	return copyStringSlice(input)
}

func uniqueSortedStrings(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	sort.Strings(input)
	output := input[:0]
	var last string
	for index, value := range input {
		if index > 0 && value == last {
			continue
		}
		output = append(output, value)
		last = value
	}
	return output
}

func capabilitiesResponse(capabilities providers.Capabilities) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("supports_value_readback", capabilities.SupportsValueReadback),
		responseField("supports_metadata_readback", capabilities.SupportsMetadataReadback),
		responseField("supports_payload_hash_metadata", capabilities.SupportsPayloadHashMetadata),
		responseField("supports_update_if_owned", capabilities.SupportsUpdateIfOwned),
		responseField("supports_delete_if_owned", capabilities.SupportsDeleteIfOwned),
		responseField("supports_secret_path", capabilities.SupportsSecretPath),
		responseField("supports_secret_key", capabilities.SupportsSecretKey),
		responseField("supports_data_map", capabilities.SupportsDataMap),
		responseField("max_payload_bytes", capabilities.MaxPayloadBytes),
	)
}
