# Kubernetes Secrets

The Kubernetes provider writes one Kubernetes `Opaque` Secret for each
`secret-path` association object. By default, the canonical payload is stored
in the Secret `data.payload` key. With `data_mapping=source-keys`, top-level
source keys are stored as Kubernetes Secret `.data` entries.

The provider stores ownership metadata in labels and annotations.

## Provider documents

- [Configuration](configuration.md): auth modes, TLS options, sensitive fields,
  and validation commands.
- [Secret shapes](secret-shapes.md): supported association shapes, Kubernetes
  names, and source-key data maps.
- [Operations](operations.md): RBAC permissions, ownership behavior, data-key
  replacement semantics, and e2e test paths.
