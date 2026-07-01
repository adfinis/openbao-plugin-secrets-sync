// Package observability contains low-cardinality OpenTelemetry instrumentation.
package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	instrumentationName = "github.com/adfinis/openbao-secret-sync"

	MetricQueueDepth              = "openbao.secret_sync.queue.depth"
	MetricOperations              = "openbao.secret_sync.operations"
	MetricProviderRequests        = "openbao.secret_sync.provider.requests"
	MetricProviderRequestDuration = "openbao.secret_sync.provider.request.duration"
	MetricReadinessChecks         = "openbao.secret_sync.readiness.checks"
	MetricRemoteMutationBlocked   = "openbao.secret_sync.remote_mutation.blocked"
	MetricReconcileRuns           = "openbao.secret_sync.reconcile.runs"
	MetricQueueCapacity           = "openbao.secret_sync.queue.capacity"
	MetricQueueUtilization        = "openbao.secret_sync.queue.utilization"
	MetricRestoreGuardActive      = "openbao.secret_sync.restore_guard.active"

	AttributeProvider        = "provider"
	AttributeDestinationType = "destination_type"
	AttributeOperation       = "operation"
	AttributeState           = "state"
	AttributeResult          = "result"
	AttributeErrorClass      = "error_class"
	AttributeGranularity     = "granularity"
	AttributeCheck           = "check"
	AttributeBlocker         = "blocker"
	AttributeReason          = "reason"

	ValueNone    = "none"
	ValueUnknown = "unknown"

	ResultSuccess = "success"
	ResultRetry   = "retry"
	ResultFailure = "failure"
	ResultSkipped = "skipped"

	OperationUpsert    = "upsert"
	OperationDelete    = "delete"
	OperationReadState = "read_state"
	OperationPlan      = "plan"
	OperationValidate  = "validate"
	OperationHealth    = "health"
	OperationDrain     = "drain"
	OperationPeriodic  = "periodic"

	CheckSource      = "source"
	CheckDestination = "destination"

	ReasonDisabled         = "disabled"
	ReasonRestoreGuard     = "restore_guard"
	ReasonReplicationState = "replication_state"
)

// Recorder captures the plugin's metric surface.
type Recorder interface {
	QueueDepth(context.Context, string, int)
	Operation(context.Context, OperationEvent)
	ProviderRequest(context.Context, ProviderRequestEvent)
	ReadinessCheck(context.Context, ReadinessCheckEvent)
	RemoteMutationBlocked(context.Context, RemoteMutationBlockedEvent)
	ReconcileRun(context.Context, ReconcileRunEvent)
	QueueCapacity(context.Context, QueueCapacityEvent)
	RestoreGuardActive(context.Context, bool)
}

// OperationEvent describes one logical sync engine operation result.
type OperationEvent struct {
	Operation       string
	Result          string
	ErrorClass      string
	DestinationType string
	Granularity     string
}

// ProviderRequestEvent describes one destination provider request.
type ProviderRequestEvent struct {
	Provider   string
	Operation  string
	Result     string
	ErrorClass string
	Duration   time.Duration
}

// ReadinessCheckEvent describes one source or destination readiness check.
type ReadinessCheckEvent struct {
	Check           string
	Result          string
	Blocker         string
	DestinationType string
}

// RemoteMutationBlockedEvent describes a blocked remote mutation attempt.
type RemoteMutationBlockedEvent struct {
	Operation string
	Reason    string
}

// ReconcileRunEvent describes one reconcile object result.
type ReconcileRunEvent struct {
	Result          string
	ErrorClass      string
	DestinationType string
	Granularity     string
}

// QueueCapacityEvent describes current durable queue capacity pressure.
type QueueCapacityEvent struct {
	Capacity    int
	Utilization float64
}

// New returns the default OpenTelemetry recorder. It is safe when no OTel SDK
// provider has been installed; the global OTel meter is no-op by default.
func New() Recorder {
	recorder, err := NewWithMeter(otel.Meter(instrumentationName))
	if err != nil {
		return Noop{}
	}
	return recorder
}

