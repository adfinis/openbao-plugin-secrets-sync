# API surface

This page describes the Secret Sync API groups exposed under the plugin mount.
Examples assume the plugin is mounted at `secret-sync/`.

The API is organized around local source data, destination configuration,
associations, durable queue work, status, and reconcile. Remote destination
mutation is asynchronous. Event-triggered dispatch wakes due queue work after
enqueue, and operators can explicitly drain due work with `queue/drain`.

## Contract rules

- Source payload values are returned only by source data read paths.
- Destination sensitive fields are redacted on destination reads.
- Plan, queue, status, reconcile, metrics, and logs do not expose source
  payload values.
- Destination mutation requires destination authority, an enabled association,
  queue capacity, and an allowed OpenBao replication state. When
  `require_source_opt_in=true`, it also requires source eligibility metadata.
  When `delegated_mode=true`, it also requires constrained destinations.
- The mount-wide `disabled` flag blocks background provider traffic and remote
  mutation. Manual reconcile remains available.
- Restore guard blocks remote mutation. Background drift detection and manual
  reconcile remain read-only and may refresh local status while the guard is
  active.
- Provider failures use stable error classes such as `authn`, `authz`,
  `rate_limit`, `unavailable`, `ownership`, `collision`, and `drift`.
- Operator-facing blocked or terminal states include a human-readable `hint` and
  may include structured `next_actions`. On successful responses these fields
  are top-level response data fields. On OpenBao error responses they are nested
  under `data` so the response remains an OpenBao error.

## Config and restore guard

| Path | Purpose |
| --- | --- |
| `info` | Read static plugin version, association defaults, and provider capability flags. |
| `config` | Read or update mount-wide sync settings for pause, queue capacity, source opt-in, delegated mode, drift work, and event dispatch. |
| `config/restore-guard/acknowledge` | Acknowledge restore or clone review and resume remote mutation. |

`info` is the stable place for clients and operators to discover static
association defaults and registered provider capabilities. Association create,
plan, read, and lifecycle responses return effective association values without
repeating the static defaults object.

`drift_repair` controls opt-in background drift work:

- `off` disables background drift work. This is the default.
- `detect` periodically runs read-only reconcile for due association objects
  and records status drift.
- `repair` does the same detection and enqueues owned `DRIFTED` objects for
  normal outbox upsert repair.

Background drift sweeps use `drift_reconcile_interval` (`1h` default, minimum
`1m`) and `drift_reconcile_batch` (`16` default) to limit provider read load.
Repair never mutates providers directly; it creates queue operations with
`trigger=drift-repair`.

`event_dispatch_enabled` defaults to `true`. Enqueue-producing requests wake a
coalesced dispatcher after durable queue commit, bounded by
`event_dispatch_max_operations` (`16` default). This reduces normal sync latency
without changing the asynchronous API contract; periodic processing remains the
fallback for missed wakeups, retries, and restart recovery.

`delegated_mode` defaults to `false` for platform-operated mounts. When set to
`true`, it requires `require_source_opt_in=true` and rejects association use of
destinations whose `allowed_source_path_prefixes` or
`allowed_resolved_name_prefixes` are empty. Destination checks report
`destination_unconstrained` for that blocker.

## Source data and metadata

| Path | Purpose |
| --- | --- |
| `data/<path>` | Write, read, or soft-delete local source secret data. |
| `metadata/<path>` | Manage source version policy and custom metadata. |
| `metadata/` and `metadata/<path>` with `LIST` | List source metadata keys. |
| `sources/<path>/enable` | Mark a source path as syncable by setting source metadata. |
| `sources/<path>/check` | Check source readiness for sync. |
| `delete/<path>` | Soft-delete selected source versions. |
| `undelete/<path>` | Undelete selected source versions. |
| `destroy/<path>` | Permanently destroy selected source versions. |

Each source write creates a new version. Writes that produce sync work reserve
queue capacity before accepting the new source version.
Source writes accept either a KV-v2-like wrapped body:

```json
{
  "data": {
    "username": "app",
    "password": "initial"
  },
  "options": {
    "cas": 1
  }
}
```

or CLI shorthand where top-level fields become source payload keys:

```sh
bao write secret-sync/data/app/db username=app password=initial cas=1
```

In shorthand mode, `data`, `options`, `cas`, and `version` are reserved field
names. Use the wrapped body when the source payload needs one of those literal
top-level keys.
Mounts default `require_source_opt_in=false` and `delegated_mode=false`. When
strict opt-in is enabled, `sources/<path>/enable` sets
`custom_metadata.syncable=true` and source checks report `source_not_syncable`
until that metadata is present.
Source paths cannot end with reserved association route segments such as
`plan`, `disable`, `enable`, or `sync`.

