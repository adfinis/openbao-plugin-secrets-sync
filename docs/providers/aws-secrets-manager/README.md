# AWS Secrets Manager

The AWS Secrets Manager provider writes one AWS Secrets Manager secret for each
association object. The provider stores the canonical Secret Sync payload as
the secret value and stores ownership metadata as AWS tags.

The provider can use default AWS endpoints, LocalStack-style local endpoints,
or explicitly approved HTTPS private endpoints.

## Provider documents

- [Configuration](configuration.md): auth modes, endpoint policy, value drift
  detection, sensitive fields, and validation commands.
- [Secret shapes](secret-shapes.md): supported association shape and naming
  examples.
- [Operations](operations.md): IAM permissions, ownership behavior, delete
  recovery, drift behavior, and e2e test paths.
