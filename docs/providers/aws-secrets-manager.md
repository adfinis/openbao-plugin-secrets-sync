# AWS Secrets Manager

## What it writes

The AWS Secrets Manager provider writes one AWS Secrets Manager secret for each
association object. The provider stores the canonical Secret Sync payload as
the secret value and stores ownership metadata as AWS tags.

The provider can use default AWS endpoints, LocalStack-style local endpoints,
or explicitly approved HTTPS private endpoints.

## Supported auth modes

Use `auth_mode=default` to use the AWS SDK default credential chain:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=default \
  delete_recovery_window_days=7
```

Use `auth_mode=assume_role` when the plugin must assume a destination role:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=assume_role \
  role_arn=arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync \
  external_id=tenant-or-environment-id \
  session_name=openbao-plugin-secrets-sync
```

Static AWS access keys and session tokens are recognized as sensitive fields
but are not supported auth material. Use workload identity, the AWS SDK default
chain, or assume-role auth.

Custom endpoints require an explicit endpoint policy. Use `local` for
LocalStack and other local development endpoints:

```sh
bao write secret-sync/destinations/aws-sm/local \
  region=us-east-1 \
  auth_mode=default \
  endpoint_url=http://localstack:4566 \
  endpoint_policy=local
```

Use `private` only for explicitly approved HTTPS private endpoint deployments:

```sh
bao write secret-sync/destinations/aws-sm/private \
  region=eu-central-1 \
  auth_mode=assume_role \
  role_arn=arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync \
  external_id=tenant-or-environment-id \
  endpoint_url=https://vpce-1234567890abcdef.secretsmanager.eu-central-1.vpce.amazonaws.com \
  endpoint_policy=private
```

By default, explicit plan, upsert, and read-state checks use AWS tag metadata
for payload drift decisions. Set `value_drift_detection=true` when the
destination identity may read secret values and you want those operations to
compare the live AWS secret value with the desired OpenBao payload hash:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=default \
  value_drift_detection=true
```

## Supported association shapes

The examples assume the source path already has a current local version. Fresh
mounts default `require_source_opt_in=false`; if strict source opt-in is
enabled, mark the source with `sources/<path>/enable` first.

The AWS provider supports `secret-path` granularity with `format=json`. The
default association shape works for AWS Secrets Manager:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=aws-sm \
  destination_name=prod

bao write secret-sync/associations/app/db \
  destination_type=aws-sm \
  destination_name=prod
```

Use `resolved_name` or `name_template` when the remote secret name must differ
from the OpenBao source path:

```sh
bao write secret-sync/associations/app/db \
  destination_type=aws-sm \
  destination_name=prod \
  resolved_name=openbao-plugin-secrets-sync/team-a/app-db
```

The AWS provider does not support `secret-key` granularity, `format=raw`, or
destination-native data maps.

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

When `value_drift_detection=true`, also grant
`secretsmanager:GetSecretValue`. If that permission is missing, explicit plan,
upsert, and read-state operations that need value readback fail visibly instead
of falling back to metadata-only checks.

When using `auth_mode=assume_role`, the base AWS identity must also be allowed
to call `sts:AssumeRole` on the configured `role_arn`. Use an `external_id`
condition when the destination role is shared across trust boundaries.

The manual AWS e2e fixture also grants `GetSecretValue` and `UntagResource` for
test verification and cleanup. The provider uses `GetSecretValue` for normal
sync decisions only when `value_drift_detection=true`.

## Sensitive fields

The backend stores `external_id`, `access_key_id`, `secret_access_key`, and
`session_token` under the seal-wrapped destination secret prefix and redacts
them on destination reads.

`access_key_id`, `secret_access_key`, and `session_token` are rejected as auth
material because static AWS auth is not supported.

## Ownership and delete behavior

The provider writes ownership tags that include the association ID, source
path, source version, object ID, payload hash, plugin instance, and restore
epoch. Owned update and delete operations require matching ownership metadata.
If ownership cannot be proven, the provider returns an ownership error instead
of mutating the remote secret.

Plan, upsert no-op detection, and reconcile compare AWS tag metadata by
default. With `value_drift_detection=true`, explicit plan, upsert, and
read-state operations read owned secret values and compare the live value hash
with the desired payload hash. Manual value-only changes with unchanged
ownership tags are detected only in that opt-in mode.

`delete_recovery_window_days` controls the AWS Secrets Manager scheduled-delete
recovery window used when an association with `delete_mode=delete` deletes an
owned remote secret. The default is `7`. AWS accepts values from `7` through
`30`.

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

## Validation and check commands

Read destination config. Sensitive fields are redacted:

```sh
bao read secret-sync/destinations/aws-sm/prod
```

Check destination readiness:

```sh
bao read secret-sync/destinations/aws-sm/prod/check
```

Use `validate` and `health` when you need separate configuration and runtime
diagnostics:

```sh
bao read secret-sync/destinations/aws-sm/prod/validate
bao read secret-sync/destinations/aws-sm/prod/health
```

If custom endpoints fail validation:

- Use no `endpoint_url` for normal AWS endpoints.
- Use `endpoint_policy=local` only for local development endpoints.
- Use `endpoint_policy=private` only for approved HTTPS private endpoints.
- Do not put credentials or userinfo in endpoint URLs.

## E2E test path

- Use [LocalStack e2e](../../test/e2e/localstack/README.md) for self-contained
  provider testing.
- Use [manual AWS e2e](../../test/e2e/aws/README.md) for opt-in real AWS
  validation.
