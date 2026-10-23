---
verifiedBy:
  - internal/providers/awssecretsmanager/provider.go
  - internal/backend/path_destinations.go
  - internal/backend/path_associations.go
  - test/e2e/aws/tofu/main.tofu
  - test/e2e/aws/README.md
  - test/e2e/localstack
---

# Sync to AWS Secrets Manager

The `aws-sm` provider writes one AWS Secrets Manager secret for each
association. It stores the canonical JSON payload as the secret string and
records ownership in AWS tags.

## Choose an auth mode

Set `auth_mode` on the destination. `region` is optional when the AWS SDK
default chain supplies it. The default, `auth_mode=default`, uses the AWS SDK
default credential chain of the OpenBao process.

Use `assume_role` to assume a destination role with the default-chain
identity. `role_arn` is required and must be an IAM role ARN. `external_id` and
`session_name` are optional:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=assume_role \
  role_arn=arn:aws:iam::123456789012:role/openbao-secret-sync \
  external_id=<external-id> \
  session_name=openbao-secret-sync
```

Use `web_identity` to call STS `AssumeRoleWithWebIdentity` with a projected
OIDC token file. `web_identity_token_file` must be an absolute path readable by
the plugin process. `external_id` is rejected in this mode:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=web_identity \
  role_arn=arn:aws:iam::123456789012:role/openbao-secret-sync \
  web_identity_token_file=/var/run/secrets/eks.amazonaws.com/serviceaccount/token
```

The backend seal-wraps `external_id` and redacts it on reads. It stores only the
token file path; the SDK reads the token at runtime. Static access keys are not
destination fields, and the backend rejects them as unsupported. A caller who
can write `destinations/*` chooses which file the plugin reads, so keep that
capability with trusted platform operators.

The provider's HTTP client has a 30-second timeout and ignores proxy
environment variables, so OpenBao must reach AWS STS and Secrets Manager
directly.

## Configure a custom endpoint

Omit `endpoint_url` for normal AWS endpoints. A custom `endpoint_url` requires
`endpoint_policy`, and the URL must have a host and no userinfo, query, or
fragment.

- `local`: for LocalStack and development. HTTP or HTTPS, and the host must be
  `localhost`, a `.localhost` name, `localstack`, or a loopback address.
  Connections dial only loopback or private addresses.
- `private`: for approved HTTPS private endpoints. The host must not be a local
  development host, and the literal address and every DNS answer must be an
  RFC 1918 IPv4 or unique-local IPv6 address.

```sh
bao write secret-sync/destinations/aws-sm/local \
  region=us-east-1 \
  endpoint_url=http://localstack:4566 \
  endpoint_policy=local
```

The provider resolves the endpoint again for every connection and dials an
approved address directly. The policy applies to the Secrets Manager client
only; STS calls use the default AWS endpoints.

## Grant permissions

Scope the sync actions to the approved secret name prefix. `DescribeSecret`
serves plan, ownership, and read-state checks. `CreateSecret`,
`PutSecretValue`, and `TagResource` serve upserts. `DeleteSecret` and
`RestoreSecret` serve owned deletes and scheduled-delete recovery. Add
`GetSecretValue` only when `value_drift_detection=true`.

Destination health calls `ListSecrets`, which AWS does not scope to resources.
It returns metadata, not values. Without it, health reports an authorization
failure even when sync works. Policy fragment:

```json
"Statement": [
  { "Effect": "Allow", "Action": "secretsmanager:ListSecrets", "Resource": "*" },
  { "Effect": "Allow",
    "Action": ["secretsmanager:CreateSecret", "secretsmanager:DeleteSecret",
               "secretsmanager:DescribeSecret", "secretsmanager:PutSecretValue",
               "secretsmanager:RestoreSecret", "secretsmanager:TagResource"],
    "Resource": "arn:aws:secretsmanager:<region>:<account-id>:secret:<prefix>*" }
]
```

