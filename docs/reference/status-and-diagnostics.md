---
verifiedBy:
  - internal/domain/status.go
  - internal/providers/provider.go
  - internal/backend/path_status.go
  - internal/backend/path_data.go
  - internal/backend/path_reconcile.go
  - internal/backend/path_queue.go
  - internal/backend/storage_records.go
  - internal/backend/storage_outbox.go
  - internal/backend/storage_status.go
  - internal/backend/dispatcher.go
  - internal/backend/backend.go
  - internal/backend/diagnostics.go
  - internal/backend/diagnostics_test.go
  - internal/backend/terminal_retention_test.go
---

# Status and diagnostics

`status/<path>` is the convergence view for one source path. Queue paths show
the work that is still waiting. Responses include `hint` and `next_actions`
when the next operator step is known.

## Sync states

`bao read secret-sync/status/<path>` returns these top-level fields:

- `state`: `PENDING` when any operation for the path is `pending` or
  `retry_wait`. Otherwise, the first object state that is not `SYNCED`.
  `SYNCED` when every object is synced. `NO_ASSOCIATION` when nothing is
  queued and no object status exists. A path with no source data returns
  `UNKNOWN` with empty lists.
- `version`: the current source version.
- `operation_ids`: queued operations for the path.
- `objects`: one entry per association object, with `association_id`,
  `object_id`, `destination_ref`, `resolved_name`, `state`, `version`, `remote_version`, `verification`, `last_operation_id`,
  `last_success_time`, `last_reconcile_time`, `last_drift_detected_time`,
  `last_repair_time`, `repair_count`, `last_error_class`, `last_error`, and
  any `hint` and `next_actions`.

Source writes and `DELETE data/<path>` return `sync_state` in their
`metadata` object: `PENDING`, `NO_ASSOCIATION`, or, for a blocked hardened
posture write, `VALIDATION_ERROR`.

