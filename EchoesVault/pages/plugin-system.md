---
type: architecture
title: Plugin System
description: Explicit frontend, backend, and feature plugin registration and extension surface.
stack: [go]
tags: [plugins, frontends, backends, features]
status: active
---

# Plugin System

## Registration Model

Explicit, static, per-composition-root. No DI containers, no reflection registries, no Go `plugin` package. `internal/pluginreg/` owns standard distribution registration.

```
NewRegistry() -> InstallStandardBundleOn(reg, keys) -> reg.Build() -> runtimebundle.Built
```

## Frontend Plugins (`internal/plugins/frontends/`)

Decode incoming HTTP/SSE into canonical `lipapi` requests. Encode canonical events into protocol-specific responses.

| Frontend | Protocol |
|---|---|
| `openairesponses/` | OpenAI Responses API |
| `openailegacy/` | Legacy OpenAI chat completions |
| `anthropic/` | Anthropic Messages API |
| `gemini/` | Gemini generateContent API |

Shared helpers: `decodeqos/`, `execerr/`, `exechold/`, `frontendconfig/`, `holdalive/`, `jsonguard/`, `limits/`, `openaiwire/`, `parity/`, `reqbody/`, `routeselect/`, `sessionwire/`.

## Backend Plugins (`internal/plugins/backends/`)

Translate canonical requests into upstream calls. Map upstream responses into canonical events. Provider SDKs stay here.

| Category | Backends |
|---|---|
| Hosted/Provider | `openairesponses`, `openailegacy`, `anthropic`, `gemini`, `bedrock`, `acp`, `openrouter`, `nvidia`, `huggingface`, `openaicodex`, `opencodego`, `opencodezen` |
| Local/Compatible | `ollama`, `ollama-cloud`, `llamacpp`, `lmstudio`, `vllm`, `localstub` |
| Custom | `openaicompat` - operator-configured OpenAI/Anthropic-compatible rows |

Shared helpers: `checkcfg/`, `credpool/`, `modeldiscover/`, `openaicaps/`, `openaicred/`, `openaifamily/`, `openaiusage/`, `opencodecommon/`, `protocols/`, `streampeek/`.

## Feature Plugins (`internal/plugins/features/`)

Consume `pkg/lipsdk` facades. No `internal/core` imports.

- **No-op compatibility hooks:** `submitnoop/`, `partsnoop/`, `toolreactornoop/`
- **Reference/proof features:** `refsubmit/`, `refparts/`, `reftool/`, `reftoolpolicy/`, `refautoappend/`, `refworkspaceguard/`, `reftraffictranscript/`, `refverifier/`, `prerequestpolicy/`, `codexclientcompat/`

## Extension Pipeline

Core owns the fixed legal stage list and immutable per-request runtime snapshots. Hooks and extension stages are seams for plugin behavior.

Documented extension categories include submit hooks, request/response part hooks, route hints, tool reactors, completion gates, auxiliary clients, traffic observers, diagnostics observers, and model inventory/capability providers.
