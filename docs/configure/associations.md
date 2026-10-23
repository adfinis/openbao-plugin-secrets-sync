---
verifiedBy:
  - internal/backend/path_associations.go
  - internal/backend/association_plan.go
  - internal/backend/granularity.go
  - internal/backend/storage_records.go
  - internal/backend/storage_association.go
  - internal/backend/storage_outbox.go
  - internal/backend/dispatcher.go
  - internal/backend/path_data.go
  - internal/backend/path_versions.go
  - internal/backend/path_metadata.go
  - internal/backend/path_info.go
  - internal/payload/payload.go
  - internal/providers/awssecretsmanager/provider.go
  - internal/providers/kubernetessecrets/provider.go
  - internal/providers/gitlab/project_variables.go
---

# Configure associations

An association connects one source path in the mount to one destination. It
defines how many remote objects the source produces, how each payload is
encoded, what the remote objects are called, and what happens to them when the
source is deleted.

Before you create an association:

- the source path must have a current, undeleted version;
- the destination `<type>/<name>` must exist and must not be disabled; and
- in hardened posture, source sync must be enabled for the path. See
  [Enable source sync](delegate.md#enable-source-sync).

Plan the association with the same fields you intend to write. Plan builds the
payload and asks the provider what it would do, without storing the
association or changing the destination:

```sh
bao write secret-sync/associations/app/db/plan destination=aws-sm/prod
bao write secret-sync/associations/app/db destination=aws-sm/prod
```

The write returns `sync_operation_ids` for queued work. Queued work is not
converged remote state; see
[Accepted is not synced](../concepts/sync-model.md#accepted-is-not-synced).

When you omit fields, a new association uses `granularity=secret-path`,
`format=json`, `data_mapping=payload`, `delete_mode=retain`, and
`enabled=true`. `bao read secret-sync/info` returns these defaults and each
registered provider's capability flags, including `max_payload_bytes`.

## Choose a secret shape

Three fields set the shape:

- `granularity`: `secret-path` writes one remote object for the source path.
  `secret-key` writes one remote object for each top-level source key.
- `format`: `json` writes deterministic JSON with sorted keys. `raw` writes the
  exact bytes of one source value and requires `secret-key`.
- `data_mapping`: `payload` (default) stores the encoded payload as one remote
  value. `source-keys` maps each top-level source key to a destination-native
  data key and requires `secret-path` with `format=json`.

| Shape | Remote result | AWS Secrets Manager | Kubernetes Secrets | GitLab project variables |
| --- | --- | --- | --- | --- |
| `secret-path`, `json`, `payload` | One object containing the full source as JSON. | Yes | Yes | Yes |
| `secret-path`, `json`, `source-keys` | One object with one native data key per source key. | No | Yes | No |
| `secret-key`, `raw` | One object per source key containing the raw value. | No | No | Yes |
| `secret-key`, `json` | One object per source key containing `{"<key>": <value>}`. | No | No | Yes |

The backend rejects a shape that the destination provider does not support
when you plan or write the association.

`raw` and `source-keys` accept only string or byte source values. JSON shapes
can carry structured values. With `secret-key`, the source must have at least
one key, and each key must be non-empty, have no surrounding whitespace, contain
no `/`, and not be `.` or `..`.

Each provider has a maximum payload size, listed on its page. Plan reports an
oversized payload as `action=blocked` with `error_class=capacity`, and the
dispatcher fails the operation with the same class before calling the provider.

## Name remote objects

Use `resolved_name` for a literal remote name, or `name_template` to render
one. Templates use literal placeholder replacement only. They have no
functions, filters, conditionals, or escaping. A rendered name that still
contains `{{` or `}}` is rejected.

| Placeholder | Renders |
| --- | --- |
| `{{ path }}` | The normalized source path, without leading or trailing `/`. |
| `{{ key }}` | The source key for `secret-key` objects. For `secret-path`, it renders `secret-path`. |
| `{{ destination.type }}` | The destination type, such as `aws-sm`. |
| `{{ destination.name }}` | The destination name. |

The backend trims leading and trailing `/` from the rendered name and rejects
an empty result.

- `secret-path`: the default template is `{{ path }}`. `resolved_name`, when
  set, takes precedence and is stored as the remote name.
- `secret-key`: `resolved_name` is rejected. `name_template` must include
  `{{ key }}`, and the default is `{{ path }}/{{ key }}`.

`data_key_template` names Kubernetes Secret data keys when
`data_mapping=source-keys`. It must include `{{ key }}`, supports no other
placeholder, and defaults to `{{ key }}`. Each rendered data key must be at
most 253 characters, contain only letters, digits, `-`, `_`, or `.`, have no
surrounding whitespace, and must not be `.`, `..`, or start with `..`. Two
source keys that render the same data key are rejected. The template is
rejected for any other `data_mapping`.

Templating does not adapt names to the destination. Choose names the provider
accepts:

- AWS Secrets Manager accepts `/`, so the default `{{ path }}` works.
- Kubernetes Secret names must be DNS-1123 subdomains. Set a name such as
  `resolved_name=app-db`.
- GitLab variable keys allow only letters, digits, and `_`, up to 255 bytes.
  Use a template such as `name_template='APP_{{ key }}'`.

Provider name checks run during plan and dispatch, not when the association is
stored. Plan before you create an association with a derived name.

## Name reservations

The backend reserves each remote name per destination so that two associations
cannot manage the same remote object. GitLab includes `environment_scope` in
the reservation, so the same variable key can be reserved once per scope.

- `secret-path` reserves the resolved name.
- `secret-key` reserves the rendered name pattern and the concrete name for
  each current source key.

Create, update, and enable reject a name that another association already
holds, with `resolved_name "<name>" is already reserved for destination
<type>/<name>`. Source writes refresh the concrete `secret-key` names first and
reject the write, before the new version is accepted, when a new key would
render a name that another association holds.

Reservations cover associations in this mount only. Ownership metadata
protects remote objects created by anything else; see
[Collisions and stale objects](../concepts/ownership-and-drift.md#collisions-and-stale-objects).

## Select an association

Address associations with `destination=<type>/<name>` for plan, create,
update, disable, enable, and manual sync:

```sh
bao read secret-sync/associations/app/db
bao write secret-sync/associations/app/db/disable destination=aws-sm/prod
bao write secret-sync/associations/app/db/enable destination=aws-sm/prod
bao write secret-sync/associations/app/db/sync destination=aws-sm/prod
```

- `disable` stops future enqueue and cancels the association's queued work.
- `enable` enqueues the current source version when the association was
  disabled.
- `sync` enqueues the current source version and requires an enabled
  association.

One source path can have several associations to the same destination only
when a provider identity field differs, such as GitLab `environment_scope`.
Then include that field in plan and update writes. Destination-addressed
`disable`, `enable`, and `sync` fail as ambiguous; use the association ID from
the read response instead:

```sh
bao write secret-sync/associations/app/db/<association-id>/sync
```

Delete an association only by ID:

```sh
bao delete secret-sync/associations/app/db/<association-id>
```

Deleting an association removes its queued operations and fails while a worker
holds a claim on one of them. It never deletes or detaches remote objects. A
source path's metadata cannot be deleted while associations exist for it.

## Update an association

Write to `associations/<path>` with the same destination, and the same provider
identity fields when there are several. Fields you omit keep their stored
values.

Identity fields cannot change in place: `granularity`, the `secret-path`
`resolved_name`, the `secret-key` `name_template`, and provider identity fields.
The write fails with `<field> change would create a new association identity;
delete <association-id> first, then create the new association explicitly`.
For GitLab, a write with a new `environment_scope` creates a second association
instead. Recreating an association under a new name does not rename or delete
the old remote object.

Whether an update on an enabled association enqueues work depends on the
field:

- `format`, `data_mapping`, `data_key_template`, or provider attributes such as
  the GitLab variable flags: the write enqueues the current source version.
- `delete_mode` or AWS `delete_recovery_window_days` only: the write returns
  `sync_operation_ids=[]` with a `hint` and a `next_actions` entry for manual
  sync. The new policy applies to the next delete.
- `enabled=true` on a disabled association: the write enqueues the current
  source version.

## Choose a delete mode

`delete_mode` controls what happens to remote objects when the source is
deleted:

- `retain` (default) keeps remote objects.
- `orphan` is an alias of `retain`.
- `delete` removes owned remote objects.

`retain` and `orphan` keep the remote value and its ownership metadata. Neither
detaches ownership nor transfers the object to another association.

With `delete_mode=delete`, deleting or destroying the current source version
enqueues a remote delete for each enabled association on the path:

```sh
bao delete secret-sync/data/app/db
```

The provider deletes only objects whose ownership metadata matches the
association, and treats an already missing object as deleted. Undeleting the
current version enqueues upserts again. Disabling or deleting an association
never deletes remote objects.

> [!WARNING]
> Kubernetes and GitLab deletes take effect immediately, and the plugin cannot
> undo them. AWS Secrets Manager schedules deletion with a recovery window.
> Check the provider page before you enable `delete_mode=delete`.

See [Remote deletes](../concepts/ownership-and-drift.md#remote-deletes) for how
ownership guards apply to deletes.
