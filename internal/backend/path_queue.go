package backend

import (
	"context"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	defaultDrainMaxOperations = 100
	restoreGuardActiveError   = "restore guard is active; acknowledge " +
		"config/restore-guard/acknowledge before remote mutation"
	remoteMutationUnsafeError = "remote mutation is not allowed on this replication node"
)

type queueSummary struct {
	Pending          int
	RetryWait        int
	Claimed          int
	Terminal         int
	Canceled         int
	OldestAgeSeconds int
	Capacity         int
	Utilization      float64
}

func pathQueue(b *secretSyncBackend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "queue/?",
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ReadOperation: &framework.PathOperation{
					Callback: b.pathQueueRead,
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

func (b *secretSyncBackend) pathQueueRead(
	ctx context.Context,
	req *logical.Request,
	_ *framework.FieldData,
) (*logical.Response, error) {
	cfg, err := readGlobalConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	summary, err := readQueueSummary(ctx, req.Storage, nowUTC())
	if err != nil {
		return nil, err
	}
	summary.applyCapacity(cfg.QueueCapacity)
	b.recordQueueSummary(ctx, summary)
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
		b.recordRemoteMutationBlocked(ctx, observability.OperationDrain, observability.ReasonDisabled)
		return logical.ErrorResponse("secret sync is disabled"), nil
	}
	if cfg.RestoreGuard {
		b.recordRemoteMutationBlocked(ctx, observability.OperationDrain, observability.ReasonRestoreGuard)
		return logical.ErrorResponse(restoreGuardActiveError), nil
	}
	if !b.remoteMutationAllowed() {
		b.recordRemoteMutationBlocked(ctx, observability.OperationDrain, observability.ReasonReplicationState)
		return logical.ErrorResponse(remoteMutationUnsafeError), nil
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
	summary, err := readQueueSummary(ctx, req.Storage, now)
	if err != nil {
		return nil, err
	}
	summary.applyCapacity(cfg.QueueCapacity)
	b.recordQueueSummary(ctx, summary)
	b.observer.Operation(ctx, observability.OperationEvent{
		Operation: observability.OperationDrain,
		Result:    observability.ResultSuccess,
	})
	return &logical.Response{Data: newResponseData(
		responseField("processed", processed),
		responseField("max_operations", maxOperations),
		responseField("queue_pending", summary.Pending),
		responseField("queue_retry_wait", summary.RetryWait),
		responseField("queue_claimed", summary.Claimed),
		responseField("queue_terminal", summary.Terminal),
		responseField("queue_canceled", summary.Canceled),
		responseField("queue_oldest_age_seconds", summary.OldestAgeSeconds),
		responseField("queue_capacity", summary.Capacity),
		responseField("queue_utilization", summary.Utilization),
		responseField("queue", queueSummaryResponse(summary)),
	)}, nil
}

func (b *secretSyncBackend) recordQueueSummary(ctx context.Context, summary queueSummary) {
	b.observer.QueueDepth(ctx, outboxStatePending, summary.Pending)
	b.observer.QueueDepth(ctx, outboxStateRetryWait, summary.RetryWait)
	b.observer.QueueDepth(ctx, outboxStateFailedTerminal, summary.Terminal)
	b.observer.QueueDepth(ctx, outboxStateCanceled, summary.Canceled)
	b.observer.QueueCapacity(ctx, observability.QueueCapacityEvent{
		Capacity:    summary.Capacity,
		Utilization: summary.Utilization,
	})
}

func readQueueSummary(ctx context.Context, storage logical.Storage, now time.Time) (queueSummary, error) {
	pendingIDs, err := listOutboxIDsForState(ctx, storage, outboxStatePending)
	if err != nil {
		return queueSummary{}, err
	}
	retryWaitIDs, err := listOutboxIDsForState(ctx, storage, outboxStateRetryWait)
	if err != nil {
		return queueSummary{}, err
	}
	terminalIDs, err := listOutboxIDsForState(ctx, storage, outboxStateFailedTerminal)
	if err != nil {
		return queueSummary{}, err
	}
	canceledIDs, err := listOutboxIDsForState(ctx, storage, outboxStateCanceled)
	if err != nil {
		return queueSummary{}, err
	}
	summary := queueSummary{}
	summary.Pending = len(pendingIDs)
	summary.RetryWait = len(retryWaitIDs)
	summary.Terminal = len(terminalIDs)
	summary.Canceled = len(canceledIDs)
	for _, id := range append(pendingIDs, retryWaitIDs...) {
		record, err := getOutbox(ctx, storage, id)
		if err != nil {
			return queueSummary{}, err
		}
		if record == nil || !isQueuedOutboxState(record.State) {
			continue
		}
		if isOutboxClaimActive(*record, now) {
			summary.Claimed++
		}
		summary.recordQueuedAge(*record, now)
	}
	return summary, nil
}

func (summary *queueSummary) recordQueuedAge(record outboxRecord, now time.Time) {
	if record.CreatedTime == "" {
		return
	}
	createdTime, err := time.Parse(timeFormatRFC3339, record.CreatedTime)
	if err != nil || createdTime.After(now) {
		return
	}
	ageSeconds := int(now.Sub(createdTime).Seconds())
	if ageSeconds > summary.OldestAgeSeconds {
		summary.OldestAgeSeconds = ageSeconds
	}
}

func (summary *queueSummary) applyCapacity(capacity int) {
	if capacity <= 0 {
		capacity = defaultQueueCapacity
	}
	summary.Capacity = capacity
	summary.Utilization = float64(summary.queuedDepth()) / float64(capacity)
}

func (summary queueSummary) queuedDepth() int {
	return summary.Pending + summary.RetryWait
}

func queueSummaryResponse(summary queueSummary) map[string]interface{} { //nolint:forbidigo
	return newResponseData(
		responseField("pending", summary.Pending),
		responseField("retry_wait", summary.RetryWait),
		responseField("claimed", summary.Claimed),
		responseField("terminal", summary.Terminal),
		responseField("canceled", summary.Canceled),
		responseField("oldest_age_seconds", summary.OldestAgeSeconds),
		responseField("capacity", summary.Capacity),
		responseField("utilization", summary.Utilization),
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
	now := nowUTC()
	if isOutboxClaimActive(*record, now) {
		return logical.ErrorResponse("operation is currently claimed"), nil
	}
	switch record.State {
	case outboxStatePending:
		return nil, nil
	case outboxStateRetryWait, outboxStateFailedTerminal, outboxStateCanceled:
		nowString := now.Format(timeFormatRFC3339)
		record.Attempts = 0
		record.State = outboxStatePending
		record.NotBefore = nowString
		record.UpdatedTime = nowString
		clearOutboxClaim(record)
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
	now := nowUTC()
	if isOutboxClaimActive(*record, now) {
		return logical.ErrorResponse("operation is currently claimed"), nil
	}
	switch record.State {
	case outboxStateCanceled:
		return nil, nil
	case outboxStatePending, outboxStateRetryWait:
		nowString := now.Format(timeFormatRFC3339)
		record.State = outboxStateCanceled
		record.UpdatedTime = nowString
		clearOutboxClaim(record)
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
		responseField("claimed", isOutboxClaimActive(record, nowUTC())),
		responseField("claim_owner", record.ClaimOwner),
		responseField("claim_expires_time", record.ClaimExpiresTime),
		responseField("claim_attempt", record.ClaimAttempt),
	)
}
