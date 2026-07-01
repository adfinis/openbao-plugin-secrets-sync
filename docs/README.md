# OpenBao Secret Sync docs


These docs describe the current design and implementation direction for
`openbao-plugin-secrets-sync`. The plugin is still early-stage, so documents
may describe intended contracts as well as implemented behavior. When that
matters, the document should say so explicitly.

## Documentation shape

Use the root [README](../README.md) as the short project front door. Use this
page when you need to choose the right detailed document.

### Get started

- [Get started](getting-started/README.md) points to the shortest local
  validation paths.
- [User guide](guides/user-guide.md) shows the current operator workflow for
  installing, configuring, writing source data, creating associations, and
  inspecting status.
- [Provider guides](providers/README.md) explain destination-specific
  configuration for AWS Secrets Manager, Kubernetes Secrets, and GitLab project
  variables.

### Operate the plugin

- [Operations](operations/README.md) collects operator-facing procedures.
- [Operator runbook](operations/operator-runbook.md) gives operational checks,
  troubleshooting flows, restore-guard handling, and failure response guidance.
- [Observability](operations/observability.md) describes the current
  OpenTelemetry metric surface and attribute policy.
- [Release engineering](operations/release.md) describes the artifact workflow
  and plugin verification steps.

### Review security

- [Security](security/README.md) collects security-facing documents.
- [Security model](security/security-model.md) records the threat model,
  authorization shape, redaction rules, restore safety, packaging, and
  operational requirements.

### Build or review implementation

- [Development](development/README.md) collects implementation-facing
  documents.
- [Architecture](development/architecture.md) explains the plugin boundary,
  storage model, queueing, background work, and consistency model.
- [Provider contract](development/provider-contract.md) defines the provider
  interface, capability model, payload rules, ownership behavior, and
  conformance expectations.
- [Provider implementation guide](development/provider-implementation.md)
  explains the practical steps and review checklist for adding a provider.
- [Testing and hardening](development/testing.md) defines unit, contract, model,
  fuzz, e2e, and security test lanes.
- [Documentation style](development/documentation-style.md) defines the
  project documentation style baseline.

### Inspect references

- [Reference](reference/README.md) collects API and compatibility references.
- [API compatibility](reference/api-compatibility.md) explains the KV-v2-like
  source API claim and the intentional differences.
- [API inspection artifacts](reference/api/README.md) include the draft OpenAPI
  spec for reviewing path shape, defaults, response fields, and error classes.

### Run provider e2e tests

- [LocalStack e2e workflow](../test/e2e/localstack/README.md) covers AWS
  Secrets Manager behavior against LocalStack.
- [Kind e2e workflow](../test/e2e/kind/README.md) covers Kubernetes Secrets
  behavior in a disposable kind cluster.
- [GitLab e2e workflow](../test/e2e/gitlab/README.md) covers GitLab project
  variables in a Dockerized GitLab CE stack.
- [Manual AWS e2e workflow](../test/e2e/aws/README.md) covers opt-in real AWS
  testing with OpenTofu-managed IAM fixtures.

## Documentation maintenance

When behavior changes, update docs at the same ownership level as the code:

- User-visible command or response changes: update the user guide and runbook.
- Provider configuration changes: update the affected provider guide.
- Provider interface or capability changes: update the provider contract and
  provider implementation guide.
- Queue, restore, authorization, or redaction changes: update security,
  testing, and the runbook.
- New hardening evidence: update testing and the affected development or
  operations document.
- Documentation wording or structure changes: follow the project
  [documentation style](development/documentation-style.md).
