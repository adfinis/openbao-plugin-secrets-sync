# AWS Secrets Manager


The AWS Secrets Manager provider writes one remote AWS Secrets Manager secret
for each association object.

## Configure default auth

Use `auth_mode=default` to use the AWS SDK default credential chain:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=default \
  delete_recovery_window_days=7
```

## Configure assume-role auth

Use `auth_mode=assume_role` when OpenBao should assume a destination role:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=assume_role \
  role_arn=arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync \
  external_id=tenant-or-environment-id \
  session_name=openbao-plugin-secrets-sync
```

Static AWS access keys and session tokens are intentionally not supported yet.
Use workload identity or assume-role auth.

`delete_recovery_window_days` controls the AWS Secrets Manager scheduled-delete
recovery window used when an association with `delete_mode=delete` deletes an
owned remote secret. The default is `7`. AWS accepts values from `7` through
`30`.

## Configure custom endpoints

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

## Validate the destination

Read destination config. Sensitive fields are redacted:

```sh
bao read secret-sync/destinations/aws-sm/prod
```

Check destination readiness:

```sh
bao read secret-sync/destinations/aws-sm/prod/check
```

Use `destinations/aws-sm/prod/validate` and
`destinations/aws-sm/prod/health` when you need separate configuration and
runtime diagnostics.

## Create an association

The AWS provider currently supports `secret-path` granularity with `json`
payloads. The default association shape works for AWS Secrets Manager:

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

## Recover after delete

AWS Secrets Manager keeps deleted secrets in a scheduled-deletion state during
the configured recovery window. During that window, creating a new secret with
the same name is blocked by AWS.

If the scheduled-delete secret is still owned by the same association, the AWS
provider treats a new upsert as recovery: it calls `RestoreSecret`, writes the
current payload when needed, and refreshes ownership metadata. Plans report this
as `action=update` with a message that the secret will be restored before
upsert.

The provider does not restore or mutate scheduled-delete secrets that are not
owned by the association. Those plans report a collision, and upserts fail with
an ownership error.

## Troubleshoot endpoints

If custom endpoints fail validation:

- Use no `endpoint_url` for normal AWS endpoints.
- Use `endpoint_policy=local` only for local development endpoints.
- Use `endpoint_policy=private` only for approved HTTPS private endpoints.
- Don't put credentials or userinfo in endpoint URLs.

## Test the provider

- Use [LocalStack e2e](../../test/e2e/localstack/README.md) for self-contained
  provider testing.
- Use [manual AWS e2e](../../test/e2e/aws/README.md) for opt-in real AWS
  validation.
