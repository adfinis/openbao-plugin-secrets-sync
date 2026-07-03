# Queue And Dispatch

The outbox stores durable remote-mutation intent. Queue records are local
state; providers are called only after dispatch claims a due outbox operation
and all mutation gates allow the call.

## Outbox States

Supported operation states are:

```text
pending
retry_wait
failed_terminal
canceled
```

Only `pending` and `retry_wait` are dispatchable. Terminal failures remain
visible for operator inspection and retry decisions. Canceled operations are
non-dispatchable historical records until pruned or ignored by queue views.

## Enqueue Intent

Source writes and source lifecycle mutations write enqueue intents before they
write outbox records. Enqueue intents contain the expected operation IDs,
operation types, association IDs, object IDs, and destination references.

Periodic work, queue drains, and event-triggered dispatch recover incomplete
enqueue intents before processing due outbox records. Upsert intents recover
only while the source version is live. Delete intents recover only when the
referenced source version is deleted or unavailable.

The source generation is part of operation identity and idempotency. Deleting
and recreating a source path therefore does not reuse historical operation IDs
when version numbers restart.

## Dispatch Flow

The dispatcher processes due `pending` and `retry_wait` records. For each
operation it:

1. claims the operation;
2. loads the association and destination;
3. validates provider capabilities and destination policy;
4. loads the source version when the operation is an upsert;
5. builds the canonical payload;
6. calls the provider;
7. writes per-object status;
8. prunes successful outbox work or schedules retry/terminal failure.

Dispatch claims are stored on the outbox record with a claim owner, expiry
time, and attempt number. Unexpired claims are skipped. Expired claims are
reclaimable.

The current dispatcher is intentionally sequential. Dispatch claims already
allow safe concurrency, so a future throughput step can add a bounded worker
pool partitioned by destination reference to prevent a slow destination from
serializing unrelated destinations.

## Retry Behavior

Only provider errors classified as `rate_limit` or `unavailable` retry
automatically. Automatic retries use bounded backoff and a finite attempt
count.

Authentication, authorization, validation, ownership, collision, drift,
capacity, and internal failures remain terminal until an operator changes
configuration, retries the queue operation, or runs manual sync for the current
source version.

## Event Dispatch

Event-triggered dispatch wakes the same dispatcher after successful
enqueue-producing requests, such as source writes, association activation,
manual association sync, queue retry, and config changes that resume remote
mutation.

The wakeup is best-effort, coalesced, and bounded by
`event_dispatch_max_operations` per pass. If a pass processes the full limit,
the event dispatcher immediately re-checks mutation gates and runs another
pass until a pass comes back under the limit. This makes the per-pass limit a
fairness boundary instead of a throughput ceiling.

Retry-wait operations arm a timer for the earliest future `not_before` value so
retries do not wait for the periodic tick.

Event dispatch does not make API responses synchronous. Source and association
responses still report queued operation IDs, and periodic processing remains
the recovery path for missed wakeups, plugin restarts, and incomplete enqueue
intents.

## Periodic Dispatch

The OpenBao periodic callback is the recovery path for queue progress. It:

1. checks replication safety;
2. ensures runtime state and schema compatibility;
3. reads mount config;
4. returns early when `disabled=true`;
5. recovers incomplete enqueue intents;
6. runs periodic drift work when configured;
7. returns before dispatch when restore guard is active;
8. processes due outbox operations when mutation gates allow it.

This ordering intentionally allows restore-guarded mounts to detect drift while
still blocking repair enqueue and remote mutation.

## Ordering

For a single association and object, an older source version must not overwrite
a newer source version.

The backend enforces this with three layers:

- source writes supersede older inactive queued upserts for the same
  association object;
- dispatch rechecks the current source version before mutating a stale upsert;
- status writes reject older versions when newer status already exists.

Providers that can read ownership metadata also reject stale mutations when
the remote object carries a newer managed source version.
