package backend

import (
	"context"

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
		HelpDescription: "Returns scaffolded status until per-object status records are implemented.",
	}
}

func pathStatusRead(_ context.Context, _ *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	return &logical.Response{Data: newResponseData(
		responseField("path", data.Get("path").(string)),
		responseField("state", "UNKNOWN"),
		responseField("objects", []string{}),
	)}, nil
}
