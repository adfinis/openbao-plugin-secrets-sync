# GitLab project variables

The GitLab provider writes project-level CI/CD variables. Each association
object maps to one GitLab variable key. The provider stores ownership metadata
and payload hash metadata in a human-readable variable description.

## Provider documents

- [Configuration](configuration.md): GitLab endpoint, destination attributes,
  masked and hidden variable constraints, sensitive fields, and validation
  commands.
- [Secret shapes](secret-shapes.md): per-key raw variables, JSON variants, and
  GitLab variable key constraints.
- [Operations](operations.md): API permissions, ownership behavior, attribute
  updates, stale ownership recovery, and e2e test paths.
