# OpenBao Secret Sync

`openbao-plugin-secrets-sync` is an OpenBao secret engine plugin for
association-driven, one-way synchronization from OpenBao-managed source data to
external secret destinations.

OpenBao is the source of desired state. The plugin stores source data in its own
mount, lets operators or delegated owners create explicit associations, and
then converges those associations into provider-specific objects such as AWS
Secrets Manager secrets, Kubernetes Secrets, or GitLab project variables.

## Status

This repository is in preview and is not production-ready. Use it for local
evaluation, design feedback, provider development, and controlled testing.

Before a stable release, APIs, response fields, storage records, provider
behavior, defaults, and operational guidance can change. Do not rely on preview
builds as a compatibility or migration boundary.

## Current shape

The plugin supports:

- KV-v2-like source storage in the plugin mount;
- explicit association-based sync from source paths to named destinations;
- optional delegated mode with strict source opt-in and destination constraints;
- provider-specific secret shapes, naming rules, and ownership metadata;
- safe defaults for JSON `secret-path` sync, including retained remote data on
  source delete unless overridden;
- durable queued dispatch with status, manual sync, retry, cancel, drain, and
  reconcile surfaces;
- restore and clone safeguards that prevent accidental mutation from an
  unreviewed plugin identity;
- background drift detection and repair policy controls;
- provider packages for AWS Secrets Manager, Kubernetes Secrets, and GitLab
  project variables;
- fake provider support for development and tests;
- local and self-contained e2e coverage for the main provider paths.

## Start here

- Choose the right document: [docs/README.md](docs/README.md)
- Use the plugin locally: [docs/guides/user-guide.md](docs/guides/user-guide.md)
- Configure a destination provider:
  [docs/providers/README.md](docs/providers/README.md)
- Understand source, sync, drift, and safety concepts:
  [docs/concepts/README.md](docs/concepts/README.md)
- Operate and troubleshoot it:
  [docs/operations/operator-runbook.md](docs/operations/operator-runbook.md)
- Review security posture and policy examples:
  [docs/security/README.md](docs/security/README.md)
- Add or review a provider:
  [docs/development/provider-implementation.md](docs/development/provider-implementation.md)

## Development

Common local checks:

```sh
make ci-fast
make test
make lint
make ci-core
```

Self-contained e2e paths:

```sh
make test-e2e
make test-e2e-resilience
make test-e2e-kind
E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab
```

## License and plugin policy

This repository is an external OpenBao secret engine plugin, maintained and
released independently from the OpenBao core release cycle. That release model
matches the OpenBao plugin support policy for external, community-supported
plugins:
[openbao.org/community/policies/plugins](https://openbao.org/community/policies/plugins/).

The project source is licensed under Apache-2.0. Linked dependencies keep their
own licenses; release builds run dependency license checks and publish a
`go-licenses-report.csv` artifact alongside the binary, SBOM, checksum, and
provenance artifacts.

## Contributing and security

Use conventional commits with DCO sign-off for repository changes. See
[CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidance and
[SECURITY.md](SECURITY.md) for vulnerability reporting.
