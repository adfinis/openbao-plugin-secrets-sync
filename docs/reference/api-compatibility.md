# API Compatibility


`openbao-plugin-secrets-sync` exposes a KV-v2-like local source-secret API. The
goal is operator familiarity, not drop-in replacement compatibility with the
OpenBao KV-v2 secrets engine.

## Compatibility Position

The source API is compatible with KV-v2 at the concept and workflow level:

- source secrets are addressed under `data/<path>`;
- each write creates a new version;
- reads return `data` and version `metadata`;
- writes support `options.cas`;
- `metadata/<path>` owns version policy and custom metadata;
- `delete/<path>`, `undelete/<path>`, and `destroy/<path>` mutate selected
  versions;
- `DELETE data/<path>` soft-deletes the latest version;
- `DELETE metadata/<path>` removes all local source versions and metadata.

The source API is not a strict client compatibility layer:

- responses may contain sync-specific fields such as queued operation IDs and
  sync state;
- metadata deletion is blocked while associations exist;
- association activation requires source eligibility metadata;
- sync-specific paths such as `destinations/*`, `associations/*`, `queue/*`,
  and `status/*` are part of the engine contract;
- exact KV-v2 wire compatibility must be proven by golden tests before it is
  claimed.

## Implemented Source Paths

```text
POST   data/<path>       write a new local source version
GET    data/<path>       read latest or selected local source version
DELETE data/<path>       soft-delete latest local source version
POST   delete/<path>     soft-delete selected local source versions
POST   undelete/<path>   undelete selected local source versions
POST   destroy/<path>    permanently destroy selected local source versions
POST   metadata/<path>   create or update local source metadata policy
GET    metadata/<path>   read local source metadata and sync summary
LIST   metadata/<path>   list local source metadata keys
DELETE metadata/<path>   delete all local source metadata and versions
```

## List Pagination

Public LIST endpoints accept OpenBao paginated-list parameters and pass them
through to storage:

- `after`: optional key to begin listing after; it does not need to exist in
  the result set;
- `limit`: optional maximum number of keys to return; non-positive values
  return all keys.

This applies to `metadata`, `metadata/<path>`, `destinations/<type>`, and
`associations`.

`PATCH data/<path>` is intentionally deferred until partial-update semantics
are worth the extra compatibility and validation surface.

## Metadata Policy

Metadata writes support:

```json
{
  "max_versions": 10,
  "cas_required": true,
  "delete_version_after": "0s",
  "custom_metadata": {
    "syncable": "true",
    "owner": "platform"
  }
}
```

Enabled associations require:

```json
{
  "custom_metadata": {
    "syncable": "true"
  }
}
```

This makes sync opt-in at the source path and keeps destination mutation from
being triggered by arbitrary local secret writes.
