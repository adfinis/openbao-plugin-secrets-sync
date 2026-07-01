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

## Test the provider

Use [GitLab e2e](../../test/e2e/gitlab/README.md) to test project variables in
a Dockerized GitLab CE stack.
