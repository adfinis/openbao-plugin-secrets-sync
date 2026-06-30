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
		HelpDescription: "Returns queue counters from durable outbox records.",
	}
}

func pathQueueRead(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	ids, err := listOutboxIDs(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	pending := 0
	retryWait := 0
	terminal := 0
	for _, id := range ids {
		record, err := getOutbox(ctx, req.Storage, id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		switch record.State {
		case outboxStatePending:
			pending++
		case outboxStateRetryWait:
			retryWait++
		case outboxStateFailedTerminal:
			terminal++
		}
	}
	return &logical.Response{Data: newResponseData(
		responseField("pending", pending),
		responseField("retry_wait", retryWait),
		responseField("terminal", terminal),
		responseField("oldest_age_seconds", 0),
	)}, nil
}
