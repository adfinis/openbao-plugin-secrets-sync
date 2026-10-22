# Restore and clone review

Use this procedure after restoring OpenBao storage, cloning an environment, or
starting a mount whose local Secret Sync state may no longer match remote
destinations.

Secret Sync does not detect snapshot restore or cloning automatically. Config,
queue records, and the restore epoch are stored in the restored timeline.
A snapshot can contain `restore_guard=false` and `disabled=false`. Changing
these fields in the current timeline does not change their values in an
earlier or different snapshot. A restored or cloned mount can start dispatching
as soon as it initializes and can retain ownership identity that matches live
objects.

**Prevent the affected instances from mutating real destinations before they
start or receive restored storage.** Use an external network or provider
authorization control that does not depend on the restored OpenBao storage.
Keep that control in place until the review and ownership remediation below
are complete. The plugin's manual guard alone cannot provide this boundary.

While the restore guard is active, event-triggered dispatch,
manual `queue/drain`, periodic dispatch, and background drift-repair enqueue
are blocked. Reconcile planning remains available so operators can inspect
remote state before resuming sync. If background drift detection is enabled, it
may continue to refresh local status from provider read-state checks while the
guard is active; it does not mutate remote destinations or enqueue repair until
the guard is acknowledged.

Use [Convergence](../concepts/convergence.md) for queue operation handling and
[Reconcile and drift](../concepts/reconcile-and-drift.md) for the read-state
model used during review. Use [Ownership and safety](../concepts/ownership-and-safety.md)
for restore epoch and ownership metadata behavior.

Mounts default `restore_guard=false`. The procedure below arms the guard after
external fencing is verified. Automatic restore-generation detection remains
an upstream dependency.

## Fence destinations before restore or startup

1. Identify the restored or cloned instances and all destinations they can
   reach, including credentials or file inputs restored with the environment.
2. Deny destination writes from those instances before starting them or
   restoring storage into a running cluster. Use network isolation, a provider
   write-denial policy, or isolated test destinations. Apply the control outside
   the snapshot timeline and scope it to the affected instances.
3. Verify the control from the affected execution environment. A configuration
   record in OpenBao is not evidence that destination access is blocked.
   Before an in-place restore, quiesce local writes and account for provider
   requests already in progress. Pause and guard settings do not cancel
   accepted provider requests or undo completed remote mutations.
4. Start or restore the fenced environment. Arm and pause each Secret Sync
   mount before granting provider access:

   ```sh
   bao write secret-sync/config restore_guard=true disabled=true
   bao read secret-sync/config
   ```

   Verify that both fields are `true`. Keep the external write fence active.
5. If remote inspection requires access, grant only the provider reads needed
   for health and reconcile. Keep writes denied during review. A clone must
   remain isolated from production destinations unless its ownership and
   destination configuration have been reviewed for that use.

## Guard Semantics

Restore guard is a remote-mutation gate, not a full provider-read silence
switch:

- event-triggered dispatch is blocked;
- manual `queue/drain` is blocked;
- periodic dispatch is blocked;
- `drift_repair=repair` cannot enqueue repair work;
- `drift_repair=detect` may still refresh local status from provider
  read-state checks;
- manual reconcile plan and apply remain available because they do not write
  destination secrets.

If the review must avoid all background provider traffic, also set
`disabled=true`. Manual reconcile remains available for explicit operator
read-state checks while disabled.

Acknowledging an active restore guard rotates the restore epoch. It does not
update ownership metadata on existing remote objects. Objects with the previous
epoch can then report `REMOTE_OWNERSHIP_LOST`; the plugin does not adopt them
automatically. Keep the mount disabled and externally fenced while resolving
that ownership boundary.

## Confirm Runtime State

Read mount config:

```sh
bao read secret-sync/config
```

Verify `restore_guard=true` and `disabled=true` after arming the mount. Keep the
guard active until the local source versions, associations, queued work, and
remote ownership state are understood.

Do not acknowledge the guard as a first step. Setting `disabled=true` gates
new background work, but does not cancel an existing reconciliation pass or
provider request. It is not a quiescence barrier. Manual reconcile can still
read provider state when external authorization permits it.

## Inventory local state

List source metadata and associations:

```sh
bao list secret-sync/metadata
bao list secret-sync/associations
```

For each important source path, read source metadata, associations, status, and
queue state:

```sh
bao read secret-sync/metadata/app/db
bao read secret-sync/associations/app/db
bao read secret-sync/status/app/db
bao read secret-sync/queue
```

Use queue operation IDs from the queue summary or status output to inspect
individual operations:

```sh
bao read secret-sync/queue/<operation-id>
```

Read static defaults and provider capabilities when an association shape is
unclear:

