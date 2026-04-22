# Technology Stack (Steering)

## Stack summary

- Language/runtime: Go 1.26.x toolchain pinned in `go.mod`
- HTTP server: standard library `net/http`
- Logging: standard library `log/slog`
- Serialization: standard library `encoding/json` by default
- YAML config: `gopkg.in/yaml.v3`
- Provider SDKs: official vendor Go SDKs inside backend plugins only
- Testing: standard library `testing`, `httptest`, fuzzing, and race detector

Exact versions belong in `go.mod`. This file records stable technical patterns and guardrails.

## Runtime composition

### Small runtime, explicit wiring

The core is assembled through constructors and registration in composition roots.
Avoid DI containers, reflection-heavy registries, and service locators.

`cmd/lipstd/` is expected to:
- load runtime config,
- register official frontend plugins,
- register official backend plugins,
- register official feature plugins,
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

Use official Go SDKs where practical:
- OpenAI: `openai-go`
- Anthropic: `anthropic-sdk-go`
- Google Gemini / GenAI: `googleapis/go-genai`
- Bedrock: `aws-sdk-go-v2/service/bedrockruntime`
- ACP: thin local transport/client built from official protocol definitions and JSON-RPC semantics

Official SDK types must not leak into `pkg/lipapi`, `pkg/lipsdk`, or `internal/core`.

## Concurrency and streaming patterns

- Every external call receives `context.Context`.
- Streaming is the default provider contract.
- Keepalive and flush behavior are implemented centrally in stream components.
- Preserve deterministic event ordering.
- Avoid complex channel topologies when a simple iterator, callback, or pump object is clearer.
- Never retry after the first client-visible content event.

## Routing and resilience patterns

Routing is core-owned because it defines product behavior, not provider behavior.

Stable routing concepts:
- strict selector parsing,
- weighted routing,
- ordered failover,
- eligibility filtering by capability,
- bounded attempt budgets,
- explicit pre-output vs post-output failure handling,
- B2BUA A-leg and B-leg continuity identifiers.

## Configuration patterns

- Keep runtime config typed and minimal.
- Parse core config into typed structs.
- Pass plugin-specific config as raw subtrees to plugin factories.
- Avoid framework-style config mutation at startup.
- Prefer immutable config after construction.

## Error and diagnostics patterns

- Use wrapped Go errors with classification metadata.
- Map internal errors to protocol-specific responses at the frontend edge.
- Emit structured logs for routing decisions, attempts, failovers, and cancellations.
- Keep diagnostics and observability as orthogonal concerns.

## Tooling expectations

Default verification stack:
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `staticcheck ./...`
- `go tool govulncheck ./...` (pinned in `go.mod`)

Optional repo tooling may add:
- `golangci-lint`
- reproducible conformance runners
- fixture update helpers

## Dependency policy

Add dependencies only when they clearly reduce complexity or risk.
Default preference order:
1. standard library,
2. small focused library with strong adoption,
3. larger framework only with explicit architectural justification.

---
_Initial Go steering version: 2026-04-20_
_Reason: capture the technical defaults for the Go rewrite and prevent a repeat of the Python-era coupling patterns._
