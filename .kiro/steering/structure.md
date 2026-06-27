# Project Structure (Steering)

## Mental model

Treat the repository as a small runtime plus official plugins.
The architecture has five primary zones:

1. stable public contracts,
2. small internal core runtime,
3. official frontend plugins,
4. official backend and feature plugins,
5. test and operational support surfaces.

Around the core and plugins sit explicit **standard distribution** packages (`internal/pluginreg`, `internal/infra/runtimebundle`, `internal/stdhttp`) and **test-only harness** trees (`internal/refbackend`, `internal/refclient`) that must not blur ownership boundaries above.

## Repository layout

### 1. Public contracts

`pkg/lipapi/`
- canonical request, message, part, tool, capability, event, validation, collection, and error types
- protocol-neutral
- stable import surface for plugins and external tooling

`pkg/lipsdk/`
- plugin registration contracts
- frontend/backend/hook interfaces
- feature SDK facades for auth, session, workspace, request shaping, route hints, tools, completion gates, auxiliary calls, state, traffic, usage, model inventory, and continuity
- plugin metadata, factory inputs, and standard distribution requirements
- no core implementation details

Orchestration policy (routing, recovery, extension stages) lives in `internal/core`, not in these public trees; see `docs/architecture-guardrails.md` for dependency checks.

### 2. Internal core runtime

`internal/core/` owns product policy and cross-protocol orchestration. Current subpackages group into these capabilities:

- execution and lifecycle: `runtime/`, `execbackend/`, `execctx/`, `leglifecycle/`, `lineage/`
- routing and planning: `routing/`, `affinity/`, `policy/`
- continuity and sessions: `b2bua/`, `continuity/`, `securesession/`
- auth/access/trust: `accessmode/`, `auth/`, `admin/`, `http/`, `safety/`
- canonical support: `capabilities/`, `jsonpresence/`, `diag/`, `config/`
- streaming: `stream/`, `streamrecovery/`
- hooks and extension pipeline: `hooks/`, `extensions/`
- feature-facing core state: `auxreq/`, `state/`, `traffic/`, `workspace/`
- model and usage support: `modelcatalog/`, `modelregistry/`, `accounting/`, `tokenaccounting/`

Core rules:
- core imports `pkg/lipapi` and `pkg/lipsdk`,
- core does not import concrete plugins,
- core does not import official provider SDKs,
- core owns orchestration but not provider-specific protocol logic.

### 2a. Standard distribution assembly (not “another core”)

`internal/pluginreg/`
- explicit per-composition-root registration for the standard distribution (`NewRegistry` + `InstallStandardBundleOn(reg, keys)`)
- standard frontend/backend/feature bundle tables; exact standard plugin registration lives in `standard_table.go`
- per-family `*_install.go` factory helpers
- registry validation helpers, default wire metadata used by routing defaults, and backend credential-posture metadata

`internal/infra/runtimebundle/`
- composes a runnable `Built` from config + registrations: executor, continuity and secure-session stores, shared upstream HTTP client, health/observer seams, model/catalog support, token accounting, and security policy checks

`internal/stdhttp/`
- standard HTTP surface: route mounting, transport auth/principal attachment, security guard, recovery, diagnostics, model-catalog status, access logs, `Run` / `RunWithRuntime` entrypoints consumed by `cmd/lipstd`

### 3. Official frontend plugins

`internal/plugins/frontends/`
- wire frontends: `openairesponses/`, `openailegacy/`, `anthropic/`, `gemini/`
- shared frontend helpers: `decodeqos/`, `execerr/`, `exechold/`, `frontendconfig/`, `holdalive/`, `jsonguard/`, `limits/`, `openaiwire/`, `parity/`, `reqbody/`, `routeselect/`, `sessionwire/`

Wire frontend packages decode incoming HTTP/SSE requests into canonical requests and encode canonical events back into protocol-specific responses. Helper packages should stay frontend-owned and not leak provider SDK types into core.

