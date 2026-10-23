---
verifiedBy:
  - internal/backend/path_config.go
  - internal/backend/path_queue.go
  - internal/backend/path_status.go
  - internal/backend/path_sources.go
  - internal/backend/path_destinations.go
  - internal/backend/path_reconcile.go
  - internal/observability/observability.go
---

# Monitor sync

Poll the Secret Sync API to watch for paused mounts, queue pressure, and
objects that are not converging. The preview exports no plugin metrics, so
these responses are the operational signal.

## What to poll

| Path | Watch | Act when |
| --- | --- | --- |
| `secret-sync/config` | `disabled`, `restore_guard` | Either stays `true` longer than a planned pause or a [restore review](restore-and-clone.md). |
| `secret-sync/queue` | `utilization`, `pending`, `retry_wait`, `terminal`, `oldest_age_seconds` | `utilization` stays high or `oldest_age_seconds` keeps growing; `terminal` increases. |
| `secret-sync/status/<path>` | Top-level `state` and each object's `state` and `last_error_class` | `state` is not `SYNCED` after the expected dispatch delay. |
| `secret-sync/sources/<path>/check` | `ready`, `blockers` | `ready` is `false`. |
| `secret-sync/destinations/<type>/<name>/check` | `ready`, `valid`, `healthy`, `blockers` | `ready` is `false`. |
| `secret-sync/reconcile/<path>/plan` | `state` and each object's `verification` | `state` differs from the stored status, or `verification` is weaker than you expect. |

`utilization` is the queued depth (`pending` plus `retry_wait`) divided by
`capacity`. With `capacity` `0`, any queued work reports `1`. Route a
non-`SYNCED` state with [Route by status](troubleshoot.md#route-by-status).

Destination checks and reconcile plans call the provider on every request.
Poll them less often than `config`, `queue`, and `status`, and account for
provider rate limits.

## Scope monitoring credentials

Give the polling token read access to the paths above and nothing else. Read
access does not let it drain the queue, apply reconcile, or enable sources,
because those endpoints require write. Do not grant read on
`secret-sync/data/*`, which returns source secret values. This policy fragment
covers the table above:

```hcl
path "secret-sync/config" {
  capabilities = ["read"]
}

path "secret-sync/queue" {
  capabilities = ["read"]
}

path "secret-sync/status/*" {
  capabilities = ["read"]
}

path "secret-sync/sources/*" {
  capabilities = ["read"]
}

path "secret-sync/destinations/+/+/check" {
  capabilities = ["read"]
}

path "secret-sync/reconcile/*" {
  capabilities = ["read"]
}
```

For a broader read-only role, see the
[auditor policy](../security/policies.md#auditor).

## Metrics

The preview does not export plugin metrics. The plugin records measurements
through the OpenTelemetry metric API but installs no SDK meter provider or
exporter, so nothing leaves the plugin process. OpenBao telemetry configuration
and `OTEL_EXPORTER_OTLP_*` environment variables do not change this. Upstream
[OpenBao issue #2973](https://github.com/openbao/openbao/issues/2973) tracks
metrics support for external plugins.
[Observability internals](../development/observability.md) lists the internal
instruments and their attribute policy.
