package backend

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestClaimOutboxRecordAdvancesClaimAttemptOnReclaim(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)

	first, ok, err := claimOutboxRecord(context.Background(), env.storage, *operation, "worker", nowUTC())
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !ok {
		t.Fatal("first claim was not acquired")
	}
	if got := first.ClaimAttempt; got != 1 {
		t.Fatalf("first claim attempt = %d, want 1", got)
	}

	first.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *first); err != nil {
		t.Fatalf("expire first claim: %v", err)
	}
	second, ok, err := claimOutboxRecord(context.Background(), env.storage, *first, "worker", nowUTC())
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if !ok {
		t.Fatal("second claim was not acquired")
	}
	if got := second.ClaimAttempt; got != 2 {
		t.Fatalf("second claim attempt = %d, want 2", got)
	}
}

func TestClaimRejectsOperationBeforeCanonicalNotBefore(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	now := nowUTC()
	operation.State = outboxStateRetryWait
	operation.NotBefore = now.Add(time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("write future retry operation: %v", err)
	}

	claimed, ok, err := env.b.claimDispatchableOutboxRecord(
		context.Background(),
		env.storage,
		operation.ID,
		"worker",
		now,
		observability.OperationPeriodic,
	)
	if err != nil {
		t.Fatalf("claim future operation: %v", err)
	}
	if ok || claimed != nil {
		t.Fatalf("future operation claim = %#v, ok = %t; want no claim", claimed, ok)
	}
	assertOutboxOperation(t, env.storage, operation.ID, operation.Version, outboxStateRetryWait)
}

func TestSequentialDispatchRefreshesClaimTimePerOperation(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	env.createFakeDestination("default")
	associationResp := env.createFakeSecretKeyAssociation(deleteModeRetain)
	if got := len(operationIDsFromResponse(t, associationResp)); got != 2 {
		t.Fatalf("queued operations = %d, want 2", got)
	}
	env.acknowledgeRestoreGuard()
	storage := &claimExpiryRecordingStorage{Storage: env.storage}
	batchNow := nowUTC().Add(time.Minute)
	clock := newSequenceClock(
		batchNow,
		batchNow,
		batchNow.Add(3*time.Minute),
	)

	processed, err := env.b.processDueOutboxLimitWithClock(
		context.Background(),
		storage,
		batchNow,
		2,
		observability.OperationPeriodic,
		clock,
	)
	if err != nil {
		t.Fatalf("process sequential operations: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed operations = %d, want 2", processed)
	}
	expires := storage.claimExpiries()
	if len(expires) != 2 {
		t.Fatalf("recorded claim expiries = %v, want two", expires)
	}
	first, err := time.Parse(timeFormatRFC3339, expires[0])
	if err != nil {
		t.Fatalf("parse first claim expiry: %v", err)
	}
	second, err := time.Parse(timeFormatRFC3339, expires[1])
	if err != nil {
		t.Fatalf("parse second claim expiry: %v", err)
	}
	if got := second.Sub(first); got != 3*time.Minute {
		t.Fatalf("claim expiry advance = %s, want 3m", got)
	}
}

func newSequenceClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(times) {
			return times[len(times)-1]
		}
		now := times[index]
		index++
		return now
	}
}

type claimExpiryRecordingStorage struct {
	logical.Storage

	mu       sync.Mutex
	expiries []string
}

func (storage *claimExpiryRecordingStorage) Put(ctx context.Context, entry *logical.StorageEntry) error {
	if strings.HasPrefix(entry.Key, outboxStoragePrefix) {
		var record outboxRecord
		if err := entry.DecodeJSON(&record); err != nil {
			return err
		}
		if record.ClaimOwner != "" {
			storage.mu.Lock()
			storage.expiries = append(storage.expiries, record.ClaimExpiresTime)
			storage.mu.Unlock()
		}
	}
	return storage.Storage.Put(ctx, entry)
}

func (storage *claimExpiryRecordingStorage) claimExpiries() []string {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return append([]string(nil), storage.expiries...)
}

func TestDispatchSkipsSuccessCommitAfterClaimReclaim(t *testing.T) {
	env, provider, associationID, operationID := setupBlockingMutationDispatch(t, nil)
	errCh := runPeriodicAsync(env)
	waitForBlockingMutation(t, provider.started)

	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if operation.ClaimOwner == "" {
		t.Fatal("operation must be claimed by the first worker")
	}
	operation.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("expire first worker claim: %v", err)
	}
	reclaimed, ok, err := env.b.claimDispatchableOutboxRecord(
		context.Background(),
		env.storage,
		operationID,
		operation.ClaimOwner,
		nowUTC(),
		observability.OperationPeriodic,
	)
	if err != nil {
		t.Fatalf("reclaim operation: %v", err)
	}
	if !ok {
		t.Fatal("operation was not reclaimed")
	}
	if reclaimed.ClaimAttempt == operation.ClaimAttempt {
		t.Fatalf("reclaimed claim attempt = %d, want a new claim generation", reclaimed.ClaimAttempt)
	}

	provider.releaseMutation()
	if err := <-errCh; err != nil {
		t.Fatalf("periodic: %v", err)
	}
	operation = assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if got := operation.ClaimAttempt; got != reclaimed.ClaimAttempt {
		t.Fatalf("claim attempt after stale worker commit = %d, want %d", got, reclaimed.ClaimAttempt)
	}
	if got := operation.Attempts; got != 0 {
		t.Fatalf("attempts after stale worker commit = %d, want 0", got)
	}
	status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want no stale success status", status)
	}
}

