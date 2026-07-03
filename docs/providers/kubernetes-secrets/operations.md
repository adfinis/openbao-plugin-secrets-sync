# Kubernetes Secrets operations

## Required permissions

Grant the destination identity namespace-scoped access to Kubernetes Secrets in
only the namespaces that are approved as sync destinations:

```yaml
apiGroups: [""]
resources: ["secrets"]
verbs: ["get", "list", "create", "update", "delete"]
```

The provider uses `list` for health checks, `get` for plan and read-state
checks, `create` and `update` for upserts, and `delete` for owned deletes.

## Ownership and delete behavior

The provider writes an ownership label and annotations that include the
association ID, source path, source version, object ID, payload hash, payload
format, data keys, plugin instance, and restore epoch. Owned update and delete
operations require matching ownership metadata. If ownership cannot be proven,
the provider returns an ownership error instead of mutating the Secret.

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