// NewWithMeter creates a recorder from an explicit meter, primarily for tests.
func NewWithMeter(meter metric.Meter) (Recorder, error) {
	queueDepth, err := meter.Int64Gauge(
		MetricQueueDepth,
		metric.WithDescription("Durable secret sync outbox depth by state."),
	)
	if err != nil {
		return nil, err
	}
	operationsTotal, err := meter.Int64Counter(
		MetricOperations,
		metric.WithDescription("Logical secret sync operation results."),
	)
	if err != nil {
		return nil, err
	}
	providerRequestsTotal, err := meter.Int64Counter(
		MetricProviderRequests,
		metric.WithDescription("Destination provider request results."),
	)
	if err != nil {
		return nil, err
	}
	providerRequestSeconds, err := meter.Float64Histogram(
		MetricProviderRequestDuration,
		metric.WithDescription("Destination provider request duration."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	readinessChecksTotal, err := meter.Int64Counter(
		MetricReadinessChecks,
		metric.WithDescription("Source and destination readiness check results."),
	)
	if err != nil {
		return nil, err
	}
	remoteMutationBlockedTotal, err := meter.Int64Counter(
		MetricRemoteMutationBlocked,
		metric.WithDescription("Remote mutation attempts blocked by safety gates."),
	)
	if err != nil {
		return nil, err
	}
	reconcileRunsTotal, err := meter.Int64Counter(
		MetricReconcileRuns,
		metric.WithDescription("Reconcile object results."),
	)
	if err != nil {
		return nil, err
	}
	restoreGuardActive, err := meter.Int64Gauge(
		MetricRestoreGuardActive,
		metric.WithDescription("Whether restore guard currently blocks remote mutation."),
	)
	if err != nil {
		return nil, err
	}
	queueCapacity, err := meter.Int64Gauge(
		MetricQueueCapacity,
		metric.WithDescription("Configured durable outbox queue capacity."),
	)
	if err != nil {
		return nil, err
	}
	queueUtilization, err := meter.Float64Gauge(
		MetricQueueUtilization,
		metric.WithDescription("Durable queued operation depth divided by configured queue capacity."),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, err
	}
	return meterRecorder{
		queueDepth:                 queueDepth,
		operationsTotal:            operationsTotal,
		providerRequestsTotal:      providerRequestsTotal,
		providerRequestSeconds:     providerRequestSeconds,
		readinessChecksTotal:       readinessChecksTotal,
		remoteMutationBlockedTotal: remoteMutationBlockedTotal,
		reconcileRunsTotal:         reconcileRunsTotal,
		queueCapacity:              queueCapacity,
		queueUtilization:           queueUtilization,
		restoreGuardActive:         restoreGuardActive,
	}, nil
}

// Noop records no metrics.
type Noop struct{}

func (Noop) QueueDepth(context.Context, string, int)               {}
func (Noop) Operation(context.Context, OperationEvent)             {}
func (Noop) ProviderRequest(context.Context, ProviderRequestEvent) {}
func (Noop) ReadinessCheck(context.Context, ReadinessCheckEvent)   {}
func (Noop) RemoteMutationBlocked(context.Context, RemoteMutationBlockedEvent) {
}
func (Noop) ReconcileRun(context.Context, ReconcileRunEvent)   {}
func (Noop) QueueCapacity(context.Context, QueueCapacityEvent) {}
func (Noop) RestoreGuardActive(context.Context, bool)          {}

type meterRecorder struct {
	queueDepth                 metric.Int64Gauge
	operationsTotal            metric.Int64Counter
	providerRequestsTotal      metric.Int64Counter
	providerRequestSeconds     metric.Float64Histogram
	readinessChecksTotal       metric.Int64Counter
	remoteMutationBlockedTotal metric.Int64Counter
	reconcileRunsTotal         metric.Int64Counter
	queueCapacity              metric.Int64Gauge
	queueUtilization           metric.Float64Gauge
	restoreGuardActive         metric.Int64Gauge
}

func (r meterRecorder) QueueDepth(ctx context.Context, state string, depth int) {
	r.queueDepth.Record(ctx, int64(depth), metric.WithAttributes(
		attribute.String(AttributeState, normalize(state)),
	))
}

func (r meterRecorder) Operation(ctx context.Context, event OperationEvent) {
	r.operationsTotal.Add(ctx, 1, metric.WithAttributes(operationAttributes(event)...))
}

func (r meterRecorder) ProviderRequest(ctx context.Context, event ProviderRequestEvent) {
	attributes := providerRequestAttributes(event)
	r.providerRequestsTotal.Add(ctx, 1, metric.WithAttributes(attributes...))
	r.providerRequestSeconds.Record(ctx, event.Duration.Seconds(), metric.WithAttributes(attributes...))
}

func (r meterRecorder) ReadinessCheck(ctx context.Context, event ReadinessCheckEvent) {
	r.readinessChecksTotal.Add(ctx, 1, metric.WithAttributes(readinessCheckAttributes(event)...))
}

func (r meterRecorder) RemoteMutationBlocked(ctx context.Context, event RemoteMutationBlockedEvent) {
	r.remoteMutationBlockedTotal.Add(ctx, 1, metric.WithAttributes(remoteMutationBlockedAttributes(event)...))
}

func (r meterRecorder) ReconcileRun(ctx context.Context, event ReconcileRunEvent) {
	r.reconcileRunsTotal.Add(ctx, 1, metric.WithAttributes(reconcileRunAttributes(event)...))
}

func (r meterRecorder) QueueCapacity(ctx context.Context, event QueueCapacityEvent) {
	r.queueCapacity.Record(ctx, int64(event.Capacity))
	r.queueUtilization.Record(ctx, event.Utilization)
}

func (r meterRecorder) RestoreGuardActive(ctx context.Context, active bool) {
	value := int64(0)
	if active {
		value = 1
	}
	r.restoreGuardActive.Record(ctx, value)
}

func operationAttributes(event OperationEvent) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(AttributeOperation, normalize(event.Operation)),
		attribute.String(AttributeResult, normalize(event.Result)),
		attribute.String(AttributeErrorClass, normalizeNone(event.ErrorClass)),
		attribute.String(AttributeDestinationType, normalize(event.DestinationType)),
		attribute.String(AttributeGranularity, normalize(event.Granularity)),
	}
}

