package backend

import (
	"context"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

func TestSourceEnablePreservesInFlightOperation(t *testing.T) {
	env, provider, _, operationID := setupBlockingMutationDispatch(t, nil)
	errCh := runPeriodicAsync(env)
	waitForBlockingMutation(t, provider.started)
	before := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)

	reenableSource(t, env)
	after := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if *after != *before {
		t.Fatalf("source enable changed the in-flight operation: before=%#v after=%#v", before, after)
	}
	_, claimed, err := env.b.claimDispatchableOutboxRecord(
		context.Background(), env.storage, operationID, before.ClaimOwner, nowUTC(), observability.OperationPeriodic,
	)
	if err != nil || claimed {
		t.Fatalf("second dispatch claim = %t, err = %v; want no claim", claimed, err)
	}

	provider.releaseMutation()
	if err := <-errCh; err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	assertOutboxMissing(t, env.storage, operationID)
}

func TestSourceEnablePreservesRetrySchedule(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	env.enableAppDBSourceSync()
	env.createFakeDestination("default")
	response := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, response), "association")
	before := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	before.State = outboxStateRetryWait
	before.Attempts = 2
	before.ClaimAttempt = 3
	before.NotBefore = nowUTC().Add(time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *before); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}

	reenableSource(t, env)
	after := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if *after != *before {
		t.Fatalf("source enable changed scheduled retry: before=%#v after=%#v", before, after)
	}
}

func TestSourceEnablePreservesExpiredClaimGeneration(t *testing.T) {
	env, provider, _, operationID := setupBlockingMutationDispatch(t, nil)
	errCh := runPeriodicAsync(env)
	waitForBlockingMutation(t, provider.started)
	original := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	expired := *original
	expired.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, expired); err != nil {
		t.Fatalf("expire original claim: %v", err)
	}

	reenableSource(t, env)
	reclaimed, ok, err := env.b.claimDispatchableOutboxRecord(
		context.Background(), env.storage, operationID, original.ClaimOwner, nowUTC(), observability.OperationPeriodic,
	)
	if err != nil || !ok {
		t.Fatalf("reclaim expired operation: ok=%t err=%v", ok, err)
	}
	if reclaimed.ClaimAttempt <= original.ClaimAttempt {
		t.Fatalf("claim attempt = %d, want greater than %d", reclaimed.ClaimAttempt, original.ClaimAttempt)
	}
	provider.releaseMutation()
	if err := <-errCh; err != nil {
		t.Fatalf("stale dispatch: %v", err)
	}
	after := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if *after != *reclaimed {
		t.Fatal("stale dispatch changed the replacement claim")
	}
}

func TestSourceEnablePreservesTerminalClaimGeneration(t *testing.T) {
	env, provider, _, operationID := setupBlockingMutationDispatch(t, nil)
	errCh := runPeriodicAsync(env)
	waitForBlockingMutation(t, provider.started)
	original := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	expired := *original
	expired.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, expired); err != nil {
		t.Fatalf("expire original claim: %v", err)
	}
	replacement, ok, err := env.b.claimDispatchableOutboxRecord(
		context.Background(), env.storage, operationID, original.ClaimOwner, nowUTC(), observability.OperationPeriodic,
	)
	if err != nil || !ok {
		t.Fatalf("claim replacement: ok=%t err=%v", ok, err)
	}
	if err := env.b.markClaimedOperationFailed(context.Background(), env.storage, *replacement, operationFailure{
		class: providers.ErrorClassValidation, message: "test validation failure",
	}, nowUTC()); err != nil {
		t.Fatalf("fail replacement operation: %v", err)
	}
	reenableSource(t, env)
	current, ok, err := env.b.claimDispatchableOutboxRecord(
		context.Background(), env.storage, operationID, original.ClaimOwner, nowUTC(), observability.OperationPeriodic,
	)
	if err != nil || !ok {
		t.Fatalf("claim re-enqueued terminal operation: ok=%t err=%v", ok, err)
	}
	provider.releaseMutation()
	if err := <-errCh; err != nil {
		t.Fatalf("stale dispatch: %v", err)
	}
	after := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if *after != *current {
		t.Fatal("stale dispatch changed the re-enqueued terminal operation")
	}
}

func TestOutboxClaimRejectsRecreatedOperation(t *testing.T) {
	original := outboxRecord{
		ClaimOwner: "worker", ClaimAttempt: 1,
		ClaimExpiresTime: nowUTC().Add(-time.Minute).Format(timeFormatRFC3339),
	}
	recreated := original
	recreated.ClaimExpiresTime = nowUTC().Add(outboxClaimLease).Format(timeFormatRFC3339)
	if outboxClaimMatches(&recreated, original) {
		t.Fatal("claim from an earlier operation lifetime matched the recreated operation")
	}
}

func reenableSource(t *testing.T, env *backendTestEnv) {
	t.Helper()
	assertNoErrorResponse(t, env.update("sources/app/db/disable"))
	assertNoErrorResponse(t, env.update("sources/app/db/enable"))
}
