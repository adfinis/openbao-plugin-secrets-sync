# OpenBao Lifecycle Resilience E2E Tests

This directory contains a self-contained OpenBao plus LocalStack e2e test for
OpenBao lifecycle behavior with persistent OpenBao storage. It uses explicit
OpenBao configuration in `config/node0.hcl`, `config/node1.hcl`, and
`config/node2.hcl`, three-node Raft storage, and a static seal key generated
under `dist/e2e/resilience`.

The test initializes OpenBao once, registers and mounts the plugin, pauses
dispatch, enqueues AWS Secrets Manager sync work, stops the primary OpenBao
node, verifies the plugin state through a remaining HA node, starts the primary
node again, resumes dispatch, and verifies the remote secret and status state.
It then restarts OpenBao nodes, queues an update, seals OpenBao through the
system API, restarts into static-seal self-unseal, and verifies the plugin
mount can drain the queued update.

Run it with:

```sh
make test-e2e-resilience
```

Default ports:

- OpenBao node 0: `127.0.0.1:18205`
- OpenBao node 1: `127.0.0.1:18206`
- OpenBao node 2: `127.0.0.1:18207`
- LocalStack: `127.0.0.1:4567`

Override ports when needed:

```sh
E2E_RESILIENCE_OPENBAO_PORT=19205 \
E2E_RESILIENCE_OPENBAO_STANDBY_PORT=19206 \
E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT=19207 \
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
