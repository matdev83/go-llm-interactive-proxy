# Project Structure (Steering)

## Mental model

Treat the repository as a small runtime plus official plugins.
The architecture has five primary zones:

1. stable public contracts,
2. small internal core runtime,
3. official frontend plugins,
4. official backend and feature plugins,
5. test and operational support surfaces.

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

### 2. Internal core runtime

`internal/core/`
- `runtime/` - request execution pipeline and lifecycle orchestration
- `routing/` - selector parsing, weighted choice, ordered failover, eligibility filtering
- `continuity/` - B2BUA-like session resolution (`Manager`, `ResolveALegRecord`); the executor resolves A-legs through this package over `b2bua.Store`
- `stream/` - canonical event stream engine, collectors, keepalive, cancellation
- `capabilities/` - capability negotiation and downgrade validation
- `config/` - typed config loading and validation for the runtime only
- `http/` - shared server wiring, middleware, health/admin surfaces
- `admin/` - diagnostics, backend reactivation, and operator-facing endpoints

Core rules:
- core imports `pkg/lipapi` and `pkg/lipsdk`,
- core does not import concrete plugins,
- core does not import official provider SDKs,
- core owns orchestration but not provider-specific protocol logic.

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

### 5. Official feature plugins and hook implementations

`internal/plugins/features/`
- `routepolicy/` - advanced route strategies that extend core selectors without bloating the core
- `observe/` - usage logging, tracing, wire taps, metrics
- `mutate/` - request and response hook implementations
- `toolreactors/` - future tool call reactor implementations

Hooks are extension seams, not an excuse to reintroduce god objects.

### 6. Support surfaces

`internal/infra/`
- stores, clocks, id generation, entropy, persistence adapters, logging helpers

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
- Canonical model changes: `pkg/lipapi/`
- Plugin contract changes: `pkg/lipsdk/`
- Routing, failover, B2BUA continuity: `internal/core/routing/` and `internal/core/continuity/`
- Stream semantics and collectors: `internal/core/stream/`
- Config semantics for the runtime: `internal/core/config/`
- Observability and supporting infra: `internal/infra/` or feature plugins

## Structural guardrails

- No protocol-specific branching inside core packages.
- No provider SDK imports outside backend plugins.
- No frontend package may call provider SDKs directly.
- No feature plugin may depend on another concrete plugin without an explicit SDK contract.
- Non-streaming code must not become a second execution path.
- B2BUA continuity must stay isolated from protocol codec packages.
- Request/response mutation logic must live behind hooks, not in the routing engine.

## Naming and import conventions

- package names are short, lowercase, and singular where practical.
- avoid stutter such as `routing.RouterService`.
- define interfaces where they are consumed.
- keep exported surface area small.
- prefer internal packages for code that should not be imported externally.

---
_Initial Go steering version: 2026-04-20_
_Reason: establish the default repository map for the greenfield rewrite and make ownership boundaries explicit._
