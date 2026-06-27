# Technology Stack (Steering)

## Stack summary

- Language/runtime: Go 1.26.x toolchain pinned in `go.mod` (patch version is authoritative; currently 1.26.4)
- HTTP server: standard library `net/http`
- Logging: standard library `log/slog` composed with small `samber/slog-*` helpers where useful
- Serialization: standard library `encoding/json` by default; JSON null-vs-empty conventions live in `internal/core/jsonpresence` for encoded shapes that must round-trip cleanly
- YAML config: `gopkg.in/yaml.v3`
- Observability (optional, config-gated): Prometheus metrics (`prometheus/client_golang`), OpenTelemetry tracing (`go.opentelemetry.io/otel` + OTLP HTTP exporter)
- Persistence: standard `database/sql`, Bun-backed stores where needed, pure-Go SQLite via `modernc.org/sqlite`
- Provider SDKs: official vendor Go SDKs inside backend plugins only
- Testing: standard library `testing`, `httptest`, fuzzing, race detector, and `go.uber.org/goleak` in packages with goroutine-heavy tests

Exact versions belong in `go.mod`. This file records stable technical patterns and guardrails.

## Runtime composition

### Small runtime, explicit wiring

The core is assembled through constructors and registration in composition roots.
Avoid DI containers, reflection-heavy registries, and service locators.

When tightening seams, prefer the smallest shape that improves ownership and testability.
That may be a consumer-owned interface, a function seam, or a narrow frozen struct.
Do not introduce repo-wide `ports` packages or symmetry-driven interfaces unless they solve a real coupling problem.

`cmd/lipstd/` is expected to:
- load runtime config,
- create a standard registry,
- register official frontend plugins,
- register official backend plugins,
- register official feature plugins,
- assemble `runtimebundle.Built`,
- start the HTTP server.

### Canonical contract pattern

The implementation translates all protocols through:
- a canonical request model in `pkg/lipapi`, and
- a canonical event stream in `pkg/lipapi`.

Consequences:
- no pairwise translators,
- non-streaming is collection over stream events,
- capability negotiation happens before backend execution.

### Plugin pattern

Use explicit registration and static linking for v1.

Reasons:
- simpler builds,
- portable binaries,
- race detector support remains intact,
- plugin boundaries are enforced through contracts instead of dynamic loading magic.

Do not use Go's native `plugin` package in v1.

## Provider integration policy

Use official Go SDKs where practical and keep them at backend edges:

- OpenAI: `github.com/openai/openai-go/v3`
- Anthropic: `github.com/anthropics/anthropic-sdk-go`
- Google Gemini / GenAI: `google.golang.org/genai`
- Bedrock: `aws-sdk-go-v2/service/bedrockruntime`
- ACP: thin local transport/client built from official protocol definitions and JSON-RPC semantics

Other standard backends are HTTP-compatible, local-runtime, or agent-specific adapters (OpenRouter, NVIDIA, Hugging Face, OpenAI Codex, OpenCode Go/Zen, Ollama (`ollama` / `ollama-cloud`), llama.cpp, LM Studio, vLLM, `localstub`, custom-compatible rows). They should reuse shared compatible-protocol helpers where that reduces duplication without moving provider semantics into core.

Official SDK types and provider wire structs must not leak into `pkg/lipapi`, `pkg/lipsdk`, or `internal/core`.

## Concurrency and streaming patterns

- Every external call receives `context.Context`.
- Streaming is the default provider contract.
- Keepalive and flush behavior are implemented centrally in stream components.
- Preserve deterministic event ordering.
- Avoid complex channel topologies when a simple iterator, callback, or pump object is clearer.
- Never retry after the first client-visible content event.
- Do not add per-request handler goroutines without extending the allowlist and recording the reason.

## Security and startup posture

Security-sensitive runtime behavior should fail closed at composition or startup boundaries:

- `no_auth` is for explicit loopback single-user operation only,
- standard HTTP startup refuses administrative/root-style execution,
- backend factories declare credential posture so non-local deployments reject unknown or user-OAuth credentials early,
- secure-session wiring is mandatory for the standard execution path; legacy continuity-only execution should not reappear silently,
- diagnostics, pprof, metrics, model-catalog diagnostics, and session summaries require deliberate exposure and shared-secret posture when enabled.

Keep these checks out of protocol codecs. Config, runtimebundle, plugin registration, and stdhttp are the right enforcement zones.

## Routing and resilience patterns

