package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/openbao/openbao/sdk/v2/framework"
	obcustommetadata "github.com/openbao/openbao/sdk/v2/helper/custommetadata"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathMetadata(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "metadata/?",
			Fields:  paginationFields(),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{
					Callback:  pathMetadataListRoot,
					Summary:   "List root metadata keys.",
					Responses: apiListResponse(),
				},
			},
			HelpSynopsis:    "List local metadata.",
			HelpDescription: "Lists local source secret metadata keys.",
		},
		{
			Pattern: "metadata/" + framework.MatchAllRegex("path") + "/?",
			Fields: withPaginationFields(map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path or metadata list prefix.",
				},
				"max_versions": {
					Type:        framework.TypeInt,
					Description: "Maximum number of local source versions to retain.",
				},
				"cas_required": {
					Type:        framework.TypeBool,
					Description: "Require options.cas on data writes for this source path.",
				},
				"delete_version_after": {
					Type:        framework.TypeString,
					Description: "Default deletion interval for versions. Stored for compatibility and future enforcement.",
				},
				"custom_metadata": {
					Type:        framework.TypeMap,
					Description: "Non-secret source metadata for operators and external tooling.",
				},
			}),
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.CreateOperation: &framework.PathOperation{
					Callback:  b.pathMetadataWrite,
					Summary:   "Create local source metadata policy.",
					Responses: apiMetadataResponse(),
				},
				logical.UpdateOperation: &framework.PathOperation{
					Callback:  b.pathMetadataWrite,
					Summary:   "Update local source metadata policy.",
					Responses: apiMetadataResponse(),
				},
				logical.ListOperation: &framework.PathOperation{
					Callback:  pathMetadataList,
					Summary:   "List metadata keys under a prefix.",
					Responses: apiListResponse(),
				},
				logical.ReadOperation: &framework.PathOperation{
					Callback:  pathMetadataRead,
					Summary:   "Read local source metadata.",
					Responses: apiMetadataResponse(),
				},
				logical.DeleteOperation: &framework.PathOperation{
					Callback:  b.pathMetadataDelete,
					Summary:   "Delete local source metadata and versions.",
					Responses: apiNoContentResponse("Source metadata and versions deleted."),
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
	data *framework.FieldData,
) (*logical.Response, error) {
	keys, err := listMetadataKeysPage(ctx, req.Storage, "", listPaginationFromFieldData(data))
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
	keys, err := listMetadataKeysPage(ctx, req.Storage, prefix, listPaginationFromFieldData(data))
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
		metadataResponseFields(*metadata, len(queuedOperations), len(statusRecords))...,
	)}, nil
}

func (b *secretSyncBackend) pathMetadataWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePath(path)
	defer unlock()

	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = newMetadataRecordPtr()
	}
	changed, err := updateMetadataFromFieldData(metadata, data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	if !changed {
		return logical.ErrorResponse("metadata write requires at least one metadata field"), nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	metadata.UpdatedTime = now
	if err := pruneExcessVersions(ctx, req.Storage, path, metadata); err != nil {
		return nil, err
	}
	if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
		return nil, err
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
		metadataResponseFields(*metadata, len(queuedOperations), len(statusRecords))...,
	)}, nil
}

func (b *secretSyncBackend) pathMetadataDelete(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	unlock := b.lockSourcePath(path)
	defer unlock()

	associations, err := listAssociationsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if len(associations) > 0 {
		return logical.ErrorResponse("source path has associations and cannot be deleted"), nil
	}
	b.enqueueMu.Lock()
	err = deleteSourcePath(ctx, req.Storage, path)
	b.enqueueMu.Unlock()
	if err != nil {
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

func updateMetadataFromFieldData(metadata *metadataRecord, data *framework.FieldData) (bool, error) {
	changed := false
	if rawMaxVersions, ok := data.GetOk("max_versions"); ok {
		maxVersions := rawMaxVersions.(int)
		if maxVersions <= 0 {
			return false, fmt.Errorf("max_versions must be greater than zero")
		}
		metadata.MaxVersions = maxVersions
		changed = true
	}
	if rawCASRequired, ok := data.GetOk("cas_required"); ok {
		metadata.CASRequired = rawCASRequired.(bool)
		changed = true
	}
	if rawDeleteAfter, ok := data.GetOk("delete_version_after"); ok {
		deleteAfter := rawDeleteAfter.(string)
		if deleteAfter == "" {
			deleteAfter = defaultDeleteVersionAfter
		}
		duration, err := time.ParseDuration(deleteAfter)
		if err != nil {
			return false, fmt.Errorf("delete_version_after must be a Go duration string: %w", err)
		}
		if duration != 0 {
			return false, fmt.Errorf("delete_version_after is not enforced yet; set it to 0s or omit it")
		}
		metadata.DeleteVersionAfter = defaultDeleteVersionAfter
		changed = true
	}
	if rawCustomMetadata, ok := data.GetOk("custom_metadata"); ok {
		customMetadata, err := parseCustomMetadata(rawCustomMetadata)
		if err != nil {
			return false, err
		}
		metadata.CustomMetadata = customMetadata
		changed = true
	}
	return changed, nil
}

//nolint:forbidigo // OpenBao framework TypeMap boundary.
func parseCustomMetadata(raw interface{}) (map[string]string, error) {
	customMetadataMap, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("custom_metadata must be a map")
	}
	customMetadata, err := obcustommetadata.Parse(customMetadataMap, false)
	if err != nil {
		return nil, fmt.Errorf("custom_metadata must contain string values: %w", err)
	}
	if err := obcustommetadata.Validate(customMetadata); err != nil {
		return nil, err
	}
	return customMetadata, nil
}

func metadataResponseFields(
	metadata metadataRecord,
	queuedOperations int,
	statusObjects int,
) []responseEntry {
	return []responseEntry{
		responseField("current_version", metadata.CurrentVersion),
		responseField("oldest_version", metadata.OldestVersion),
		responseField("max_versions", metadata.MaxVersions),
		responseField("cas_required", metadata.CASRequired),
		responseField("delete_version_after", metadata.DeleteVersionAfter),
		responseField("source_sync_enabled", metadata.SourceSyncEnabled),
		responseField("custom_metadata", metadata.CustomMetadata),
		responseField("updated_time", metadata.UpdatedTime),
		responseField("versions", metadata.Versions),
		responseField("sync", newResponseData(
			responseField("queued_operations", queuedOperations),
			responseField("objects", statusObjects),
		)),
	}
}