### 4. Official backend plugins

`internal/plugins/backends/`
- standard hosted/provider adapters: `openairesponses/`, `openailegacy/`, `anthropic/`, `gemini/`, `bedrock/`, `acp/`, `openrouter/`, `nvidia/`, `huggingface/`, `openaicodex/`, `opencodego/`, `opencodezen/`
- local/OpenAI-compatible adapters: `ollama/`, `llamacpp/`, `lmstudio/`, `vllm/`, `localstub/`, `openaicompat/`
- shared backend helpers/protocols: `checkcfg/`, `credpool/`, `modeldiscover/`, `openaicaps/`, `openaicred/`, `openaifamily/`, `openaiusage/`, `opencodecommon/`, `protocols/`, `streampeek/`

These packages turn canonical requests into upstream calls and map upstream responses into canonical events.
Provider SDKs and provider wire models stay here or in backend-private protocol helper packages.

`internal/plugins/openrouterwire/`
- shared OpenRouter extension payload helpers used by OpenRouter-compatible paths

`internal/plugins/openaiutil/`
- currently empty/reserved; do not build new code here unless a real shared OpenAI adapter need appears

`internal/plugins/stores/`
- bundled persistence / continuity store plugin seam; may remain sparse
- current SQLite continuity implementation lives in `internal/core/continuity/sqlitestore/`, not here

### 5. Official feature plugins and hook implementations

`internal/plugins/features/`
- bundled no-op compatibility hooks: `submitnoop/`, `partsnoop/`, `toolreactornoop/`
- reference/proof features: `refsubmit/`, `refparts/`, `reftool/`, `reftoolpolicy/`, `refautoappend/`, `refworkspaceguard/`, `reftraffictranscript/`, `refverifier/`, `prerequestpolicy/`, `codexclientcompat/`, and related proof directories
- feature plugins are expected to consume `pkg/lipsdk` facades rather than `internal/core`

Hooks and extension stages are seams, not an excuse to reintroduce god objects.

### 6. Support surfaces

`internal/infra/`
- cross-cutting infrastructure seams shared by runtime and plugins: HTTP client tuning (`httpclient`), structured logging helpers, Prometheus metrics wiring (`metrics`), OpenTelemetry tracing bootstrap (`tracing`), DB helpers (`db`), auth-event sinks, model catalog/registry loaders, routing health, token accounting/tokenizers, clocks, ids, OS identity checks, and other adapters not specific to one protocol codec

`internal/refbackend/`
- spec-shaped HTTP **emulator** servers for integration tests; import only from `*_test.go` (must not appear on production dependency paths)

`internal/refclient/`
- official-SDK-based **reference clients** for conformance and matrix tests; not for production runtime wiring

`internal/testkit/`
- stub providers, fixture loaders, fake streams, fake stores, fake clocks, builders, synthetic credentials, model-catalog snapshots, and conformance helpers

`internal/safecast/`
- small shared numeric conversion helpers

`internal/qa/`
- repository hygiene and other non-domain quality tests (for example root-level file policy)

`internal/archtest/`
- architecture guardrail tests (complexity budgets, dependency direction, forbidden `init` patterns in the standard bundle path, etc.)

`testdata/`
- golden protocol payloads
- routing selector fixtures
- canonical event fixtures
- migration captures reused from Python LIP where appropriate

`docs/`
- architecture notes, operator docs, migration guides, plugin authoring docs, release gates, performance checks, and specification-bundle indexes

`.kiro/`
- steering and spec-driven development artifacts; active specs live under `.kiro/specs/`, completed historical specs under `.kiro/specs/archive/`

## Where to change code (by intent)

