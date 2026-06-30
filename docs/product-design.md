# Product Design

Status: draft
Date: 2026-06-30

## Product Intent

`openbao-plugin-secrets-sync` provides a sync-aware KV-v2-like secret engine for
OpenBao. Operators mount it where they want secrets to be eligible for external
synchronization. Application teams write secrets to that mount. Platform teams
configure destinations and associations that synchronize selected source
secrets to external systems.

OpenBao remains the source of truth. Destination systems receive copies for
runtime integration, CI/CD integration, cloud-native consumption, or migration
support. Destination state must not become authoritative local OpenBao state.

## Goals

- Provide a production-oriented MVP for one-way secret sync from OpenBao to
  external destinations.
- Avoid OpenBao core changes and avoid depending on upstream contribution.
- Preserve OpenBao as the source of truth for synced secrets.
- Make writes safe by default: durable local write first, then asynchronous
  destination sync.
- Provide clear operational status, retry behavior, reconciliation, and failure
  classification.
- Protect destination credentials and synced secret material from logs, status
  responses, metrics, errors, and tests.
- Make destination providers extensible without rewriting the engine.
- Make collision, drift, restore, and partial-success behavior explicit.

## Non-Goals

- Transparent sync of arbitrary existing `kv/` mounts in the MVP.
- Bidirectional sync.
- A generic automation engine.
- Replacing OpenBao policy, audit, or lease semantics.
- Strong exactly-once delivery to external services.
- A full UI in the MVP.
- Storing external destination state as authoritative truth.
- Reimplementing Vault Enterprise `/sys/sync`.

## Design Principles

### Do Not Clone Vault

Vault Enterprise Secret Sync is useful prior art, but it is not the product
contract for this plugin. The OpenBao design must be judged on the plugin model
and on safer open-source operator workflows.

The plugin intentionally differs from Vault-style system sync:

- it is a mount-scoped external plugin, not a core `/sys/sync` feature;
- source secrets are opt-in through this engine's data model;
- association activation requires source eligibility and destination authority;
- remote overwrite requires ownership proof by default;
- all remote mutations can be planned before execution;
- provider behavior is declared through capabilities;
- restore, clone, partial success, and drift states are visible;
- operational diagnostics are part of the MVP, not a later polish item.

### OpenBao Is The Source Of Truth

Reads from `data/*` return local OpenBao data only. The plugin never treats a
remote destination value as the source of truth. Remote reads are allowed for
planning, validation, reconciliation, drift detection, and diagnostics.

### Safe Remote Mutation By Default

The default collision policy is `overwrite_owned_only`. A remote object is
owned only when provider-specific metadata proves it was created or last
managed by this plugin association. The plugin must not overwrite an unrelated
remote secret by default.

### Plan Before Mutation

Association creation, manual sync, deletion, and reconciliation must support a
plan path. A plan reports intended remote names, action type, ownership result,
provider limitations, validation errors, and expected status changes without
writing remote state.

### Capability-Declared Providers

Providers do not all support tags, version comparison, value readback, payload
hashing, or transactional updates. The core engine should not pretend they do.
Each provider declares capabilities and the engine derives allowed safety modes
from those capabilities.

### Recoverability Over Inline Cleverness

Correctness must come from durable source versions, durable sync intent,
idempotent operations, and reconciliation. Opportunistic inline sync is allowed
only as an optimization.

## External Behavior

### Write Path

For a write to `sync-kv/data/app/db`:

1. OpenBao authenticates and authorizes the request for the plugin path.
2. The plugin validates the request body and write options.
3. The plugin checks CAS when configured or supplied.
4. The plugin writes a new local version and durable sync intent.
5. The plugin resolves active associations for the source path.
6. The plugin creates one or more durable outbox operations.
7. The plugin returns success after local state and sync intent are
   recoverable.
8. Periodic processing and reconciliation drive destination state to the
   desired version.

Destination outages should not block local writes unless a future explicit
`sync_required=true` mode is enabled.

### Read Path

`GET data/<path>` reads only the local source version. The response must not
include remote secret values. Metadata and status paths may include sync state,
versions, hashes, destination references, and redacted errors.

### Delete Path

Deletion policy is explicit per association or destination:

- `retain`: delete or mark local source state only; leave remote objects intact.
- `delete`: delete remote objects only when ownership can be proven.
- `orphan`: remove association and stop managing remote objects.

Current MVP source-delete behavior:

- association `delete_mode` defaults to `retain`;
- deleting the latest local source version cancels queued upserts for that
  source version;
- `delete_mode=delete` enqueues a provider delete operation for enabled
  associations whose destination is active;
- successful provider delete sets object status to `REMOTE_MISSING`;
- `retain` and `orphan` do not mutate remote objects on source delete.

The plugin must distinguish:

- deleting a source secret version;
- deleting all source metadata;
- deleting an association;
- deleting a destination;
- deleting a remote object.

