# Observability


The first observability slice instruments the plugin with OpenTelemetry metric
API calls behind `internal/observability`. It intentionally does not configure
an exporter, collector endpoint, or credentials. The OpenTelemetry SDK/exporter
boundary remains a deployment concern.

## Metric Surface

Implemented OpenTelemetry instrument names:

```text
openbao.secret_sync.queue.depth
openbao.secret_sync.queue.capacity
openbao.secret_sync.queue.utilization
openbao.secret_sync.operations
openbao.secret_sync.provider.requests
openbao.secret_sync.provider.request.duration
openbao.secret_sync.readiness.checks
openbao.secret_sync.remote_mutation.blocked
openbao.secret_sync.reconcile.runs
openbao.secret_sync.restore_guard.active
```

The duration histogram uses seconds as its unit. Prometheus exporters may expose
these names in Prometheus form, for example
`openbao_secret_sync_provider_request_duration_seconds` and counter `_total`
series, but that is an exporter transformation rather than the plugin's
instrument contract.

Current instrumentation points:

- queue summary reads and drains record queue depth by durable outbox state,
  configured capacity, and capacity utilization;
- queue drain records a logical drain operation result;
- blocked periodic runs and manual drains record the safety gate that blocked
  remote mutation;
- dispatch records logical upsert/delete operation outcomes;
- destination readiness checks record a low-cardinality source or destination
  check result and primary blocker;
- provider validate, health, plan, upsert, delete, and read-state calls record
  request counts and durations;
- reconcile plan/apply records one result per reconciled object;
- config read/write and restore-guard acknowledgement record restore guard
  active state.

## Attribute Policy

Allowed metric attributes:

```text
provider
destination_type
operation
state
result
error_class
granularity
check
blocker
reason
```

Forbidden attributes:

```text
path
source_path
resolved_name
remote_name
destination_name
association_id
operation_id
payload_sha256
remote_version
aws_arn
account_id
```

The first implementation has unit tests that validate generated metric
attributes against this policy. Status and API responses may still expose
operator-facing metadata such as paths and payload hashes; this policy is
specific to telemetry labels because telemetry is usually aggregated and
exported outside the OpenBao trust boundary.

## Exporter Boundary

The plugin currently uses the global OpenTelemetry meter. Without an installed
OpenTelemetry SDK meter provider, these instruments are no-op. Future exporter
work should prefer standard OpenTelemetry environment variables such as
`OTEL_EXPORTER_OTLP_*` and avoid storing exporter credentials in plugin storage.
