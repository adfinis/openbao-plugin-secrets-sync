package backend

import (
	"context"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const defaultDrainMaxOperations = 100

type queueSummary struct {
	Pending   int
	RetryWait int
	Terminal  int
	Canceled  int
}

func pathQueue(b *secretSyncBackend) []*framework.Path {
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
			Pattern: "queue/drain",
			Fields: map[string]*framework.FieldSchema{
				"max_operations": {
					Type:        framework.TypeInt,
					Description: "Maximum due operations to process in this drain request. Defaults to 100.",
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: b.pathQueueDrain,
					Summary:  "Process due sync queue operations.",
				},
			},
			HelpSynopsis:    "Drain due sync queue work.",
			HelpDescription: "Runs the same durable outbox dispatcher used by the periodic function.",
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
	summary, err := readQueueSummary(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	return &logical.Response{Data: queueSummaryResponse(summary)}, nil
}

func (b *secretSyncBackend) pathQueueDrain(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if cfg.Disabled {
		return logical.ErrorResponse("secret sync is disabled"), nil
	}
	maxOperations := data.Get("max_operations").(int)
	if maxOperations < 0 {
		return logical.ErrorResponse("max_operations must be greater than or equal to zero"), nil
	}
	if maxOperations == 0 {
		maxOperations = defaultDrainMaxOperations
	}
	now := nowUTC()
	if err := recoverIncompleteEnqueueIntents(ctx, req.Storage, now); err != nil {
		return nil, err
	}
	processed, err := b.processDueOutboxLimit(ctx, req.Storage, now, maxOperations)
	if err != nil {
		return nil, err
	}
	summary, err := readQueueSummary(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	return &logical.Response{Data: newResponseData(
		responseField("processed", processed),
		responseField("max_operations", maxOperations),
		responseField("queue", queueSummaryResponse(summary)),
	)}, nil
}

func readQueueSummary(ctx context.Context, storage logical.Storage) (queueSummary, error) {
	ids, err := listOutboxIDs(ctx, storage)
	if err != nil {
		return queueSummary{}, err
	}
	summary := queueSummary{}
	for _, id := range ids {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return queueSummary{}, err
		}
		if record == nil {
			continue
		}
		switch record.State {
		case outboxStatePending:
			summary.Pending++
		case outboxStateRetryWait:
			summary.RetryWait++
		case outboxStateFailedTerminal:
			summary.Terminal++
		case outboxStateCanceled:
			summary.Canceled++
		}
	}
	return summary, nil
}

func queueSummaryResponse(summary queueSummary) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("pending", summary.Pending),
		responseField("retry_wait", summary.RetryWait),
		responseField("terminal", summary.Terminal),
		responseField("canceled", summary.Canceled),
		responseField("oldest_age_seconds", 0),
	)
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
		record.Attempts = 0
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
