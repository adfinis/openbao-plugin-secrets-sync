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
versions require separate validation. See the additional validation below.

## Storage transactions

Clearing an active restore guard requires `logical.TransactionalStorage` at
runtime. The plugin checks the storage interface supplied by OpenBao. It does
not infer transaction support from the server version or backend name.

OpenBao 2.6.4 exposes transactions for Raft and PostgreSQL storage, and for
ordinary in-memory development storage unless transactions are disabled.
The deprecated `file` backend does not expose transactions. A mount without
transactions can arm its restore guard but cannot clear it. Use transactional
storage before relying on the restore acknowledgement workflow. See
[Review a restore or clone](../operate/restore-and-clone.md#resume-remote-mutation).

PostgreSQL transaction support has a server-side limitation in OpenBao 2.6.4
and 2.7.1: transactions started by external plugins are rolled back when the
`BeginTx` RPC ends. Secret Sync reproduces HTTP 500 with
`sql: transaction has already been committed or rolled back` on a config
write. These server versions remain unqualified with PostgreSQL storage.
See [OpenBao issue #4176](https://github.com/openbao/openbao/issues/4176).
The fix is merged in
[OpenBao PR #4177](https://github.com/openbao/openbao/pull/4177) and backported
to `release/2.7.x` in
[PR #4248](https://github.com/openbao/openbao/pull/4248), but is absent from
2.7.1. Validate a server release containing the fix before qualifying
PostgreSQL storage.

## Additional server validation

OpenBao 2.7.1 passes the following checks on Linux `arm64` with plugin source
commit `adf1b7ab7c07fd42409457cb4c36cd3c3b4dab5a`, validated on 2026-10-23:

| Check | Storage | Result |
| --- | --- | --- |
| `make test-e2e` | In-memory | Pass: LocalStack synchronization and create/update ACLs. |
| `make test-e2e-kind` | In-memory | Pass: Kubernetes Secret synchronization, policy failures, ownership loss, and immutable Secrets. |
| `make test-e2e-resilience` | Three-node Raft | Pass: restart, leader loss, seal/unseal, and persistent state. |
| Both restore-guard clearing paths over the HTTP API | In-memory, Raft, PebbleDB | Pass: clearing an active guard rotates the epoch once; repeated acknowledgement preserves it. |
| Both restore-guard clearing paths with transactions disabled | In-memory | Pass: HTTP 400, with config and epoch unchanged. |

The server image is pinned to:

```text
ghcr.io/openbao/openbao:2.7.1@sha256:6d2b93856e3fcf7b18ad855a0b51eaba474dc8b79cf554379ea32034797d2acf
```

The Kubernetes run uses the existing kind `v0.31.0`, kubectl `v1.35.3`, and
`kindest/node:v1.35.0` pins. The PostgreSQL check uses PostgreSQL 18.6 and
reproduces the server-side failure described above on both OpenBao 2.6.4 and
2.7.1.

These results cover the tested patch version and storage setups. The default
CI baseline remains OpenBao 2.6.4; OpenBao 2.7.1 is an additional manual
validation target.

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
