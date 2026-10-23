---
verifiedBy:
  - internal/backend/path_config.go
  - internal/backend/path_config_test.go
  - internal/backend/storage_records.go
  - internal/backend/runtime_state.go
  - internal/backend/event_dispatch.go
  - internal/backend/event_dispatch_test.go
  - internal/backend/enqueue.go
  - internal/backend/storage_outbox.go
  - internal/backend/backend.go
  - internal/backend/dispatcher.go
  - internal/backend/path_queue.go
  - internal/backend/periodic_reconcile.go
  - internal/backend/periodic_test.go
---

# Runtime settings

Mount-wide runtime settings live at `secret-sync/config`. Read the current
values:

```sh
bao read secret-sync/config
```

Writes are partial updates: omitted fields keep their stored values. The
plugin rejects unknown fields and serializes concurrent config writes handled
by one mount process, so one write cannot restore another field from a stale
snapshot.

## Configuration fields

| Field | Default | Allowed values | Effect |
| --- | --- | --- | --- |
| `security_posture` | `standard` | `standard`, `hardened` | `hardened` requires source sync enablement and constrained destinations before remote mutation. See [Enable hardened posture](delegate.md#enable-hardened-posture). |
| `disabled` | `false` | `true`, `false` | Pauses background work and remote mutation for the mount. See [Pause the mount](#pause-the-mount). |
| `restore_guard` | `false` | `true`, `false` | Blocks remote mutation until acknowledged. Setting `true` clears `restore_guard_acknowledged_time`. Setting `false` on an active guard rotates the restore epoch, like `config/restore-guard/acknowledge`. See [Review a restore or clone](../operate/restore-and-clone.md). |
| `queue_capacity` | `1000` | Integer, `0` or greater | Maximum number of `pending` and `retry_wait` queue operations. See [Set queue capacity](#set-queue-capacity). |
| `drift_repair` | `off` | `off`, `detect`, `repair` | Background drift policy. See [Enable drift detection or repair](#enable-drift-detection-or-repair). |
| `drift_reconcile_interval` | `1h` | Go duration, `1m` or longer | Minimum time between background drift checks of one association object. |
| `drift_reconcile_batch` | `16` | Integer, `1` or greater | Maximum association objects checked in one periodic drift sweep. |
| `event_dispatch_enabled` | `true` | `true`, `false` | Wakes the dispatcher after writes that enqueue work. See [Tune event dispatch](#tune-event-dispatch). |
| `event_dispatch_max_operations` | `16` | Integer, `1` or greater | Maximum due operations processed in one event-triggered wakeup. |

The integer fields have no upper bound. A config read also returns read-only
fields: `restore_guard_acknowledged_time`, `restore_epoch`, `mount_uuid`,
`storage_schema_version`, and `storage_schema_min_compatible_version`.

> [!WARNING]
> The plugin does not detect restore or cloning. `restore_guard` and `disabled`
> are restored with the snapshot, so neither guarantees that a restored or
> cloned instance starts fenced. Block destination writes outside OpenBao
> first, as described in
> [Fence destinations](../operate/restore-and-clone.md#fence-destinations).

## Pause the mount

Set `disabled=true` to stop dispatch and background provider work:

```sh
bao write secret-sync/config disabled=true
```

While the mount is paused:

- source writes and association changes are still accepted, and their queue
  operations stay `pending`;
- periodic dispatch, enqueue recovery, and background drift work stop, while
  periodic pruning of expired terminal operations continues;
- `queue/drain` fails with a diagnostic; and
- manual `reconcile/<path>/plan` and `reconcile/<path>` stay available because
  they do not write destination secrets.

The dispatcher checks the flag before each claim, so an operation already in
progress finishes. Resume the mount:

```sh
bao write secret-sync/config disabled=false
```

With event dispatch enabled, resuming wakes the dispatcher for due work. For
how pause compares with the restore guard and replication state, see
[Mutation gates](../concepts/ownership-and-drift.md#mutation-gates).

## Set queue capacity

`queue_capacity` limits the number of `pending` and `retry_wait` operations.
Terminal operations do not count. A request that would push the queue past the
limit fails before it commits, so a rejected source write stores no new
version. Work that only replaces a queued upsert for the same object needs no
extra capacity.

```sh
bao write secret-sync/config queue_capacity=5000
```

Lowering the limit below the current depth discards nothing; new work is
rejected until the queue has room again. Set `queue_capacity=0` only
for a deliberate enqueue freeze. Existing work can still drain when the
[mutation gates](../concepts/ownership-and-drift.md#mutation-gates) allow it.

## Enable drift detection or repair

Background drift work is off by default, and the plugin then makes no periodic
provider reads for drift. Set `detect` to record drift in status from
read-only checks, or `repair` to also enqueue repair work for owned drifted
objects:

```sh
bao write secret-sync/config \
  drift_repair=detect \
  drift_reconcile_interval=1h \
  drift_reconcile_batch=16
```

Each periodic sweep checks at most `drift_reconcile_batch` objects, oldest
reconcile first, and skips objects checked within `drift_reconcile_interval`.
Repair goes through the normal queue with `trigger=drift-repair` and never
takes over objects whose ownership is lost. See
[Background drift work](../concepts/ownership-and-drift.md#background-drift-work)
and [Repair eligibility](../concepts/ownership-and-drift.md#repair-eligibility).

## Tune event dispatch

With `event_dispatch_enabled=true`, a write that enqueues work wakes a bounded
dispatcher after the queue commit, so sync usually starts without waiting for
the periodic callback. Each wakeup processes at most
`event_dispatch_max_operations` due operations:

```sh
bao write secret-sync/config event_dispatch_max_operations=32
```

The API stays asynchronous: writes return `sync_operation_ids`, and periodic
processing still handles missed wakeups, retries, and restart recovery. With
event dispatch off, periodic processing is the only automatic dispatch path.
Turning it back on wakes the dispatcher. See
[Dispatch paths](../concepts/sync-model.md#dispatch-paths).
