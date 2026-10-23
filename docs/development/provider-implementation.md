---
verifiedBy:
  - internal/providers/provider.go
  - internal/providers/providertest/provider.go
  - internal/providers/providerutil/providerutil.go
  - internal/providers/endpointguard/endpointguard.go
  - internal/backend/backend.go
  - internal/backend/path_destinations.go
  - internal/backend/path_associations.go
  - internal/backend/api_golden_test.go
---

# Implement a provider

Add or change a destination provider by following the steps below, then review
the result against the checklist. The interface, request semantics, plan
actions, error classes, and core and provider responsibilities are defined in
[Provider contract](provider-contract.md). Do not restate them in provider
code comments or docs; link to the contract.

## Prerequisites

- Read [Provider contract](provider-contract.md) and
  [Ownership and drift](../concepts/ownership-and-drift.md).
- Identify the destination API calls for create, update, delete, and read, and
  the remote field that can carry ownership metadata.
- Identify the destination's real maximum payload size and naming rules.

## Implementation steps

1. Create a package under `internal/providers/<name>` with a stable
   `ProviderType` constant and a `New()` constructor.
2. Implement `providers.Provider`. Use `providerutil.New(ProviderType)` for
   config parsing, validation errors, blocked plans, and classified provider
   errors.
3. Declare the weakest accurate `Capabilities`. Do not advertise a capability
   the provider could support later.
4. Implement `ValidateConfig`. Reject missing fields, unsupported auth modes,
   recognized but unsupported sensitive fields, and names or scopes the
   provider cannot safely manage.
5. Implement `NormalizeAssociationConfig`. Reject non-empty config if the
   provider has no association settings. If it has settings, return the
   complete canonical config and a stable `Identity`.
6. Put destination API calls behind a client interface so tests can mock them.
   Bound HTTP clients with a request timeout, constrained or disabled redirects,
   and bounded response reads. Route custom API addresses through
   `internal/providers/endpointguard`.
7. Implement `Plan` without remote mutation.
8. Implement `Upsert` with ownership metadata, ownership checks, stale source
   version rejection, and content idempotency.
9. Implement `Delete` for owned objects only, if the provider advertises
   `SupportsDeleteIfOwned`.
10. Implement `ReadState` before you rely on reconcile or drift behavior. Fill
    `PayloadSHA256` and `Verification` only from evidence the provider compared.
11. Implement `Health` without remote mutation.
12. Map every provider failure to a `providers.ErrorClass`.
13. Add a `providertest.Run` harness, including the maturity matrix, and
    provider-specific mocked tests.
14. Register the provider's fields in the backend:
    - destination fields in `destinationConfigFieldKeysByType` and sensitive
      fields in `destinationSensitiveConfigFieldKeysByType` in
      `internal/backend/path_destinations.go`;
    - association fields in `associationProviderConfigFieldKeys` and
      `associationProviderConfigFieldKeysByType`, and identity fields in
      `associationProviderIdentityFieldKeysByType`, in
      `internal/backend/path_associations.go`.
15. Add the provider to `productionProviders` in `internal/backend/backend.go`
    only after mutation and error paths are covered.
16. Update the API golden snapshots for the new fields, then review the diff:

    ```sh
    UPDATE_API_GOLDEN=1 go test ./internal/backend -run TestAPIGoldenResponses
    ```

17. Add an e2e lane under `test/e2e/<name>` when mocks cannot prove the API
    behavior. Keep it self-contained where practical. Use opt-in real-provider
    tests only for IAM, token, or managed-service behavior that local stacks
    cannot prove.

## Credentials and endpoints

Prefer workload identity, a default SDK credential chain with explicit opt-in,
or short-lived federation over static keys. Store credentials only in fields
registered as sensitive, so the backend seal-wraps them and redacts reads.

Custom endpoints need an explicit endpoint policy. Non-local plain HTTP must be
rejected unless the provider has an explicit local or test escape hatch. Token
auth providers that accept an API address must apply the
[endpoint address policy](../security/threat-model.md#endpoint-address-policy).

## Review checklist

Before you merge a new or materially changed provider, confirm:

- [ ] Capabilities match implemented and tested behavior, including
      `MaxPayloadBytes`.
- [ ] Validation rejects unsupported auth modes, unsupported sensitive fields,
      unsafe endpoints, and unmanageable names with the `validation` class.
- [ ] Sensitive fields are registered as sensitive and never appear in
      destination reads, errors, or logs.
- [ ] `Plan`, `Health`, and `ReadState` never mutate remote state.
- [ ] `Upsert` and `Delete` refuse objects whose ownership metadata does not
      match, and return the `ownership` class.
- [ ] `Upsert` rejects a remote source version newer than the request.
- [ ] Repeating an upsert or delete succeeds without changing the outcome.
- [ ] A missing object on delete returns a successful result.
- [ ] Multi-step mutations declare `classified_failure` partial success and
      return no `SyncResult` on a post-write failure.
- [ ] Throttling maps to `rate_limit`, outages to `unavailable`, and auth
      failures to `authn` or `authz`.
- [ ] Error messages and remote metadata contain no secret values or
      credentials.
- [ ] Backend tests prove destination config flows through validation, health,
      plan, dispatch, delete, and reconcile.
- [ ] The conformance harness and provider-specific tests pass under
      `make ci-core`.

## Documentation checklist

When you add or materially change a provider, update:

- the provider page, `docs/configure/<provider>.md`, for auth modes,
  permissions, ownership metadata keys, payload limit, and drift and delete
  behavior;
- the shape support matrix in
  [Choose a secret shape](../configure/associations.md#choose-a-secret-shape);
- the [registered providers table](provider-contract.md#registered-providers)
  in the provider contract;
- [Testing](testing.md) for new conformance or e2e lanes; and
- the e2e README under `test/e2e/<name>/README.md`, such as
  [the kind e2e README](../../test/e2e/kind/README.md), when a local or manual
  test path exists.