## Destinations

| Path | Purpose |
| --- | --- |
| `destinations/<type>` with `LIST` | List destination names for a provider type. |
| `destinations/<type>/<name>` | Create, update, read, or delete a destination. |
| `destinations/<type>/<name>/check` | Check destination readiness, including static config and runtime reachability. |
| `destinations/<type>/<name>/validate` | Validate static destination configuration. |
| `destinations/<type>/<name>/health` | Check runtime destination reachability and authorization. |

Destination config is split into public fields and seal-wrapped sensitive
fields. Reads return redacted sensitive values.

## Associations

Primary routes:

| Path | Purpose |
| --- | --- |
| `associations` with `LIST` | List association source paths. |
| `associations/<path>` | Create, update, or read associations for a source path. |
| `associations/<path>/plan` | Plan association behavior without storing the association or mutating the destination. |
| `associations/<path>/disable` | Disable one association resolved by `destination=<type>/<name>` and cancel eligible queued work. |
| `associations/<path>/enable` | Enable one association resolved by `destination=<type>/<name>` and enqueue current source work where needed. |
| `associations/<path>/sync` | Manually enqueue sync work for one association resolved by `destination=<type>/<name>`. |

ID-addressed routes:

| Path | Purpose |
| --- | --- |
| `associations/<path>/<association-id>` | Read or delete one association exactly. |
| `associations/<path>/<association-id>/disable` | Disable one association and cancel eligible queued work. |
| `associations/<path>/<association-id>/enable` | Enable one association and enqueue current source work where needed. |
| `associations/<path>/<association-id>/sync` | Manually enqueue sync work for one association. |

Associations link a source path to one destination and define the remote name,
granularity, payload format, data mapping, delete mode, and enabled state.
Association creation and updates validate provider capabilities before they are
accepted.
Association create, update, plan, and primary lifecycle requests use
`destination=<type>/<name>` to identify the destination. Association IDs remain
stable response identifiers and exact-addressing escape hatches.
Updates that resolve exactly one existing association may change non-identity
fields in place. Changes to `granularity` or the remote-name reservation
(`resolved_name` for `secret-path`; rendered name pattern and current concrete
rendered names for `secret-key`) require an explicit new association plus
deletion of the old one.
Updating an already-enabled association does not enqueue sync work. The response
returns `sync_operation_ids=[]` with a `hint` and `next_actions` pointing to
`associations/<path>/sync` when an operator wants to push or retry the current
source version.
Association activation and source writes reject secret-key configurations whose
rendered names would overlap another association for the same destination.
Read `info` to discover static association defaults and provider capability
flags.

## Queue, status, and reconcile

| Path | Purpose |
| --- | --- |
| `queue` | Read durable queue depth, capacity, utilization, and state counts. |
| `queue/drain` | Drain due queue work for deterministic testing or controlled catch-up. |
| `queue/<operation-id>` | Read one queued operation. |
| `queue/<operation-id>/retry` | Retry one failed or canceled operation. |
| `queue/<operation-id>/cancel` | Cancel one queued operation. |
| `status/<path>` | Read per-source sync status. |
| `reconcile/<path>/plan` | Read provider remote state and calculate local status without changing status or destination secrets. |
| `reconcile/<path>` | Apply reconcile by refreshing local status from provider read-state results. |

Event-triggered dispatch normally wakes due queue work after enqueue and when
retry-wait work becomes due.
`queue/drain` can execute remote mutations and is operator-scoped for
deterministic testing or controlled catch-up. Reconcile reads remote state but
does not write destination secrets.
Queue operation reads include `trigger`, which is `user` for ordinary writes
and manual syncs and `drift-repair` for background repair work.
Status objects can include `verification`, `last_reconcile_time`,
`last_drift_detected_time`, `last_repair_time`, and `repair_count`.
Status and reconcile objects include `hint` and `next_actions` for actionable
states such as `REMOTE_MISSING`, `REMOTE_OWNERSHIP_LOST`, `DRIFTED`,
`VALIDATION_ERROR`, `QUEUE_BLOCKED`, destination failures, and `DISABLED`.

Use [Convergence](../concepts/convergence.md) for queue/status semantics and
[Reconcile and drift](../concepts/reconcile-and-drift.md) for provider
read-state and background drift semantics.

## List pagination

Public `LIST` endpoints accept OpenBao paginated-list parameters and pass them
through to storage:

- `after`: optional key to begin listing after; the key does not need to exist
  in the result set;
- `limit`: optional maximum number of keys to return; non-positive values
  return all keys.

This applies to `metadata`, `metadata/<path>`, `destinations/<type>`, and
`associations`.
