# Delegated use

Use this guide when platform operators let application owners create or manage
their own source paths and associations. The goal is to keep OpenBao the source
of truth while preventing one delegated owner from syncing unrelated sources or
remote names.

For OpenBao policy snippets, use [Policy examples](../security/policies.md).

## Enable strict source opt-in

Fresh mounts default to platform-operated mode:

```text
require_source_opt_in=false
delegated_mode=false
```

In that mode, a trusted platform operator is expected to own both
`destinations/*` and `associations/*`. Unconstrained destinations are allowed
for simple onboarding and operator-managed sync.

When application owners can manage their own `associations/<path>` prefixes,
enable delegated mode and strict source opt-in together:

```sh
bao write secret-sync/config require_source_opt_in=true delegated_mode=true
```

When strict opt-in is enabled, an enabled association can enqueue or dispatch
remote mutation only if the source metadata has `custom_metadata.syncable=true`.
`delegated_mode=true` requires `require_source_opt_in=true`.

Application owners can mark their own source path syncable when policy grants
the source enable endpoint:

```sh
bao write -force secret-sync/sources/apps/team-a/db/enable
```

The same state can be set through metadata when the caller is allowed to update
metadata:

```sh
bao write secret-sync/metadata/apps/team-a/db \
  @<(printf '%s' '{"custom_metadata":{"syncable":"true"}}')
```

Check source readiness before creating or enabling an association:

```sh
bao read secret-sync/sources/apps/team-a/db/check
```

## Constrain destination use

Destinations can restrict which source paths and remote object names may use
them. In delegated mode, both constraint lists are required before an
association can sync through the destination:

- `allowed_source_path_prefixes`
- `allowed_resolved_name_prefixes`

Add these fields when you configure a destination. This fragment omits
provider-specific required fields:

```text
bao write secret-sync/destinations/PROVIDER_TYPE/NAME \
  PROVIDER_SPECIFIC_FIELDS \
  allowed_source_path_prefixes=apps/team-a,shared/team-a \
  allowed_resolved_name_prefixes=openbao-plugin-secrets-sync/team-a/
```

Destination writes validate the merged provider config before storing it.
Non-empty fields from another provider type are rejected.

`allowed_source_path_prefixes` uses OpenBao source path segment boundaries:
`apps/team-a` allows `apps/team-a/db` but not `apps/team-alpha/db`.

`allowed_resolved_name_prefixes` uses exact or `/`-boundary matches:
`openbao-plugin-secrets-sync/team-a` allows
`openbao-plugin-secrets-sync/team-a/db` but not
`openbao-plugin-secrets-sync/team-alpha/db`.

Check destination readiness after enabling delegated mode:

```sh
bao read secret-sync/destinations/PROVIDER_TYPE/NAME/check
```

If either constraint list is empty, readiness reports
`destination_unconstrained` and association create, enable, manual sync,
reconcile, and queued dispatch refuse to use that destination.

## Split responsibilities

Separate these privileges unless a team intentionally owns the full workflow:

- source payload write access under `data/<path>`;
- source metadata and `sources/<path>/enable` access;
- association create/update/delete access for the delegated source prefix;
- destination management access;
- queue drain, queue retry, restore guard acknowledgement, and runtime config
  access.

Delegated association owners usually need association access for their source
prefix, source readiness reads, status reads, and reconcile plan reads. They do
not usually need destination write access or queue mutation.
