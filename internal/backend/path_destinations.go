package backend

import (
	"context"
	"fmt"
	"sort"
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
	gitlab.ConfigKeyBaseURL,
	gitlab.ConfigKeyProjectID,
	gitlab.ConfigKeyEnvironmentScope,
	gitlab.ConfigKeyProtected,
	gitlab.ConfigKeyMasked,
	gitlab.ConfigKeyHidden,
	gitlab.ConfigKeyVariableRaw,
	gitlab.ConfigKeyVariableType,
	gitlab.ConfigKeyAllowInsecureHTTP,
	kubernetessecrets.ConfigKeyNamespace,
	kubernetessecrets.ConfigKeyKubeconfigPath,
	kubernetessecrets.ConfigKeyKubeContext,
}

var destinationSensitiveConfigFieldKeys = []string{
	awssecretsmanager.ConfigKeyExternalID,
	awssecretsmanager.ConfigKeyAccessKeyID,
	awssecretsmanager.ConfigKeySecretAccessKey,
	awssecretsmanager.ConfigKeySessionToken,
	gitlab.ConfigKeyToken,
}

var destinationConfigFieldKeysByType = map[string][]string{
	awssecretsmanager.ProviderType: {
		awssecretsmanager.ConfigKeyRegion,
		awssecretsmanager.ConfigKeyEndpointURL,
		awssecretsmanager.ConfigKeyEndpointPolicy,
		awssecretsmanager.ConfigKeyAuthMode,
		awssecretsmanager.ConfigKeyRoleARN,
		awssecretsmanager.ConfigKeySessionName,
	},
	gitlab.ProviderType: {
		gitlab.ConfigKeyBaseURL,
		gitlab.ConfigKeyProjectID,
		gitlab.ConfigKeyEnvironmentScope,
		gitlab.ConfigKeyProtected,
		gitlab.ConfigKeyMasked,
		gitlab.ConfigKeyHidden,
		gitlab.ConfigKeyVariableRaw,
		gitlab.ConfigKeyVariableType,
		gitlab.ConfigKeyAllowInsecureHTTP,
	},
	kubernetessecrets.ProviderType: {
		kubernetessecrets.ConfigKeyNamespace,
		kubernetessecrets.ConfigKeyKubeconfigPath,
		kubernetessecrets.ConfigKeyKubeContext,
		kubernetessecrets.ConfigKeyAuthMode,
	},
}

var destinationSensitiveConfigFieldKeysByType = map[string][]string{
	awssecretsmanager.ProviderType: {
		awssecretsmanager.ConfigKeyExternalID,
		awssecretsmanager.ConfigKeyAccessKeyID,
		awssecretsmanager.ConfigKeySecretAccessKey,
		awssecretsmanager.ConfigKeySessionToken,
	},
	gitlab.ProviderType: {
		gitlab.ConfigKeyToken,
	},
}