- Frontend/API behavior: `internal/plugins/frontends/`
- Backend provider behavior: `internal/plugins/backends/`
- Bundled store / persistence plugin seams: `internal/plugins/stores/`; current core continuity stores: `internal/core/continuity/`
- Standard distribution registration tables: `internal/pluginreg/`
- Lipstd HTTP wiring: `internal/stdhttp/`
- Wiring executor + continuity + shared clients from config: `internal/infra/runtimebundle/`
- Canonical model changes: `pkg/lipapi/`
- Plugin contract changes: `pkg/lipsdk/`
- Routing, failover, B2BUA continuity: `internal/core/routing/`, `internal/core/b2bua/`, and `internal/core/continuity/`
- Stream semantics and collectors: `internal/core/stream/` and `internal/core/streamrecovery/`
- Config semantics for the runtime: `internal/core/config/`
- Secure-session authority, resume policy, and session diagnostics: `internal/core/securesession/`, `internal/infra/runtimebundle/`, `internal/stdhttp/`
- Extension-platform stages and SDK facade assembly: `internal/core/extensions/`, `pkg/lipsdk/*`, `internal/pluginreg/`
- Model catalog/registry and capability inventory: `internal/core/modelcatalog/`, `internal/core/modelregistry/`, `internal/infra/modelcatalog/`, `internal/infra/modelregistry/`, `pkg/lipsdk/modelinventory/`
- Token accounting / usage: `internal/core/tokenaccounting/`, `internal/infra/tokenaccounting/`, `internal/infra/tokenizers/`, `pkg/lipsdk/usage/`
- Observability and supporting infra: `internal/infra/` or feature plugins
- Reference emulators/clients for tests: `internal/refbackend/`, `internal/refclient/`
- Repo-wide hygiene checks: `internal/qa/`
- Architecture budgets and import-pattern tests: `internal/archtest/`

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

## Naming and import conventions

- package names are short, lowercase, and singular where practical.
- avoid stutter such as `routing.RouterService`.
- define interfaces where they are consumed.
- keep exported surface area small.
- prefer internal packages for code that should not be imported externally.

## Pragmatic hexagonal guidance

Apply hexagonal architecture here as an ownership and dependency-direction discipline, not as a directory-renaming exercise.

- keep the existing package map when it already expresses a clean boundary,
- prefer selective seam extraction over repo-wide package churn,
- place new seams near the consuming capability, not in generic `ports`, `interfaces`, or `services` buckets,
- prefer concrete inbound services for driving adapters unless multiple real consumers justify an interface,
- distinguish pure domain policy, application/use-case orchestration, and edge translation when a feature becomes complex enough to need those names,
- keep transactions, durable writes, and outbox-style side effects explicit at the orchestration boundary; never leak driver handles into core policy,
- use dedicated read/query adapters for operator views, diagnostics, or reporting when a write-shaped repository would hide intent,
- allow dedicated query adapters and read DTOs for diagnostics, admin, or reporting flows when they are simpler than repository-shaped write abstractions,
- do not create interfaces only for mocking or symmetry.

This means a seam may legitimately be:

- a small interface,
- a narrow function-typed contract,
- or a frozen concrete struct,

as long as it gives the core a real substitution boundary and keeps technology details at the edge.

---
_Initial Go steering version: 2026-04-20_
_Updated 2026-04-23: pragmatic hexagonal guidance for seam placement, query adapters, and inbound concrete services._
_Reason: reflect the current brownfield direction: preserve the working package map, tighten ownership, and avoid architecture theater._
_Updated 2026-04-26: refreshed package map for secure sessions, extension snapshots, panic isolation, credential posture, and startup guardrails._
_Reason: steering had drifted from the hardened Go runtime and stage-four extension-platform implementation._
_Updated 2026-04-26: added optional hexagonal ownership prompts for domain/app/adapters, explicit transactions, and query seams._
_Reason: future specs can benefit from ports-and-adapters discipline without requiring repo-wide restructuring._
_Updated 2026-06-27: refreshed current package map, backend/frontend helper packages, infra surfaces, and store-seam wording._
_Reason: steering had drifted from the current standard distribution and package layout._
