---
verifiedBy:
  - internal/backend/backend.go
  - internal/backend/path_info.go
  - internal/backend/path_config.go
  - internal/backend/path_data.go
  - internal/backend/path_metadata.go
  - internal/backend/path_versions.go
  - internal/backend/path_sources.go
  - internal/backend/path_destinations.go
  - internal/backend/path_associations.go
  - internal/backend/path_queue.go
  - internal/backend/path_reconcile.go
  - internal/backend/path_status.go
  - internal/backend/existence_checks.go
---

# Policy examples

These OpenBao policies cover common roles for a Secret Sync mount at
`secret-sync/`. Each example uses `apps/team-a` as the delegated source prefix.
Adapt the prefixes and wildcards to your deployment.

Keep these path rules in mind when you adapt a policy:

- OpenBao checks `create` and `update` separately for `data/<path>`,
  `metadata/<path>`, `destinations/<type>/<name>`, and `associations/<path>`.
  Grant both to roles that manage the full lifecycle. See
  [Create and update capabilities](../reference/api.md#create-and-update-capabilities).
- Lifecycle actions are `update` requests: `sources/<path>/enable`,
  `sources/<path>/disable`, `associations/<path>/plan`, association `enable`,
  `disable`, and `sync`, `reconcile/<path>`, `queue/drain`, and
  `queue/<operation-id>/retry` and `cancel`.
- Inspection is `read`: `sources/<path>/check`, `reconcile/<path>/plan`,
  `destinations/<type>/<name>/check`, `status/<path>`, and `queue`.
- Source paths can be nested, so grant prefixes such as
  `sources/apps/team-a/*` instead of matching suffixes such as `*/enable`. Use
  fixed-depth `+` segments only when your source path layout is known and
  intentionally shallow.

## Platform operator

Platform operators manage destinations, associations, queue operations,
restore guard acknowledgement, reconcile, and status. This policy does not
grant source payload reads or source sync enablement.

> [!WARNING]
> This role combines write access to `destinations/*` and `associations/*`.
> That combination is full exfiltration authority for every source path the
> role can associate, because the role can create a destination and bind
> sources to it. Use it only for trusted platform operators, or split it as
> shown in [Split platform roles](#split-platform-roles) when the deployment
> requires two-person control. See
> [Authorization boundaries](threat-model.md#authorization-boundaries).

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/config" {
  capabilities = ["read", "update"]
}

path "secret-sync/config/restore-guard/acknowledge" {
  capabilities = ["update"]
}

path "secret-sync/destinations/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "secret-sync/associations/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "secret-sync/sources/*" {
  capabilities = ["read"]
}

path "secret-sync/queue" {
  capabilities = ["read"]
}

path "secret-sync/queue/*" {
  capabilities = ["read", "update"]
}

path "secret-sync/reconcile/*" {
  capabilities = ["read", "update"]
}

path "secret-sync/status/*" {
  capabilities = ["read"]
}

path "secret-sync/metadata/*" {
  capabilities = ["read", "list"]
}
```

`queue/*` with `update` includes `queue/drain`, which runs due remote
mutations for every source in the mount.

## Split platform roles

Destination administrators manage destination config without binding source
paths:

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/destinations/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
```

Destination write access still controls the plugin's network targets,
credential files, and ambient credentials. Grant it only to trusted platform
operators.

Association administrators bind approved destinations to source paths but
cannot create destinations:

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/associations/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "secret-sync/status/*" {
  capabilities = ["read"]
}

path "secret-sync/metadata/*" {
  capabilities = ["read", "list"]
}
```

## App writer

App writers manage source payloads, versions, and source metadata for their
own prefix. The `sources` grant lets them check readiness and enable or
disable source sync for their own paths.

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/data/apps/team-a/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "secret-sync/delete/apps/team-a/*" {
  capabilities = ["update"]
}

path "secret-sync/undelete/apps/team-a/*" {
  capabilities = ["update"]
}

path "secret-sync/metadata/apps/team-a/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "secret-sync/sources/apps/team-a/*" {
  capabilities = ["read", "update"]
}

path "secret-sync/status/apps/team-a/*" {
  capabilities = ["read"]
}
```

Add `update` on `secret-sync/destroy/apps/team-a/*` only when writers may
permanently destroy source versions. Destroy cannot be undone.

## App reader

App readers can read source payloads, source metadata, and sync status for
their own prefix. They cannot create associations or operate the queue.

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/data/apps/team-a/*" {
  capabilities = ["read"]
}

path "secret-sync/metadata/apps/team-a/*" {
  capabilities = ["read", "list"]
}

path "secret-sync/status/apps/team-a/*" {
  capabilities = ["read"]
}
```

## Delegated association owner

Delegated association owners create and manage associations for their own
source prefix. Before you grant this policy, enable hardened posture and
constrain every destination that the owner may use, as described in
[Delegate to application owners](../configure/delegate.md).

The policy grants `read` on `sources/apps/team-a/*`, so the owner can check
source readiness but cannot opt a source in to sync. The app writer or another
source owner enables source sync. Combine this policy with app reader or app
writer access only when the owner also needs the source payload.

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/metadata/apps/team-a/*" {
  capabilities = ["read", "list"]
}

path "secret-sync/sources/apps/team-a/*" {
  capabilities = ["read"]
}

path "secret-sync/associations/apps/team-a/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "secret-sync/status/apps/team-a/*" {
  capabilities = ["read"]
}

path "secret-sync/reconcile/apps/team-a/*" {
  capabilities = ["read"]
}
```

The `associations` grant includes `update` on `associations/<path>/plan`, which
plans an association without creating it. The `reconcile` grant allows only
`reconcile/<path>/plan`, which reads provider state without changing local
status.

Do not grant delegated owners write access to `destinations/*`, `queue/*`, or
association paths outside their source prefix.

## Auditor

Auditors can inspect redacted destination config, associations, queue state,
and status. This policy does not grant source payload reads or queue mutation.

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/config" {
  capabilities = ["read"]
}

path "secret-sync/destinations/*" {
  capabilities = ["read", "list"]
}

path "secret-sync/associations/*" {
  capabilities = ["read", "list"]
}

path "secret-sync/queue" {
  capabilities = ["read"]
}

path "secret-sync/queue/*" {
  capabilities = ["read"]
}

path "secret-sync/status/*" {
  capabilities = ["read"]
}

path "secret-sync/metadata/*" {
  capabilities = ["read", "list"]
}
```

These reads expose operational metadata such as source paths, remote names,
and, in status records, payload hashes. See
[Secret and credential protection](threat-model.md#secret-and-credential-protection).
