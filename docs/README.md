# OpenBao Secret Sync Documentation

Status: draft
Date: 2026-06-30

This directory contains the initial design package for
`openbao-plugin-secrets-sync`, an OpenBao secret engine plugin for one-way
secret synchronization from OpenBao-managed source data to external
destinations.

The project is intentionally not a copy of Vault Enterprise Secret Sync. It
borrows proven concepts such as destinations, associations, retries, name
templates, and reconciliation, but the implementation model, safety defaults,
and operator diagnostics are OpenBao-native and plugin-first.

## Design Bar

- OpenBao remains the source of truth.
- The MVP is a mount-scoped external plugin, not a core `/sys/sync` feature.
- Sync is explicit and opt-in through this engine's data model.
- Remote overwrite requires ownership proof by default.
- Destructive and mutating remote actions must be plan-able.
- Destination providers must declare their real capabilities.
- Restore, clone, drift, partial success, and collision states are first-class.
- Operational status is part of the product contract.

## Document Map

- [HLD/LLD entry point](openbao-secret-sync-hld-lld.md) - compact summary and
  current recommendation.
- [Product design](product-design.md) - goals, non-goals, design principles,
  user-facing behavior, and API shape.
- [Architecture](architecture.md) - plugin boundary, components, storage model,
  consistency, queueing, and background work.
- [Provider contract](provider-contract.md) - provider interface,
  capabilities, naming, payload formatting, and provider test expectations.
- [Security and operations](security-operations.md) - threat model,
  authorization, redaction, ownership, audit, metrics, restore, and runbooks.
- [Implementation plan](implementation-plan.md) - MVP scope, phased plan, test
  strategy, and open decisions.

## How To Use These Docs

Start with [Product design](product-design.md) before making API or UX
decisions. Use [Architecture](architecture.md) and
[Provider contract](provider-contract.md) while implementing core packages.
Use [Security and operations](security-operations.md) as the review checklist
for any feature that writes remote state, logs data, stores credentials, or
changes authorization behavior.
