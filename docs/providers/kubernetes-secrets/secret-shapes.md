# Kubernetes Secrets secret shapes

Fresh mounts default `require_source_opt_in=false`; if strict source opt-in is
enabled, mark the source with `sources/<path>/enable` before creating an
enabled association. Use the [user guide](../../guides/user-guide.md) for
generic association lifecycle commands and
[Templating](../../concepts/templating.md) for name placeholder behavior.

The Kubernetes provider supports `secret-path` granularity with `format=json`.
Set `resolved_name` because the default `{{ path }}` template can contain `/`,
which is not valid in Kubernetes Secret names:

```sh
bao write secret-sync/data/app/db \
  username=app \
  password=initial

bao write secret-sync/associations/app/db/plan \
  destination=k8s/apps \
  resolved_name=app-db

bao write secret-sync/associations/app/db \
  destination=k8s/apps \
  resolved_name=app-db
```

The `resolved_name` must be a valid Kubernetes Secret name. Use a DNS-safe name
such as `app-db` instead of `app/db`.

Use `data_mapping=source-keys` when consumers expect individual Kubernetes
Secret `.data` entries instead of one `payload` entry:

```sh
bao write secret-sync/data/app/db \
  username=app \
  password=initial

bao write secret-sync/associations/app/db/plan \
  destination=k8s/apps \
  resolved_name=app-db \
  data_mapping=source-keys \
  data_key_template='{{ key }}'

bao write secret-sync/associations/app/db \
  destination=k8s/apps \
  resolved_name=app-db \
  data_mapping=source-keys \
  data_key_template='{{ key }}'
```

Source-key data mapping keeps one Kubernetes Secret object per association.
Only string and bytes source values are accepted. Rendered data keys must be
valid Kubernetes Secret keys: alphanumeric characters, `-`, `_`, or `.`.
Managed data keys are replaced on update. Unrelated existing keys are
preserved, but a desired managed key will not overwrite an unmanaged key.

The Kubernetes provider does not support `secret-key` granularity or
`format=raw`.

Kubernetes Secrets are limited to 1 MiB. The provider enforces that payload
limit before sending an upsert.
