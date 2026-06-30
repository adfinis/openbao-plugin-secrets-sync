package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/adfinis/openbao-secret-sync/internal/providers/fake"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathAssociations(_ *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "associations/?",
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback: pathAssociationList,
					Summary:  "List association source path prefixes.",
				},
			},
			HelpSynopsis:    "List associations.",
			HelpDescription: "Lists configured association source path prefixes.",
		},
		{
			Pattern: "associations/(?P<path>.+)/(?P<association_id>assoc-[0-9a-f]+)",
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path.",
				},
				"association_id": {
					Type:        framework.TypeString,
					Description: "Association identifier.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.DeleteOperation: &framework.PathOperation{
					Callback: pathAssociationDelete,
					Summary:  "Delete an association.",
				},
			},
			HelpSynopsis:    "Delete associations.",
			HelpDescription: "Deletes one source-to-destination association.",
		},
		{
			Pattern: "associations/" + framework.MatchAllRegex("path"),
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path.",
				},
				"destination_type": {
					Type:        framework.TypeString,
					Description: "Destination provider type.",
				},
				"destination_name": {
					Type:        framework.TypeString,
					Description: "Destination name.",
				},
				"name_template": {
					Type:        framework.TypeString,
					Description: "Destination object name template.",
				},
				"resolved_name": {
					Type:        framework.TypeString,
					Description: "Explicit resolved destination object name.",
				},
				"granularity": {
					Type:        framework.TypeString,
					Description: "Sync granularity. This phase supports secret-path.",
				},
				"format": {
					Type:        framework.TypeString,
					Description: "Payload format. This phase supports json.",
				},
				"enabled": {
					Type:        framework.TypeBool,
					Description: "Whether the association should enqueue sync work.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.CreateOperation: &framework.PathOperation{
					Callback: pathAssociationWrite,
					Summary:  "Create an association.",
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathAssociationWrite,
					Summary:  "Create or update an association.",
				},
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathAssociationRead,
					Summary:  "Read associations for a source path.",
				},
			},
			HelpSynopsis:    "Manage associations.",
			HelpDescription: "Associates source secrets with fake destinations for asynchronous sync.",
		},
	}
}

func pathAssociationWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return logical.ErrorResponse("source path does not exist"), nil
	}

	record, err := associationRecordFromFieldData(ctx, req.Storage, path, data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	existing, err := getAssociation(ctx, req.Storage, path, record.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		record.CreatedTime = existing.CreatedTime
	}

	shouldEnqueue := record.Enabled && existing == nil
	if shouldEnqueue {
		if err := ensureQueueCapacityFor(ctx, req.Storage, 1); err != nil {
			return logical.ErrorResponse(err.Error()), nil
		}
	}
	if err := putAssociation(ctx, req.Storage, record); err != nil {
		return nil, err
	}

	operationIDs := []string{}
	if shouldEnqueue {
		operation := newAssociationOutboxRecord(record, metadata.CurrentVersion, nowUTC().Format(timeFormatRFC3339))
		if err := putOutbox(ctx, req.Storage, operation); err != nil {
			return nil, err
		}
		operationIDs = append(operationIDs, operation.ID)
	}

	return &logical.Response{Data: newResponseData(
		responseField("association", associationResponse(record)),
		responseField("sync_operation_ids", operationIDs),
	)}, nil
}

func pathAssociationRead(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	records, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	associations := make([]map[string]interface{}, 0, len(records)) //nolint:forbidigo
	for _, record := range records {
		associations = append(associations, associationResponse(record))
	}
	return &logical.Response{Data: newResponseData(
		responseField("path", path),
		responseField("associations", associations),
	)}, nil
}

func pathAssociationList(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	keys, err := req.Storage.List(ctx, associationStoragePrefix)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(keys), nil
}

