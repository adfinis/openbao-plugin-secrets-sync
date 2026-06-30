package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathQueue(_ *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "queue/?",
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathQueueRead,
					Summary:  "Read outbox queue summary.",
				},
			},
			HelpSynopsis:    "Inspect sync queue state.",
			HelpDescription: "Returns queue counters from durable outbox records.",
		},
		{
			Pattern: "queue/" + framework.GenericNameRegex("operation_id"),
			Fields: map[string]*framework.FieldSchema{
				"operation_id": {
					Type:        framework.TypeString,
					Description: "Outbox operation identifier.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback: pathQueueOperationRead,
					Summary:  "Read one outbox operation.",
				},
			},
			HelpSynopsis:    "Inspect a sync queue operation.",
			HelpDescription: "Returns one durable outbox operation without source payload data.",
		},
		{
			Pattern: "queue/" + framework.GenericNameRegex("operation_id") + "/retry",
			Fields: map[string]*framework.FieldSchema{
				"operation_id": {
					Type:        framework.TypeString,
					Description: "Outbox operation identifier.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathQueueOperationRetry,
					Summary:  "Retry one outbox operation.",
				},
			},
			HelpSynopsis:    "Retry a sync queue operation.",
			HelpDescription: "Moves canceled, retry-wait, or terminal failed outbox work back to pending.",
		},
		{
			Pattern: "queue/" + framework.GenericNameRegex("operation_id") + "/cancel",
			Fields: map[string]*framework.FieldSchema{
				"operation_id": {
					Type:        framework.TypeString,
					Description: "Outbox operation identifier.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathQueueOperationCancel,
					Summary:  "Cancel one queued outbox operation.",
				},
			},
			HelpSynopsis:    "Cancel a sync queue operation.",
			HelpDescription: "Moves pending or retry-wait outbox work to canceled.",
		},
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
	canceled := 0
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
		case outboxStateCanceled:
			canceled++
		}
	}
	return &logical.Response{Data: newResponseData(
		responseField("pending", pending),
		responseField("retry_wait", retryWait),
		responseField("terminal", terminal),
		responseField("canceled", canceled),
		responseField("oldest_age_seconds", 0),
	)}, nil
}

func pathQueueOperationRead(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, err := getOutbox(ctx, req.Storage, data.Get("operation_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	return &logical.Response{Data: outboxOperationResponse(*record)}, nil
}

func pathQueueOperationRetry(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, err := getOutbox(ctx, req.Storage, data.Get("operation_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	switch record.State {
	case outboxStatePending:
		return nil, nil
	case outboxStateRetryWait, outboxStateFailedTerminal, outboxStateCanceled:
		now := nowUTC().Format(timeFormatRFC3339)
		record.State = outboxStatePending
		record.NotBefore = now
		record.UpdatedTime = now
		if err := putOutbox(ctx, req.Storage, *record); err != nil {
			return nil, err
		}
		return &logical.Response{Data: outboxOperationResponse(*record)}, nil
	default:
		return logical.ErrorResponse("operation is not retryable"), nil
	}
}

func pathQueueOperationCancel(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	record, err := getOutbox(ctx, req.Storage, data.Get("operation_id").(string))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	switch record.State {
	case outboxStateCanceled:
		return nil, nil
	case outboxStatePending, outboxStateRetryWait:
		now := nowUTC().Format(timeFormatRFC3339)
		record.State = outboxStateCanceled
		record.UpdatedTime = now
		if err := putOutbox(ctx, req.Storage, *record); err != nil {
			return nil, err
		}
		return &logical.Response{Data: outboxOperationResponse(*record)}, nil
	default:
		return logical.ErrorResponse("operation is not cancelable"), nil
	}
}

func outboxOperationResponse(record outboxRecord) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("id", record.ID),
		responseField("type", string(record.Type)),
		responseField("path", record.Path),
		responseField("version", record.Version),
		responseField("association_id", record.AssociationID),
		responseField("object_id", record.ObjectID),
		responseField("destination_ref", record.DestinationRef),
		responseField("state", record.State),
		responseField("attempts", record.Attempts),
		responseField("not_before", record.NotBefore),
		responseField("created_time", record.CreatedTime),
		responseField("updated_time", record.UpdatedTime),
		responseField("idempotency_key", record.IdempotencyKey),
	)
}