func TestDispatchSkipsFailureCommitAfterClaimReclaim(t *testing.T) {
	env, provider, associationID, operationID := setupBlockingMutationDispatch(
		t,
		&providers.Error{Class: providers.ErrorClassUnavailable, Message: "blocked unavailable"},
	)
	errCh := runPeriodicAsync(env)
	waitForBlockingMutation(t, provider.started)

	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	operation.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("expire first worker claim: %v", err)
	}
	reclaimed, ok, err := env.b.claimDispatchableOutboxRecord(
		context.Background(),
		env.storage,
		operationID,
		operation.ClaimOwner,
		nowUTC(),
		observability.OperationPeriodic,
	)
	if err != nil {
		t.Fatalf("reclaim operation: %v", err)
	}
	if !ok {
		t.Fatal("operation was not reclaimed")
	}

	provider.releaseMutation()
	if err := <-errCh; err != nil {
		t.Fatalf("periodic: %v", err)
	}
	operation = assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if got := operation.ClaimAttempt; got != reclaimed.ClaimAttempt {
		t.Fatalf("claim attempt after stale worker failure = %d, want %d", got, reclaimed.ClaimAttempt)
	}
	if got := operation.Attempts; got != 0 {
		t.Fatalf("attempts after stale worker failure = %d, want 0", got)
	}
	status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want no stale failure status", status)
	}
}

func TestDispatchSkipsStatusAfterExpiredClaimAssociationDelete(t *testing.T) {
	env, provider, associationID, operationID := setupBlockingMutationDispatch(t, nil)
	errCh := runPeriodicAsync(env)
	waitForBlockingMutation(t, provider.started)

	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	operation.ClaimExpiresTime = nowUTC().Add(-time.Minute).Format(timeFormatRFC3339)
	if err := putOutbox(context.Background(), env.storage, *operation); err != nil {
		t.Fatalf("expire first worker claim: %v", err)
	}

	deleteResp := env.delete("associations/app/db/" + associationID)
	if deleteResp != nil && deleteResp.IsError() {
		t.Fatalf("unexpected association delete error: %v", deleteResp.Error())
	}
	assertOutboxMissing(t, env.storage, operationID)

	provider.releaseMutation()
	if err := <-errCh; err != nil {
		t.Fatalf("periodic: %v", err)
	}
	assertOutboxMissing(t, env.storage, operationID)
	status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != nil {
		t.Fatalf("status = %#v, want no status for deleted association", status)
	}
}

func setupBlockingMutationDispatch(
	t *testing.T,
	err error,
) (*backendTestEnv, *blockingMutationProvider, string, string) {
	t.Helper()
	env := newBackendTestEnv(t)
	provider := newBlockingMutationProvider(err)
	t.Cleanup(provider.releaseMutation)
	env.b.providerRegistry = providers.MustNewRegistry(provider)

	env.writeAppDBSecret("initial")
	enableAppDBSourceSync(t, env.b, env.storage)
	destinationResp := env.update("destinations/claim/prod")
	if destinationResp != nil && destinationResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", destinationResp.Error())
	}
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef("claim", "prod"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, associationResp)
	associationID := associationIDFromResponse(t, associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	env.acknowledgeRestoreGuard()
	return env, provider, associationID, operationID
}

func runPeriodicAsync(env *backendTestEnv) <-chan error {
	env.t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.b.periodic(context.Background(), &logical.Request{Storage: env.storage})
	}()
	return errCh
}

func waitForBlockingMutation(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider mutation")
	}
}

type blockingMutationProvider struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
	err         error
}

func newBlockingMutationProvider(err error) *blockingMutationProvider {
	return &blockingMutationProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     err,
	}
}

func (p *blockingMutationProvider) Type() string {
	return "claim"
}

func (p *blockingMutationProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		MaxPayloadBytes:             1024 * 1024,
	}
}

func (*blockingMutationProvider) ValidateConfig(context.Context, providers.DestinationConfig) error {
	return nil
}

func (p *blockingMutationProvider) OpenDestination(
	context.Context,
	providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	return blockingMutationRuntime{provider: p}, nil
}

func (p *blockingMutationProvider) releaseMutation() {
	p.releaseOnce.Do(func() {
		close(p.release)
	})
}

type blockingMutationRuntime struct {
	provider *blockingMutationProvider
}

func (blockingMutationRuntime) Health(context.Context) (*providers.HealthResult, error) {
	return &providers.HealthResult{Healthy: true}, nil
}

func (blockingMutationRuntime) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
}

func (r blockingMutationRuntime) Upsert(context.Context, providers.UpsertRequest) (*providers.SyncResult, error) {
	r.provider.startedOnce.Do(func() {
		close(r.provider.started)
	})
	<-r.provider.release
	if r.provider.err != nil {
		return nil, r.provider.err
	}
	return &providers.SyncResult{RemoteVersion: "blocked"}, nil
}

func (r blockingMutationRuntime) Delete(context.Context, providers.DeleteRequest) (*providers.SyncResult, error) {
	r.provider.startedOnce.Do(func() {
		close(r.provider.started)
	})
	<-r.provider.release
	if r.provider.err != nil {
		return nil, r.provider.err
	}
	return &providers.SyncResult{RemoteVersion: "blocked-delete"}, nil
}

func (blockingMutationRuntime) ReadState(context.Context, providers.ReadStateRequest) (*providers.RemoteState, error) {
	return &providers.RemoteState{Exists: false}, nil
}

func (blockingMutationRuntime) Close(context.Context) error {
	return nil
}
