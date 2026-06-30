# Implementation Plan

Status: draft
Date: 2026-06-30

## MVP Scope

### Must Have

- External plugin binary with multiplex support.
- KV-v2-like write, read, list, soft delete, undelete, destroy, metadata delete,
  and metadata read.
- CAS support for writes.
- Destination registry with redacted reads.
- Association registry with source eligibility checks.
- Provider capability model.
- Fake provider for local and integration testing.
- AWS Secrets Manager provider.
- Kubernetes Secret provider.
- Durable outbox and enqueue-intent recovery.
- Periodic retry.
- Manual sync.
- Manual reconcile and reconcile plan.
- Status endpoint with stable states.
- Per-object status for `secret-path` and `secret-key` granularity.
- Destination validation and health endpoint.
- Name templates with reservation index.
- `json` and `raw` payload formats.
- `secret-path` and `secret-key` granularity.
- Safety modes: `fail_if_exists`, `overwrite_owned_only`, `overwrite_any`.
- Delete modes: `retain`, `delete`, `orphan`.
- Global pause, restore guard, and queue capacity.
- Unit tests for storage, queue, templates, capabilities, redaction, and fake
  provider behavior.
- Integration test against a local OpenBao dev server and fake destination
  provider.

### Should Have

- Per-destination rate limit.
- Queue cancellation and retry endpoints.
- Drift detection.
- Metrics endpoint.
- Provider-specific local integration using localstack, envtest, or kind.
- Structured runbook examples.

### Later

- GitHub Actions provider.
- GitLab CI/CD provider.
- Azure Key Vault provider.
- GCP Secret Manager provider.
- UI integration.
- Brownfield external controller for existing KV mounts.
- Import remote secret as new local version.
- Namespaces beyond what the mounted plugin naturally receives.
- Rich OpenAPI docs.
- Synchronous `sync_required=true` mode.

## Phase 0: Design Spike

Tasks:

- Confirm OpenBao plugin SDK version and minimum OpenBao version.
- Create repository/package skeleton.
- Implement backend registration and minimal mount.
- Implement fake provider with configurable responses.
- Implement minimal `data/*`, `associations/*`, `queue/*`, and `status/*`
  paths.
- Validate periodic processing in OpenBao dev.

Exit criteria:

- plugin can be registered and mounted;
- local secret write creates durable source version and enqueue intent;
- fake sync operation is processed by periodic function;
- status marks fake sync as `SYNCED`;
- redaction canary does not appear in logs or status.

## Phase 1: Local KV And Queue

Tasks:

- Implement KV-v2-like storage records.
- Implement CAS, metadata list/read, soft delete, undelete, destroy, and
  metadata delete.
- Implement durable outbox.
- Implement enqueue-intent recovery.
- Implement operation claiming and retry schedule.
- Implement queue capacity behavior.
- Implement per-object status model.

Exit criteria:

- local KV behavior is covered by unit tests;
- queue survives plugin restart;
- incomplete enqueue intent is recovered;
- stale operations do not overwrite newer source versions;
- status reflects pending, synced, failed, and disabled states.

## Phase 2: Destination Framework

Tasks:

- Implement provider interface and capability model.
- Implement template engine and name reservation index.
- Implement canonical payload builder.
- Implement destination config validation.
- Implement dry-run plan endpoint.
- Implement source eligibility checks for association activation.
- Implement fake provider test harness.

Exit criteria:

- fake provider can report create, update, delete, conflict, and partial-success
  plans;
- destination credentials are redacted on read;
- invalid templates fail at association creation;
- name collisions are rejected or require explicit operator resolution;
- associations cannot bypass source eligibility.

## Phase 3: AWS Secrets Manager Provider

Tasks:

- Implement AWS auth options, preferring workload identity and role assumption.
- Implement upsert, delete, read-state, and health.
- Add ownership tags.
- Add collision policy behavior.
- Add AWS error classification.
- Add local integration tests with mocks or localstack where practical.

Exit criteria:

