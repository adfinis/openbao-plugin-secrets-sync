---
verifiedBy:
  - internal/backend/backend.go
  - internal/backend/write_locks.go
  - internal/backend/storage_transactions.go
  - internal/backend/existence_checks.go
  - internal/backend/path_data.go
  - internal/backend/path_versions.go
  - internal/backend/path_metadata.go
  - internal/backend/path_sources.go
  - internal/backend/path_associations.go
  - internal/backend/association_enqueue.go
  - internal/backend/association_policy.go
  - internal/backend/path_queue.go
  - internal/backend/enqueue.go
  - internal/payload/payload.go
  - internal/backend/existence_checks_test.go
  - internal/backend/storage_transactions_test.go
---

# Request lifecycle

Each path handler validates input, takes the locks it needs, checks local
policy, and commits local records. Handlers never mutate remote state. The
public path table is in [API reference](../../reference/api.md#paths).

## Request entry

`HandleRequest` runs `ensureRuntimeState` before every non-help request. A
schema compatibility error becomes an error response before any handler runs.
See [Storage model](storage-model.md#schema-state).

## Locks

`write_locks.go` uses striped `locksutil` locks keyed by:

- `source:<path>` for every source, metadata, version, source sync, and
  association lifecycle mutation;
- `association-name:<destination-ref>:<reservation>` for association writes
  and deletes;
- `destination:<destination-ref>` for destination writes and deletes, and for
  association writes so a destination cannot be deleted while an association
  for it is being created.

`lockWriteKeys` acquires the stripes for all keys in stripe order, once each,
and releases them in reverse order.

`enqueueMu` serializes queue mutations inside the process. Code that needs
both acquires the striped locks first. Source writes, source deletes, and version
mutations run through `withEnqueueMutationTransaction`: on storage that
implements `logical.TransactionalStorage` the mutation runs in one native
transaction without `enqueueMu`; otherwise it holds `enqueueMu` and writes
keys in the order below. Association enqueue, queue retry and cancel, claims,
commits, recovery, and terminal pruning hold `enqueueMu`.

## Create and update classification

OpenBao calls an existence check before ACL evaluation on paths that support
both create and update. `existence_checks.go` reports:

- source data and metadata exist when `metadata/<path>` exists;
- a destination exists when `destinations/<type>/<name>` exists;
- an association exists when the destination and provider identity selector
  matches one or more records at the source path.

An ambiguous selector counts as existing, so a create-only token cannot use
ambiguity to overwrite a record; the write then returns its ambiguity error. An
invalid path or malformed selector counts as missing and fails validation in
the handler. Read-only storage errors count as missing, as in the KV engine;
other storage errors fail the request. User-facing capability rules are in
[API reference](../../reference/api.md#create-and-update-capabilities).

## HA request forwarding

`queue/drain` sets `ForwardPerformanceStandby`, so OpenBao forwards it to the
active node before the callback runs. This prevents dispatch against an
eventually consistent standby view. The drain callback still runs the
replication check, as do the periodic callback and event dispatch, because
background work has no request to forward. The plugin does not enable
`ForwardPerformanceSecondary` or `WriteForwardedStorage`.

## Source writes

`pathDataWrite` stores a new version and enqueues upserts in this order:

1. Normalize the source path and parse the payload.
2. Acquire `source:<path>`.
3. Open the enqueue mutation transaction.
4. Load metadata, or start a new record with a new generation, and apply CAS.
5. List enabled associations whose destination exists and is enabled.
6. In hardened posture, check source eligibility. If it fails, drop all
   associations from this write and keep the error for the response.
7. Render concrete secret-key reservation names and reject conflicts with
   other associations.
8. Build `pending` upsert records for the next version.
9. Find unclaimed queued upserts for older versions of the same association
   objects.
10. Check queue capacity, counting those stale upserts as replaced.
11. Write associations whose concrete reservations changed.
12. Write `enqueue_intent/<path>/<version>`, including the stale upsert IDs.
13. Write the version record.
14. Write metadata and prune excess versions.
15. Delete the stale upserts.
16. Write the outbox records.
17. Delete the enqueue intent.
18. Commit, release locks, and signal event dispatch when operations were
    written.

A capacity error rejects the write before the version is stored. A hardened
eligibility failure does not reject the write: the version is stored, nothing
is enqueued, and the response reports `sync_state=VALIDATION_ERROR` with a
`hint`. Steps 12 and 17 are skipped when there are no operations.

## Source lifecycle mutations

`delete`, `undelete`, and `destroy` run each requested version in its own
enqueue mutation transaction. Only a mutation of the current version
participates in sync:

- Delete or destroy of the current version removes every queued upsert for
  that version and enqueues `delete` operations for enabled associations with
  `delete_mode=delete`. A claimed upsert for that version rejects the request.
- Undelete of a soft-deleted current version removes queued deletes for that
  version and enqueues upserts for enabled associations. A claimed delete
  rejects the request.

Both use the same intent, version, metadata, cancel, outbox, intent-delete
order as a source write, and both check capacity first. `DELETE data/<path>`
soft-deletes the current version through the same plan.

Metadata delete requires that no association exists for the path. It then
deletes `failed_terminal` outbox records, versions, status, and metadata under
`enqueueMu`.

`sources/<path>/enable` sets `source_sync_enabled` and enqueues the current
version for enabled associations. If enqueue fails, the metadata change is
rolled back.

## Association writes

The association write handler:

1. Builds the record from request fields and the selected existing record,
   normalizes provider config through the provider, and resolves names.
2. Validates provider capabilities: granularity, format, data mapping, and
   `delete_mode=delete` support.
3. Rejects changes to identity fields of an existing record.
4. Acquires `source:<path>`, the name reservation lock, and the destination
   lock.
5. Requires a live current source version, renders concrete reservations, and
   rejects reservation conflicts.
6. When the record is enabled, checks hardened source eligibility.
7. Checks that the destination exists, is enabled, and allows the path and
   every resolved name.
8. Writes the record, then enqueues when needed.

The write enqueues the current version when the record is enabled and is new,
was disabled, or changed desired state. Desired state is `format`,
`data_mapping`, `data_key_template`, or provider config, except the AWS
delete recovery window. A desired-state change uses a salted operation ID. An
enabled record with no desired-state change enqueues nothing and returns a
manual sync `hint` and `next_actions`. User-facing update rules are in
[Associations](../../configure/associations.md#update-an-association).

`enable` runs the same eligibility, destination, and reservation checks, writes
the enabled record, and enqueues the current version if the record was
disabled. If enqueue fails after the record was written, the previous record
is restored, or the new record is deleted, so an error never leaves an active
association behind.

`disable` removes queued operations for the association, writes
`enabled=false`, and writes `DISABLED` status for its objects. Association
delete removes every outbox record for the association, in any state, then
the record and its indexes. Both reject the request while an operation for
the association is claimed. Neither enqueues a remote delete.

Manual `sync` requires an enabled association, a live current version,
eligibility, and destination policy, then enqueues the current version with a
salted operation ID. It does not cancel other queued work. Provider mutation
still happens later in dispatch.

## Destination policy enforcement

`validateDestinationPolicyForObject` checks `allowed_source_path_prefixes`,
`allowed_resolved_name_prefixes`, and, in hardened posture, that both lists
are set. It runs during association plan, write, enable, and manual sync, in
dispatch before each provider call, and in manual and periodic reconcile
before each provider read. A tightened policy therefore blocks queued work and
remote reads for objects it no longer allows. User-facing rules are in
[Delegate to application owners](../../configure/delegate.md#constrain-destinations).

Disabled destinations stay readable and checkable. Source writes skip their
associations, and dispatch fails their queued operations with a `validation`
error.

## Payload construction

`buildCanonicalPayloadForObject` builds the provider payload from the source
version:

- `format=json` with `secret-path` encodes the whole data map as JSON.
- `format=json` with `secret-key` encodes `{<key>: <value>}` for one key.
- `format=raw` with `secret-key` sends the key's string or byte value as is.
- `data_mapping=source-keys` with `secret-path` renders each key through
  `data_key_template` and sends a key-to-bytes map; values must be strings or
  bytes.

JSON is encoded with sorted keys and without HTML escaping or a trailing
newline. The hash is `sha256:` plus the hex digest of the payload bytes. For a
data map, the payload bytes are the canonical JSON of the map, and the
provider also receives the map itself. Providers that store or compare the
hash must not reformat the payload bytes. Dispatch and reconcile reject a
payload larger than the provider's `MaxPayloadBytes` with a `capacity` error.
