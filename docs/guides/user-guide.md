# User guide


This guide shows the operator workflow for the OpenBao Secret Sync plugin. The
plugin stores source secrets in its own mount, then synchronizes eligible
source paths to configured external destinations.

The plugin supports:

- KV-v2-like source storage under `data/*` and `metadata/*`;
- optional source opt-in through `sources/<path>/enable` or
  `custom_metadata.syncable=true` when `require_source_opt_in=true`;
- destination config for AWS Secrets Manager, Kubernetes Secrets, and GitLab
  project variables;
- asynchronous queue processing with manual `queue/drain`;
- association planning, create, manual sync, disable, enable, and delete;
- status inspection and explicit remote delete semantics.

## Install and mount

Build the plugin binary:

```sh
make build
```

Register the plugin with OpenBao:

```sh
bao plugin register \
  -sha256="$(shasum -a 256 bin/openbao-plugin-secrets-sync | awk '{print $1}')" \
  -command=openbao-plugin-secrets-sync \
  -version=v0.0.0-dev \
  secret openbao-plugin-secrets-sync
```

Mount the secret engine:

```sh
bao secrets enable \
  -path=secret-sync \
  -plugin-name=openbao-plugin-secrets-sync \
  -plugin-version=v0.0.0-dev \
  plugin
```

Fresh mounts start with remote mutation allowed. If `restore_guard=true` after
a restore, clone, or manual restore-guard rearm, review destination safety
before acknowledging the guard:

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
```

Fresh mounts also default `require_source_opt_in=false`, so creating an enabled
association is the source authorization step. To require per-source
`custom_metadata.syncable=true` before association activation or dispatch, set:

```sh
bao write secret-sync/config require_source_opt_in=true
```

## Choose a provider

Configure at least one destination before you create an association:

- [AWS Secrets Manager](../providers/aws-secrets-manager.md)
- [Kubernetes Secrets](../providers/kubernetes-secrets.md)
- [GitLab project variables](../providers/gitlab-project-variables.md)

Provider docs include destination config, supported association shape, naming
constraints, and provider-specific troubleshooting.

## Paginate list responses

Public LIST endpoints accept OpenBao paginated-list parameters:

- `after`: optional key to begin listing after; the key does not need to
  exist in the result set;
- `limit`: optional maximum number of keys to return; non-positive values
  return all keys.

This applies to `metadata`, `metadata/<path>`, `destinations/<type>`, and
`associations`. Use pagination for automation that may operate across many
source paths or associations.

## Constrain destination use

Destinations can restrict which source paths and remote object names may use
them. These fields are useful when delegated app owners can create
associations that only sync their own path and remote prefix.

Add these fields when you configure a destination. This fragment omits
provider-specific required fields:

```text
bao write secret-sync/destinations/PROVIDER_TYPE/NAME \
  PROVIDER_SPECIFIC_FIELDS \
  allowed_source_path_prefixes=apps/team-a,shared/team-a \
  allowed_resolved_name_prefixes=openbao-plugin-secrets-sync/team-a/
```

Destination writes validate the merged provider config before storing it.
Non-empty fields from another provider type are rejected.

`allowed_source_path_prefixes` uses OpenBao source path segment boundaries:
`apps/team-a` allows `apps/team-a/db` but not `apps/team-alpha/db`.
`allowed_resolved_name_prefixes` uses exact or `/`-boundary matches:
`openbao-plugin-secrets-sync/team-a` allows
`openbao-plugin-secrets-sync/team-a/db` but not
`openbao-plugin-secrets-sync/team-alpha/db`.

## Write source data

Source paths are slash-separated OpenBao paths. They cannot contain empty,
`.` or `..` segments, cannot contain the reserved `versions` segment, and
cannot end in the reserved `plan` segment.

Mark a source path as syncable when `require_source_opt_in=true`:

```sh
bao write -force secret-sync/sources/app/db/enable
```

Write the source secret:

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"password":"initial"}}')
```

Read the latest source version:

```sh
bao read secret-sync/data/app/db
```

Check source readiness before creating the association:

```sh
bao read secret-sync/sources/app/db/check
```

## Plan and create an association

Plan first. Planning reads remote metadata where the provider supports it, but
does not mutate remote state:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=aws-sm \
  destination_name=prod
```

Create the association:

```sh
bao write secret-sync/associations/app/db \
  destination_type=aws-sm \
  destination_name=prod
