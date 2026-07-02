# Manual AWS E2E Test

This directory contains the opt-in manual e2e test against real AWS Secrets
Manager. It is intended for sandbox validation only and must not run in default
CI.

The test starts OpenBao dev mode in Docker, registers and mounts the plugin,
configures an `aws-sm` destination with `auth_mode=assume_role`, then verifies
create, update, delete, ownership tags, queue drain, and status transitions
against real AWS Secrets Manager.

The plugin creates the test secret. OpenTofu creates only IAM and policy
fixtures.

## Environment

Use direnv for the repeated environment variables:

```sh
cd test/e2e/aws
cp .envrc.example .envrc
$EDITOR .envrc
direnv allow
```

Set `AWS_VAULT_PROFILE` in `.envrc` to an `aws-vault` profile with access to
the sandbox account. The example also exports `BAO_ADDR` and `BAO_TOKEN` for
the local dev OpenBao instance. Verify the account:

```sh
aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  aws sts get-caller-identity
```

Set `E2E_AWS_REGION` or `AWS_REGION` in `.envrc` before applying the
OpenTofu fixture. When changing regions later, re-apply the fixture so the IAM
policy is scoped to the new Secrets Manager region, then rewrite the OpenBao
destination config with the new `region` value.

## Create IAM Fixture

Initialize and apply the OpenTofu fixture:

```sh
aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  tofu -chdir=tofu init

aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  tofu -chdir=tofu apply

direnv reload
```

The fixture creates:

- an IAM role the plugin test assumes;
- a least-privilege Secrets Manager policy scoped to
  `openbao-plugin-secrets-sync-manual/*`;
- an external ID stored in OpenTofu state;
- outputs consumed by the manual e2e target.

## Manual OpenBao Flow

Use this flow when you want to start OpenBao yourself, register the plugin, and
run the `bao` commands by hand.

Build the Linux plugin binary used by the OpenBao container:

```sh
make -C ../../.. e2e-build-plugin
```

Start OpenBao from inside `aws-vault`. Plain `docker compose up -d` will fail
unless `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` are already exported in
your shell. The OpenBao container needs those base credentials because the
plugin uses the AWS SDK default chain before assuming the test role.

```sh
aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  docker compose up -d --wait
```

After direnv loads, your local `bao` CLI points at
`http://127.0.0.1:${E2E_AWS_OPENBAO_PORT}` with the dev root token.

Register and mount the plugin:

```sh
bao plugin register \
  -sha256="$(shasum -a 256 ../../../bin/e2e/openbao-plugin-secrets-sync | awk '{print $1}')" \
  -command=openbao-plugin-secrets-sync \
  -version=v0.0.0-dev \
  secret openbao-plugin-secrets-sync

bao secrets enable \
  -path=secret-sync \
  -plugin-name=openbao-plugin-secrets-sync \
  -plugin-version=v0.0.0-dev \
  plugin
```

Configure the AWS destination from the OpenTofu outputs loaded by direnv:

```sh
bao write secret-sync/destinations/aws-sm/prod \
  region="${E2E_AWS_REGION}" \
  auth_mode=assume_role \
  role_arn="${E2E_AWS_ROLE_ARN}" \
  external_id="${E2E_AWS_EXTERNAL_ID}" \
  session_name=openbao-plugin-secrets-sync-manual \
  delete_recovery_window_days=7

bao write -force secret-sync/destinations/aws-sm/prod/validate
bao read secret-sync/destinations/aws-sm/prod/health

bao write -force secret-sync/config/restore-guard/acknowledge
```

Create a syncable source secret:

```sh
bao write secret-sync/metadata/app/db \
  @<(printf '%s' '{"custom_metadata":{"syncable":"true"}}')

bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"password":"initial"}}')
```

Plan and create the association:

```sh
export E2E_AWS_REMOTE_NAME="${E2E_AWS_SECRET_PREFIX}manual-$(date +%s)"

bao write secret-sync/associations/app/db/plan \
  destination_type=aws-sm \
  destination_name=prod \
  resolved_name="${E2E_AWS_REMOTE_NAME}" \
  granularity=secret-path \
  format=json \
  delete_mode=delete

bao write secret-sync/associations/app/db \
  destination_type=aws-sm \
  destination_name=prod \
  resolved_name="${E2E_AWS_REMOTE_NAME}" \
  granularity=secret-path \
  format=json \
  delete_mode=delete
```

Drain queued work and inspect status:

```sh
bao write secret-sync/queue/drain max_operations=10
bao read secret-sync/status/app/db
bao read -format=json secret-sync/status/app/db | jq .data
```

Verify the remote secret:

```sh
aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  aws secretsmanager get-secret-value \
    --region "${E2E_AWS_REGION}" \
    --secret-id "${E2E_AWS_REMOTE_NAME}" \
    --query SecretString \
    --output text
```

Update and sync again:

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"password":"updated"}}')

bao write secret-sync/queue/drain max_operations=10
bao read secret-sync/status/app/db
```

Delete the source secret and process the owned remote delete:

```sh
bao delete secret-sync/data/app/db
bao write secret-sync/queue/drain max_operations=10
bao read secret-sync/status/app/db
```

Stop OpenBao when finished:

```sh
docker compose down -v --remove-orphans
```

## Run The Test

Run the test from inside `aws-vault` so the OpenBao container receives
temporary AWS credentials. Keep the confirmation flag explicit:

```sh
aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  make -C ../../.. E2E_AWS_CONFIRM=1 test-e2e-aws
```

The target uses `127.0.0.1:18201` for OpenBao by default. Override with
`E2E_AWS_OPENBAO_PORT` if that port is already in use.

## Cleanup

The test force-deletes its generated secret during normal cleanup. If a failed
or interrupted run leaves secrets under the test prefix, run:

```sh
aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  make -C ../../.. E2E_AWS_CLEAN_CONFIRM=1 test-e2e-aws-clean
```

Destroy the IAM fixture when finished:

```sh
aws-vault exec "${AWS_VAULT_PROFILE:?set AWS_VAULT_PROFILE}" -- \
  tofu -chdir=tofu destroy
```

## Safety Guards

- `make test-e2e-aws` requires `E2E_AWS_CONFIRM=1`.
- `make test-e2e-aws-clean` requires `E2E_AWS_CLEAN_CONFIRM=1`.
- The cleanup test refuses prefixes that do not contain `openbao-plugin-secrets-sync`.
- The committed `.envrc.example` is a template; local `.envrc` files are
  ignored.
- OpenTofu state files and local variable files are ignored in this directory.
