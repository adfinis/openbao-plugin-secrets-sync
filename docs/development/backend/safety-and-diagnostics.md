---
verifiedBy:
  - internal/backend/backend.go
  - internal/backend/dispatcher.go
  - internal/backend/event_dispatch.go
  - internal/backend/path_queue.go
  - internal/backend/periodic_reconcile.go
  - internal/backend/path_config.go
  - internal/backend/path_associations.go
  - internal/backend/path_destinations.go
  - internal/backend/diagnostics.go
  - internal/backend/storage_status.go
  - internal/backend/redaction_test.go
  - internal/backend/diagnostics_test.go
  - internal/backend/dispatcher_global_guard_test.go
---

# Safety and diagnostics

Backend safety rules keep the mount the source of truth without taking over
unrelated remote objects or leaking source payloads into operational records.
Change these rules only with matching tests.

## Safety invariants

- Source payload values appear only in source data read responses and in
  provider upsert requests. Plan and read-state requests carry the payload
  hash, size, and data-map key names, not values.
- Sensitive destination config is stored only under `destinations_secrets/`
  and is redacted on destination reads.
- Request handlers never mutate remote state. Only the dispatcher calls
  provider `Upsert` and `Delete`, and only for a claimed operation.
- A dispatcher commits a result only while it still holds the claim.
- Association activation requires an existing, enabled destination whose
  policy allows the path and every resolved name, and, in hardened posture,
  source eligibility and constrained destinations.
- Association activation restores the previous record when the initial
  enqueue fails, so an error never leaves a newly active association behind.
- A queue capacity error rejects a source write before the version is stored.
- Remote delete requires `delete_mode=delete`. Association writes accept that
  mode only when the provider reports delete-if-owned support, and dispatch
  rechecks the mode before every delete.
- Dispatch rechecks association and destination state, source eligibility,
  and destination policy before every provider call.
- Background drift repair enqueues normal outbox work only for drift with
  verified ownership.
- An older operation cannot overwrite newer per-object status.
- Provider capability flags must match implemented and tested behavior.

## Gate enforcement points

User-facing gate semantics are in
[Ownership and drift](../../concepts/ownership-and-drift.md#mutation-gates).
The backend checks them at these points:

| Code path | Replication state | `disabled` | `restore_guard` |
| --- | --- | --- | --- |
| Periodic callback | Returns before any work | Returns after terminal pruning | Returns after drift work, before dispatch |
| Periodic repair enqueue | Covered by the callback | Covered by the callback | Skipped per object |
| Event dispatch pass | Stops | Stops | Stops |
| `queue/drain` | Error response | Error response | Error response |
| Outbox claim | Not checked | Claim skipped | Claim skipped |
| Manual reconcile, association plan, destination check and health | Not checked | Not checked | Not checked |
| Source writes, version mutations, association enqueue, manual sync | Not checked | Not checked | Not checked |

The claim check reads `config` for every operation, so a `config` write that
sets `disabled` or `restore_guard` stops further claims in a running batch. It
does not cancel a provider call already in flight or a drift batch that
already read the previous configuration. Pause is not a quiescence barrier.

Request-path enqueue is not gated. It writes durable local intent, and
dispatch stays blocked until the gate clears.

## Replication check

`remoteMutationAllowed` returns true for a local mount. Otherwise it returns
false when the replication state has any of these flags: performance
secondary, performance standby, performance bootstrapping, DR secondary, or DR
bootstrapping. The check covers the periodic callback, event dispatch, and
queue drain, which together are every path that claims outbox work.

## Restore guard

`restore_guard` is a field of the `config` record. It defaults to `false`, so
it travels with snapshots like any other config. Initialization never detects
a restore and never arms the guard; operators must fence destinations outside
the restored storage before the plugin starts. Clearing an active guard,
through `config/restore-guard/acknowledge` or `restore_guard=false`, rotates
`identity/restore-epoch`, records `restore_guard_acknowledged_time`, and wakes
event dispatch. Rotation does not rewrite remote ownership metadata or adopt
remote objects. The procedure is in
[Review a restore or clone](../../operate/restore-and-clone.md).

## Ownership checks

Providers that store ownership metadata must write and verify enough state to
distinguish the mount, source path, association, object, source version,
payload hash, and restore epoch where supported. The contract is in
[Provider contract](../provider-contract.md#ownership-metadata).

Providers refuse to overwrite a remote object owned by another mount,
association, or restore epoch, or by an unmanaged owner, and return an
`ownership` or `collision` error. The backend treats both as terminal: they
never retry automatically and map to `REMOTE_OWNERSHIP_LOST` and `DRIFTED`.
Background repair never selects these objects. Recovery is an operator
action; see
[Ownership and drift](../../concepts/ownership-and-drift.md#collisions-and-stale-objects).

## Response diagnostics

Responses carry `hint` and `next_actions` when the next operator action is
known. `diagnostics.go` builds them. Each action has:

- `action`: a stable action name;
- `operation`: the `bao` subcommand, such as `read` or `write`;
- `path`: the mount-relative path;
- `mutates_remote`: whether the action can mutate remote state;
- `command`: a ready-to-run `bao` command for the request's mount;
- `parameters`: suggested parameters, when any;
- `force`: `true` when the command needs `-force`.

`errorResponseWithDiagnostic` keeps the OpenBao error shape. It appends the
hint and the first action's command to the error text, so the CLI shows them,
and puts the structured fields under `data`.

Builders exist for: already-enabled association update without enqueue,
disabled association, active restore guard, disabled mount, unsafe
replication node, queue capacity, validation failures (hardened source
eligibility, unconstrained destination, disabled destination, and a generic
plan action), and each sync state from status or reconcile. Diagnostics
must point to a concrete next command instead of repeating generic error text.
The user-facing table is in
[Status and diagnostics](../../reference/status-and-diagnostics.md#hints-and-next-actions).

## Redaction

Never put source payload values, sensitive destination config, provider
tokens, provider secret values, or provider request or response bodies into:

- status, queue, association, or public destination records;
- logs or metrics;
- `hint` or `next_actions`;
- plan, reconcile, status, or queue responses.

Dispatch stores a fixed message such as `provider upsert failed` and the error
class, never provider error text. Validation messages can include source
paths, destination references, and resolved names.

Source paths, resolved remote names, object IDs, versions, payload hashes, and
destination-native key names are operational metadata, not payload values.
Keep them in the documented plan, status, reconcile, queue, and audit surfaces
and never next to the corresponding values. Metric attribute rules are in
[Observability internals](../observability.md).

Add a redaction test for every new response path that reads provider state or
includes failure context. `redaction_test.go` holds the current boundary
tests.
