# OCI Plugin E2E Tests

This directory contains the self-contained OCI plugin distribution e2e test.
It proves the OpenBao declarative OCI plugin path before a release image is
published.

The workflow builds a Linux plugin binary, packages it in the OCI plugin image
format, starts a disposable TLS registry and LocalStack, publishes the image to
the registry, starts OpenBao with `plugin_auto_download=true` and
`plugin_auto_register=true`, then runs the same AWS Secrets Manager sync
assertions used by the normal LocalStack e2e lane.

## Run

```sh
make test-e2e-oci-localstack
```

The default host ports are:

- OpenBao: `127.0.0.1:18204`
- LocalStack: `127.0.0.1:4566`
- OCI registry: `127.0.0.1:15000`

If a port is already in use, override it:

```sh
E2E_OCI_OPENBAO_PORT=18214 \
E2E_LOCALSTACK_PORT=14566 \
E2E_LOCALSTACK_ENDPOINT=http://127.0.0.1:14566 \
E2E_OCI_REGISTRY_PORT=15010 \
make test-e2e-oci-localstack
```

## Useful Targets

```sh
make e2e-oci-build-plugin
make e2e-oci-image-archive
make e2e-oci-up
make e2e-oci-down
make test-e2e-oci-localstack
```

`make test-e2e-oci-localstack` tears the stack down automatically. Use
`make e2e-oci-up` and `make e2e-oci-down` when debugging OpenBao OCI plugin
download or registry TLS behavior.
