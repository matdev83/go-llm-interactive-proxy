# Project Structure (Steering)

## Architecture Overview

Five-zone modular design: stable public contracts at the edge, a policy-owning internal core, frontend/backend/feature plugins, and infrastructure/harness support.

```text
                    ┌─────────────────────────┐
                    │      pkg/lipapi         │  Canonical Contracts
                    └───────────┬─────────────┘
                                │
┌──────────────────┐  ┌─────────▼───────────┐  ┌──────────────────┐
│ Frontends (FEs)  ├──►    internal/core    ◄──┤  Backends (BEs)  │
│ (openresponses,  │  │  (routing, b2bua,   │  │ (hosted, acp,    │
│  responses, chat,│  │   authoritycoord,   │  │  connectors)     │
│  anthropic, gmi) │  │   securesession)    │  │                  │
└──────────────────┘  └─────────▲───────────┘  └──────────────────┘
                                │
                    ┌───────────┴─────────────┐
                    │      pkg/lipsdk         │  Plugin Seams & SDK Facades
                    └─────────────────────────┘
```

---

## Package Inventory by Zone

### 1. Public Contracts (Stable Surface)

- `pkg/lipapi/` — Protocol-neutral canonical request, item, part, tool, event, capability, limit, and error types. Zero provider SDK or HTTP dependencies.
- `pkg/lipsdk/` — Plugin registration contracts, frontend/backend/hook interfaces, SDK facades (`auth`, `session`, `workspace`, `request`, `routehint`, `toolcatalog`, `toolpolicy`, `completion`, `auxiliary`, `state`, `traffic`, `usage`, `modelinventory`, `securesession`, `continuation`).
  - `pkg/lipsdk/secretguard/` — Ingress secret-guard contracts (`Guard`, `Matcher`, `DecisionEvent`).
  - `pkg/lipsdk/configreload/` — Secret-safe runtime reload contract (`Trigger`, `Result`, `Status`, `HistoryEntry`).
  - `pkg/lipsdk/backendplugin/` — Versioned gRPC connector ABI, DTOs, and table-driven converter helpers.

### 2. Internal Core Runtime (`internal/core/`)

Core owns orchestration and policy. Core imports `pkg/lipapi` and `pkg/lipsdk`; core **never** imports concrete plugins or provider SDKs.

- **Execution & Lifecycle**: `runtime/` (executor), `execbackend/`, `execctx/`, `leglifecycle/`, `lineage/`, `terminal/`, `terminalwork/`, `continuation/`
- **Routing & Policy**: `routing/` (selector parser, failover, parallel race, TTFT budgets), `affinity/`, `policy/`, `modelview/`
- **Authority Coordination & Control Plane**:
  - `authoritycoord/` — Stage evaluator (`stage_evaluator.go`), attempt coordination, attempt-stage settle failure recording.
  - `concurrencyauthority/` & `usageauthority/` — Principal turn and usage quota tracking.
  - `authorityattribution/` — Leg attribution tracking.
  - `controlplane/` — Ledgerstore projections (`usage_projector.go`), metering bridges, readiness reports (`readiness_report.go`), query bounds, privacy guardrails.
  - `metering/` — Usage/cost metering models.
- **Continuity & Sessions**: `b2bua/` (attempt lineage/store), `continuity/` (`bunstore`), `securesession/` (`adapters/`, `storecontract/`, `domain/`, `app/`)
- **Auth, Security & Identity**: `accessmode/`, `auth/`, `admin/`, `http/`, `safety/`, `proxycredentials/`, `identity/`, `secretsguard/` (ingress secrets catalog/matcher)
- **Canonical Support & State**: `capabilities/`, `jsonpresence/`, `jsonshape/` (preflight guards), `toolcallrepair/`, `diag/`, `config/`, `configreload/`, `interleavedthinking/` (reasoning memo store/shape), `interleavedstate/`, `snapshotgen/`
- **Streaming**: `stream/` (canonical stream, event pumps), `streamrecovery/`
- **Hooks & Extensions**: `hooks/` (stage evaluation), `extensions/` (stage-four extension platform)
- **Core State & Accounting**: `auxreq/`, `state/`, `traffic/`, `workspace/`, `modelcatalog/`, `modelregistry/`, `accounting/`, `tokenaccounting/`

### 2a. Composition & Standard Distribution Assembly

- `internal/pluginreg/` — Standard distribution plugin registry & validation.
- `internal/standardplugins/` — Built-in bundle tables (`standard_table.go`), `InstallStandardBundleOn`, `ResolveUpstreamAPIKeysFromEnv`.
- `internal/featurebundle/` — Feature merge engine (`MergeFeatureSurface`).
- `internal/infra/runtimebundle/` — Process `Host` builder (`runtimebundle.BuildHost`), immutable generation management (`GenerationRuntime`), shutdown coordinator; the host lifecycle ends through `Host.Close`.
- `internal/stdhttp/` — Standard HTTP surface, route mounting, auth attachment, diagnostics, access logs.

