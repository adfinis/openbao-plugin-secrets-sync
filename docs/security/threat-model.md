---
verifiedBy:
  - internal/backend/backend.go
  - internal/backend/path_destinations.go
  - internal/backend/association_policy.go
  - internal/backend/provider_runtime_cache.go
  - internal/backend/storage_records.go
  - internal/backend/redaction_test.go
  - internal/backend/dispatcher.go
  - internal/backend/event_dispatch.go
  - internal/payload/payload.go
  - internal/providers/endpointguard/endpointguard.go
  - internal/providers/awssecretsmanager/provider.go
  - internal/providers/gitlab/project_variables.go
  - internal/providers/kubernetessecrets/provider.go
  - internal/observability/observability.go
  - .github/workflows/release.yml
  - SECURITY.md
---

# Threat model

Secret Sync copies secret values out of OpenBao into external systems. Its
security rests on two controls: OpenBao policy decides who can define where a
secret goes, and the plugin refuses to overwrite remote objects that it cannot
prove it owns. Report vulnerabilities as described in
[SECURITY.md](../../SECURITY.md).

## Assets

- Source secret values stored in the plugin mount.
- Destination credentials: GitLab and Kubernetes tokens, the AWS assume-role
  `external_id`, and the process identity used by ambient auth modes.
- Destination and association records, which decide where secrets are copied.
- Secret values written to remote destinations.
- Queue and status records, which reveal source paths, remote names, versions,
  and payload hashes.
- Remote ownership metadata, which decides whether the plugin may mutate a
  remote object.

## Threats

In scope:

- A delegated application owner syncing a source to a destination or remote
  name that they are not authorized to use.
- A destination writer pointing the plugin at internal network endpoints,
  files, or ambient credentials.
- Accidental overwrite of an existing remote secret that the plugin does not
  own.
- Secret values leaking through responses, logs, metrics, or error text.
- A destination outage causing lost or duplicated sync work.
- A restored or cloned mount overwriting newer remote state, or two mounts
  managing the same remote object.
