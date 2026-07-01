# OpenBao Secret Sync Docs

Status: draft
Date: 2026-07-01

These docs describe the current design and implementation direction for
`openbao-plugin-secrets-sync`. The plugin is still early-stage, so documents
may describe intended contracts as well as implemented behavior. When that
matters, the document should say so explicitly.

## Documentation Shape

Use the root [README](../README.md) as the short project front door. Use this
page when you need to choose the right detailed document.

### Evaluate The Design

- [Product design](product-design.md) explains goals, non-goals, safety
  principles, and the user-facing model.
- [Architecture](architecture.md) explains the plugin boundary, storage model,
  queueing, background work, and consistency model.
- [API compatibility](api-compatibility.md) explains the KV-v2-like source API
  claim and the intentional differences.
- [HLD/LLD entry point](openbao-secret-sync-hld-lld.md) preserves the original
  design summary and recommendation trail.

### Use Or Operate The Plugin

- [User guide](user-guide.md) gives the current hands-on workflow for
  installing, configuring, writing source data, creating associations, and
  inspecting status.
- [Operator runbook](operator-runbook.md) gives operational checks,
  troubleshooting flows, restore-guard handling, and failure response guidance.
- [Release engineering](release.md) describes the current artifact workflow and
  plugin verification steps.
- [Security and operations](security-operations.md) records the threat model,
  authorization shape, redaction rules, restore safety, packaging, and
  operational requirements.
- [Observability](observability.md) describes the current OpenTelemetry metric
  surface and attribute policy.

### Build Or Review Implementation

- [Implementation plan](implementation-plan.md) tracks MVP scope, implemented
  slices, remaining hardening, and open questions.
- [Testing and hardening](testing.md) defines unit, contract, model, fuzz, e2e,
  and security test lanes.
- [API inspection artifacts](api/README.md) include the draft OpenAPI spec for
  reviewing path shape, defaults, response fields, and error classes.
- [Provider implementation guide](provider-implementation.md) explains the
  practical steps and review checklist for adding a provider.
- [Provider contract](provider-contract.md) defines the provider interface,
  capability model, payload rules, ownership behavior, and conformance
  expectations.

### Run Provider E2E Tests

- [LocalStack e2e workflow](../test/e2e/localstack/README.md) covers AWS
  Secrets Manager behavior against LocalStack.
- [Kind e2e workflow](../test/e2e/kind/README.md) covers Kubernetes Secrets
  behavior in a disposable kind cluster.
- [GitLab e2e workflow](../test/e2e/gitlab/README.md) covers GitLab project
  variables in a Dockerized GitLab CE stack.
- [Manual AWS e2e workflow](../test/e2e/aws/README.md) covers opt-in real AWS
  testing with OpenTofu-managed IAM fixtures.

## Documentation Maintenance

When behavior changes, update docs at the same ownership level as the code:

- user-visible command or response changes: update the user guide and runbook;
- provider interface or capability changes: update the provider contract and
  provider implementation guide;
- queue, restore, authorization, or redaction changes: update security and
  operations, testing, and the runbook;
- new hardening evidence: update testing and the implementation plan.
