# Policy examples

These examples show common OpenBao policy shapes for a Secret Sync mount at
`secret-sync/`. Adapt path prefixes, destination names, and OpenBao policy
wildcards to the deployment.

The examples use `apps/team-a/*` as the delegated source prefix.

Source paths can be nested, so the examples use prefix grants such as
`sources/apps/team-a/*` rather than trying to match route suffixes like
`*/enable`. Use fixed-depth `+` segments only when your source path layout is
known and intentionally shallow.

## Platform operator

Platform operators manage destinations, associations, queue operations,
restore guard acknowledgement, reconcile, and status. This policy does not
grant source payload reads.

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

path "secret-sync/queue" {
  capabilities = ["read"]
}

path "secret-sync/queue/drain" {
  capabilities = ["update"]
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

## App writer

App writers manage source payloads and source metadata for their own prefix.
Grant `sources/<path>/enable` when app writers may mark their own source path
as syncable.

```hcl
path "secret-sync/info" {
  capabilities = ["read"]
}

path "secret-sync/data/apps/team-a/*" {
  capabilities = ["create", "read", "update", "delete"]
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
source prefix. Combine this policy with app reader or app writer access when
the delegated owner also needs source payload access.

Constrain the destination itself with `allowed_source_path_prefixes` and
`allowed_resolved_name_prefixes` so delegated owners cannot use a shared
destination for unrelated source paths or remote names.

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

Do not grant delegated owners write access to `destinations/*`, `queue/drain`,
or association paths outside their source prefix.

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

## Destination constraints

Use destination constraints with delegated association owners:

```sh
bao write secret-sync/destinations/PROVIDER_TYPE/NAME \
  PROVIDER_SPECIFIC_FIELDS \
  allowed_source_path_prefixes=apps/team-a \
  allowed_resolved_name_prefixes=openbao-plugin-secrets-sync/team-a/
```

Delegated association writes should use the compact destination selector:

```sh
bao write secret-sync/associations/apps/team-a/db \
  destination=PROVIDER_TYPE/NAME \
  name_template='openbao-plugin-secrets-sync/team-a/{{ path }}' \
  granularity=secret-path
```

The backend checks these constraints during association plan, association
activation, manual sync, enable, manual reconcile, background drift read-state,
and queued dispatch.
