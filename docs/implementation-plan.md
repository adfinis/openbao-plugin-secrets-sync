# Implementation Plan

Status: draft
Date: 2026-06-30

## Current Implementation Baseline

Implemented backend slices now include:

- KV-v2-like local source storage with CAS, metadata operations, soft delete,
  undelete, destroy, and metadata deletion guards;
- destination registry with redacted reads, validation, health, and a fake
  provider;
- provider-agnostic dispatch through the provider registry;
- provider conformance harness with fake, AWS Secrets Manager, Kubernetes
  Secrets, and GitLab project variable coverage;
- association creation, planning, deletion, source eligibility checks, name
  reservations, and template rendering;
- core `secret-key` granularity expansion for providers that advertise support,
  including per-key operation IDs, per-key status, per-key reconcile, and fake
  provider coverage;
- `raw` payload format for `secret-key` associations with string or byte source
  values;
- per-association disable, enable, and manual sync controls;
- association `delete_mode` with source-delete enqueue semantics;
- durable outbox, enqueue-intent recovery, queue summary, operation read,
  cancel, manual retry, and bounded manual drain;
- provider delete dispatch for durable delete operations;
- manual per-path reconcile plan/apply using provider read-state;
- automatic retry for `rate_limit` and `unavailable` provider errors with a
  bounded retry budget;
- OpenTelemetry metric API instrumentation for queue depth, dispatch,
  provider requests, reconcile results, and restore guard state;
- status records with payload hashes and no secret payload disclosure;
- AWS Secrets Manager provider with type, capabilities, validation, SDK-backed
  client boundary, mocked plan/upsert/delete/read-state/health behavior, AWS
  error classification, ownership tag handling, destination config for default
  and assume-role auth, optional endpoint override, and backend registration.
- Kubernetes Secrets provider with type, capabilities, validation, client-go
  client boundary, mocked plan/upsert/delete/read-state/health behavior,
  Kubernetes error classification, ownership labels and annotations,
  destination config for in-cluster and kubeconfig auth, and backend
  registration.
- GitLab project variable provider with type, capabilities, validation,
  standard HTTP client boundary, mocked plan/upsert/delete/read-state/health
  behavior, HTTP error classification, ownership metadata in variable
  descriptions, seal-wrapped API token config, and backend registration.
- self-contained OpenBao plus LocalStack e2e coverage for plugin registration,
  mounting, AWS destination configuration, queue drain, create, update, delete,
  ownership tags, and status transitions.
- self-contained OpenBao plus kind e2e coverage for plugin registration,
  mounting, in-cluster Kubernetes auth, destination validation, health,
  queue drain, create, update, reconcile/read-state, delete, ownership labels,
  and payload metadata.

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
- GitLab project variable provider.
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
- Queue cancellation and retry endpoints.
- Unit tests for storage, queue, templates, capabilities, redaction, and fake
  provider behavior.
- Integration test against a local OpenBao dev server and fake destination
  provider.
- Self-contained e2e test against OpenBao dev mode and LocalStack-backed AWS
  Secrets Manager.
- Self-contained e2e test against OpenBao dev mode in kind and Kubernetes
  Secrets.

### Should Have

- Per-destination rate limit.
- Drift detection.
- OpenBao/plugin telemetry integration, with a metrics endpoint only as a
  fallback if runtime telemetry cannot expose plugin OTel instruments.
- Provider-specific local integration using LocalStack, kind, or Dockerized
  GitLab.
- Opt-in real GitLab project variable e2e test with disposable project
  fixture.
- Structured runbook examples.

### Later

- GitHub Actions provider.
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
- Implement CAS, metadata list/read/write, latest and selected-version soft
  delete, undelete, destroy, and metadata delete.
- Implement durable outbox.
- Implement enqueue-intent recovery.
- Implement operation claiming and retry schedule.
- Implement queue capacity behavior.
- Implement per-object status model.

Exit criteria:

- local KV behavior is covered by unit tests;
- source metadata policy is covered by unit tests;
- queue survives plugin restart;
- incomplete enqueue intent is recovered;
- stale operations do not overwrite newer source versions;
- status reflects pending, synced, failed, and disabled states.

## Phase 2: Destination Framework

Tasks:

