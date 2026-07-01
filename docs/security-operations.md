# Security And Operations

Status: draft
Date: 2026-06-30

## Threat Model

Primary assets:

- local secret values stored in the plugin mount;
- destination credentials or workload identity configuration;
- association policy that decides where secrets are copied;
- remote destination secret values;
- queue and status records that may reveal operational metadata;
- ownership metadata that controls remote mutation safety.

Threats in scope:

- destination outage causing lost or duplicated sync operations;
- compromised destination credential being reused outside the plugin;
- accidental overwrite of existing remote secrets;
- app writer causing sync to a destination they should not control;
- SSRF through custom destination endpoints;
- secret value leakage through logs, status, metrics, errors, or tests;
- stale restore data overwriting newer remote destination state;
- replay of old queue operations after plugin or OpenBao restart;
- two plugin instances or restored clusters managing the same remote object.

Threats out of scope for the plugin:

- compromise of OpenBao root/operator credentials;
- compromise of OpenBao storage encryption or seal material;
- malicious OpenBao core binary;
- full compromise of destination provider control planes.

## Authorization Model

OpenBao policies remain the primary authorization layer. The plugin must expose
separate path surfaces so operators can delegate safely:

```text
data/*                    app/team writers and readers
metadata/*                app/team readers or delegated owners
associations/*            platform operators or delegated app owners
destinations/*            platform operators
queue/*                   platform operators
reconcile*                platform operators
status/*                  app/team readers or platform operators
config                    platform operators
```

Association creation is the highest-risk authorization operation because it
causes a secret to leave OpenBao. It must require source eligibility and
destination authority.

Source eligibility can be proven through:

- read permission on `data/<path>` at association create/update time;
- required source metadata such as `syncable=true`;
- an operator-only association path that bypasses delegated creation.

Destination authority can be proven through:

- destination path policy;
- mount-level destination allowlist;
- association-level provider constraints;
- optional required metadata such as team, environment, or owner.

`queue/drain` is an operator action because it can execute remote mutations for
all due operations in the durable queue. It must be policy-gated like retry and
cancel operations, honors the global disabled flag and restore guard, refuses
unsafe OpenBao replication states, recovers incomplete enqueue intents before
dispatch, and must not expose source payload data in responses.

## Confused-Deputy Controls

The plugin should support:

- `custom_metadata.syncable=true` before enabled association activation;
- destination allowlist per mount;
- maximum destinations per secret;
- maximum secret size;
- forbidden path patterns;
- required ownership metadata;
- plan output that clearly shows remote object names and destination scope.

Delegated app owners should not be able to use a broad platform destination to
write arbitrary remote names. Name templates and allowed prefixes should be
constrained per destination or per association policy.

## Source Secret Protection

- Secret values are stored only in plugin storage under the OpenBao barrier.
- Reads return only requested secret data, following OpenBao policy.
- Secret payloads never appear in logs, status records, metrics, or error
  strings.
- Status records may include payload hashes, versions, destination names, and
  remote object identifiers, but not values.
- Tests must include canary secret values and assert they do not appear in
  logs, errors, status, plan output, or metrics.

Open question: local source versions may optionally be seal-wrapped. Destination
credentials must be seal-wrapped.

## Destination Credential Protection

- Destination credential paths must be listed in `SealWrapStorage`.
- Sensitive fields must be split from non-sensitive destination metadata.
- Reads of destination config must redact sensitive values.
- Credential rotation must be possible without recreating associations.
- Prefer workload identity and federated identity over static keys where the
  destination supports it.
- Current AWS support stores assume-role `external_id` in seal-wrapped
  sensitive config. Static access keys and session tokens are recognized as
  sensitive fields but intentionally remain unsupported auth material.
- Derived short-lived tokens should be cached only in memory and invalidated on
  destination config updates.
- Provider SDK default credential chains must be explicitly allowed or disabled
  by destination config; no surprising ambient credentials.

## SSRF And Network Controls

Destination configs that include custom endpoints must be explicit and
validated:

- `endpoint_url` requires an `endpoint_policy`;
- `endpoint_policy=local` is for development endpoints such as LocalStack and
  may use `http` only for local hosts;
- `endpoint_policy=private` requires `https` and rejects direct loopback,
  link-local, multicast, and unspecified addresses;
- default AWS endpoints remain preferred for production;
- DNS resolution still needs to be checked at connection time, not only at
  config time;
- optional explicit allowlists are still required for hardened private endpoint
  deployments;
- redirects disabled or constrained;
- proxy behavior explicit in destination config.

Provider clients should use context timeouts and must not allow unbounded
response bodies.

## Ownership And Collision Controls

Default safety mode is `overwrite_owned_only`.

Remote ownership metadata should include the fields supported by the provider:

```text
openbao-sync=true
openbao-sync-plugin-instance=<plugin-instance-id>
openbao-sync-restore-epoch=<restore-epoch>
openbao-sync-association=<association-id>
openbao-sync-path=<source-path>
openbao-sync-version=<source-version>
openbao-sync-object=<object-id>
openbao-sync-payload-sha256=<hash>
```

