# Restart Resilience E2E Tests

This directory contains a self-contained OpenBao plus LocalStack e2e test for
restart behavior with persistent OpenBao storage. It uses explicit OpenBao
configuration in `config/node0.hcl`, file storage, and a static seal key
generated under `dist/e2e/resilience`.

The test initializes OpenBao once, registers and mounts the plugin, pauses
dispatch, enqueues AWS Secrets Manager sync work, restarts OpenBao, verifies
the queue survived the restart, resumes dispatch, and verifies the remote
secret and status state.

Run it with:

```sh
make test-e2e-resilience
```

Default ports:

- OpenBao: `127.0.0.1:18205`
- LocalStack: `127.0.0.1:4567`

Override ports when needed:

```sh
E2E_RESILIENCE_OPENBAO_PORT=19205 \
E2E_RESILIENCE_LOCALSTACK_PORT=14567 \
make test-e2e-resilience
```

Useful manual workflow:

```sh
make e2e-resilience-up
make e2e-resilience-down
make test-e2e-resilience
```

`make test-e2e-resilience` tears the stack down automatically. Use
`make e2e-resilience-up` and `make e2e-resilience-down` when debugging the
running OpenBao or LocalStack services.
