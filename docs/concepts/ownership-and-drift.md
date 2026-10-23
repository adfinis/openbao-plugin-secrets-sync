---
verifiedBy:
  - internal/providers/provider.go
  - internal/providers/awssecretsmanager/provider.go
  - internal/providers/kubernetessecrets/provider.go
  - internal/providers/gitlab/project_variables.go
  - internal/backend/runtime_state.go
  - internal/backend/path_config.go
  - internal/backend/path_reconcile.go
  - internal/backend/periodic_reconcile.go
  - internal/backend/backend.go
  - internal/backend/event_dispatch.go
  - internal/backend/dispatcher.go
  - internal/backend/path_queue.go
  - internal/backend/path_data.go
  - internal/backend/path_associations.go
  - internal/backend/association_enqueue.go
---

# Ownership and drift

Secret Sync updates or deletes a remote object only when metadata on that
object proves that this mount and association own it. It never takes over an
existing or foreign object.

## Ownership metadata

Every provider write attaches ownership metadata to the remote object. The
logical record has these fields:

```text
managed=true
mount_uuid=<mount-uuid>
restore_epoch=<restore-epoch>
association_id=<association-id>
source_path=<path>
object_id=<object-id>
source_version=<source-version>
payload_sha256=<hash>
```

An object is owned only when the managed marker is present and the association
ID, source path, object ID, mount UUID, and restore epoch all match the
request. Source version and payload hash are not ownership proof. They order
writes and detect drift.

