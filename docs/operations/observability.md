# Observability

Secret Sync emits low-cardinality OpenTelemetry metric API calls through
`internal/observability`. The plugin does not configure an exporter, collector
endpoint, or exporter credentials. Configure the OpenTelemetry SDK and exporter
in the OpenBao deployment.

Use [Convergence](../concepts/convergence.md) for queue and dispatch semantics,
[Reconcile and drift](../concepts/reconcile-and-drift.md) for read-state and
drift semantics, and the [operator runbook](operator-runbook.md) for
troubleshooting commands.

## Metric Instruments

OpenTelemetry instrument names are the plugin contract. Prometheus exporters
may transform them into Prometheus names such as
`openbao_secret_sync_provider_requests_total` or
`openbao_secret_sync_provider_request_duration_seconds`, but those suffixes are
exporter output, not the in-plugin instrument names.

| Instrument | Kind | Attributes | Notes |
| --- | --- | --- | --- |
| `openbao.secret_sync.queue.depth` | `Int64Gauge` | `state` | Durable outbox depth by queue state. |
| `openbao.secret_sync.queue.capacity` | `Int64Gauge` | none | Configured queue capacity. |
| `openbao.secret_sync.queue.utilization` | `Float64Gauge` | none | Queued depth divided by capacity, unit `1`. |
| `openbao.secret_sync.operations` | `Int64Counter` | `operation`, `result`, `error_class`, `destination_type`, `granularity` | Logical sync engine operations. |
| `openbao.secret_sync.provider.requests` | `Int64Counter` | `provider`, `operation`, `result`, `error_class` | Provider calls. |
| `openbao.secret_sync.provider.request.duration` | `Float64Histogram` | `provider`, `operation`, `result`, `error_class` | Provider request duration, unit `s`. |
| `openbao.secret_sync.readiness.checks` | `Int64Counter` | `check`, `result`, `blocker`, `destination_type` | Source and destination readiness checks. |
| `openbao.secret_sync.remote_mutation.blocked` | `Int64Counter` | `operation`, `reason` | Safety gates blocking remote mutation. |
| `openbao.secret_sync.reconcile.runs` | `Int64Counter` | `result`, `error_class`, `destination_type`, `granularity` | Manual reconcile and background drift-detect object results. |
| `openbao.secret_sync.drift.repairs` | `Int64Counter` | `result`, `error_class`, `destination_type`, `granularity` | Background drift-repair operation results. |
| `openbao.secret_sync.restore_guard.active` | `Int64Gauge` | none | `1` when restore guard blocks remote mutation, otherwise `0`. |

Blank error-class and blocker values are normalized to `none`. Other blank
attribute values are normalized to `unknown`.

## Recording Points

Queue summary reads and queue drains record queue depth, capacity, and
utilization.

Dispatch records logical upsert and delete outcomes. Queue drain and
event-triggered dispatch record separate logical operation outcomes; empty
event-dispatch wakeups are reported as `result=skipped`.

Remote mutation blocked metrics are recorded when periodic work,
event-triggered dispatch, background drift repair enqueue, or manual drains hit
safety gates such as `disabled`, `restore_guard`, `replication_state`, or
`capacity`.

Provider validate, health, plan, upsert, delete, and read-state calls record
request counts and durations.

Reconcile plan, reconcile apply, and background drift detection record one
`reconcile.runs` result per reconciled object. Background drift repair records
one `drift.repairs` result per repair operation.

Config read/write and restore-guard acknowledgement record the current
`restore_guard.active` gauge value.

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

Unit tests validate generated metric attributes against this policy. Status,
plan, reconcile, and queue responses may expose operator-facing identifiers
such as source paths, destination names, operation IDs, and remote object names.
Telemetry labels stay stricter because metrics are usually aggregated and
exported outside the OpenBao trust boundary.

## Operational Signals

Useful starting points:

- `openbao.secret_sync.restore_guard.active` is last recorded as `1` after the
  expected restore or clone review window.
- `openbao.secret_sync.queue.utilization` remains high while
  `openbao.secret_sync.queue.depth{state="pending"}` is not draining.
- `openbao.secret_sync.remote_mutation.blocked` increases with
  `reason=disabled`, `reason=restore_guard`, `reason=replication_state`, or
  `reason=capacity`.
- `openbao.secret_sync.provider.requests` failures increase for a provider,
  operation, or error class.
- `openbao.secret_sync.provider.request.duration` increases for provider
  health, plan, upsert, delete, or read-state calls.
- `openbao.secret_sync.reconcile.runs` reports reconcile success, skipped, or
  failure counts by destination type, granularity, and error class.
- `openbao.secret_sync.drift.repairs` reports repeated failure or retry for
  background repair work.
- `openbao.secret_sync.readiness.checks` failures identify source or
  destination onboarding blockers.

Use these signals to choose the next inspection path. Metrics do not include
source paths or remote names, so move from a metric symptom to `queue`,
`status/<path>`, `reconcile/<path>/plan`, or destination health checks when you
need object-level context.

## Exporter Boundary

The plugin uses the global OpenTelemetry meter. Without an installed
OpenTelemetry SDK meter provider, these instruments are no-op. Prefer standard
OpenTelemetry environment variables such as `OTEL_EXPORTER_OTLP_*` for exporter
configuration.

Do not store exporter credentials in plugin storage. Keep exporter credentials,
collector endpoints, sampling, and retention policy in the OpenBao deployment
or platform observability stack.
