---
type: architecture
title: Plugin System
description: Explicit frontend/feature registration plus hybrid essential and executable backend connectors.
stack: [go]
tags: [plugins, frontends, backends, features, connectors]
status: active
---

# Plugin System

## Registration Model

Explicit construction at the composition root. No DI containers, no reflection registries, no Go native `plugin` package.

- `internal/pluginreg/` — registry type (`NewRegistry`, register/build APIs, discovered backend provenance).
- `internal/standardplugins/` — essential/static distribution tables and `InstallStandardBundleOn` (frontends, features, **essential** backends).
- Optional backends — closed-manifest discovery into discovered factories ([ADR 0008](../../docs/adr/0008-hybrid-backend-connector-plugins.md); [backend-connector-plugins](backend-connector-plugins.md)).
- Feature merge — `internal/featurebundle/`; hook bus — `internal/infra/runtimebundle/`.

Backend registrations declare credential posture and access scope. `BackendAccessLocalOnly` connectors are rejected when `access.mode: multi_user` is active.

```
NewRegistry() -> InstallStandardBundleOn(reg, keys) -> discover connectors -> runtimebundle.BuildHost -> Host (GenerationRuntime gen 1)
```

## Frontend Plugins (`internal/plugins/frontends/`)

| Frontend | Protocol |
|---|---|
| `openairesponses/` | OpenAI Responses API |
| `openailegacy/` | Legacy OpenAI chat completions |
| `anthropic/` | Anthropic Messages API |
| `gemini/` | Gemini generateContent API |

Shared helpers: `decodeqos/`, `execerr/`, `exechold/`, `frontendconfig/`, `holdalive/`, `jsonguard/`, `limits/`, `openaiwire/`, `parity/`, `reqbody/`, `routeselect/`, `sessionwire/`.

## Backend Plugins (hybrid)

### Essential builtins (`internal/plugins/backends/` + essential tables)

`openairesponses`, `openailegacy`, `anthropic`, `gemini`, `bedrock`, plus built-in custom OpenAI/Anthropic-compatible kinds. Shared helpers may include `openaicompat/`, `openaifamily/`, and related packages still imported by the root module.

Experimental in-tree `cursorsdk` (Node bridge over exact `@cursor/sdk` 1.0.23) may remain as an opt-in adapter outside `EssentialBackendBundle`; production optional delivery is the external executable connector path. Operator docs: [docs/cursor-sdk-backend.md](../../docs/cursor-sdk-backend.md). See [Cursor SDK Backend](cursor-sdk-backend.md).

### Optional executable connectors (`connectors/`)

Installed artifacts (examples): `openrouter`, `nvidia`, `huggingface`, `ollama` / `ollama-cloud`, `llamacpp`, `lmstudio`, `vllm`, `localstub`, `opencode` (Go/Zen exports), `codex` (HTTP + app-server exports), ACP-family CLIs including `cursorcliacp`. Support modules live under `connector-support/`. Do not add these to essential fixed tables.

## Feature Plugins (`internal/plugins/features/`)

Consume `pkg/lipsdk` facades. No `internal/core` imports.

- **No-op compatibility hooks:** `submitnoop/`, `partsnoop/`, `toolreactornoop/`
- **Reference/proof features:** `refsubmit/`, `refparts/`, `reftool/`, `reftoolpolicy/`, `refautoappend/`, `refworkspaceguard/`, `reftraffictranscript/`, `refverifier/`, `prerequestpolicy/`, `codexclientcompat/`
- **Product features (examples):** `secretsguard/`, `toolcallrepair/`, `reasoningpreservation/` (see [reasoning-output-preservation](reasoning-output-preservation.md))

## Extension Pipeline

Core owns the fixed legal stage list and immutable per-request runtime snapshots. Hooks and extension stages are seams for plugin behavior.

Documented extension categories include submit hooks, request/response part hooks, route hints, tool reactors, completion gates, auxiliary clients, traffic observers, diagnostics observers, and model inventory/capability providers.