| State | Meaning | Usual next step |
| --- | --- | --- |
| `NO_ASSOCIATION` | No work is queued and no object status exists. In a write response, the write queued no work. | Create or enable an association. In hardened posture, check source sync. |
| `PENDING` | Work for the path is queued. Top-level only. | Wait for dispatch. If it does not progress, read `queue` and check the [mutation gates](../concepts/ownership-and-drift.md#mutation-gates). |
| `SYNCED` | After dispatch, the provider accepted the write for this version. After reconcile, the remote evidence matches the desired payload hash. | None. Check `verification` for the strength of the evidence. |
| `DRIFTED` | The owned remote object has a different payload hash or source version. Dispatch failures of class `drift` or `collision` also record this state. | Run reconcile plan, then manual sync or background repair. If `last_error_class=collision`, treat it as an ownership conflict. |
| `REMOTE_MISSING` | The remote object does not exist. This is the expected state after a successful remote delete. | Run manual sync if the object should exist. |
| `REMOTE_OWNERSHIP_LOST` | A remote object exists, but its ownership metadata does not match this mount, association, object, and restore epoch. | Inspect or remove the remote object, then run manual sync. |
| `DESTINATION_AUTH_ERROR` | The provider rejected the credentials (`authn`). | Fix the destination credentials, check destination health, then retry the operation. |
| `DESTINATION_POLICY_ERROR` | The provider denied permission (`authz`). | Fix IAM, RBAC, or token scope, then retry the operation. |
| `DESTINATION_RATE_LIMITED` | The provider rate limited the request (`rate_limit`). | Let automatic retry run. Retry the operation if it ends terminal. |
| `DESTINATION_UNAVAILABLE` | The endpoint, network, or provider service failed (`unavailable`). | Wait for recovery. Retry the operation if it ends terminal. |
| `VALIDATION_ERROR` | A local or provider validation rule blocked the work. Examples: source sync not enabled in hardened posture, destination prefix constraints, an association or destination disabled after queueing. | Follow `next_actions`, fix the condition, then plan or run manual sync. |
| `QUEUE_BLOCKED` | The payload exceeds the provider size limit, or the provider rejected the request as too large (`capacity`). | Reduce the payload or change the secret shape, then run manual sync. |
| `DISABLED` | The association or destination is disabled. | Enable it when sync should resume. |
| `UNKNOWN` | The provider returned no comparable payload evidence. | Read reconcile output and the provider page. |
| `INTERNAL_ERROR` | A plugin-internal or unclassified failure, such as a missing association or destination record at dispatch. | Check plugin logs, capture evidence, then retry the operation. |

Queue capacity exhaustion is not a sync state. The request that would exceed
capacity fails with an error and a hint. An object keeps its last recorded
state until a later dispatch or reconcile replaces it.

[Route by status](../operate/troubleshoot.md#route-by-status) maps each state to
recovery steps.

## Queue operation states

| State | Dispatchable | Meaning |
| --- | --- | --- |
| `pending` | Yes, when due | Waiting for dispatch. |
| `retry_wait` | Yes, after `not_before` | Waiting for an automatic retry. |
| `failed_terminal` | No | Failed with a class that does not retry, or ran out of attempts. Stays until retried, canceled, or cleaned up. |
| `canceled` | No | Returned once in the cancel response. The record is then deleted. |

The dispatcher deletes a successful operation after it writes status. It also
drops an upsert that a newer source version superseded, without a status
update. A missing `queue/<operation-id>` record therefore proves nothing; read
`status/<path>`.

Operation records include `trigger` (`user` or `drift-repair`), `attempts`,
`not_before`, and claim fields. A dispatcher claims an operation with a
five-minute lease. Retry and cancel reject an operation with an active claim.
An expired claim can be claimed again.

`bao read secret-sync/queue` returns `pending`, `retry_wait`, `claimed`,
`terminal`, `oldest_age_seconds`, `capacity`, and `utilization`. Only
`pending` and `retry_wait` operations count against `queue_capacity`.

Cleanup removes `failed_terminal` records:

- It keeps the newest 1,000 records and deletes records last updated more than
  seven days ago.
- Each pass deletes at most 100 records above the count limit and 100 expired
  records. A large backlog takes several passes.
- It runs in every periodic callback, including on a disabled mount, and after
  every dispatcher batch. It does not run on a node in an unsafe replication
  state.
- It skips records with an active claim.

[Manage queued operations](../operate/troubleshoot.md#manage-queued-operations)
covers retry, cancel, and drain.

## Verification values

Status and reconcile objects carry `verification`, which says what evidence the
latest provider read used:

| Value | Meaning |
| --- | --- |
| `value` | The provider read the live remote value or native data and compared its hash with the desired payload hash. |
| `metadata` | The provider compared the payload hash in the ownership metadata without reading the value. An edit that changes only the value is invisible. |
| empty | No reconcile or drift check has run for this object. |

Dispatch does not read the object back. A successful dispatch keeps the
`verification` value from the latest check. Each provider page states which
value it reports under `Drift and delete behavior`. For example, AWS Secrets
Manager reports `metadata` unless `value_drift_detection=true`.

## Error classes and retry

Providers classify every failure into one class. The class sets the object
state and the retry behavior.

| Class | Meaning | State | Automatic retry |
| --- | --- | --- | --- |
| `rate_limit` | The provider throttled the request. | `DESTINATION_RATE_LIMITED` | Yes |
| `unavailable` | Network, timeout, or provider service failure. | `DESTINATION_UNAVAILABLE` | Yes |
| `authn` | Credentials were rejected. | `DESTINATION_AUTH_ERROR` | No |
| `authz` | Permission was denied. | `DESTINATION_POLICY_ERROR` | No |
| `validation` | A local or provider validation rule rejected the request. | `VALIDATION_ERROR` | No |
| `ownership` | The remote object is not owned by this association. | `REMOTE_OWNERSHIP_LOST` | No |
| `collision` | An object already exists at the remote name. | `DRIFTED` | No |
| `drift` | The remote object changed or carries a newer source version. | `DRIFTED` | No |
| `capacity` | The payload is too large for the provider. | `QUEUE_BLOCKED` | No |
| `internal` | Plugin-internal or unclassified failure. | `INTERNAL_ERROR` | No |

An operation gets at most three dispatch attempts. After the first and second
retryable failure it waits in `retry_wait`, about 30 and 60 seconds plus up to
20 percent jitter. A third failure, or any failure with a class that does not
retry, moves it to `failed_terminal`. `queue/<operation-id>/retry` moves a
`retry_wait` or `failed_terminal` operation back to `pending` and resets its
attempt count.

[Error classification](../development/provider-contract.md#error-classification)
defines how providers map their native errors to these classes.

## Hints and next actions

`hint` names the next step in plain language. `next_actions` lists concrete
commands, most relevant first. Prefer these actions over improvised recovery
commands, and review any action with `mutates_remote=true` before you run it.

Where they appear depends on the response:

- **Success responses** carry them next to the data they describe: on each
  object in `status/<path>` and reconcile responses, at the top level of an
  association update that queued no work, and in the `metadata` object of a
  source write that hardened posture blocked.
- **Error responses** keep the OpenBao error shape. `hint` and `next_actions`
  are nested in the response `data` object.
- **The CLI** prints the error message, which ends with a `Hint:` line and a
  `Next action:` line that holds the first action's command.

Each action has these fields:

| Field | Meaning |
| --- | --- |
| `action` | Action name, such as `manual_sync` or `enable_source`. |
| `operation` | `read` or `write`. |
| `path` | Path relative to the mount. |
| `parameters` | Request parameters, when the action needs them. |
| `force` | `true` when the CLI call needs `-force`. |
| `mutates_remote` | `true` when the action can change a destination. |
| `command` | A ready-to-run `bao` command that includes the mount path. |

```json
{
  "action": "manual_sync",
  "operation": "write",
  "path": "associations/<path>/sync",
  "parameters": { "destination": "<type>/<name>" },
  "mutates_remote": true,
  "command": "bao write secret-sync/associations/<path>/sync destination=<type>/<name>"
}
```

The plugin emits these actions: `manual_sync`, `retry_operation`, and
`drain_queue` (all `mutates_remote=true`); `enable_source`,
`enable_association`, `enable_destination`, `enable_secret_sync`, and
`acknowledge_restore_guard`; and the read actions `inspect_association_plan`,
`read_destination`, `check_destination_health`, `read_queue`, and
`read_config`. The unsafe replication diagnostic has a hint but no actions.

> [!WARNING]
> `acknowledge_restore_guard` and `enable_secret_sync` report
> `mutates_remote=false` because the call itself writes no destination. Both
> resume remote mutation, and acknowledging the guard rotates the restore
> epoch. Run them only at the end of a
> [restore or clone review](../operate/restore-and-clone.md#resume-remote-mutation).
