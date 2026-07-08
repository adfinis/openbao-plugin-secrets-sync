# Request Lifecycle

The logical backend is path-oriented. Each path handler validates request
input, loads durable state, enforces local policy, and either returns local
state or writes durable queue/status records. Provider calls happen only from
dispatch and reconcile paths after the backend has resolved destination config
and runtime identity.

## Path Ownership

`backend.go` registers these path groups:

- `info`: defaults, registered providers, and provider capability summaries.
- `config`: mount-wide security posture, pause, restore guard, queue capacity,
  drift work, and event dispatch settings.
- `destinations`: destination configuration, destination checks, provider
  defaults, provider capabilities, and destination policy fields.
- `associations`: association plan, create/update/read/list/delete, enable,
  disable, and manual sync.
- `metadata`: source metadata create/update/read/list/delete.
- `sources`: source eligibility check and source opt-in helper.
- `data`: source data create/update/read/delete.
- `delete`, `undelete`, `destroy`: source version lifecycle mutations.
- `status`: per-source sync status summary.
- `reconcile`: manual provider read-state checks and local status refresh.
- `queue`: queue summary, drain, read, retry, and cancel.

Path code should stay close to the ownership above. Shared helpers belong in
focused backend files only when multiple path groups depend on the same rule.

## Source Writes

Source data writes are KV-v2-like. A write creates a new version record and
updates source metadata. The backend is not wire-compatible with the OpenBao
KV-v2 engine, and source data is local to this plugin mount.

Before committing a source write that can enqueue sync work, the backend:

1. normalizes the source path;
2. locks the source path;
3. loads metadata and applies CAS rules;
4. identifies enabled associations for the path;
5. checks source eligibility in hardened posture;
6. checks queue capacity;
7. writes enqueue intent;
8. writes source version and metadata;
9. writes outbox records;
10. clears completed enqueue intent;
11. signals event dispatch when configured.

If queue capacity is exhausted, the write fails before accepting the new source
version.

## Source Lifecycle Mutations

Current-version delete and destroy operations participate in sync. They cancel
stale queued upserts and enqueue remote deletes for enabled associations using
`delete_mode=delete`.

Undeleting the current source version enqueues replacement upserts for enabled
associations. Deleted or destroyed versions are not used for upsert recovery.

Metadata delete removes local source state only when no associations remain for
the path. It deletes source versions, source metadata, and status records for
that path.

## Association Writes

An association links one source path to one destination. It defines:

- destination reference;
- resolved remote name or name template;
- granularity;
- payload format;
- destination-native data mapping;
- delete mode;
- enabled state.

The API accepts the compact `destination=<type>/<name>` selector. The backend
stores normalized destination type, destination name, and destination reference
on the association record.

Enabled associations require source eligibility in hardened posture. Mounts
default `security_posture=standard`; in hardened posture, eligibility requires
source custom metadata `syncable=true`.

The backend validates association requests against provider capabilities before
it accepts them. Capability checks cover secret-path support, secret-key
support, destination-native data mapping, owned delete support, and provider
payload limits.

Association records reserve the destination and remote-name identity they
manage. This prevents two associations from managing the same remote object for
the same destination.

## Association Activation And Sync

Association create and disabled-to-enabled transitions enqueue current source
state when the association is enabled and the source has a live current
version.

Updating an already enabled association does not automatically enqueue sync
work. The response includes a hint and `next_actions` entry for manual sync
when the update did not enqueue work. This keeps updates explicit and avoids
surprising remote mutation.

Manual sync always targets the current source version. It enqueues durable
work; provider mutation still happens later through the normal dispatch path.
It is the operator path for retrying a current version, recovering after
terminal status, recreating a missing remote object, or pushing after an
association update.

## Destination Policy

Destinations can restrict source paths and remote names through:

- `allowed_source_path_prefixes`
- `allowed_resolved_name_prefixes`

The backend checks destination policy during:

- association planning;
- association activation;
- manual association sync;
- association enable;
- queued dispatch.

This means a tightened destination policy blocks already queued work before the
provider mutates remote state.

Disabled destinations remain readable and checkable, but they cannot be used
for sync mutation until enabled again.

## Payload Model

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
