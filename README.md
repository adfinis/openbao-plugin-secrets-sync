# OpenBao Secret Sync

`openbao-plugin-secrets-sync` is an early-stage OpenBao secret engine plugin
for one-way synchronization from OpenBao-managed source data to external
destinations.

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

## Start here

- Use the plugin locally: [docs/guides/user-guide.md](docs/guides/user-guide.md)
- Configure a destination provider: [docs/providers/README.md](docs/providers/README.md)
- Operate and troubleshoot it: [docs/operations/operator-runbook.md](docs/operations/operator-runbook.md)
- Understand the design: [docs/README.md](docs/README.md)
- Add or review a provider: [docs/development/provider-implementation.md](docs/development/provider-implementation.md)

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
