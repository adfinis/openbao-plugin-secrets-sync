# API inspection artifacts


This directory contains API review artifacts for the OpenBao Secret Sync
plugin.

- [openapi.yaml](openapi.yaml) describes the current mounted HTTP API relative
  to `/v1/secret-sync`.

The OpenAPI spec is intentionally a design and inspection aid while the plugin
is pre-release. Use it to review path shape, field names, defaults, response
structure, and error classes before the API is treated as stable. Use
[API surface](../api-surface.md) for the user-facing API group summary.
