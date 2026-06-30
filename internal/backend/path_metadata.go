package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathMetadata(_ *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "metadata/?",
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback: pathMetadataListRoot,
					Summary:  "List root metadata keys.",
				},
			},
			HelpSynopsis:    "List local metadata.",
			HelpDescription: "Lists local source secret metadata keys.",
		},
		{
			Pattern: "metadata/" + framework.MatchAllRegex("path") + "/?",
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path or metadata list prefix.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback: pathMetadataList,
					Summary:  "List metadata keys under a prefix.",
				},
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathMetadataRead,
					Summary:  "Read local source metadata.",
				},
				logical.DeleteOperation: &framework.PathOperation{
					Callback: pathMetadataDelete,
					Summary:  "Delete local source metadata and versions.",
				},
			},
			HelpSynopsis:    "Manage local metadata.",
			HelpDescription: "Reads, lists, and deletes local source secret metadata.",
		},
	}
}

func pathMetadataListRoot(
	ctx context.Context,
	req *logical.Request,
	_ *framework.FieldData,
) (*logical.Response, error) {
	keys, err := listMetadataKeys(ctx, req.Storage, "")
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(keys), nil
}

func pathMetadataList(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	prefix, err := normalizeOptionalPath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	keys, err := listMetadataKeys(ctx, req.Storage, prefix)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(keys), nil
}

func pathMetadataRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, nil
	}
	queuedOperations, err := listQueuedOutboxIDsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	statusRecords, err := listStatusRecordsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	return &logical.Response{Data: newResponseData(
		responseField("current_version", metadata.CurrentVersion),
		responseField("oldest_version", metadata.OldestVersion),
		responseField("max_versions", metadata.MaxVersions),
		responseField("cas_required", metadata.CASRequired),
		responseField("updated_time", metadata.UpdatedTime),
		responseField("versions", metadata.Versions),
		responseField("sync", newResponseData(
			responseField("queued_operations", len(queuedOperations)),
			responseField("objects", len(statusRecords)),
		)),
	)}, nil
}

func pathMetadataDelete(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	associations, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if len(associations) > 0 {
		return logical.ErrorResponse("source path has associations and cannot be deleted"), nil
	}
	if err := deleteSourcePath(ctx, req.Storage, path); err != nil {
		return nil, err
	}
	return nil, nil
}

func normalizeOptionalPath(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	return normalizeSourcePath(input)
}
