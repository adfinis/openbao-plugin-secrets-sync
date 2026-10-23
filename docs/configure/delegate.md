---
verifiedBy:
  - internal/backend/path_config.go
  - internal/backend/path_sources.go
  - internal/backend/path_destinations.go
  - internal/backend/association_policy.go
  - internal/backend/association_plan.go
  - internal/backend/path_associations.go
  - internal/backend/path_data.go
  - internal/backend/path_reconcile.go
  - internal/backend/dispatcher.go
  - internal/backend/storage_records.go
  - internal/backend/path_sources_test.go
  - internal/backend/dispatcher_test.go
  - internal/backend/path_destinations_test.go
---

# Delegate to application owners

Set `security_posture=hardened` before you let application owners manage
associations for their own source paths. Hardened posture requires every
destination to restrict which source paths and remote names may use it, and
requires each source path to opt in to sync. A delegated owner then cannot
sync unrelated sources or write arbitrary remote names through a shared
destination.

Fresh mounts use `security_posture=standard`. That posture assumes a trusted
platform operator owns both destinations and associations.

| Behavior | `standard` | `hardened` |
| --- | --- | --- |
| Destination write without both constraint lists | Accepted | Rejected |
| Use of a destination that lacks either list | Allowed | Blocked with `destination_unconstrained` |
| Constraint lists that are set | Enforced | Enforced |
| Source sync enablement before remote mutation | Not required | Required |

