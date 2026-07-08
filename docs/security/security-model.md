# Security Model

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
info                            all roles that need API defaults or capabilities
config                          platform operators
config/restore-guard/*          platform operators
data/*                          app/team writers and readers
metadata/*                      app/team readers, writers, or delegated owners
sources/*                       app/team writers or delegated owners
destinations/*                  platform operators
associations/*                  platform operators or delegated app owners
queue/*                         platform operators
status/*                        app/team readers or platform operators
reconcile/<path>/plan           delegated owners or platform operators
reconcile/<path>                platform operators when applying status refresh
```

Use [Policy examples](policies.md) for concrete OpenBao policy snippets.

Association creation is the highest-risk authorization operation because it
causes a secret to leave OpenBao. It requires destination authority and, in
hardened posture, source eligibility.

Fresh mounts start with `security_posture=standard`. In that mode, a trusted
platform operator may own both `destinations/*` and `associations/*`, and
unconstrained destinations are valid for operator-managed sync. When
application owners receive association-management policy for their own source
prefixes, operators should set `security_posture=hardened`.

Source eligibility is local source metadata, not an implicit source-read
permission check. In hardened posture, the backend requires
`custom_metadata.syncable=true` before an enabled association can enqueue or
dispatch sync work. OpenBao policy must therefore control who can update
`metadata/<path>` or call `sources/<path>/enable`.

Delegated association owners do not need source payload read access unless the
deployment also wants them to inspect the secret value. Combine delegated
association access with app reader or writer policy only when that is intended.
`security_posture=hardened` makes this self-service model enforceable:
association create, enable, manual sync, reconcile, and queued dispatch refuse
destinations whose `allowed_source_path_prefixes` or
`allowed_resolved_name_prefixes` are empty, returning the
`destination_unconstrained` blocker. Hardened posture also rejects writes that
would create or update an unconstrained destination.

Destination authority can be proven through:

- OpenBao policy controlling who can write `destinations/*`;
- destination-level source path prefix constraints;
- destination-level resolved remote-name prefix constraints;
- provider capability checks during association plan, activation, manual sync,
  enable, and dispatch.

Event-triggered dispatch and `queue/drain` execute remote mutations only through
the durable queue dispatcher. They honor the global disabled flag and restore
guard, refuse unsafe OpenBao replication states, recover incomplete enqueue
intents before dispatch, and do not expose source payload data in responses.
`queue/drain` is an operator action because it can execute remote mutations for
all due operations in the queue.

## Confused-deputy controls

Implemented controls:

- `custom_metadata.syncable=true` before enabled association activation in
  hardened posture;
- opt-in `security_posture=hardened`, which requires constrained destinations
  for delegated association use;
- destination-level allowed source path prefixes;
- destination-level allowed resolved remote-name prefixes;
- required ownership metadata;
- plan output that shows remote object names and destination scope.

Delegated app owners must not be able to use a broad platform destination to
write arbitrary remote names. Destination records can constrain source paths and
resolved remote-name prefixes. In hardened posture, both constraint lists must be
present. The configured constraints are checked during association plan,
association activation, manual sync, enable, manual reconcile, background drift
read-state, and queued dispatch, so a destination policy tightened after enqueue
still blocks remote mutation and provider read-state.

## Source secret protection

- Secret values are stored only in plugin storage under the OpenBao barrier.
- Reads return only requested secret data, following OpenBao policy.
- Secret payloads do not appear in logs, status records, metrics, or error
  strings.
- Queue, status, plan, and reconcile responses can include versions,
  destination names, verification markers, and remote object identifiers, but
  not source values.
- Non-payload metadata, including source paths, resolved remote names, and
  Kubernetes data-map key names, can appear in validation and diagnostic
  responses. Grant plan, reconcile, status, and association read access only to
  roles that may inspect that metadata.
- Durable status records can include payload hashes for comparison and drift
  tracking, but payload hashes are not telemetry labels.
- Tests include canary secret values and assert they do not appear in
  logs, errors, queue, status, plan output, reconcile output, or metrics.

Source secret versions and destination credentials are stored under
seal-wrapped storage prefixes.

## Destination credential protection

- Destination credential paths are listed in `SealWrapStorage`.
- Sensitive fields are split from non-sensitive destination metadata.
- Reads of destination config redact sensitive values.
- Credential rotation is possible without recreating associations.
- Prefer workload identity and federated identity over static keys where the
  destination supports it.
- AWS destinations support SDK default credentials, assume-role auth, and
  web-identity auth. Assume-role `external_id` is stored in seal-wrapped
  sensitive config. Web-identity token contents are not stored by the backend;
  the configured token file must be mounted for the plugin process.
- `web_identity_token_file` and Kubernetes `kubeconfig_path` are public
  destination config file paths. A caller with `destinations/*` write access can
  point the plugin process at files that the process can read; contents are not
  stored or echoed by the backend. Treat destination write access as trusted
  platform-operator authority over these runtime file inputs.
- Static access keys and session tokens are recognized as sensitive fields but
  intentionally remain unsupported auth material.
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
- GitLab `base_url` rejects localhost, private, link-local, multicast, and
  unspecified hosts by default, and rechecks DNS answers at client creation
  time unless `allow_private_network=true` is set;
- Kubernetes token-auth `api_server` rejects localhost, private, link-local,
  multicast, and unspecified hosts by default, and rechecks DNS answers at
  client creation time unless `allow_private_api_server=true` is set;
- optional explicit allowlists are still required for hardened private endpoint
  deployments;
- AWS and GitLab provider-owned HTTP clients do not inherit ambient proxy
  configuration;
- GitLab provider redirects are disabled by the default provider HTTP client.

Provider clients use context timeouts and bounded network behavior where the
provider owns HTTP transport. AWS and GitLab providers use bounded default HTTP
clients; GitLab provider requests also use bounded response-body reads.

## Ownership and collision controls

The implemented safety posture is ownership-only overwrite. There is no API
setting that force-overwrites unowned remote objects.

Use [Ownership and safety](../concepts/ownership-and-safety.md) for the
operator-facing model behind ownership metadata, drift, collisions, stale
objects, and restore identity.

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
`REMOTE_OWNERSHIP_LOST` and does not overwrite. If ownership matches but the
remote payload differs from the current OpenBao source version, reconcile
reports `DRIFTED`. Operators can remove or inspect the remote object and then
run manual sync when OpenBao should recreate or repair the object.

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
beyond the explicit `data/*` read path. Requests that write `data/<path>` still
flow through OpenBao audit devices like other secret-engine writes; configure
and rotate audit HMAC keys according to the deployment's OpenBao audit policy.

For remote operations, the plugin persists and logs correlation metadata:

- OpenBao mount accessor or mount name;
- source path;
- source version;
- association ID;
- object ID;
- operation ID;
- destination reference;
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
- event-triggered, periodic, and manual-drain remote mutation is disabled while
  the guard is active;
- background drift detection may continue to update local status from provider
  read-state checks while the guard is active, but repair enqueue and dispatch
  remain blocked;
- `reconcile/<path>/plan` and `reconcile/<path>` remain available to inspect
  provider state before pushing restored data to destinations;
- explicit operator acknowledgement to resume sync;
- persisted restore epoch rotated on acknowledgement and included in provider
  ownership metadata where possible.

Use the operator runbook for restore review.

## Metrics

Instrument metrics through the OpenTelemetry metric API where practical. The
plugin must not store exporter credentials in plugin storage. Keep exporter and
collector setup as a deployment concern.

OpenTelemetry instrument names are documented in
[Observability](../operations/observability.md). Security-relevant instruments
include:

```text
openbao.secret_sync.queue.depth
openbao.secret_sync.queue.capacity
openbao.secret_sync.queue.utilization
openbao.secret_sync.operations
openbao.secret_sync.provider.requests
openbao.secret_sync.provider.request.duration
openbao.secret_sync.readiness.checks
openbao.secret_sync.remote_mutation.blocked
openbao.secret_sync.reconcile.runs
openbao.secret_sync.drift.repairs
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

Destination outages degrade sync status and queue progress, not local source
read/write availability. There is no synchronous provider-write mode.

## Rate limiting

Implemented rate-limit handling is based on provider error classification and
bounded queue retry. Provider `rate_limit` errors map to
`DESTINATION_RATE_LIMITED` status and retry-wait queue state. Queue capacity
limits accepted backlog before source writes commit.

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
- SBOM, dependency vulnerability scan, and dependency license report;
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
