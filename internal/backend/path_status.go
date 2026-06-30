package backend

import (
	"context"

	"github.com/adfinis/openbao-secret-sync/internal/domain"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathStatus(_ *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: "status/" + framework.MatchAllRegex("path"),
		Fields: map[string]*framework.FieldSchema{
			"path": {
				Type:        framework.TypeString,
				Description: "Source secret path.",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback: pathStatusRead,
				Summary:  "Read per-path sync status.",
			},
		},
		HelpSynopsis:    "Inspect source path sync status.",
		HelpDescription: "Returns current source version and pending sync operation identifiers.",
	}
}

func pathStatusRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return &logical.Response{Data: newResponseData(
			responseField("path", path),
			responseField("state", string(domain.SyncStateUnknown)),
			responseField("operation_ids", []string{}),
			responseField("objects", []string{}),
		)}, nil
	}

	operationIDs, err := listOutboxIDsForPath(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	state := domain.SyncStateUnknown
	if len(operationIDs) > 0 {
		state = domain.SyncStatePending
	}
	return &logical.Response{Data: newResponseData(
		responseField("path", path),
		responseField("version", metadata.CurrentVersion),
		responseField("state", string(state)),
		responseField("operation_ids", operationIDs),
		responseField("objects", []string{}),
	)}, nil
}
