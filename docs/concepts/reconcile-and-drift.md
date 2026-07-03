# Reconcile And Drift

Reconcile compares OpenBao desired state with provider remote state. Drift work
uses the same provider read-state checks on a background schedule.

Use this page when you need to understand plan versus apply, detect versus
repair, value verification, and safety gates.

## Manual Reconcile

Manual reconcile has two paths:

- `reconcile/<path>/plan` reads provider state and returns computed object
  state without changing local status.
- `reconcile/<path>` reads provider state and refreshes local status from the
  provider result.

Both paths are read-only with respect to destination secrets. They do not create,
update, or delete provider objects, and they do not enqueue repair work.

Manual reconcile still calls provider read-state APIs. It can fail because of
provider authentication, authorization, network, rate limit, validation, or
read-state capability errors.

## Result Mapping

For each association object, reconcile compares the current OpenBao source
version and desired payload hash with provider evidence.

Typical outcomes:

| State | Meaning |
| --- | --- |
| `SYNCED` | Ownership and payload evidence match current local source state. |
| `DRIFTED` | Remote object is owned, but payload evidence differs from current local source state. |
| `REMOTE_MISSING` | The expected remote object does not exist. |
| `REMOTE_OWNERSHIP_LOST` | A remote object exists, but ownership does not match this mount or association. |
| `UNKNOWN` | The provider lacks enough comparable metadata or value evidence. |
| `VALIDATION_ERROR` | Local association or destination state cannot be used for read-state. |
| `DISABLED` | The association or destination is disabled. |

When a state has a known recovery path, reconcile and status responses can
include `hint` and `next_actions`.

## Verification

Status and reconcile objects can include `verification`:

| Verification | Meaning |
| --- | --- |
| `value` | The provider compared live remote value bytes or provider-native data against the desired payload hash. |
| `metadata` | The provider compared ownership or payload-hash metadata without reading the live value. |

Provider behavior differs:

- AWS Secrets Manager uses metadata verification by default. Set
  `value_drift_detection=true` on the destination to detect manual value edits
  that leave tags unchanged.
- Kubernetes Secrets use value verification by reading Secret data.
- GitLab project variables use value verification through GitLab variable
  readback.

If verification is `metadata`, a manual value-only change can be invisible when
the provider metadata still matches.

## Background Drift Modes

Background drift work is controlled by mount config:

| Mode | Behavior |
| --- | --- |
| `off` | No periodic provider reads for drift. This is the default. |
| `detect` | Periodically reads provider state and refreshes local status only. |
| `repair` | Runs detect behavior and enqueues normal queued repair for repairable owned `DRIFTED` objects. |

Drift cadence and cost are controlled by `drift_reconcile_interval` and
`drift_reconcile_batch`.

Repair is intentionally narrower than manual sync. Background repair does not
take over ownership-lost objects, recreate missing objects, or write when
ownership cannot be verified.

## Repair Path

Background repair never mutates a provider directly from the drift sweep. It
enqueues normal outbox work with trigger `drift-repair`.

That repair work still passes through:

- queue capacity and ordering;
- restore guard;
- disabled mount checks;
- destination and association enabled checks;
- destination source and remote-name policy;
- provider capability validation;
- provider ownership checks;
- provider retry and terminal-failure classification.

Use [Convergence](convergence.md) for queue behavior and
[Ownership and safety](ownership-and-safety.md) for ownership rules.

## Restore Guard And Disabled Mounts

Restore guard blocks remote mutation and repair enqueue until acknowledged.
Background detect may still run while restore guard is active so operators can
inspect remote state before consenting to resume mutation.

`disabled=true` is stronger: it blocks background provider traffic entirely.
Manual reconcile remains available because it is an explicit operator read of
provider state and does not write destination secrets.

If `drift_repair=repair` is enabled, acknowledging restore guard also allows
future background repair work to enqueue owned drift repairs. Review reconcile
output before acknowledging restored or cloned environments.

## When To Use Reconcile

Use reconcile plan when:

- creating or changing an association;
- a status state is stale or unclear;
- a remote object was manually edited, deleted, or restored;
- a restore or clone review is in progress;
- background drift detection is disabled but you want a one-off check.

Use reconcile apply when:

- local status should reflect the latest provider state;
- an operator wants `status/<path>` to carry current hints and next actions;
- a restore review needs durable local evidence before mutation resumes.

Use manual sync, not reconcile, when the desired action is to write the current
OpenBao source version to the destination.
