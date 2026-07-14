# AWS Secrets Manager configuration

## Supported auth modes

Use `auth_mode=default` to use the AWS SDK default credential chain. `region`
is optional when the SDK default chain supplies it, but setting it explicitly
keeps destination behavior easier to inspect:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=default
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

Use `auth_mode=web_identity` when the plugin should call STS
`AssumeRoleWithWebIdentity` with a projected OIDC token file:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region=eu-central-1 \
  auth_mode=web_identity \
  role_arn=arn:aws:iam::123456789012:role/openbao-plugin-secrets-sync \
  web_identity_token_file=/var/run/secrets/eks.amazonaws.com/serviceaccount/token \
  session_name=openbao-plugin-secrets-sync
```

`web_identity_token_file` must be an absolute path readable by the OpenBao
plugin process. `external_id` is not supported with `web_identity`; use
`assume_role` when an external ID is required. Role chaining from web identity
into a second role is intentionally not part of the initial auth surface. A
caller with `destinations/*` write access controls this file path; keep that
capability limited to trusted platform operators.

Static AWS access keys and session tokens are recognized as sensitive fields
but are not supported auth material. Use workload identity, the AWS SDK default
chain, web-identity auth, or assume-role auth.

## Endpoint policy

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

Endpoint URLs must include a host and must not include userinfo, query strings,
or fragments. The provider validates custom endpoints before opening the AWS
client:

- Use no `endpoint_url` for normal AWS endpoints.
- Use `endpoint_policy=local` only for local development endpoints. Local
  endpoints may use HTTP or HTTPS, but the host must be `localhost`,
  `localstack`, a `.localhost` name, or a loopback address. At connection time,
  names must resolve only to loopback or private addresses.
- Use `endpoint_policy=private` only for approved HTTPS private endpoints.
  Private endpoints must not target local development hosts and must not
  resolve to public or special-use addresses. Literal addresses and every DNS
  answer must be an RFC 1918 IPv4 or unique-local IPv6 address.
- Do not put credentials or userinfo in endpoint URLs.

The provider resolves custom endpoint names again for every new connection and
dials an approved address directly, so DNS changes cannot bypass the endpoint
policy between validation and connection. The provider HTTP client uses a
30-second timeout and does not use ambient proxy configuration from the
OpenBao process environment.

## Value drift detection

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

## Sensitive fields

The backend stores `external_id` under the seal-wrapped destination secret
prefix and redacts it on destination reads.

`web_identity_token_file` stores only the token file path as public destination
configuration. The OIDC token contents remain on disk and are read by the AWS
SDK credential provider at runtime. The backend does not store or echo the file
contents.

Static AWS credential fields such as `access_key_id`, `secret_access_key`, and
`session_token` are not part of the destination API. Use OpenBao runtime
environment credentials with `auth_mode=default`, or use `assume_role` or
`web_identity`.

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
