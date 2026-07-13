# Storage Model

The backend stores all durable state in the mount-scoped OpenBao storage view.
All keys below are relative to that storage view.

```text
config
schema/version
identity/plugin-instance
identity/restore-epoch

data/<path>/versions/<version>
metadata/<path>

destinations/<type>/<name>
destinations_secrets/<type>/<name>

associations/<path>/<association-id>
associations_by_destination/<type>/<name>/<association-id>
association_names/<destination-ref>/<reservation>/<association-id>

enqueue_intent/<path>/<version>

outbox/<operation-id>
outbox_by_due/<timestamp>/<operation-id>
outbox_by_path/<path>/<operation-id>
outbox_by_state/<state>/<operation-id>

status/<path>/<association-id>/<object-id>
```

## Seal-Wrapped Prefixes

`backend.go` lists these prefixes in `PathsSpecial.SealWrapStorage`:

- `data/`
- `destinations_secrets/`

Source version records contain source payload values and must stay under
`data/`. Destination sensitive configuration is stored separately from public
destination metadata and must stay under `destinations_secrets/`.

Status, queue, destination, and association records can include operational
metadata such as source paths, association IDs, remote names, versions,
operation IDs, payload hashes, and error classes. They must not include source
payload values.

## Schema State

`schema/version` records the storage schema understood by the plugin binary.
The backend initializes the schema on the first storage-backed request. If a
stored schema requires a newer incompatible plugin, request handling and
periodic processing fail closed before source or remote mutation.

Current schema compatibility constants live in `storage_records.go`:

- `currentStorageSchema`
- `minSupportedStorageSchema`

Schema changes must include migration or compatibility handling before the
backend accepts storage records written by another version.

Pre-release schema policy:

- the current schema is v1;
- request paths do not run automatic storage migrations;
- a future stored schema may be read only when its `min_compatible_version` is
  less than or equal to the running binary's `currentStorageSchema`;
- incompatible schemas fail closed before path handlers, queue drain, periodic
  dispatch, or event dispatch can mutate source or remote state;
- persisted records are decoded with tolerant JSON readers, so an older binary
  can silently drop fields added by a newer binary when it rewrites a record;
- any added, removed, renamed, or semantics-changing persisted field must bump
  the storage compatibility floor or add explicit compatibility handling;
- the first schema bump must add explicit migration or compatibility handling,
  tests, and release notes before the schema version is increased.

## Tolerant Reads and Orphans

Primary records are authoritative. Secondary indexes are redundant lookup aids
and must be dereferenced through the primary record before they affect source,
queue, association, destination, or remote mutation behavior.

Outbox updates write the replacement path, state, and due-time indexes before
the canonical record, then remove obsolete indexes after the canonical write.
An interrupted update therefore leaves either the previous valid canonical
view or a new canonical record with all required indexes. Queue readers still
validate every index candidate against the canonical path, state, and due time;
the dispatcher repeats the due-time check while acquiring the operation claim.
Enqueue-intent recovery refreshes indexes for canonical operations that already
exist before pruning the recovered intent.

Crash recovery only replays pending `enqueue_intent/<path>/<version>` records.
It does not run a general storage compaction pass. Interrupted writes can leave
orphaned source version records, stale outbox index entries, stale association
destination indexes, or stale association name reservations. Readers tolerate
those records by ignoring index entries whose primary record is missing or no
longer matches the indexed destination, reservation, state, path, or due-time
candidate.

This means stale derived records can accumulate until source deletion, targeted
maintenance, or a future bounded compaction feature removes them. They must not
authorize dispatch, block destination deletion, or reserve a remote object name
without a live matching primary record. Any future compaction must preserve
version records referenced by pending enqueue intents, outbox records, or
status diagnostics.

## Runtime Identity

`identity/plugin-instance` is generated once per mount. Provider requests carry
this ID so providers can write and verify ownership metadata for the OpenBao
mount that produced a remote object.

`identity/restore-epoch` is generated once per mount and rotates when an active
restore guard is acknowledged. Providers include the restore epoch in ownership
metadata where the destination supports it. This helps distinguish restored or
cloned mounts that might otherwise manage the same remote object.

Each source metadata record carries a random generation. Operation IDs and
idempotency keys include that generation so deleting and recreating a source
path does not reuse historical operation IDs when version numbers restart.

## Source Records

Source data is KV-v2-like but not wire-compatible with the OpenBao KV-v2
engine. Each source write creates a new version under
`data/<path>/versions/<version>` and updates metadata under `metadata/<path>`.

Source metadata records track:

