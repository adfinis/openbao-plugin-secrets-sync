# Backend

Use this section when you change the logical backend, storage records, queue
behavior, reconcile behavior, response diagnostics, or safety gates. These
documents are implementation-facing and intentionally more detailed than the
operator and concept docs.

## Backend Documents

- [Storage model](storage-model.md)
- [Request lifecycle](request-lifecycle.md)
- [Queue and dispatch](queue-and-dispatch.md)
- [Reconcile and drift](reconcile-and-drift.md)
- [Safety and diagnostics](safety-and-diagnostics.md)

## Code Map

The backend implementation lives mostly in `internal/backend`:

- `backend.go` wires the OpenBao framework backend, path list, seal-wrapped
  storage prefixes, invalidation hook, cleanup hook, and periodic callback.
- `storage_records.go` defines storage key prefixes, durable record shapes,
  defaults, source path normalization, and storage-key helpers.
- `path_config.go` owns mount-wide config, restore guard acknowledgement, drift
  settings, event dispatch settings, and config response shape.
- `path_data.go`, `path_metadata.go`, `path_versions.go`, and
  `path_sources.go` own source data, source metadata, lifecycle mutations, and
  source eligibility helpers.
- `path_destinations.go` owns destination configuration, destination checks,
  provider defaults, provider capabilities, and destination policy fields.
- `path_associations.go` owns association planning, create/update, enable,
  disable, manual sync, capability checks, destination policy checks, and
  remote-name reservation.
- `path_queue.go`, `dispatcher.go`, `event_dispatch.go`, and `recovery.go` own
  queue inspection, queue mutation, durable dispatch, low-latency wakeups, and
  enqueue-intent recovery.
- `path_reconcile.go` and `periodic_reconcile.go` own manual reconcile and
  periodic drift detection or repair.
- `path_status.go`, `diagnostics.go`, `response.go`, and `metrics.go` own
  status responses, response hygiene, hints, `next_actions`, and observability.
- `provider_runtime_cache.go` owns destination runtime construction, caching,
  invalidation, and cleanup.
- `write_locks.go` owns per-source and per-remote-name write locks.

Provider-specific code lives in `internal/providers`. Shared payload building
lives in `internal/payload`, and shared sync states live in `internal/domain`.

## Maintainer Rule

When backend behavior changes, update the document at the same ownership level:

- storage key or record changes: update [Storage model](storage-model.md);
- request path or response behavior changes: update
  [Request lifecycle](request-lifecycle.md) and, when user visible,
  [API surface](../../reference/api-surface.md);
- queue, dispatch, retry, or enqueue recovery changes: update
  [Queue and dispatch](queue-and-dispatch.md);
- reconcile, drift detection, or drift repair changes: update
  [Reconcile and drift](reconcile-and-drift.md) and
  [Runtime configuration](../../operations/runtime-configuration.md);
- guard, ownership, redaction, hint, or `next_actions` changes: update
  [Safety and diagnostics](safety-and-diagnostics.md) and the
  [Operator runbook](../../operations/operator-runbook.md) when recovery steps
  change.
