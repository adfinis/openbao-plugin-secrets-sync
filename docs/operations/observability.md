# Observability


Secret Sync emits OpenTelemetry metric API calls through
`internal/observability`. The plugin does not configure an exporter, collector
endpoint, or exporter credentials. Configure the OpenTelemetry SDK and exporter
in the OpenBao deployment.

## Metric surface

OpenTelemetry instrument names:

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

Instrumentation points:

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

## Attribute policy

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

Unit tests validate generated metric attributes against this policy. Status and
API responses may expose operator-facing metadata such as paths and remote
names, but not payload hashes. This policy is stricter for telemetry labels
because telemetry is usually aggregated and exported outside the OpenBao trust
boundary.

## Exporter boundary

The plugin uses the global OpenTelemetry meter. Without an installed
OpenTelemetry SDK meter provider, these instruments are no-op. Prefer standard
OpenTelemetry environment variables such as `OTEL_EXPORTER_OTLP_*` for exporter
configuration. Do not store exporter credentials in plugin storage.