- create, update, and delete sync works;
- remote ownership loss is detected;
- AWS API errors map to stable error classes;
- payload size limits are enforced before remote calls;
- no secret value appears in logs, errors, status, or plan output.

## Phase 4: Kubernetes Provider

Tasks:

- Implement kubeconfig, in-cluster, and service-account auth where appropriate.
- Implement namespace and name validation.
- Implement Secret upsert, delete, read-state, and health.
- Add labels and annotations for ownership.
- Add envtest or kind-backed integration path.

Exit criteria:

- sync to Kubernetes Secret works;
- delete mode works;
- per-key partial status is visible;
- drift and ownership status are visible;
- Kubernetes auth and policy errors map to stable classes.

## Phase 5: Hardening

Tasks:

- Add rate limiting.
- Add metrics endpoint.
- Add structured redaction tests.
- Add fault injection tests for transient destination failure.
- Add e2e tests for plugin restart and OpenBao restart.
- Add restore and clone simulation tests.
- Write initial runbooks.

Exit criteria:

- documented operational runbook;
- failure-mode tests pass;
- no secret values in logs/status fixtures;
- restore guard prevents background remote mutation until explicit resume;
- queue pressure behavior is documented and tested.

## Test Strategy

### Unit Tests

- storage key normalization;
- versioning and CAS behavior;
- metadata list behavior;
- association validation;
- source eligibility checks;
- template rendering;
- name reservation and collision behavior;
- canonical payload hashing;
- outbox retry schedule;
- enqueue-intent recovery;
- operation ordering and stale operation suppression;
- error classification;
- redaction;
- provider fake behavior.

### Integration Tests

- plugin registration and mount;
- write/read/list/delete;
- association creation;
- association rejection without source eligibility;
- fake provider sync;
- queue retry after plugin restart;
- reconciliation after missed operation;
- destination validation failures;
- global pause and resume;
- queue capacity behavior;
- restore guard behavior.

### Provider Tests

- AWS Secrets Manager using mocks and optional localstack;
- Kubernetes using envtest or kind;
- ownership metadata behavior;
- collision behavior;
- delete behavior;
- rate-limit and transient failure mapping;
- provider-specific name and size constraints.

### Security Tests

- destination credentials redacted from all read endpoints;
- logs do not contain secret payloads;
- status does not contain secret payloads;
- plan output does not contain secret payloads;
- metrics do not contain secret payloads or high-cardinality paths by default;
- custom endpoint SSRF validation;
- policy examples enforce operator/app-user separation;
- association creation cannot sync unreadable or ineligible source secrets.

### Model Tests

Use a small state-machine test model for:

- write version;
- create association;
- update association;
- delete source;
- delete association;
- enqueue operation;
- claim operation;
- provider success;
- provider transient failure;
- provider terminal failure;
- provider partial success;
- plugin restart;
- OpenBao restart;
- restore snapshot;
- reconcile.

The invariant: remote mutation never occurs without an eligible source,
authorized association, durable intent, and allowed provider capability.

## Open Questions

- Should local secret versions be seal-wrapped by default, or only destination
  credentials?
- What is the best default mount name: `sync-kv`, `secrets-sync`, or `kv-sync`?
- Should Kubernetes remain an MVP provider, or should GitHub/GitLab be
  prioritized for customer demos after AWS?
- Should destination provider plugins become separate binaries later, or remain
  packages in the same plugin binary?
- How should telemetry from an external plugin integrate with OpenBao telemetry
  consistently across deployments?
- What OpenBao versions must be supported?
- What is the exact operator workflow for declaring a restore or clone event?

## Definition Of Done For MVP

The MVP is not done when happy-path sync works. It is done when these scenarios
are boring and observable:

- destination outage;
- plugin restart;
- OpenBao restart;
- conflicting remote secret;
- queue capacity pressure;
- partial provider success;
- source eligibility failure;
- stale operation after newer source version;
- restored storage snapshot;
- credential rotation;
- redaction canary across logs, status, metrics, errors, and tests.
