package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathSources(_ *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "sources/(?P<path>.+)/enable",
			Fields: map[string]*framework.FieldSchema{
				"path": {
					Type:        framework.TypeString,
					Description: "Source secret path.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathSourceEnable,
					Summary:  "Mark a source path as syncable.",
				},
			},
			HelpSynopsis:    "Enable source sync eligibility.",
			HelpDescription: "Marks a source path as explicitly syncable without requiring a custom_metadata JSON write.",
		},
	}
}

func pathSourceEnable(
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
	if metadata == nil {
		metadata = newMetadataRecordPtr()
	}
	changed := metadata.CustomMetadata[sourceMetadataKeySyncable] != sourceMetadataValueTrue
	if changed {
		metadata.CustomMetadata[sourceMetadataKeySyncable] = sourceMetadataValueTrue
		metadata.UpdatedTime = nowUTC().Format(timeFormatRFC3339)
		if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
			return nil, err
		}
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
		responseField("path", path),
		responseField("syncable", true),
		responseField("changed", changed),
		responseField("metadata", newResponseData(
			metadataResponseFields(*metadata, len(queuedOperations), len(statusRecords))...,
		)),
	)}, nil
}
