# Restore and clone review

Use this procedure after restoring OpenBao storage, cloning an environment, or
starting a mount whose local Secret Sync state may no longer match remote
destinations.

Secret Sync uses a restore guard to prevent restored queue work from blindly
mutating remote secrets. While the guard is active, background processing and
manual `queue/drain` remote mutations are blocked. Reconcile planning remains
available so operators can inspect remote state before resuming sync.

Mounts default `restore_guard=false`. Operators can set `restore_guard=true`
before or during restore and clone review.

## Confirm the guard state

Read mount config:

```sh
bao read secret-sync/config
```

If `restore_guard=true`, keep the guard active until the local source versions,
associations, queued work, and remote ownership state are understood.

Do not acknowledge the guard as a first step. Acknowledgement rotates the
restore epoch and allows remote mutation to resume.

## Inventory local state

List source metadata and associations:

```sh
bao list secret-sync/metadata
bao list secret-sync/associations
```

For each important source path, read source metadata, associations, status, and
queue state:

```sh
bao read secret-sync/metadata/app/db
bao read secret-sync/associations/app/db
bao read secret-sync/status/app/db
bao read secret-sync/queue
```

Use queue operation IDs from the queue summary or status output to inspect
individual operations:

```sh
bao read secret-sync/queue/<operation-id>
```

## Review remote state

Run reconcile plan for each important source path:

```sh
bao read secret-sync/reconcile/app/db/plan
```

Reconcile plan reads provider remote state and calculates local status without
changing local status or destination secrets. It is safe while the restore
guard is active.

When the plan reports ownership loss, collision, drift, missing remote objects,
or unexpected source versions, inspect the destination-side metadata before
retrying or acknowledging the guard. Provider guides describe where ownership
metadata is stored:

- AWS Secrets Manager: tags;
- Kubernetes Secrets: labels and annotations;
- GitLab project variables: variable description.

## Decide queued work handling

For each queued operation, decide whether to keep it, retry it, or cancel it.

Keep queued work when:

- the source version is still the intended source of truth;
- the association still owns the remote object;
- the destination policy still allows the source path and resolved remote name.

Cancel queued work when:

- the source version is stale after restore;
- the remote object is intentionally managed elsewhere;
- the association no longer needs to mutate the destination.

Retry queued work only after the destination config, source eligibility,
association, and remote ownership state are understood:

```sh
bao write -force secret-sync/queue/<operation-id>/retry
bao write -force secret-sync/queue/<operation-id>/cancel
```

## Refresh local status

After reviewing a reconcile plan, apply reconcile when local status needs to be
refreshed from provider read-state results:

```sh
bao write -force secret-sync/reconcile/app/db
```

Reconcile apply updates local status only. It does not write destination
secrets.

## Resume remote mutation

Acknowledge the restore guard only after the review is complete:

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
```

After acknowledgement, drain due work in bounded batches:

```sh
bao write secret-sync/queue/drain max_operations=10
```

Read status after each drain batch:

```sh
bao read secret-sync/status/app/db
```

## Evidence to keep

For audit or incident review, keep:

- restore or clone timestamp;
- plugin version and mount path;
- source paths reviewed;
- association IDs and destination names;
- reconcile plan output with secret payloads absent;
- queue operation IDs and final actions;
- restore guard acknowledgement time;
- destination-side ownership metadata.

Do not store source secret values, destination credentials, tokens, raw audit
records containing secrets, or full provider responses containing secret data.
