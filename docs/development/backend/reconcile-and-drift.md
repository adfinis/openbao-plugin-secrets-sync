# Reconcile And Drift

Reconcile and drift work both use provider read-state capability, but they have
different contracts.

Manual reconcile is an operator-requested API operation for one source path.
Periodic drift work is background work controlled by mount config.

## Manual Reconcile

Manual reconcile has two paths:

- `reconcile/<path>/plan` reads provider state and returns computed object
  state without changing local status.
- `reconcile/<path>` reads provider state and writes local status from the
  result.

Manual reconcile does not write destination secrets and does not enqueue repair
work. It remains available when `disabled=true` because it is not a remote
mutation path.

Manual reconcile still calls providers, so response redaction and provider
error classification rules apply.

## Reconcile Result Mapping

For each association object, reconcile compares the current OpenBao source
version with provider remote state.

Typical outcomes are:

- `SYNCED`: remote payload and ownership match current local source state.
- `DRIFTED`: remote object is owned by this association but differs from the
  current local source state.
- `REMOTE_MISSING`: the expected remote object does not exist.
- `REMOTE_OWNERSHIP_LOST`: a remote object exists but is not owned by this
  association or mount identity.
- `VALIDATION_ERROR`: local association or destination state cannot be used for
  provider read-state.
- `DISABLED`: association or destination is disabled.
- provider error states: provider read-state returned classified transport,
  auth, authorization, capacity, validation, or internal failure.

Responses include object-level diagnostics where the state has a useful
operator recovery path.

## Periodic Drift Work

Background drift work is controlled by `drift_repair`:

```text
off
detect
repair
```

The default `off` performs no periodic provider reads for drift.

`detect` periodically reads provider state and refreshes local status. It does
not enqueue repair work.

`repair` periodically reads provider state, refreshes local status, and
enqueues normal queued repair for repairable `DRIFTED` objects.

Periodic drift work also uses:

- `drift_reconcile_interval`
- `drift_reconcile_batch`

Candidates are enabled associations with a live current source version and due
or missing reconcile status. The batch is sorted by oldest reconcile time, then
source path, association ID, and object ID.

## Repair Eligibility

Periodic repair is intentionally narrower than manual sync. A drift result is
repairable only when:

- the state is `DRIFTED`;
- the remote object exists;
- ownership metadata is known;
- the remote object is owned by this mount and association;
- the provider returned a payload hash;
- the provider returned a verification marker.

Periodic repair does not take over `REMOTE_OWNERSHIP_LOST` objects, recreate
`REMOTE_MISSING` objects, or repair objects when ownership cannot be verified.
Use manual sync after operator review for those states.

Periodic repair enqueues regular outbox work with trigger `drift-repair`.
Dispatch still performs the normal source-version, destination-policy,
capability, ownership, and provider checks before mutating the remote object.

## Mutation Gates

Replication safety is checked before periodic drift or dispatch work. Unsafe
replication states block background provider traffic.

`disabled=true` blocks background provider traffic entirely. Periodic drift
detection, periodic repair enqueue, and periodic dispatch all stop while the
mount is disabled. Manual reconcile remains available.

Restore guard has narrower semantics. Periodic drift detection can still run
while restore guard is active so operators can inspect remote state. Restore
guard blocks periodic repair enqueue, event dispatch, queue drain, and queued
remote mutation until acknowledged.

`drift_repair=detect` and active restore guard can inspect objects that already
have queued upserts. `drift_repair=repair` avoids selecting objects that
already have current-version queued upsert work, because queued mutation is
already the repair path.
