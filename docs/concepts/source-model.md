# Source Model

OpenBao Secret Sync stores source secrets inside the Secret Sync mount. It does
not read source values from other OpenBao mounts.

This is an OpenBao plugin boundary constraint: a secret-engine plugin receives
a mount-scoped storage view and logical requests routed to its own paths. It
does not receive global storage access or observe writes to unrelated mounts.
Secret Sync therefore exposes a local, KV-v2-like source API and syncs from
that local source of truth.

## What This Means

The plugin is not a drop-in implementation of `/sys/sync`, and it is not a
bridge that automatically mirrors existing KV-v2 secrets. To sync a value, write
the value to `data/<path>` in the Secret Sync mount and create an association
from that path to a destination.

The source API is intentionally familiar:

- source values live under `data/<path>`;
- each write creates a new version;
- source metadata lives under `metadata/<path>`;
- selected versions can be soft-deleted, undeleted, or destroyed;
- metadata can require CAS and can limit retained versions.

CLI writes can put source payload keys directly at the top level:

```sh
bao write secret-sync/data/app/db username=app password=initial
```

HTTP clients can use the KV-v2-like wrapped body with `data` and `options`.
The wrapped form is also the escape hatch when a source payload key must be
named `data`, `options`, `cas`, or `version`.

Use [API compatibility](../reference/api-compatibility.md) for the exact
KV-v2-like contract and intentional differences.

## Local Source Versions

Each source write creates a local source version. Enabled associations enqueue
remote work for the current live source version when the operation is supposed
to sync.

A successful source write means the plugin accepted the local source version
and any required queue work. It does not mean the destination already contains
that version. Use [Sync model](sync-model.md) for queue and convergence
behavior.

Queue capacity is checked before accepting a source write that would enqueue
remote work. If the queue cannot accept the work, the source write fails before
the new version is stored.

## Source Metadata

Source metadata controls local version policy and can carry custom metadata.
In hardened posture, enabled associations require source sync to be explicitly
enabled through `sources/<path>/enable` before they can enqueue or dispatch
remote mutation. Enabling a source also enqueues its current version for enabled
associations with active destinations. Queue admission is all-or-nothing: if
the required operations do not fit, the source remains disabled.

Fresh mounts default `security_posture=standard`. In that platform-operated
mode, creating or enabling an association is the authorization step that
permits sync for the source path.

Use [Delegated use](../guides/delegated-use.md) when application owners manage
their own source paths or associations.

## Source Lifecycle And Remote Work

Current-version source lifecycle operations participate in sync:

- `DELETE data/<path>`, `delete/<path>`, and `destroy/<path>` cancel stale
  queued upserts and enqueue remote deletes for enabled associations with
  `delete_mode=delete`.
- `undelete/<path>` on the current version enqueues replacement upserts for
  enabled associations.
- `DELETE metadata/<path>` removes local source state only when no
  associations remain for the path.

Remote delete still requires provider-owned delete support and ownership proof.
Use [Ownership and safety](ownership-and-safety.md) for the remote mutation
safety model.

## Future Integration

Syncing values directly from other OpenBao secret engines would require an
explicit cross-plugin or core-mediated source integration. Until that exists,
Secret Sync's stable model is own-mount source data plus associations from that
local source state.
