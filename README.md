# OpenBao Secret Sync

`openbao-plugin-secrets-sync` is an OpenBao secret engine plugin for one-way
secret synchronization from OpenBao-managed source data to external
destinations.

The project is intentionally not a clone of Vault Enterprise Secret Sync. It is
a mount-scoped OpenBao plugin with explicit ownership checks, plan-first remote
mutation, provider capability declarations, restore safety, and implementation
diagnostics from the start.

## Status

This repository is in early implementation state. The current code builds a
logical backend plugin with KV-v2-like source storage, associations, a durable
outbox, provider-agnostic dispatch, a fake provider, and an AWS Secrets Manager
and Kubernetes Secrets provider foundation.

## Documentation

Start with [docs/README.md](docs/README.md). For hands-on use, see
[docs/user-guide.md](docs/user-guide.md). The current design package is split
into product, architecture, provider, security, operations, and
implementation-plan documents.

## Development

```sh
make test
make build
```

The self-contained e2e paths are available with:

```sh
make test-e2e
make test-e2e-kind
```

The project follows the tool and CI layout used by the OpenBao Kubernetes KMS
provider where it fits this plugin. Hugo and docs-site generation are
intentionally omitted.
