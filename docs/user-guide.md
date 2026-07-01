# User Guide

Status: draft
Date: 2026-06-30

This guide shows the current operator workflow for the OpenBao Secret Sync
plugin. The plugin stores source secrets in its own mount, then synchronizes
eligible source paths to configured external destinations.

The current implementation supports:

- KV-v2-like source storage under `data/*` and `metadata/*`;
- source opt-in through `custom_metadata.syncable=true`;
- destination config for the fake provider, AWS Secrets Manager, and
  Kubernetes Secrets;
- AWS SDK default auth and AWS assume-role auth;
- Kubernetes in-cluster auth and kubeconfig auth;
- asynchronous queue processing with manual `queue/drain`;
- association planning, create, manual sync, disable, enable, and delete;
- status inspection and explicit remote delete semantics.

Static AWS access keys and session tokens are intentionally not supported yet.
Use workload identity or `auth_mode=assume_role`.

## Install And Mount

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

Remote mutation is guarded by default. After a new mount, or after reviewing a
restore/clone event, explicitly acknowledge the guard before queue workers or
manual drains can write destination state:

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
```

## Configure AWS Secrets Manager

Default AWS SDK credential chain:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=default
```

Assume-role auth:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=assume_role \
  role_arn=arn:aws:iam::123456789012:role/openbao-secret-sync \
  external_id=tenant-or-environment-id \
  session_name=openbao-secret-sync
```

Custom endpoints require an explicit endpoint policy. Use `local` for
LocalStack and other local development endpoints:

```sh
bao write secret-sync/destinations/aws-sm/local \
  region=us-east-1 \
  auth_mode=default \
  endpoint_url=http://localstack:4566 \
  endpoint_policy=local
```

Use `private` only for explicitly approved HTTPS private endpoint deployments:

```sh
bao write secret-sync/destinations/aws-sm/private \
  region=eu-central-1 \
  auth_mode=assume_role \
  role_arn=arn:aws:iam::123456789012:role/openbao-secret-sync \
  external_id=tenant-or-environment-id \
  endpoint_url=https://vpce-1234567890abcdef.secretsmanager.eu-central-1.vpce.amazonaws.com \
  endpoint_policy=private
```

Read destination config. Sensitive fields are redacted:

```sh
bao read secret-sync/destinations/aws-sm/prod
```

Validate and check health:

```sh
bao write -force secret-sync/destinations/aws-sm/prod/validate
bao read secret-sync/destinations/aws-sm/prod/health
```

## Constrain Destination Use

Destinations can restrict which source paths and remote object names may use
them. These fields are useful when delegated app owners can create
associations but should only sync their own path and remote prefix:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=default \
  allowed_source_path_prefixes=apps/team-a,shared/team-a \
  allowed_resolved_name_prefixes=openbao-secret-sync/team-a/
```

`allowed_source_path_prefixes` uses OpenBao source path segment boundaries:
`apps/team-a` allows `apps/team-a/db` but not `apps/team-alpha/db`.
`allowed_resolved_name_prefixes` is a literal remote-name prefix. Keep a
trailing `/` when you want a folder-like boundary.

## Configure Kubernetes Secrets

Use in-cluster auth when OpenBao runs in the target Kubernetes cluster:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=in_cluster
```

Use kubeconfig auth for local development or external cluster access:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=kubeconfig \
  kubeconfig_path="$HOME/.kube/config" \
  context=kind-openbao
```

The Kubernetes provider writes one `Opaque` Secret per `secret-path`
association. The canonical payload is stored in the Secret `data.payload` key.
Ownership metadata is stored in labels and annotations. The `resolved_name`
must be a valid Kubernetes Secret name, so use a DNS-safe name such as `app-db`
instead of `app/db`.

Validate and check health:

```sh
bao write -force secret-sync/destinations/k8s/apps/validate
bao read secret-sync/destinations/k8s/apps/health
```

## Configure GitLab Project Variables

The GitLab provider writes project-level CI/CD variables. Use a token with the
least project scope needed to manage CI/CD variables:

```sh
bao write secret-sync/destinations/gitlab/prod \
  project_id=platform/app \
  environment_scope=production \
  token="$GITLAB_TOKEN"
```

For self-managed GitLab, set `base_url`:

```sh
bao write secret-sync/destinations/gitlab/prod \
  base_url=https://gitlab.example.com \
  project_id=platform/app \
  environment_scope=production \
  protected=true \
  variable_raw=true \
  token="$GITLAB_TOKEN"
```

Non-local `http://` GitLab URLs are rejected by default. For a local Docker or
private test network that intentionally uses HTTP, set
`allow_insecure_http=true`; production destinations should use HTTPS.

Sensitive fields are redacted and seal-wrapped:

```sh
bao read secret-sync/destinations/gitlab/prod
```

Validate and check health:

