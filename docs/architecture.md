# Architecture

Status: draft
Date: 2026-06-30

## Plugin Boundary

The design assumes the current OpenBao external plugin model:

- the plugin is a separate process managed by OpenBao;
- the plugin receives a mount-scoped storage view, not a global storage view;
- the plugin is invoked through logical backend request paths;
- OpenBao core remains responsible for authentication, authorization, audit
  request handling, plugin lifecycle, routing, and sealing;
- the plugin cannot transparently observe writes to unrelated mounts.

This boundary is deliberate. The project is a sync-aware secret engine, not a
drop-in implementation of `/sys/sync`.

The plugin should use `ServeMultiplex` so a single plugin binary can serve
multiple mounts. All per-mount state remains scoped by backend UUID, mount
identity, and OpenBao storage.

## Component Model

```text
+--------------------------------------------------------------------------+
| openbao-plugin-secrets-sync                                              |
|                                                                          |
| API paths                                                                |
|   data/*, metadata/*, destinations/*, associations/*, status/*, queue/*  |
|                                                                          |
| Core services                                                            |
|   KV store                  Version metadata store                       |
|   Destination registry      Association registry                         |
|   Outbox queue              Sync dispatcher                              |
|   Reconciler                Status store                                 |
|   Provider registry         Validation and planning service              |
|   Capability evaluator      Redaction and error classifier               |
|                                                                          |
| Storage                                                                  |
|   config, schema, identity, source versions, associations, outbox, status |
+------+------------------+---------------------+-------------------------+
       |                  |                     |
       v                  v                     v
 AWS Secrets Manager   Kubernetes Secrets   Future providers
```

Providers receive prepared requests and resolved destination configuration.
They never receive OpenBao request objects and must never log secret values.

## Storage Model

All paths are relative to the plugin storage view.

```text
config/global
config/limits
schema/version
identity/plugin-instance
identity/restore-epoch

data/<path>/versions/<version>
metadata/<path>
metadata_index/<prefix>

destinations/<type>/<name>
destinations_secrets/<type>/<name>

associations/<normalized-path>/<association-id>
associations_by_destination/<type>/<name>/<association-id>
association_names/<destination-ref>/<resolved-name>/<association-id>

outbox/<operation-id>
outbox_by_due/<timestamp>/<operation-id>
outbox_by_path/<normalized-path>/<operation-id>
outbox_by_association/<association-id>/<operation-id>
enqueue_intent/<normalized-path>/<version>

status/<normalized-path>/<association-id>/<object-id>
status_by_destination/<type>/<name>/<association-id>/<object-id>

reconcile_cursors/<scope>
locks/<lock-name>
```

### Schema And Identity

`schema/version` records the storage schema understood by the plugin binary. The
backend initializes this record on first storage-backed request. If the stored
schema requires a newer incompatible plugin, request handling and periodic
processing fail closed with a clear operator error before source or remote
mutation.

`identity/plugin-instance` is generated once per mount and exposed through
`config` reads. Provider requests carry it, and providers include it in
ownership metadata where supported. This helps distinguish two OpenBao mounts
using the same remote destination.

`identity/restore-epoch` is generated once per mount and rotates when an active
restore guard is acknowledged. Provider requests carry it, and providers
include it in remote ownership metadata where supported.

### Secret Version Record

```json
{
  "version": 4,
  "created_time": "2026-06-30T12:00:00Z",
  "created_by": {
    "entity_id": "entity-id",
    "display_name": "user"
  },
  "data": {
    "username": "app",
    "password": "secret"
  },
  "deletion_time": "",
  "destroyed": false
}
```

### Metadata Record

```json
{
  "current_version": 4,
  "oldest_version": 1,
  "max_versions": 10,
  "cas_required": true,
  "delete_version_after": "0s",
  "custom_metadata": {
    "owner": "platform",
    "syncable": "true"
  }
}
```

### Association Record

```json
{
  "id": "assoc_01",
  "path": "app/db",
  "destination": {
    "type": "aws-sm",
    "name": "prod"
  },
  "name_template": "{{ mount }}/{{ path }}",
  "resolved_name": "sync-kv/app/db",
  "granularity": "secret-path",
  "format": "json",
  "enabled": true,
  "created_time": "2026-06-30T12:00:00Z",
  "source_eligibility": {
    "mode": "metadata",
    "require_custom_metadata": {
      "syncable": "true"
    }
  }
}
```

### Outbox Record

```json
{
  "id": "op_01",
  "type": "upsert",
  "path": "app/db",
  "version": 4,
  "association_id": "assoc_01",
  "object_id": "secret-path",
  "destination": {
    "type": "aws-sm",
    "name": "prod"
  },
  "state": "pending",
  "attempts": 0,
  "not_before": "2026-06-30T12:00:00Z",
  "last_error_class": "",
  "last_error": "",
  "created_time": "2026-06-30T12:00:00Z",
  "updated_time": "2026-06-30T12:00:00Z",
  "idempotency_key": "sync-kv:app/db:4:assoc_01:secret-path:upsert"
}
```

### Status Record

```json
{
  "path": "app/db",
  "version": 4,
  "association_id": "assoc_01",
  "object_id": "secret-path",
  "destination": {
    "type": "aws-sm",
    "name": "prod"
  },
  "resolved_name": "sync-kv/app/db",
  "state": "SYNCED",
  "payload_sha256": "sha256:...",
  "remote_version": "provider-version",
  "last_operation_id": "op_01",
  "last_success_time": "2026-06-30T12:00:03Z",
  "last_error_class": "",
  "last_error": ""
}
```

`object_id` is required because a single association can produce many remote
objects when using `secret-key` granularity.

## Write Consistency

