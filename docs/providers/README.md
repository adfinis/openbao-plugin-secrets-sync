# Providers

Use these pages when you configure a destination provider. Each provider page
focuses on destination setup, auth choices, validation commands, ownership
behavior, and provider-specific constraints. Use the [user guide](../guides/user-guide.md)
for the generic association lifecycle and the [sync model](../concepts/sync-model.md)
for the shared association model.

Provider secret-shape pages include source writes and the association shape
required by the provider. Fresh mounts default `require_source_opt_in=false`,
so no provider-specific `syncable` step is required. If strict source opt-in
is enabled, mark the source with `sources/<path>/enable` before creating or
enabling associations.

Use [Templating](../concepts/templating.md) for `resolved_name`,
`name_template`, and `data_key_template` behavior. Use
[Secret shapes](../guides/secret-shapes.md) for generic AWS, Kubernetes, and
GitLab association examples. Use [Ownership and safety](../concepts/ownership-and-safety.md)
when provider ownership blocks sync. Use [Convergence](../concepts/convergence.md)
and [Reconcile and drift](../concepts/reconcile-and-drift.md) when provider
state does not match OpenBao desired state. Use the
[operator runbook](../operations/operator-runbook.md) for recovery commands.

## Provider summary

| Provider | Destination type | Remote object | Supported association shape |
| --- | --- | --- | --- |
| AWS Secrets Manager | `aws-sm` | AWS Secrets Manager secret | `secret-path` with `json` |
| Kubernetes Secrets | `k8s` | Kubernetes `Opaque` Secret | `secret-path` with `json`; optional `source-keys` data mapping |
| GitLab project variables | `gitlab` | Project CI/CD variable | `secret-key` with `raw` recommended; `secret-path` also supported |

## Secret shape matrix

| Secret shape | AWS Secrets Manager | Kubernetes Secrets | GitLab project variables |
| --- | --- | --- | --- |
| `secret-path`, `format=json`, `data_mapping=payload` | Yes | Yes | Yes |
| `secret-path`, `format=json`, `data_mapping=source-keys` | No | Yes | No |
| `secret-key`, `format=raw` | No | No | Yes |
| `secret-key`, `format=json` | No | No | Yes |

## Capability matrix

| Provider | Auth modes | `secret-path` | `secret-key` | `raw` | `json` | Data map | Read-state | Value readback | Owned delete | Metadata ownership | Local e2e | Real-provider e2e |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| AWS Secrets Manager | AWS SDK default chain; STS assume role | Yes | No | No | Yes | No | Yes | Opt-in | Yes | AWS tags | LocalStack | Manual AWS |
| Kubernetes Secrets | In-cluster; kubeconfig; bearer token | Yes | No | No | Yes | Yes | Yes | Yes | Yes | Labels and annotations | kind | No |
| GitLab project variables | GitLab API token | Yes | Yes | Yes | Yes | No | Yes | Yes | Yes | Human-readable variable description | Dockerized GitLab CE | No |

## Provider documents

| Provider | Overview | Configuration | Secret shapes | Operations |
| --- | --- | --- | --- | --- |
| AWS Secrets Manager | [Overview](aws-secrets-manager/README.md) | [Configuration](aws-secrets-manager/configuration.md) | [Secret shapes](aws-secrets-manager/secret-shapes.md) | [Operations](aws-secrets-manager/operations.md) |
| Kubernetes Secrets | [Overview](kubernetes-secrets/README.md) | [Configuration](kubernetes-secrets/configuration.md) | [Secret shapes](kubernetes-secrets/secret-shapes.md) | [Operations](kubernetes-secrets/operations.md) |
| GitLab project variables | [Overview](gitlab-project-variables/README.md) | [Configuration](gitlab-project-variables/configuration.md) | [Secret shapes](gitlab-project-variables/secret-shapes.md) | [Operations](gitlab-project-variables/operations.md) |

## Implementation references

- [Provider contract](../development/provider-contract.md)
- [Provider implementation guide](../development/provider-implementation.md)
- [Testing and hardening](../development/testing.md)
