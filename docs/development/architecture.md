# Architecture

This page is the durable architecture overview for
`openbao-plugin-secrets-sync`. Use it to understand the plugin boundary and
the major implementation areas. Use the backend pages when you need storage
keys, request-path behavior, queue processing, reconcile behavior, or safety
invariants.

## Plugin Boundary

Secret Sync is an OpenBao external secret-engine plugin. OpenBao runs the
plugin as a separate process and routes logical backend requests to it.

OpenBao remains responsible for:

- authentication and authorization;
- audit request handling;
- plugin lifecycle and routing;
- seal and storage encryption;
- replication and active-node routing.

The plugin receives a mount-scoped storage view. It does not receive global
storage and cannot observe writes to unrelated mounts. Secret Sync therefore
stores source data in its own mount and synchronizes that source data to
external destinations. It is not a drop-in implementation of `/sys/sync`.

The plugin uses the OpenBao framework backend with `logical.TypeLogical`.
Backend setup registers path handlers, seal-wrapped storage prefixes,
invalidation hooks, cleanup hooks, and periodic work.

## Component Model

```text
+--------------------------------------------------------------------------+
| openbao-plugin-secrets-sync                                              |
|                                                                          |
| API paths                                                                |
|   info, config, sources, data, metadata, destinations, associations,     |
|   queue, status, reconcile                                               |
|                                                                          |
| Core services                                                            |
|   Source store              Source metadata store                        |
|   Destination registry      Association registry                         |
|   Outbox queue              Sync dispatcher                              |
|   Event dispatch wakeups    Periodic drift work                          |
|   Status store              Runtime identity                             |
|   Provider registry         Runtime cache                                |
|   Capability validation     Redaction and error classification           |
|   Observability recorder    Response diagnostics                         |
+------+------------------+---------------------+-------------------------+
       |                  |                     |
       v                  v                     v
 AWS Secrets Manager   Kubernetes Secrets   GitLab project variables
```

Providers receive resolved destination configuration, runtime identity, remote
names, ownership fields, and prepared payloads. Providers never receive OpenBao
request objects and never make OpenBao policy decisions.

## Backend Internals

Backend implementation details live under [backend](backend/README.md):

- [Storage model](backend/storage-model.md) covers storage prefixes, schema
  state, runtime identity, source records, destination records, association
  records, outbox records, and status records.
- [Request lifecycle](backend/request-lifecycle.md) covers path ownership,
  source writes, source lifecycle mutations, association activation, destination
  policy checks, and payload preparation.
- [Queue and dispatch](backend/queue-and-dispatch.md) covers enqueue intents,
  outbox states, dispatch claims, retry behavior, event-triggered dispatch,
  and operation ordering.
- [Reconcile and drift](backend/reconcile-and-drift.md) covers manual
  reconcile, periodic drift detection, periodic drift repair, and the mutation
  gates that apply to each path.
- [Safety and diagnostics](backend/safety-and-diagnostics.md) covers safety
  invariants, restore guard behavior, disabled behavior, replication checks,
  ownership checks, redaction, hints, and `next_actions`.

Keep this page focused on stable architecture. Put implementation details in
the narrower backend documents so stale sections are easier to review.

## Provider Boundary

The backend builds canonical provider requests after it has checked OpenBao
state, destination policy, association configuration, and provider capability
flags. Providers implement destination-specific behavior only:

- validating destination configuration;
- reporting destination capabilities;
- planning remote state for one resolved object;
- reading provider state for status and reconcile;
- upserting prepared payload bytes;
- deleting owned remote objects when supported.

The provider contract is documented in
[Provider contract](provider-contract.md). The implementation checklist for new
providers is documented in
[Provider implementation guide](provider-implementation.md).

## Consistency Model

The source of truth is the Secret Sync mount. Local source writes commit
versioned source data and durable outbox intent before remote mutation occurs.
Remote mutation is asynchronous: API responses report queued operation IDs and
status/reconcile paths report observed remote state.

The backend preserves ordering for a single association object by canceling or
skipping stale queued upserts, checking source version again during dispatch,
and rejecting older status writes when newer status already exists. Providers
that can read ownership metadata also reject stale or unowned remote mutation.

Queue processing is recoverable. Periodic work remains the fallback for missed
event wakeups, plugin restarts, retry-wait operations, and incomplete enqueue
intents.

## Future Cross-Plugin Communication

OpenBao has a
[cross-plugin communication RFC](https://gist.github.com/cipherboy/eb2dfa598615ac5c510c534ca383d3ba)
that proposes hook-driven and internal request paths between plugins. If that
API becomes stable, Secret Sync can use it as an optional source-ingest path
from other OpenBao secret engines.

The architecture does not depend on that RFC. Secret Sync owns its source data,
outbox, provider interface, reconcile model, and confused-deputy controls
inside its own mount. A future integration should add cross-plugin source
adapters at the boundary and should not use localhost API clients, manually
managed tokens, or network loopback calls between plugins.
