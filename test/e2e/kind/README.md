# Kind E2E Tests

This directory contains the self-contained kind e2e test for the Kubernetes
Secrets provider.

The workflow builds the Linux plugin binary, bakes it into an OpenBao dev-mode
image, creates a disposable kind cluster, grants the OpenBao service account
namespace-scoped Secret permissions, registers and mounts the plugin, then
verifies destination validation, health, create, update, reconcile/read-state,
delete semantics, ownership labels, and payload metadata against real
Kubernetes API calls.

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
