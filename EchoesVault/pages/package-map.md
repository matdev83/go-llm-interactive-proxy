---
type: reference
title: Package Map
description: Repository package zones and responsibilities for public contracts, core, plugins, infra, and tests.
stack: [go]
tags: [packages, structure, architecture]
status: active
---

# Package Map

## Public Contracts

| Package | Responsibility |
|---|---|
| `pkg/lipapi/` | Canonical request, event, capability, validation, error contracts. Protocol-neutral stable surface. |
| `pkg/lipsdk/` | Plugin registration, frontend/backend/hook interfaces, SDK facades for session, workspace, shaping, tools, traffic, usage, model inventory, continuity. |

## Internal Core Runtime (`internal/core/`)

| Subpackage | Responsibility |
|---|---|
| `runtime/` | Executor: orchestrates request lifecycle from frontend intake through backend dispatch to response encoding |
| `execbackend/` | Backend execution lifecycle management |
| `execctx/` | Execution context types |
| `leglifecycle/` | B-leg attempt lifecycle tracking |
| `lineage/` | A-leg/B-leg lineage identifiers and records |
| `routing/` | Selector parsing, model alias expansion, candidate resolution, weighted/failover/parallel planning |
| `affinity/` | Session affinity and stickiness |
| `policy/` | Orchestration policy rules |
| `b2bua/` | B2BUA continuity store, A-leg tracking, attempt lineage |
| `continuity/` | Continuity managers (memory, SQLite store) |
| `securesession/` | Session authority, BeginTurn, resume, denial, diagnostics |
| `accessmode/` | Access mode definitions and enforcement |
| `auth/` | Authentication logic |
| `admin/` | Admin HTTP surface |
| `http/` | HTTP helpers within core |
| `safety/` | Safety checks and guardrails |
| `capabilities/` | Capability negotiation and catalogs |
| `jsonpresence/` | JSON null-vs-empty round-trip preservation |
| `diag/` | Diagnostics identifiers and helpers |
| `config/` | Runtime config types and loading |
| `stream/` | Stream pumps, collectors, event plumbing |
| `streamrecovery/` | Stream recovery after interruptions |
| `hooks/` | Hook dispatch, ordering, panic isolation |
| `extensions/` | Extension pipeline stages, immutable snapshots |
| `auxreq/` | Auxiliary request support |
| `state/` | Feature-facing core state |
| `traffic/` | Traffic observation and capture |
| `workspace/` | Workspace resolution |
| `modelcatalog/` | Core model catalog |
| `modelregistry/` | Core model registry |
| `accounting/` | Usage accounting |
| `tokenaccounting/` | Token counting and accounting |
| `controlplane/` | Control plane operations |
| `interleavedstate/` | Interleaved state tracking |
| `interleavedthinking/` | Interleaved thinking tracking |

## Standard Distribution Assembly

| Package | Responsibility |
|---|---|
| `internal/pluginreg/` | Explicit registry: `NewRegistry`, `RegisterBackend`/`RegisterFrontend`/`RegisterFeature`, `BuildBackend`/`BuildFeatureBundle`, `ValidateBundledFactories`, `EffectiveAPIKeys` |
| `internal/standardplugins/` | Standard distribution: `InstallStandardBundleOn`, standard frontend/backend/feature tables, per-backend factory helpers, `ResolveUpstreamAPIKeysFromEnv`, `DefaultWireModel` |
| `internal/featurebundle/` | Feature merge surface: `MergeFeatureSurface` (SDK hook slices only; no `internal/core/hooks`) |
| `internal/infra/runtimebundle/` | Composes `Built` from config + registrations: executor, stores, HTTP client, health, model, accounting; owns `BuildFeatureHooks` / `hooks.New` |
| `internal/stdhttp/` | HTTP mounting, auth/principal, security guard, recovery, diagnostics, access logs, `Run`/`RunWithRuntime` |

## Plugin Packages

