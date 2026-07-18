# GitLab project variables operations

## Required permissions

Use a GitLab token that can read the target project and create, update, and
delete project CI/CD variables. The provider uses these GitLab API surfaces:

- `GET /projects/:id` for destination health;
- `GET /projects/:id/variables/:key` for plan and read-state checks;
- `POST /projects/:id/variables` for new variables;
- `PUT /projects/:id/variables/:key` for owned updates;
- `DELETE /projects/:id/variables/:key` for owned deletes.

Scope the token to the target project where possible.

## Ownership and delete behavior

The provider stores ownership metadata in the GitLab variable description. New
writes use a labelled single-line description that starts with
`OpenBao sync:`. The visible summary includes the source path, object ID, and
source version, followed by association, payload hash, format, OpenBao mount
UUID, and restore epoch metadata. Long identity values are replaced with stable
hashes when needed to stay within GitLab's description limit. The readable
mount fields are `mount` and `mount_hash`; compact descriptions use `m` and
`mh`. Owned update and delete operations require matching ownership metadata.
If ownership cannot be proven, the provider returns an ownership error instead
of mutating the variable.

Plan, upsert no-op detection, and reconcile compare the GitLab API value
readback with the desired payload hash. Manual value edits are detected even
when the variable description still contains the previous payload hash, and the
next manual sync or background `drift_repair=repair` pass repairs owned drift.

Connection and credential settings belong to the GitLab destination. Variable
attributes belong to each association. Plan an attribute change when desired,
then write it to the association:

```sh
bao write secret-sync/associations/app/db/plan \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete \
  environment_scope=production \
  protected=true \
  masked=true

bao write secret-sync/associations/app/db \
  destination=gitlab/prod \
  environment_scope=production \
  protected=true \
  masked=true

bao write secret-sync/queue/drain max_operations=10
```

Changing `protected`, `masked`, `hidden`, `variable_raw`, or `variable_type` on
an enabled association automatically enqueues the current source version. The
provider converges owned variable attributes even when the source payload has
not changed. The `hidden` flag is different: GitLab accepts hidden variables
only at creation time, so existing visible variables cannot be converted to
hidden variables by a sync.

`environment_scope` is an association identity field. A write with a new scope
creates a separate association, allowing the same variable key to be managed in
multiple scopes. Once multiple associations point to the same destination,
include `environment_scope` for writes and plans; use association-ID lifecycle
routes for disable, enable, sync, and delete when destination selection is
ambiguous.

Remote delete is sent only when the association uses `delete_mode=delete`.
Missing owned variables are treated idempotently.

If a GitLab variable exists with ownership metadata from a torn-down or restored
environment, the provider reports ownership loss instead of overwriting it.
Inspect or remove the GitLab variable first. Then run the `manual_sync` action
returned by status or reconcile so OpenBao recreates the variable from the
current source version.

## E2E test path

Use [GitLab e2e](../../../test/e2e/gitlab/README.md) to test project variables
in a Dockerized GitLab CE stack.