func providerRequestAttributes(event ProviderRequestEvent) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(AttributeProvider, normalize(event.Provider)),
		attribute.String(AttributeOperation, normalize(event.Operation)),
		attribute.String(AttributeResult, normalize(event.Result)),
		attribute.String(AttributeErrorClass, normalizeNone(event.ErrorClass)),
	}
}

func readinessCheckAttributes(event ReadinessCheckEvent) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(AttributeCheck, normalize(event.Check)),
		attribute.String(AttributeResult, normalize(event.Result)),
		attribute.String(AttributeBlocker, normalizeNone(event.Blocker)),
		attribute.String(AttributeDestinationType, normalize(event.DestinationType)),
	}
}

func remoteMutationBlockedAttributes(event RemoteMutationBlockedEvent) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(AttributeOperation, normalize(event.Operation)),
		attribute.String(AttributeReason, normalize(event.Reason)),
	}
}

func reconcileRunAttributes(event ReconcileRunEvent) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(AttributeResult, normalize(event.Result)),
		attribute.String(AttributeErrorClass, normalizeNone(event.ErrorClass)),
		attribute.String(AttributeDestinationType, normalize(event.DestinationType)),
		attribute.String(AttributeGranularity, normalize(event.Granularity)),
	}
}

func normalize(value string) string {
	if value == "" {
		return ValueUnknown
	}
	return value
}

func normalizeNone(value string) string {
	if value == "" {
		return ValueNone
	}
	return value
}
