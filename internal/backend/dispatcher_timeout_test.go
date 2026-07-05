package backend

import (
	"context"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
)

func TestProviderMutationContextIsBoundedBelowClaimLease(t *testing.T) {
	provider := &providerMutationTestProvider{
		providerType:    "deadline",
		openDeadline:    make(chan deadlineObservation, 1),
		upsertDeadline:  make(chan deadlineObservation, 1),
		providerVersion: "deadline",
	}
	env, operationID := setupProviderMutationTest(t, provider)

	env.runPeriodicAllowed("provider mutation deadline")

	assertOutboxMissing(t, env.storage, operationID)
	assertProviderMutationDeadline(t, receiveDeadlineObservation(t, provider.openDeadline), "open destination")
	assertProviderMutationDeadline(t, receiveDeadlineObservation(t, provider.upsertDeadline), "upsert")
}

func TestProviderDeleteMutationContextIsBoundedBelowClaimLease(t *testing.T) {
	provider := &providerMutationTestProvider{
		providerType:    "delete-deadline",
		deleteDeadline:  make(chan deadlineObservation, 1),
		providerVersion: "deadline",
	}
	env, _ := setupProviderMutationTestWithDeleteMode(t, provider, deleteModeDelete)
	env.runPeriodicAllowed("provider mutation initial upsert")

	deleteResp := env.delete("data/app/db")
	assertNoErrorResponse(t, deleteResp)
	operationID := requireSingleOperationID(
		t,
		operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{})),
		"delete",
	)
	env.runPeriodicAllowed("provider mutation delete deadline")

	assertOutboxMissing(t, env.storage, operationID)
	assertProviderMutationDeadline(t, receiveDeadlineObservation(t, provider.deleteDeadline), "delete")
}

func TestProviderMutationDeadlineExceededRetriesAsUnavailable(t *testing.T) {
	provider := &providerMutationTestProvider{
		providerType: "deadline-exceeded",
		upsertErr:    context.DeadlineExceeded,
	}
	env, operationID := setupProviderMutationTest(t, provider)

	env.runPeriodicAllowed("provider mutation timeout")

	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStateRetryWait)
	if got := operation.Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if operation.ClaimOwner != "" || operation.ClaimExpiresTime != "" || operation.ClaimAttempt != 0 {
		t.Fatalf("claim fields after provider timeout = %#v, want cleared", operation)
	}
	assertFutureNotBefore(t, operation.NotBefore)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassUnavailable)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateDestinationUnavailable)
}

func TestIsDispatchContextCanceledIgnoresProviderDeadline(t *testing.T) {
	if isDispatchContextCanceled(context.Background(), context.DeadlineExceeded) {
		t.Fatal("provider deadline must not be treated as parent dispatch cancellation")
	}
}

func setupProviderMutationTest(
	t *testing.T,
	provider *providerMutationTestProvider,
) (*backendTestEnv, string) {
	t.Helper()
	return setupProviderMutationTestWithDeleteMode(t, provider, "")
}

