# Security

This project is early-stage and does not yet publish stable release support
windows. Treat `main` as the active development line.

## Report a vulnerability

Do not open a public issue with exploit details, secret material, tokens, full
ciphertexts, or production endpoint data.

Use [GitHub private vulnerability reporting](https://github.com/adfinis/openbao-plugin-secrets-sync/security/advisories/new).
If private reporting is unavailable, contact the maintainers privately through
the [Adfinis contact channel](https://www.adfinis.com/en/contact/) and include a
minimal summary that is safe to route internally.

## What to include

- A short description of the issue and affected provider or API path.
- Steps to reproduce in a local or synthetic environment.
- The expected impact and any known prerequisites.
- Relevant logs or traces with all secrets and identifiers redacted.

## Scope

In scope:

- OpenBao plugin API authorization and storage behavior.
- Provider ownership checks, payload handling, and delete semantics.
- Redaction in API responses, logs, status, metrics, and diagnostics.
- Release artifacts, provenance, and OCI plugin distribution.

Out of scope:

- Findings that require access to real credentials without another plugin flaw.
- Vulnerabilities in external services such as AWS, Kubernetes, GitLab, or
  OpenBao itself unless the plugin makes the impact materially worse.
