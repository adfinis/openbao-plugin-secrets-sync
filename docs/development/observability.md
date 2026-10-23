---
verifiedBy:
  - internal/observability/observability.go
  - internal/observability/observability_test.go
  - internal/backend/metrics.go
  - internal/backend/backend.go
  - internal/backend/dispatcher.go
  - internal/backend/event_dispatch.go
  - internal/backend/periodic_reconcile.go
  - internal/backend/path_queue.go
  - internal/backend/path_reconcile.go
  - internal/backend/path_config.go
  - internal/backend/path_destinations.go
  - internal/backend/path_sources.go
  - internal/backend/association_plan.go
  - go.mod
---

# Observability internals

The preview exports no plugin metrics. The plugin calls the OpenTelemetry
metric API through `internal/observability`, but it installs no SDK meter
provider or exporter. `observability.New` uses the process-global meter, which
records nothing in the standalone plugin process. OpenBao telemetry
configuration and `OTEL_EXPORTER_OTLP_*` environment variables do not install
an SDK inside the plugin process, and OpenBao core metrics do not include these
instruments.

The instruments below define the internal recorder and its tested attribute
policy. Treat them as an implementation contract for a future exporter, not as
time series an operator can scrape. For what operators poll today, see
[Monitor sync](../operate/monitor.md).

## Instruments

| Instrument | Kind | Unit | Attributes | Records |
| --- | --- | --- | --- | --- |
| `openbao.secret_sync.queue.depth` | `Int64Gauge` | none | `state` | Durable outbox depth for `pending`, `retry_wait`, and `failed_terminal`. |
| `openbao.secret_sync.queue.capacity` | `Int64Gauge` | none | none | Configured queue capacity. |
| `openbao.secret_sync.queue.utilization` | `Float64Gauge` | `1` | none | Queued depth (`pending` plus `retry_wait`) divided by capacity. |
| `openbao.secret_sync.operations` | `Int64Counter` | none | `operation`, `result`, `error_class`, `destination_type`, `granularity` | Logical sync engine operations. |
| `openbao.secret_sync.provider.requests` | `Int64Counter` | none | `provider`, `operation`, `result`, `error_class` | Provider calls. |
| `openbao.secret_sync.provider.request.duration` | `Float64Histogram` | `s` | `provider`, `operation`, `result`, `error_class` | Provider call duration. |
| `openbao.secret_sync.readiness.checks` | `Int64Counter` | none | `check`, `result`, `blocker`, `destination_type` | Source and destination readiness checks. |
| `openbao.secret_sync.remote_mutation.blocked` | `Int64Counter` | none | `operation`, `reason` | Safety gates that blocked remote mutation. |
| `openbao.secret_sync.reconcile.runs` | `Int64Counter` | none | `result`, `error_class`, `destination_type`, `granularity` | Reconcile results, one per reconciled object. |
| `openbao.secret_sync.drift.repairs` | `Int64Counter` | none | `result`, `error_class`, `destination_type`, `granularity` | Background drift repair operation results. |
| `openbao.secret_sync.restore_guard.active` | `Int64Gauge` | none | none | `1` while restore guard blocks remote mutation, otherwise `0`. |

The `result` attribute takes `success`, `retry`, `failure`, or `skipped`. Blank
`error_class` and `blocker` values are normalized to `none`. Other blank
attribute values are normalized to `unknown`.

Exporter-specific names, such as Prometheus `_total` or `_seconds` suffixes,
would be exporter output. They are not the in-plugin instrument names.

## Recording points

| Code path | Instruments | Notes |
| --- | --- | --- |
| Queue summary read and queue drain | `queue.depth`, `queue.capacity`, `queue.utilization` | Recorded after the summary is computed. |
| Dispatch of an upsert or delete operation | `operations` | `operation=upsert` or `delete`. `result=retry` when the error class is automatically retried and attempts remain, otherwise `failure`. |
| Queue drain | `operations` | One `operation=drain`, `result=success` event per completed drain. |
| Event-triggered dispatch | `operations` | `operation=event_dispatch`. A wakeup that processes nothing records `result=skipped`. Dispatch errors record `result=failure`. |
| Periodic work, event-triggered dispatch, queue drain, and operation claim | `remote_mutation.blocked` | `operation` is `periodic`, `event_dispatch`, or `drain`. `reason` is `disabled`, `restore_guard`, or `replication_state`. |
| Background drift repair enqueue | `remote_mutation.blocked` | `operation=periodic` with `reason=restore_guard` or `reason=capacity`. |
| Destination write, `validate`, and `check` | `provider.requests`, `provider.request.duration` | `operation=validate`. |
| Destination `health` and `check` | `provider.requests`, `provider.request.duration` | `operation=health`. An unhealthy result counts as `failure`. |
| Association plan | `provider.requests`, `provider.request.duration` | `operation=plan`. |
| Dispatch | `provider.requests`, `provider.request.duration` | `operation=upsert` or `delete`. |
| Reconcile plan, reconcile apply, and background drift detection | `provider.requests`, `provider.request.duration`, `reconcile.runs` | `operation=read_state` for the provider call. `reconcile.runs` uses `success` for synced, `skipped` for disabled, and `failure` otherwise. |
| Dispatch of a drift-repair operation | `drift.repairs` | Recorded alongside the `operations` event with the same result. |
| `sources/<path>/check` | `readiness.checks` | `check=source`. `blocker` holds the first blocker only. |
| Destination `check` | `readiness.checks` | `check=destination`. `blocker` holds the first blocker only. |
| Config read, config write, and restore guard acknowledgement | `restore_guard.active` | Records the current restore guard value. |

Provider errors that do not carry a provider error class go through transport
classification, then fall back to `internal`. See
[Provider contract](provider-contract.md#error-classification).

## Attribute policy

Metric attributes are restricted to low-cardinality, non-identifying keys.
`TestMetricAttributesStayLowCardinality` in `internal/observability` fails on any key
outside the allowed list and on any forbidden key.

Allowed keys:

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

Forbidden keys:

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

Status, plan, reconcile, and queue responses can expose source paths,
destination names, operation IDs, and remote object names to callers that
OpenBao policy authorizes. Metric attributes stay stricter because metrics are
usually aggregated and exported outside the OpenBao trust boundary. When you
add an instrument or attribute, extend the policy test in the same change.

## Exporter boundary

Tests supply a meter through `observability.NewWithMeter` and assert
measurements and attributes in process. That seam is not an
operator-configurable export path, and the plugin has no bridge to OpenBao core
telemetry. Upstream
[OpenBao issue #2973](https://github.com/openbao/openbao/issues/2973) tracks
external-plugin metrics support.

An export integration must keep exporter credentials outside plugin storage and
must define its deployment and retention requirements in the same change. Alert
design belongs with that integration, not with the preview recorder.
