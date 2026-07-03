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
- custom metadata such as `syncable=true`;
- per-version deletion and destroy state;
- updated time.

Version records track version number, creation time, source payload data,
deletion time, and destroy state.

## Destination Records

Public destination fields are stored in `destinations/<type>/<name>`. Sensitive
destination fields are stored in `destinations_secrets/<type>/<name>`.

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
remote object for the same destination. Secret-key associations reserve their
name template. Secret-path associations reserve their resolved name.

Association records carry the source path, destination reference, name or name
template, granularity, format, data mapping, delete mode, enabled state, and
timestamps.

## Queue Records

`enqueue_intent/<path>/<version>` records source-write intent before all
outbox records are written. It is used to recover partial enqueue work after an
interrupted write path.

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

Status records are diagnostic state. They must not contain source payload
values or sensitive destination config.

## Runtime Cache

Provider destination runtimes are cached in memory. Cache entries are keyed by
destination identity and invalidated when destination public or sensitive
configuration changes. Backend cleanup closes cached runtimes.

Runtime caches are an optimization. Correctness comes from durable storage,
queue claims, provider idempotency, provider ownership checks, and the safety
gates described in [Safety and diagnostics](safety-and-diagnostics.md).
