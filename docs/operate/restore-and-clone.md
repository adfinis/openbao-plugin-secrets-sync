---
verifiedBy:
  - internal/backend/path_config.go
  - internal/backend/backend.go
  - internal/backend/dispatcher.go
  - internal/backend/event_dispatch.go
  - internal/backend/periodic_reconcile.go
  - internal/backend/path_queue.go
  - internal/backend/path_reconcile.go
  - internal/backend/runtime_state.go
  - internal/providers/provider.go
  - test/e2e/resilience/restart_resilience_e2e_test.go
---

# Review a restore or clone

Run this procedure after you restore OpenBao storage, clone an environment, or
start a mount whose local Secret Sync state might no longer match its
destinations. It keeps the restored instance from overwriting production
destinations until you have reviewed its state.

Secret Sync does not detect a snapshot restore or a clone. The mount
configuration, the queue, and the restore epoch are stored in OpenBao storage,
so they come back with the snapshot. A snapshot can contain
`restore_guard=false` and `disabled=false`, and changing these fields in the
current timeline does not change an earlier or different snapshot. A restored
or cloned mount can start dispatching as soon as it initializes, and it still
carries the ownership identity of the live remote objects.

> [!WARNING]
> **Block destination writes from the affected instances before they start or
> receive restored storage.** Use a network or provider authorization control
> that does not depend on the restored OpenBao storage, and keep it in place
> until ownership remediation is complete. The restore guard and the pause
> setting are restored with the snapshot and cannot provide this boundary.

## Fence destinations

1. Identify the restored or cloned instances and every destination they can
   reach, including credentials and files restored with the environment.
2. Deny destination writes from those instances before you start them or
   restore storage into a running cluster. Use network isolation, a provider
   write-denial policy, or isolated test destinations. Apply the control
   outside the snapshot and scope it to the affected instances.
3. Verify the control from the affected execution environment. A configuration
   record in OpenBao is not evidence that destination access is blocked.
   Before an in-place restore, stop local writes and account for provider
   requests already in flight. Pause and guard settings do not cancel accepted
   provider requests or undo completed remote changes.
4. Start or restore the fenced environment. Arm the guard and pause each Secret
   Sync mount before you grant any provider access:

   ```sh
   bao write secret-sync/config restore_guard=true disabled=true
   bao read secret-sync/config
   ```

   Both `restore_guard` and `disabled` must read `true`. Keep the external
   fence active.
5. If remote inspection needs provider access, grant only the reads needed for
   health checks and reconcile. Keep writes denied during the review. Keep a
   clone isolated from production destinations unless you have reviewed its
   ownership and destination configuration for that use.