```sh
bao read secret-sync/info
```

## Review remote state

Run reconcile plan for each important source path:

```sh
bao read secret-sync/reconcile/app/db/plan
```

Reconcile plan reads provider remote state and calculates local status without
changing local status or destination secrets. It is safe while the restore
guard is active.

Review the reconcile `verification` field. `value` means the provider compared
live remote value bytes or provider-native data against the desired payload
hash. `metadata` means the provider compared ownership or payload-hash metadata
without reading the live value. AWS Secrets Manager uses metadata verification
by default unless the destination has `value_drift_detection=true`.

When the plan reports ownership loss, collision, drift, missing remote objects,
metadata-only verification, or unexpected source versions, inspect the
destination-side metadata before retrying or acknowledging the guard. Provider
guides describe where ownership metadata is stored:

- AWS Secrets Manager: tags;
- Kubernetes Secrets: labels and annotations;
- GitLab project variables: variable description.

## Decide queued work handling

For each queued operation, decide whether to keep it, retry it, or cancel it.

Keep queued work when:

- the source version is still the intended source of truth;
- the association still owns the remote object;
- the destination policy still allows the source path and resolved remote name.

Cancel queued work when:

- the source version is stale after restore;
- the remote object is intentionally managed elsewhere;
- the association no longer needs to mutate the destination.

Retry queued work only after the destination config, source eligibility,
association, and remote ownership state are understood:

```sh
bao write -force secret-sync/queue/<operation-id>/retry
bao write -force secret-sync/queue/<operation-id>/cancel
```

Prefer manual association sync over retry when the goal is to push the current
source version after review:

```sh
bao write secret-sync/associations/app/db/sync destination=<type>/<name>
```

## Refresh local status

After reviewing a reconcile plan, apply reconcile when local status needs to be
refreshed from provider read-state results:

```sh
bao write -force secret-sync/reconcile/app/db
```

Reconcile apply updates local status only. It does not write destination
secrets.

## Resume remote mutation

Before resuming, decide whether background repair should be allowed. If
`drift_repair=repair` is configured, acknowledgement allows a future background
sweep to enqueue owned drift repair. Set `drift_repair=detect` or
`drift_repair=off` first if the restored local source versions should not yet
auto-repair destinations.

Acknowledge the active restore guard after reviewing local work. Keep
`disabled=true` and the external write fence in place:

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
bao read secret-sync/config
```

Verify that `restore_guard=false`, `disabled=true`, and `restore_epoch` has
changed. Re-run reconcile plans against the new epoch. A successful plan before
acknowledgement does not prove ownership after rotation.

For each object with stale ownership, choose a reviewed recovery action:

- Retain the remote object under its current manager and cancel local work
  that would mutate it.
- Configure an unused remote name or isolated destination, then sync the
  reviewed current source version to create a new owned object.
- If the remote object must be recreated at the same name, preserve required
  remote data and obtain destination-owner approval before removing stale
  provider state. Follow the provider operations guide, including Kubernetes
  shared-key preservation and AWS scheduled-deletion behavior. Then use manual
  association sync to recreate it from the reviewed source.

There is no generic ownership-adoption or metadata-rewrite endpoint. Do not
retry ownership-lost work expecting it to bypass the ownership check.

Once destination configuration, queued work, and ownership recovery are ready,
remove the external fence for the reviewed destinations while the mount is
still disabled. Resume the mount:

```sh
bao write secret-sync/config disabled=false
```

Enabling the mount can immediately resume event-triggered dispatch. For manual
catch-up batches, set `event_dispatch_enabled=false` before enabling the mount;
periodic dispatch still runs, so this does not provide an exclusive drain
window. Drain due work when needed:

```sh
bao write secret-sync/queue/drain max_operations=10
```

Read status after each drain batch:

```sh
bao read secret-sync/status/app/db
```

Stop and re-plan if status reports `REMOTE_OWNERSHIP_LOST`, unexpected
`DRIFTED` objects, validation errors, or queue-blocked diagnostics.

## Evidence to keep

For audit or incident review, keep:

- restore or clone timestamp;
- external fencing method, verification, scope, and removal time;
- plugin version and mount path;
- restore guard and disabled state before acknowledgement;
- source paths reviewed;
- association IDs and destination names;
- source versions reviewed;
- reconcile plan output with secret payloads absent;
- verification mode reported by reconcile or status;
- queue operation IDs and final actions;
- restore guard acknowledgement time;
- restore epoch after acknowledgement when visible in destination metadata;
- ownership remediation decisions after epoch rotation;
- destination-side ownership metadata.

Do not store source secret values, destination credentials, tokens, raw audit
records containing secrets, or full provider responses containing secret data.
