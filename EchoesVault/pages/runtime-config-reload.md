---
type: reference
title: Runtime Config Reload
description: Explicit SIGHUP and management-API runtime configuration reload — no watcher, atomic source replace, generation publication, address-env management opt-in.
stack: [go]
tags: [reload, config, operations, management-api, sighup]
status: active
---

# Runtime Config Reload

## Operator contract

Editing the config file alone has **no** effect. Reload runs only after an explicit trigger:

- `SIGHUP` on Unix-like platforms (does not terminate the process)
- Authenticated management HTTP (`POST /admin/config/reload`) when enabled
- `pkg/lipruntime.Runtime.Reload`

There is no file watcher, mtime poller, debounce loop, periodic rescan, or automatic retry. Every attempt re-reads the absolute `--config` path fixed at startup.

Authoritative operator guide: [`docs/runtime-config-reload.md`](../../docs/runtime-config-reload.md). ADR: [`docs/adr/0008-versioned-runtime-config-reload.md`](../../docs/adr/0008-versioned-runtime-config-reload.md). Spec: `.kiro/specs/versioned-runtime-reloadable-proxy-configuration/`.

## Source and limits

| Rule | Value / behavior |
|---|---|
| Size bound | 2 MiB (`DefaultConfigMaxBytes`) |
| YAML | Strict one-document known-fields decode |
| Changed content | Atomic path replace (new file identity); in-place digest change → `source_non_atomic_update` |
| Same identity + digest | Successful `no-op` |
| `check-config` | Same generation compiler, dry-run + full rollback, never publishes |

## Publication model

Startup publishes generation 1. A successful material reload compiles a complete immutable request-plane generation beside the active one and atomically swaps the active pointer. In-flight requests/streams keep their acquired generation. Process services (stores, metrics/tracing providers, listeners, capacity limiters) stay process-owned. Default retained-generation budget is 8; exhaustion yields `retention-blocked` without killing pinned streams.

## Restart-required vs reloadable

`internal/core/configreload.Classify` owns the matrix. Startup-only examples: access mode, listener/timeouts/decode admission, logging, diagnostics, observability, database/continuity store topology, management bind/auth. Reloadable examples: plugin rows, routes/aliases, many request-plane limits, identity, generation-owned HTTP client knobs. Mixed candidates reject as one `restart-required` transaction.

## Management opt-in

Disabled unless `LIP_RELOAD_MANAGEMENT_ADDRESS` is set (recommended `127.0.0.1:9090`). `LIP_RELOAD_MANAGEMENT_TOKEN` (≥16 Unicode code points) is required for multi-user or non-loopback; single-user loopback may use local trust. Cookie/data-plane keys never authorize reload. Paths: `/admin/config/reload`, `/admin/config/status` on the management listener only.

## Shutdown and close

Shutdown rejects new triggers, cancels candidate work, drains HTTP, awaits coordinator idle, drains generations, closes management, then process services. `Runtime.Close` is serialized and retryable after deadline/teardown failure (not `sync.Once`).

## Related concepts

- [security-auth](security-auth.md) — access modes and credential floors
- [architecture-overview](architecture-overview.md) — generation vs process ownership
- [package-map](package-map.md) — `configreload`, `runtimehost`, `configsource`, management adapter packages
