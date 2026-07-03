# Secret shapes

Use this guide when you need to choose how one OpenBao source path becomes
remote provider objects. For the end-to-end first sync workflow, use the
[user guide](user-guide.md). For provider-specific constraints, use the
[provider guides](../providers/README.md). For naming placeholders and
reservation behavior, use [Templating](../concepts/templating.md).

## Shape matrix

| Goal | Association shape | Provider fit |
| --- | --- | --- |
| One remote secret containing the full source payload as JSON. | `granularity=secret-path`, `format=json`, `data_mapping=payload` | AWS Secrets Manager, simple Kubernetes Secrets, occasional GitLab JSON variables. |
| One Kubernetes Secret with one `.data` entry per source key. | `granularity=secret-path`, `format=json`, `data_mapping=source-keys` | Kubernetes Secrets. |
| One remote value per top-level source key. | `granularity=secret-key`, `format=raw` | GitLab project CI/CD variables. |
| One remote JSON value per top-level source key. | `granularity=secret-key`, `format=json` | Provider-specific GitLab use cases. |

## One JSON object

The default shape is `secret-path` with one canonical JSON payload. Use it when
one remote object should contain the whole source payload.

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"username":"app","password":"initial"}}')

bao write secret-sync/associations/app/db \
  destination=aws-sm/prod
```

That creates or updates one AWS Secrets Manager secret named `app/db`. Set
`resolved_name` or `name_template` when the remote secret name must differ
from the source path.

## One native data map

Use `data_mapping=source-keys` when one remote object should expose top-level
source keys as destination-native data keys. This is useful for Kubernetes
Secrets where applications read keys such as `username` and `password`.

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"username":"app","password":"initial"}}')

bao write secret-sync/associations/app/db \
  destination=k8s/apps \
  resolved_name=app-db \
  data_mapping=source-keys \
  data_key_template='{{ key }}'
```

That creates or updates one Kubernetes Secret named `app-db`. The source keys
become Secret `.data` keys. Source values used with `source-keys` must be
strings or bytes.

## One value per source key

Use `secret-key` with `format=raw` when each top-level source key should become
a separate remote value. This is the normal GitLab project CI/CD variable
shape.

```sh
bao write secret-sync/data/app/ci \
  @<(printf '%s' '{"data":{"USERNAME":"app","PASSWORD":"initial"}}')

bao write secret-sync/associations/app/ci \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete
```

That creates or updates GitLab variables named `APP_USERNAME` and
`APP_PASSWORD`. Choose source key names that already match the desired variable
suffix because `{{ key }}` is substituted as-is. Source values used with
`format=raw` must be strings or bytes.

## One JSON value per source key

GitLab also supports `secret-key` with `format=json`. Each top-level source key
becomes a separate GitLab variable, but the value is canonical JSON for that
single key rather than raw bytes.

```sh
bao write secret-sync/associations/app/ci \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=json \
  delete_mode=delete
```

Use this only when the consumer expects JSON in each remote value. For normal
CI/CD variables, prefer `format=raw`.

Run the same association fields against `associations/<path>/plan` first when
you want to inspect provider metadata and planned action without changing local
state or the destination.
