---
verifiedBy:
  - internal/backend/path_config.go
  - internal/backend/path_status.go
  - internal/backend/diagnostics.go
  - internal/backend/dispatcher.go
  - internal/backend/path_queue.go
  - internal/backend/storage_outbox.go
  - internal/backend/backend.go
  - internal/backend/path_destinations.go
  - internal/backend/path_reconcile.go
  - internal/backend/path_associations.go
  - test/e2e/resilience/restart_resilience_e2e_test.go
---

# Troubleshoot sync

Start with the response. Blocked and failed responses carry a `hint` and a
list of `next_actions`, each with a ready-to-run `bao` command. Check the
action's `mutates_remote` flag before you run it.
[Hints and next actions](../reference/status-and-diagnostics.md#hints-and-next-actions)
describes these fields. The sections below cover what to check when the
suggested action is not enough.

The examples assume the mount path `secret-sync/`. Replace `<path>` with the
source path and `<type>/<name>` with the destination.

## Check the mount

Read the mount configuration:

```sh
bao read secret-sync/config
```

Check these fields first:

- `restore_guard` `true`: the mount blocks remote mutation after a restore or
  clone. Do not acknowledge the guard to unblock sync. Acknowledgement rotates
  the restore epoch and can break ownership of existing remote objects. Follow
  [Review a restore or clone](restore-and-clone.md) instead.
- `disabled` `true`: remote mutation is paused for the whole mount. Find out
  who paused it and why before you resume with
  `bao write secret-sync/config disabled=false`. See
  [Pause the mount](../configure/runtime.md#pause-the-mount).
- `security_posture` `hardened`: sources must have source sync enabled and
  destinations must have prefix constraints. See
  [Delegate to application owners](../configure/delegate.md).
- `queue_capacity`: compare it with the queue depth in
  [Manage queued operations](#manage-queued-operations).

## Route by status

Read the status of the source path:

```sh
bao read -format=json secret-sync/status/<path> | jq .data
```

The top-level `state` summarizes the path. Each entry in `objects` has its own
`state`, `last_error_class`, `last_error`, `hint`, and `next_actions`. State
meanings and retry behavior are defined in
[Sync states](../reference/status-and-diagnostics.md#sync-states) and
[Error classes and retry](../reference/status-and-diagnostics.md#error-classes-and-retry).

| State | Check next | Recovery |
| --- | --- | --- |
| `UNKNOWN` | The path spelling, and `bao read secret-sync/metadata/<path>`. | Write the source data. |
| `NO_ASSOCIATION` | `bao read secret-sync/associations/<path>`. | Create an association. |
| `PENDING` | Queue summary, and `disabled`, `restore_guard`, and `event_dispatch_enabled` in config. | Wait for dispatch, resolve the pause or guard, or drain the queue. |
| `DRIFTED` | Reconcile plan and the remote ownership metadata. Collisions with foreign objects also report `DRIFTED`. | Manual sync for owned drift. For a foreign object, see [Recover ownership loss](#recover-ownership-loss). |
| `REMOTE_MISSING` | Reconcile plan. | Manual sync recreates the object. |
| `REMOTE_OWNERSHIP_LOST` | The remote ownership metadata. | [Recover ownership loss](#recover-ownership-loss). |
| `DESTINATION_AUTH_ERROR` | Destination health, credential material, workload identity. | Fix or rotate the credential, then retry the operation. |
| `DESTINATION_POLICY_ERROR` | IAM, RBAC, token scopes, or project permissions for create, update, read, and delete. | Fix the permissions, then retry the operation. |
| `DESTINATION_RATE_LIMITED` | `retry_wait` in the queue summary. | Let automatic retry continue. Use smaller drain batches. |
| `DESTINATION_UNAVAILABLE` | Destination health, DNS, proxy, and network path. | Retry the operation after the destination recovers. |
| `VALIDATION_ERROR` | `last_error`, the association plan, destination config, remote name rules, payload size, and hardened posture requirements. | Fix the cause, then run manual sync. |
| `QUEUE_BLOCKED` | Payload size against the provider limit on the provider page. The state's built-in hint suggests queue actions, which do not help here. | Reduce the payload or split the source path, then run manual sync. |
| `DISABLED` | Whether the association or the destination is disabled. | Enable the disabled object. |
| `INTERNAL_ERROR` | Plugin logs in the OpenBao server log. | Fix the cause, then retry the operation. |

Run these recovery commands after you resolve the cause:

```sh
# Manual sync pushes the current source version for one association.
bao write secret-sync/associations/<path>/sync destination=<type>/<name>

# Retry moves a failed operation back to pending.
bao write -force secret-sync/queue/<operation-id>/retry

# Plan the association again after a validation fix.
bao write secret-sync/associations/<path>/plan destination=<type>/<name>
```

Run the association plan with `bao write`. The `inspect_association_plan`
action currently prints a `bao read` command, which the plan endpoint rejects.

To compare local status with the destination, read the reconcile plan. It reads
provider state and changes nothing:

```sh
bao read secret-sync/reconcile/<path>/plan
```

To store the reconcile result as local status, apply it. Apply updates local
status only and never writes destination secrets, so it is safe while the
restore guard is active:

```sh
bao write -force secret-sync/reconcile/<path>
```

## Check a destination

Read the destination configuration. Sensitive fields are returned only as
`sensitive_config` with `redacted` `true` and the configured key names:

```sh
bao read secret-sync/destinations/<type>/<name>
```

Run the checks. None of them change remote state:

```sh
bao read secret-sync/destinations/<type>/<name>/check
bao read secret-sync/destinations/<type>/<name>/validate
bao read secret-sync/destinations/<type>/<name>/health
```

- `check` combines validation and health and lists `blockers`.
- `validate` finds configuration mistakes.
- `health` finds runtime problems such as network, IAM, token, RBAC, or
  provider availability failures.

A destination can validate and still be unhealthy. In `hardened` posture,
`check` reports `destination_unconstrained` until the destination has both
source path and resolved name prefixes; see
[Constrain destinations](../configure/delegate.md#constrain-destinations).
Provider-specific checks are on each provider page under
`Check the destination`.

## Manage queued operations

Read the queue summary:

```sh
bao read secret-sync/queue
```

The summary reports `pending`, `retry_wait`, `claimed`, `terminal`,
`oldest_age_seconds`, `capacity`, and `utilization`. Sustained high
`utilization` is backpressure. Drain more often, reduce the write rate, or
raise capacity as described in
[Set queue capacity](../configure/runtime.md#set-queue-capacity).

Read one operation:

```sh
bao read secret-sync/queue/<operation-id>
```

A successful operation is removed from the queue once its status is stored. A
missing operation ID is not a failure; read `status/<path>` to confirm the
result.

Drain due operations in a controlled batch:

```sh
bao write secret-sync/queue/drain max_operations=10
```

`max_operations` defaults to 100. The response reports `processed` and the new
queue summary. Draining runs provider writes, so restrict it to operators. It
is rejected while the mount is disabled, while the restore guard is active, and
on replication nodes that cannot mutate remote state. A drain sent to an HA
standby is forwarded to the active node.

Retry or cancel one operation:

```sh
bao write -force secret-sync/queue/<operation-id>/retry
bao write -force secret-sync/queue/<operation-id>/cancel
```

- Retry moves a `retry_wait` or terminal failed operation back to pending and
  resets its attempt count. Retrying a pending operation does nothing.
- Cancel deletes a pending, `retry_wait`, or terminal failed operation. The
  record is not kept. To push the source again later, use manual sync.
- Both are rejected while a dispatcher holds a claim on the operation.

Terminal failed records are cleaned up by periodic work, including on a
disabled mount. Cleanup keeps the newest 1,000 terminal records and removes
records older than seven days. Each pass removes at most 100 excess and 100
expired records, so a large backlog takes several passes.

## Recover ownership loss

`REMOTE_OWNERSHIP_LOST` means the remote object exists but its ownership
metadata does not match this association. This happens when another system
took over the object, or after a restore guard acknowledgement rotated the
restore epoch. [Ownership metadata](../concepts/ownership-and-drift.md#ownership-metadata)
explains the check.

1. Read the remote object's ownership metadata at the destination. The
   provider page lists the keys under `Ownership metadata`.
2. Find out whether the takeover was intentional. If the mount was restored or
   cloned, follow [Review a restore or clone](restore-and-clone.md).
3. Choose one recovery:
   - Leave the object with its current manager. Disable the association and
     cancel its queued work.
   - Sync to an unused remote name or another destination.
   - With approval from the destination owner, preserve any data you need,
     remove the remote object, then run manual sync to recreate it. Follow the
     provider page under `Drift and delete behavior` first.

There is no endpoint that adopts a remote object or rewrites its ownership
metadata. Retrying the operation does not bypass the ownership check.

## Capture evidence

For troubleshooting or an issue report, capture:

- mount path and plugin version;
- destination type and redacted destination configuration;
- source path and association ID;
- source version, not the secret value;
- operation ID and queue state;
- status and reconcile plan output;
- remote object metadata, not the remote value; and
- exact error class and message.

Do not paste secret payloads, source reads, provider tokens, static
credentials, GitLab tokens, AWS external IDs, kubeconfigs, or raw audit records
that contain secrets.
