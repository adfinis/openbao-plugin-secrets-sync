package backend

import (
	"context"

	"github.com/adfinis/openbao-secret-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

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
			Pattern: "destinations/" + framework.GenericNameRegex("type") + "/" + framework.GenericNameRegex("name"),
			Fields: map[string]*framework.FieldSchema{
				"type": {
					Type:        framework.TypeString,
					Description: "Destination provider type.",
				},
				"name": {
					Type:        framework.TypeString,
					Description: "Destination name.",
				},
				"description": {
					Type:        framework.TypeString,
					Description: "Human-readable destination description.",
				},
				"disabled": {
					Type:        framework.TypeBool,
					Description: "Disable dispatch for associations using this destination.",
				},
			},
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
			HelpDescription: "Stores destination configuration. This phase supports the fake provider.",
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
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
		responseField("sensitive_config", newResponseData(
			responseField("redacted", true),
		)),
		responseField("capabilities", capabilitiesResponse(capabilities)),
	)
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