AWS Secrets Manager tags, Kubernetes annotations, and GitLab variable
descriptions include plugin instance and restore epoch metadata. Provider
ownership checks require these values to match the current mount identity when
the request carries them; missing or mismatched values are treated as ownership
loss.

If ownership metadata is absent or conflicts, the plugin reports
`REMOTE_OWNERSHIP_LOST` or `DRIFTED` and does not overwrite unless policy
explicitly allows weaker behavior.

Current delete behavior:

- association `delete_mode` defaults to `retain`;
- remote delete is enqueued only for `delete_mode=delete`;
- provider delete must prove ownership or return an `ownership` error;
- local source delete cancels queued upserts for the deleted version before any
  remote delete is processed.

## Audit Model

OpenBao audit devices capture requests to plugin paths according to normal
OpenBao audit behavior. The plugin must not add secret values to responses
beyond the explicit `data/*` read path.

For remote operations, the plugin should persist and log correlation metadata:

- OpenBao mount accessor or mount name;
- source path;
- source version;
- association ID;
- object ID;
- operation ID;
- destination type and name;
- remote object identifier;
- result state and error class.

This gives operators enough evidence to correlate OpenBao audit logs with
destination-side audit logs without recording secret payloads.

## Restore And Clone Protection

OpenBao backup/restore captures local secret versions, destination configs,
associations, queue, and status. After restore, blind reconciliation could
overwrite newer remote state.

The plugin needs a safe mode:

- `restore_guard=true` after detected or operator-declared restore;
- background and manual-drain remote mutations disabled while the guard is
  active;
- `reconcile/<path>/plan` and `reconcile/<path>` before pushing restored data
  to destinations;
- explicit operator acknowledgement to resume sync;
- persisted restore epoch rotated on acknowledgement and included in provider
  ownership metadata where possible.

Operators should be able to choose how to handle existing outbox operations
after restore:

- cancel all pending operations;
- re-plan all pending operations;
- resume only operations that still match current local source versions.

## Metrics

Instrument metrics through the OpenTelemetry metric API where practical. The
plugin must not store exporter credentials in plugin storage; exporter and
collector setup should remain a deployment concern.

Initial OpenTelemetry instruments:

```text
openbao.secret_sync.queue.depth{state}
openbao.secret_sync.operations{operation,result,error_class,destination_type,granularity}
openbao.secret_sync.provider.requests{provider,operation,result,error_class}
openbao.secret_sync.provider.request.duration{provider,operation,result,error_class}
openbao.secret_sync.reconcile.runs{result,error_class,destination_type,granularity}
openbao.secret_sync.restore_guard.active
```

Prometheus exporters may translate instrument names and units into Prometheus
series such as `openbao_secret_sync_operations_total`.

Metric labels must not include secret values, high-cardinality source paths,
resolved remote names, destination names, association ids, operation ids,
payload hashes, ARNs, or cloud account ids by default.

## Logs

Logs should include:

- operation ID;
- association ID;
- destination reference;
- source path;
- source version;
- error class;
- retry schedule.

Logs must not include:

- secret payloads;
- destination credential material;
- authorization tokens;
- full provider responses when they may contain secret data.

## Health And Readiness

Health should be layered:

- plugin process healthy;
- mount configured;
- queue writable and below critical capacity;
- destination config valid;
- destination reachable;
- destination authorized;
- restore guard state visible.

Destination outages should degrade sync status, not local secret read/write
availability, unless a future explicit synchronous mode is enabled.

## Rate Limiting

Rate limits should exist at three levels:

- global plugin concurrency;
- per-destination concurrency;
- per-provider API rate.

Retries should use jitter to avoid synchronized bursts after external outages.
Rate-limit state must be visible in status and queue output.

Current automatic retry policy:

- retry provider `rate_limit` and `unavailable` errors only;
- use a bounded attempt budget before marking the operation terminal;
- keep validation, authentication, authorization, ownership, collision, and
  capacity failures terminal until an operator changes configuration or retries
  manually;
- manual queue retry resets the attempt counter and schedules the operation
  immediately.

## Packaging And Release

The plugin should be shipped like other OpenBao external plugins:

- reproducible Go builds where practical;
- checksums for every release artifact;
- SBOM and dependency vulnerability scan;
- signed release artifacts if the release process supports it;
- documented `bao plugin register` flow;
- documented minimum OpenBao version;
- documented plugin file ownership and permissions;
- changelog with migration notes for storage schema changes.

Storage schema changes must be explicit and backward compatible where possible.
If a migration is required, the plugin should fail closed with a clear error
until an operator runs an explicit migration or mounts a compatible version.

## Runbooks

The practical operator workflow lives in
[Operator runbook](operator-runbook.md). This document records the security and
operations requirements that the runbook should continue to satisfy.

MVP runbooks should cover:

- install, register, and mount;
- configure destination;
- create association;
- validate destination;
- inspect queue and status;
- drain due queue work for deterministic verification or controlled catch-up;
- retry failed operation;
- pause and resume sync;
- restore-safe reconciliation;
- rotate destination credentials;
- handle remote ownership loss;
- handle destination rate limits;
- handle queue capacity pressure;
- uninstall and retain, orphan, or delete owned remote secrets.