func pathAssociationDelete(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	record, err := getAssociation(ctx, req.Storage, path, data.Get("association_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	if err := deleteQueuedOutboxForAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	if err := deleteAssociation(ctx, req.Storage, *record); err != nil {
		return nil, err
	}
	return nil, nil
}

func associationRecordFromFieldData(
	ctx context.Context,
	storage logical.Storage,
	path string,
	data *framework.FieldData,
) (associationRecord, error) {
	destinationType := data.Get("destination_type").(string)
	destinationName := data.Get("destination_name").(string)
	if err := validateDestinationType(destinationType); err != nil {
		return associationRecord{}, err
	}
	destination, err := getDestination(ctx, storage, destinationType, destinationName)
	if err != nil {
		return associationRecord{}, err
	}
	if destination == nil {
		return associationRecord{}, fmt.Errorf("destination %s/%s does not exist", destinationType, destinationName)
	}
	if destination.Disabled {
		return associationRecord{}, fmt.Errorf("destination %s/%s is disabled", destinationType, destinationName)
	}

	granularity := stringFromField(data, "granularity", syncObjectIDSecretPath)
	format := stringFromField(data, "format", defaultAssociationFormat)
	nameTemplate := stringFromField(data, "name_template", defaultNameTemplate)
	resolvedName := stringFromField(data, "resolved_name", "")
	if resolvedName == "" {
		renderedName, err := renderAssociationName(nameTemplate, path, destinationType, destinationName)
		if err != nil {
			return associationRecord{}, err
		}
		resolvedName = renderedName
	}
	resolvedName = strings.Trim(resolvedName, "/")
	if resolvedName == "" {
		return associationRecord{}, fmt.Errorf("resolved_name must not be empty")
	}
	if err := validateAssociationCapabilities(granularity, format); err != nil {
		return associationRecord{}, err
	}

	id := newAssociationID(path, destinationType, destinationName, resolvedName, granularity)
	destinationReference := destinationRef(destinationType, destinationName)
	reservations, err := listAssociationNameReservationIDs(ctx, storage, destinationReference, resolvedName)
	if err != nil {
		return associationRecord{}, err
	}
	if len(reservations) > 0 && (len(reservations) != 1 || reservations[0] != id) {
		return associationRecord{}, fmt.Errorf(
			"resolved_name %q is already reserved for destination %s",
			resolvedName,
			destinationReference,
		)
	}

	enabled := true
	if value, ok := data.GetOk("enabled"); ok {
		enabled = value.(bool)
	}
	now := nowUTC().Format(timeFormatRFC3339)
	return associationRecord{
		ID:              id,
		Path:            path,
		DestinationType: destinationType,
		DestinationName: destinationName,
		DestinationRef:  destinationReference,
		NameTemplate:    nameTemplate,
		ResolvedName:    resolvedName,
		Granularity:     granularity,
		Format:          format,
		Enabled:         enabled,
		CreatedTime:     now,
		UpdatedTime:     now,
	}, nil
}

func validateAssociationCapabilities(granularity string, format string) error {
	if granularity != syncObjectIDSecretPath {
		return fmt.Errorf("unsupported granularity %q", granularity)
	}
	if format != defaultAssociationFormat {
		return fmt.Errorf("unsupported format %q", format)
	}
	capabilities := fake.Provider{}.Capabilities()
	if !capabilities.SupportsSecretPath {
		return fmt.Errorf("fake provider does not support secret-path granularity")
	}
	return nil
}

func renderAssociationName(
	template string,
	path string,
	destinationType string,
	destinationName string,
) (string, error) {
	rendered := strings.NewReplacer(
		"{{ path }}", path,
		"{{ destination.type }}", destinationType,
		"{{ destination.name }}", destinationName,
	).Replace(template)
	if strings.Contains(rendered, "{{") || strings.Contains(rendered, "}}") {
		return "", fmt.Errorf("unsupported name_template %q", template)
	}
	return rendered, nil
}

func stringFromField(data *framework.FieldData, key string, fallback string) string {
	value := strings.TrimSpace(data.Get(key).(string))
	if value == "" {
		return fallback
	}
	return value
}

func associationResponse(record associationRecord) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("id", record.ID),
		responseField("path", record.Path),
		responseField("destination", newResponseData(
			responseField("type", record.DestinationType),
			responseField("name", record.DestinationName),
		)),
		responseField("destination_ref", record.DestinationRef),
		responseField("name_template", record.NameTemplate),
		responseField("resolved_name", record.ResolvedName),
		responseField("granularity", record.Granularity),
		responseField("format", record.Format),
		responseField("enabled", record.Enabled),
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
	)
}
