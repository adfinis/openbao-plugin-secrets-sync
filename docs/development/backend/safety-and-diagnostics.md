# Safety And Diagnostics

Backend safety rules keep OpenBao as the source of truth without silently
taking over unrelated remote objects or leaking source payloads into
operational records.

## Safety Invariants

Preserve these invariants:

- Source payload values appear only in source read responses and provider
  mutation requests.
- Destination sensitive config is stored separately from public destination
  metadata and is redacted on reads.
- Providers mutate only through prepared payloads and resolved destination
  config.
- Association activation requires destination authority and source eligibility
  when strict source opt-in is enabled.
- Queue capacity errors occur before a new source version is accepted.
- Remote deletes require `delete_mode=delete` and provider-owned delete
  support.
- Restore guard blocks remote mutation until an operator acknowledges it.
- `disabled=true` blocks background provider traffic and remote mutation.
- Manual reconcile can still read provider state while disabled because it does
  not write destination secrets.
- Event-triggered dispatch is only a low-latency wakeup for durable queued
  work; periodic processing remains the fallback.
- Manual reconcile reads provider state and updates local status only.
- Periodic drift repair enqueues normal outbox work only for verified owned
  drift.
- Older operations cannot overwrite newer per-object status.
- Provider capability flags must match implemented and tested behavior.

## Restore Guard

Restore guard is a mutation gate for restored or cloned mounts. When active,
it blocks queue drain, event dispatch, periodic dispatch, and periodic repair
enqueue.

Periodic drift detection can still refresh status while restore guard is
active. This gives operators evidence for the restore or clone review before
acknowledging the guard.

Acknowledging an active restore guard rotates the restore epoch. Provider
requests carry the current restore epoch where the destination supports
ownership metadata.

## Disabled Mounts

`disabled=true` is a mount pause. It blocks background provider traffic and
remote mutation. Periodic work returns before drift detection or dispatch, and
event dispatch does not process queue work.

Manual reconcile remains available because it does not write destination
secrets. Manual sync can still enqueue durable work when the association is
valid, but queue drain and dispatch provider mutation remain blocked until the
mount is enabled again.

## Replication Safety

Remote mutation is allowed on local mounts and on active nodes that are safe to
own provider writes.

Remote mutation is blocked on unsafe replication states, including performance
secondary, performance standby, performance bootstrapping, DR secondary, and DR
bootstrapping states.

The same replication check protects periodic drift work, event dispatch, queue
drain, and queued provider mutation.

## Ownership Checks

Providers that can store ownership metadata must write and verify enough state
to distinguish this mount, source path, association object, source version,
payload hash, and restore epoch where supported.

The backend treats ownership loss as a terminal safety state. It does not
overwrite a remote object that appears to belong to another mount, association,
restore epoch, or unmanaged owner. Operators can remove or inspect the remote
object, then run manual sync to recreate owned state from OpenBao.

## Response Diagnostics

Responses use `hint` and `next_actions` when the next operator action is known.
Diagnostic actions include:

- action name;
- OpenBao operation;
- path;
- suggested parameters;
- force flag when the API path should be called with `-force`;
- `mutates_remote` to distinguish read/status operations from remote mutation.

Diagnostics are intentionally action-oriented. They should not repeat generic
error text when the response can point to a concrete recovery command.

Common diagnostics include:

- already-enabled association update with no queued work;
- disabled association;
- disabled destination;
- active restore guard;
- disabled mount;
- queue capacity exhaustion;
- unsafe replication node;
- remote missing;
- remote drift;
- remote ownership loss;
- source opt-in failure;
- destination policy or validation failure.

## Redaction

Do not put source payload values, destination sensitive config, provider
tokens, provider secret values, or unredacted provider request bodies into:

- status records;
- queue records;
- association records;
- destination public records;
- logs;
- metrics;
- hints;
- `next_actions`;
- reconcile responses;
- plan responses.

Redaction tests should cover every response path that reads provider state or
includes failure context.
