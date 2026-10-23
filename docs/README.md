# Secret Sync documentation

Secret Sync stores source secrets in its own OpenBao mount and pushes them to
external destinations through explicit associations. Start with
[Sync your first secret](get-started/first-sync.md), then configure the
destination you need.

## Get started

- [Install the plugin](get-started/install.md): verify release artifacts,
  register the plugin, and mount the engine.
- [Sync your first secret](get-started/first-sync.md): write a source secret,
  create an association, and confirm convergence.

## Configure

| Destination | Type | Remote object |
| --- | --- | --- |
| [AWS Secrets Manager](configure/aws-secrets-manager.md) | `aws-sm` | One secret per association |
| [Kubernetes Secrets](configure/kubernetes-secrets.md) | `k8s` | One `Opaque` Secret per association |
| [GitLab project variables](configure/gitlab-project-variables.md) | `gitlab` | One CI/CD variable per association object |

- [Configure associations](configure/associations.md): choose a secret shape,
  name remote objects, update associations, and choose a delete mode.
- [Delegate to application owners](configure/delegate.md): enable hardened
  posture, constrain destinations, and enable source sync.
- [Runtime settings](configure/runtime.md): pause the mount, set queue capacity,
  and enable drift detection or repair.

## Operate

- [Troubleshoot sync](operate/troubleshoot.md): diagnose blocked or failed sync
  and manage queued operations.
- [Review a restore or clone](operate/restore-and-clone.md): fence destinations
  and review state before remote mutation resumes.
- [Monitor sync](operate/monitor.md): poll queue, status, and readiness APIs.

## Security

- [Threat model](security/threat-model.md): assets, threats, authorization
  boundaries, and implemented controls.
- [Policy examples](security/policies.md): OpenBao policies for operator,
  application, delegated-owner, and auditor roles.

## Reference

- [API reference](reference/api.md): paths, source write forms, KV-v2
  differences, and pagination.
- [Status and diagnostics](reference/status-and-diagnostics.md): sync states,
  queue states, error classes, and `next_actions`.
- [Compatibility](reference/compatibility.md): supported OpenBao versions and
  preview stability.

## Concepts

- [Sync model](concepts/sync-model.md): source data, associations, and
  asynchronous convergence.
- [Ownership and drift](concepts/ownership-and-drift.md): ownership metadata,
  reconcile, drift repair, and mutation gates.

## Development

- [Architecture](development/architecture.md): plugin boundary, components, and
  code map.
- Backend internals: [storage model](development/backend/storage-model.md),
  [request lifecycle](development/backend/request-lifecycle.md),
  [queue and dispatch](development/backend/queue-and-dispatch.md),
  [reconcile and drift](development/backend/reconcile-and-drift.md), and
  [safety and diagnostics](development/backend/safety-and-diagnostics.md).
- [Provider contract](development/provider-contract.md) and
  [Implement a provider](development/provider-implementation.md).
- [Testing](development/testing.md): local gates, test lanes, and evidence by
  change type.
- [Release engineering](development/release-engineering.md): release approval,
  signing, and artifact workflow.
- [Observability internals](development/observability.md): metric instruments
  and attribute policy.
- [Documentation standard](development/documentation-style.md): how to write and
  verify these docs.
