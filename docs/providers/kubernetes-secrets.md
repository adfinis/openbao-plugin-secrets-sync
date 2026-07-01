# Kubernetes Secrets


The Kubernetes provider writes one `Opaque` Secret per `secret-path`
association. The canonical payload is stored in the Secret `data.payload` key.
Ownership metadata is stored in labels and annotations.

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

## Name Kubernetes Secret resources

The `resolved_name` must be a valid Kubernetes Secret name. Use a DNS-safe name
such as `app-db` instead of `app/db`.

## Create an association

The Kubernetes provider currently supports `secret-path` granularity with
`json` payloads. Set `resolved_name` because the default `{{ path }}` template
can contain `/`, which isn't valid in Kubernetes Secret names:

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

## Validate the destination

Check destination readiness:

```sh
bao read secret-sync/destinations/k8s/apps/check
```

## Test the provider

Use [kind e2e](../../test/e2e/kind/README.md) to test Kubernetes Secrets in a
disposable kind cluster.
