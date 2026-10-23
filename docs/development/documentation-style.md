---
verifiedBy:
  - mk/docs.mk
  - hack/tools/docs_check/main.go
---

# Documentation standard

Secret Sync documentation describes supported behavior for people who install,
configure, secure, operate, and develop the plugin. Write for the reader's task
and verify product claims against the code.

This standard follows Google's guidance for
[clear technical writing](https://developers.google.com/tech-writing/one),
[procedures](https://developers.google.com/style/procedures),
[headings](https://developers.google.com/style/headings),
[voice](https://developers.google.com/style/voice),
[person](https://developers.google.com/style/person), and
[prescriptive language](https://developers.google.com/style/prescriptive-documentation).

## Organize documentation by task

The docs are plain Markdown rendered by GitHub. [docs/README.md](../README.md)
is the only index page; do not add section index pages.

| Section | Owns |
| --- | --- |
| `get-started/` | Installing the plugin and the first successful sync. |
| `configure/` | Destinations (one page per provider), associations, delegation, and mount runtime settings. |
| `operate/` | Troubleshooting, restore and clone review, and monitoring. |
| `security/` | Threat model, trust boundaries, and policy examples. |
| `reference/` | Exact API, status, diagnostic, and compatibility contracts. |
| `concepts/` | The sync and ownership model that the task pages rely on. |
| `development/` | Implementation contracts, provider development, testing, and release engineering. |

Add a page only when it gives a task or contract a clear home. Prefer extending
an existing page when a new page would repeat prerequisites, warnings, or
explanations already owned elsewhere.

## Give every topic one owner

Each topic has exactly one owning page. Other pages link to the owner in one
sentence instead of restating it. When behavior changes, update the owner first.

| Topic | Owner |
| --- | --- |
| Source path rules, write forms, CAS, metadata policy, KV-v2 differences, path table, pagination | [API reference](../reference/api.md) |
| Sync states, queue states, verification values, error classes, retry policy, `hint` and `next_actions` | [Status and diagnostics](../reference/status-and-diagnostics.md) |
| Supported OpenBao versions and preview stability | [Compatibility](../reference/compatibility.md) |
| Own-mount source boundary, asynchronous convergence, dispatch paths, ordering | [Sync model](../concepts/sync-model.md) |
| Ownership metadata, restore epoch, reconcile, drift modes, repair eligibility, restore guard and pause semantics, collisions, remote deletes | [Ownership and drift](../concepts/ownership-and-drift.md) |
| Secret shapes, provider shape support, name templates, reservations, selectors, association updates, delete modes | [Associations](../configure/associations.md) |
| Security posture, source sync enablement, destination constraints | [Delegate to application owners](../configure/delegate.md) |
| Mount configuration fields and defaults | [Runtime settings](../configure/runtime.md) |
| Provider auth, endpoints, permissions, metadata keys, provider-specific drift and delete behavior | The provider page under `configure/` |
| Endpoint address policy for GitLab and Kubernetes token auth | [Threat model](../security/threat-model.md) |
| Release artifact verification and installation | [Install the plugin](../get-started/install.md) |
| Recovery procedures and evidence capture | [Troubleshoot sync](../operate/troubleshoot.md) |
| Restore and clone procedure | [Review a restore or clone](../operate/restore-and-clone.md) |
| Metric instruments and attribute policy | [Observability internals](observability.md) |
| Provider interface and conformance | [Provider contract](provider-contract.md) |
| Storage keys and schema policy | [Storage model](backend/storage-model.md) |

## Write directly

- State the outcome before background or rationale.
- Use active voice, second person, present tense, and direct verbs.
- Use sentence case for headings.
- Start task headings with a base-form verb. Use noun phrases for concept and
  reference headings.
- Keep one idea in each sentence and one topic in each paragraph.
- Put prerequisites before the procedure.
- Present one recommended path first. Put genuine alternatives in a separate
  section.
- Use `must` for requirements, `can` for permission or capability, and `might`
  for possibility.
- Do not open a page with a list of related links or with commentary about the
  page, such as "This page explains". Link to related pages where the reader
  needs them.
- Remove slogans, repeated navigation, roadmap speculation, and implementation
  history that does not affect the current contract.
- Do not use em dashes. `make docs-check` rejects them.

## Write complete procedures

Use numbered steps for ordered work. Start each step with an imperative and keep
one primary action in each step. Introduce commands with the action and expected
effect. Explain placeholders before readers copy the command, and state the
observable result afterward.

Examples must be internally complete for the task they claim to perform. Label
partial policy, request, or configuration fragments as fragments. Do not present
an accepted write, a queued operation ID, or one status field as proof that a
destination has converged, unless the page explains what that evidence proves.

Assume the mount path `secret-sync/`. Use explicit placeholders such as
`<type>`, `<name>`, `<path>`, and `<operation-id>`. Keep shell examples safe to
paste after substitution. Do not show commands that expose credentials or imply
that a destructive operation is reversible.

## Preserve operational boundaries

Keep requirements and warnings that protect security, data, availability, or
access. A shorter page must not hide:

- authority and ownership boundaries;
- trust roots, credentials, or secret custody;
- destructive or irreversible effects;
- compatibility and version constraints;
- required network paths and external dependencies;
- restore prerequisites and the absence of automatic restore detection; or
- the difference between accepted local state, queued work, and converged
  remote state.

Use a GitHub `> [!WARNING]` alert only when ignoring it can cause material
harm. Use `> [!NOTE]` for scope, ownership, limitations, and other context. Do
not use alerts as decoration.

## Verify product claims

Every page except [docs/README.md](../README.md) starts with YAML frontmatter
that lists the narrowest repository paths proving its claims:

```yaml
---
verifiedBy:
  - internal/backend/path_config.go
  - internal/providers/gitlab
---
```

Choose evidence in this order:

1. Request fields, defaults, and validation: path definitions and request
   parsing in `internal/backend`.
2. Runtime behavior: backend services, providers, unit tests, and e2e tests.
3. Commands and examples: e2e tests, Make targets, and workflow configuration.
4. Release and compatibility claims: `.ci/versions.yaml`, release workflows,
   and release tooling.

Do not copy a claim merely because an older page contained it. When code and
documentation disagree, document the supported runtime behavior or resolve the
product contract before merging. Track unresolved product work in an issue.

## Use the smallest useful presentation

Prefer Markdown paragraphs, short lists, short tables, and ordinary links. Use a
table only for a genuine comparison across consistent columns. Use relative
links for repository content and link to the most authoritative external
source. Keep link text descriptive.

## Definition of done

A documentation change is complete when:

- each page has one clear task or contract, and each topic has one owner;
- behavioral claims cite current `verifiedBy` evidence;
- examples are complete, safe, and aligned with the API and runtime;
- security, data, availability, access, and compatibility boundaries remain
  explicit;
- [docs/README.md](../README.md) lists every page; and
- `make docs-check` passes.
