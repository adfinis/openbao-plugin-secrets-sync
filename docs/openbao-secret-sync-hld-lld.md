# OpenBao Secret Sync Plugin HLD/LLD

Status: draft
Date: 2026-06-30
Owner: project maintainers

This file is the entry point for the split HLD/LLD. Detailed implementation
guidance now lives in focused documents:

- [Product design](product-design.md)
- [Architecture](architecture.md)
- [Provider contract](provider-contract.md)
- [Security and operations](security-operations.md)
- [Implementation plan](implementation-plan.md)

## Executive Summary

`openbao-plugin-secrets-sync` is a sync-aware, KV-v2-like OpenBao secret engine
plugin. OpenBao remains the source of truth for secret values, while selected
secrets are asynchronously synchronized to external destinations such as AWS
Secrets Manager, Kubernetes Secrets, GitHub Actions secrets, GitLab CI/CD
variables, Azure Key Vault, or GCP Secret Manager.

The recommended MVP is a plugin-based design, not a core `/sys/sync`
implementation:

```text
install plugin -> register plugin -> mount engine -> configure destinations -> write syncable secrets
```

The main product constraint is explicit: the MVP syncs secrets written through
this plugin's mount. It does not transparently observe arbitrary existing
`kv/` mounts. Brownfield sync from existing KV mounts should be a separate
controller after the plugin proves useful.

## Non-Clone Constraint

This project must not become a copy of Vault Enterprise Secret Sync.

It can reuse proven product concepts when they independently make sense for
OpenBao users, but the design intentionally differs:

- mount-scoped external plugin instead of core/system backend coupling;
- opt-in source data model instead of transparent sync of unrelated mounts;
- safe default remote ownership checks instead of unconditional last-write-wins;
- plan-first mutating operations;
- provider capability declarations;
- explicit restore and clone protection;
- per-object operational status for partial success and drift;
- implementation-facing diagnostics from the first release.

If a proposed behavior exists only because Vault does it, reject it unless it
is independently useful and safer in the OpenBao plugin model.

## Current Recommendation

Build the MVP as `openbao-plugin-secrets-sync` with:

- external plugin binary with multiplex support;
- KV-v2-like data and metadata paths;
- destination registry;
- association registry;
- durable outbox with retry and reconciliation;
- fake provider for Phase 0;
- AWS Secrets Manager provider;
- Kubernetes Secret provider;
- ownership-safe collision policy;
- source-read or source-eligibility checks before association activation;
- per-object status for `secret-path` and `secret-key` granularity;
- dry-run planning for association, sync, delete, and reconcile operations.

The most important proof is not the happy path. The MVP must demonstrate that a
destination outage, plugin restart, OpenBao restart, conflicting remote secret,
queue capacity pressure, partial provider failure, or restored storage snapshot
produces clear status and does not leak secret material.

## Architecture Summary

```text
                    +-----------------------------+
                    | OpenBao Core                |
                    | - auth, policy, audit       |
                    | - plugin lifecycle          |
                    | - mount-scoped storage view |
                    +--------------+--------------+
                                   |
                                   | logical backend RPC
                                   v
+--------------------------------------------------------------------------+
| openbao-plugin-secrets-sync                                              |
|                                                                          |
|  API paths                                                               |
|    data/*, metadata/*, destinations/*, associations/*, status/*, queue/* |
|                                                                          |
|  Domain services                                                         |
|    KV store      Destination registry      Association registry          |
|    Outbox queue  Sync dispatcher           Reconciler                    |
|    Status store  Provider adapters         Validation/planning           |
|                                                                          |
|  Storage                                                                 |
|    config, identity, schema, data, metadata, associations, outbox, status |
+------+------------------+---------------------+-------------------------+
       |                  |                     |
       v                  v                     v
 AWS Secrets Manager   Kubernetes Secrets   GitHub/GitLab/Azure/GCP/...
```

The plugin receives a mount-scoped storage view and cannot observe writes to
unrelated mounts. That boundary is the reason this design is a sync-aware
secret engine instead of a `/sys/sync` clone.

## Key Product Semantics

- Local writes succeed only after the local version and durable sync intent are
  recoverable.
- Destination sync is asynchronous by default.
- Remote reads are used only for validation, planning, reconciliation, drift
  detection, and diagnostics.
- Remote state is never authoritative for local secret data.
- Delete behavior is explicit: `retain`, `delete`, or `orphan`.
- `delete` is allowed only when ownership can be proven.
- Reconciliation enqueues work instead of doing unbounded remote mutation
  inline.
- Global pause, restore guard, queue capacity, and provider health are part of
  the MVP control plane.

## Document Ownership

Design changes should update the focused document that owns the behavior:

- API and user behavior: [Product design](product-design.md)
- storage, state machines, queueing: [Architecture](architecture.md)
- destination behavior: [Provider contract](provider-contract.md)
- authorization, redaction, restore: [Security and operations](security-operations.md)
- scope and delivery sequencing: [Implementation plan](implementation-plan.md)