With both settings active, the mount runs no background provider work, while
manual reconcile and destination checks still read provider state when the
fence allows it. [Mutation gates](../concepts/ownership-and-drift.md#mutation-gates)
compares what each setting blocks. Setting `disabled=true` does not stop a
reconcile pass or provider request that is already running.

Do not acknowledge the guard yet.

## Inventory local state

6. List source paths and associations:

   ```sh
   bao list secret-sync/metadata
   bao list secret-sync/associations
   ```

7. For each important source path, read its metadata, associations, and
   status, then read the queue summary:

   ```sh
   bao read secret-sync/metadata/<path>
   bao read secret-sync/associations/<path>
   bao read secret-sync/status/<path>
   bao read secret-sync/queue
   ```

8. Inspect each queued operation by the IDs in the status `operation_ids`:

   ```sh
   bao read secret-sync/queue/<operation-id>
   ```

9. If an association shape is unclear, read the static defaults and provider
   capabilities:

   ```sh
   bao read secret-sync/info
   ```

## Review remote state

10. Read the reconcile plan for each important source path. It reads provider
    state and changes neither local status nor destination secrets, so it is
    safe while the guard is active:

    ```sh
    bao read secret-sync/reconcile/<path>/plan
    ```

11. Check the `verification` value of each object. `metadata` verification
    compares ownership and payload hash metadata, not the live value; see
    [Verification values](../reference/status-and-diagnostics.md#verification-values).
12. Inspect the destination-side ownership metadata whenever the plan reports
    ownership loss, a collision, drift, a missing object, metadata-only
    verification, or an unexpected source version. Each provider page lists
    the metadata keys under `Ownership metadata`:
    [AWS Secrets Manager](../configure/aws-secrets-manager.md#ownership-metadata),
    [Kubernetes Secrets](../configure/kubernetes-secrets.md#ownership-metadata),
    [GitLab project variables](../configure/gitlab-project-variables.md#ownership-metadata).

## Decide queued work

13. Decide for each queued operation whether to keep, cancel, or retry it.

    Keep it when the source version is still the intended source of truth, the
    association still owns the remote object, and the destination still allows
    the source path and resolved name.

    Cancel it when the source version is stale after the restore, the remote
    object is now managed elsewhere, or the association no longer needs to
    change the destination:

    ```sh
    bao write -force secret-sync/queue/<operation-id>/cancel
    ```

    Retry it only after you understand the destination configuration, source
    eligibility, association, and remote ownership:

    ```sh
    bao write -force secret-sync/queue/<operation-id>/retry
    ```

    To push the current source version after review, prefer manual sync over
    retry:

    ```sh
    bao write secret-sync/associations/<path>/sync destination=<type>/<name>
    ```

    Queued work, including manual sync, stays queued until the guard is
    acknowledged and the mount is resumed.

14. To store reviewed reconcile results as local status, apply reconcile. It
    updates local status only and never writes destination secrets:

    ```sh
    bao write -force secret-sync/reconcile/<path>
    ```

## Resume remote mutation

15. Decide whether background repair may run. With `drift_repair=repair`,
    acknowledgement lets the next background sweep queue repairs for owned
    drift. Set `drift_repair=detect` or `drift_repair=off` first if the
    restored source versions must not yet overwrite destinations. See
    [Enable drift detection or repair](../configure/runtime.md#enable-drift-detection-or-repair).

16. Acknowledge the guard. Keep `disabled=true` and the external fence:

    ```sh
    bao write -force secret-sync/config/restore-guard/acknowledge
    ```

    The response reports `restore_guard` `false`, `disabled` `true`, and a new
    `restore_epoch`. Writing `restore_guard=false` to `config` on a guarded
    mount has the same effect.

    Acknowledgement rotates the restore epoch but does not update ownership
    metadata on existing remote objects. Objects written under the previous
    epoch can then report `REMOTE_OWNERSHIP_LOST`, and the plugin does not
    adopt them. See
    [Runtime identity and restore epoch](../concepts/ownership-and-drift.md#runtime-identity-and-restore-epoch).

17. Read the reconcile plans again. A plan from before acknowledgement does not
    prove ownership after the epoch rotation.

18. For each object with stale ownership, choose a reviewed recovery:
    - Leave the remote object with its current manager and cancel local work
      that would change it.
    - Configure an unused remote name or an isolated destination, then sync the
      reviewed source version to create a new owned object.
    - To recreate the object at the same name, preserve the remote data you
      need and get approval from the destination owner before you remove the
      stale object. Follow the provider page under `Drift and delete behavior`
      first. Then run manual sync to recreate it from the reviewed source.

    There is no endpoint that adopts a remote object or rewrites its ownership
    metadata. Do not retry ownership-lost work expecting it to bypass the
    ownership check.

19. Remove the external fence for the reviewed destinations while the mount is
    still disabled.

20. To catch up in manual batches, disable event dispatch before you resume.
    Periodic dispatch still runs, so this is not an exclusive drain window:

    ```sh
    bao write secret-sync/config event_dispatch_enabled=false
    ```

21. Resume the mount. Event-triggered dispatch can start immediately if it is
    enabled:

    ```sh
    bao write secret-sync/config disabled=false
    ```

22. Drain due work in small batches and read the status after each batch:

    ```sh
    bao write secret-sync/queue/drain max_operations=10
    bao read secret-sync/status/<path>
    ```

    Stop and plan again if the status reports `REMOTE_OWNERSHIP_LOST`,
    unexpected `DRIFTED` objects, validation errors, or queue-blocked
    diagnostics.

## Evidence to keep

For audit or incident review, keep:

- restore or clone timestamp;
- external fencing method, verification, scope, and removal time;
- plugin version and mount path;
- `restore_guard` and `disabled` values before acknowledgement;
- reviewed source paths and source versions;
- association IDs and destination names;
- reconcile plan output, without secret payloads;
- verification values reported by reconcile or status;
- queue operation IDs and the action taken on each;
- acknowledgement time and the new `restore_epoch`;
- ownership remediation decisions after the epoch rotation; and
- destination-side ownership metadata.

Do not store source secret values, destination credentials, tokens, raw audit
records that contain secrets, or full provider responses that contain secret
data.
