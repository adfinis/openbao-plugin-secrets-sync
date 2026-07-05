package backend

import (
	"context"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const (
	defaultEventDispatchMaxOperations = 16
	defaultEventDispatchRecoveryLimit = defaultPeriodicRecoveryMaxIntents
)

type eventDispatchSignal struct{}

type eventDispatchRunResult struct {
	nextRetryAt *time.Time
}

func (b *secretSyncBackend) startEventDispatcher(storage logical.Storage) {
	if storage == nil {
		return
	}
	b.eventDispatchMu.Lock()
	defer b.eventDispatchMu.Unlock()

	if b.eventDispatchCh != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.eventDispatchCh = make(chan eventDispatchSignal, 1)
	b.eventDispatchCancel = cancel
	b.eventDispatchDone = make(chan struct{})
	go b.eventDispatchLoop(ctx, storage, b.eventDispatchCh, b.eventDispatchDone)
}

func (b *secretSyncBackend) stopEventDispatcher(ctx context.Context) {
	b.eventDispatchMu.Lock()
	cancel := b.eventDispatchCancel
	done := b.eventDispatchDone
	b.eventDispatchCh = nil
	b.eventDispatchCancel = nil
	b.eventDispatchDone = nil
	b.eventDispatchMu.Unlock()

	if cancel == nil || done == nil {
		return
	}
	cancel()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (b *secretSyncBackend) signalEventDispatch() {
	b.eventDispatchMu.Lock()
	ch := b.eventDispatchCh
	b.eventDispatchMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- eventDispatchSignal{}:
	default:
	}
}

func (b *secretSyncBackend) eventDispatchLoop(
	ctx context.Context,
	storage logical.Storage,
	signals <-chan eventDispatchSignal,
	done chan<- struct{},
) {
	defer close(done)
	var retryTimer *time.Timer
	var retryTimerC <-chan time.Time
	stopRetryTimer := func() {
		if retryTimer == nil {
			return
		}
		if !retryTimer.Stop() {
			select {
			case <-retryTimer.C:
			default:
			}
		}
		retryTimer = nil
		retryTimerC = nil
	}
	armRetryTimer := func(nextRetryAt *time.Time) {
		stopRetryTimer()
		if nextRetryAt == nil {
			return
		}
		delay := time.Until(*nextRetryAt)
		if delay < 0 {
			delay = 0
		}
		retryTimer = time.NewTimer(delay)
		retryTimerC = retryTimer.C
	}
	defer stopRetryTimer()

	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			result := b.runEventDispatch(ctx, storage)
			armRetryTimer(result.nextRetryAt)
		case <-retryTimerC:
			retryTimer = nil
			retryTimerC = nil
			result := b.runEventDispatch(ctx, storage)
			armRetryTimer(result.nextRetryAt)
		}
	}
}

func (b *secretSyncBackend) runEventDispatch(ctx context.Context, storage logical.Storage) eventDispatchRunResult {
	totalProcessed := 0
	for {
		if err := ctx.Err(); err != nil {
			b.recordEventDispatchError(ctx, err)
			return eventDispatchRunResult{}
		}
		processed, limit, ok := b.runEventDispatchPass(ctx, storage)
		if !ok {
			return eventDispatchRunResult{}
		}
		totalProcessed += processed
		if processed < limit {
			break
		}
	}
	result := observability.ResultSuccess
	if totalProcessed == 0 {
		result = observability.ResultSkipped
	}
	b.observer.Operation(ctx, observability.OperationEvent{
		Operation: observability.OperationEventDispatch,
		Result:    result,
	})
	nextRetryAt, err := nextFutureOutboxDueTime(ctx, storage, nowUTC())
	if err != nil {
		b.recordEventDispatchError(ctx, err)
		return eventDispatchRunResult{}
	}
	return eventDispatchRunResult{nextRetryAt: nextRetryAt}
}

func (b *secretSyncBackend) runEventDispatchPass(
	ctx context.Context,
	storage logical.Storage,
) (processed int, limit int, ok bool) {
	if !b.remoteMutationAllowed() {
		b.recordRemoteMutationBlocked(
			ctx,
			observability.OperationEventDispatch,
			observability.ReasonReplicationState,
		)
		return 0, 0, false
	}
	if _, err := ensureRuntimeState(ctx, storage); err != nil {
		b.recordEventDispatchError(ctx, err)
		return 0, 0, false
	}
	cfg, err := readGlobalConfig(ctx, storage)
	if err != nil {
		b.recordEventDispatchError(ctx, err)
		return 0, 0, false
	}
	if !cfg.EventDispatchEnabled {
		return 0, 0, false
	}
	if cfg.Disabled {
		b.recordRemoteMutationBlocked(ctx, observability.OperationEventDispatch, observability.ReasonDisabled)
		return 0, 0, false
	}
	if cfg.RestoreGuard {
		b.recordRemoteMutationBlocked(ctx, observability.OperationEventDispatch, observability.ReasonRestoreGuard)
		return 0, 0, false
	}

	now := nowUTC()
	if err := b.recoverIncompleteEnqueueIntentsLimit(ctx, storage, now, defaultEventDispatchRecoveryLimit); err != nil {
		b.recordEventDispatchError(ctx, err)
		return 0, 0, false
	}
	processed, err = b.processDueOutboxLimit(ctx, storage, now, cfg.EventDispatchMaxOperations)
	if err != nil {
		b.recordEventDispatchError(ctx, err)
		return 0, 0, false
	}
	return processed, cfg.EventDispatchMaxOperations, true
}

func (b *secretSyncBackend) recordEventDispatchError(ctx context.Context, err error) {
	b.Logger().Error("event-triggered dispatch failed", "error", err)
	b.observer.Operation(ctx, observability.OperationEvent{
		Operation: observability.OperationEventDispatch,
		Result:    observability.ResultFailure,
	})
}