### 3. Official Frontend Plugins (`internal/plugins/frontends/`)

Wire frontends translate protocol payloads <-> canonical contracts:
- **Wire Frontends**: `openresponses/` (OpenResponses 2026-04-24 API, HTTP/WS turns & continuation), `openairesponses/`, `openailegacy/`, `anthropic/`, `gemini/`
- **Frontend Helpers**: `frontendpipe/` (unified ServeHTTP pipeline & SSE `stream.PumpSSE`), `identitywire/` (product identity headers), `streamdebug/`, `decodeqos/`, `execerr/`, `exechold/`, `frontendconfig/`, `holdalive/`, `jsonguard/`, `limits/`, `openaiwire/`, `parity/`, `reqbody/`, `routeselect/`, `sessionwire/`

### 4. Official Backend Plugins & Connectors (Hybrid Architecture — ADR 0008)

- **Essential Hosted Backends** (`internal/plugins/backends/` — statically linked): `alibabatokenplanintl/`, `openairesponses/`, `openailegacy/`, `anthropic/`, `gemini/`, `bedrock/`
- **Custom-Compatible Helpers**: `openresponsescompat/`, `openaicompat/`, `compatibleutil/`, `transporterr/`, `checkcfg/`, `credpool/`, `httpidentity/`, `modeldiscover/`, `openaicaps/`, `openaicred/`, `openaifamily/`, `openaiusage/`, `protocols/`, `streampeek/`
- **Protocol Protocols**: `internal/plugins/protocols/openairesponsesitem` (exact OpenAI Responses reasoning-item Opaque schema).
- **Optional Backend Connectors** (`connectors/` — independent modules, gRPC ABI over IPC): `acp`, `agycliacp`, `codex`, `cursorcliacp`, `cursorsdk`, `geminicliacp`, `huggingface`, `llamacpp`, `lmstudio`, `localstub`, `nvidia`, `ollama`, `opencode`, `openrouter`, `vllm`.
- **Connector Support**: `connector-support/` (`acp/`, `openaicompat/`).

### 5. Official Feature Plugins (`internal/plugins/features/`)

- `reasoningpreservation/` — Default-on reasoning output capture/restore (`EventReasoningPart` + Chat/Anthropic/Codex dialects).
- `codexclientcompat/` — OpenAI Codex native compaction reasoning output preservation.
- `secretsguard/` — Ingress credential scanner & enforcement Guard.
- `toolcallrepair/` — Malformed tool-call YAML auto-repair.
- Proof/Ref Features: `refsubmit/`, `refparts/`, `reftool/`, `reftoolpolicy/`, `refautoappend/`, `refworkspaceguard/`, `reftraffictranscript/`, `refverifier/`, `prerequestpolicy/`, `submitnoop/`, `partsnoop/`, `toolreactornoop/`.

### 6. Support & Test Surfaces

- `internal/infra/` — HTTP client tuning, structured logging, Prometheus metrics, OTLP tracing, DB connectors, secret audit.
- `internal/refbackend/` — Test-only backend emulators (HTTP).
- `internal/refclient/` — Test-only official SDK reference clients.
- `internal/testkit/` — Stubs, fakes, fixtures, reasoning E2E plans (`reasoninge2e/`).
- `internal/reasoningreplay/` — Reasoning prefix matcher (`compatible-auto.v2`).
- `internal/qa/` & `internal/archtest/` — Repository hygiene & architecture guardrail gates.

The architecture gates also include the deterministic change-surface reporter at `internal/archtest/tools/changesurface`; it classifies Git paths and keeps profile-only shared-boundary footprint at zero.

---

## Quick Intent-to-Package Map

| Developer Intent | Target Directory / File |
| :--- | :--- |
| Add/modify client API format | `internal/plugins/frontends/<protocol>/` |
| Change unified HTTP/SSE pipeline | `internal/plugins/frontends/frontendpipe/`, `internal/core/stream/` |
| Add essential hosted backend | `internal/plugins/backends/<provider>/`, register in `internal/standardplugins/` |
| Add optional backend connector | `connectors/<name>/` (independent module with gRPC ABI) |
| Change stage evaluation / attempt logic | `internal/core/authoritycoord/` |
| Change control plane ledger / metering | `internal/core/controlplane/` |
| Change dual SQLite/Postgres persistence | `internal/core/continuity/bunstore/`, `internal/core/securesession/adapters/` |
| Modify canonical request/event structs | `pkg/lipapi/` |
| Modify plugin SDK or extension facades | `pkg/lipsdk/` |
| Change routing rules / selector syntax | `internal/core/routing/` |
| Change stream semantics or keepalives | `internal/core/stream/`, `internal/core/streamrecovery/` |
| Modify reasoning preservation | `internal/plugins/features/reasoningpreservation/`, `internal/core/interleavedthinking/` |
| Update standard HTTP server / auth | `internal/stdhttp/`, `internal/infra/runtimebundle/` |

