# GitLab project variables secret shapes

Fresh mounts default `require_source_opt_in=false`; if strict source opt-in is
enabled, mark the source with `sources/<path>/enable` before creating an
enabled association. Use the [user guide](../../guides/user-guide.md) for
generic association lifecycle commands and
[Templating](../../concepts/templating.md) for name placeholder behavior.

Use `secret-key` granularity with `format=raw` for normal CI/CD variable use.
Each top-level OpenBao source key becomes one GitLab CI/CD variable value.
GitLab variable keys may contain only letters, digits, and `_`, so choose a
compatible template:

```sh
bao write secret-sync/data/app/ci \
  USERNAME=app \
  PASSWORD=initial

bao write secret-sync/associations/app/ci/plan \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete

bao write secret-sync/associations/app/ci \
  destination=gitlab/prod \
  name_template='APP_{{ key }}' \
  granularity=secret-key \
  format=raw \
  delete_mode=delete
```

Source keys used with `secret-key` granularity must be non-empty, have no
surrounding whitespace, and must not contain `/`, `.`, or `..`. Rendered
GitLab variable keys may contain only letters, digits, and `_`, and must not
exceed 255 bytes.

The GitLab provider also supports:

- `granularity=secret-key` with `format=json`;
- `granularity=secret-path` with `format=json`.

For `secret-key` with `format=json`, use the same source shape as raw
variables. Each top-level source key becomes one GitLab variable whose value is
canonical JSON for that key:

```sh
bao write secret-sync/data/app/ci-json \
  USERNAME=app \
  PASSWORD=initial

bao write secret-sync/associations/app/ci-json \
  destination=gitlab/prod \
  name_template='APP_JSON_{{ key }}' \
  granularity=secret-key \
  format=json \
  delete_mode=delete
```

Use `secret-path` only when one GitLab variable needs to contain the full
canonical JSON payload:

```sh
bao write secret-sync/data/app/ci-config \
  username=app \
  password=initial

bao write secret-sync/associations/app/ci-config \
  destination=gitlab/prod \
  resolved_name=APP_CONFIG \
  granularity=secret-path \
  format=json \
  delete_mode=delete
```

Do not combine masked variables, `variable_raw=false`, and JSON payloads; use
`secret-key` with `format=raw` for masked GitLab variables.

The GitLab provider does not support destination-native data maps.

GitLab project variable values are limited to 10,000 bytes. The provider
enforces that payload limit before sending an upsert.
