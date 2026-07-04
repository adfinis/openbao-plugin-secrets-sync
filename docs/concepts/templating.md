# Templating

Templates control how source paths and source keys become provider object
names. They are deliberately small and deterministic.

Use this page when choosing `resolved_name`, `name_template`, or
`data_key_template`. Use the provider secret-shape pages for destination
constraints such as Kubernetes Secret names or GitLab variable keys.

## Current Template Model

The current template engine performs literal placeholder replacement only. It
does not support functions, filters, conditionals, loops, escaping, or nested
expressions.

If a rendered template still contains `{{` or `}}`, the backend rejects it as
unsupported.

## Remote Object Names

Associations use two fields for remote object naming:

- `resolved_name`: a literal remote object name for `secret-path`
  associations.
- `name_template`: a template that renders a remote object name.

For `secret-path` granularity, the default `name_template` is `{{ path }}`.
If `resolved_name` is omitted, the backend renders `name_template` and stores
the result as the association's resolved remote name. Use `resolved_name` when
you already know the exact provider object name.

For `secret-key` granularity, `resolved_name` is invalid because one source
path can create one remote object per top-level source key. `name_template`
must include `{{ key }}` so every source key has a distinct remote object name.
The default is `{{ path }}/{{ key }}`.

Supported `name_template` placeholders:

| Placeholder | Meaning |
| --- | --- |
| `{{ path }}` | Normalized source path, without leading or trailing `/`. |
| `{{ key }}` | Source key for `secret-key` objects. For `secret-path`, this renders the synthetic object ID `secret-path`. |
| `{{ destination.type }}` | Destination provider type, such as `aws-sm`, `k8s`, or `gitlab`. |
| `{{ destination.name }}` | Destination name from `destination=<type>/<name>`. |

## Data Key Templates

`data_key_template` is separate from remote object naming. It applies only
when `data_mapping=source-keys` maps top-level source keys into
destination-native data keys, currently for Kubernetes Secrets.

`data_key_template` must include `{{ key }}` and currently supports only that
placeholder. The default is `{{ key }}`.

Rendered data keys must be non-empty, must not have surrounding whitespace,
must not be `.`, `..`, or start with `..`, must be at most 253 characters, and
must contain only alphanumeric characters, `-`, `_`, or `.`.

## Provider Constraints

Templating does not normalize names for a provider. Choose templates that
already render valid names for the destination:

- AWS Secrets Manager can use the default `{{ path }}` shape when `/` is
  desired in secret names.
- Kubernetes Secret names must be DNS-safe. Use `resolved_name=app-db` or a
  template that already renders a valid Kubernetes name.
- GitLab variable keys may contain only letters, digits, and `_`. Use source
  keys and templates that render valid variable keys, such as
  `name_template='APP_{{ key }}'`.

Run `associations/<path>/plan` before creating an association when the remote
name is derived from a template or constrained by destination policy.

## Reservation Behavior

The backend reserves the destination and rendered remote-name identity managed
by an association. This prevents two associations from managing the same remote
object in the same destination.

For `secret-path` associations, the reservation is the resolved remote name.
For `secret-key` associations, the backend reserves both the rendered name
pattern and the concrete rendered names for the current source keys. The
pattern substitutes source path and destination placeholders, keeps a stable
key placeholder, and applies the same slash trimming as normal object-name
rendering.

Source writes refresh concrete `secret-key` reservations before committing a
new source version. If a new or changed source key would make one association
render the same remote object name as another association for the same
destination, the write is rejected before the new version is accepted.

Changing `granularity`, `resolved_name`, or the remote-name reservation
requires creating a new association and deleting the old one. Updating an
already enabled association does not automatically enqueue sync work; use
manual sync after reviewing the plan when you want to push the current source
version.
