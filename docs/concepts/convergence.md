# Convergence

Secret Sync is asynchronous. API writes change local desired state first, then
the plugin converges provider objects through durable queued work.

This page explains what queued operation IDs mean, how dispatch runs, when to
use `queue/drain`, and how to interpret status states.

## Accepted Is Not Synced

Responses that can create remote work return `sync_operation_ids`. Those IDs
mean OpenBao accepted local desired state and wrote durable queue records. They
do not mean the destination already contains the new value.

Normal enqueue-producing operations include:

- source writes and current-version source lifecycle changes;
- association create;
- disabled-to-enabled association transitions;
- manual association sync;
- queue operation retry;
- background drift repair.

An empty `sync_operation_ids` list is not always an error. For example,
updating an already enabled association does not enqueue sync work. When the
backend knows the next useful action, the response includes `hint` and
`next_actions`.

## Queue Lifecycle

Queued operations are durable remote-mutation intent. Providers are called only
after the dispatcher claims a due operation and all mutation gates allow the
call.

Queue operation states are:

| State | Meaning |
| --- | --- |
| `pending` | Ready to dispatch when due and mutation gates allow it. |
| `retry_wait` | Waiting for bounded automatic retry after a transient provider failure. |
| `failed_terminal` | Not dispatchable until an operator retries or supersedes it. |
| `canceled` | Not dispatchable; retained only as historical queue state. |

Successful operations are removed from the queue after status is persisted. Use
`status/<path>` to confirm completed sync instead of expecting old successful
operation IDs to remain readable.

Queue operation reads include `trigger`. Ordinary user actions use `user`.
Background drift repair uses `drift-repair`.

## Dispatch Paths

Event-triggered dispatch is enabled by default. After a request durably enqueues
work, the plugin wakes a bounded dispatcher pass so normal sync usually starts
without waiting for the periodic callback.

Event dispatch is still asynchronous. It does not make write responses wait for
provider convergence, and it does not replace durable queue recovery.

Periodic dispatch remains the fallback for:

- missed event wakeups;
- plugin or OpenBao restart recovery;
- retry-wait operations becoming due;
- incomplete enqueue-intent recovery.

`queue/drain` uses the same dispatcher path. It is useful for deterministic
local tests, controlled catch-up, or explicit operator action after event
dispatch was disabled. It can execute remote mutations, so keep it
operator-scoped.

## Ordering

For a single association object, newer source state wins:

- source writes supersede older inactive queued upserts for the same object;
- dispatch rechecks the current source version before mutating a stale upsert;
- status writes reject older versions when newer status already exists;
- providers that can read ownership metadata reject stale mutations when the
  remote object already carries a newer managed source version.

This means old operation IDs can become irrelevant after a newer source write.
When recovering an object, prefer manual association sync if you want to push
the current source version.

## Status States

`status/<path>` is the main convergence view. It reports per-association object
state, source version, operation IDs, error class, remote version, verification,
drift timestamps, repair counters, hints, and next actions.

Common states:

| State | Meaning | Usual next step |
| --- | --- | --- |
| `NO_ASSOCIATION` | No association exists for the source path. | Create an association. |
| `PENDING` | Work is queued or in progress. | Wait, inspect `queue`, or drain when appropriate. |
| `SYNCED` | Remote object matches current local source state according to provider evidence. | None. |
| `DRIFTED` | Remote object is owned but differs from current local source state. | Review, then manual sync or background repair. |
| `REMOTE_MISSING` | Expected remote object does not exist. | Review, then manual sync when recreation is intended. |
| `REMOTE_OWNERSHIP_LOST` | Remote object exists but is not owned by this mount or association. | Inspect or remove remote object, then manual sync. |
| `DESTINATION_AUTH_ERROR` | Provider authentication failed. | Fix credentials or workload identity, then retry or sync. |
| `DESTINATION_POLICY_ERROR` | Provider authorization or destination policy blocked the operation. | Fix IAM/RBAC/token scope or destination prefixes. |
| `DESTINATION_RATE_LIMITED` | Provider rate limit blocked the operation. | Let retry progress or reduce drain pressure. |
| `DESTINATION_UNAVAILABLE` | Provider endpoint or dependency was unavailable. | Wait for recovery, then retry if needed. |
| `VALIDATION_ERROR` | Local association, destination, payload, or provider rule is invalid. | Fix the invalid field and plan again. |
| `QUEUE_BLOCKED` | Queue capacity, restore guard, disabled state, or replication safety blocked work. | Follow `next_actions`. |
| `DISABLED` | Association or destination is disabled. | Enable intentionally before syncing. |
| `UNKNOWN` | Provider could not provide enough comparable state. | Inspect provider docs and reconcile output. |
| `INTERNAL_ERROR` | Plugin-internal failure. | Capture evidence and investigate. |

Some states are temporary only while queued work exists. Others are terminal for
the failed operation but recoverable after the operator changes local config,
fixes provider state, retries the queued operation, or enqueues a fresh manual
sync.

## Manual Sync, Retry, And Cancel

Manual association sync enqueues work for the current source version. Use it
when you want OpenBao to push the current desired state after reviewing status,
reconcile output, remote drift, remote missing state, ownership recovery, or an
association update that did not enqueue work.

Queue retry reattempts one existing queued operation. Use it when the same
operation is still the desired action and the blocking condition has been
fixed.

Queue cancel discards queued work. It does not roll back source state, status,
or remote provider state. Cancel stale work when it should no longer run, then
use manual sync if the current source version should still converge.
