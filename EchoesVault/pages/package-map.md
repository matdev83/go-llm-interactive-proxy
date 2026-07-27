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
| `pkg/lipsdk/` | Plugin registration, frontend/backend/hook interfaces, SDK facades for session, workspace, shaping, tools, traffic, usage, model inventory, continuity; **`configreload/`** is the one reload contract. |

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
| `jsonshape/` | Protocol-neutral JSON size/shape preflight (`encoding/json.Decoder.Token`): request 8 MiB/depth 128 duplicate-compatible; schema 256 KiB/depth 32 strict; args 64 KiB/depth 64 strict |
| `toolcallrepair/` | Canonical tool-call repair engine, schema cache, ordered JSON materialize (ADR 0007); recursive builder retained after bounded preflight |
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
| `usageauthority/domain/` | Rule policy, safe credential/label dimensions, explicit units, windows, reservations, and authority status |
| `usageauthority/app/` | Rule snapshots, admission, atomic reservation sets, settlement/release, failure posture, and evidence projection |
| `controlplane/` | Control plane operations |
| `interleavedstate/` | Interleaved state tracking |
| `interleavedthinking/` | Interleaved thinking tracking |

## Standard Distribution Assembly

| Package | Responsibility |
|---|---|
| `internal/pluginreg/` | Explicit registry: `NewRegistry`, `RegisterBackend`/`RegisterFrontend`/`RegisterFeature`, `BuildBackend`/`BuildFeatureBundle`, `ValidateBundledFactories`, `EffectiveAPIKeys` |
| `internal/standardplugins/` | Essential/static distribution: `InstallStandardBundleOn`, frontend/feature/essential-backend tables (`EssentialBackendBundle`), `ResolveUpstreamAPIKeysFromEnv` (OpenAI/Anthropic/Gemini only), `DefaultWireModel` |
| `internal/featurebundle/` | Feature merge surface: `MergeFeatureSurface` (SDK hook slices only; no `internal/core/hooks`) |
| `internal/infra/runtimebundle/` | `BuildHost` returns one process-owned `Host`: process runtime + immutable `GenerationRuntime` generations, feature hooks (`BuildFeatureHooks` / `hooks.New`), reload coordinator binding; **`Host.Close`** is the sole process shutdown coordinator |
| `internal/stdhttp/` | HTTP mounting, auth/principal, security guard, recovery, diagnostics, access logs, generation-dispatcher serve (Host-owned lifecycle) |

## Plugin Packages

Hybrid backends: [backend-connector-plugins](backend-connector-plugins.md), [ADR 0008](../../docs/adr/0008-hybrid-backend-connector-plugins.md).

| Package | Responsibility |
|---|---|
| `internal/plugins/frontends/openairesponses/` | OpenAI Responses API frontend |
| `internal/plugins/frontends/openailegacy/` | Legacy OpenAI chat API frontend |
| `internal/plugins/frontends/anthropic/` | Anthropic Messages API frontend |
| `internal/plugins/frontends/gemini/` | Gemini generateContent API frontend |
| `internal/plugins/frontends/decodeqos/` | Shared weighted decode admission limiter (finite defaults; Decode-only weight after ReadAll) |
| `internal/plugins/frontends/reqbody/` | Bounded decompressed body ReadAll (413 on oversize) |
| `internal/plugins/backends/openairesponses/` | Essential OpenAI Responses backend |
| `internal/plugins/backends/openailegacy/` | Essential legacy OpenAI backend |
| `internal/plugins/backends/anthropic/` | Essential Anthropic backend |
| `internal/plugins/backends/gemini/` | Essential Gemini backend |
| `internal/plugins/backends/bedrock/` | Essential AWS Bedrock backend |
| `internal/plugins/backends/openaicompat/` | Shared OpenAI-compatible helpers for essential custom-compatible kinds |
| `connectors/*` | Optional executable backend plugins (openrouter, nvidia, huggingface, ollama, local runtimes, opencode, codex, ACP family, localstub, …) |
| `connector-support/*` | Shared support modules for connectors (e.g. acp, openaicompat) |
| `internal/plugins/features/` | Feature plugins: submit/parts/tool-reactor no-ops, reference features, `toolcallrepair/` (YAML-only; ADR 0007) |

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
| `runtimebundle/` | `BuildHost` / Host / GenerationRuntime assembly from config |
| `usageauthority/configsource/` | Immutable validated authority rule snapshots with source freshness |
| `usageauthority/authoritystore/` | Clone-based memory store, Bun durable transaction adapter, live windows, reservations, decisions, and mutation log |
| `usageauthority/evidencesink/` | Policydecision/control-plane authority evidence adapter |

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