- A compromised destination credential being reused outside the plugin.
- Tampered release artifacts. Verify them as described in
  [Verify release artifacts](../get-started/install.md#verify-release-artifacts).

Out of scope:

- Compromise of OpenBao root or operator credentials.
- Compromise of OpenBao storage encryption or seal material.
- A malicious OpenBao core binary.
- Full compromise of a destination provider control plane.

## Authorization boundaries

OpenBao policy is the primary authorization layer. The plugin adds no identity
checks of its own, so the authority each path grants is the authority of the
policy that reaches it.

| Path | Authority |
| --- | --- |
| `data/*`, `metadata/*` | Read or change source values and version metadata. |
| `sources/<path>/enable`, `sources/<path>/disable` | Opt a source in to or out of sync in hardened posture. |
| `destinations/*` (write) | Choose provider endpoints, credentials, runtime file paths, and delegation constraints. |
| `associations/*` (write) | Bind sources to destinations and choose remote names. |
| `queue/drain`, `queue/<operation-id>/retry`, `queue/<operation-id>/cancel` | Execute, reschedule, or cancel remote mutations. `queue/drain` runs due operations for any source in the mount. |
| `config`, `config/restore-guard/acknowledge` | Pause the mount, change posture, and release the restore guard. |
| `status/*`, `reconcile/*`, `associations/*` (read), `queue/*` (read) | Inspect operational metadata, not source values. |

> [!WARNING]
> Write access to both `destinations/*` and `associations/*` is full
> exfiltration authority for every source path that the role can associate. The
> role can create a destination it controls and bind sources to it. Grant this
> combination only to trusted platform operators, or split it as shown in
> [Split platform roles](policies.md#split-platform-roles).

Destination write access is also authority over the plugin process:

- `web_identity_token_file` (AWS) and `kubeconfig_path` (Kubernetes) are public
  config fields. The plugin process reads whatever file they name. The backend
  does not store or return the file contents.
- AWS `auth_mode=default` and `assume_role` start from the AWS SDK default
  credential chain of the plugin process. Kubernetes uses `in_cluster` auth,
  which runs as the pod service account, when no `auth_mode`, `token`,
  `api_server`, or `kubeconfig_path` is set.
- Endpoint fields and their private-network opt-ins decide which hosts the
  plugin connects to. See [Endpoint address policy](#endpoint-address-policy).

Treat `destinations/*` write access as trusted platform-operator authority.

Association creation is the step that causes a secret to leave OpenBao. Source
sync eligibility is plugin metadata on the source path, not a check that the
caller can read the source value. Delegated association owners do not need
`data/*` read access. Grant it only when they must also see the value.

[Delegate to application owners](../configure/delegate.md) owns the hardened
posture that makes delegated association management enforceable.
[Policy examples](policies.md) shows role policies.

## Confused-deputy controls

- Destination constraints. `allowed_source_path_prefixes` and
  `allowed_resolved_name_prefixes` limit which sources and remote names can use
  a destination. The backend rechecks them before every provider read-state
  and every queued mutation, so tightening a destination after work is queued
  still blocks that work. See
  [Constrain destinations](../configure/delegate.md#constrain-destinations).
- Hardened posture. It requires both constraint lists on every destination and
  explicit source sync enablement before remote mutation. See
  [Enable hardened posture](../configure/delegate.md#enable-hardened-posture).
- Ownership-only overwrite. No API setting force-overwrites an unowned remote
  object. A missing or mismatched ownership marker stops the mutation. See
  [Ownership and drift](../concepts/ownership-and-drift.md).
- Name reservations. Two associations on one destination cannot claim the same
  remote name. See
  [Name reservations](../configure/associations.md#name-reservations).
- Plans. `associations/<path>/plan` shows resolved remote names and blockers
  before an association exists.

## Secret and credential protection

Source versions and destination sensitive config are stored under seal-wrapped
storage prefixes inside the OpenBao barrier.

Sensitive destination fields are `external_id` for `aws-sm` and `token` for
`gitlab` and `k8s`. The backend stores them in a record separate from the
public destination config. Destination reads return only a `sensitive_config`
summary with `redacted`, `configured`, and the key names. Destination writes
reject fields that the schema does not define, so the backend never stores
static AWS access keys.

To rotate a stored token, write the new value to the existing destination. The
write replaces the sensitive record and invalidates the cached provider client.
Associations reference the destination by name and do not change. The plugin
caches provider clients and any credentials derived from them, such as STS
session credentials, only in memory.

Source values appear only in `data/<path>` reads and in provider requests.
Backend tests write a canary value and assert that it does not appear in
destination reads, association plan and write responses, queue reads, queue
drain, status, or reconcile responses. GitLab HTTP errors report only the
status code, and the provider reads at most 1 MiB of a GitLab response body.

Plan, reconcile, status, queue, and association responses do contain
operational metadata: source paths, resolved remote names, Kubernetes data key
names, versions, and payload hashes. Grant read access to those paths only to
roles that may see that metadata.

## Endpoint address policy

GitLab `base_url` and Kubernetes `api_server` in token auth use a shared address
policy. Without `allow_private_network=true` (GitLab) or
`allow_private_api_server=true` (Kubernetes), the provider rejects:

- the names `localhost` and `*.localhost`;
- addresses that are not global unicast: loopback, link-local, multicast,
  unspecified, and IPv4 broadcast;
- private addresses: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, and
  `fc00::/7`; and
- these special-purpose ranges.

| Family | Restricted ranges |
| --- | --- |
| IPv4 | `0.0.0.0/8`, `100.64.0.0/10`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `240.0.0.0/4` |
| IPv6 | `::/96`, `64:ff9b:1::/48`, `100::/64`, `100:0:0:1::/64`, `2001::/23`, `2001:db8::/32`, `2002::/16`, `3fff::/20`, `5f00::/16`, `fec0::/10` |

The ranges come from entries that the
[IANA IPv4 special-purpose registry](https://www.iana.org/assignments/iana-ipv4-special-registry/)
and
[IANA IPv6 special-purpose registry](https://www.iana.org/assignments/iana-ipv6-special-registry/)
do not mark globally reachable, plus deprecated IPv4-compatible (`::/96`) and
site-local (`fec0::/10`) IPv6. The checked-in list was reviewed against the
registries on 2026-10-22.

The most specific matching range decides. These globally reachable assignments
stay allowed inside restricted parents: `192.0.0.9`, `192.0.0.10`,
`2001:1::1`, `2001:1::2`, `2001:1::3`, `2001:3::/32`, `2001:4:112::/48`,
`2001:20::/28`, and `2001:30::/28`.

IPv4-mapped IPv6 addresses use the IPv4 policy. Addresses in the well-known
NAT64 prefix `64:ff9b::/96` use the policy of their embedded IPv4 address, as
[RFC 6052 section 3.1](https://www.rfc-editor.org/rfc/rfc6052#section-3.1)
requires. An IPv6 zone identifier does not change the classification.

The provider applies the policy at three points:

1. Config validation rejects `localhost` names and restricted IP literals.
2. Client creation resolves the host name and rejects the destination if any
   address in the answer is restricted.
3. Every new connection resolves the host name again, rejects an answer that
   contains any restricted address, and dials an approved IP directly. DNS
   resolution times out after 5 seconds.

Guarded clients ignore proxy environment variables, because a proxy would
resolve the host outside the guard. The GitLab client never follows redirects
and never uses a proxy, with or without the opt-in.

The opt-ins remove all address checks for that destination. Enable them only
for an approved endpoint. Kubernetes `in_cluster` and `kubeconfig` auth do not
use this policy. They, and token auth with `allow_private_api_server=true`,
use the client-go default transport, which honors proxy environment variables.

AWS custom endpoints use a separate `endpoint_policy` described in
[Sync to AWS Secrets Manager](../configure/aws-secrets-manager.md). AWS clients
never use a proxy. STS calls for `assume_role` and `web_identity` are not
subject to the custom endpoint policy.

Provider HTTP clients time out after 30 seconds. Kubernetes clients use a
30-second request timeout unless the kubeconfig sets one.

This policy classifies addresses. It does not prove Internet routability or
inspect deployment routes, network-specific translation prefixes, or gateways.
Enforce the deployment's destination boundary with network egress controls.

## Ownership metadata exposure

Each provider stores ownership metadata in clear text on the remote object, in
AWS tags, Kubernetes annotations, or GitLab variable descriptions. The fields
are listed in
[Ownership metadata](../concepts/ownership-and-drift.md#ownership-metadata).

Anyone who can read destination metadata can see the stored fields, which
include source paths, versions, object IDs, the mount UUID, the restore epoch,
and the payload hash.
The payload hash is an unsalted SHA-256 of the payload bytes, so a low-entropy
secret value can be guessed offline from it. Status records hold the same hash.

- Do not put sensitive information in source paths or remote names.
- Scope destination metadata read permissions to roles that need them.
- Treat `status/*` read access as access to payload hashes.

## Audit and logs

OpenBao audit devices record requests to plugin paths like any other secret
engine, including `data/<path>` writes. Configure audit HMAC keys according to
your OpenBao audit policy. Responses carry source values only on `data/*`
reads.

The plugin logs only two events: a failed event-triggered dispatch pass, and a
warning when background drift repair for one object exceeds a repair count
threshold. It does not log per-operation details. Per-operation correlation evidence
lives in queue and status records instead: operation ID, source path, source
version, association ID, object ID, destination reference, resolved remote
name, state, and error class. Use it to correlate OpenBao audit entries with
destination-side audit logs.

The preview exports no plugin metrics. Metric attributes are limited to
low-cardinality values such as operation, result, error class, and destination
type. See [Observability internals](../development/observability.md).

## Restore and clone

An OpenBao snapshot contains source values, destination config including
sensitive fields, associations, queue, and status. A restored or cloned mount
carries the earlier ownership identity, so the providers treat its remote
objects as owned and it can overwrite newer remote state or compete with the
original mount.

> [!WARNING]
> The plugin does not detect a restore or a clone. A snapshot can restore an
> inactive restore guard. Fence destination writes outside the restored storage
> before the restored mount starts, then follow
> [Review a restore or clone](../operate/restore-and-clone.md).

[Mutation gates](../concepts/ownership-and-drift.md#mutation-gates) describes
what the restore guard and pause block. Neither cancels a provider request that
is already in progress.

Remote mutation is blocked on performance secondary, performance standby, DR
secondary, and bootstrapping replication states, unless the mount is local.
