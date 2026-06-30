# OpenBao Secret Sync

`openbao-plugin-secrets-sync` is an OpenBao secret engine plugin for one-way
secret synchronization from OpenBao-managed source data to external
destinations.

The project is intentionally not a clone of Vault Enterprise Secret Sync. It is
a mount-scoped OpenBao plugin with explicit ownership checks, plan-first remote
mutation, provider capability declarations, restore safety, and implementation
diagnostics from the start.

## Status

This repository is in early scaffold state. The current code builds a logical
backend plugin with initial path surfaces and repository quality gates. The KV,
outbox, provider, reconciliation, and destination implementations are still
planned work.

## Documentation

Start with [docs/README.md](docs/README.md). The current design package is
split into product, architecture, provider, security, operations, and
implementation-plan documents.

## Development

```sh
make test
make build
```

The project follows the tool and CI layout used by the OpenBao Kubernetes KMS
provider where it fits this plugin. Hugo and docs-site generation are
intentionally omitted.
