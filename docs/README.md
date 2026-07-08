# OpenBao Secret Sync docs


These docs describe how to use, operate, secure, and maintain
`openbao-plugin-secrets-sync`. Documents in this tree describe maintained
behavior and contracts.

## Documentation shape

Use the root [README](../README.md) as the short project front door. Use this
page when you need to choose the right detailed document.

### Get started

- [Get started](getting-started/README.md) points to the shortest local
  validation paths.
- [User guide](guides/user-guide.md) shows the first-success workflow for
  installing, writing source data, creating one association, and checking
  status.
- [Secret shapes](guides/secret-shapes.md) explains how source paths become
  AWS, Kubernetes, or GitLab remote objects.
- [Delegated use](guides/delegated-use.md) explains hardened posture,
  source sync enablement, and destination prefix constraints.
- [Provider guides](providers/README.md) explain destination-specific
  configuration for AWS Secrets Manager, Kubernetes Secrets, and GitLab project
  variables.

### Understand the model

- [Concepts](concepts/README.md) collects the shared model behind user,
  provider, and operator docs.
- [Source model](concepts/source-model.md) explains why Secret Sync stores
  source data in its own mount and how its KV-v2-like source API behaves.
- [Sync model](concepts/sync-model.md) explains source state, associations,
  destination selectors, provider object shapes, and the main safety model.
- [Convergence](concepts/convergence.md) explains queued operations,
  `sync_operation_ids`, event dispatch, `queue/drain`, status states, manual
  sync, retry, and cancel.
- [Reconcile and drift](concepts/reconcile-and-drift.md) explains reconcile
  plan versus apply, background detect versus repair, verification, restore
  guard behavior, and disabled behavior.
- [Ownership and safety](concepts/ownership-and-safety.md) explains remote
  ownership metadata, restore identity, drift, collisions, and safe recovery.
- [Templating](concepts/templating.md) explains how `resolved_name`,
  `name_template`, and `data_key_template` turn source paths and keys into
  provider object names.

### Operate the plugin

- [Operations](operations/README.md) collects operator-facing procedures.
- [Runtime configuration](operations/runtime-configuration.md) explains
  mount-wide security posture, pause, restore guard, queue capacity, drift
  work, and dispatch tuning.
- [Operator runbook](operations/operator-runbook.md) gives operational checks,
  troubleshooting flows, restore-guard handling, and failure response guidance.
- [Observability](operations/observability.md) describes the OpenTelemetry
  metric surface and attribute policy.
- [Install and verify release artifacts](operations/install-and-verify.md)
  describes artifact verification and plugin installation.
- [Restore and clone review](operations/restore-and-clone.md) describes the
  restore guard review workflow before remote mutation resumes.

### Review security

- [Security](security/README.md) collects security-facing documents.
- [Security model](security/security-model.md) records the threat model,
  authorization shape, redaction rules, restore safety, packaging, and
  operational requirements.
- [Policy examples](security/policies.md) provides OpenBao policy snippets for
  common operator, app, delegated-owner, and auditor roles.

### Build or review implementation

- [Development](development/README.md) collects implementation-facing
  documents.
- [Architecture](development/architecture.md) explains the plugin boundary,
  component model, provider boundary, and consistency model.
- [Backend](development/backend/README.md) explains backend storage, request
  lifecycles, queueing, background work, safety gates, and diagnostics.
- [Provider contract](development/provider-contract.md) defines the provider
  interface, capability model, payload rules, ownership behavior, and
  conformance expectations.
- [Provider implementation guide](development/provider-implementation.md)
  explains the practical steps and review checklist for adding a provider.
- [Release engineering](development/release-engineering.md) describes the
  maintainer release automation and artifact workflow.
- [Testing and hardening](development/testing.md) defines unit, contract, model,
  fuzz, e2e, and security test lanes.
- [Documentation style](development/documentation-style.md) defines the
  project documentation style baseline.

### Inspect references

- [Reference](reference/README.md) collects API and compatibility references.
- [API surface](reference/api-surface.md) explains the Secret Sync API path
  groups and conceptual contract.
- [API compatibility](reference/api-compatibility.md) explains the KV-v2-like
  source API claim and the intentional differences.
- [API inspection artifacts](reference/api/README.md) include the draft OpenAPI
  spec for reviewing path shape, defaults, response fields, and error classes.

### Run provider e2e tests

- [LocalStack e2e workflow](../test/e2e/localstack/README.md) covers AWS
  Secrets Manager behavior against LocalStack.
- [OpenBao lifecycle resilience e2e workflow](../test/e2e/resilience/README.md)
  covers durable three-node Raft storage, static seal self-unseal, HA failover,
  queued work, status persistence across OpenBao restart, and operator seal
  recovery.
- [Kind e2e workflow](../test/e2e/kind/README.md) covers Kubernetes Secrets
  behavior in a disposable kind cluster.
- [GitLab e2e workflow](../test/e2e/gitlab/README.md) covers GitLab project
  variables in a Dockerized GitLab CE stack.
- [Manual AWS e2e workflow](../test/e2e/aws/README.md) covers opt-in real AWS
  testing with OpenTofu-managed IAM fixtures.

## Documentation maintenance

When behavior changes, update docs at the same ownership level as the code:

- First-success workflow changes: update the user guide.
- Source-model, source-shape, templating, ownership, or provider-object
  changes: update secret shapes, provider guides, concepts, and the sync model
  when the mental model changes.
- Runtime, queue, convergence, drift, reconcile, restore, or dispatch changes:
  update runtime configuration, concepts, and the runbook when recovery behavior
  changes.
- Delegated authorization, security posture, or source sync changes: update
  delegated use, security policy examples, and the security model.
- Provider configuration, naming, ownership, or capability changes: update the
  affected provider guide.
- Provider interface or capability changes: update the provider contract and
  provider implementation guide.
- Redaction changes: update security, testing, concepts, and the affected
  response documentation.
- New hardening evidence: update testing and the affected development or
  operations document.
- Documentation wording or structure changes: follow the project
  [documentation style](development/documentation-style.md).
