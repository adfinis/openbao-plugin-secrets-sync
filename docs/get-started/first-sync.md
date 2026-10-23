---
verifiedBy:
  - internal/backend/path_data.go
  - internal/backend/path_sources.go
  - internal/backend/path_destinations.go
  - internal/backend/association_plan.go
  - internal/backend/path_associations.go
  - internal/backend/path_status.go
  - internal/backend/path_reconcile.go
  - internal/providers/provider.go
  - test/e2e/localstack/secret_sync_e2e_test.go
---

# Sync your first secret

Write a source secret into the Secret Sync mount, connect it to one
destination, and confirm that the destination holds the value.

## Prerequisites

- The engine is mounted at `secret-sync/`. See
  [Mount the engine](install.md#mount-the-engine).
- You have destination credentials and permissions ready, as described under
  `Choose an auth mode` and `Grant permissions` on the provider page:
  [AWS Secrets Manager](../configure/aws-secrets-manager.md),
  [Kubernetes Secrets](../configure/kubernetes-secrets.md), or
  [GitLab project variables](../configure/gitlab-project-variables.md).
- Your OpenBao token can write source data, destinations, and associations, and
  read status. The [platform operator policy](../security/policies.md#platform-operator)
  covers these paths.
- The mount uses the default `standard` security posture. In `hardened`
  posture you must also [enable source sync](../configure/delegate.md#enable-source-sync)
  and [constrain the destination](../configure/delegate.md#constrain-destinations).

The examples use the source path `app/db`. Replace `<type>/<name>` with your
destination, where `<type>` is `aws-sm`, `k8s`, or `gitlab`, and
`<remote-name>` with the name of the object to create at the destination.

## Procedure

1. Write the source secret. In CLI shorthand, each top-level field becomes a
   key in the source payload:

   ```sh
   bao write secret-sync/data/app/db username=app password=initial
   ```

   The response `metadata` reports `version` `1` and `sync_state`
   `NO_ASSOCIATION`. For the wrapped request form and check-and-set, see
   [Source write forms](../reference/api.md#source-write-forms).

2. Confirm the source is ready to sync:

   ```sh
   bao read -field=ready secret-sync/sources/app/db/check
   ```

   The command prints `true`. If it prints `false`, read the full response and
   resolve each entry in `blockers`.

3. Configure the destination at `secret-sync/destinations/<type>/<name>` as
   described on the provider page. Then check it:

   ```sh
   bao read secret-sync/destinations/<type>/<name>/check
   ```

   The response reports `ready` `true` and an empty `blockers` list. If not,
   see [Check a destination](../operate/troubleshoot.md#check-a-destination).

4. Plan the association. Planning reads remote metadata where the provider
   supports it and never changes the destination. Set `resolved_name` to a name
   the provider accepts; see
   [Name remote objects](../configure/associations.md#name-remote-objects):

   ```sh
   bao write -field=action secret-sync/associations/app/db/plan \
     destination=<type>/<name> \
     resolved_name=<remote-name>
   ```

   The command prints `create`. Stop if it prints anything else. `update` or
   `conflict` means an object already exists at that name, and `blocked` means
   the provider cannot plan a safe write. Read the full plan response, choose
   another name if needed, or read
   [Collisions and stale objects](../concepts/ownership-and-drift.md#collisions-and-stale-objects)
   before you continue.

5. Create the association with the same parameters:

   ```sh
   bao write secret-sync/associations/app/db \
     destination=<type>/<name> \
     resolved_name=<remote-name>
   ```

   The response lists one ID in `sync_operation_ids`. The ID means OpenBao
   stored the association and queued provider work. It does not mean the
   destination has the value yet; see
   [Accepted is not synced](../concepts/sync-model.md#accepted-is-not-synced).
   The defaults sync the whole source as one JSON object and keep the remote
   object when the source is deleted. To choose another shape, see
   [Choose a secret shape](../configure/associations.md#choose-a-secret-shape).

6. Check the sync status:

   ```sh
   bao read -format=json secret-sync/status/app/db \
     | jq '.data | {state, version, objects: [.objects[] | {destination_ref, resolved_name, state}]}'
   ```

   Expected output:

   ```json
   {
     "state": "SYNCED",
     "version": 1,
     "objects": [
       {
         "destination_ref": "<type>/<name>",
         "resolved_name": "<remote-name>",
         "state": "SYNCED"
       }
     ]
   }
   ```

   `PENDING` means the operation is still queued; read the status again after
   a few seconds. Any other state means the sync is blocked or failed. Follow
   the object's `hint` and `next_actions`, then
   [Route by status](../operate/troubleshoot.md#route-by-status).

7. Confirm the result against the destination. Reconcile plan reads the remote
   object and changes nothing:

   ```sh
   bao read -field=state secret-sync/reconcile/app/db/plan
   ```

   The command prints `SYNCED`.

## Update or delete the source

Write a new version to sync it to every enabled association of the path:

```sh
bao write secret-sync/data/app/db username=app password=updated
```

The status reports `version` `2`, then `SYNCED` once the provider work
completes.

Delete the latest source version:

```sh
bao delete secret-sync/data/app/db
```

With the default `delete_mode=retain`, the remote object and its ownership
metadata stay at the destination. Only associations with `delete_mode=delete`
queue a remote delete, and providers delete differently. See
[Choose a delete mode](../configure/associations.md#choose-a-delete-mode) and
the provider page before you rely on remote deletion.

Deleting an association removes its queued work but leaves the remote object in
place.

## Next steps

- [Configure associations](../configure/associations.md) for other secret
  shapes, name templates, and delete modes.
- [Delegate to application owners](../configure/delegate.md) to let teams
  manage their own sources and associations.
- [Runtime settings](../configure/runtime.md) for pause, queue capacity, and
  drift detection.
- [Troubleshoot sync](../operate/troubleshoot.md) when a sync does not reach
  `SYNCED`.
