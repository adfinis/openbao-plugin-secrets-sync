# User guide

This guide is the first-success path for the OpenBao Secret Sync plugin. It
mounts the engine, points you to provider configuration, writes one source
secret, creates one association, and checks convergence.

Use the [source model](../concepts/source-model.md) to understand why source
data lives in this mount. Use the [sync model](../concepts/sync-model.md) for
associations and provider object shapes. Use [Convergence](../concepts/convergence.md)
for queued work, status, manual sync, retry, and drain behavior. Use
[Reconcile and drift](../concepts/reconcile-and-drift.md) for provider
read-state checks and background drift modes. Use
[Ownership and safety](../concepts/ownership-and-safety.md) for stale or
foreign remote objects. Use the [operator runbook](../operations/operator-runbook.md)
when sync is blocked or you need recovery procedures.

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

For release artifact verification and production install details, use
[Install and verify release artifacts](../operations/install-and-verify.md).

## Configure a destination

Configure at least one destination before you create an association:

- [AWS Secrets Manager](../providers/aws-secrets-manager/README.md)
- [Kubernetes Secrets](../providers/kubernetes-secrets/README.md)
- [GitLab project variables](../providers/gitlab-project-variables/README.md)

Provider docs include destination config, auth choices, supported secret
shapes, naming constraints, and provider-specific validation commands.

## Write source data

Source paths are slash-separated OpenBao paths. They cannot contain empty,
`.` or `..` segments, cannot contain the reserved `versions` segment, and
cannot end in reserved route segments such as `plan`, `disable`, `enable`, or
`sync`.

Write the source secret:

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"username":"app","password":"initial"}}')
```

Read the latest source version:

```sh
bao read secret-sync/data/app/db
```

Check source readiness before creating the association:

```sh
bao read secret-sync/sources/app/db/check
```

## Create an association

Plan first. Planning reads remote metadata where the provider supports it, but
does not mutate remote state:

```sh
bao write secret-sync/associations/app/db/plan \
  destination=aws-sm/prod
```

Create the association:

```sh
bao write secret-sync/associations/app/db \
  destination=aws-sm/prod
```

Association requests identify the destination with `destination=<type>/<name>`.
The default shape is one canonical JSON payload per source path:
`granularity=secret-path`, `format=json`, `data_mapping=payload`,
`delete_mode=retain`, `enabled=true`, and `name_template='{{ path }}'`.

Use [Secret shapes](secret-shapes.md) when you need Kubernetes source-key data
maps, GitLab per-key variables, raw values, or provider-specific shape
selection. Use [Templating](../concepts/templating.md) when remote object
names need `resolved_name`, `name_template`, or `data_key_template`. Use
`secret-sync/info` to inspect static association defaults and registered
provider capability flags.

## Check convergence

Association writes return `sync_operation_ids` when remote work is queued. The
operation IDs mean OpenBao accepted desired local state and queued provider
work; they do not mean the provider has already converged.

Normal mounts wake queue processing after enqueue. For deterministic local
testing or controlled catch-up, drain due work explicitly:

```sh
bao write secret-sync/queue/drain max_operations=10
```

Read per-source status:

```sh
bao read secret-sync/status/app/db
```

For the common case, wait for `state=SYNCED`. If the status is blocked,
terminal, or unclear, follow returned `hint` and `next_actions` first, then use
the [operator runbook](../operations/operator-runbook.md).

## Update or delete source data

Updating the source path enqueues sync for enabled associations:

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"username":"app","password":"updated"}}')
```

Deleting the latest source version enqueues remote delete only for associations
with `delete_mode=delete`:

```sh
bao delete secret-sync/data/app/db
```

Use `delete_mode=retain` when remote secrets must remain after local source
deletion. This is the default.

## Common association commands

Read associations for a source path:

```sh
bao read secret-sync/associations/app/db
```

Disable, enable, or manually sync one association:

```sh
bao write secret-sync/associations/app/db/disable destination=aws-sm/prod
bao write secret-sync/associations/app/db/enable destination=aws-sm/prod
bao write secret-sync/associations/app/db/sync destination=aws-sm/prod
```

Use destination-addressed lifecycle commands for normal operations.
Association IDs remain useful for exact reads, deletes, and rare ambiguity
cases:

```sh
bao read secret-sync/associations/app/db/<association-id>
bao delete secret-sync/associations/app/db/<association-id>
```

Deleting an association does not delete remote state by itself. Use source
delete with `delete_mode=delete` when owned remote deletion is required.

## Next steps

- Use [Secret shapes](secret-shapes.md) to choose between JSON objects,
  Kubernetes data maps, and GitLab per-key variables.
- Use [Delegated use](delegated-use.md) for strict source opt-in and destination
  prefix constraints.
- Use [Runtime configuration](../operations/runtime-configuration.md) for
  mount-wide pause, restore guard, drift repair, queue capacity, and dispatch
  tuning.
- Use [API compatibility](../reference/api-compatibility.md) for pagination and
  KV-v2-like source API differences.
