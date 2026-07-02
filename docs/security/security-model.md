# Security and operations

## Threat model

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
- app writer causing sync to an unauthorized destination;
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

## Authorization model

OpenBao policies remain the primary authorization layer. The plugin exposes
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

Use [Policy examples](policies.md) for concrete OpenBao policy snippets.

Association creation is the highest-risk authorization operation because it
causes a secret to leave OpenBao. It requires destination authority and, when
`require_source_opt_in=true`, source eligibility.

Source eligibility can be proven through:

- read permission on `data/<path>` at association create/update time;
- required source metadata such as `syncable=true` when strict source opt-in is
  enabled;
- an operator-only association path that bypasses delegated creation.

Destination authority can be proven through:

- destination path policy;
- destination-level source path prefix constraints;
- destination-level resolved remote-name prefix constraints;
- association-level provider constraints;
- optional required metadata such as team, environment, or owner.

`queue/drain` is an operator action because it can execute remote mutations for
all due operations in the durable queue. It is policy-gated like retry and
cancel operations, honors the global disabled flag and restore guard, refuses
unsafe OpenBao replication states, recovers incomplete enqueue intents before
dispatch, and does not expose source payload data in responses.

## Confused-deputy controls

Implemented controls:

- optional `custom_metadata.syncable=true` before enabled association
  activation when `require_source_opt_in=true`;
- destination-level allowed source path prefixes;
- destination-level allowed resolved remote-name prefixes;
- required ownership metadata;
- plan output that shows remote object names and destination scope.

Delegated app owners must not be able to use a broad platform destination to
write arbitrary remote names. Destination records can constrain source paths and
resolved remote-name prefixes. These constraints are checked during association
plan, association activation, manual sync, enable, and queued dispatch, so a
destination policy tightened after enqueue still blocks remote mutation.

## Source secret protection

- Secret values are stored only in plugin storage under the OpenBao barrier.
- Reads return only requested secret data, following OpenBao policy.
- Secret payloads do not appear in logs, status records, metrics, or error
  strings.
- Status records can include versions, destination names, and
  remote object identifiers, but not values.
- Tests include canary secret values and assert they do not appear in
  logs, errors, status, plan output, or metrics.

Source secret versions and destination credentials are stored under
seal-wrapped storage prefixes.

## Destination credential protection

- Destination credential paths are listed in `SealWrapStorage`.
- Sensitive fields are split from non-sensitive destination metadata.
- Reads of destination config redact sensitive values.
- Credential rotation is possible without recreating associations.
- Prefer workload identity and federated identity over static keys where the
  destination supports it.
- AWS destinations store assume-role `external_id` in seal-wrapped sensitive
  config. Static access keys and session tokens are recognized as sensitive
  fields but intentionally remain unsupported auth material.
- GitLab API tokens and Kubernetes bearer tokens are stored in seal-wrapped
  sensitive config and redacted on destination reads.
- Derived short-lived tokens are cached only in memory and invalidated on
  destination config updates.
- Provider SDK default credential chains are explicitly allowed or disabled
  by destination config; no surprising ambient credentials.

## SSRF and network controls

Destination configs that include custom endpoints are explicit and
validated:

- `endpoint_url` requires an `endpoint_policy`;
- `endpoint_policy=local` is for development endpoints such as LocalStack and
  may use `http` only for local hosts;
- `endpoint_policy=private` requires `https` and rejects direct loopback,
  link-local, multicast, and unspecified addresses;
- default AWS endpoints remain preferred for production;
- AWS private custom endpoints are rechecked at client creation time and DNS
  answers resolving to loopback, link-local, multicast, or unspecified
  addresses are rejected;
- optional explicit allowlists are still required for hardened private endpoint
  deployments;
- AWS and GitLab provider-owned HTTP clients do not inherit ambient proxy
  configuration;
- GitLab provider redirects are disabled by the default provider HTTP client.

Provider clients use context timeouts and bounded network behavior where the
provider owns HTTP transport. AWS and GitLab providers use bounded default HTTP
clients; GitLab provider requests also use bounded response-body reads.

## Ownership and collision controls

Default safety mode is `overwrite_owned_only`.

Remote ownership metadata includes the fields supported by the provider:

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

Delete behavior:

- association `delete_mode` defaults to `retain`;
- remote delete is enqueued only for `delete_mode=delete`;
- provider delete must prove ownership or return an `ownership` error;
- deleting, soft-deleting, or destroying the current local source version
  cancels queued upserts for that version before any remote delete is processed;
- undeleting the current local source version queues replacement upserts for
  enabled associations.

## Audit model

OpenBao audit devices capture requests to plugin paths according to normal
OpenBao audit behavior. The plugin does not add secret values to responses
beyond the explicit `data/*` read path.

For remote operations, the plugin persists and logs correlation metadata:

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

## Restore and clone protection

OpenBao backup/restore captures local secret versions, destination configs,
associations, queue, and status. After restore, blind reconciliation could
overwrite newer remote state.

The plugin uses a restore guard:

- fresh mounts start with `restore_guard=false`;
- operators can set `restore_guard=true` after restore or clone review starts;
- background and manual-drain remote mutations disabled while the guard is
  active;
- `reconcile/<path>/plan` and `reconcile/<path>` before pushing restored data
  to destinations;
- explicit operator acknowledgement to resume sync;
- persisted restore epoch rotated on acknowledgement and included in provider
  ownership metadata where possible.

Use the operator runbook for restore review.

## Metrics

Instrument metrics through the OpenTelemetry metric API where practical. The
plugin must not store exporter credentials in plugin storage. Keep exporter and
collector setup as a deployment concern.

OpenTelemetry instruments:

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

Metric labels do not include secret values, high-cardinality source paths,
resolved remote names, destination names, association ids, operation ids,
payload hashes, ARNs, or cloud account ids by default.

## Logs

Logs include:

- operation ID;
- association ID;
- destination reference;
- source path;
- source version;
- error class;
- retry schedule.

Logs do not include:

- secret payloads;
- destination credential material;
- authorization tokens;
- full provider responses when they may contain secret data.

## Health and readiness

Health is layered:

- plugin process healthy;
- mount configured;
- queue writable and below critical capacity;
- destination config valid;
- destination reachable;
- destination authorized;
- restore guard state visible.

Destination outages degrade sync status, not local secret read/write
availability, unless a separate synchronous mode is enabled.

## Rate limiting

Rate limits are tracked at these levels for production readiness:

- global plugin concurrency;
- per-destination concurrency;
- per-provider API rate.

Rate-limit state is visible in status and queue output.

Automatic retry policy:

- retry provider `rate_limit` and `unavailable` errors only;
- use a bounded attempt budget before marking the operation terminal;
- keep validation, authentication, authorization, ownership, collision, and
  capacity failures terminal until an operator changes configuration or retries
  manually;
- manual queue retry resets the attempt counter and schedules the operation
  immediately.

## Packaging and release

The plugin is shipped like other OpenBao external plugins:

- reproducible Go builds where practical;
- checksums for every release artifact;
- SBOM and dependency vulnerability scan;
- signed release artifacts;
- documented `bao plugin register` flow;
- documented minimum OpenBao version;
- documented plugin file ownership and permissions;
- changelog with migration notes for storage schema changes.

Storage schema changes are explicit and backward compatible where possible.
If a migration is required, the plugin fails closed with a clear error until an
operator runs an explicit migration or mounts a compatible version.

## Runbooks

The practical operator workflow lives in
[Operator runbook](../operations/operator-runbook.md). This document records
the security and operations requirements that the runbook satisfies.

Runbooks cover:

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
