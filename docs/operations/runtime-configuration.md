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

## Security posture

Fresh mounts default `security_posture=standard`. This keeps onboarding simple:
unconstrained destinations are accepted, and explicit source sync enablement is
not required before sync.

Set `security_posture=hardened` when application owners can manage their own
source paths or associations:

```sh
bao write secret-sync/config security_posture=hardened
```

Hardened posture requires source sync to be explicitly enabled with
`sources/<path>/enable` before enabled associations can enqueue or dispatch
remote mutation. It also
rejects destination writes that do not set both
`allowed_source_path_prefixes` and `allowed_resolved_name_prefixes`.
Association create, enable, manual sync, reconcile, and queued dispatch reject
destinations that do not set both
`allowed_source_path_prefixes` and `allowed_resolved_name_prefixes`.
Destination checks report `destination_unconstrained` for that condition.

When moving an active mount from standard to hardened posture, constrain its
destinations first, set `security_posture=hardened`, and then enable each source.
Queued operations rejected by the newly active source guard become terminal;
`sources/<path>/enable` re-enqueues the current version for enabled associations
with active destinations so those sources can converge under the hardened
policy. The enable request fails and leaves source sync disabled when the queue
cannot admit all required operations.

Changing back to `security_posture=standard` relaxes those posture checks for
future operations.

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
