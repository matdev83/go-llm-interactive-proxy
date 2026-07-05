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

Around them: standard distribution assembly (`internal/pluginreg/`, `internal/infra/runtimebundle/`, `internal/stdhttp/`).

## Hexagonal Mapping

- **Domain/policy center:** `pkg/lipapi` + `internal/core/`
- **Application/use-case orchestration:** executor, routing, continuity, extension pipeline
- **Driving adapters:** HTTP frontends, CLI, admin/diagnostic surfaces
- **Driven adapters:** backend plugins, stores, model/catalog providers, tokenizers, metrics/tracing
- **Composition roots:** `cmd/lipstd/`, `internal/pluginreg/`, `internal/infra/runtimebundle/`, `internal/stdhttp/`

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
