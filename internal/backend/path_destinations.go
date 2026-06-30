package backend

import (
	"context"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/adfinis/openbao-secret-sync/internal/providers/awssecretsmanager"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

var destinationConfigFieldKeys = []string{
	awssecretsmanager.ConfigKeyRegion,
	awssecretsmanager.ConfigKeyEndpointURL,
	awssecretsmanager.ConfigKeyAuthMode,
	awssecretsmanager.ConfigKeyRoleARN,
	awssecretsmanager.ConfigKeyExternalID,
	awssecretsmanager.ConfigKeySessionName,
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
				framework.GenericNameRegex("name") + "/validate",
			Fields: destinationIdentityFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
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
	fields[awssecretsmanager.ConfigKeyRegion] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "AWS region for aws-sm destinations. If omitted, the AWS SDK may load it from the runtime environment.",
	}
	fields[awssecretsmanager.ConfigKeyEndpointURL] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Custom AWS Secrets Manager endpoint URL, primarily for localstack.",
	}
	fields[awssecretsmanager.ConfigKeyAuthMode] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "AWS auth mode for aws-sm destinations: default or assume_role.",
	}
	fields[awssecretsmanager.ConfigKeyRoleARN] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "IAM role ARN for aws-sm assume_role destinations.",
	}
	fields[awssecretsmanager.ConfigKeyExternalID] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "External ID passed to STS for aws-sm assume_role destinations.",
	}
	fields[awssecretsmanager.ConfigKeySessionName] = &framework.FieldSchema{
		Type:        framework.TypeString,
		Description: "Optional STS session name for aws-sm assume_role destinations.",
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

	now := nowUTC().Format(timeFormatRFC3339)
	existing, err := getDestination(ctx, req.Storage, destinationType, name)
	if err != nil {
		return nil, err
	}
	record := destinationRecord{
		Type:        destinationType,
		Name:        name,
		Description: data.Get("description").(string),
		Disabled:    data.Get("disabled").(bool),
		Config:      destinationConfigFromFieldData(existing, data),
		CreatedTime: now,
		UpdatedTime: now,
	}
	if existing != nil {
		record.CreatedTime = existing.CreatedTime
	}
	if err := putDestination(ctx, req.Storage, record); err != nil {
		return nil, err
	}
	return nil, nil
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
	providerErr := provider.Validate(ctx, destinationConfig(*record))
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

func (b *secretSyncBackend) pathDestinationHealth(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, provider, response, err := b.destinationProviderFromRequest(ctx, req, data)
	if response != nil || err != nil {
		return response, err
	}
	result, providerErr := provider.Health(ctx, destinationConfig(*record))
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
	return &logical.Response{Data: destinationResponse(*record, provider.Capabilities())}, nil
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
	return nil, nil
}

func (b *secretSyncBackend) validateDestinationType(destinationType string) error {
	_, err := b.providerRegistry.MustGet(destinationType)
	return err
}

func destinationResponse(
	record destinationRecord,
	capabilities providers.Capabilities,
) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("type", record.Type),
		responseField("name", record.Name),
		responseField("description", record.Description),
		responseField("disabled", record.Disabled),
		responseField("config", destinationConfigResponse(record.Config)),
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
		responseField("sensitive_config", newResponseData(
			responseField("redacted", true),
		)),
		responseField("capabilities", capabilitiesResponse(capabilities)),
	)
}

func destinationRefResponse(record destinationRecord) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("type", record.Type),
		responseField("name", record.Name),
	)
}

func destinationConfig(record destinationRecord) providers.DestinationConfig {
	return providers.DestinationConfig{
		Name:   record.Name,
		Config: copyStringMap(record.Config),
	}
}

func destinationConfigFromFieldData(
	existing *destinationRecord,
	data *framework.FieldData,
) map[string]string {
	config := map[string]string{}
	if existing != nil {
		config = copyStringMap(existing.Config)
	}
	for _, key := range destinationConfigFieldKeys {
		value, ok := data.GetOk(key)
		if !ok {
			continue
		}
		stringValue := strings.TrimSpace(value.(string))
		if stringValue == "" {
			delete(config, key)
			continue
		}
		config[key] = stringValue
	}
	return config
}

func destinationConfigResponse(config map[string]string) map[string]interface{} { //nolint:forbidigo
	response := make(map[string]interface{}, len(config)) //nolint:forbidigo
	for key, value := range config {
		response[key] = value
	}
	return response
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
