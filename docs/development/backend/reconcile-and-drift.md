---
verifiedBy:
  - internal/backend/path_reconcile.go
  - internal/backend/periodic_reconcile.go
  - internal/backend/association_enqueue.go
  - internal/backend/storage_status.go
  - internal/backend/backend.go
  - internal/backend/path_reconcile_test.go
  - internal/backend/periodic_test.go
---

# Reconcile and drift

Manual reconcile and periodic drift work share one read path,
`reconcileAssociationObject`, and differ in candidate selection and in what
they do with the result. User-facing semantics, including plan versus apply
and drift modes, are in
[Ownership and drift](../../concepts/ownership-and-drift.md#reconcile).

## Read path

For one association object, the backend:

1. Fails with `DISABLED` when the association or destination is disabled, and
   with `VALIDATION_ERROR` when the provider is unknown or the destination is
   missing.
2. Resolves public and sensitive destination config and the runtime identity.
3. Resolves the remote name and checks destination policy for the object. A
   policy failure is `VALIDATION_ERROR` and skips the provider read.
4. Builds the canonical payload and enforces `MaxPayloadBytes`.
5. Calls `ReadState` on the cached runtime with the payload hash, never the
   payload bytes.
6. Maps the remote state with `reconcileStateForRemoteState`.

The mapping checks, in order:

1. `Exists=false` is `REMOTE_MISSING`.
2. Known ownership with `Owned=false` is `REMOTE_OWNERSHIP_LOST`.
3. A remote source version that differs from the current version is
   `DRIFTED`.
4. A remote payload hash that differs from the desired hash is `DRIFTED`.
5. A matching hash is `SYNCED`.
6. Anything else is `UNKNOWN`.

A provider error maps to a state through its error class. State meanings are
in [Status and diagnostics](../../reference/status-and-diagnostics.md#sync-states).

## Manual reconcile

`reconcile/<path>/plan` (read) and `reconcile/<path>` (update) both require a
live current version and read every association at the path, enabled or not.
Only the update operation writes status, through `putReconcileStatus`.

`putReconcileStatus` sets `last_reconcile_time`, the verification marker,
remote version, and error fields; sets `last_drift_detected_time` for
`DRIFTED`; and keeps the stored last operation ID and success time. The
version-ordering rule in `putStatus` still applies.

Manual reconcile does not check the replication state, `disabled`, or
`restore_guard`, because it never writes destination secrets or enqueues
work. Destination policy still applies before each provider read.

## Periodic drift work

`periodicDriftReconcile` runs from the periodic callback after enqueue-intent
recovery and before the restore guard return. It does nothing when
`drift_repair=off`. Configuration fields and defaults are in
[Runtime settings](../../configure/runtime.md#enable-drift-detection-or-repair).

### Candidate selection

`driftReconcileCandidates` scans every key under `associations/` and keeps an
association object when:

- the association is enabled;
- the source has a current version that is not deleted or destroyed;
- the object IDs resolve from the current payload;
- the object's status is missing, has no parseable `last_reconcile_time`, or
  was last reconciled at least `drift_reconcile_interval` ago; and
- when `drift_repair=repair` and `restore_guard=false`, the object has no
  queued upsert for the current version or newer.

With `drift_repair=detect`, or while the restore guard is active, objects with
queued upserts stay candidates so operators can inspect them. In repair mode
the queued upsert is already the repair path.

Destination state is not a selection filter. A disabled destination produces a
`DISABLED` result when the object is read.

### Batch order

Candidates sort by `last_reconcile_time` ascending, with never-reconciled
objects first, then by source path, association ID, and object ID. The first
`drift_reconcile_batch` candidates are processed in order. For each one, the
backend reads remote state, records a reconcile metric, writes status with
`putReconcileStatus`, and then considers repair.

The configuration snapshot is read once at the start of the periodic callback.
A `config` write during the batch does not stop the batch.

### Repair

Repair is considered only when `drift_repair=repair`. `isRepairableDrift`
requires all of the following:

- state `DRIFTED`;
- a remote state with `Exists=true`, `OwnershipKnown=true`, and `Owned=true`;
- a non-empty remote payload hash;
- a non-empty verification marker.

`REMOTE_MISSING`, `REMOTE_OWNERSHIP_LOST`, and drift without verified
ownership are never repaired in the background. User-facing rules are in
[Ownership and drift](../../concepts/ownership-and-drift.md#repair-eligibility).

For a repairable result, `enqueuePeriodicDriftRepair`:

1. Skips the object and records a restore guard block when
   `restore_guard=true`.
2. Skips enqueue when the association already has a queued upsert for the
   current version or newer.
3. Enqueues salted upserts with trigger `drift-repair` for every object of the
   association, not only the drifted one. Later drifted objects of the same
   association then hit step 2.
4. Skips the object and records a capacity block when the queue is full.

Repair enqueue does not signal event dispatch. The same periodic callback
dispatches it in its final step, and dispatch repeats the source version,
eligibility, destination policy, and provider ownership checks before any
mutation. A successful repair sets `last_repair_time` and increments
`repair_count`. When the count exceeds 3, the backend logs a warning with the
destination type, granularity, and count.