- source generation;
- current version;
- oldest retained version;
- maximum retained versions;
- CAS requirement;
- delete-after setting;
- custom metadata supplied by callers;
- plugin-owned source sync enablement state;
- per-version deletion and destroy state;
- updated time.

Version records track version number, creation time, source payload data,
deletion time, and destroy state.

## Destination Records

Public destination fields are stored in `destinations/<type>/<name>`. Sensitive
destination fields use versioned seal-wrapped records under
`destinations_secrets/<type>/<name>/versions/<version>`. The public record points
to the active sensitive version, so readers never combine parts from different
destination writes. Unversioned sensitive records remain readable only for
legacy public records.

Public records contain destination identity, description, disabled state,
non-sensitive provider config, source-path policy prefixes, remote-name policy
prefixes, and timestamps.

Sensitive records contain only provider config that must not appear in normal
reads or logs. Destination read responses redact sensitive fields.

## Association Records

Association records live under the source path:

```text
associations/<path>/<association-id>
```

The backend also keeps destination and remote-name indexes:

```text
associations_by_destination/<type>/<name>/<association-id>
association_names/<destination-ref>/<reservation>/<association-id>
```

The remote-name reservation prevents two associations from managing the same
remote object identity for the same destination. Provider-normalized identity
is prepended to the reservation when present; GitLab uses its environment scope,
so the same variable key can be reserved independently in different scopes.
Secret-path associations reserve their resolved name. Secret-key associations
reserve their rendered name pattern:
the backend substitutes the source path and destination placeholders, keeps a
stable key placeholder, and applies the same slash trimming used for concrete
remote object names. They also reserve the concrete rendered names for the
current source keys, and source writes refresh those concrete reservations
before committing a new version.

Association records carry the source path, destination reference, name or name
template, granularity, format, data mapping, normalized provider config, opaque
provider identity, delete mode, enabled state, and timestamps. Provider config
is non-sensitive association desired state. Provider identity is derived during
normalization and is used only for stable IDs, selection, and reservations.

## Queue Records

`enqueue_intent/<path>/<version>` records source-write intent before all
outbox records are written. It is used to recover partial enqueue work after an
interrupted write path.

Intent completion is deletion-based: a completed intent is removed rather than
marked complete in place.

`outbox/<operation-id>` stores durable remote-mutation intent. Outbox records
are indexed by due time, source path, and state:

```text
outbox_by_due/<timestamp>/<operation-id>
outbox_by_path/<path>/<operation-id>
outbox_by_state/<state>/<operation-id>
```

Outbox records carry operation type, path, version, association ID, object ID,
destination reference, state, attempt count, due time, idempotency key, trigger,
claim fields, and timestamps.

The due-time index uses UTC RFC3339 timestamps at whole-second precision as
lexical sort keys. The zero-time string `0001-01-01T00:00:00Z` is the sentinel
for queued operations without a valid future `not_before` value.

Outbox state strings are part of the key layout because they appear under
`outbox_by_state/<state>/`. Renaming a state string is a storage migration, not
just a response-shape change.

## Status Records

Status records live at:

```text
status/<path>/<association-id>/<object-id>
```

Status is per association object. A secret-path association uses the synthetic
object ID `secret-path`. A secret-key association has one object ID per source
key.

Status records carry the last observed state, source version, destination
reference, resolved remote name, payload hash, provider remote version,
verification marker, operation ID, success time, reconcile time, drift time,
repair time, repair count, error class, error message, and updated time.

The secret-path association object uses the synthetic object ID `secret-path`.
That value appears in status storage keys and is a frozen storage sentinel.

Status records are diagnostic state. They must not contain source payload
values or sensitive destination config.

## Runtime Cache

Provider destination runtimes are cached in memory. Cache entries are keyed by
destination identity and invalidated when destination public or sensitive
configuration changes. Backend cleanup closes cached runtimes.

Runtime caches are an optimization. Correctness comes from durable storage,
queue claims, provider idempotency, provider ownership checks, and the safety
gates described in [Safety and diagnostics](safety-and-diagnostics.md).

## Path and ID Encoding

Source paths are stored byte-exact after slash trimming and segment validation.
They are not case-normalized: `App/DB` and `app/db` are different sources.
The plugin does not impose a separate source-path length limit; effective
limits come from OpenBao request routing and the configured storage backend.

Association IDs are deterministic hashes of the source path, destination,
reserved remote name, and granularity. The ID encoding is `assoc-` plus the
first 128 bits of the SHA-256 hash in lowercase hex. Widening or shortening it
later would be a storage and API compatibility change.
