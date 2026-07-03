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
source version, followed by association, payload hash, format, plugin instance,
and restore epoch metadata. Long identity values are replaced with stable
hashes when needed to stay within GitLab's description limit. Owned update and
delete operations require matching ownership metadata. If ownership cannot be
proven, the provider returns an ownership error instead of mutating the
variable.

Plan, upsert no-op detection, and reconcile compare the GitLab API value
readback with the desired payload hash. Manual value edits are detected even
when the variable description still contains the previous payload hash, and the
next manual sync or background `drift_repair=repair` pass repairs owned drift.

Changing a GitLab destination updates stored config and validates the merged
provider settings, but it does not enqueue sync work for existing associations.
If a change to `protected`, `masked`, `variable_raw`, or `variable_type` needs
to be reflected in existing GitLab variables, plan the association and then
trigger a manual sync:

```sh
bao write secret-sync/associations/app/db/plan \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete

bao write secret-sync/associations/app/db/sync destination=gitlab/prod
bao write secret-sync/queue/drain max_operations=10
```

For variables owned by this plugin, the provider repairs these attribute
changes even when the source payload has not changed. The `hidden` flag is
different: GitLab accepts hidden variables only at creation time, so existing
visible variables cannot be converted to hidden variables by a sync.

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
