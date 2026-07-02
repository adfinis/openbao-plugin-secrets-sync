# GitLab project variables

## What it writes

The GitLab provider writes project-level CI/CD variables. Each association
object maps to one GitLab variable key. The provider stores ownership metadata
and payload hash metadata in the variable description.

## Supported auth modes

Use a GitLab API token with the least project scope needed to manage CI/CD
variables:

```sh
bao write secret-sync/destinations/gitlab/prod \
  project_id=platform/app \
  environment_scope=production \
  token="$GITLAB_TOKEN"
```

`base_url` defaults to `https://gitlab.com`. Set `base_url` for self-managed
GitLab:

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
`allow_insecure_http=true`. Use HTTPS for production destinations.

GitLab destination config controls the attributes written to created or updated
project variables:

- `environment_scope`: GitLab environment scope. The default is `*`.
- `protected`: whether GitLab exposes the variable only to protected refs.
- `masked`: whether GitLab masks the variable value in job logs.
- `hidden`: whether GitLab creates the variable as masked and hidden.
- `variable_raw`: whether GitLab treats the value as a raw string. The default
  is `true`; set `false` only when GitLab expands variable references in the
  value.
- `variable_type`: `env_var` by default, or `file` when jobs need to receive a
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

GitLab can also reject masked values that match an existing CI/CD variable
name. Choose generated secret values that do not look like variable keys.

When `masked=true` and `variable_raw=false`, the provider only accepts
`format=raw` and rejects values with characters outside GitLab's expanded
masked variable character set. For ordinary masked CI/CD variables, prefer
`granularity=secret-key`, `format=raw`, and `variable_raw=true`.

## Supported association shapes

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

The GitLab provider also supports:

- `granularity=secret-key` with `format=json`;
- `granularity=secret-path` with `format=json`.

Use `secret-path` only when one GitLab variable needs to contain the full
canonical JSON payload. Do not combine masked variables, `variable_raw=false`,
and JSON payloads; use `secret-key` with `format=raw` for masked GitLab
variables.

The GitLab provider does not support destination-native data maps.

## Required permissions

Use a GitLab token that can read the target project and create, update, and
delete project CI/CD variables. The provider uses these GitLab API surfaces:

- `GET /projects/:id` for destination health;
- `GET /projects/:id/variables/:key` for plan and read-state checks;
- `POST /projects/:id/variables` for new variables;
- `PUT /projects/:id/variables/:key` for owned updates;
- `DELETE /projects/:id/variables/:key` for owned deletes.

Scope the token to the target project where possible.

## Sensitive fields

The backend stores `token` under the seal-wrapped destination secret prefix and
redacts it on destination reads.

Destination reads can show non-sensitive variable attributes such as
`project_id`, `environment_scope`, `protected`, `masked`, `hidden`,
`variable_raw`, and `variable_type`.

## Ownership and delete behavior

The provider stores ownership metadata in the GitLab variable description. The
metadata includes the association ID, source path, source version, object ID,
payload hash, plugin instance, and restore epoch. Owned update and delete
operations require matching ownership metadata. If ownership cannot be proven,
the provider returns an ownership error instead of mutating the variable.

Changing a GitLab destination updates stored config and validates the merged
provider settings, but it does not enqueue sync work for existing associations.
If a change to `protected`, `masked`, `variable_raw`, or `variable_type` needs
to be reflected in existing GitLab variables, plan the association and then
trigger a manual sync:

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

For variables owned by this plugin, the provider repairs these attribute
changes even when the source payload has not changed. The `hidden` flag is
different: GitLab accepts hidden variables only at creation time, so existing
visible variables cannot be converted to hidden variables by a sync.

Remote delete is sent only when the association uses `delete_mode=delete`.
Missing owned variables are treated idempotently.

## Validation and check commands

Read destination config. Sensitive fields are redacted:

```sh
bao read secret-sync/destinations/gitlab/prod
```

Check destination readiness:

```sh
bao read secret-sync/destinations/gitlab/prod/check
```

Use `validate` and `health` when you need separate configuration and runtime
diagnostics:

```sh
bao read secret-sync/destinations/gitlab/prod/validate
bao read secret-sync/destinations/gitlab/prod/health
```

## E2E test path

Use [GitLab e2e](../../test/e2e/gitlab/README.md) to test project variables in
a Dockerized GitLab CE stack.
