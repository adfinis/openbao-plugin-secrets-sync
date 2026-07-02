# Provider implementation guide

This guide explains the practical workflow for adding or reviewing a destination
provider. The normative interface and safety rules remain in
[Provider contract](provider-contract.md).

Providers are Go packages compiled into the external OpenBao plugin binary.
They are not separate plugin processes.

## Provider boundary

The core backend owns:

- OpenBao request handling and policy surface;
- source storage and metadata;
- association validation and lifecycle;
- destination-level source path and resolved-name prefix constraints;
- payload construction and payload hashing;
- durable queueing and retry decisions;
- status, reconcile, and observability records;
- redaction of destination reads.

The provider owns:

- provider-specific destination config validation;
- provider API client construction;
- remote plan, upsert, delete, read-state, and health behavior;
- provider-specific ownership metadata;
- provider error classification.

Providers do not receive OpenBao request objects, read OpenBao storage, make
authorization decisions, or log secret payloads.

## Implementation steps

1. Add a package under `internal/providers/<name>`.
2. Implement `providers.Provider`.
3. Declare conservative capabilities.
4. Validate destination config and sensitive-field rules.
5. Add a client boundary so API behavior can be mocked.
6. Implement plan before mutation.
7. Implement idempotent upsert.
8. Implement owned delete if the provider advertises delete support.
9. Implement read-state before relying on drift or reconcile behavior.
10. Implement health diagnostics.
11. Map provider failures to stable `providers.ErrorClass` values.
12. Add provider conformance coverage.
13. Add provider-specific mocked tests for edge cases.
14. Register the provider in the backend only after mutation and error paths
    are covered.
15. Add local or opt-in e2e coverage when the provider has an API surface that
    cannot be trusted from mocks alone.

## Capability rules

Start with the weakest accurate capability set. Do not advertise a capability
because the provider could support it later.

Key capability questions:

- Can the provider read back values?
- Can the provider read back metadata?
- Can it store or compare the payload hash?
- Can it prove a remote object is owned before update?
- Can it prove ownership before delete?
- Does it support `secret-path` granularity?
- Does it support `secret-key` granularity?
- Does it support destination-native source-key data mapping?
- What is the real max payload size before provider mutation?

If ownership proof is partial, document the reduced guarantee in
[Provider contract](provider-contract.md) and make plan/status diagnostics
clear.

## Destination config

Provider config distinguishes sensitive and non-sensitive fields.

Non-sensitive fields may be stored in the destination record and returned by
read endpoints. Store sensitive fields under the seal-wrapped destination
secret prefix and redact them on reads.

Validation rejects:

- missing required fields;
- unsupported auth modes;
- sensitive fields that are recognized but not implemented;
- custom endpoints without an explicit endpoint policy;
- non-local insecure HTTP unless the provider has an explicit local/test
  escape hatch;
- names or scopes the provider cannot safely manage.

Prefer workload identity, default SDK chains with explicit opt-in, or
short-lived federation over static keys.

HTTP providers use bounded clients: request timeout, constrained or disabled
redirects, bounded response-body reads, and explicit validation for custom or
insecure endpoints.

## Plan, upsert, delete, and read-state

Plan does not mutate remote state. It returns one of the stable provider
actions:

- `create`
- `update`
- `noop`
- `conflict`
- `blocked`

Upsert receives prepared payload bytes and the payload hash. It does not
reformat the payload before writing when the provider stores or compares that
hash.

Delete only removes owned objects. If ownership cannot be proven, return the
`ownership` error class instead of deleting.

Read-state returns remote existence, ownership, payload hash, source version,
and remote version where the provider can know them. Reconcile and
drift status depend on this being precise.

## Error classification

Map provider errors into stable classes:

- `validation`
- `authn`
- `authz`
- `rate_limit`
- `unavailable`
- `collision`
- `ownership`
- `drift`
- `capacity`
- `internal`

Only `rate_limit` and `unavailable` are automatically retried by the core retry
policy. Treat auth, policy, validation, ownership, collision, drift, and
capacity failures as terminal until the operator changes configuration or
manually retries.

Provider errors must not expose secret values, credentials, tokens, raw
provider responses containing secret material, or high-cardinality payload
data.

## Test expectations

Every provider uses `internal/providers/providertest` for shared contract
coverage. The conformance harness covers:

- provider type and capabilities;
- valid and invalid destination config;
- health diagnostics;
- plan action mapping;
- create/update/delete/read-state lifecycle when implemented;
- upsert and delete error classification;
- the provider maturity matrix for ownership loss, auth failure, throttling,
  payload limits, partial-success behavior, stale remote state, and delete
  semantics.

Provider-specific tests cover behavior the shared harness cannot know:

- provider API request shape;
- ownership metadata layout;
- stale source-version rejection;
- provider name and scope validation;
- payload-size limits;
- authn and authz mapping;
- throttling and service outage mapping;
- collision and ownership-loss behavior;
- delete semantics;
- redaction of sensitive config fields.

Partial success is provider-specific. Providers with atomic value+metadata
mutations declare that in the maturity matrix and keep lifecycle tests covering
the atomic mutation. Providers with multi-step mutations include a classified
failure case where an earlier remote mutation can succeed but the overall
provider call still returns no `SyncResult` and a stable error class.

Backend tests prove the provider is registered and that destination config
flows through validation, health, plan, queue dispatch, delete, and reconcile
paths.

Keep E2E tests self-contained where practical. Use opt-in real-provider tests
only for IAM, token, or managed-service behavior that local stacks cannot
prove.

## Documentation checklist

When adding or materially changing a provider, update:

- [User guide](../guides/user-guide.md) for operator commands and examples;
- [Operator runbook](../operations/operator-runbook.md) for failure response details;
- [Provider contract](provider-contract.md) for provider-specific guarantees;
- [Testing and hardening](testing.md) for new conformance or e2e lanes;
- the relevant e2e workflow README when a local or manual test path exists.
