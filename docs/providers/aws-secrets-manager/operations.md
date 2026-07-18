# AWS Secrets Manager operations

## Required permissions

Grant the destination identity permission to manage only the approved Secrets
Manager name prefix. The provider uses these AWS APIs:

- `secretsmanager:ListSecrets` for health checks;
- `secretsmanager:DescribeSecret` for plan, ownership, and read-state checks;
- `secretsmanager:CreateSecret` for new managed secrets;
- `secretsmanager:PutSecretValue` for owned updates;
- `secretsmanager:DeleteSecret` for owned deletes;
- `secretsmanager:RestoreSecret` for owned scheduled-delete recovery;
- `secretsmanager:TagResource` for ownership metadata.

AWS does not support resource-level permissions for `ListSecrets`, so the
health-check statement must use `Resource: "*"`. The API returns secret
metadata, not secret values. If this account-wide metadata permission is not
acceptable, destination health reports an authorization failure even when the
more narrowly scoped sync APIs are permitted.

When `value_drift_detection=true`, also grant
`secretsmanager:GetSecretValue`. If that permission is missing, explicit plan,
upsert, and read-state operations that need value readback fail visibly instead
of falling back to metadata-only checks.

When using `auth_mode=assume_role`, the base AWS identity must also be allowed
to call `sts:AssumeRole` on the configured `role_arn`. Use an `external_id`
condition when the destination role is shared across trust boundaries.

When using `auth_mode=web_identity`, the configured role trust policy must allow
`sts:AssumeRoleWithWebIdentity` for the OIDC issuer, audience, and subject that
produces the mounted token file. Validate this against real AWS; LocalStack can
exercise Secrets Manager behavior, but it is not production evidence for AWS
OIDC trust-policy evaluation.

The manual AWS e2e fixture also grants `GetSecretValue` and `UntagResource` for
test verification and cleanup. The provider uses `GetSecretValue` for normal
sync decisions only when `value_drift_detection=true`.

## Ownership and delete behavior

The provider writes ownership tags that include the association ID, source
path, source version, object ID, payload hash, OpenBao mount UUID, and restore
epoch. The mount tag is `openbao-sync-mount-uuid`. Owned update and delete
operations require matching ownership metadata. If ownership cannot be proven,
the provider returns an ownership error instead of mutating the remote secret.

Plan, upsert no-op detection, and reconcile compare AWS tag metadata by
default. With `value_drift_detection=true`, explicit plan, upsert, and
read-state operations read owned secret values and compare the live value hash
with the desired payload hash. Manual value-only changes with unchanged
ownership tags are detected only in that opt-in mode.

Background `drift_repair=detect|repair` uses the same read-state behavior. To
detect and automatically repair manual AWS value edits, configure the
destination with `value_drift_detection=true`; otherwise background drift work
can only reason from ownership tags and payload-hash metadata.

`delete_recovery_window_days` is association configuration. It controls the AWS
Secrets Manager scheduled-delete recovery window used when that association has
`delete_mode=delete` and deletes an owned remote secret. The default is `7`.
AWS accepts values from `7` through `30`. Changing only this operational policy
does not enqueue a sync; it applies to the association's next delete.

```sh
bao write secret-sync/associations/app/db \
  destination=aws-sm/prod \
  resolved_name=prod/app/db \
  delete_mode=delete \
  delete_recovery_window_days=14
```

After `DescribeSecret` proves ownership, the provider binds value readback,
restore, update, tag, and delete calls to the described secret ARN. Creation
still uses the resolved secret name.

AWS Secrets Manager keeps deleted secrets in a scheduled-deletion state during
the configured recovery window. During that window, creating a new secret with
the same name is blocked by AWS. If the scheduled-delete secret is still owned
by the same association, the provider treats a new upsert as recovery: it calls
`RestoreSecret`, writes the current payload when needed, and refreshes
ownership metadata. Plans report this as `action=update` with a message that
the secret will be restored before upsert.

The provider does not restore or mutate scheduled-delete secrets that are not
owned by the association. Those plans report a collision, and upserts fail with
an ownership error.

Deletes are idempotent for already-missing remote secrets. If an owned secret is
already scheduled for deletion, delete returns success without calling
`DeleteSecret` again.

After an operator resolves remote ownership or scheduled-delete state, use the
`manual_sync` action returned by status or reconcile to enqueue the current
OpenBao source version.

## E2E test path

- Use [LocalStack e2e](../../../test/e2e/localstack/README.md) for
  self-contained provider testing.
- Use [manual AWS e2e](../../../test/e2e/aws/README.md) for opt-in real AWS
  validation.