> [!WARNING]
> Do not grant delegated owners write access to `destinations/*`. Destination
> write access plus association write access is full exfiltration authority,
> and destination fields control the plugin's network targets, credential
> files, and ambient credentials. See
> [Authorization boundaries](../security/threat-model.md#authorization-boundaries).

## Enable hardened posture

For a new mount, set the posture before you create destinations so that every
destination write must carry constraints. For a mount that already syncs, use
[Move an active mount to hardened posture](#move-an-active-mount-to-hardened-posture)
instead.

1. Set the posture:

   ```sh
   bao write secret-sync/config security_posture=hardened
   ```

   The config write validates only the value. It does not check existing
   destinations.

2. Confirm the posture:

   ```sh
   bao read -field=security_posture secret-sync/config
   ```

   The output is `hardened`.

3. Create each destination with both constraint lists, as described in
   [Constrain destinations](#constrain-destinations).
4. Grant the
   [delegated association owner policy](../security/policies.md#delegated-association-owner)
   and the source owner policy that allows
   [source sync enablement](#enable-source-sync).

Setting `security_posture=standard` again relaxes the posture checks for later
requests and dispatch. Operations that already failed stay failed until you
retry them. See
[Manage queued operations](../operate/troubleshoot.md#manage-queued-operations).

## Constrain destinations

Add both constraint fields when you create or update a destination. This
fragment omits the provider fields. Replace `<type>` and `<name>` with the
destination type and name:

```sh
bao write secret-sync/destinations/<type>/<name> \
  allowed_source_path_prefixes=apps/team-a,shared/team-a \
  allowed_resolved_name_prefixes=team-a/
```

On an update, omitted fields keep their stored values, so you can add the
constraints to an existing destination without repeating its provider config
or token. Each field accepts a comma-separated string or a list. An empty value
clears the list, which hardened posture rejects.

`allowed_source_path_prefixes` matches whole path segments. The backend
normalizes each prefix like a source path. A prefix matches the same path or
any path below it: `apps/team-a` allows `apps/team-a` and `apps/team-a/db`, but
not `apps/team-alpha/db`.

`allowed_resolved_name_prefixes` matches whole `/`-separated name segments. The
backend ignores a leading `/` on prefixes and names and a trailing `/` on
prefixes. `team-a/` allows `team-a` and `team-a/db`, but not `team-alpha/db`.
Every resolved name of the association must match, including each per-key name
of a `secret-key` association.

Set lists are enforced in both postures. An empty list restricts nothing in
standard posture. In hardened posture, a destination with either list empty
reports `destination_unconstrained`.

The backend checks the constraints at each point where a source or remote name
meets a destination:

| Request or work | Source sync enablement (hardened) | Destination constraints |
| --- | --- | --- |
| `associations/<path>/plan` | Reported as a blocked plan action | Reported as a blocked plan action |
| Association create or update | Checked for enabled associations | Checked, including for disabled associations |
| Association enable | Checked | Checked |
| Association manual sync | Checked | Checked |
| Source write to `data/<path>` | Checked; the version is stored but no work is queued | Checked at dispatch |
| `sources/<path>/enable` | Not applicable | Checked at dispatch |
| Manual reconcile and background drift read-state | Not checked | Checked before the provider read; a violation records `VALIDATION_ERROR` |
| Queued dispatch of upserts and deletes | Checked | Checked; a violation fails the operation terminally |

Because dispatch and read-state recheck the destination, tightening a
destination after work is queued still blocks that work.

Check a destination's readiness after you change its constraints or the
posture:

```sh
bao read secret-sync/destinations/<type>/<name>/check
```

The `blockers` list contains `destination_unconstrained` when hardened posture
is active and either list is empty.

## Enable source sync

In hardened posture, an enabled association can queue or dispatch remote
mutation only after its source path opts in. The opt-in is a plugin-owned flag
in the source metadata. It is not a check that the caller can read the source
value.

1. Enable source sync for the path. Replace `<path>` with the source path:

   ```sh
   bao write -force secret-sync/sources/<path>/enable
   ```

   When the flag changes, the request also queues the current source version
   for enabled associations with active destinations. The response returns
   `sync_operation_ids` and `sync_state`. If the queue cannot admit every
   operation, the request fails and source sync stays disabled.

2. Check source readiness:

   ```sh
   bao read secret-sync/sources/<path>/check
   ```

   `ready` is `true` and `blockers` is empty. `source_sync_not_enabled` appears
   only when hardened posture is active and the path has not opted in.

The enable endpoint also works in standard posture, where the flag has no
effect until the mount switches to hardened.

When hardened posture is active and the path has not opted in:

- A source write still stores the new version. The response metadata reports
  `sync_state=VALIDATION_ERROR`, no `sync_operation_ids`, and an
  `enable_source` action in `next_actions`.
- Creating, enabling, or manually syncing an enabled association fails.

To stop sync for a path, disable it:

```sh
bao write -force secret-sync/sources/<path>/disable
```

In hardened posture, later source writes queue nothing, and queued upserts and
deletes for the path fail terminally at dispatch. Disabling does not delete or
change existing remote objects.

OpenBao policy on `sources/<path>/enable` and `sources/<path>/disable` decides
who can opt a source in. Grant it to the source owner, not to the delegated
association owner, when you want both parties to agree before a secret leaves
OpenBao.

## Move an active mount to hardened posture

Switching posture takes effect immediately for queued work. Constrain
destinations first so that queued work through them keeps flowing.

1. List the destinations for each provider type, such as `aws-sm`, `gitlab`,
   or `k8s`:

   ```sh
   bao list secret-sync/destinations/<type>
   ```

2. Add both constraint lists to every destination, as described in
   [Constrain destinations](#constrain-destinations).
3. Confirm that existing associations still fit the constraints. For each
   synced source path, read the reconcile plan:

   ```sh
   bao read secret-sync/reconcile/<path>/plan
   ```

   An object that violates a constraint reports `VALIDATION_ERROR` with a
   message naming the source path or resolved name. This request reads
   provider state but does not change local status or remote objects.

4. Set the posture:

   ```sh
   bao write secret-sync/config security_posture=hardened
   ```

5. Enable source sync for each source path that has enabled associations:

   ```sh
   bao write -force secret-sync/sources/<path>/enable
   ```

   Queued operations for a path that has not opted in fail terminally at
   dispatch, and source writes to it queue nothing. Enabling the path queues
   its current version again, so those sources converge under the hardened
   policy.

6. Check each destination and source with the `check` requests above, then
   confirm convergence in `status/<path>`.

Because the enable endpoint also works in standard posture, you can run step 5
before step 4 to avoid the window in which writes and queued work fail. Each
enable queues the current version again.

## Split responsibilities

Separate these privileges unless one team intentionally owns the whole
workflow:

- source payload writes under `data/<path>`;
- source metadata writes and source sync enable and disable;
- association create, update, and delete for the delegated source prefix;
- destination management; and
- queue drain, queue retry, restore guard acknowledgement, and runtime config.

Delegated association owners usually need association access for their source
prefix and read access to source checks, status, and reconcile plans. They do
not need destination write access or queue mutation.
[Policy examples](../security/policies.md) shows a policy for each role.