The write path must avoid a gap where the local version is committed but sync
intent is lost. If OpenBao storage transactions are available for the target
minimum version, use them for metadata, version, enqueue intent, and outbox
creation.

If transactions are not available, use a recoverable state:

1. acquire per-path write lock;
2. validate CAS and compute next version;
3. write version record;
4. write `enqueue_intent/<path>/<version>` with expected associations;
5. create outbox records;
6. mark enqueue intent complete;
7. update metadata current version;
8. release lock.

The reconciler must scan incomplete enqueue intents and committed versions to
recreate missing outbox records. This makes crash recovery explicit.

## Operation State Machine

```text
pending -> claimed -> applying -> succeeded
                     -> retry_wait
                     -> failed_terminal
                     -> canceled

retry_wait -> pending
claimed    -> pending        when claim expires
applying   -> pending        when claim expires and provider operation is idempotent
```

The dispatcher persists claim owner, expiry, and attempt metadata directly on
the outbox record rather than exposing `claimed` as a separate public operation
state. Due `pending` and `retry_wait` records with an unexpired claim are
skipped; expired claims are reclaimable. Automatic retry is reserved for
provider `rate_limit` and `unavailable` classes, with a bounded attempt budget
and `not_before` delay. Manual queue retry moves canceled, retry-wait, or
terminal failed work back to `pending` and resets the attempt counter.

The `queue/drain` path runs the same due-operation dispatcher as background
work with a request-bounded operation limit. It first checks global mutation
safety gates, including `disabled`, `restore_guard`, and OpenBao replication
state, then recovers incomplete enqueue intents and returns a queue summary
without exposing source payload data. The path exists for deterministic tests,
operator-controlled catch-up, and break-glass workflows; normal progress should
come from the periodic function.

Source delete uses the same durable outbox model. Deleting the latest local
version cancels queued upsert work for that version. Associations with
`delete_mode=delete` enqueue provider delete operations; other delete modes
leave the remote object untouched. Delete enqueue intent recovery is
type-aware: upsert intents recover only while the source version is live,
delete intents recover only after the source version is deleted.

Claims include owner, expiry, and attempt number. In-memory locks are only an
optimization. Correctness comes from durable claims, idempotency keys, and
provider-side version or ownership checks.

## Ordering

For a single association and object, operations must not allow an older source
version to overwrite a newer source version.

Allowed strategies:

- block processing of version N+1 until version N is terminal;
- allow newer versions to supersede older pending operations before dispatch;
- provider compares desired source version metadata before upserting.

The MVP should prefer superseding stale pending operations before dispatch and
provider-side version checks where supported.

## Queue Capacity And Backpressure

Global configuration must define queue capacity. When the queue is full, the
write path must return a clear error before accepting a new source version, or
must accept the version only if enqueue intent recovery guarantees later queue
creation. The MVP should fail the write before committing the source version
when capacity is known to be exceeded.

Queue listing should expose:

- total pending operations;
- oldest pending operation age;
- retry-wait operation count;
- terminal failure count;
- per-destination counts;
- capacity and remaining capacity.

## Background Work

Periodic function pseudo-flow:

```text
if global disabled:
  return
if restore guard active:
  return
if not writable cluster state:
  return

recover_incomplete_enqueue_intents(limit=A)
process_due_outbox(limit=B)
enqueue_reconciliation_work_if_due(limit=C)
process_due_reconciliation(limit=D)
```

Outbox processing:

```text
load due operation
claim operation
load association
load destination
load provider capabilities
load source version and metadata
build canonical payload
plan or read remote state if safety policy requires it
apply provider upsert or delete
write per-object status
mark operation done, retry, or terminal
```

## Reconciliation

Reconciliation is bounded and resumable:

- per-destination cursor;
- per-path narrow reconcile;
- per-mount limits;
- configurable concurrency;
- operator-triggered dry-run plan.

The first implementation provides manual per-path reconcile. The plan path only
reads provider state; the apply path writes local status but still does not
mutate destination objects. Reconciliation should detect missing outbox entries
and stale or missing remote
objects. It should enqueue operations rather than mutating many remote objects
inside the scan.

## HA And Replication

The plugin should assume:

- writes route to the active OpenBao node;
- storage writes can return read-only errors on non-active nodes;
- periodic work should run only where writes are safe;
- performance secondary and DR secondary behavior must be guarded using
  OpenBao replication state.

Periodic processing should check `System().ReplicationState()` and
`System().LocalMount()` before writing queue or status records.

## Backend Construction

The backend should use the OpenBao framework backend.

Key construction choices:

- `BackendType: logical.TypeLogical`
- `RunningVersion: pluginVersion`
- `PathsSpecial.SealWrapStorage`: destination sensitive config and optional
  local secret data prefixes
- `PathsSpecial.LocalStorage`: runtime locks only if they must not replicate
- `PeriodicFunc`: outbox processing and reconciliation scheduler
- `WALRollback`: only where provider operations need explicit compensation
- `Invalidate`: clear destination, association, and credential caches

## Storage Abstraction

Create a small internal store layer over `logical.Storage`:

```go
type Store struct {
    storage logical.Storage
}

func (s *Store) PutSecretVersion(ctx context.Context, path string, rec VersionRecord) error
func (s *Store) GetSecretVersion(ctx context.Context, path string, version int) (*VersionRecord, error)
func (s *Store) PutMetadata(ctx context.Context, path string, rec MetadataRecord) error
func (s *Store) ListMetadata(ctx context.Context, prefix string) ([]string, error)
func (s *Store) PutOutbox(ctx context.Context, op Operation) error
func (s *Store) ClaimDueOperations(ctx context.Context, now time.Time, limit int) ([]Operation, error)
```

Use structured JSON records initially. Avoid ad hoc string parsing except for
normalized storage key construction.
