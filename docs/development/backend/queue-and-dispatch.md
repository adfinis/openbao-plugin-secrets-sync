---
verifiedBy:
  - internal/backend/enqueue.go
  - internal/backend/association_enqueue.go
  - internal/backend/recovery.go
  - internal/backend/dispatcher.go
  - internal/backend/event_dispatch.go
  - internal/backend/backend.go
  - internal/backend/path_queue.go
  - internal/backend/storage_outbox.go
  - internal/backend/provider_runtime_cache.go
  - internal/backend/dispatcher_claim_test.go
  - internal/backend/dispatcher_retry_test.go
  - internal/backend/dispatcher_timeout_test.go
  - internal/backend/event_dispatch_test.go
  - internal/backend/recovery_test.go
  - internal/backend/terminal_retention_test.go
  - internal/backend/terminal_pruning_concurrency_test.go
---

# Queue and dispatch

The outbox stores durable remote-mutation intent. A provider is called only
after the dispatcher claims a due outbox record and every gate allows it.
User-facing dispatch paths and ordering are in
[Sync model](../../concepts/sync-model.md#dispatch-paths). Queue state
meanings are in
[Status and diagnostics](../../reference/status-and-diagnostics.md#queue-operation-states).

## Enqueue paths

| Producer | Operation | ID | Trigger |
| --- | --- | --- | --- |
| Source write, current-version undelete, `sources/<path>/enable` | `upsert` | deterministic | `user` |
| Current-version delete or destroy, `DELETE data/<path>` | `delete` | deterministic | `user` |
| Association create or enable from disabled | `upsert` | deterministic | `user` |
| Association update that changes desired state | `upsert` | salted | `user` |
| Manual association `sync` | `upsert` | salted | `user` |
| Periodic drift repair | `upsert` | salted | `drift-repair` |

With a deterministic ID, a repeated enqueue for the same generation, version,
association object, and type overwrites the existing record instead of adding
one. Salted IDs create a new record per request. Drift repair skips enqueue when the association
already has a queued upsert for the current version or newer.

Every producer checks queue capacity first. Capacity counts `pending` and
`retry_wait` records, ignores operations that already exist in a queued
state, and subtracts operations the mutation replaces. Exceeding
`queue_capacity` fails the request with a capacity diagnostic. Periodic drift
repair records the block in metrics and skips the object. See
[Runtime settings](../../configure/runtime.md#set-queue-capacity).

## Enqueue intents and recovery

Every producer writes `enqueue_intent/<path>/<version>` before its outbox
records and deletes it afterward. With native storage transactions, source
mutations write and delete the intent inside one transaction. Association
enqueue paths hold `enqueueMu` without a transaction, so an intent can remain
after an interrupted write on any storage.

`recoverEnqueueIntent` handles one intent:

1. Load the referenced version. Upsert operations are recoverable only while
   the version is live. Delete operations are recoverable only when the
   version is soft-deleted, destroyed, or missing.
2. If no operation is recoverable, the source mutation never committed. Delete
   the intent and stop.
3. If the version record exists, repair `metadata/<path>` from it: advance
   `current_version` or restore that version's deletion and destroy state.
4. Delete the intent's `cancel_operation_ids`. A claimed operation fails the
   pass, and a later pass retries.
5. For each recoverable operation whose association still exists, rewrite the
   indexes of an existing outbox record, or write a new `pending` record.
6. Delete the intent.

Recovery runs under `enqueueMu`. The periodic callback and each event dispatch
pass recover at most 100 intents. `queue/drain` recovers all of them.

## Claims

`processDueOutboxLimit` lists `outbox_by_due/` keys up to now in lexical
order and processes them one at a time. The claim owner is
`<mount_uuid>/<worker-id>`, where the worker ID is random per backend
instance. For each candidate, `claimDispatchableOutboxRecord` runs under
`enqueueMu`:

1. Reload the record and require a queued state and a due `not_before`.
2. Delete records with an unknown type or empty object ID.
3. Read `config` and stop if `disabled` or `restore_guard` is set.
4. Skip the record if another claim has not expired.
5. Write `claim_owner`, `claim_expires_time` (now plus 5 minutes), and an
   incremented `claim_attempt`.
6. Read the record back and require the same owner, expiry, and attempt.

Logical storage has no compare-and-swap, so step 6 makes the claim optimistic.
Each operation in a batch computes its own lease start from elapsed time, so a
long batch does not reuse a stale start time. A dispatcher never clears
another worker's unexpired claim. An expired claim is reclaimable.

Queue retry, queue cancel, association disable and delete, and source
mutations that cancel queued work run their checks under `enqueueMu` or a
transaction and reject an operation with an active claim.

## Dispatch flow

`processUpsert` handles a claimed upsert:

1. If the current source version is newer than the operation's version,
   delete the operation under the claim and stop.
2. Load the version. A missing, deleted, or destroyed version fails with
   `internal`.
3. Load the association, destination, and provider. A missing record fails
   with `internal`; an unknown provider, a disabled association or
   destination, or failed hardened source eligibility fails with `validation`.
4. Build the canonical payload. An encoding failure is `validation`; a
   payload over `MaxPayloadBytes` is `capacity`.
5. Resolve the remote name for the object.
6. Check destination policy for the object. A failure is `validation`.
7. Resolve public and sensitive destination config and the runtime identity.
8. Call `Upsert` on the cached runtime with a 2-minute timeout.
9. On success, write `SYNCED` status and delete the operation under the claim.
   A `drift-repair` operation also sets the repair time and increments the
   repair count.

`processDelete` loads the version and fails with `validation` if it is still
live. It requires `delete_mode=delete` on the association, then runs steps 3
and 5 to 8 with `Delete`. On success it writes `REMOTE_MISSING` status and
deletes the operation.

A provider error is classified by its `*providers.Error` class, then by
transport error classification, and otherwise as `internal`. If the dispatch
context is canceled, the pass returns the error without writing status; the
claim expires and the operation is retried later.

## Commit under claim

Provider calls run outside `enqueueMu` and can outlive the claim lease.
`commitIfOutboxClaimHeld` reloads the record under `enqueueMu` and applies
the success, failure, or stale-cancel commit only when the stored claim owner
and attempt still match. Otherwise the result is discarded, because another
worker owns the operation.

## Retry

`markOperationFailed` increments `attempts`. A `rate_limit` or `unavailable`
failure with fewer than 3 attempts moves the record to `retry_wait`; every
other failure, and the third retryable failure, moves it to
`failed_terminal`. The status record gets the mapped sync state, the error
class, and a fixed message such as `provider upsert failed`. Provider error
text is not stored.

The retry delay is 30 seconds after the first failure and doubles with each
further failure, capped at 5 minutes. It adds up to 20 percent deterministic
jitter derived from the operation ID and attempt count. `queue/<operation-id>/retry` resets `attempts` to 0, sets `pending`
with `not_before` of now, and signals event dispatch. User-facing retry rules
are in
[Status and diagnostics](../../reference/status-and-diagnostics.md#error-classes-and-retry).

## Terminal retention

`pruneTerminalOutboxRecords` runs in the periodic callback, including on a
disabled mount, and after every dispatch batch. It keeps the newest 1,000
`failed_terminal` records and removes records older than 7 days, measured
from `updated_time`, or `created_time` if `updated_time` does not parse.

Discovery lists and loads the whole terminal index and sorts it oldest first
without `enqueueMu`, so this scan grows with the backlog. Each pass selects at
most 100 records above the count target and 100 more expired records. Each
candidate then takes `enqueueMu` and a storage transaction when available,
and is deleted only if it still equals the discovered record, is still
terminal, and has no active claim. The lock is released between candidates.
A backlog above either target drains over several passes.

Cancel deletes any `pending`, `retry_wait`, or `failed_terminal` record and
returns a `canceled` snapshot.

## Event dispatch

`Initialize` starts one event dispatch goroutine bound to the initialized
storage handle; cleanup and re-initialize stop it. `signalEventDispatch` sends
on a one-slot channel and drops the signal if one is already pending, so
wakeups coalesce. Signals follow source writes and version mutations that
enqueued work, `sources/<path>/enable`, association create, update, enable,
and manual sync that enqueued work, queue retry, restore guard
acknowledgement, and a `config` write that resumes event dispatch.

Each pass:

1. Stops when the replication state is unsafe.
2. Runs `ensureRuntimeState` and reads `config`.
3. Stops when `event_dispatch_enabled` is false, `disabled` is true, or
   `restore_guard` is true.
4. Recovers up to 100 enqueue intents.
5. Processes up to `event_dispatch_max_operations` due operations (default 16).

If a pass processes the full limit, another pass starts immediately, so the
limit bounds fairness, not throughput. After the last pass, the loop arms a
timer for the earliest future `outbox_by_due/` entry so `retry_wait`
operations run without waiting for the periodic tick. Event dispatch does not
make responses synchronous. See
[Runtime settings](../../configure/runtime.md#tune-event-dispatch).

## Periodic callback

`periodic` in `backend.go` runs in this order:

1. Return when the replication state is unsafe.
2. Run `ensureRuntimeState`.
3. Read `config`.
4. Prune terminal records.
5. Return when `disabled=true`.
6. Recover up to 100 enqueue intents.
7. Run periodic drift work when `drift_repair` is not `off`.
8. Return when `restore_guard=true`.
9. Process up to 100 due operations.

Drift work runs before the restore guard return, so a guarded mount still
refreshes drift status while repair enqueue and dispatch stay blocked. See
[Reconcile and drift](reconcile-and-drift.md).

## Queue drain

`queue/drain` rejects the request with a diagnostic when `disabled` or
`restore_guard` is set or the replication state is unsafe. It then recovers
all enqueue intents and processes up to `max_operations` due operations
(default 100). It is forwarded from performance standbys; see
[Request lifecycle](request-lifecycle.md#ha-request-forwarding).

## Concurrency

Each dispatch pass is sequential. Periodic, event, and drain passes can
overlap in one process, and claims decide which pass owns an operation.

## Provider runtime cache

`provider_runtime_cache.go` caches one runtime per destination reference,
keyed by a fingerprint of the resolved config, public and sensitive. A
mismatched fingerprint builds a new runtime. Concurrent callers wait for one
in-flight build. A runtime built while an invalidation raced is returned to
its caller and closed after use, not cached.

Destination writes and deletes drop that destination's entry directly. The
`Invalidate` hook drops the entry for a `destinations/<type>/<name>` or
`destinations_secrets/<type>/<name>` key and clears the whole cache for any
other key under those prefixes. Backend cleanup clears the cache and closes
cached runtimes. When OpenBao disables
caching, each call opens and closes its own runtime. The cache is an
optimization; correctness comes from storage, claims, provider idempotency,
and provider ownership checks.
