package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathData(_ *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: "data/" + framework.MatchAllRegex("path"),
		Fields: map[string]*framework.FieldSchema{
			"path": {
				Type:        framework.TypeString,
				Description: "Source secret path.",
			},
			"data": {
				Type:        framework.TypeMap,
				Description: "Source secret payload.",
			},
			"options": {
				Type:        framework.TypeMap,
				Description: "Write options such as CAS.",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.CreateOperation: &framework.PathOperation{
				Callback: pathDataWrite,
				Summary:  "Write a new local source secret version.",
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: pathDataWrite,
				Summary:  "Write a new local source secret version.",
			},
			logical.ReadOperation: &framework.PathOperation{
				Callback: pathDataRead,
				Summary:  "Read a local source secret version.",
			},
			logical.DeleteOperation: &framework.PathOperation{
				Callback: pathDataDelete,
				Summary:  "Soft-delete the latest local source secret version.",
			},
		},
		HelpSynopsis:    "Manage local source secret data.",
		HelpDescription: "This scaffold exposes the data path before the KV and outbox implementation is added.",
	}
}

func pathDataWrite(_ context.Context, _ *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	return logical.ErrorResponse("data write is scaffolded; KV and outbox implementation pending"), nil
}

func pathDataRead(_ context.Context, _ *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	return logical.ErrorResponse("data read is scaffolded; KV implementation pending"), nil
}

func pathDataDelete(_ context.Context, _ *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	return logical.ErrorResponse("data delete is scaffolded; KV and remote delete policy implementation pending"), nil
}