func pathDestinations(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "destinations/" + framework.GenericNameRegex("type") + "/?",
			Fields: map[string]*framework.FieldSchema{
				"type": {
					Type:        framework.TypeString,
					Description: "Destination provider type.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback: b.pathDestinationList,
					Summary:  "List configured destinations for a provider type.",
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
					Callback: b.pathDestinationCheck,
					Summary:  "Check destination readiness.",
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
					Callback: b.pathDestinationValidate,
					Summary:  "Validate a configured destination.",
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathDestinationValidate,
					Summary:  "Validate a configured destination.",
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
					Callback: b.pathDestinationHealth,
					Summary:  "Check destination health.",
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
					Callback: b.pathDestinationWrite,
					Summary:  "Create a destination.",
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathDestinationWrite,
					Summary:  "Create or update a destination.",
				},
				logical.ReadOperation: &framework.PathOperation{
					Callback: b.pathDestinationRead,
					Summary:  "Read a destination.",
				},
				logical.DeleteOperation: &framework.PathOperation{
					Callback: b.pathDestinationDelete,
					Summary:  "Delete a destination.",
				},
			},
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
		Type:        framework.TypeString,
		Description: "Provider auth mode. aws-sm: default, assume_role, or reserved static. k8s: in_cluster or kubeconfig.",
	}
	fields[awssecretsmanager.ConfigKeyRoleARN] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "IAM role ARN for aws-sm assume_role destinations.",
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
		Description: "Optional STS session name for aws-sm assume_role destinations.",
	}
	fields[awssecretsmanager.ConfigKeyAccessKeyID] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Static AWS access key ID. Static auth is intentionally unsupported until a later slice.",
		DisplayAttrs: &framework.DisplayAttributes{
			Sensitive: true,
		},
	}
	fields[awssecretsmanager.ConfigKeySecretAccessKey] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Static AWS secret access key. Static auth is intentionally unsupported until a later slice.",
		DisplayAttrs: &framework.DisplayAttributes{
			Sensitive: true,
		},
	}
	fields[awssecretsmanager.ConfigKeySessionToken] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Static AWS session token. Static auth is intentionally unsupported until a later slice.",
		DisplayAttrs: &framework.DisplayAttributes{
			Sensitive: true,
		},
	}
	fields[gitlab.ConfigKeyBaseURL] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab instance base URL for gitlab destinations. Defaults to https://gitlab.com.",
	}
	fields[gitlab.ConfigKeyProjectID] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab project ID or path for gitlab project variable destinations.",
	}
	fields[gitlab.ConfigKeyEnvironmentScope] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab variable environment scope. Defaults to *.",
	}
	fields[gitlab.ConfigKeyProtected] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab protected variable flag: true or false.",
	}
	fields[gitlab.ConfigKeyMasked] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab masked variable flag: true or false.",
	}
	fields[gitlab.ConfigKeyHidden] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab hidden variable flag: true or false. Hidden variables are sent as masked_and_hidden.",
	}
	fields[gitlab.ConfigKeyVariableRaw] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab raw variable flag controlling variable reference expansion: true or false.",
	}
	fields[gitlab.ConfigKeyVariableType] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab variable type: env_var or file.",
	}
	fields[gitlab.ConfigKeyAllowInsecureHTTP] = &framework.FieldSchema{
		Type: framework.TypeString,
		Description: "Allow non-local http GitLab base URLs for local Docker or private test networks. " +
			"Defaults to false.",
	}
	fields[gitlab.ConfigKeyToken] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "GitLab API token for project variable management.",
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
	if response := b.validateDestinationWrite(ctx, provider, record, sensitiveConfig); response != nil {
		return response, nil
	}
	if err := storeDestinationWrite(ctx, req.Storage, record, sensitiveConfig, sensitiveCreatedTime, now); err != nil {
		return nil, err
	}
	return nil, nil
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
	existingSensitive, err := getDestinationSensitiveConfig(ctx, storage, destinationType, name)
	if err != nil {
		return destinationRecord{}, nil, "", nil, err
	}
	config, err := destinationConfigFromFieldData(destinationType, existing, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	sensitiveConfig, err := destinationSensitiveConfigFromFieldData(destinationType, existingSensitive, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	migrateSensitiveConfigFromDestination(existing, data, sensitiveConfig)
	removeSensitiveConfigKeys(config)
	allowedSourcePrefixes, err := destinationSourcePathPrefixesFromFieldData(existing, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	allowedNamePrefixes, err := destinationResolvedNamePrefixesFromFieldData(existing, data)
	if err != nil {
		return destinationRecord{}, nil, "", logical.ErrorResponse(err.Error()), nil
	}
	record := destinationRecord{
		Type:                        destinationType,
		Name:                        name,
		Description:                 data.Get("description").(string),
		Disabled:                    data.Get("disabled").(bool),
		Config:                      config,
		AllowedSourcePathPrefixes:   allowedSourcePrefixes,
		AllowedResolvedNamePrefixes: allowedNamePrefixes,
		CreatedTime:                 now,
		UpdatedTime:                 now,
	}
	if existing != nil {
		record.CreatedTime = existing.CreatedTime
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
) *logical.Response {
	resolvedConfig := destinationConfigFromParts(record, sensitiveConfig)
	providerStart := time.Now()
	validationErr := provider.Validate(ctx, resolvedConfig)
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
	if err := putDestination(ctx, storage, record); err != nil {
		return err
	}
	if len(sensitiveConfig) == 0 {
		if err := deleteDestinationSensitiveConfig(ctx, storage, record.Type, record.Name); err != nil {
			return err
		}
		return nil
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
	if err := putDestinationSensitiveConfig(ctx, storage, sensitiveRecord); err != nil {
		return err
	}
	return nil
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
	providerErr := provider.Validate(ctx, resolvedConfig)
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
	blockers := []string{}
	if record.Disabled {
		blockers = append(blockers, "destination_disabled")
	}
	valid := true
	validationErrorClass := ""
	validationMessage := ""
	providerStart := time.Now()
	if providerErr := provider.Validate(ctx, resolvedConfig); providerErr != nil {
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
		result, providerErr := provider.Health(ctx, resolvedConfig)
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
	result, providerErr := provider.Health(ctx, resolvedConfig)
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
	sensitiveRecord, err := getDestinationSensitiveConfig(ctx, req.Storage, record.Type, record.Name)
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
	names, err := listDestinationNames(ctx, req.Storage, destinationType)
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
		responseField("allowed_source_path_prefixes", copyStringSlice(record.AllowedSourcePathPrefixes)),
		responseField("allowed_resolved_name_prefixes", copyStringSlice(record.AllowedResolvedNamePrefixes)),
		responseField("config", destinationConfigResponse(record.Config)),
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
	sensitiveRecord, err := getDestinationSensitiveConfig(ctx, storage, record.Type, record.Name)
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
	return destinationConfigMapFromFieldData(
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
	return destinationConfigMapFromFieldData(
		destinationType,
		existingConfig,
		destinationSensitiveConfigFieldKeysForType(destinationType),
		destinationSensitiveConfigFieldKeys,
		data,
	)
}

func destinationConfigMapFromFieldData(
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
		value, ok := data.GetOk(key)
		if !ok {
			continue
		}
		stringValue := strings.TrimSpace(value.(string))
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

func destinationConfigFieldKeysForType(destinationType string) []string {
	return destinationConfigFieldKeysByType[destinationType]
}

func destinationSensitiveConfigFieldKeysForType(destinationType string) []string {
	return destinationSensitiveConfigFieldKeysByType[destinationType]
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

func removeSensitiveConfigKeys(config map[string]string) {
	for _, key := range destinationSensitiveConfigFieldKeys {
		delete(config, key)
	}
}

func migrateSensitiveConfigFromDestination(
	existing *destinationRecord,
	data *framework.FieldData,
	sensitiveConfig map[string]string,
) {
	if existing == nil {
		return
	}
	for _, key := range destinationSensitiveConfigFieldKeys {
		if _, ok := data.GetOk(key); ok {
			continue
		}
		value := strings.TrimSpace(existing.Config[key])
		if value == "" {
			continue
		}
		if _, ok := sensitiveConfig[key]; !ok {
			sensitiveConfig[key] = value
		}
	}
}

func destinationConfigResponse(config map[string]string) map[string]interface{} { //nolint:forbidigo
	publicConfig := copyStringMap(config)
	removeSensitiveConfigKeys(publicConfig)
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
		responseField("max_payload_bytes", capabilities.MaxPayloadBytes),
	)
}