```sh
bao write -force secret-sync/destinations/gitlab/prod/validate
bao read secret-sync/destinations/gitlab/prod/health
```

## Write Source Data

Mark a source path as syncable:

```sh
bao write secret-sync/metadata/app/db \
  @<(printf '%s' '{"custom_metadata":{"syncable":"true"}}')
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

## Plan And Create An Association

Plan first. Planning reads remote metadata where the provider supports it, but
does not mutate remote state:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=aws-sm \
  destination_name=prod \
  resolved_name=openbao-secret-sync/app/db \
  granularity=secret-path \
  format=json \
  delete_mode=delete
```

Create the association:

```sh
bao write secret-sync/associations/app/db \
  destination_type=aws-sm \
  destination_name=prod \
  resolved_name=openbao-secret-sync/app/db \
  granularity=secret-path \
  format=json \
  delete_mode=delete
```

For Kubernetes, use the `k8s` destination type and a Kubernetes-safe
`resolved_name`:

```sh
bao write secret-sync/associations/app/db \
  destination_type=k8s \
  destination_name=apps \
  resolved_name=app-db \
  granularity=secret-path \
  format=json \
  delete_mode=delete
```

`secret-key` granularity creates one destination object per top-level source
key. It requires `name_template` instead of `resolved_name`, and the template
must include `{{ key }}`:

```sh
bao write secret-sync/associations/app/db \
  destination_type=fake \
  destination_name=default \
  name_template='prod/{{ path }}/{{ key }}' \
  granularity=secret-key \
  format=json \
  delete_mode=retain
```

For `json` format, each remote object receives canonical JSON containing only
its source key. For example, source data with `password` and `username` creates
objects such as `prod/app/db/password` and `prod/app/db/username`. Source keys
used with `secret-key` granularity must be non-empty, have no surrounding
whitespace, and must not contain `/`, `.`, or `..`.

For GitLab project variables, use `secret-key` with `format=raw` so each
source key becomes one CI/CD variable value. GitLab variable keys may contain
only letters, digits, and `_`, so choose a compatible template:

```sh
bao write secret-sync/associations/app/db \
  destination_type=gitlab \
  destination_name=prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete
```

Current provider support:

- `fake`: `secret-path` and `secret-key`.
- `aws-sm`: `secret-path` only.
- `gitlab`: `secret-path` and `secret-key`; `secret-key` with `format=raw` is
  the recommended shape for CI/CD variables.
- `k8s`: `secret-path` only.

The write returns `sync_operation_ids`. Queue processing is asynchronous.
For one-to-one associations, lifecycle responses also include top-level fields
such as `association_id`, `destination_ref`, `resolved_name`, `enabled`, and
`delete_mode` so they are easy to read in the default `bao` table output. The
nested `association` object remains available for scripts.

## Process And Inspect Queue Work

Drain due operations manually for deterministic testing or controlled catch-up:

```sh
bao write secret-sync/queue/drain max_operations=10
```

Inspect queue summary:

```sh
bao read secret-sync/queue
```

Queue summaries include pending, retry-wait, terminal, and canceled counters.
`oldest_age_seconds` reports the age of the oldest pending or retry-wait
operation.

Inspect, retry, or cancel one operation:

```sh
bao read secret-sync/queue/<operation-id>
bao write -force secret-sync/queue/<operation-id>/retry
bao write -force secret-sync/queue/<operation-id>/cancel
```

## Reconcile Remote State

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

## Check Sync Status

Read per-source status:

```sh
bao read secret-sync/status/app/db
```

Common states in the current implementation include:

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
`payload_sha256`, `remote_version`, and `last_operation_id`. The full
per-object list is still available under `objects`. Status records include
versions, destination references, remote names, and payload hashes. They must
not include secret payload values.

Use JSON output when copying identifiers into follow-up commands:

```sh
bao read -format=json secret-sync/status/app/db | jq .data
```

## Update Or Delete Source Data

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

## Association Lifecycle

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

Delete an association:

```sh
bao delete secret-sync/associations/app/db/<association-id>
```

Deleting an association does not delete remote state by itself. Use source
delete with `delete_mode=delete` when owned remote deletion is required.

## Troubleshooting

For operational response flows and evidence to capture, see the
[Operator runbook](operator-runbook.md).

If sync does not happen:

- confirm `metadata/<path>` has `custom_metadata.syncable=true`;
- run `destinations/<type>/<name>/validate`;
- run `destinations/<type>/<name>/health`;
- inspect `queue` and the returned operation IDs;
- inspect `status/<path>`;
- verify the association is enabled and the destination is not disabled;
- verify remote names are not already owned by another association.

If AWS custom endpoints fail validation:

- use no `endpoint_url` for normal AWS endpoints;
- use `endpoint_policy=local` only for local development endpoints;
- use `endpoint_policy=private` only for approved HTTPS private endpoints;
- do not put credentials or userinfo in endpoint URLs.
