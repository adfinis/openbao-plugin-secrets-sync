---
verifiedBy:
  - internal/backend/backend.go
  - internal/backend/path_data.go
  - internal/backend/path_versions.go
  - internal/backend/path_associations.go
  - internal/backend/enqueue.go
  - internal/backend/association_enqueue.go
  - internal/backend/dispatcher.go
  - internal/backend/event_dispatch.go
  - internal/backend/path_queue.go
  - internal/backend/recovery.go
  - internal/backend/storage_status.go
  - internal/backend/redaction_test.go
---

# Sync model

Secret Sync pushes source data that lives in its own mount to external
destinations. A request changes local state right away. Destinations converge
later, through a durable queue.

## Source data lives in the mount

Secret Sync syncs only source data written to its own mount under
`data/<path>`. It cannot read or mirror secrets from other OpenBao mounts, such
as an existing KV-v2 engine.

This is an OpenBao plugin boundary. A secrets engine plugin receives a storage
view scoped to its own mount and only the requests routed to its own paths. It
cannot observe writes to other mounts. To sync a value, write it to
`secret-sync/data/<path>` and create an association for that path.

The source API is KV-v2-like: each write creates a version, and source metadata
controls version retention, check-and-set, and custom metadata. The
[API reference](../reference/api.md#source-write-forms) owns write forms and
KV-v2 differences.

Source values appear only in source reads and in provider requests. Plan,
queue, status, and reconcile responses never contain them.

## Associations connect sources to destinations

An association connects one source path to one destination, addressed as
`<type>/<name>`. The association decides how the source becomes remote
objects: secret shape, remote name, provider object settings, delete mode, and
enabled state. The destination holds connection settings and credentials.
[Configure associations](../configure/associations.md) owns shapes, naming,
selection, and delete modes.

Only enabled associations on enabled destinations produce remote work. In
hardened posture, the source path also needs source sync enabled; see
[Enable source sync](../configure/delegate.md#enable-source-sync).

## Accepted is not synced

A successful request means OpenBao stored the local change and durably queued
any remote work it needs. It does not mean the destination has the new value.

These requests return the queued operation IDs in `sync_operation_ids`:

- source writes and `DELETE data/<path>`, inside the response `metadata`
  object next to `sync_state`;
- association create, enable, and `associations/<path>/sync`;
- `sources/<path>/enable`.

The version paths `delete/<path>`, `undelete/<path>`, and `destroy/<path>`
also queue work when they change the current version, but they return no IDs.

An empty `sync_operation_ids` list is not always an error. For example,
updating an association that is already enabled queues nothing and returns a
`hint` to run manual sync.

The plugin checks queue capacity before it accepts a change. If the work does
not fit, the request fails and the new source version or association change is
not stored. In hardened posture without source sync enabled, a source write
stores the version, queues nothing, and returns `sync_state=VALIDATION_ERROR`
with a hint.

Use `status/<path>` as convergence evidence. Successful operations are removed
from the queue, so their IDs stop resolving. [Status and
diagnostics](../reference/status-and-diagnostics.md) defines every state and
what `SYNCED` proves.

## Dispatch paths

Three paths run the same dispatcher over the same queue. Each one claims a due
operation, rechecks the [mutation gates](ownership-and-drift.md#mutation-gates),
calls the provider, and writes per-object status. None of them makes an API
response wait for the provider.

| Path | Runs when | Operations per pass |
| --- | --- | --- |
| Event dispatch | A request queued work, a queue operation was retried, a config write resumed dispatch, or a `retry_wait` operation became due. | `event_dispatch_max_operations` (default 16). Another pass runs while a pass reaches the limit. |
| Periodic dispatch | OpenBao calls the mount's periodic function. | 100 |
| `queue/drain` | An operator calls it. | `max_operations` (default 100) |

Event dispatch only lowers latency. Wakeups are coalesced and best-effort.
Periodic dispatch is the recovery path for missed wakeups, restarts, and writes
that were interrupted after recording their enqueue intent. Each path recovers
such incomplete intents before it dispatches. To turn event dispatch off, see
[Tune event dispatch](../configure/runtime.md#tune-event-dispatch).

`queue/drain` can mutate destinations, so restrict it to operators. It returns
an error with a hint when the mount is disabled, the restore guard is active,
or the node is in an unsafe replication state.

## Ordering

Ordering is guaranteed per association object. Newer source state wins:

- a source write replaces older queued upserts for the same object, unless a
  dispatcher has already claimed them;
- dispatch drops an upsert whose version is older than the current source
  version;
- a status write for an older version than the stored status is ignored;
- providers that read ownership metadata refuse a write when the remote object
  carries a newer source version, with error class `drift`.

Operations for different objects or destinations have no ordering guarantee.

To push the current source version after a recovery, run manual sync instead of
retrying an old operation. Dispatch drops a retried upsert that a newer source
version has superseded.
