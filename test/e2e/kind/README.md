# Kind E2E Tests

This directory contains the self-contained kind e2e test for the Kubernetes
Secrets provider.

The workflow builds the Linux plugin binary, bakes it into an OpenBao dev-mode
image, creates a disposable kind cluster, grants the OpenBao service account
namespace-scoped Secret permissions, registers and mounts the plugin, then
verifies destination validation, health, in-cluster auth, token auth, create,
update, reconcile/read-state, delete semantics, ownership labels, payload
metadata, source-key data mapping, unmanaged key preservation, RBAC denial
handling, ownership loss, and immutable Secret behavior against real Kubernetes
API calls.

The OpenBao service account is intentionally granted only namespace-scoped
Secret access:

```yaml
apiGroups: [""]
resources: ["secrets"]
verbs: ["get", "list", "create", "update", "delete"]
```

For production, bind equivalent permissions only in namespaces that are
explicitly approved as sync destinations. The provider health and sync failure
tests depend on cross-namespace access being denied by default.

## Run

```sh
make test-e2e-kind
```

The default host port is:

- OpenBao: `127.0.0.1:18202`

If that port is already in use:

```sh
E2E_KIND_OPENBAO_PORT=18212 \
E2E_KIND_OPENBAO_ADDR=http://127.0.0.1:18212 \
make test-e2e-kind
```

## Useful Targets

```sh
make e2e-kind-image
make e2e-kind-up
make e2e-kind-down
make test-e2e-kind
```

`make test-e2e-kind` tears the kind cluster down automatically. Use
`make e2e-kind-up` and `make e2e-kind-down` when debugging the running
OpenBao pod.