Destination deletion must fail while active associations exist unless the
request explicitly chooses `orphan`, `retain`, or `delete_owned` behavior.

### Reconciliation Path

The reconciler detects:

- local version not synced remotely;
- missing outbox operations for committed local versions;
- remote secret missing;
- remote secret present but not owned by this plugin association;
- remote version or payload hash differs from expected value;
- destination credentials invalid;
- destination API unavailable;
- destination object blocked by provider policy;
- queue capacity or retry exhaustion conditions.

Reconciliation scans are bounded, resumable, and enqueue work instead of doing
unbounded remote mutation inline.

## API Shape

The source-secret API should feel familiar to KV-v2 users without claiming
drop-in KV-v2 client compatibility. The compatibility target is documented in
[API compatibility](api-compatibility.md).

### Secret Data

```text
POST   data/<path>       write new version
PATCH  data/<path>       partial update, optional after MVP
GET    data/<path>       read latest or selected local version
DELETE data/<path>       soft-delete latest local version
POST   delete/<path>     soft-delete selected local versions
POST   metadata/<path>   create or update local metadata policy
LIST   metadata/<path>   list local secrets
GET    metadata/<path>   read local metadata and sync summary
POST   undelete/<path>   undelete versions
POST   destroy/<path>    permanently destroy versions
DELETE metadata/<path>   delete all local metadata and versions
```

Write example:

```json
{
  "data": {
    "username": "app",
    "password": "secret"
  },
  "options": {
    "cas": 3
  },
  "sync": {
    "require_association": false
  }
}
```

### Destinations

```text
POST   destinations/<type>/<name>
GET    destinations/<type>/<name>
LIST   destinations/<type>
DELETE destinations/<type>/<name>
POST   destinations/<type>/<name>/validate
GET    destinations/<type>/<name>/health
POST   destinations/<type>/<name>/rotate-credentials
```

Destination reads must redact sensitive fields. Destination writes must reject
unsupported safety modes based on provider capabilities.

### Associations

```text
POST   associations/<path>
GET    associations/<path>
LIST   associations
DELETE associations/<path>/<association-id>
POST   associations/<path>/plan
POST   associations/<path>/<association-id>/sync
POST   associations/<path>/<association-id>/disable
POST   associations/<path>/<association-id>/enable
```

Association activation requires:

- the caller is authorized to manage the association path;
- the source path is eligible for sync;
- the association would resolve to valid remote names;
- the destination allows the chosen safety, format, and granularity;
- provider validation succeeds or is explicitly deferred.

Source eligibility should be enforced with at least one of:

- caller has read permission for `data/<path>` at creation/update time;
- source metadata contains required `custom_metadata.syncable=true`;
- an operator-only path creates the association on behalf of source owners.

Association lifecycle controls are per association, not per source path:

- disable sets the association inactive, cancels queued work for that
  association, and marks its object status `DISABLED`;
- enable revalidates source eligibility and destination availability before
  queueing the current source version;
- manual sync revalidates the same activation gates and queues the current
  source version for an enabled association.

### Status And Operations

```text
GET  status/<path>
GET  status/destinations/<type>/<name>
GET  queue
GET  queue/<operation-id>
POST queue/<operation-id>/retry
POST queue/<operation-id>/cancel
GET  metrics
```

Status must be per resolved remote object, not only per association. This is
required for `secret-key` granularity and partial provider failures.

Stable status states:

```text
UNKNOWN
PENDING
SYNCING
SYNCED
UNSYNCED
DRIFTED
REMOTE_MISSING
REMOTE_OWNERSHIP_LOST
DESTINATION_AUTH_ERROR
DESTINATION_POLICY_ERROR
DESTINATION_RATE_LIMITED
DESTINATION_UNAVAILABLE
VALIDATION_ERROR
QUEUE_BLOCKED
INTERNAL_ERROR
DISABLED
```

### Global Configuration

```text
GET  config
POST config
```

Configuration should include:

- `disabled`: pause all background remote mutations;
- `restore_guard`: require explicit resume after restore or clone detection;
- `queue_capacity`: maximum pending operations;
- `default_collision_policy`;
- `default_delete_policy`;
- `default_rate_limits`;
- `required_source_metadata`;
- `destination_allowlist`.

## MVP Product Decisions

- Build as a sync-aware secret engine, not a global sync service.
- Include CAS in MVP because concurrent local writes directly affect remote
  correctness.
- Include queue capacity and global pause in MVP because they shape write
  semantics.
- Include restore guard in MVP because restored storage can damage remote state.
- Include source eligibility checks in MVP to avoid confused-deputy sync.
- Include dry-run planning in MVP for association activation and manual sync.
- Defer `sync_required=true` until basic async semantics are proven.
- Defer brownfield sync from existing KV mounts to a separate controller.
