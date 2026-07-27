---
type: architecture
title: Backend Connector Plugins
description: Hybrid essential builtins plus executable gRPC optional backend connectors (ADR 0008).
stack: [go]
tags: [plugins, backends, connectors, grpc, hybrid]
status: active
---

# Backend Connector Plugins

## Summary

Go-LIP uses **hybrid** backend composition ([ADR 0008](../../docs/adr/0008-hybrid-backend-connector-plugins.md)):

- **Essential builtins** — statically linked and registered via `internal/standardplugins` (`EssentialBackendBundle` / `EssentialBackendKinds`).
- **Optional connectors** — independent modules under `connectors/` (support under `connector-support/`) packaged as **executable** plugins speaking the versioned **gRPC** ABI in `pkg/lipsdk/backendplugin`.

Core owns orchestration, routing, failover, and B2BUA continuity. Optional connectors never enter essential/`standard_table` fixed tables.

## Hard rules

- Reject Go native `plugin` (shared objects).
- Closed manifests; unknown fields fail closed.
- Digest-bound exact-executable launch; approved secure local IPC; lazy activation; declared process models.
- Manifest-driven discovery registers `BackendSourceDiscovered` factories at the composition root.
- Root `UpstreamAPIKeys` env pools are essential OpenAI/Anthropic/Gemini only; migrated connectors use connector-local credentials.

## Operator workflow

Install, trusted roots, closed manifests, inspect/doctor, upgrade/rollback, and troubleshooting: [`docs/backend-plugins/operator.md`](../../docs/backend-plugins/operator.md). Validated examples live under `config/examples/plugin-operator-*.yaml` and `docs/backend-plugins/examples/operator/`.

## Threat model

Executable plugins are **trust-equivalent** to proxy-account native code; process isolation is **not** a malicious-code sandbox. Accepted controls (trusted paths, closed manifests, digest-bound launch, approved local IPC, peer/generation gates, env/FD minimization, bounds, event validation, redaction): [`docs/backend-plugins/threat-model.md`](../../docs/backend-plugins/threat-model.md). Verify with `make backend-plugin-security-checks`.

## Validation Status / Human Decision

Local validation on this branch (Windows host) includes Windows-native plugin lifecycle/process-tree evidence plus local gates such as `make backend-plugin-release-gates`, `make test-unit`, lint (0 issues), `make quality-checks`, and vuln scan. Linux native race/security/process evidence remains a reviewer/CI handoff (not re-observed on Windows).

**Human decision (2026-07-27):** no macOS hardware is available locally, so macOS-native tests are intentionally skipped for this PR. Do not claim they passed. CI `macos-latest` jobs remain configured and must be reviewed after push; checkbox/evidence stays honest until those runs are observed:

- `.github/workflows/acp-process-tree.yml` (task 6.3)
- `.github/workflows/backend-plugin-cross-platform.yml` (task 9.4)
- `.github/workflows/backend-plugin-release-gates.yml` (task 9.5)
- related security/race workflows for task 9.3 / Phase 8.2

This is a local validation waiver only; implementation semantics are not weakened.

## Related

- [plugin-system](plugin-system.md)
- [package-map](package-map.md)
- [architecture-overview](architecture-overview.md)
- [security-auth](security-auth.md)
- `docs/backend-plugins/authoring.md`
- `docs/backend-plugins/operator.md`
- `docs/backend-plugins/threat-model.md`
