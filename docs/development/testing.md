---
verifiedBy:
  - Makefile
  - mk/ci.mk
  - mk/checks.mk
  - mk/security.mk
  - mk/build.mk
  - mk/e2e.mk
  - mk/tools.mk
  - mk/config.mk
  - .ci/versions.yaml
  - hack/tools/version_policy/main.go
  - hack/ci/release_verification_test.go
  - .github/workflows/ci.yml
  - .github/workflows/aws-web-identity-e2e.yml
  - internal/backend/api_golden_test.go
  - internal/backend/storage_golden_test.go
  - test/e2e
---

# Testing

Match the evidence to the risk of the change. Documentation and small local
code changes need the fast gate. Provider, queue, security, release, API, and
storage changes also need the matching lanes below.

## Local gates

Run the fast gate while you develop:

```sh
make ci-fast
```

`ci-fast` runs `verify-tidy`, `docs-check`, `versions-check`, `verify-fmt`,
`vet`, `test`, and `build`.

Run the core gate before you open or merge a non-trivial change:

```sh
make ci-core
```

`ci-core` runs `verify-tidy`, `lint`, `security-ci`, `test`, `test-race`,
`fuzz`, `build`, and `release-artifacts`. `lint` runs `docs-check`,
`versions-check`, `verify-fmt`, `workflow-lint`, `semgrep-ci`, `vet`, and
`golangci-lint`. `security-ci` runs `vulncheck`, `license-check`, and the Trivy
filesystem scan.

`ci-core` fails when a required tool is missing. A missing scanner does not
count as a passing check. Install the tools before you run it:

- Run `make install-go-tools` for `gofumpt`, `staticcheck`, `govulncheck`,
  `golangci-lint`, `go-licenses`, and `actionlint`.
- Install Semgrep and Trivy separately at the versions pinned in
  `.ci/versions.yaml`.
- Install Git, Bash, GnuPG (`gpg` and `gpgconf`), and `jq`. `make test` runs the
  offline release verification tests in `hack/ci`, which fail without them.

Run `git diff --check` before you commit documentation or generated text.

## Version policy

`make versions-check` compares `.ci/versions.yaml` with `.go-version`, `go.mod`,
the Make tool and image defaults, the kind build image, Compose files under
`test/e2e`, and GitHub Action pins in workflow and composite-action files under
`.github`. An unlisted external action, a changed pin, or a mismatched default
fails the check. Keep local actions under `.github/actions` so their external
dependencies are checked too.

OpenBao and GitLab defaults include image digests. Environment overrides remain
available for upgrade tests. Record the resolved Compose images and the server
versions that the e2e tests report with the candidate commit. A passing
`versions-check` proves configuration consistency only; the matching e2e lanes
provide runtime qualification.

## Test lanes

| Lane | Command | Evidence |
| --- | --- | --- |
| Unit and contract | `make test` | Package behavior, backend path contracts, provider conformance, payload construction, redaction, error classification, and offline release verification. |
| Race | `make test-race` | No detected data races in source writes, association updates, queue claims, runtime cache use, and dispatch. |
| Fuzz | `make fuzz` | `FuzzBuildRaw`, `FuzzBuildJSON`, and `FuzzRenderAssociationObjectName` tolerate malformed input for `FUZZTIME` (default `10s`) each. |
| Security | `make security-ci` | Go vulnerabilities, dependency licenses, and Trivy filesystem findings pass. |
| Release artifacts | `make release-artifacts` | Linux `amd64` and `arm64` binaries, SPDX SBOMs, the license report, and checksums build from the tree. |

The `Core Quality` job in `.github/workflows/ci.yml` requires the fast, lint,
security, heavy (`make ci-heavy`), LocalStack e2e, kind e2e, and lifecycle e2e
jobs to succeed on every pull request.

## Provider and lifecycle e2e

Run the matching lane when a change touches a provider, its destination config,
payload shape, ownership metadata, error classification, or fixture.

