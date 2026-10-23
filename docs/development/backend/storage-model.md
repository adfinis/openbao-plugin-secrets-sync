---
verifiedBy:
  - internal/backend/storage_records.go
  - internal/backend/storage_source.go
  - internal/backend/storage_destination.go
  - internal/backend/storage_association.go
  - internal/backend/storage_outbox.go
  - internal/backend/storage_status.go
  - internal/backend/runtime_state.go
  - internal/backend/path_config.go
  - internal/backend/path_destinations.go
  - internal/backend/backend.go
  - internal/backend/storage_golden_test.go
  - internal/backend/storage_index_test.go
---

# Storage model

The backend stores all durable state in the mount-scoped OpenBao storage view.
Keys below are relative to that view. `<destination-ref>` is `<type>/<name>`.

```text
config
schema/version
identity/restore-epoch

data/<path>/versions/<version>
metadata/<path>

destinations/<type>/<name>
destinations_secrets/<type>/<name>/versions/<sensitive-version>
destinations_secrets/<type>/<name>                      (legacy, unversioned)

associations/<path>/<association-id>
associations_by_destination/<type>/<name>/<association-id>
association_names/<destination-ref>/<reservation-hash>/<association-id>

enqueue_intent/<path>/<version>

outbox/<operation-id>
outbox_by_due/<due-time>/<operation-id>
outbox_by_path/<path>/<operation-id>
outbox_by_state/<state>/<operation-id>

status/<path>/<association-id>/<object-id>
```

Changing any key layout, prefix, persisted state string, or sentinel is a
storage migration, not a refactor.

## Seal-wrapped prefixes

`backend.go` lists two prefixes in `PathsSpecial.SealWrapStorage`:

- `data/` holds source version records, which contain source payload values.
- `destinations_secrets/` holds sensitive destination configuration.

Source payload values must stay under `data/`. Sensitive destination fields
must stay under `destinations_secrets/`. All other records can hold
operational metadata such as source paths, association IDs, remote names,
versions, operation IDs, payload hashes, and error classes. They must not hold
source payload values or sensitive destination config.

## Schema state

`schema/version` stores `version`, `min_compatible_version`, and timestamps.
`ensureRuntimeState` creates the schema record, the restore epoch, and the
default `config` record when they are missing. It runs in `Initialize`, before
every non-help request in `HandleRequest`, in the periodic callback, and in each
event dispatch pass.

`validateStorageSchemaVersions` fails closed when the stored `version` is below
`minSupportedStorageSchema` or when `min_compatible_version` is above
`currentStorageSchema`. A failing request returns an error response before any
path handler runs. Periodic and event dispatch stop before recovery, drift
work, or dispatch. A record with `min_compatible_version` of `0` is treated as
its own `version`.

Schema policy before the first stable release:

- the current schema is v1, and both constants in `storage_records.go` are `1`;
- request paths do not run automatic storage migrations;
- records are decoded with tolerant JSON readers, so an older binary silently
  drops fields that a newer binary added when it rewrites a record;
- any added, removed, renamed, or semantics-changing persisted field must raise
  the compatibility floor or add explicit compatibility handling;
- the first schema bump must ship migration or compatibility handling, tests,
  and release notes.

## Runtime identity

OpenBao supplies `BackendConfig.BackendUUID`. The backend keeps it in memory
and sends it to providers as `mount_uuid`; it is not stored in plugin storage.
The plugin catalog probes the factory with an empty configuration, so the
factory accepts a missing UUID. `Initialize` and every storage-backed request
require it and fail when it is missing.

