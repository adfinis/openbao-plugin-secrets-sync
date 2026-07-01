# Providers


Use these pages when you configure a destination provider. Each provider page
focuses on destination setup, auth choices, validation commands, and provider
constraints.

## Provider summary

| Provider | Destination type | Remote object | Supported association shape |
| --- | --- | --- | --- |
| AWS Secrets Manager | `aws-sm` | AWS Secrets Manager secret | `secret-path` with `json` |
| Kubernetes Secrets | `k8s` | Kubernetes `Opaque` Secret | `secret-path` with `json` |
| GitLab project variables | `gitlab` | Project CI/CD variable | `secret-key` with `raw` recommended; `secret-path` also supported |

## Provider guides

- [AWS Secrets Manager](aws-secrets-manager.md)
- [Kubernetes Secrets](kubernetes-secrets.md)
- [GitLab project variables](gitlab-project-variables.md)

## Implementation references

- [Provider contract](../development/provider-contract.md)
- [Provider implementation guide](../development/provider-implementation.md)
- [Testing and hardening](../development/testing.md)
