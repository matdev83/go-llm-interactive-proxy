---
type: architecture
title: Architecture Overview
description: Hexagonal architecture mapping, core ownership rules, and architectural decisions for Go LIP.
stack: [go]
tags: [architecture, hexagonal, boundaries]
status: active
---

# Architecture Overview

## Mental Model

Five primary zones:
1. Stable public contracts (`pkg/lipapi`, `pkg/lipsdk`)
2. Internal core runtime (`internal/core/`)
3. Official frontend plugins (`internal/plugins/frontends/`)
4. Official backend & feature plugins (`internal/plugins/backends/`, `internal/plugins/features/`)
5. Test and operational support (`internal/refbackend/`, `internal/refclient/`, `internal/testkit/`)

Around them: standard distribution assembly (`internal/standardplugins/`, `internal/featurebundle/`, `internal/infra/runtimebundle/`, `internal/stdhttp/`) plus the explicit registry type in `internal/pluginreg/`.

## Hexagonal Mapping

- **Domain/policy center:** `pkg/lipapi` + `internal/core/`
- **Application/use-case orchestration:** executor, routing, continuity, extension pipeline
- **Driving adapters:** HTTP frontends, CLI, admin/diagnostic surfaces
- **Driven adapters:** backend plugins, stores, model/catalog providers, tokenizers, metrics/tracing
- **Composition roots:** `cmd/lipstd/`, `internal/standardplugins/`, `internal/infra/runtimebundle/`, `internal/stdhttp/` (with `internal/pluginreg/` providing the registry type)

### Usage-authority bounded context

Usage authority is a core-owned policy/application capability with explicit
driven adapters. `internal/core/usageauthority/domain/` owns rule matching,
safe credential and policy-label dimensions, units, windows, reservations, and
authority status; `internal/core/usageauthority/app/` owns immutable snapshots,
admission, atomic reservation-set descriptors, settlement/release orchestration,
failure posture, and evidence projection. `internal/infra/usageauthority/configsource/`
supplies config snapshots, `authoritystore/` contains the shared clone-based
memory mutation core plus the Bun durable adapter and mutation log, and
`evidencesink/` bridges policydecision/control-plane recording. Runtime lifecycle
integration stays in `internal/core/runtime/authority_lifecycle.go`, which passes
one complete reservation set with legal stage and backend/output flags.

Memory publishes a mutation projection only after a complete set succeeds;
durable stores lock and re-read committed rows, flush the set in one transaction,
and reload or invalidate projections after rollback or flush failure.

## Core Ownership Rules

- Core imports `pkg/lipapi` and `pkg/lipsdk` only
- Core does not import concrete plugins or provider SDKs
- Core owns orchestration, routing, failover, B2BUA continuity
- Protocol-specific logic stays in adapters
- No pairwise protocol translators; always protocol <-> canonical

## Key Architecture Decisions

| Decision | Rationale |
|---|---|
| Explicit registration, no DI containers | Simpler builds, portable binaries, race detector works |
| Static linking (no Go `plugin` package) | Boundaries enforced through contracts, not dynamic loading |
| Streaming is primary execution path | Non-streaming collects the canonical stream |
| No retry after first content event | Post-output failures surface, not silently retry |
| Provider SDKs at adapter edges only | Core must not know provider wire types |
| Small interfaces where consumed | Avoids interface pollution; real substitution boundaries only |
| Explicit runtime config reload | SIGHUP / management API / `lipruntime.Reload` only; immutable generations; no watcher — see [runtime-config-reload](runtime-config-reload.md) |
