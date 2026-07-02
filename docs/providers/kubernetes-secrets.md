# Kubernetes Secrets


The Kubernetes provider writes one `Opaque` Secret per `secret-path`
association. By default, the canonical payload is stored in the Secret
`data.payload` key. With `data_mapping=source-keys`, top-level source keys are
stored as Kubernetes Secret `.data` entries. Ownership metadata is stored in
labels and annotations.

## Configure in-cluster auth

Use in-cluster auth when OpenBao runs in the target Kubernetes cluster:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=in_cluster
```

## Configure kubeconfig auth

Use kubeconfig auth for local development or external cluster access:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=kubeconfig \
  kubeconfig_path="$HOME/.kube/config" \
  context=kind-openbao
```

## Configure token auth

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

The `token` value is stored seal-wrapped and redacted on read. `ca_cert_pem` is
optional when the API server certificate chains to the runtime trust store.
`tls_server_name` can be set when the API endpoint name and certificate name
differ.

## Name Kubernetes Secret resources

The `resolved_name` must be a valid Kubernetes Secret name. Use a DNS-safe name
such as `app-db` instead of `app/db`.

## Create an association

The Kubernetes provider supports `secret-path` granularity with `json`
payloads. Set `resolved_name` because the default `{{ path }}` template can
contain `/`, which isn't valid in Kubernetes Secret names:

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

## Map source keys to Secret data keys

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
Managed data keys are replaced on update; unrelated existing keys are
preserved, but a desired managed key will not overwrite an unmanaged key.

## Validate the destination

Check destination readiness:

```sh
bao read secret-sync/destinations/k8s/apps/check
```

## Test the provider

Use [kind e2e](../../test/e2e/kind/README.md) to test Kubernetes Secrets in a
disposable kind cluster.
