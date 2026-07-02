# Architecture

This page describes the maintained architecture of
`openbao-plugin-secrets-sync`. Use it when you review backend behavior, storage
records, provider boundaries, or consistency rules.

## Plugin boundary

Secret Sync is an OpenBao external secret-engine plugin. OpenBao runs the
plugin as a separate process and routes logical backend requests to it.

OpenBao remains responsible for:

- authentication and authorization;
- audit request handling;
- plugin lifecycle and routing;
- seal and storage encryption;
- replication and active-node routing.

The plugin receives a mount-scoped storage view. It does not receive global
storage and cannot observe writes to unrelated mounts. Secret Sync therefore
stores source data in its own mount and synchronizes that source data to
external destinations. It is not a drop-in implementation of `/sys/sync`.

The plugin uses the OpenBao framework backend with `logical.TypeLogical`.
Backend setup registers path handlers, seal-wrapped storage prefixes,
invalidation hooks, cleanup hooks, and periodic work.

### Future cross-plugin communication

OpenBao has a
[cross-plugin communication RFC](https://gist.github.com/cipherboy/eb2dfa598615ac5c510c534ca383d3ba)
that proposes hook-driven and internal request paths between plugins. If that
API becomes stable, Secret Sync can use it as an optional source-ingest path
from other OpenBao secret engines.

The architecture does not depend on that RFC. Secret Sync owns its source data,
outbox, provider interface, reconcile model, and confused-deputy controls
inside its own mount. A future integration should add cross-plugin source
adapters at the boundary and should not use localhost API clients, manually
managed tokens, or network loopback calls between plugins.

## Component model

```text
+--------------------------------------------------------------------------+
| openbao-plugin-secrets-sync                                              |
|                                                                          |
| API paths                                                                |
|   config, sources, data, metadata, destinations, associations, queue,    |
|   status, reconcile                                                      |
|                                                                          |
| Core services                                                            |
|   Source store              Source metadata store                        |
|   Destination registry      Association registry                         |
|   Outbox queue              Sync dispatcher                              |
|   Reconciler                Status store                                 |
|   Provider registry         Runtime cache                                |
|   Capability validation     Redaction and error classification           |
|   Observability recorder    Runtime identity                             |
+------+------------------+---------------------+-------------------------+
       |                  |                     |
       v                  v                     v
 AWS Secrets Manager   Kubernetes Secrets   GitLab project variables
```

Providers receive resolved destination configuration, runtime identity, remote
names, ownership fields, and prepared payloads. Providers never receive OpenBao
request objects and never make OpenBao policy decisions.

## Storage records

All keys are relative to the plugin storage view.

```text
config
schema/version
identity/plugin-instance
identity/restore-epoch

data/<path>/versions/<version>
metadata/<path>

destinations/<type>/<name>
destinations_secrets/<type>/<name>

associations/<path>/<association-id>
associations_by_destination/<type>/<name>/<association-id>
association_names/<destination-ref>/<reservation>/<association-id>

enqueue_intent/<path>/<version>

outbox/<operation-id>
outbox_by_due/<timestamp>/<operation-id>
outbox_by_path/<path>/<operation-id>
outbox_by_state/<state>/<operation-id>

status/<path>/<association-id>/<object-id>
```

Destination writes split public and sensitive configuration. Public fields are
stored in `destinations/<type>/<name>`. Sensitive fields are stored under
`destinations_secrets/<type>/<name>`, which is listed in
`PathsSpecial.SealWrapStorage`.

Source version records are also stored under a seal-wrapped prefix. Status,
queue, and association records can include operational metadata such as source
paths, association IDs, remote names, versions, operation IDs, and error
classes, but they must not include source payload values.

## Runtime identity

`schema/version` records the storage schema understood by the plugin binary.
The backend initializes the schema on the first storage-backed request. If a
stored schema requires a newer incompatible plugin, request handling and
periodic processing fail closed before source or remote mutation.

`identity/plugin-instance` is generated once per mount. Provider requests carry
this ID so providers can write and verify ownership metadata for the OpenBao
mount that produced a remote object.

`identity/restore-epoch` is generated once per mount and rotates when an active
restore guard is acknowledged. Providers include the restore epoch in ownership
metadata where the destination supports it. This helps distinguish restored or
cloned mounts that might otherwise manage the same remote object.

Each source metadata record carries a random generation. Operation IDs and
idempotency keys include that generation so deleting and recreating a source
path does not reuse historical operation IDs when version numbers restart.

## Source lifecycle

Source data is KV-v2-like but not wire-compatible with the OpenBao KV-v2
engine. Each source write creates a new version under
`data/<path>/versions/<version>` and updates metadata under `metadata/<path>`.

Source metadata controls:

- the current version;
- the oldest retained version;
- maximum retained versions;
- CAS requirements;
- custom metadata such as `syncable=true`;
- per-version deletion and destroy state.

Source writes that can enqueue sync work reserve queue capacity before they
commit the new source version. If the queue is full, the write returns an
error before accepting the new version.

Source delete, soft-delete, destroy, and undelete operations participate in
sync. Current-version delete and destroy operations cancel stale queued upserts
and enqueue remote deletes for associations that use `delete_mode=delete`.
Undeleting the current source version enqueues replacement upserts for enabled
associations.

## Association lifecycle

An association links one source path to one destination. It defines:

- the destination type and name;
- the resolved remote name or name template;
- granularity;
- payload format;
- destination-native data mapping;
- delete mode;
- enabled state.

Enabled associations require source eligibility when
`require_source_opt_in=true`. Mounts default that setting to `false`. In strict
mode, eligibility requires source custom metadata `syncable=true`.

The backend validates association requests against provider capabilities before
it accepts them. Capability checks cover secret-path support, secret-key
support, destination-native data mapping, owned delete support, and provider
payload limits.

Association records reserve the destination and remote-name identity they
manage. This prevents two associations from managing the same remote object for
the same destination.

## Payload model

The core backend builds canonical payloads before calling a provider.

Supported payload forms are:

- `format=json` with `granularity=secret-path`, which sends the whole source
  data map as deterministic JSON;
- `format=json` with `granularity=secret-key`, which sends one deterministic
  JSON object per top-level source key;
- `format=raw` with `granularity=secret-key`, which sends the selected source
  key as raw string or byte data;
- `data_mapping=source-keys` with `granularity=secret-path`, which maps
  top-level source keys into destination-native data keys when the provider
  advertises data-map support.

The payload hash is computed over the exact bytes that the provider receives.
Providers must not reformat the payload before writing it when they also store
or compare the payload hash.

## Queue and dispatch

The outbox stores durable remote-mutation intent. Queue records are indexed by
state, due time, and source path.

Supported operation states are:

```text
pending
retry_wait
failed_terminal
```

Dispatch claims are stored on the outbox record with a claim owner, expiry
time, and attempt number. Unexpired claims are skipped. Expired claims are
reclaimable.

The dispatcher processes due `pending` and `retry_wait` records. For each
operation it:

1. claims the operation;
2. loads the association and destination;
3. validates provider capabilities and destination policy;
4. loads the source version when the operation is an upsert;
5. builds the canonical payload;
6. calls the provider;
7. writes per-object status;
8. prunes successful outbox work or schedules retry/terminal failure.

Only provider errors classified as `rate_limit` or `unavailable` retry
automatically. Authentication, authorization, validation, ownership, collision,
drift, capacity, and internal failures remain terminal until an operator
changes configuration or retries the operation manually.

## Enqueue recovery

Source writes and source lifecycle mutations write enqueue intents before they
write outbox records. Enqueue intents contain the expected operation IDs,
operation types, association IDs, object IDs, and destination references.

Periodic work and queue drains recover incomplete enqueue intents before
processing due outbox records. Upsert intents recover only while the source
version is live. Delete intents recover only when the referenced source version
is deleted or unavailable.

## Ordering

For a single association and object, an older source version must not overwrite
a newer source version.

The backend enforces this with three layers:

- source writes supersede older inactive queued upserts for the same
  association object;
- dispatch rechecks the current source version before mutating a stale upsert;
- status writes reject older versions when newer status already exists.

Providers that can read ownership metadata also reject stale mutations when
the remote object carries a newer managed source version.

## Destination policy

Destinations can restrict the source paths and remote names that may use them.

The backend checks destination policy during:

- association planning;
- association activation;
- manual association sync;
- association enable;
- queued dispatch.

This means a tightened destination policy blocks already queued work before the
provider mutates remote state.

## Reconciliation

Reconciliation reads provider remote state and reports local status. It does
not mutate destination secrets.

The per-path reconcile plan path reads provider state and returns the computed
state without changing local status. The per-path reconcile apply path writes
local status from provider read-state results.

## HA and replication

Secret Sync assumes OpenBao routes writes to an active node. Periodic and
manual remote mutation paths check OpenBao replication state before writing
queue or status records or calling providers.

Remote mutation is blocked on unsafe replication states, including performance
secondary, performance standby, performance bootstrapping, DR secondary, and
DR bootstrapping states. Local mounts are allowed to proceed.

The lifecycle e2e fixture covers a three-node Raft deployment with static seal
self-unseal, HA failover, queued work, status persistence, and operator seal
recovery.

## Runtime caches

The backend caches provider destination runtimes in memory. Cache entries are
keyed by destination identity and invalidated when destination public or
sensitive configuration changes. Backend cleanup also closes cached runtimes.

Runtime caches are an optimization. Correctness comes from durable storage,
queue claims, provider idempotency, and provider ownership checks.

## Safety invariants

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
- Restore guard blocks background and manual-drain remote mutation until an
  operator acknowledges it.
- Reconcile reads provider state and updates local status only.
- Older operations cannot overwrite newer per-object status.
- Provider capability flags must match implemented and tested behavior.