`identity/restore-epoch` holds a random `epoch-<hex>` value created once per
mount. It rotates only when an active restore guard is cleared, either through
`config/restore-guard/acknowledge` or a `config` write that sets
`restore_guard=false`. The stored epoch rolls back with a snapshot, and
initialization never detects a restore or rotates it. Rotation does not
rewrite remote ownership metadata. User-facing semantics are in
[Ownership and drift](../../concepts/ownership-and-drift.md#runtime-identity-and-restore-epoch).

## Source records

`metadata/<path>` (`metadataRecord`) holds the source generation, current
version, oldest retained version, `max_versions` (default 10), `cas_required`,
`delete_version_after`, custom metadata, `source_sync_enabled`, per-version
created, deletion, and destroy state, and the updated time.

`data/<path>/versions/<version>` (`versionRecord`) holds the version number,
created time, payload data, deletion time, and destroyed flag. Destroy clears
`data` and keeps the record as a tombstone.

The generation is a random value created with the metadata record. Operation
IDs and idempotency keys include it, so deleting and recreating a path does
not reuse operation IDs when version numbers restart.

`pruneExcessVersions` deletes versions older than `max_versions`, except
versions referenced by a queued upsert. Metadata delete
(`deleteSourcePath`) removes `failed_terminal` outbox records for the path,
all version records, all status records, and the metadata record. It is
rejected while any association exists for the path.

## Destination records

`destinations/<type>/<name>` holds the identity, description, disabled flag,
non-sensitive provider config, `allowed_source_path_prefixes`,
`allowed_resolved_name_prefixes`, `sensitive_config_version`, and timestamps.

Sensitive fields are written to a new
`destinations_secrets/<type>/<name>/versions/<sensitive-version>` record
before the public record is updated to point at it. The previous sensitive
version is deleted only after the public record is written. Readers resolve
the sensitive record through `sensitive_config_version`, so they never mix
parts of two writes. The value `none` means no sensitive config. An empty
value reads the legacy unversioned key. Destination reads redact sensitive
fields.

## Association records

`associations/<path>/<association-id>` holds the source path, destination
type, name, and reference, `name_template`, `resolved_name`, concrete
`reservation_names`, granularity, format, data mapping, data-key template,
normalized `provider_config`, opaque `provider_identity`, delete mode, enabled
flag, and timestamps. Provider config is non-sensitive desired state. Provider
identity is derived by the provider during normalization and is used only for
IDs, selection, and reservations.

The association ID is `assoc-` plus the first 128 bits, in lowercase hex, of
SHA-256 over the source path, destination type, destination name, reservation
key, and granularity. The reservation key is the provider identity and the
reservation name joined with a NUL byte, or the reservation name alone when
the identity is empty. Changing the ID width or inputs is a storage and API
compatibility change.

Two indexes point back to the primary record. Each stores the source path as
its value:

- `associations_by_destination/<type>/<name>/<association-id>` blocks
  destination deletion while associations exist.
- `association_names/<destination-ref>/<reservation-hash>/<association-id>`
  enforces one owner per remote name. `<reservation-hash>` is the first 96 bits
  of SHA-256 over the reservation key, in lowercase hex.

A secret-path association reserves its resolved name. A secret-key association
reserves its rendered name pattern, with `${key}` in place of the key, plus
the concrete names for the current source keys. Source writes refresh the
concrete reservations before they commit a new version. User-facing
reservation rules are in
[Associations](../../configure/associations.md#name-reservations).

## Queue records

`enqueue_intent/<path>/<version>` records the operations a mutation is about to
write: ID, type, association ID, object ID, destination reference,
`not_before`, idempotency key, and trigger, plus the generation and the
`cancel_operation_ids` the mutation supersedes. A completed intent is deleted,
not marked complete.

`outbox/<operation-id>` holds the operation type, path, version, association
ID, object ID, destination reference, state, attempt count, `not_before`,
idempotency key, trigger (`user` or `drift-repair`), `claim_owner`,
`claim_expires_time`, `claim_attempt`, and timestamps. A missing trigger reads
as `user`.

The operation ID is `op-` plus the first 64 bits of SHA-256 over generation,
path, version, association ID, object ID, and type, followed by `-<version>`.
Manual sync, drift repair, and desired-state association updates add a random
salt so each request gets a new ID.

Each outbox index stores the operation ID:

- `outbox_by_path/<path>/` and `outbox_by_state/<state>/` exist for every
  record.
- `outbox_by_due/<due-time>/` exists only for `pending` and `retry_wait`.
  `<due-time>` is `not_before` as UTC RFC 3339 at whole-second precision, used
  as a lexical sort key. `0001-01-01T00:00:00Z` is the sentinel when
  `not_before` does not parse.

The persisted states are `pending`, `retry_wait`, and `failed_terminal`.
`canceled` is a response-only state: cancel deletes the record. Because state
strings appear in `outbox_by_state/<state>/`, renaming one is a migration.

## Status records

`status/<path>/<association-id>/<object-id>` holds one record per association
object. A secret-path association uses the object ID `secret-path`, which is a
frozen storage sentinel. A secret-key association uses one object ID per
top-level source key.

A status record holds the state, source version, destination reference,
resolved name, payload hash, remote version, verification marker, last
operation ID, last success, reconcile, drift-detected, and repair times,
repair count, error class, error message, and updated time. State meanings are
in [Status and diagnostics](../../reference/status-and-diagnostics.md#sync-states).

`putStatus` drops a write whose version is older than the stored version. It
keeps the stored verification marker, reconcile, drift, and repair fields when
the new record leaves them empty.

## Index write order and orphans

Primary records are authoritative. Indexes are lookup aids, and every reader
dereferences an index through its primary record before it acts on it.

`putAssociation` and `putOutbox` write new indexes first, then the primary
record, then delete indexes the new record no longer uses. An interrupted
write leaves either the previous valid record or the new record with all its
indexes, plus possibly stale indexes. `deleteOutbox` deletes the primary
record, then the indexes of both the caller's copy and the stored copy.

Readers skip an index entry whose primary record is missing or no longer
matches the indexed destination, reservation, path, state, or due time. The
dispatcher repeats the due-time check when it claims an operation.
Enqueue-intent recovery rewrites the indexes of an outbox record that already
exists.

Recovery replays only `enqueue_intent/` records. There is no general
compaction pass, so interrupted writes can leave orphaned version records,
stale outbox indexes, stale destination indexes, or stale name reservations.
These must never authorize dispatch, block destination deletion, or reserve a
name without a live matching primary record. A future compaction must keep
version records referenced by enqueue intents, outbox records, or status.

## Source path encoding

`normalizeSourcePath` trims leading and trailing slashes, rejects empty, `.`,
and `..` segments and the segment `versions`, and rejects a final segment of
`plan`, `enable`, `disable`, or `sync`. Paths are stored byte-exact and are not
case-normalized: `App/DB` and `app/db` are different sources. The plugin sets
no separate path length limit; OpenBao routing and the storage backend set the
effective limit.
