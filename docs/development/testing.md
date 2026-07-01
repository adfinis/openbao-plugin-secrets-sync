# Testing And Hardening


This document defines the hardening test lanes for the secret sync plugin. The
goal is to keep evidence clear: unit tests prove local contracts, model tests
prove state-machine invariants, fuzz tests mutate narrow input boundaries, e2e
tests prove OpenBao plugin behavior, and security checks cover dependency and
static-analysis risk.

## Local Gates

Use these gates while implementing:

```sh
go test ./...
go test -race ./...
make fuzz
make lint
git diff --check
```

Use provider e2e gates only when the relevant runtime is available:

```sh
make test-e2e
make test-e2e-kind
E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab
```

Manual real-provider gates stay opt-in:

```sh
E2E_AWS_CONFIRM=1 make test-e2e-aws
```

## Test Lanes

### Unit And Contract Tests

Unit tests cover package-local behavior and stable API contracts. They should
stay fast, deterministic, and free of external services.

Provider contract tests live behind the shared `providertest` harness. Every
provider should cover:

- capability flags;
- config validation and sensitive-field redaction;
- plan/create/update/delete/read-state lifecycle;
- authn, authz, rate-limit, validation, capacity, collision, drift, and
  ownership error classes.

Registered production providers must also pass the shared provider maturity
matrix:

- ownership loss rejects update/delete instead of overwriting or removing a
  foreign object;
- authentication failures map to `authn`;
- throttling or rate limits map to `rate_limit`;
- payload-size failures map to `capacity` before or at the provider boundary;
- partial-success behavior is explicit: atomic providers document the atomic
  mutation, while multi-step providers classify post-write failures and return
  no successful `SyncResult`;
- stale remote state with a newer managed source version maps to `drift`;
- delete semantics cover owned delete and idempotent missing-remote delete.

### Model Tests

Model tests exercise state transitions across several operations and assert
invariants after every transition. They are not random fuzz tests; each action
sequence should be small enough to debug from the failure name.

Core model invariants:

- remote mutation intent requires an eligible source and an association;
- queued upserts reference an available source version;
- queued deletes reference a deleted or unavailable source version;
- disabling an association cancels queued work and records disabled status;
- deleting a source replaces stale queued upserts with allowed delete work;
- status, plan, and queue responses never include secret values.

### Fuzz Tests

Fuzz tests mutate narrow input boundaries where parser or canonicalization
mistakes are likely. Current smoke targets cover:

- raw payload canonicalization;
- JSON payload determinism and digest correctness;
- destination name-template rendering.

Run them with:

```sh
make fuzz
```

Override `FUZZTIME` for longer local sweeps:

```sh
FUZZTIME=60s make fuzz
```

### E2E Tests

Self-contained e2e tests prove the OpenBao plugin boundary in dev mode,
including plugin registration, mount, destination configuration, queue drain,
provider API behavior, and status transitions.

Current self-contained e2e coverage:

- LocalStack-backed AWS Secrets Manager;
- kind-backed Kubernetes Secrets;
- Dockerized GitLab CE project variables;
- OCI plugin distribution through a disposable TLS registry, OpenBao
  declarative plugin download, OpenBao declarative plugin registration, and the
  LocalStack-backed AWS Secrets Manager flow.

Manual real-provider e2e tests prove cloud-specific IAM and API behavior, but
must stay explicit and sandbox-scoped.

Documented provider validation paths:

- AWS Secrets Manager: LocalStack self-contained path in
  `test/e2e/localstack/README.md` and opt-in real AWS path in
  `test/e2e/aws/README.md`;
- Kubernetes Secrets: kind-backed real API-server path in
  `test/e2e/kind/README.md`;
- GitLab project variables: Dockerized GitLab CE path in
  `test/e2e/gitlab/README.md`. A real GitLab project fixture remains opt-in
  future work.
- OCI plugin distribution: disposable TLS registry path in
  `test/e2e/oci/README.md`.

### Security Checks

Security checks cover dependency vulnerabilities, licenses, and filesystem
static analysis:

```sh
make security-ci
```

Runtime security assertions belong in unit/model/e2e tests when they depend on
plugin behavior. Examples:

- no secret values in status, plan, logs, or metrics;
- sensitive destination fields stored separately and redacted on read;
- destination policy constraints reject disallowed source path prefixes and
  resolved remote-name prefixes before provider mutation;
- custom endpoints require explicit policy;
- AWS private custom endpoints reject unsafe DNS answers at client creation
  time;
- AWS and GitLab provider-owned HTTP clients use bounded timeouts and do not
  inherit ambient proxy configuration;
- non-local HTTP destinations require explicit insecure opt-in;
- GitLab provider HTTP client tests cover timeout configuration, redirect
  refusal, and bounded response reads;
- provider errors map to stable classes without leaking secret values.

Current backend security-boundary coverage asserts that:

- AWS and GitLab sensitive destination fields are stored separately from public
  destination metadata and redacted on read;
- source payload canaries do not appear in association plan/create responses,
  queue summaries, queue operation reads, drain responses, status responses,
  or reconcile plan/apply responses.

Current destination policy coverage asserts that:

- destination prefix constraints are normalized and visible on read;
- association create and plan reject disallowed source paths and resolved
  remote names;
- queued dispatch rechecks destination policy and blocks remote mutation if a
  destination policy is tightened after enqueue.

Current queue hardening coverage asserts that:

- unexpired outbox claims are skipped by manual drain and block operator cancel;
- expired outbox claims are reclaimable and cleared after successful dispatch;
- retryable provider failures clear claim metadata before moving to
  `retry_wait`;
- periodic processing skips unsafe OpenBao replication states;
- manual drain returns an operator-visible error on unsafe replication states.

## Hardening Order

Hardening should proceed in this order:

1. Test architecture baseline and first core model invariants.
2. Provider-agnostic state model expansion.
3. Security boundary tests for redaction, SSRF, auth, and restore guard.
4. Provider conformance expansion across AWS, Kubernetes, and GitLab.
5. Restart, retry, and real-provider resilience e2e coverage.