| Area | Command | Run when |
| --- | --- | --- |
| AWS Secrets Manager with LocalStack | `make test-e2e` | AWS provider behavior, ownership tags, the LocalStack fixture, or queue and status behavior changes. Runs in CI. |
| Kubernetes Secrets | `make test-e2e-kind` | Kubernetes auth, RBAC, Secret metadata, data mapping, read-state, owned delete, or immutable Secret behavior changes. Runs in CI. |
| OpenBao lifecycle | `make test-e2e-resilience` | Queue durability, status persistence, restore guard, HA failover, seal recovery, or replication safety changes. Runs in CI. |
| OCI plugin distribution | `make test-e2e-oci-localstack` | OCI image layout, OpenBao plugin download, or auto-registration changes. The release workflow runs the release-binary variant. |
| GitLab project variables | `E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab` | Variable attributes, masked or hidden variables, ownership descriptions, read-state, or GitLab HTTP behavior changes. Starts a local GitLab container. |
| Real AWS Secrets Manager | `E2E_AWS_CONFIRM=1 make test-e2e-aws` | IAM, STS assume role, web identity, AWS API behavior, or delete recovery needs real AWS evidence. |

Real-provider tests are opt-in, sandbox-scoped, and never required for normal
CI. After a real AWS run, remove leftover secrets under `E2E_AWS_SECRET_PREFIX`
with `E2E_AWS_CONFIRM=1 make test-e2e-aws-clean`. The manual
`.github/workflows/aws-web-identity-e2e.yml` workflow runs the same AWS test
with GitHub OIDC. Each lane's setup is in its README under `test/e2e`, such as
[the AWS e2e README](../../test/e2e/aws/README.md).

## Evidence by change type

### Provider changes

- Provider unit tests for config validation, request shape, API edge cases, and
  error classification.
- `providertest` conformance coverage, including the maturity matrix. See
  [Conformance tests](provider-contract.md#conformance-tests).
- The matching provider e2e lane when mocks cannot prove the behavior.
- The documentation listed in
  [Implement a provider](provider-implementation.md#documentation-checklist).

### Queue and dispatcher changes

- Unit tests for operation state transitions, due and path indexes, claim
  expiry, retry scheduling, cancellation, and pruning.
- Drift detect and repair tests for disabled config, restore guard, queue
  capacity, duplicate repair suppression, and repair trigger attribution.
- Race tests for concurrent source writes, association writes, runtime cache
  use, and dispatch paths.
- Model-style tests that assert invariants across several operations.
- `make test-e2e-resilience` when persistence, restart, HA, restore guard, or
  replication safety behavior changes.

### Security and authorization changes

- Tests proving source payload values do not appear in plan, queue, status,
  reconcile, logs, metrics, or errors.
- Tests proving sensitive destination config is stored separately and redacted
  on reads.
- Destination constraint tests for source path and resolved-name prefixes.
- Provider tests for custom endpoint validation, bounded HTTP behavior, and
  `authn` and `authz` mapping.
- `make security-ci`.
- Updates to the [threat model](../security/threat-model.md),
  [policy examples](../security/policies.md), provider pages, or
  [Troubleshoot sync](../operate/troubleshoot.md) when operator behavior
  changes.

### API and storage changes

- Path tests that lock request fields, response fields, defaults, and error
  classes.
- API golden snapshots for response-shape drift.
- Pagination tests for `LIST` endpoints when listing behavior changes.
- Storage golden and compatibility tests when schema handling changes. See
  [Storage model](backend/storage-model.md).
- OpenAPI declaration and generated-document test updates when paths, request
  fields, or response fields change.
- Updates to the [API reference](../reference/api.md),
  [Status and diagnostics](../reference/status-and-diagnostics.md), or
  [Compatibility](../reference/compatibility.md) when user-visible behavior
  changes.

Update the snapshots explicitly, then review the fixture diff before you
commit:

```sh
UPDATE_API_GOLDEN=1 go test ./internal/backend -run TestAPIGoldenResponses
UPDATE_STORAGE_GOLDEN=1 go test ./internal/backend -run TestStorageGoldenLayout
```

During the preview, these snapshots are an early warning, not a stable
compatibility promise.

### Release and artifact changes

- `make ci-core`, which includes release artifact generation.
- `make test-e2e-oci-localstack` when OCI image layout or OpenBao OCI download
  behavior changes.
- Updates to [Release engineering](release-engineering.md) and
  [Verify release artifacts](../get-started/install.md#verify-release-artifacts)
  when operator verification steps change.

## Merge evidence

Before a non-trivial pull request is ready:

1. Run `make ci-core`.
2. Run the provider or lifecycle e2e lane that matches the changed behavior.
3. Record any opt-in real-provider evidence in the pull request.
4. Update the page that owns the changed behavior. See the owner map in the
   [documentation standard](documentation-style.md#give-every-topic-one-owner).
5. Verify generated or inspection artifacts when API, release, or docs tooling
   changes.