```

The default association shape is `granularity=secret-path`, `format=json`,
`data_mapping=payload`, `delete_mode=retain`, `enabled=true`, and
`name_template='{{ path }}'`. Set `resolved_name`, `name_template`, `format`,
`data_mapping`, `data_key_template`, or `delete_mode` only when the destination
needs a different remote name, payload shape, data-key mapping, or delete
behavior.
Create and plan responses include a `defaults` object beside the effective
values so these defaults are visible in CLI and API output.

When updating an existing association, omitted optional fields keep the stored
values if the source path and destination match exactly one association. A
partial update such as changing only `delete_mode` will not change granularity,
name template, format, or enabled state.
Changing an existing association from `enabled=false` to `enabled=true`
through the same write path queues the current source version, matching the
explicit lifecycle enable endpoint.

For provider-specific association examples, supported granularities, and remote
name constraints, see the [provider guides](../providers/README.md).

Some providers support `secret-key` granularity, which creates one destination
object per top-level source key. Source keys used with `secret-key`
granularity must be non-empty, have no surrounding whitespace, and must not
contain `/`, `.`, or `..`.

Some providers support `data_mapping=source-keys`, which keeps
`secret-path` granularity but maps top-level source keys into destination-native
data keys inside one remote object. For Kubernetes Secrets this writes one
Secret object whose `.data` entries are rendered from `data_key_template`.

The write returns `sync_operation_ids`. Queue processing is asynchronous.
For one-to-one associations, lifecycle responses also include top-level fields
such as `association_id`, `destination_ref`, `resolved_name`, `enabled`, and
`delete_mode` so they are easy to read in the default `bao` table output. The
nested `association` object remains available for scripts.

## Process and inspect queue work

Drain due operations manually for deterministic testing or controlled catch-up:

```sh
bao write secret-sync/queue/drain max_operations=10
```

Inspect queue summary:

```sh
bao read secret-sync/queue
```

Queue summaries include pending, retry-wait, claimed, and terminal counters.
`queue_capacity=0` is an explicit enqueue freeze: writes that would create
sync work fail before committing a new source version.
`oldest_age_seconds` reports the age of the oldest pending or retry-wait
operation. Successful and canceled operations are removed from the queue;
inspect `status/<path>` for success evidence.

Newer writes supersede older inactive queued upserts for the same association
object. Current-version deletes and destroys cancel queued upserts and queue
remote deletes when the association uses `delete_mode=delete`; undeleting the
current version queues replacement upserts for enabled associations.

Inspect, retry, or cancel one operation:

```sh
bao read secret-sync/queue/<operation-id>
bao write -force secret-sync/queue/<operation-id>/retry
bao write -force secret-sync/queue/<operation-id>/cancel
```

Cancel discards queued work. Re-enqueue with an association sync or source
write if the remote mutation is needed again.

## Reconcile remote state

Plan reconcile without changing local status or remote objects:

```sh
bao read secret-sync/reconcile/app/db/plan
```

Apply reconcile to update local status from provider read-state metadata:

```sh
bao write -force secret-sync/reconcile/app/db
```

Reconcile is safe to run while restore guard is active because it does not
mutate destination secrets. It reports remote existence, ownership metadata,
payload hash metadata, source version metadata, and stable failure states where
the provider supports those fields.

## Check sync status

Read per-source status:

```sh
bao read secret-sync/status/app/db
```

Common states include:

- `SYNCED`: remote state was updated successfully;
- `PENDING`: sync work is queued or waiting;
- `NO_ASSOCIATION`: the source path exists but has no sync association or
  object status yet;
- `DISABLED`: association or destination is disabled;
- `REMOTE_MISSING`: an owned delete completed or remote object is absent;
- `REMOTE_OWNERSHIP_LOST`, `DESTINATION_AUTH_ERROR`,
  `DESTINATION_POLICY_ERROR`, `DESTINATION_RATE_LIMITED`,
  `DESTINATION_UNAVAILABLE`, `VALIDATION_ERROR`, `QUEUE_BLOCKED`, and
  `DRIFTED`: provider or safety failures that require operator inspection.

For the common single-object case, status includes top-level summary fields
such as `association_id`, `destination_ref`, `resolved_name`,
`remote_version`, and `last_operation_id`. The full per-object list is still
available under `objects`. Status records include versions, destination
references, remote names, operation ids, and error classes. They must not
include secret payload values or payload hashes.

Use JSON output when copying identifiers into follow-up commands:

```sh
bao read -format=json secret-sync/status/app/db | jq .data
```

## Update or delete source data

Updating the source path enqueues sync for enabled associations:

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"password":"updated"}}')

bao write secret-sync/queue/drain max_operations=10
```

Deleting the latest source version enqueues remote delete only for associations
with `delete_mode=delete`:

```sh
bao delete secret-sync/data/app/db
bao write secret-sync/queue/drain max_operations=10
```

Use `delete_mode=retain` when remote secrets must remain after local source
deletion. This is the default.

## Association lifecycle

Read associations for a source path:

```sh
bao read secret-sync/associations/app/db
```

Disable, enable, or manually sync an association:

```sh
bao write -force secret-sync/associations/app/db/<association-id>/disable
bao write -force secret-sync/associations/app/db/<association-id>/enable
bao write -force secret-sync/associations/app/db/<association-id>/sync
```

Destination config writes validate and store the new provider settings, but do
not enqueue existing associations by themselves. If a destination config change
needs to update remote object attributes, run an association plan to review the
desired state. Then use manual association sync or write a new source version
to enqueue work.

Delete an association:

```sh
bao delete secret-sync/associations/app/db/<association-id>
```

Deleting an association does not delete remote state by itself. Use source
delete with `delete_mode=delete` when owned remote deletion is required.

## Troubleshooting

For operational response flows and evidence to capture, see the
[Operator runbook](../operations/operator-runbook.md).

If sync does not happen:

- read `sources/<path>/check`;
- read `destinations/<type>/<name>/check`;
- inspect `queue` and the returned operation IDs;
- inspect `status/<path>`;
- verify the association is enabled and the destination is not disabled;
- verify remote names are not already owned by another association;
- use the relevant provider guide for provider-specific validation and naming
  rules.