Routing is core-owned because it defines product behavior, not provider behavior.

Stable routing concepts:
- strict selector parsing,
- model alias expansion,
- weighted routing,
- ordered failover,
- parallel routing races,
- TTFT budgets and handicaps,
- eligibility filtering by capability and health,
- bounded attempt budgets,
- explicit pre-output vs post-output failure handling,
- B2BUA A-leg and B-leg continuity identifiers.

## Extension platform pattern

Feature expansion uses the stage-four extension platform:

- the core owns the fixed legal stage list and immutable per-request runtime snapshots,
- `pkg/lipsdk/*` packages expose narrow facades for session, workspace, request shaping, route hints, tool catalogs, auxiliary calls, completion gates, state, traffic, transport auth, usage, and model inventory,
- hook-only plugins remain supported through compatibility bundle assembly,
- new advanced behavior should extend the platform or add a feature plugin instead of branching executor/provider code.

## Configuration patterns

- Keep runtime config typed and minimal.
- Parse core config into typed structs.
- Pass plugin-specific config as raw subtrees to plugin factories.
- Avoid framework-style config mutation at startup.
- Prefer immutable config after construction.
- Validate startup posture before serving network traffic.

## Error and diagnostics patterns

- Use wrapped Go errors with classification metadata.
- Map internal errors to protocol-specific responses at the frontend edge.
- Emit structured logs for routing decisions, attempts, failovers, cancellations, auth decisions, and session events.
- Keep diagnostics and observability as orthogonal concerns: HTTP diagnostics routes are core-owned; optional `/metrics` and OTLP tracing are wired through `internal/infra/` when enabled in config.

## Tooling expectations

Default verification stack is Makefile-first:
- `make quality-checks` for format/tidy/build/vet/guard scripts/archtest
- `make test` for quality checks, default unit tests, and conformance parity checks
- `make test-unit` for `go test -parallel=8 -timeout=10m ./...`
- `make parity-checks` for `go test -parallel=8 -timeout=10m -tags=precommit,integration ./internal/testkit/conformance/...`
- `make test-precommit-extra` for repo hygiene and executor matrices
- `make test-race` for race scan (skipped on Windows; strict in CI on Linux)
- `make test-fuzz` for release-gate fuzz smoke
- `make qa` for quality checks, one full tagged test pass, golangci-lint v2, and `go tool govulncheck ./...`

Optional repo tooling may add:
- `make bench` for benchmark smoke across hot packages (see `docs/performance-checks.md`)
- weekly CI benchmark artifact upload (`.github/workflows/benchmarks.yml`, manual `benchstat` workflow)
- reproducible conformance runners
- fixture update helpers

## Dependency policy

Add dependencies only when they clearly reduce complexity or risk.
Default preference order:
1. standard library,
2. small focused library with strong adoption,
3. larger framework only with explicit architectural justification.

## Pragmatic port and query rules

- define outbound seams where the core consumes them,
- keep inbound seams concrete by default for driving adapters,
- use interfaces for real substitution boundaries, not just mocks,
- keep ports business-shaped or capability-shaped; do not expose HTTP, SQL, ORM, provider SDK, or queue SDK types through them,
- let app/use-case code own workflow order and transaction intent when a capability spans multiple writes, stores, or side effects,
- let adapters own transport/storage/provider translation, retries, and known infrastructure-to-core error mapping,
- prefer an explicit transactor/outbox-style seam over hidden "save then publish" flows when durability and publication must line up,
- allow dedicated query/read adapters for read-only operator and diagnostic flows,
- avoid forcing every read through repository-style write abstractions.

---
_Initial Go steering version: 2026-04-20_
_Updated 2026-04-23: pragmatic seam-shape rules and query/read guidance for the current architecture direction._
_Reason: capture current technical defaults for a small-core, explicitly wired, pragmatically hexagonal Go runtime._
_Updated 2026-04-26: added startup-security, mandatory secure-session, and stage-four extension-platform technical patterns._
_Reason: recent runtime hardening changed the durable engineering defaults for new work._
_Updated 2026-04-26: added optional hexagonal port, adapter, transaction, and query-flow guidance._
_Reason: future growth benefits from explicit dependency direction without making hexagonal structure mandatory everywhere._
_Updated 2026-06-27: refreshed toolchain, provider families, Makefile-first verification, and current runtime surfaces._
_Reason: steering had drifted from current standard-bundle and CI behavior._
