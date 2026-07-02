# GitLab project variables


The GitLab provider writes project-level CI/CD variables.

## Configure GitLab.com

Use a token with the least project scope needed to manage CI/CD variables:

```sh
bao write secret-sync/destinations/gitlab/prod \
  project_id=platform/app \
  environment_scope=production \
  token="$GITLAB_TOKEN"
```

## Configure self-managed GitLab

Set `base_url` for self-managed GitLab:

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
`allow_insecure_http=true`. Production destinations should use HTTPS.

## Configure variable attributes

GitLab destination config controls the attributes written to created or updated
project variables:

- `environment_scope`: GitLab environment scope. The default is `*`.
- `protected`: whether GitLab should expose the variable only to protected refs.
- `masked`: whether GitLab should mask the variable value in job logs.
- `hidden`: whether GitLab should create the variable as masked and hidden.
- `variable_raw`: whether GitLab treats the value as a raw string. The default
  is `true`; set `false` only when GitLab should expand variable references in
  the value.
- `variable_type`: `env_var` by default, or `file` when jobs should receive a
  temporary file path.

`hidden=true` implies `masked=true`. GitLab only supports making a variable
hidden when the variable is created, so the provider blocks attempts to turn an
existing visible variable into a hidden variable. To use `hidden=true` for an
already-visible variable, remove or rename the existing GitLab variable first,
then sync it again as a new variable.

Masked and hidden variables must use values that GitLab accepts for masked
variables. The provider pre-validates the requirements it can check locally:

- the value must be valid UTF-8;
- the value must be at least 8 characters;
- the value must be a single line with no whitespace.

GitLab can also reject masked values that match an existing CI/CD variable name.
Choose generated secret values that do not look like variable keys.

When `masked=true` and `variable_raw=false`, the provider only accepts
`format=raw` and rejects values with characters outside GitLab's expanded masked
variable character set. For ordinary masked CI/CD variables, prefer
`granularity=secret-key`, `format=raw`, and `variable_raw=true`.

## Validate the destination

Sensitive fields are redacted and seal-wrapped:

```sh
bao read secret-sync/destinations/gitlab/prod
```

Check destination readiness:

```sh
bao read secret-sync/destinations/gitlab/prod/check
```

## Create project variables

Mark the source path as syncable and write the current source secret before
planning an association:

```sh
bao write -force secret-sync/sources/app/db/enable

bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"password":"initial-password"}}')
```

Use `secret-key` granularity with `format=raw` for normal CI/CD variable use.
Each top-level OpenBao source key becomes one GitLab CI/CD variable value.
GitLab variable keys may contain only letters, digits, and `_`, so choose a
compatible template:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=gitlab \
  destination_name=prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete

bao write secret-sync/associations/app/db \
  destination_type=gitlab \
  destination_name=prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete
```

Source keys used with `secret-key` granularity must be non-empty, have no
surrounding whitespace, and must not contain `/`, `.`, or `..`.

The GitLab provider also supports `secret-path` associations. Use that shape
only when one GitLab variable should contain the full canonical JSON payload.
Do not combine masked variables, `variable_raw=false`, and JSON payloads; use
`secret-key` with `format=raw` for masked GitLab variables.

## Reapply destination changes

Changing a GitLab destination updates stored config and validates the merged
provider settings, but it does not enqueue sync work for existing associations.
If a change to `protected`, `masked`, `variable_raw`, or `variable_type` should
be reflected in existing GitLab variables, plan the association and then trigger
a manual sync:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=gitlab \
  destination_name=prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete

bao write -force secret-sync/associations/app/db/<association-id>/sync
bao write secret-sync/queue/drain max_operations=10
```

For variables owned by this plugin, the provider repairs these attribute changes
even when the source payload has not changed. The `hidden` flag is different:
GitLab accepts hidden variables only at creation time, so existing visible
variables cannot be converted to hidden variables by a sync.

## Test the provider

Use [GitLab e2e](../../test/e2e/gitlab/README.md) to test project variables in
a Dockerized GitLab CE stack.
