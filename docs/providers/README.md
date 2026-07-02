# Providers


Use these pages when you configure a destination provider. Each provider page
focuses on destination setup, auth choices, validation commands, and provider
constraints.

Provider association examples assume the source path already has a current
local version. Fresh mounts default `require_source_opt_in=false`, so no
provider-specific `syncable` step is required. If strict source opt-in is
enabled, mark the source with `sources/<path>/enable` before creating or
enabling associations.

## Provider summary

| Provider | Destination type | Remote object | Supported association shape |
| --- | --- | --- | --- |
| AWS Secrets Manager | `aws-sm` | AWS Secrets Manager secret | `secret-path` with `json` |
| Kubernetes Secrets | `k8s` | Kubernetes `Opaque` Secret | `secret-path` with `json`; optional `source-keys` data mapping |
| GitLab project variables | `gitlab` | Project CI/CD variable | `secret-key` with `raw` recommended; `secret-path` also supported |

## Capability matrix

| Provider | Auth modes | `secret-path` | `secret-key` | `raw` | `json` | Data map | Read-state | Value readback | Owned delete | Metadata ownership | Local e2e | Real-provider e2e |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| AWS Secrets Manager | AWS SDK default chain; STS assume role | Yes | No | No | Yes | No | Yes | Opt-in | Yes | AWS tags | LocalStack | Manual AWS |
| Kubernetes Secrets | In-cluster; kubeconfig; bearer token | Yes | No | No | Yes | Yes | Yes | Yes | Yes | Labels and annotations | kind | No |
| GitLab project variables | GitLab API token | Yes | Yes | Yes | Yes | No | Yes | Yes | Yes | Variable description | Dockerized GitLab CE | No |

## Provider guides

- [AWS Secrets Manager](aws-secrets-manager.md)
- [Kubernetes Secrets](kubernetes-secrets.md)
- [GitLab project variables](gitlab-project-variables.md)

## Implementation references

- [Provider contract](../development/provider-contract.md)
- [Provider implementation guide](../development/provider-implementation.md)
- [Testing and hardening](../development/testing.md)
