# API surface

This page describes the Secret Sync API groups exposed under the plugin mount.
Examples assume the plugin is mounted at `secret-sync/`.

The API is organized around local source data, destination configuration,
associations, durable queue work, status, and reconcile. Remote destination
mutation is asynchronous unless an operator explicitly drains due queue work.

## Contract rules

- Source payload values are returned only by source data read paths.
- Destination sensitive fields are redacted on destination reads.
- Plan, queue, status, reconcile, metrics, and logs do not expose source
  payload values.
- Destination mutation requires source eligibility, destination authority, an
  enabled association, queue capacity, and an allowed OpenBao replication
  state.
- The mount-wide `disabled` flag and restore guard block remote mutation.
- Provider failures use stable error classes such as `authn`, `authz`,
  `rate_limit`, `unavailable`, `ownership`, `collision`, and `drift`.

## Config and restore guard

| Path | Purpose |
| --- | --- |
| `config` | Read or update mount-wide sync settings such as `disabled` and `queue_capacity`. |
| `config/restore-guard/acknowledge` | Acknowledge restore or clone review and resume remote mutation. |

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

| Path | Purpose |
| --- | --- |
| `associations` with `LIST` | List association source paths. |
| `associations/<path>` | Create, update, or read associations for a source path. |
| `associations/<path>/plan` | Plan association behavior without storing the association or mutating the destination. |
| `associations/<path>/<association-id>` | Delete one association. |
| `associations/<path>/<association-id>/disable` | Disable one association and cancel eligible queued work. |
| `associations/<path>/<association-id>/enable` | Enable one association and enqueue current source work where needed. |
| `associations/<path>/<association-id>/sync` | Manually enqueue sync work for one association. |

Associations link a source path to one destination and define the remote name,
granularity, payload format, data mapping, delete mode, and enabled state.
Association creation and updates validate provider capabilities before they are
accepted.

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

`queue/drain` can execute remote mutations and is operator-scoped. Reconcile
reads remote state but does not write destination secrets.

## List pagination

Public `LIST` endpoints accept OpenBao paginated-list parameters and pass them
through to storage:

- `after`: optional key to begin listing after; the key does not need to exist
  in the result set;
- `limit`: optional maximum number of keys to return; non-positive values
  return all keys.

This applies to `metadata`, `metadata/<path>`, `destinations/<type>`, and
`associations`.