- Implement provider interface and capability model.
- Implement provider registry and make dispatch provider-agnostic.
- Implement template engine and name reservation index.
- Implement canonical payload builder and status payload hashing.
- Implement destination config validation.
- Implement dry-run plan endpoint.
- Implement source eligibility checks for association activation.
- Implement association disable, enable, and manual sync lifecycle controls.
- Implement fake provider test harness.

Exit criteria:

- fake provider can report create, update, delete, conflict, and partial-success
  plans;
- fake provider dispatch runs through the same registry path as real providers;
- destination validation, health, and association plan endpoints return
  structured diagnostics;
- payload size limits are enforced before provider mutation;
- status records include payload hashes but never secret values;
- destination credentials are redacted on read;
- invalid templates fail at association creation;
- name collisions are rejected or require explicit operator resolution;
- enabled associations cannot bypass source eligibility;
- disabled associations do not enqueue new work and cancel queued work when
  disabled;
- manual sync and enable use the same activation gates as association
  creation;
- source delete cancels queued upserts and enqueues owned delete operations
  only for `delete_mode=delete`.

## Phase 3: AWS Secrets Manager Provider

Completed foundation:

- AWS-specific mocked client cases exercise health, plan, upsert, delete,
  read-state, ownership rejection, and error classification.
- SDK client boundary uses the AWS SDK default configuration chain.
- Destination config supports `region`, `endpoint_url` with explicit
  `endpoint_policy`, `auth_mode=default`, and `auth_mode=assume_role` with
  `role_arn`, seal-wrapped `external_id`, and `session_name`.
- Sensitive destination fields are split from non-sensitive destination
  metadata, stored under the seal-wrapped destination secret prefix, and
  redacted on reads.
- Upsert, owned delete, read-state, and health behavior are implemented behind
  the provider interface.
- Ownership tags include association id, source path, source version, object id,
  and payload hash.
- Collision, ownership loss, throttling, authorization, and service failure
  paths map to stable provider results or error classes.
- Provider read-state reports remote existence, ownership metadata, payload
  hash, source version, and remote version where supported.
- Manual per-path reconcile plan/apply maps provider read-state into local
  status without mutating remote objects.
- Stale update and delete attempts are rejected when AWS metadata shows a newer
  managed source version.
- The backend registers `aws-sm` and passes destination config through
  validation, health, plan, upsert, and delete paths.
- The self-contained OpenBao plus LocalStack e2e path proves plugin catalog
  registration, mount configuration, AWS create/update/delete dispatch,
  ownership tags, and status updates.
- A manual real-AWS e2e path is scaffolded with OpenTofu-managed IAM fixtures,
  explicit operator confirmation, and cleanup under a disposable Secrets
  Manager name prefix.

Remaining tasks:

- Keep static AWS keys and session tokens unsupported until their auth path,
  rotation semantics, and tests are explicit.
- Add DNS-time endpoint checks and optional allowlists for production private
  endpoint deployments.
- Broaden LocalStack coverage for auth variants and AWS failure paths after
  sensitive destination config storage exists.
- Extend opt-in real AWS e2e coverage as provider auth modes and reconcile
  behavior grow.

Exit criteria:

- create, update, and delete sync works;
- remote ownership loss is detected;
- AWS API errors map to stable error classes;
- payload size limits are enforced before remote calls;
- no secret value appears in logs, errors, status, or plan output.

## Phase 4: Kubernetes Provider

Tasks:

- Implement kubeconfig and in-cluster auth.
- Implement namespace and name validation.
- Implement Secret upsert, delete, read-state, and health.
- Add labels and annotations for ownership.
- Add envtest or kind-backed integration path.

Exit criteria:

- unit-level sync to Kubernetes Secret works with the fake client;
- unit-level delete mode works;
- drift and ownership status are visible;
- Kubernetes auth and policy errors map to stable classes;
- kind-backed integration proves the provider against a real API server using
  in-cluster auth.

## Phase 5: Hardening

Tasks:

- Add rate limiting.
- Add exporter/runtime integration once the OpenBao plugin telemetry boundary
  is clear.
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
- deterministic queue drain for due work;
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
- AWS Secrets Manager LocalStack e2e for plugin registration, mount,
  create/update/delete, ownership tags, and status;
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
- Should GitHub Actions, GitHub variables, Azure Key Vault, or GCP Secret
  Manager follow GitLab project variables?
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
