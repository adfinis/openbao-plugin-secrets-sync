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
	MetricReconcileRuns           = "openbao.secret_sync.reconcile.runs"
	MetricRestoreGuardActive      = "openbao.secret_sync.restore_guard.active"

	AttributeProvider        = "provider"
	AttributeDestinationType = "destination_type"
	AttributeOperation       = "operation"
	AttributeState           = "state"
	AttributeResult          = "result"
	AttributeErrorClass      = "error_class"
	AttributeGranularity     = "granularity"

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
	OperationDrain     = "drain"
)

// Recorder captures the plugin's metric surface.
type Recorder interface {
	QueueDepth(context.Context, string, int)
	Operation(context.Context, OperationEvent)
	ProviderRequest(context.Context, ProviderRequestEvent)
	ReconcileRun(context.Context, ReconcileRunEvent)
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

// ReconcileRunEvent describes one reconcile object result.
type ReconcileRunEvent struct {
	Result          string
	ErrorClass      string
	DestinationType string
	Granularity     string
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
	return meterRecorder{
		queueDepth:             queueDepth,
		operationsTotal:        operationsTotal,
		providerRequestsTotal:  providerRequestsTotal,
		providerRequestSeconds: providerRequestSeconds,
		reconcileRunsTotal:     reconcileRunsTotal,
		restoreGuardActive:     restoreGuardActive,
	}, nil
}

// Noop records no metrics.
type Noop struct{}

func (Noop) QueueDepth(context.Context, string, int)               {}
func (Noop) Operation(context.Context, OperationEvent)             {}
func (Noop) ProviderRequest(context.Context, ProviderRequestEvent) {}
func (Noop) ReconcileRun(context.Context, ReconcileRunEvent)       {}
func (Noop) RestoreGuardActive(context.Context, bool)              {}

type meterRecorder struct {
	queueDepth             metric.Int64Gauge
	operationsTotal        metric.Int64Counter
	providerRequestsTotal  metric.Int64Counter
	providerRequestSeconds metric.Float64Histogram
	reconcileRunsTotal     metric.Int64Counter
	restoreGuardActive     metric.Int64Gauge
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

func (r meterRecorder) ReconcileRun(ctx context.Context, event ReconcileRunEvent) {
	r.reconcileRunsTotal.Add(ctx, 1, metric.WithAttributes(reconcileRunAttributes(event)...))
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