For `assume_role`, the base identity needs `sts:AssumeRole` on `role_arn`. Add
an `sts:ExternalId` condition when the role is shared across trust boundaries.
For `web_identity`, the role trust policy must allow
`sts:AssumeRoleWithWebIdentity` for the token's issuer, audience, and subject.
Validate trust policies against real AWS; LocalStack results are not evidence
of AWS trust-policy evaluation.

## Create an association

AWS supports only the `secret-path`, `format=json`, `data_mapping=payload`
shape; see [Choose a secret shape](associations.md#choose-a-secret-shape). The
payload limit is 65,536 bytes. The default `{{ path }}` template works because
AWS secret names accept `/`. The provider does not pre-validate names, so AWS
rejects names it does not accept at dispatch.

`delete_recovery_window_days` is an association field that sets the AWS
recovery window for owned deletes. It defaults to `7` and accepts `7` through
`30`. Changing it does not enqueue a sync. Plan with the same fields first:

```sh
bao write secret-sync/associations/app/db \
  destination=aws-sm/prod \
  resolved_name=team-a/app-db \
  delete_mode=delete \
  delete_recovery_window_days=14
```

## Ownership metadata

| Tag | Value |
| --- | --- |
| `openbao-sync` | `true` |
| `openbao-sync-association` | Association ID |
| `openbao-sync-path` | Source path |
| `openbao-sync-version` | Source version |
| `openbao-sync-object` | Object ID |
| `openbao-sync-payload-sha256` | Payload hash |
| `openbao-sync-mount-uuid` | Mount UUID |
| `openbao-sync-restore-epoch` | Restore epoch |

The provider updates or deletes a secret only when `openbao-sync=true` and the
association, path, object, mount UUID, and restore epoch tags match. Tags are
plaintext metadata visible to anyone who can describe the secret. See
[Ownership metadata](../concepts/ownership-and-drift.md#ownership-metadata) for
the general model.

## Drift and delete behavior

By default, plan, upsert, and read-state compare the
`openbao-sync-payload-sha256` tag with the desired payload hash and report
`verification=metadata`. A manual value edit that leaves the tags unchanged is
not detected.

Set `value_drift_detection=true` on the destination to read the live value with
`GetSecretValue` and compare its hash. Read-state then reports
`verification=value`, and background drift detection and repair can see manual
value edits. If `GetSecretValue` is denied, these operations fail instead of
falling back to tags. See
[Enable drift detection or repair](runtime.md#enable-drift-detection-or-repair).
Destination writes keep fields you omit:

```sh
bao write secret-sync/destinations/aws-sm/prod value_drift_detection=true
```

After `DescribeSecret` proves ownership, the provider addresses value reads,
restores, updates, tags, and deletes to the described secret ARN.

With `delete_mode=delete`, an owned delete calls `DeleteSecret` with the
association's recovery window. A missing secret, or an owned secret already
scheduled for deletion, counts as deleted. Read-state reports a secret
scheduled for deletion as missing.

During the recovery window, AWS blocks creating a secret with the same name.
When the scheduled secret is owned by the association, the next upsert calls
`RestoreSecret`, writes the payload if needed, and refreshes the tags. Plan
reports `action=update` with a message that the secret will be restored first.
When the scheduled secret is not owned, plan reports a collision and upsert
fails with an ownership error.

After you resolve an ownership or scheduled-delete conflict, run the
`manual_sync` action from status or reconcile. See
[Recover ownership loss](../operate/troubleshoot.md#recover-ownership-loss).

## Check the destination

`check` runs validation and, when the destination is enabled and valid, health.
`validate` checks configuration only. `health` calls `ListSecrets`. Reads
redact `external_id`:

```sh
bao read secret-sync/destinations/aws-sm/prod
bao read secret-sync/destinations/aws-sm/prod/check
bao read secret-sync/destinations/aws-sm/prod/validate
bao read secret-sync/destinations/aws-sm/prod/health
```

End-to-end tests: [LocalStack](../../test/e2e/localstack/README.md) and
[manual AWS](../../test/e2e/aws/README.md).