func setupProviderMutationTestWithDeleteMode(
	t *testing.T,
	provider *providerMutationTestProvider,
	deleteMode string,
) (*backendTestEnv, string) {
	t.Helper()
	env := newBackendTestEnv(t)
	env.b.providerRegistry = providers.MustNewRegistry(provider)

	env.writeAppDBSecret("initial")
	env.markAppDBSyncable()
	destinationResp := env.update("destinations/"+provider.providerType+"/default", map[string]interface{}{})
	if destinationResp != nil && destinationResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", destinationResp.Error())
	}
	associationData := map[string]interface{}{
		"destination":   destinationRef(provider.providerType, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	}
	if deleteMode != "" {
		associationData["delete_mode"] = deleteMode
	}
	associationResp := env.update("associations/app/db", associationData)
	assertNoErrorResponse(t, associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	env.acknowledgeRestoreGuard()
	return env, operationID
}

type deadlineObservation struct {
	set       bool
	remaining time.Duration
}

func observeDeadline(ctx context.Context) deadlineObservation {
	deadline, ok := ctx.Deadline()
	if !ok {
		return deadlineObservation{}
	}
	return deadlineObservation{
		set:       true,
		remaining: time.Until(deadline),
	}
}

func receiveDeadlineObservation(t *testing.T, ch <-chan deadlineObservation) deadlineObservation {
	t.Helper()
	select {
	case observation := <-ch:
		return observation
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider deadline observation")
		return deadlineObservation{}
	}
}

func assertProviderMutationDeadline(t *testing.T, observation deadlineObservation, label string) {
	t.Helper()
	if !observation.set {
		t.Fatalf("%s context has no deadline", label)
	}
	if observation.remaining <= 0 {
		t.Fatalf("%s deadline remaining = %s, want positive duration", label, observation.remaining)
	}
	if observation.remaining > providerMutationTimeout {
		t.Fatalf("%s deadline remaining = %s, want <= %s", label, observation.remaining, providerMutationTimeout)
	}
	if observation.remaining >= outboxClaimLease {
		t.Fatalf("%s deadline remaining = %s, want below claim lease %s", label, observation.remaining, outboxClaimLease)
	}
}

type providerMutationTestProvider struct {
	providerType    string
	openDeadline    chan deadlineObservation
	upsertDeadline  chan deadlineObservation
	deleteDeadline  chan deadlineObservation
	upsertErr       error
	providerVersion string
}

func (p *providerMutationTestProvider) Type() string {
	return p.providerType
}

func (*providerMutationTestProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		SupportsValueReadback:       true,
		SupportsMetadataReadback:    true,
		SupportsPayloadHashMetadata: true,
		SupportsUpdateIfOwned:       true,
		SupportsDeleteIfOwned:       true,
		SupportsSecretPath:          true,
		MaxPayloadBytes:             1024 * 1024,
	}
}

func (*providerMutationTestProvider) ValidateConfig(context.Context, providers.DestinationConfig) error {
	return nil
}

func (p *providerMutationTestProvider) OpenDestination(
	ctx context.Context,
	_ providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	if p.openDeadline != nil {
		p.openDeadline <- observeDeadline(ctx)
	}
	return providerMutationTestRuntime{provider: p}, nil
}

type providerMutationTestRuntime struct {
	provider *providerMutationTestProvider
}

func (providerMutationTestRuntime) Health(context.Context) (*providers.HealthResult, error) {
	return &providers.HealthResult{Healthy: true}, nil
}

func (providerMutationTestRuntime) Plan(context.Context, providers.PlanRequest) (*providers.PlanResult, error) {
	return &providers.PlanResult{Action: providers.PlanActionCreate}, nil
}

func (r providerMutationTestRuntime) Upsert(
	ctx context.Context,
	_ providers.UpsertRequest,
) (*providers.SyncResult, error) {
	if r.provider.upsertDeadline != nil {
		r.provider.upsertDeadline <- observeDeadline(ctx)
	}
	if r.provider.upsertErr != nil {
		return nil, r.provider.upsertErr
	}
	return &providers.SyncResult{RemoteVersion: r.provider.providerVersion}, nil
}

func (r providerMutationTestRuntime) Delete(
	ctx context.Context,
	_ providers.DeleteRequest,
) (*providers.SyncResult, error) {
	if r.provider.deleteDeadline != nil {
		r.provider.deleteDeadline <- observeDeadline(ctx)
	}
	return &providers.SyncResult{RemoteVersion: "deleted"}, nil
}

func (providerMutationTestRuntime) ReadState(
	context.Context,
	providers.ReadStateRequest,
) (*providers.RemoteState, error) {
	return &providers.RemoteState{Exists: true, OwnershipKnown: true, Owned: true}, nil
}

func (providerMutationTestRuntime) Close(context.Context) error {
	return nil
}
