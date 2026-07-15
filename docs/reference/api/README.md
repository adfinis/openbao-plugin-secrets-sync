# Generated API inspection

OpenBao generates an OpenAPI 3.0.2 document for mounted framework backends from
their path, request field, and response declarations. Secret Sync keeps those
declarations with the backend implementation and verifies the generated
document in tests; a generated copy is not committed to this repository.

Inspect one mounted path with:

```sh
bao path-help -format=json secret-sync/
```

Inspect the combined OpenAPI document for a running OpenBao server with:

```sh
bao read -format=json sys/internal/specs/openapi
```

Use [API surface](../api-surface.md) for the user-facing API group summary and
the API golden responses for broad response-shape drift detection.
