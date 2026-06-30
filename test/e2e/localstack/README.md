# LocalStack E2E Tests

This directory contains the self-contained OpenBao plus LocalStack e2e test for
the AWS Secrets Manager provider.

The test builds the plugin as a Linux binary, starts OpenBao dev mode with that
binary in its plugin directory, registers and mounts the plugin, configures an
`aws-sm` destination that points at LocalStack, then verifies create, update,
delete, ownership tags, queue drain, and status transitions.

## Run

```sh
make test-e2e
```

The default host ports are:

- OpenBao: `127.0.0.1:18200`
- LocalStack: `127.0.0.1:4566`

If LocalStack port `4566` is already in use, override both the published port
and the endpoint used by the Go test:

```sh
E2E_LOCALSTACK_PORT=14566 \
E2E_LOCALSTACK_ENDPOINT=http://127.0.0.1:14566 \
make test-e2e
```

## Useful Targets

```sh
make e2e-build-plugin
make e2e-up
make e2e-down
make test-e2e
```

`make test-e2e` tears the stack down automatically. Use `make e2e-up` and
`make e2e-down` when debugging the running OpenBao or LocalStack services.
