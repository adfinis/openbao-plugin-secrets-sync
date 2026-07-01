# OpenBao Secret Sync

`openbao-plugin-secrets-sync` is an early-stage OpenBao secret engine plugin
for one-way synchronization from OpenBao-managed source data to external
destinations.

It is intentionally a mount-scoped OpenBao plugin, not a clone of Vault
Enterprise Secret Sync and not a core `/sys/sync` feature.

## Status

This repository is under active implementation. APIs, storage records, provider
behavior, and operational guidance may still change before a stable release.
Do not treat the current state as production-ready.

Current implementation work includes:

- KV-v2-like source storage in the plugin mount;
- explicit source opt-in and association-based sync;
- safe association defaults for JSON `secret-path` sync with retained remote
  data on source delete unless overridden;
- durable queued dispatch with status and reconcile surfaces;
- provider packages for fake, AWS Secrets Manager, Kubernetes Secrets, and
  GitLab project variables;
- local and self-contained e2e coverage for the main provider paths.

## Start Here

- Use the plugin locally: [docs/user-guide.md](docs/user-guide.md)
- Operate and troubleshoot it: [docs/operator-runbook.md](docs/operator-runbook.md)
- Understand the design: [docs/README.md](docs/README.md)
- Add or review a provider: [docs/provider-implementation.md](docs/provider-implementation.md)

## Development

Common local checks:

```sh
make test
make lint
make ci-core
```

Self-contained e2e paths:

```sh
make test-e2e
make test-e2e-kind
E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab
```
