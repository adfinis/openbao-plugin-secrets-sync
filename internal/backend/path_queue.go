package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathQueue(_ *secretSyncBackend) *framework.Path {
	return &framework.Path{
		Pattern: "queue/?",
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{
				Callback: pathQueueRead,
				Summary:  "Read outbox queue summary.",
			},
		},
		HelpSynopsis:    "Inspect sync queue state.",
		HelpDescription: "Returns scaffolded queue counters until durable outbox implementation is added.",
	}
}

func pathQueueRead(_ context.Context, _ *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	return &logical.Response{Data: newResponseData(
		responseField("pending", 0),
		responseField("retry_wait", 0),
		responseField("terminal", 0),
		responseField("oldest_age_seconds", 0),
	)}, nil
}
