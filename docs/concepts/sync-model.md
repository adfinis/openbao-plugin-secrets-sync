# Sync model

OpenBao Secret Sync separates desired state, queued remote work, and observed
remote state. This separation keeps source writes local and durable while still
making remote convergence visible to operators.

## Desired local state

OpenBao stores source data in the plugin mount under `data/<path>` and
`metadata/<path>`. Each source write creates a local source version. Source reads
return source payloads; operational responses such as plan, queue, status, and
reconcile must not expose source payload values.

Use [Source model](source-model.md) for the own-mount source boundary,
KV-v2-like behavior, source metadata, and source lifecycle semantics.

Destinations store provider configuration and sensitive provider credentials.
Sensitive destination fields are seal-wrapped and redacted on reads.

Associations connect one source path to one destination. They define the remote
object shape: destination reference, remote name template, granularity, payload
format, data mapping, delete behavior, and enabled state.

## Secret shapes

An association shape answers two questions: how many remote objects are written,
and how the source payload is encoded for those objects.

| Shape | Remote result | Common fit |
| --- | --- | --- |
| `secret-path`, `format=json`, `data_mapping=payload` | One remote object containing the full canonical JSON source payload. | AWS Secrets Manager and simple Kubernetes Secrets. |
| `secret-path`, `format=json`, `data_mapping=source-keys` | One remote object whose native data keys come from top-level source keys. | Kubernetes Secrets where applications read individual `.data` keys. |
| `secret-key`, `format=raw` | One remote object per top-level source key, using the raw source value. | GitLab CI/CD variables. |
| `secret-key`, `format=json` | One remote object per top-level source key, with each value wrapped as canonical JSON. | Provider-specific cases where separate objects should still carry JSON. |

Provider capabilities decide which shapes are valid. AWS supports only the
first shape. Kubernetes supports the first shape and `source-keys` data mapping.
GitLab supports both granularities and both `json` and `raw` formats, but
`secret-key` with `format=raw` is the normal CI/CD variable shape.

`raw` payloads and `source-keys` data maps require source values that can be
represented as bytes, such as strings. JSON shapes can carry structured source
values because the plugin renders canonical JSON before dispatch.

## Association selectors

Use `destination=<type>/<name>` as the normal selector for association create,
update, plan, disable, enable, and manual sync workflows. This selector matches
the way operators think about the target provider and avoids copying opaque
association IDs for common lifecycle work.

Association IDs still exist. They are stable identifiers in responses and are
used for exact association read/delete routes and rare ambiguity cases. Treat
ID-addressed lifecycle routes as an escape hatch, not the normal workflow.

## Naming Templates

Associations use `resolved_name`, `name_template`, and sometimes
`data_key_template` to turn source paths and source keys into provider object
names. The current template model is literal placeholder replacement, not a
function language. Use [Templating](templating.md) for the exact placeholders,
provider constraints, and reservation behavior.

## Asynchronous remote mutation

Requests that need provider mutation enqueue durable outbox operations and
return `sync_operation_ids`. A successful write means OpenBao accepted desired
local state and queued remote work where required. It does not mean the provider
has already converged.

Event-triggered dispatch, periodic dispatch, and `queue/drain` all process the
same durable queue. Use [Convergence](convergence.md) for queued operation
states, `sync_operation_ids`, event dispatch, manual sync, retry, cancel,
drain, and status-state recovery.

## Status and reconcile

Status is the primary convergence view. `status/<path>` reports queued work,
per-object state, last operation identifiers, error class, remote version, drift
timestamps, and repair counters.

Reconcile reads provider remote state and compares it with desired OpenBao
state. Reconcile plan is read-only and does not update local status. Reconcile
apply updates local status records but does not write destination secrets.

Background drift detection uses the same read-state behavior. In `detect` mode,
it refreshes status only. In `repair` mode, it also enqueues normal outbox
upserts for owned `DRIFTED` objects.

Use [Reconcile and drift](reconcile-and-drift.md) for plan versus apply,
verification, background drift modes, repair eligibility, restore guard
behavior, and disabled behavior.

## Ownership and safety

Providers attach ownership metadata to remote objects. Owned updates and deletes
require matching metadata. If ownership cannot be proven, providers return an
ownership error instead of overwriting remote state.

Restore guard blocks remote mutation until an operator reviews destination
state and acknowledges the guard. Mount-wide `disabled=true` also blocks
background provider traffic and remote mutation. Manual reconcile remains
available because it only reads provider state and updates local status.

Use [Ownership and safety](ownership-and-safety.md) for ownership metadata,
restore identity, drift, collisions, stale remote objects, and recovery rules.

## Diagnostics

Blocked or terminal responses include a human-readable `hint` and may include
structured `next_actions`. Follow `next_actions` before inventing recovery
commands. Typical actions include:

- acknowledge restore guard after restore review;
- enable a source path when strict source opt-in blocks sync;
- inspect queue/config when queue capacity is exhausted;
- run manual association sync after resolving remote missing, drifted, or
  ownership-lost state.

OpenBao error responses keep the normal error shape. Diagnostic fields are
nested under error `data` so clients can still recognize the response as an
error.
