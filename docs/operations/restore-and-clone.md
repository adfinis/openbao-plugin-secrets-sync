# Restore and clone review

Use this procedure after restoring OpenBao storage, cloning an environment, or
starting a mount whose local Secret Sync state may no longer match remote
destinations.

Secret Sync uses a restore guard to prevent restored queue work from blindly
mutating remote secrets. While the guard is active, event-triggered dispatch,
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

Mounts default `restore_guard=false`. Operators can set `restore_guard=true`
before or during restore and clone review.

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

Acknowledging restore guard rotates the restore epoch and allows remote
mutation to resume when other gates also allow it. Treat acknowledgement as
consent to let the current local source versions write to destinations.

## Confirm Runtime State

Read mount config:

```sh
bao read secret-sync/config
```

If `restore_guard=true`, keep the guard active until the local source versions,
associations, queued work, and remote ownership state are understood.

Do not acknowledge the guard as a first step. Acknowledgement rotates the
restore epoch and allows remote mutation to resume.

For a quiet review that avoids background provider reads, pause the mount:

```sh
bao write secret-sync/config disabled=true
```

Leave `restore_guard=true` until the remote review is complete.

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

Acknowledge the restore guard only after the review is complete:

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
```

If the mount was paused for review, resume it intentionally:

```sh
bao write secret-sync/config disabled=false
```

After acknowledgement, event-triggered dispatch is allowed to resume. Drain due
work manually when you need a controlled catch-up batch:

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
- destination-side ownership metadata.

Do not store source secret values, destination credentials, tokens, raw audit
records containing secrets, or full provider responses containing secret data.