Missing or mismatched metadata is ownership loss, never permission to
overwrite. Each provider stores the record differently: AWS Secrets Manager in
tags, Kubernetes in labels and annotations, GitLab in the variable
description. The provider page's `Ownership metadata` section lists the exact
keys, for example
[AWS Secrets Manager](../configure/aws-secrets-manager.md#ownership-metadata).
Anyone who can read the remote object can read this metadata; see
[Ownership metadata exposure](../security/threat-model.md#ownership-metadata-exposure).

## Runtime identity and restore epoch

Two values identify the writing mount:

- The mount UUID comes from OpenBao. A new mount, even at the same path, has a
  new UUID and does not own objects that an earlier mount created.
- The restore epoch is a random ID that the plugin stores in its own mount
  storage on first use.

Both values live in OpenBao storage. A snapshot restore or a clone therefore
brings back the same mount UUID and restore epoch, and the restored instance
can prove ownership of every object the original wrote.

> [!WARNING]
> The plugin does not detect restore or cloning. A restored or cloned instance
> can start dispatching as soon as it initializes and can overwrite or delete
> remote objects with older source data. Fence destination writes outside
> OpenBao before a restored or cloned instance starts, then follow
> [Review a restore or clone](../operate/restore-and-clone.md#fence-destinations).

The restore guard (`restore_guard=true` in `config`) blocks remote mutation as
listed in [Mutation gates](#mutation-gates). It defaults to `false` and is
stored with the rest of the mount state, so a snapshot holds whatever value it
had when the snapshot was taken.

Acknowledging an active guard rotates the restore epoch. Both
`config/restore-guard/acknowledge` and writing `restore_guard=false` while the
guard is active do this. New provider requests carry the new epoch, but remote
objects keep the old one. After acknowledgement, every object written before it
reports `REMOTE_OWNERSHIP_LOST`, and the plugin refuses to update or delete it.
The plugin does not adopt or rewrite those objects. Plan for that remediation
before you acknowledge.

## Reconcile

Reconcile reads provider state for each object of the current source version
and compares it with the desired payload:

- `reconcile/<path>/plan` (read) returns the comparison without changing local
  status.
- `reconcile/<path>` (write) returns the same comparison and records it in
  `status/<path>`, including `verification` and `last_reconcile_time`.

Neither path writes destination objects or queues work. Both call provider read
APIs, so provider authentication and network errors appear as object states.
Destination constraints are checked before the read. An object outside
`allowed_source_path_prefixes` or `allowed_resolved_name_prefixes` reports
`VALIDATION_ERROR` without a provider call. A disabled association or
destination reports `DISABLED` without a provider call. Reconcile needs a live
current source version and returns an error for a deleted source.

The comparison runs in this order:

1. No remote object: `REMOTE_MISSING`.
2. Ownership metadata does not match: `REMOTE_OWNERSHIP_LOST`.
3. The remote source version or payload hash differs: `DRIFTED`.
4. The payload hash matches: `SYNCED`.
5. Otherwise: `UNKNOWN`.

[Verification values](../reference/status-and-diagnostics.md#verification-values)
says how strong each comparison is. To write the current source version to the
destination, run manual sync, not reconcile.

## Background drift work

The `drift_repair` setting controls background checks. Set it as described in
[Enable drift detection or repair](../configure/runtime.md#enable-drift-detection-or-repair).

| Mode | Behavior |
| --- | --- |
| `off` | No background provider reads. This is the default. |
| `detect` | Runs reconcile apply for due objects and records the result in status. |
| `repair` | Runs `detect`, then queues repair for eligible `DRIFTED` objects. |

Each periodic pass selects up to `drift_reconcile_batch` objects whose last
reconcile is older than `drift_reconcile_interval`, oldest first. Candidates
come from enabled associations whose source has a live current version. Each
candidate costs at least one provider read.

In `repair` mode, the sweep skips objects that already have a queued upsert for
the current version, because that work is already the repair. In `detect` mode,
and while the restore guard is active, the sweep checks those objects too.

Repair queues ordinary operations with `trigger=drift-repair`. It never calls a
provider directly. Dispatch applies the same gates, policy checks, and
ownership checks as any other operation. A successful repair increments the
object's `repair_count` and sets `last_repair_time`. The plugin logs a warning
when `repair_count` exceeds 3, which usually means something else keeps
writing the remote object.

## Repair eligibility

Background repair queues work for an object only when all of these hold:

- `drift_repair=repair` and the restore guard is not active;
- the state is `DRIFTED`;
- the remote object exists, its ownership is known, and this mount and
  association own it;
- the provider returned a payload hash and a verification value;
- no upsert for the current version is already queued;
- the queue has capacity. Otherwise a later pass tries again.

Background repair never recreates `REMOTE_MISSING` objects, never takes over
`REMOTE_OWNERSHIP_LOST` objects, and never acts on `UNKNOWN` results. Review
those states and run manual sync when the write is intended.

## Mutation gates

Three mount-wide conditions stop provider work. The restore guard blocks
remote mutation but still allows reads. `disabled=true` pauses all background
provider work. An unsafe replication state stops the periodic function, event
dispatch, and drain on that node.

| Work | Restore guard active | `disabled=true` | Unsafe replication state |
| --- | --- | --- | --- |
| Event dispatch | Blocked | Blocked | Blocked |
| Periodic dispatch | Blocked | Blocked | Blocked |
| `queue/drain` | Rejected with a hint | Rejected with a hint | Rejected with a hint |
| Background drift detection | Runs | Skipped | Skipped |
| Background drift repair enqueue | Skipped | Skipped | Skipped |
| Manual reconcile plan and apply | Runs | Runs | Not checked |
| Manual sync and other enqueue requests | Queued, not dispatched | Queued, not dispatched | Not checked |

Unsafe replication states are performance secondary, performance standby,
performance bootstrapping, DR secondary, and DR bootstrapping. Local mounts are
never blocked by replication state.

The dispatcher rechecks `disabled` and the restore guard before it claims each
operation, so changing either one stops a running batch at the next claim.
Neither setting cancels a provider call already in progress. Pausing is not a
quiescence barrier. Setting `disabled=false` or acknowledging the guard wakes
event dispatch right away.

To stop all background provider traffic during a review, set both
`restore_guard=true` and `disabled=true`. Manual reconcile still reads provider
state on request. Disabling one association or destination is narrower; see
[Configure associations](../configure/associations.md).

## Collisions and stale objects

Providers check an existing remote object before they write:

- `associations/<path>/plan` reports `conflict` with error class `collision`
  when an object exists at the remote name without matching ownership.
- An upsert against an existing object that this association does not own
  fails with class `ownership`, and status reports `REMOTE_OWNERSHIP_LOST`.
- A create that loses a race with another writer fails with class
  `collision`. Status records this as `DRIFTED` with
  `last_error_class=collision`. Check `last_error_class` before you follow the
  manual sync hint.

Some providers keep a recoverable tombstone, such as an AWS secret scheduled
for deletion. The provider restores it only when this association owns it. The
provider page describes that behavior.

Stale objects from a torn-down, restored, or re-mounted environment fail the
ownership check. There is no adoption or metadata-rewrite endpoint. Inspect or
remove the remote object, then run the `manual_sync` action from status or
reconcile to recreate it from the current source version. [Recover ownership
loss](../operate/troubleshoot.md#recover-ownership-loss) has the procedure.

## Remote deletes

The plugin queues a remote delete only when the association has
`delete_mode=delete` and the current source version is deleted or destroyed
through `DELETE data/<path>`, `delete/<path>`, or `destroy/<path>`. Queueing the
delete also removes older queued upserts for that version. The default
`retain`, and its alias `orphan`, leave the remote object and its ownership
metadata in place. See [Choose a delete mode](../configure/associations.md#choose-a-delete-mode).

At dispatch, the plugin rechecks that the source version is still deleted and
that `delete_mode` is still `delete`. The provider then proves ownership before
it deletes. It refuses when the remote object carries a newer source version. A
remote object that is already gone counts as success. After a successful
delete, the object's status is `REMOTE_MISSING`.

Disabling or deleting an association never deletes remote objects. It removes
the association's queued work and leaves its remote objects, with their
ownership metadata, in place. Provider-specific delete behavior, such as
recovery windows and shared Kubernetes Secret keys, is on each provider page
under `Drift and delete behavior`.
