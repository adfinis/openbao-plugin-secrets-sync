---
verifiedBy:
  - internal/providers/gitlab/project_variables.go
  - internal/backend/path_destinations.go
  - internal/backend/path_associations.go
  - test/e2e/gitlab
---

# Sync to GitLab project variables

The `gitlab` provider writes project-level CI/CD variables. Each association
object becomes one variable key in one environment scope. The provider records
ownership in the variable description.

## Choose an auth mode

The provider authenticates with a GitLab API token sent as `PRIVATE-TOKEN`.
Set `project_id` to the numeric project ID or the full project path. The
provider URL-encodes the path itself:

```sh
bao write secret-sync/destinations/gitlab/prod \
  project_id=platform/app \
  token="$GITLAB_TOKEN"
```

The backend seal-wraps `token` and redacts it on reads. `base_url` defaults to
`https://gitlab.com`.

## Connect to self-managed GitLab

Set `base_url` to the instance URL. It must have a host and no userinfo, query,
or fragment. A path prefix is kept in front of `/api/v4`:

```sh
bao write secret-sync/destinations/gitlab/prod \
  base_url=https://gitlab.example.com \
  project_id=platform/app \
  token="$GITLAB_TOKEN"
```

`base_url` rejects localhost and restricted address ranges unless you set
`allow_private_network=true`. See
[Endpoint address policy](../security/threat-model.md#endpoint-address-policy).
An `http://` URL also requires `allow_insecure_http=true` unless the host is
local. Use HTTPS for production.

The HTTP client has a 30-second timeout, does not follow redirects, and ignores
proxy environment variables, so OpenBao must reach GitLab directly.

## Grant permissions

The token's user must be able to read the project and create, update, and
delete its CI/CD variables. Scope the token to the target project where
possible. The provider calls:

- `GET /projects/:id` for health;
- `GET /projects/:id/variables/:key` for plan and read-state;
- `POST /projects/:id/variables` to create variables;
- `PUT /projects/:id/variables/:key` for owned updates; and
- `DELETE /projects/:id/variables/:key` for owned deletes.

Key-addressed calls filter on the association's `environment_scope`.

## Create an association

GitLab supports `secret-key` with `format=raw` (recommended for CI/CD
variables), `secret-key` with `format=json`, and `secret-path` with
`format=json`. It does not support `source-keys`. See
[Choose a secret shape](associations.md#choose-a-secret-shape). The value limit
is 10,000 bytes.

Variable keys may contain only letters, digits, and `_`, up to 255 bytes, so
choose a template that renders valid keys from your source keys:

```sh
bao write secret-sync/data/app/ci USERNAME=app PASSWORD=<password>

bao write secret-sync/associations/app/ci \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete \
  environment_scope=production
```

This creates `APP_USERNAME` and `APP_PASSWORD` in the `production` scope. Plan
with the same fields before each write.

## Set variable attributes

Variable attributes belong to the association. Reads, writes, and plans return
their effective values under `provider_config`.

| Field | Default | Effect |
| --- | --- | --- |
| `environment_scope` | `*` | GitLab environment scope. Part of the association identity. |
| `protected` | `false` | Expose the variable only to protected refs. |
| `masked` | `false` | Mask the value in job logs. |
| `hidden` | `false` | Create the variable masked and hidden. Implies `masked=true`. |
| `variable_raw` | `true` | Disable variable reference expansion. |
| `variable_type` | `env_var` | `env_var`, or `file` to deliver the value as a file path. |

Changing `protected`, `masked`, `hidden`, `variable_raw`, or `variable_type` on
an enabled association enqueues the current source version, and the provider
converges the attributes even when the value is unchanged.

`environment_scope` cannot change in place. A write with a new scope creates a
separate association, so one key can be managed independently in several
scopes. With more than one scope, include `environment_scope` in plans and
writes, and use association ID routes for lifecycle operations. See
[Select an association](associations.md#select-an-association).

GitLab allows hiding a variable only when it is created. When the association
sets `hidden=true` and the existing variable is visible, plan reports
`action=blocked` and upsert fails with a `validation` error. To hide it, remove
or rename the GitLab variable, then sync again.

Masked and hidden values must be valid UTF-8, at least 8 characters, and a
single line with no whitespace. With `masked=true` and `variable_raw=false`,
the association must use `format=raw`, and the value may contain only letters,
digits, and `_ : @ - + . ~ = /`. Plan checks the length and format rules;
upsert checks all of them. For masked variables, prefer `secret-key`,
`format=raw`, and `variable_raw=true`.

## Ownership metadata

The provider writes a single-line description, at most 255 bytes:

```text
OpenBao sync: <source-path> <object-id> v<source-version>; assoc=<association-id>; sha=<payload-hash>; fmt=<format>; mount_hash=<hash>; epoch_hash=<hash>
```

Each `<hash>` is a 22-character hash of the identity value. The mount UUID and
restore epoch are written as hashes whenever the hash is shorter, which is the
normal case. When the description still exceeds the limit, the provider hashes
the object ID (`object#<hash>`), then the source path (`path#<hash>`), and
finally switches to the short keys `a`, `h`, `f`, `m`, `mh`, `r`, and `rh`. In
the summary, a space in a value is written as `\s`; `\`, `;`, and line breaks
are escaped in all values.

The provider updates or deletes a variable only when the description starts
with `OpenBao sync:` and its association, path, object, mount, and restore
epoch match. Editing the description in GitLab removes ownership. Anyone who can
read the project's variables can read the description. See
[Ownership metadata](../concepts/ownership-and-drift.md#ownership-metadata) for
the general model.

## Drift and delete behavior

For visible variables, plan, upsert, and read-state hash the live value and
report `verification=value`. Manual value edits are detected even when the
description still holds the old hash, and the next manual sync or background
repair restores owned values. Plan and upsert also compare the attributes and
description, so the next upsert restores manual attribute edits. Read-state
compares only the value hash.

GitLab returns `value=null` for hidden variables. For an owned hidden variable,
the provider uses the description's payload hash and reports
`verification=metadata`. It cannot detect or repair an external value change
that keeps the description. To replace a hidden value, write a new source
version. A null value on an owned visible variable is an `unavailable` error.
An empty string is a real value, not a missing one.

With `delete_mode=delete`, an owned delete removes the variable in the
association's scope. A missing variable counts as deleted. Deletes take effect
immediately.

A variable whose description names another association, mount, or restore
epoch, for example from a torn-down or restored environment, reports ownership
loss and is not overwritten. Inspect or remove the variable, then run the
`manual_sync` action from status or reconcile. See
[Recover ownership loss](../operate/troubleshoot.md#recover-ownership-loss).

## Check the destination

`check` runs validation and, when the destination is enabled and valid, health.
`validate` checks configuration only. `health` reads the project. Reads redact
`token`:

```sh
bao read secret-sync/destinations/gitlab/prod
bao read secret-sync/destinations/gitlab/prod/check
bao read secret-sync/destinations/gitlab/prod/validate
bao read secret-sync/destinations/gitlab/prod/health
```

End-to-end tests: [GitLab](../../test/e2e/gitlab/README.md).
