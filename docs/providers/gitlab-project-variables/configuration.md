# GitLab project variables configuration

## Supported auth modes

Use a GitLab API token with the least project scope needed to manage CI/CD
variables:

```sh
bao write secret-sync/destinations/gitlab/prod \
  project_id=platform/app \
  token="$GITLAB_TOKEN"
```

`base_url` defaults to `https://gitlab.com`. Set `base_url` for self-managed
GitLab:

```sh
bao write secret-sync/destinations/gitlab/prod \
  base_url=https://gitlab.example.com \
  project_id=platform/app \
  token="$GITLAB_TOKEN"
```

`base_url` must include a host and must not include userinfo, query strings, or
fragments. A path prefix is allowed and is kept before `/api/v4`.

GitLab base URLs that target localhost, private addresses, link-local
addresses, multicast, unspecified addresses, or DNS names that resolve to those
ranges are rejected by default. Set `allow_private_network=true` only for an
approved self-managed GitLab endpoint on a private or local network.

Non-local `http://` GitLab URLs are rejected by default. For a non-local Docker
or private test network that intentionally uses HTTP, set both
`allow_private_network=true` and `allow_insecure_http=true`. Localhost HTTP is
allowed only when `allow_private_network=true`. Use HTTPS for production
destinations.

Without the private-network opt-in, the provider resolves the GitLab name again
for every new connection and dials an approved address directly, so DNS changes
cannot bypass the address policy between validation and connection. The
provider HTTP client uses a 30-second timeout, does not follow redirects, and
does not use ambient proxy configuration from the OpenBao process environment.

## Association variable attributes

Each GitLab association controls the attributes written to the project
variables it creates or updates. For example:

```sh
bao write secret-sync/associations/app/ci \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete \
  environment_scope=production \
  protected=true \
  variable_raw=true
```

The association fields are:

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

The provider uses `environment_scope` when reading, updating, and deleting
variables, so the same variable key can be managed independently for different
GitLab environment scopes. The scope participates in association identity and
remote-name reservations. Supplying a different scope on the association write
route creates a separate association instead of renaming an existing one.

The backend normalizes all six fields and returns their effective string values
under `provider_config` in association read, write, and plan responses.

## Sensitive fields

The backend stores `token` under the seal-wrapped destination secret prefix and
redacts it on destination reads.

Destination reads show connection-level fields such as `base_url`, `project_id`,
and network policy options. Association reads show non-sensitive variable
attributes under `provider_config`.

## Validation and check commands

Read destination config. Sensitive fields are redacted:

```sh
bao read secret-sync/destinations/gitlab/prod
bao read secret-sync/associations/app/ci
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
