# Operator runbook

This runbook covers operational workflows for the OpenBao Secret Sync plugin.
It assumes the plugin is mounted at `secret-sync/`; adjust paths if the mount
name differs.

## First checks

Confirm the mount responds:

```sh
bao read secret-sync/config
```

If `restore_guard=true`, remote mutation is blocked until restore or clone
review is complete and the guard is acknowledged:

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
```

Check mount-wide pause and queue capacity:

```sh
bao read secret-sync/config
```

Pause or resume remote mutation:

```sh
bao write secret-sync/config disabled=true
bao write secret-sync/config disabled=false
```

## Destination checks

Read destination config. Sensitive fields must be redacted:

```sh
bao read secret-sync/destinations/<type>/<name>
```

Check destination readiness:

```sh
bao read secret-sync/destinations/<type>/<name>/check
```

Validate static destination configuration:

```sh
bao read secret-sync/destinations/<type>/<name>/validate
```

Check destination reachability and authorization:

```sh
bao read secret-sync/destinations/<type>/<name>/health
```

Use validation for configuration mistakes and health for runtime dependency
state. A destination can validate correctly but still be unhealthy because of
network, IAM, token, RBAC, or provider availability issues.

## Source and association checks

When strict source opt-in is enabled, confirm the source path is explicitly
syncable:

```sh
bao write -force secret-sync/sources/app/db/enable
```

This is required only when `require_source_opt_in=true`. Mounts default to
`require_source_opt_in=false`.

To inspect the underlying metadata:

```sh
bao read secret-sync/metadata/app/db
```

Read the current source version:

```sh
bao read secret-sync/data/app/db
```

Check source readiness:

```sh
bao read secret-sync/sources/app/db/check
```

Plan the association before creating or changing it:

```sh
bao write secret-sync/associations/app/db/plan \
  destination_type=<type> \
  destination_name=<name> \
  resolved_name=<remote-name>
```

`secret-path`, `json`, `retain`, and `enabled=true` are the defaults.
Association create and plan responses also include a `defaults` object so the
implicit shape is visible in CLI and API output.

When updating an existing association, omitted optional fields keep the stored
association values when the source path and destination identify a single
existing association. This prevents partial updates from changing granularity,
name template, delete mode, or enabled state by accident. Use the read output
above when you need to make the update shape explicit.

Current-version source lifecycle endpoints participate in sync. `DELETE
data/<path>`, `delete/<path>`, and `destroy/<path>` cancel stale queued upserts
and enqueue remote deletes for associations with `delete_mode=delete`.
`undelete/<path>` on the current version queues replacement upserts for enabled
associations.

Read existing associations:

```sh
bao read secret-sync/associations/app/db
```

Disable, enable, or manually sync one association:

```sh
bao write -force secret-sync/associations/app/db/<association-id>/disable
bao write -force secret-sync/associations/app/db/<association-id>/enable
bao write -force secret-sync/associations/app/db/<association-id>/sync
```

## Queue operations

Read the queue summary:

```sh
bao read secret-sync/queue
```

Queue summaries include `capacity` and `utilization`. Treat sustained high
utilization as backpressure: increase drain frequency, reduce producer rate, or
raise `queue_capacity` after checking storage and provider limits.
Set `queue_capacity=0` only for a deliberate enqueue freeze; existing queued
work can still drain.
Successful operations are removed from the queue after object status is
persisted; use `status/<path>` rather than `queue/<operation-id>` to confirm
completed sync.

For deterministic local testing or controlled catch-up, drain due operations:

```sh
bao write secret-sync/queue/drain max_operations=10
```

Read one operation:

```sh
bao read secret-sync/queue/<operation-id>
```

Retry or cancel one operation:

```sh
bao write -force secret-sync/queue/<operation-id>/retry
bao write -force secret-sync/queue/<operation-id>/cancel
```

Cancel discards queued work; it is not retained in the queue summary.

`queue/drain` can execute remote mutations. Keep it operator-scoped.

## Operational signals

The plugin emits OpenTelemetry metric API calls only. Exporter setup remains an
OpenBao deployment concern.

Useful alert inputs:

- `openbao.secret_sync.restore_guard.active` stays `1` after an expected
  restore or deployment review window;
- `openbao.secret_sync.queue.utilization` remains high or increases while
  `openbao.secret_sync.queue.depth{state="pending"}` is not draining;
- `openbao.secret_sync.remote_mutation.blocked` increases with reason
  `disabled`, `restore_guard`, or `replication_state`;
- `openbao.secret_sync.provider.requests` failures increase by provider,
  operation, or error class;
- `openbao.secret_sync.readiness.checks` failures identify onboarding blockers
  without exposing source paths or destination names.

## Status and reconcile

Read per-source status:

```sh
bao read secret-sync/status/app/db
```

Use JSON output when copying identifiers into commands:

```sh
bao read -format=json secret-sync/status/app/db | jq .data
```

Plan reconcile without changing local status or remote state:

```sh
bao read secret-sync/reconcile/app/db/plan
```

Apply reconcile to refresh local status from provider read-state:

```sh
bao write -force secret-sync/reconcile/app/db
```

Reconcile reads remote state. It does not write destination secrets and is safe
to use while the restore guard is active.

## Common failure classes

`DESTINATION_AUTH_ERROR` or provider error class `authn`:

- check destination credential material or workload identity;
- rerun destination readiness, validation, and health;
- rotate the destination credential if compromise is suspected.

`DESTINATION_POLICY_ERROR` or provider error class `authz`:

- check IAM, RBAC, token scopes, project permissions, or namespace access;
- verify the destination can perform the requested create, update, read-state,
  and delete operations.

`DESTINATION_RATE_LIMITED` or provider error class `rate_limit`:

- inspect queue retry state;
- allow automatic retry to progress if attempts remain;
- reduce drain batch size during manual catch-up.

`DESTINATION_UNAVAILABLE` or provider error class `unavailable`:

- verify provider endpoint reachability;
- check proxy, DNS, private endpoint, or local test stack health;
- retry after the destination recovers.

`REMOTE_OWNERSHIP_LOST` or provider error class `ownership`:

- inspect the remote object metadata before retrying;
- decide whether the remote object was intentionally taken over;
- create a new association or remote name instead of forcing overwrite unless
  an operator explicitly accepts that risk.

`VALIDATION_ERROR`:

- check destination config fields;
- check provider name rules for the rendered remote object name;
- check payload size and granularity support.

`QUEUE_BLOCKED`:

- read `secret-sync/config` for mount-wide pause or restore guard;
- check queue capacity;
- verify the association and destination are enabled.

## Restore or clone review

Use [Restore and clone review](restore-and-clone.md) for the full review
workflow.

After restore or clone, keep mutation blocked until remote ownership has been
reviewed:

```sh
bao read secret-sync/config
bao read secret-sync/reconcile/app/db/plan
```

For each important source path:

1. Review local source version and association state.
2. Run reconcile plan.
3. Inspect remote ownership and payload hash status.
4. Cancel, retry, or re-plan queued operations as needed.
5. Acknowledge restore guard only after the destination state is understood.

Resume remote mutation:

```sh
bao write -force secret-sync/config/restore-guard/acknowledge
```

## Evidence to capture

For troubleshooting or issue reports, capture:

- mount path and plugin version;
- destination type and redacted destination config;
- association ID and source path;
- source version, not secret value;
- operation ID and queue state;
- status output with payload values removed if present in source reads;
- provider-side object metadata, not secret value;
- exact error class and message.

Do not paste secret payloads, provider tokens, static credentials, GitLab
tokens, AWS external IDs, kubeconfigs, or raw audit records containing secrets.
