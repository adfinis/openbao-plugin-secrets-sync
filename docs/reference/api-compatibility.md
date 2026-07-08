# API compatibility

`openbao-plugin-secrets-sync` exposes a KV-v2-like local source-secret API. The
goal is operator familiarity, not drop-in replacement compatibility with the
OpenBao KV-v2 secrets engine.

Use [Source model](../concepts/source-model.md) for the source data mental
model. Use [API surface](api-surface.md) for the full Secret Sync path group
summary.

## Compatibility position

The source API is compatible with KV-v2 at the concept and workflow level:

- source secrets are addressed under `data/<path>`;
- each write creates a new version;
- reads return `data` and version `metadata`;
- wrapped writes support `options.cas`;
- `metadata/<path>` owns version policy and custom metadata;
- `delete/<path>`, `undelete/<path>`, and `destroy/<path>` mutate selected
  versions;
- `DELETE data/<path>` soft-deletes the latest version;
- `DELETE metadata/<path>` removes all local source versions and metadata.

The source API is not a strict client compatibility layer:

- source writes also accept a CLI shorthand where top-level request fields
  become source payload keys;
- responses may contain sync-specific fields such as queued operation IDs and
  sync state;
- metadata deletion is blocked while associations exist;
- association activation may require source eligibility metadata when
  `security_posture=hardened`;
- sync-specific paths such as `destinations/*`, `associations/*`, `queue/*`,
  and `status/*` are part of the engine contract;
- exact KV-v2 wire compatibility must be proven by golden tests before it is
  claimed.

## Pre-release response shape changes

Association create, plan, read, list, and lifecycle responses do not include the
static `defaults` object. Clients that need association defaults or provider
capability flags should read `info`, where defaults are returned under
`defaults.association`.

## Implemented source paths

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

## List pagination

Public LIST endpoints accept OpenBao paginated-list parameters and pass them
through to storage:

- `after`: optional key to begin listing after; it does not need to exist in
  the result set;
- `limit`: optional maximum number of keys to return; non-positive values
  return all keys.

This applies to `metadata`, `metadata/<path>`, `destinations/<type>`, and
`associations`.

`PATCH data/<path>` is not part of the source API.

## Source write forms

For CLI use, write source payload keys directly:

```sh
bao write secret-sync/data/app/db username=app password=initial cas=1
```

The top-level `cas` field is an alias for `options.cas` and is not stored as a
source payload key.

For HTTP clients, automation, or source payload keys that collide with reserved
field names, use the wrapped body:

```json
{
  "data": {
    "username": "app",
    "password": "initial"
  },
  "options": {
    "cas": 1
  }
}
```

In shorthand mode, `path`, `data`, `options`, `cas`, and `version` are reserved
field names. `cas` remains the CLI alias for `options.cas`; `version` is
rejected on writes because it is only meaningful for reads. Wrapped `data`
writes reject extra top-level source payload fields so mixed request bodies do
not silently discard input.

## Metadata policy

Metadata writes support:

```json
{
  "max_versions": 10,
  "cas_required": true,
  "delete_version_after": "0s",
  "custom_metadata": {
    "owner": "platform"
  }
}
```

`delete_version_after` must be omitted or set to `0s`. Non-zero timed deletion
policy is rejected until the backend enforces it.

Mounts default `security_posture=standard`, so creating an enabled association
is the source authorization step in the platform-operated default mode.

In hardened posture, enabled associations require source sync to be explicitly
enabled for the source path:

```sh
bao write -force secret-sync/sources/app/db/enable
```

Delegated deployments should enable hardened posture:

```sh
bao write secret-sync/config security_posture=hardened
```

This requires explicit source sync enablement at the source path and constrained
destinations before delegated association owners can trigger destination
mutation.
