---
verifiedBy:
  - go.mod
  - .ci/versions.yaml
  - .github/dependabot.yml
  - mk/e2e.mk
  - internal/backend/runtime_state.go
  - internal/backend/backend.go
  - internal/backend/storage_records.go
---

# Compatibility

Secret Sync is a preview. It is not production-ready, and no release so far
establishes a compatibility boundary.

## Supported OpenBao versions

The plugin is built against the OpenBao Go API module `v2.6.0` and SDK module
`v2.6.1`, and requires an OpenBao server that provides the `2.6.0` plugin APIs.
OpenBao `2.5.x` is unsupported.

The preview qualification baseline is pinned in
[`.ci/versions.yaml`](../../.ci/versions.yaml) under `validation`:

| Component | Qualified version |
| --- | --- |
| OpenBao | `2.6.4` |
| GitLab CE | `19.5.1` |
| Kubernetes | `kindest/node:v1.35.0`, created with kind `v0.31.0` |

The end-to-end lanes in [`mk/e2e.mk`](../../mk/e2e.mk) run against these
pins. AWS Secrets Manager is a managed service with no pinned version; the
local lane uses LocalStack, described in
[the LocalStack e2e README](../../test/e2e/localstack/README.md). Other server
versions are unqualified until their test lanes pass.

## Preview stability

Before a stable release, APIs, response fields, storage records, provider
behavior and metadata, defaults, and operational guidance can change. Do not
treat a preview build as an API, storage, upgrade, downgrade, or migration
boundary.

The plugin records its storage schema and fails closed with an
`incompatible storage schema` error before any request or background work runs
when the stored schema is incompatible with the running binary, for example
when it requires a newer plugin. See
[Storage model](../development/backend/storage-model.md) for the schema policy.
