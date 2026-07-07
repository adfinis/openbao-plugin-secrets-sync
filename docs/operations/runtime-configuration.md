# Runtime configuration

Use this page when you need to tune or pause a mounted Secret Sync engine. For
blocked sync recovery and incident procedures, use the
[operator runbook](operator-runbook.md). Use [Convergence](../concepts/convergence.md)
for queue and dispatch behavior and [Reconcile and drift](../concepts/reconcile-and-drift.md)
for background drift behavior.

Read the current mount configuration:

```sh
bao read secret-sync/config
```

## Restore guard

Fresh mounts start with `restore_guard=false`. If `restore_guard=true` after a
restore, clone, or manual restore-guard rearm, remote mutation is blocked until
review is complete and the guard is acknowledged.

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
```

Use [Restore and clone review](restore-and-clone.md) before acknowledging the
guard in restored or cloned environments.

## Mount pause

Set `disabled=true` to pause background provider traffic and remote mutation:

```sh
bao write secret-sync/config disabled=true
bao write secret-sync/config disabled=false
```

Manual reconcile remains available while disabled because it does not write
destination secrets. Manual queue drains and remote mutation remain blocked
until the mount is enabled again.

## Source opt-in

Set `require_source_opt_in=true` when enabled associations should require
source metadata `custom_metadata.syncable=true` before enqueue or dispatch:

```sh
bao write secret-sync/config require_source_opt_in=true
```

Use [Delegated use](../guides/delegated-use.md) for the full source opt-in and
destination-prefix workflow.

## Delegated mode

Fresh mounts default `delegated_mode=false`. Use that default when a trusted
platform operator owns both destination configuration and association
management.

Set `delegated_mode=true` only when application owners can manage their own
association prefixes. Delegated mode requires strict source opt-in and
destination constraints:

```sh
bao write secret-sync/config require_source_opt_in=true delegated_mode=true
```

When delegated mode is enabled, association create, enable, manual sync,
reconcile, and queued dispatch reject destinations that do not set both
`allowed_source_path_prefixes` and `allowed_resolved_name_prefixes`.
Destination checks report `destination_unconstrained` for that condition.

## Queue capacity

`queue_capacity` limits the number of pending outbox operations accepted by the
mount. When the queue is full, writes that would enqueue sync work fail before
committing a new source version.

```sh
bao write secret-sync/config queue_capacity=1000
```

Set `queue_capacity=0` only for a deliberate enqueue freeze. Existing queued
work can still drain when other safety gates allow remote mutation.

## Drift work

Background drift work is opt-in. The default `drift_repair=off` performs no
periodic provider reads for drift.

Use `detect` to refresh status from provider read-state checks, or `repair` to
also enqueue owned `DRIFTED` objects for normal queued repair:

```sh
bao write secret-sync/config \
  drift_repair=detect \
  drift_reconcile_interval=1h \
  drift_reconcile_batch=16
```

`repair` does not take over ownership-lost objects. Use the provider guide and
operator runbook when status reports `REMOTE_OWNERSHIP_LOST` or
`REMOTE_MISSING`.

## Event dispatch

Fresh mounts default `event_dispatch_enabled=true`. Enqueue-producing requests
wake a bounded dispatcher immediately after durable queue commit, so normal
sync usually starts without waiting for the periodic callback.

Tune one wakeup batch with `event_dispatch_max_operations`:

```sh
bao write secret-sync/config \
  event_dispatch_enabled=true \
  event_dispatch_max_operations=16
```

The API contract remains asynchronous: writes still return
`sync_operation_ids`, and periodic processing remains the recovery path for
missed wakeups, retries, and restart recovery.
