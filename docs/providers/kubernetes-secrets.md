# Kubernetes Secrets

## What it writes

The Kubernetes provider writes one Kubernetes `Opaque` Secret for each
`secret-path` association object. By default, the canonical payload is stored
in the Secret `data.payload` key. With `data_mapping=source-keys`, top-level
source keys are stored as Kubernetes Secret `.data` entries.

The provider stores ownership metadata in labels and annotations.

## Supported auth modes

Use in-cluster auth when OpenBao runs in the target Kubernetes cluster:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=in_cluster
```

Use kubeconfig auth for local development or external cluster access:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=kubeconfig \
  kubeconfig_path="$HOME/.kube/config" \
  context=kind-openbao
```

Use token auth when OpenBao reaches a Kubernetes API server directly with a
bearer token:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=token \
  api_server=https://kubernetes.example.com \
  ca_cert_pem=@cluster-ca.pem \
  token="$KUBERNETES_BEARER_TOKEN"
```

`ca_cert_pem` is optional when the API server certificate chains to the runtime
trust store. Set `tls_server_name` when the API endpoint name and certificate
name differ.

## Supported association shapes

The examples assume the source path already has a current local version. Fresh
mounts default `require_source_opt_in=false`; if strict source opt-in is
enabled, mark the source with `sources/<path>/enable` first.

The Kubernetes provider supports `secret-path` granularity with `format=json`.
Set `resolved_name` because the default `{{ path }}` template can contain `/`,
which is not valid in Kubernetes Secret names:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=k8s \
  destination_name=apps \
  resolved_name=app-db

bao write secret-sync/associations/app/db \
  destination_type=k8s \
  destination_name=apps \
  resolved_name=app-db
```

The `resolved_name` must be a valid Kubernetes Secret name. Use a DNS-safe name
such as `app-db` instead of `app/db`.

Use `data_mapping=source-keys` when consumers expect individual Kubernetes
Secret `.data` entries instead of one `payload` entry:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=k8s \
  destination_name=apps \
  resolved_name=app-db \
  data_mapping=source-keys \
  data_key_template='{{ key }}'

bao write secret-sync/associations/app/db \
  destination_type=k8s \
  destination_name=apps \
  resolved_name=app-db \
  data_mapping=source-keys \
  data_key_template='{{ key }}'
```

Source-key data mapping keeps one Kubernetes Secret object per association.
Only string and bytes source values are accepted. Rendered data keys must be
valid Kubernetes Secret keys: alphanumeric characters, `-`, `_`, or `.`.
Managed data keys are replaced on update. Unrelated existing keys are
preserved, but a desired managed key will not overwrite an unmanaged key.

The Kubernetes provider does not support `secret-key` granularity or
`format=raw`.

## Required permissions

Grant the destination identity namespace-scoped access to Kubernetes Secrets in
only the namespaces that are approved as sync destinations:

```yaml
apiGroups: [""]
resources: ["secrets"]
verbs: ["get", "list", "create", "update", "delete"]
```

The provider uses `list` for health checks, `get` for plan and read-state
checks, `create` and `update` for upserts, and `delete` for owned deletes.

## Sensitive fields

The backend stores `token` under the seal-wrapped destination secret prefix and
redacts it on destination reads.

`ca_cert_pem` is certificate material and is not a bearer credential.
`kubeconfig_path` points to a file that must be readable by the OpenBao plugin
process when the provider opens the destination.

## Ownership and delete behavior

The provider writes an ownership label and annotations that include the
association ID, source path, source version, object ID, payload hash, payload
format, data keys, plugin instance, and restore epoch. Owned update and delete
operations require matching ownership metadata. If ownership cannot be proven,
the provider returns an ownership error instead of mutating the Secret.

Plan, upsert no-op detection, and reconcile compute the payload hash from live
Kubernetes Secret data. Manual data edits are detected even when the ownership
annotations still contain the previous payload hash, and the next explicit sync
repairs owned drift.

With `data_mapping=source-keys`, the provider manages only the rendered data
keys recorded in ownership metadata and preserves unrelated data keys where
possible. A desired managed key does not overwrite an unmanaged existing key.

Remote delete is sent only when the association uses `delete_mode=delete`.
Missing owned Secrets are treated idempotently.

## Validation and check commands

Read destination config. Sensitive fields are redacted:

```sh
bao read secret-sync/destinations/k8s/apps
```

Check destination readiness:

```sh
bao read secret-sync/destinations/k8s/apps/check
```

Use `validate` and `health` when you need separate configuration and runtime
diagnostics:

```sh
bao read secret-sync/destinations/k8s/apps/validate
bao read secret-sync/destinations/k8s/apps/health
```

## E2E test path

Use [kind e2e](../../test/e2e/kind/README.md) to test Kubernetes Secrets in a
disposable kind cluster.
