---
verifiedBy:
  - internal/backend/backend.go
  - internal/backend/dispatcher.go
  - internal/backend/storage_status.go
  - internal/backend/enqueue.go
  - internal/providers/provider.go
  - internal/backend
  - internal/payload
  - internal/domain
  - internal/outbox
---

# Architecture

Secret Sync is an OpenBao external secret-engine plugin
(`openbao-plugin-secrets-sync`). It stores source secrets in its own mount and
pushes them asynchronously to external destinations through a durable outbox.

## Plugin boundary

OpenBao runs the plugin as a separate process and routes logical requests to
it. OpenBao remains responsible for:

- authentication and authorization;
- audit request handling;
- plugin lifecycle and routing;
- seal and storage encryption;
- replication and active-node routing.

The plugin receives a mount-scoped storage view. It cannot read global storage
or observe writes to other mounts, so source data must be written to this
mount. Secret Sync is not a drop-in implementation of `/sys/sync`. See
[Sync model](../concepts/sync-model.md#source-data-lives-in-the-mount).

`backend.go` builds a `framework.Backend` with `logical.TypeLogical`. Setup
registers the path handlers, the seal-wrapped storage prefixes, and the
invalidation, cleanup, initialize, and periodic hooks.

Do not add source ingest that calls other mounts through a localhost API
client, a managed token, or a network loopback. If OpenBao ships a stable
[cross-plugin communication](https://gist.github.com/cipherboy/eb2dfa598615ac5c510c534ca383d3ba)
API, source adapters must enter at this boundary and keep the outbox, provider
interface, and confused-deputy controls inside the mount.

## Component model

```text
API request
  |
  v
path handlers ---- write locks, schema gate, policy and eligibility checks
  |
  v
mount storage: source versions, metadata, destinations, associations,
               enqueue intents, outbox, status
  |
  v
dispatcher <---- periodic callback, event wakeup, queue/drain
  |  claim, gate checks, canonical payload, commit under claim
  v
provider runtime (cached per destination)
  |
  v
AWS Secrets Manager | Kubernetes Secrets | GitLab project variables
```

Request handlers never call a provider to mutate remote state. They commit
local state and outbox records, then optionally wake the event dispatcher.
Provider mutation happens only in the dispatcher. Provider reads happen in
manual reconcile, periodic drift work, association plan, and destination
check and health paths.

## Provider boundary

The backend builds canonical provider requests after it has checked OpenBao
state, destination policy, association configuration, and provider capability
flags. Providers receive resolved destination configuration, runtime identity
(`mount_uuid` and restore epoch), resolved remote names, ownership fields, and
prepared payload bytes. They never receive OpenBao request objects and never
make OpenBao policy decisions.

Providers implement destination-specific behavior only: config validation,
association config normalization, capabilities, health, plan, read-state,
upsert, and owned delete. The interface, capability flags, and error classes
are owned by [Provider contract](provider-contract.md). The steps to add a
provider are in [Provider implementation](provider-implementation.md).

## Consistency model

The Secret Sync mount is the source of truth. A source write commits the
source version and its outbox intent before any remote mutation. API responses
report queued operation IDs; only status and reconcile report observed remote
state. See
[Accepted is not synced](../concepts/sync-model.md#accepted-is-not-synced).

For one association object, an older source version must not overwrite a
newer one. The backend enforces this in three layers:

- a source write removes unclaimed queued upserts for older versions of the
  same association object (`staleQueuedUpsertIDsForOperations`);
- dispatch drops an upsert whose version is older than the current source
  version (`staleUpsertForCurrentVersion`);
- `putStatus` ignores a status write whose version is older than the stored
  status.

Providers that read ownership metadata also reject stale or unowned remote
mutation. Queue processing is recoverable: the periodic callback is the
fallback for missed event wakeups, plugin restarts, retry-wait operations, and
incomplete enqueue intents. Details are in
[Queue and dispatch](backend/queue-and-dispatch.md).

## Code map

Backend code lives in `internal/backend`.

| File | Responsibility |
| --- | --- |
| `backend.go` | Factory, framework wiring, path list, seal-wrap prefixes, schema gate in `HandleRequest`, replication check, periodic callback. |
| `runtime_state.go` | Storage schema record, restore epoch creation and rotation, provider runtime identity. |
| `storage_records.go` | Storage key prefixes, record shapes, defaults, source path normalization, key builders, association and operation ID derivation. |
| `storage_source.go` | Metadata, version, and enqueue-intent records; version pruning; source path deletion. |
| `storage_destination.go` | Public destination records and versioned sensitive records. |
| `storage_association.go` | Association records, destination and name-reservation indexes, index-tolerant lookups. |
| `storage_outbox.go` | Outbox records and indexes, due and state queries, claim-aware cancellation, terminal pruning. |
| `storage_status.go` | Status writes with version ordering and drift bookkeeping. |
| `storage_transactions.go` | Native storage transaction, or `enqueueMu` fallback, for source mutations. |
| `write_locks.go` | Striped locks for source paths, name reservations, and destinations. |
| `existence_checks.go` | Create versus update ACL classification. |
| `path_info.go` | `info`: plugin version, association defaults, provider capabilities. |
| `path_config.go` | `config`, restore guard acknowledgement, config defaults and decoding. |
| `path_data.go`, `path_metadata.go`, `path_versions.go`, `path_sources.go` | Source data writes and reads, metadata, version lifecycle, source sync check, enable, and disable. |
| `path_destinations.go` | Destination CRUD, check, validate, health, policy prefixes. |
| `path_associations.go` | Association CRUD, enable, disable, manual sync, field defaults, capability checks, name resolution, reservations. |
| `association_plan.go` | Association plan through provider `Plan`. |
| `association_policy.go` | Activation preconditions, destination policy prefixes, hardened destination constraints. |
| `association_enqueue.go` | Enqueue current version for activation, manual sync, config change, and drift repair; `DISABLED` status on disable. |
| `granularity.go` | Object IDs, remote name rendering, data-key templates. |
| `enqueue.go` | Outbox record construction, operation IDs, idempotency keys, capacity checks, stale-upsert detection. |
| `recovery.go` | Enqueue-intent recovery. |
| `dispatcher.go` | Claims, upsert and delete processing, payload preparation, retry, commit under claim. |
| `event_dispatch.go` | Event wakeup goroutine and retry timer. |
| `path_queue.go` | Queue summary, drain, read, retry, cancel; terminal retention limits. |
| `path_reconcile.go` | Manual reconcile plan and apply, remote-state mapping, reconcile status writes. |
| `periodic_reconcile.go` | Periodic drift candidate selection, batching, repair enqueue. |
| `path_status.go` | Per-path status read. |
| `diagnostics.go`, `response.go` | `hint` and `next_actions` builders, response maps. |
| `metrics.go` | Observer helpers for readiness, blocked mutation, and health events. |
| `provider_runtime_cache.go` | Destination runtime construction, caching, invalidation, cleanup. |
| `path_pagination.go` | `after` and `limit` list fields. |
| `api_schemas.go` | OpenAPI response schemas and operation prefix. |

Other packages:

- `internal/providers` holds the provider contract, registry, transport error
  classification, and the provider implementations.
- `internal/payload` builds canonical payload bytes and hashes.
- `internal/domain` defines the external sync states.
- `internal/outbox` defines the operation types `upsert` and `delete`.
- `internal/observability` records metrics; see
  [Observability internals](observability.md).
