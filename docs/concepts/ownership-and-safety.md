# Ownership And Safety

Secret Sync treats OpenBao as the source of truth only for remote objects it can
prove it owns. It does not force-overwrite existing or foreign provider objects.

This safety model is why a remote object with stale, missing, or mismatched
metadata can block sync even when the local association is valid.

## Ownership Metadata

Provider writes include ownership metadata where the destination supports it.
The exact carrier is provider-specific:

- AWS Secrets Manager stores ownership in tags.
- Kubernetes Secrets store ownership in labels and annotations.
- GitLab project variables store ownership in the variable description.

The logical ownership record includes the fields each provider can carry:

```text
managed=<true>
plugin_instance=<plugin-instance-id>
restore_epoch=<restore-epoch>
association_id=<association-id>
source_path=<source-path>
source_version=<source-version>
object_id=<object-id>
payload_sha256=<hash>
```

The carrier and exact spelling are provider-specific. AWS uses
`openbao-sync-*` tags, Kubernetes uses `openbao.org/secrets-sync-*` labels and
annotations, and GitLab encodes the fields in a human-readable variable
description. See the provider operations page for the exact remote contract.

Providers verify this metadata before owned updates and deletes. Missing or
mismatched ownership is treated as ownership loss, not as permission to take
over the object.

## Runtime Identity

Each Secret Sync mount has a plugin instance ID and restore epoch. Providers
include these values in ownership metadata where possible.

The plugin instance identifies the mount that created the remote object. The
restore epoch distinguishes a restored or cloned mount from the earlier mount
state. When restore guard is acknowledged, the restore epoch rotates so future
provider writes carry a new reviewed epoch.

## Object States

Status and reconcile report different states depending on the remote evidence:

- `SYNCED`: the remote object is owned and matches the current OpenBao source
  version.
- `DRIFTED`: the remote object is owned, but the remote payload differs from
  the current OpenBao source version.
- `REMOTE_MISSING`: the expected remote object does not exist.
- `REMOTE_OWNERSHIP_LOST`: a remote object exists, but it is not owned by the
  current mount, association, object, or restore epoch.
- `UNKNOWN`: the provider cannot supply enough evidence to compare or prove
  state.

`DRIFTED` is repairable because ownership is known. `REMOTE_OWNERSHIP_LOST` is
not repaired automatically because that would be a takeover of a foreign or
stale object.

## Collisions And Stale Objects

When a planned create finds an existing remote object, the provider must decide
whether it is owned:

- if ownership matches, the operation can be an update or no-op;
- if ownership is absent or mismatched, the operation reports ownership loss or
  collision and does not overwrite;
- if the object was deleted and the provider keeps a recoverable tombstone, the
  provider may restore only when the tombstone is still owned by the same
  association.

For stale objects from a torn-down or restored environment, inspect or remove
the remote object first. After the remote side is ready, run the `manual_sync`
action returned by status or reconcile so OpenBao recreates the object from the
current source version.

## Deletes

Association `delete_mode` defaults to `retain`. Remote delete is enqueued only
when `delete_mode=delete`.

Provider delete must prove ownership before mutating the remote object. Missing
owned objects are treated idempotently. Provider-specific delete behavior is
documented in the provider operations pages.

## Drift Repair

Background `drift_repair=detect` only refreshes local status from provider
read-state checks. `drift_repair=repair` enqueues normal outbox work for owned
`DRIFTED` objects.

Repair never writes providers directly from the background sweep. It uses the
same queue, restore guard, disabled, destination policy, capability, ownership,
and provider checks as user-triggered sync.

Use [Runtime configuration](../operations/runtime-configuration.md) for drift
settings and [Operator runbook](../operations/operator-runbook.md) for recovery
commands.

## Restore Guard

Restore guard blocks remote mutation until an operator reviews local and remote
state. Manual reconcile remains available because it only reads provider state
and updates local status.

Acknowledging restore guard is consent to resume remote mutation with the
current local source versions. If `drift_repair=repair` is enabled, it also
allows future background repair work to enqueue owned drift repairs.
