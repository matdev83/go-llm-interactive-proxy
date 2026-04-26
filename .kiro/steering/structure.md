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

## Proposed repository layout

### 1. Public contracts

`pkg/lipapi/`
- canonical request, message, part, tool, capability, event, and error types
- protocol-neutral
- stable import surface for plugins and external tooling

`pkg/lipsdk/`
- plugin registration contracts
- frontend/backend/hook interfaces
- plugin metadata and factory inputs
- no core implementation details

Orchestration policy (routing, recovery, extension stages) lives in `internal/core`, not in these public trees; see `docs/architecture-guardrails.md` for the dependency checks.

### 2. Internal core runtime

`internal/core/`
- `runtime/` - request execution pipeline, secure-session prepare path, attempt lifecycle, and lifecycle orchestration
- `execbackend/` - core-owned backend opening contract consumed by the executor and implemented by backend plugins
- `execctx/` - stable per-request execution views for principals, sessions, workspace, tools, and hooks
- `routing/` - selector parsing, model aliases, weighted choice, ordered failover, eligibility filtering, health inputs
- `continuity/` - B2BUA-like A-leg resolution over `b2bua.Store` (memory or SQLite store implementations)
- `securesession/` - proxy-owned session authority, resume-token policy, audit records, diagnostics adapters, store contracts
- `extensions/` - stage-four legal extension pipeline runners, immutable request snapshots, and SDK facade assembly
- `stream/` - canonical event stream engine, collectors, keepalive, cancellation, and stream-panic isolation
- `hooks/` - compatibility hook bus, submit/tool policies, request/response part validation, and panic-safe hook dispatch
- `capabilities/` - capability negotiation and downgrade validation
- `config/` - typed config loading, effective defaults, startup/security validation for the runtime only
- `http/` - shared server wiring helpers and middleware primitives
- `diag/` - call/trace correlation, lineage views, crash attributes, and diagnostics query seams
- `admin/` - diagnostics, backend reactivation, and operator-facing endpoints
- `safety/` - panic isolation helpers for HTTP, backend, stream, extension, and worker boundaries
- `traffic/` - core-side traffic observation plumbing behind SDK-facing contracts

Core rules:
- core imports `pkg/lipapi` and `pkg/lipsdk`,
- core does not import concrete plugins,
- core does not import official provider SDKs,
- core owns orchestration but not provider-specific protocol logic.

### 2a. Standard distribution assembly (not “another core”)

`internal/pluginreg/`
- explicit per-composition-root registration for the standard distribution (`NewRegistry` + `InstallStandardBundleOn(reg)`)
- per-family `*_install.go` tables for frontends, backends, and features
- registry validation helpers, default wire metadata used by routing defaults, and backend credential-posture metadata

`internal/infra/runtimebundle/`
- composes a runnable `Built` from config + registrations: executor, continuity and secure-session stores, shared upstream HTTP client, health/observer seams, security policy checks

`internal/stdhttp/`
- standard HTTP surface: route mounting, transport auth/principal attachment, security guard, recovery, diagnostics, `Run` / `RunWithRuntime` entrypoints consumed by `cmd/lipstd`

### 3. Official frontend plugins

`internal/plugins/frontends/`
- `openairesponses/`
- `openailegacy/`
- `anthropic/`
- `gemini/`

These packages decode incoming HTTP/SSE requests into canonical requests and encode canonical events
back into protocol-specific responses.

### 4. Official backend plugins

`internal/plugins/backends/`
- `openairesponses/`
- `openailegacy/`
- `anthropic/`
- `gemini/`
- `bedrock/`
- `acp/`

These packages turn canonical requests into upstream calls and map upstream responses into canonical events.

`internal/plugins/stores/`
- bundled persistence / continuity store implementations that belong with official plugins rather than `internal/core` (may be sparse early on; still an intentional seam)

### 5. Official feature plugins and hook implementations

`internal/plugins/features/`
- reference and bundled feature plugins are expected to consume `pkg/lipsdk` facades rather than `internal/core`
- proof plugins should demonstrate extension seams (session open, request shaping, tool policy, workspace safety, traffic observation/capture, completion gates, auxiliary calls)
- hook-only plugins remain valid through compatibility bundle assembly while richer seams mature

Hooks and extension stages are seams, not an excuse to reintroduce god objects.

### 6. Support surfaces

`internal/infra/`
- cross-cutting infrastructure seams shared by runtime and plugins: HTTP client tuning (`httpclient`), structured logging helpers, Prometheus metrics wiring (`metrics`), OpenTelemetry tracing bootstrap (`tracing`), clocks, ids, and other adapters not specific to one protocol codec

`internal/refbackend/`
- spec-shaped HTTP **emulator** servers for integration tests; import only from `*_test.go` (must not appear on production dependency paths)

`internal/refclient/`
- official-SDK-based **reference clients** for conformance and matrix tests; not for production runtime wiring

`internal/qa/`
- repository hygiene and other non-domain quality tests (for example root-level file policy)

`internal/archtest/`
- architecture guardrail tests (complexity budgets, forbidden `init` patterns in the standard bundle path, etc.)

`internal/testkit/`
- stub providers, fixture loaders, fake streams, fake stores, fake clocks, builders

`testdata/`
- golden protocol payloads
- routing selector fixtures
- canonical event fixtures
- migration captures reused from Python LIP where appropriate

`docs/`
- architecture notes, operator docs, migration guides, plugin authoring docs

`.kiro/`
- steering and spec-driven development artifacts

## Where to change code (by intent)

- Frontend/API behavior: `internal/plugins/frontends/`
- Backend provider behavior: `internal/plugins/backends/`
- Bundled store / persistence plugins: `internal/plugins/stores/`
- Standard distribution registration tables: `internal/pluginreg/`
- Lipstd HTTP wiring: `internal/stdhttp/`
- Wiring executor + continuity + shared clients from config: `internal/infra/runtimebundle/`
- Canonical model changes: `pkg/lipapi/`
- Plugin contract changes: `pkg/lipsdk/`
- Routing, failover, B2BUA continuity: `internal/core/routing/` and `internal/core/continuity/`
- Stream semantics and collectors: `internal/core/stream/`
- Config semantics for the runtime: `internal/core/config/`
- Secure-session authority, resume policy, and session diagnostics: `internal/core/securesession/`, `internal/infra/runtimebundle/`, `internal/stdhttp/`
- Extension-platform stages and SDK facade assembly: `internal/core/extensions/`, `pkg/lipsdk/*`, `internal/pluginreg/`
- Observability and supporting infra: `internal/infra/` or feature plugins
- Reference emulators/clients for tests: `internal/refbackend/`, `internal/refclient/`
- Repo-wide hygiene checks: `internal/qa/`
- Architecture budgets and import-pattern tests: `internal/archtest/`

## Structural guardrails

- No protocol-specific branching inside core packages.
- No provider SDK imports outside backend plugins.
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
