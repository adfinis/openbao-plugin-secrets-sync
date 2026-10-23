---
verifiedBy:
  - internal/backend/backend.go
  - internal/backend/path_info.go
  - internal/backend/path_config.go
  - internal/backend/path_data.go
  - internal/backend/path_metadata.go
  - internal/backend/path_versions.go
  - internal/backend/path_sources.go
  - internal/backend/path_destinations.go
  - internal/backend/path_associations.go
  - internal/backend/path_queue.go
  - internal/backend/path_status.go
  - internal/backend/path_reconcile.go
  - internal/backend/path_pagination.go
  - internal/backend/existence_checks.go
  - internal/backend/storage_records.go
  - internal/backend/storage_source.go
  - internal/backend/diagnostics.go
  - internal/backend/api_schemas.go
  - internal/backend/openapi_generated_test.go
  - internal/backend/redaction_test.go
  - internal/backend/path_associations_test.go
---

# API reference

Secret Sync serves a KV-v2-like source API plus sync paths for destinations,
associations, queue work, status, and reconcile. Examples assume the mount path
`secret-sync/`. Remote destination mutation is asynchronous: a successful write
means the plugin stored local state and any queue work, not that the
destination has changed. See
[Accepted is not synced](../concepts/sync-model.md#accepted-is-not-synced).

## Contract rules

- Only `data/<path>` reads return source payload values. Plan, queue, status,
  and reconcile responses never return payload values. Key names can appear in
  resolved remote names for `secret-key` associations.
- Destination reads redact sensitive fields. The response reports only whether
  sensitive values are configured and which keys are set.
- Association responses return effective values. Read the static association
  defaults and provider capability flags from `info`, under
  `defaults.association` and `providers.<type>.capabilities`.
- Blocked or terminal results carry a human-readable `hint` and can carry
  structured `next_actions`. Successful responses return both as top-level
  data fields. OpenBao error responses nest them under `data` so the response
  stays an OpenBao error. The meanings are in
  [Hints and next actions](status-and-diagnostics.md#hints-and-next-actions).
- Remote mutation is subject to mount pause, restore guard, replication state,
  and, in hardened posture, source sync enablement and destination
  constraints. See [Mutation gates](../concepts/ownership-and-drift.md#mutation-gates)
  and [Enable hardened posture](../configure/delegate.md#enable-hardened-posture).

## Paths

The Operations column uses `bao` CLI verbs. `write` covers both the `create`
and `update` OpenBao operations; see
[Create and update capabilities](#create-and-update-capabilities).

### Config

| Path | Operations | Purpose |
| --- | --- | --- |
| `info` | read | Return `plugin_version`, static association defaults, and capability flags for each registered provider. |
| `config` | read, write | Read or partially update mount runtime settings. Fields and defaults are in [Runtime settings](../configure/runtime.md#configuration-fields). |
| `config/restore-guard/acknowledge` | write | Clear an active restore guard and rotate the restore epoch. Follow [Review a restore or clone](../operate/restore-and-clone.md#resume-remote-mutation) first. |

### Source data

| Path | Operations | Purpose |
| --- | --- | --- |
| `data/<path>` | write, read, delete | Write a new source version, read the latest or a selected `version`, or soft-delete the latest version. |
| `metadata/` | list | List top-level source keys. |
| `metadata/<path>` | write, read, list, delete | Manage version policy and custom metadata, read the source summary, list keys under a prefix, or delete all local versions and metadata. |
| `delete/<path>` | write | Soft-delete the versions listed in `versions`. |
| `undelete/<path>` | write | Restore soft-deleted versions listed in `versions`. |
| `destroy/<path>` | write | Permanently remove the payload of the versions listed in `versions`. |
| `sources/<path>/check` | read | Report whether the source has a current version, whether source sync is enabled, and whether it is required. |
| `sources/<path>/enable` | write | Mark the source as enabled for sync and enqueue its current version for enabled associations with active destinations. |
| `sources/<path>/disable` | write | Mark the source as not enabled for sync. |

`delete/`, `undelete/`, and `destroy/` apply each listed version in its own
storage transaction. If one version fails, earlier versions in the same request
stay changed. Deleting, destroying, or undeleting the current version queues
remote work for enabled associations; remote deletes are queued only for
associations with `delete_mode=delete`. See
[Choose a delete mode](../configure/associations.md#choose-a-delete-mode).

Only hardened posture requires source sync enablement before work is queued.
In either posture, `sources/<path>/enable` enqueues the current version when
the flag changes, and rolls the flag back if the queue cannot admit the work.
See [Enable source sync](../configure/delegate.md#enable-source-sync).

### Destinations

| Path | Operations | Purpose |
| --- | --- | --- |
| `destinations/<type>` | list | List destination names for one provider type. |
| `destinations/<type>/<name>` | write, read, delete | Create, update, read, or delete a destination. |
| `destinations/<type>/<name>/check` | read | Run static validation and, when the destination is enabled and valid, a runtime health check. |
| `destinations/<type>/<name>/validate` | read, write | Run static validation only. |
| `destinations/<type>/<name>/health` | read | Run the runtime reachability and authorization check only. |

`<type>` is a registered provider type: `aws-sm`, `gitlab`, or `k8s`. `<type>`
and `<name>` contain letters, digits, `_`, `-`, and `.`, and start and end with
a letter, digit, or `_`. None of these paths mutate remote state. Fields for
each type are on the provider pages:
[AWS Secrets Manager](../configure/aws-secrets-manager.md),
[GitLab project variables](../configure/gitlab-project-variables.md), and
[Kubernetes Secrets](../configure/kubernetes-secrets.md).

### Associations

| Path | Operations | Purpose |
| --- | --- | --- |
| `associations` | list | List top-level association path segments, such as `app/`. |
| `associations/<path>` | write, read | Create or update the association selected by `destination=<type>/<name>`, or read all associations for the source path. |
| `associations/<path>/plan` | write | Plan an association against the current source version without storing the association or mutating the destination. |
| `associations/<path>/enable` | write | Enable the association selected by `destination`, and enqueue the current version when it was disabled. |
| `associations/<path>/disable` | write | Disable the association selected by `destination`, and cancel its eligible queued work. |
| `associations/<path>/sync` | write | Enqueue the current source version for the enabled association selected by `destination`. |
| `associations/<path>/<association-id>` | read, delete | Read or delete one association by ID. |
| `associations/<path>/<association-id>/enable` | write | Enable one association by ID. |
| `associations/<path>/<association-id>/disable` | write | Disable one association by ID. |
| `associations/<path>/<association-id>/sync` | write | Enqueue sync for one association by ID. |

Deleting an association requires the ID route; `associations/<path>` has no
delete operation. `<association-id>` has the form `assoc-` followed by 32
lowercase hexadecimal characters. When several associations share one
destination, the destination-addressed lifecycle routes are ambiguous and you
must use the ID routes. Selection, provider identity fields, and update rules
are in [Select an association](../configure/associations.md#select-an-association)
and [Update an association](../configure/associations.md#update-an-association).

### Queue, status, and reconcile

| Path | Operations | Purpose |
| --- | --- | --- |
| `queue` | read | Return queue counts by state, capacity, and utilization. |
| `queue/drain` | write | Dispatch due queue work now, up to `max_operations` (default `100`). This can mutate destinations. |
| `queue/<operation-id>` | read | Read one queue operation, including its `trigger` (`user` or `drift-repair`). |
| `queue/<operation-id>/retry` | write | Move a `retry_wait` or `failed_terminal` operation back to `pending`. |
| `queue/<operation-id>/cancel` | write | Discard a `pending`, `retry_wait`, `failed_terminal`, or `canceled` operation. |
| `status/<path>` | read | Read per-object sync status for a source path. |
| `reconcile/<path>/plan` | read | Read remote state and compute status without storing it. |
| `reconcile/<path>` | write | Read remote state and store the resulting local status. |

Reconcile never writes destination secrets. OpenBao forwards `queue/drain`
from a standby node to the active node. Queue and status reads can be served
from a standby's eventually consistent view. Retry and cancel fail while a
dispatcher holds a claim on the operation.

State, verification, and error values are defined in
[Status and diagnostics](status-and-diagnostics.md). Procedures for draining,
retrying, and canceling are in
[Manage queued operations](../operate/troubleshoot.md#manage-queued-operations).

## Source path rules

The plugin trims leading and trailing `/` from `<path>`. A source path is
rejected when:

- it is empty;
- any segment is empty, `.`, or `..`;
- any segment is `versions`; or
- its last segment is `plan`, `enable`, `disable`, or `sync`.

The same rules apply to `allowed_source_path_prefixes` on destinations.

## Source write forms

Each write to `data/<path>` creates a new version. A write that produces sync
work reserves queue capacity first; if the queue is full, the write fails and
no version is stored.

For CLI use, pass source payload keys as top-level fields:

```sh
bao write secret-sync/data/app/db username=app password=initial cas=1
```

For HTTP clients, automation, or payload keys that collide with reserved
names, put the payload under `data`:

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

The fields `path`, `data`, `options`, `cas`, and `version` are reserved:

- A request that contains `data` is a wrapped write. Any other top-level field
  besides the reserved names is rejected, so mixed bodies never drop input.
- In a shorthand write, `options` and `version` are rejected, and `path` is
  rejected unless it equals the route path. Put colliding keys under `data`.
- `cas` is an alias for `options.cas` and is never stored as a payload key.
  When both are set, they must match.

Check-and-set follows KV-v2: `cas=0` succeeds only when the path has no
current version, and a positive `cas` must equal the current version. When
`cas_required=true`, every write must set `cas`.

The write response returns `metadata.version`, `metadata.created_time`,
`metadata.sync_operation_ids`, and `metadata.sync_state`. In hardened posture,
a write to a source that has enabled associations but no source sync
enablement is stored but not queued, and the response explains why in `hint`
and `next_actions`.

## Source metadata policy

`metadata/<path>` accepts these fields. A write must set at least one.

| Field | Default | Rule |
| --- | --- | --- |
| `max_versions` | `10` | Must be greater than zero. Older versions beyond the limit are pruned on the next data or metadata write, except versions that queued upserts still reference. |
| `cas_required` | `false` | When `true`, data writes must set `cas`. |
| `delete_version_after` | `0s` | Must be omitted or `0s`. Timed deletion is not enforced, so non-zero values are rejected. |
| `custom_metadata` | empty | String values, validated with OpenBao's custom metadata limits. A write replaces the whole map. |

A metadata read also returns `current_version`, `oldest_version`, `versions`,
`source_sync_enabled`, and a `sync` summary with `queued_operations` and
`objects` counts. Only `sources/<path>/enable` and `sources/<path>/disable`
change `source_sync_enabled`.

## KV-v2 differences

The source API follows KV-v2 concepts but is not a drop-in KV-v2 client
target:

- Shorthand writes accept payload keys as top-level fields.
- There is no `PATCH data/<path>` and no `subkeys/<path>` path. The `config`
  path holds Secret Sync runtime settings, not KV-v2 engine settings.
- Reading a soft-deleted or destroyed version returns not found, without
  version metadata.
- `max_versions` must be greater than zero; there is no mount-level default to
  fall back to.
- `delete_version_after` accepts only `0s`.
- `DELETE metadata/<path>` fails while any association, enabled or disabled,
  exists for the path.
- Write and metadata responses include sync fields such as
  `sync_operation_ids`, `sync_state`, `source_sync_enabled`, and `sync`.
- Some path segments are reserved. See [Source path rules](#source-path-rules).

## List pagination

`LIST` on `metadata/`, `metadata/<path>`, `destinations/<type>`, and
`associations` accepts OpenBao paginated-list parameters:

- `after`: list keys after this key. The key does not need to exist.
- `limit`: return at most this many keys. Zero or a negative value returns all
  keys.

## Create and update capabilities

OpenBao classifies a write as `create` when the target resource does not exist
and as `update` when it does. Policies can therefore allow creation without
overwrite, or overwrite without creation. The plugin defines existence as
follows:

| Path | Exists when |
| --- | --- |
| `data/<path>`, `metadata/<path>` | A source metadata record exists for the path. Data writes, metadata writes, and `sources/<path>/enable` or `disable` create it. Deleting or destroying versions does not remove it; only `DELETE metadata/<path>` does. |
| `destinations/<type>/<name>` | The destination is stored. |
| `associations/<path>` | At least one association for the path matches the `destination` and provider identity selector. An ambiguous selector counts as existing, so it cannot pass a create-only policy. |

All other write paths have no existence check, and OpenBao treats writes to
them as `update`. Policy examples are in [Policy examples](../security/policies.md).

## Inspect the OpenAPI document

OpenBao generates an OpenAPI 3.0.2 document from the path, field, and response
declarations in `internal/backend`. Operation IDs use the `secret-sync`
prefix. Tests verify the generated document; no generated copy is committed.

Show help for one path on a running server:

```sh
bao path-help secret-sync/data/app/db
```

Read the combined OpenAPI document for all mounts your token can see:

```sh
bao read -format=json sys/internal/specs/openapi
```