---

## Architectural Guardrails

1. **No Core Leaks**: `internal/core` must never import provider SDKs or concrete plugins.
2. **No Pairwise Translators**: All translation flows `Frontend -> Canonical (pkg/lipapi) -> Backend`.
3. **Streaming First**: Non-streaming responses collect events over canonical streams.
4. **No Hidden Downgrade**: Unsupported required capabilities must fail explicitly before backend call.
5. **Pre-Output Swallowing Only**: Failover/retry allowed only before client-visible output starts. Committed legs cannot failover silently.
6. **No Dynamic Loading**: Essential backends are statically linked; optional backends use out-of-process gRPC IPC connectors (`connectors/`).
7. **Explicit Wiring**: No DI containers, reflection registries, global state, or `init()` setup functions.

## Structural guardrails

- No protocol-specific branching inside core packages.
- No provider SDK imports outside backend plugins and test/reference support.
- No frontend package may call provider SDKs directly.
- No feature plugin may depend on another concrete plugin without an explicit SDK contract.
- Non-streaming code must not become a second execution path.
- B2BUA continuity must stay isolated from protocol codec packages.
- Request/response mutation logic must live behind hooks or extension stages, not in the routing engine.
- Feature plugins should depend on `pkg/lipsdk` contracts, not `internal/core` implementation packages.
- Security startup checks belong in config/runtimebundle/stdhttp composition boundaries, not inside protocol codecs.
- Backend local-only access-scope enforcement belongs in standard registration/runtimebundle policy, not inside protocol codecs.
- Concrete dependency construction belongs in composition roots or adapter constructors, not in core workflow methods.

## Naming and import conventions

- package names are short, lowercase, and singular where practical.
- avoid stutter such as `routing.RouterService`.
- define interfaces where they are consumed.
- keep interfaces small; compose larger contracts from focused pieces only when a real seam requires it.
- constructors should return concrete types unless the package is intentionally exposing a stable SDK/plugin contract.
- keep exported surface area small.
- prefer internal packages for code that should not be imported externally.
- use compile-time interface assertions near implementations for important plugin, SDK, and adapter contracts.

## Pragmatic hexagonal guidance

Apply hexagonal architecture here as an ownership and dependency-direction discipline, not as a directory-renaming exercise.

For this repository, read the usual hexagonal terms through the current LIP package map:

- **domain/policy center:** canonical contracts in `pkg/lipapi` plus core policy packages under `internal/core/`.
- **application/use-case orchestration:** executor, routing, continuity, extension, and runtime assembly paths that coordinate multiple seams.
- **driving adapters:** HTTP frontends, CLI commands, admin/diagnostic HTTP surfaces, and transport auth entrypoints.
- **driven adapters:** backend plugins, stores, model/catalog providers, tokenizers, metrics/tracing exporters, and other infrastructure implementations.
- **composition roots:** `cmd/lipstd/`, `internal/pluginreg/`, `internal/infra/runtimebundle/`, and `internal/stdhttp/`.

- keep the existing package map when it already expresses a clean boundary,
- prefer selective seam extraction over repo-wide package churn,
- place new seams near the consuming capability, not in generic `ports`, `interfaces`, or `services` buckets,
- prefer concrete inbound services for driving adapters unless multiple real consumers justify an interface,
- distinguish pure domain policy, application/use-case orchestration, and edge translation when a feature becomes complex enough to need those names,
- keep transactions, durable writes, and outbox-style side effects explicit at the orchestration boundary; never leak driver handles into core policy,
- use dedicated read/query adapters for operator views, diagnostics, or reporting when a write-shaped repository would hide intent,
- allow dedicated query adapters and read DTOs for diagnostics, admin, or reporting flows when they are simpler than repository-shaped write abstractions,
- do not create interfaces only for mocking or symmetry.
- keep provider/vendor names and SDK enums at adapter edges unless they are explicit compatibility-surface identifiers, not canonical business concepts.

This means a seam may legitimately be:

- a small interface,
- a narrow function-typed contract,
- or a frozen concrete struct,

as long as it gives the core a real substitution boundary and keeps technology details at the edge.
