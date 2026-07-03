# AWS Secrets Manager secret shapes

Fresh mounts default `require_source_opt_in=false`; if strict source opt-in is
enabled, mark the source with `sources/<path>/enable` before creating an
enabled association. Use the [user guide](../../guides/user-guide.md) for
generic association lifecycle commands and
[Templating](../../concepts/templating.md) for name placeholder behavior.

The AWS provider supports `secret-path` granularity with `format=json`. The
default association shape works for AWS Secrets Manager:

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"username":"app","password":"initial"}}')

bao write secret-sync/associations/app/db/plan \
  destination=aws-sm/prod

bao write secret-sync/associations/app/db \
  destination=aws-sm/prod
```

Use `resolved_name` or `name_template` when the remote secret name must differ
from the OpenBao source path:

```sh
bao write secret-sync/data/app/db \
  @<(printf '%s' '{"data":{"username":"app","password":"initial"}}')

bao write secret-sync/associations/app/db \
  destination=aws-sm/prod \
  resolved_name=openbao-plugin-secrets-sync/team-a/app-db
```

The AWS provider does not support `secret-key` granularity, `format=raw`, or
destination-native data maps.

AWS Secrets Manager limits the encrypted secret value to 65,536 bytes. The
provider enforces that payload limit before sending an upsert.
