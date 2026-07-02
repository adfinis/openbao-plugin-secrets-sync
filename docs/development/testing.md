# Testing and maintainer evidence

Use this page to choose the evidence needed for a change. Keep the evidence
close to the risk: small docs and local code changes need the fast gate;
provider, queue, security, release, and storage changes need the matching
specialized lanes.

## Local gates

Run the fast gate while developing:

```sh
make ci-fast
```

`ci-fast` runs tidy checks, docs checks, version checks, formatting checks,
`go vet`, unit tests, and a build.

Run the core local gate before opening or merging a non-trivial change:

```sh
make ci-core
```

`ci-core` runs tidy checks, lint, security checks, unit tests, race tests, fuzz
smoke targets, a build, and release artifact generation.

Use `git diff --check` before committing documentation or generated text.

## Test lanes

| Lane | Commands | Evidence |
| --- | --- | --- |
| Unit and contract tests | `go test ./...` | Package-local behavior, backend path contracts, provider conformance, payload construction, redaction, and error classification. |
| Race tests | `go test -race ./...` or `make test-race` | Concurrent source writes, association updates, queue claims, runtime cache use, and dispatcher behavior are free of detected data races. |
| Fuzz tests | `make fuzz` | Payload canonicalization and destination name-template rendering tolerate malformed or unusual inputs. |
| Security checks | `make security-ci` | Dependency vulnerabilities, license policy, and filesystem static-analysis rules pass. |
| Release artifacts | `make release-artifacts` | Linux plugin binaries, SBOMs, and checksums build from the current tree. |
| Self-contained e2e | `make test-e2e`, `make test-e2e-kind`, `make test-e2e-resilience` | OpenBao plugin registration, mount behavior, provider API behavior, queue drain, status transitions, restore/HA behavior, and real Kubernetes API behavior. |
| Opt-in provider e2e | `E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab`, `E2E_AWS_CONFIRM=1 make test-e2e-aws` | Dockerized GitLab API behavior or real AWS IAM and Secrets Manager behavior. |

## Provider-specific e2e

Run provider e2e tests when a change touches the provider, its destination
config, payload shape, ownership metadata, error classification, or fixture.

| Provider or area | Command | Run when |
| --- | --- | --- |
| AWS Secrets Manager with LocalStack | `make test-e2e` | AWS provider behavior, ownership tags, LocalStack fixture, queue/status behavior for AWS. |
| Real AWS Secrets Manager | `E2E_AWS_CONFIRM=1 make test-e2e-aws` | IAM, STS assume-role, AWS API behavior, or delete recovery behavior needs real AWS evidence. |
| Kubernetes Secrets | `make test-e2e-kind` | Kubernetes auth, RBAC, Secret metadata, data mapping, read-state, owned delete, or immutable Secret behavior changes. |
| GitLab project variables | `E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab` | GitLab variable attributes, masked/hidden variables, ownership descriptions, read-state, or GitLab HTTP behavior changes. |
| OpenBao lifecycle resilience | `make test-e2e-resilience` | Queue durability, status persistence, restore guard, HA failover, seal recovery, or replication-safety behavior changes. |
| OCI plugin distribution | `make test-e2e-oci-localstack` | OCI plugin image layout, OpenBao plugin download, auto-registration, or release OCI behavior changes. |

Manual real-provider tests stay opt-in and sandbox-scoped. They must not be
required for normal CI.

## Evidence by change type

### Provider changes

Provider changes need:

- provider unit tests for config validation, request shape, provider API edge
  cases, and error classification;
- shared `internal/providers/providertest` conformance coverage;
- ownership loss, stale remote state, payload-size, authn, authz,
  rate-limit, capacity, and delete semantics tests where the provider supports
  those behaviors;
- the matching provider guide and capability matrix updates;
- the matching provider e2e lane when mocks cannot prove the behavior.

### Queue and dispatcher changes

Queue, dispatcher, periodic work, and enqueue-intent changes need:

- unit tests for operation state transitions, due indexes, path indexes,
  claim expiry, retry scheduling, cancellation, and pruning;
- race tests for concurrent source writes, association writes, runtime cache
  use, and dispatch paths;
- model-style tests that assert invariants across several operations;
- lifecycle resilience e2e when persistence, restart, HA, restore guard, or
  replication-safety behavior changes.

### Security and authorization changes

Security-sensitive changes need:

- tests proving source payload values do not appear in plan, queue, status,
  reconcile, logs, metrics, or errors;
- tests proving sensitive destination config is stored separately and redacted
  on reads;
- destination policy tests for source path and resolved-name prefix checks;
- provider tests for custom endpoint validation, bounded HTTP behavior, and
  authn/authz error mapping;
- `make security-ci`;
- updates to the security model, policy examples, provider docs, or runbook
  when operator behavior changes.

### API and storage changes

API and storage changes need:

- path tests that lock request fields, response fields, defaults, and error
  classes;
- pagination tests for public `LIST` endpoints when listing behavior changes;
- storage compatibility tests when schema handling changes;
- OpenAPI inspection artifact updates when path shape or field names change;
- updates to API surface and compatibility docs when user-visible behavior
  changes.

### Release and artifact changes

Release workflow, artifact, provenance, and OCI changes need:

- `make ci-core`;
- release artifact generation;
- OCI e2e when plugin image layout or OpenBao OCI download behavior changes;
- release engineering and install/verify documentation updates when operator
  verification steps change.

## Merge evidence

Before a non-trivial PR is ready:

- run `make ci-core`;
- run the provider or lifecycle e2e lane that matches the changed behavior;
- record any opt-in manual provider evidence in the PR;
- update docs at the same ownership level as the changed behavior;
- verify generated or inspection artifacts when API, release, or docs tooling
  changes.
