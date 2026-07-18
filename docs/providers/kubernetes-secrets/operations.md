# Kubernetes Secrets operations

## Required permissions

Grant the destination identity namespace-scoped access to Kubernetes Secrets in
only the namespaces that are approved as sync destinations:

```yaml
apiGroups: [""]
resources: ["secrets"]
verbs: ["get", "create", "update", "delete"]
```

The provider uses `get` for health, plan, and read-state checks, `create` and
`update` for upserts, and `delete` for owned deletes. Health checks probe a
fixed Secret name and treat an authorized not-found response as healthy, so the
destination identity does not require `list` access that would expose every
Secret value in the namespace.

## Ownership and delete behavior

The provider writes an ownership label and annotations that include the
association ID, source path, source version, object ID, payload hash, payload
format, data keys, OpenBao mount UUID, and restore epoch. Owned update and
delete operations require matching ownership metadata. If ownership cannot be
proven, the provider returns an ownership error instead of mutating the Secret.

The exact Kubernetes metadata contract is:

| Carrier | Key | Value |
| --- | --- | --- |
| Label | `openbao.org/secrets-sync-managed` | `true` |
| Annotation | `openbao.org/secrets-sync-association-id` | Association ID |
| Annotation | `openbao.org/secrets-sync-source-path` | OpenBao source path |
| Annotation | `openbao.org/secrets-sync-source-version` | OpenBao source version |
| Annotation | `openbao.org/secrets-sync-object-id` | Association object ID |
| Annotation | `openbao.org/secrets-sync-payload-sha256` | Canonical payload hash |
| Annotation | `openbao.org/secrets-sync-format` | Rendered payload format |
| Annotation | `openbao.org/secrets-sync-data-keys` | Managed keys for `source-keys` data maps |
| Annotation | `openbao.org/secrets-sync-mount-uuid` | OpenBao-provided mount UUID |
| Annotation | `openbao.org/secrets-sync-restore-epoch` | Restore epoch |

These labels and annotations are plaintext Kubernetes object metadata. They do
not contain the source value, but they expose source naming, version, object,
and runtime identity information to identities that can read the Secret.
Avoid sensitive information in source and remote names, and scope Kubernetes
Secret read permissions accordingly.

Plan, upsert no-op detection, and reconcile compute the payload hash from live
Kubernetes Secret data. Manual data edits are detected even when the ownership
annotations still contain the previous payload hash, and the next explicit sync
or background `drift_repair=repair` pass repairs owned drift.

With `data_mapping=source-keys`, the provider manages only the rendered data
keys recorded in ownership metadata and preserves unrelated data keys where
possible. A desired managed key does not overwrite an unmanaged existing key.
For default `data_mapping=payload`, owned updates replace the Secret data with
only the managed `payload` key.

Remote delete is sent only when the association uses `delete_mode=delete`.
Missing owned Secrets are treated idempotently. For `source-keys` data maps,
delete removes only managed keys when unrelated data keys remain; the provider
removes ownership metadata and leaves the foreign keys in place. If no data
keys remain, the provider deletes the Secret.

Immutable Kubernetes Secrets block updates and report a validation failure.

After an operator resolves remote ownership conflicts, use the `manual_sync`
action returned by status or reconcile to enqueue the current OpenBao source
version.

## E2E test path

Use [kind e2e](../../../test/e2e/kind/README.md) to test Kubernetes Secrets in
a disposable kind cluster.