| Package | Responsibility |
|---|---|
| `internal/plugins/frontends/openairesponses/` | OpenAI Responses API frontend |
| `internal/plugins/frontends/openailegacy/` | Legacy OpenAI chat API frontend |
| `internal/plugins/frontends/anthropic/` | Anthropic Messages API frontend |
| `internal/plugins/frontends/gemini/` | Gemini generateContent API frontend |
| `internal/plugins/backends/openairesponses/` | OpenAI Responses backend adapter |
| `internal/plugins/backends/openailegacy/` | Legacy OpenAI backend adapter |
| `internal/plugins/backends/anthropic/` | Anthropic backend adapter |
| `internal/plugins/backends/gemini/` | Gemini backend adapter |
| `internal/plugins/backends/bedrock/` | AWS Bedrock backend adapter |
| `internal/plugins/backends/acp/` | ACP (Agent Client Protocol) backend — shared infrastructure for subprocess stdio connectors |
| `internal/plugins/backends/cursorcliacp/` | Cursor CLI ACP connector (subprocess stdio) |
| `internal/plugins/backends/geminicliacp/` | Gemini CLI ACP connector (subprocess stdio) |
| `internal/plugins/backends/agycliacp/` | AGY CLI ACP connector (subprocess stdio) |
| `internal/plugins/backends/openrouter/` | OpenRouter backend adapter |
| `internal/plugins/backends/nvidia/` | NVIDIA backend adapter |
| `internal/plugins/backends/huggingface/` | Hugging Face backend adapter |
| `internal/plugins/backends/openaicodex/` | OpenAI Codex backend adapter |
| `internal/plugins/backends/codexappserver/` | OpenAI Codex CLI app-server backend (local-agent stdio, Codex JSON-RPC protocol) |
| `internal/plugins/backends/opencodego/` | OpenCode Go backend adapter |
| `internal/plugins/backends/opencodezen/` | OpenCode Zen backend adapter |
| `internal/plugins/backends/ollama/` | Ollama backend adapter |
| `internal/plugins/backends/llamacpp/` | llama.cpp backend adapter |
| `internal/plugins/backends/lmstudio/` | LM Studio backend adapter |
| `internal/plugins/backends/vllm/` | vLLM backend adapter |
| `internal/plugins/backends/localstub/` | No-key local stub for dogfood/testing |
| `internal/plugins/backends/openaicompat/` | Custom OpenAI-compatible backend adapter |
| `internal/plugins/features/` | Feature plugins: submit no-op, parts no-op, tool reactor no-op, reference features |

## Infrastructure (`internal/infra/`)

| Package | Responsibility |
|---|---|
| `httpclient/` | Shared upstream HTTP client with proxy support |
| `logging/` | Structured logging helpers |
| `metrics/` | Prometheus metrics wiring |
| `tracing/` | OpenTelemetry tracing bootstrap |
| `db/` | Database helpers (Bun, SQLite) |
| `modelcatalog/` | Model catalog loaders |
| `modelregistry/` | Model registry loaders |
| `routinghealth/` | Routing health / circuit breaker |
| `tokenaccounting/` | Token accounting infrastructure |
| `tokenizers/` | Tokenizer implementations |
| `authevent/` | Auth event sinks |
| `osidentity/` | OS identity checks |
| `extensiontrace/` | Extension tracing helpers |
| `controlplane/` | Control plane infrastructure |
| `runtimebundle/` | Runtime assembly from config |

## Test Support

| Package | Responsibility |
|---|---|
| `internal/refbackend/` | HTTP emulator servers for integration tests |
| `internal/refclient/` | Official-SDK reference clients for conformance tests |
| `internal/testkit/` | Stubs, fixtures, fake streams/stores/clocks, builders, conformance helpers |
| `internal/archtest/` | Architecture guardrail tests (dependency direction, complexity budgets) |
| `internal/qa/` | Repository hygiene tests |
| `internal/safecast/` | Shared numeric conversion helpers |

## Commands

| Command | Purpose |
|---|---|
| `cmd/lipstd/` | Standard distribution binary |
| `cmd/codex-ws-poc/` | Codex WebSocket proof-of-concept |
| `cmd/model-inventory-proof/` | Model inventory proof-of-concept |
