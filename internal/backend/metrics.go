package backend

import (
	"context"
	"time"

	"github.com/adfinis/openbao-secret-sync/internal/observability"
	"github.com/adfinis/openbao-secret-sync/internal/providers"
)

func (b *secretSyncBackend) recordReadinessCheck(
	ctx context.Context,
	check string,
	destinationType string,
	blockers []string,
) {
	result := observability.ResultSuccess
	blocker := ""
	if len(blockers) > 0 {
		result = observability.ResultFailure
		blocker = blockers[0]
	}
	b.observer.ReadinessCheck(ctx, observability.ReadinessCheckEvent{
		Check:           check,
		Result:          result,
		Blocker:         blocker,
		DestinationType: destinationType,
	})
}

func (b *secretSyncBackend) recordRemoteMutationBlocked(ctx context.Context, operation string, reason string) {
	b.observer.RemoteMutationBlocked(ctx, observability.RemoteMutationBlockedEvent{
		Operation: operation,
		Reason:    reason,
	})
}

func (b *secretSyncBackend) recordProviderHealthRequest(
	ctx context.Context,
	providerType string,
	result *providers.HealthResult,
	err error,
	duration time.Duration,
) {
	metricResult := observability.ResultSuccess
	errorClass := ""
	switch {
	case err != nil:
		metricResult = observability.ResultFailure
		errorClass = string(providerErrorClass(err))
	case result == nil:
		metricResult = observability.ResultFailure
		errorClass = string(providers.ErrorClassInternal)
	case !result.Healthy:
		metricResult = observability.ResultFailure
		errorClass = string(result.ErrorClass)
	}
	b.observer.ProviderRequest(ctx, observability.ProviderRequestEvent{
		Provider:   providerType,
		Operation:  observability.OperationHealth,
		Result:     metricResult,
		ErrorClass: errorClass,
		Duration:   duration,
	})
}
