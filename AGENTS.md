# Rules for coding agents

Language: English.

## Kiro Spec-Driven Development

### When to use Kiro specs

Suggest spec workflow when the request involves new features, breaking changes, architecture changes,
protocol additions, plugin contract changes, routing semantics, or unclear requirements that need
structured analysis.

Code directly for small bug fixes, narrow test-only changes, documentation edits, or trivial maintenance,
unless the user explicitly asks for Kiro/spec-driven work.

### Opt-in scope

Kiro specs are user-driven and opt-in.

Enforce spec gating such as "no code edits until requirements/design are approved" only when the user:
- invokes a `/kiro:*` command,
- explicitly references a spec name/path under `.kiro/specs/`, or
- explicitly asks for spec-driven development.

If the user does not mention Kiro or a spec path, proceed with normal engineering work. You may still
suggest a spec workflow for complex requests, but do not block implementation by default.

### Workflow order

`spec-init` -> `spec-requirements` -> `spec-design` -> `spec-tasks` -> `spec-impl`

When a spec exists at `.kiro/specs/{feature}/` and the current session is clearly about that spec, no code
edits before `requirements.md` and `design.md` are approved in `spec.json`.

Key locations:
- Specs: `.kiro/specs/{feature-name}/`
- Steering: `.kiro/steering/`
- Kiro workflow guide: `.kiro/AGENTS.md`
- Templates and rules: `.kiro/settings/`

## Project identity

Go-based LLM Interactive Proxy.

This repository is the greenfield Go re-implementation of the LIP (LLM Interactive Proxy Python app, GitHub: https://github.com/matdev83/llm-interactive-proxy) with **radically simpler** architecture.
Whenever needed or user refers to the LIP repo, you can access it directly as a sibling repo living in the following absolute dir: `C:\Users\Mateusz\source\repos\llm-interactive-proxy`.
It is a universal translation, routing, and control plane for AI clients.

Non-negotiable product traits:
- small core,
- plugin-first features,
- frontend support for OpenAI Responses, legacy OpenAI-compatible, Anthropic, and Gemini APIs,
- backend support for OpenAI Responses, legacy OpenAI-compatible, Anthropic, Gemini, Bedrock, and ACP,
- cross-API translation through a canonical request model and canonical event stream,
- streaming-first execution,
- core-owned routing, failover, and B2BUA-like continuity handling.

## Architecture guardrails

1. The core owns orchestration, not provider semantics.
2. Core packages must not import official provider SDKs.
3. Core packages must not import concrete plugins.
4. No pairwise protocol translators. Only protocol <-> canonical adapters.
5. Streaming is the primary path. Non-streaming is collected from the streaming path.
6. No transparent retry or failover after the first downstream content event is emitted.
7. Capability mismatches must fail explicitly. Never silently drop required semantics.
8. B2BUA-like behavior applies only to pre-output recoverable failures and attempt lineage.
9. Advanced request/response mutation belongs behind hook interfaces, not inside core business logic.
10. Prefer explicit construction and registration over DI containers, reflection, or global registries.
11. Do not use Go's native `plugin` package in v1.
12. Keep the core boring: narrow interfaces, small files, simple control flow.

## Repository layout

Treat these paths as the default structure unless a spec says otherwise:

- `cmd/lipstd/` - standard distribution binary that wires official plugins into the runtime.
- `pkg/lipapi/` - stable canonical request, event, capability, and error contracts.
- `pkg/lipsdk/` - stable plugin SDK and registration contracts for plugins outside the repo.
- `internal/core/` - runtime, orchestration, routing, capability negotiation, stream engine, config, admin.
- `internal/plugins/frontends/` - official frontend API adapters.
- `internal/plugins/backends/` - official backend API adapters.
- `internal/plugins/features/` - official feature plugins and hook implementations.
- `internal/infra/` - persistence, clocks, ids, logging helpers, metrics, and environment adapters.
- `internal/testkit/` - provider stubs, stream harnesses, fixture loaders, fake clocks, and builders.
- `testdata/` - golden protocol payloads, event streams, selector fixtures, and migration captures.
- `docs/` - architecture notes, operator docs, and migration notes.
- `.kiro/` - steering and spec artifacts.

## Quick start commands

Prefer repo-defined scripts or make targets if they exist. If not, default to these commands:

- `go test ./...`
- `go test -race ./...`
- `go test -run TestName ./path/to/pkg`
- `go test -fuzz=Fuzz -run=^$ ./path/to/pkg`
- `go vet ./...`
- `staticcheck ./...`
- `govulncheck ./...`
- `go run ./cmd/lipstd --config ./config/config.yaml`

If the repo adds `golangci-lint` or `make`, use the repo-standard command instead of inventing custom sequences.

## Go engineering standards

### Simplicity first

- Prefer the standard library unless a dependency clearly reduces complexity.
- Avoid framework-heavy abstractions.
- Avoid package sprawl. New packages need a clear boundary reason.
- Do not create abstractions for only one implementation unless a stable seam is required.

### Types and APIs

- Avoid `any` unless unavoidable at a protocol boundary.
- Keep provider-specific payload types inside adapters/plugins.
- Public contracts in `pkg/lipapi` and `pkg/lipsdk` must be versionable, documented, and minimal.
- Use small interfaces defined where they are consumed.
- Do not use Java-style interface prefixes. Use idiomatic Go names such as `Store`, `Router`, `Clock`.

### Concurrency and streaming

- Every I/O boundary takes `context.Context`.
- No package-level mutable global state in core code.
- Establish explicit ownership for goroutines, channels, buffers, and cancellation.
- Prefer simple push/pull stream abstractions over ad hoc channel webs.
- Preserve ordering guarantees for canonical event streams.
- Emit keepalive only through well-defined stream components.

### Error handling

- Return errors, do not panic in request paths.
- Wrap errors with `%w` and preserve classification metadata.
- Frontends are responsible for mapping internal errors to protocol-specific error shapes.
- Recoverable pre-output failures must carry enough metadata for routing and diagnostics.

### Configuration

- Keep config structs typed and explicit.
- Do not allow plugin config to leak into core config structs.
- Core passes plugin-specific raw config blobs into plugin factories.

## Testing standards

1. TDD is the default: write a failing test first.
2. Tests are behavior contracts, not implementation snapshots.
3. Run directly related tests before making claims.
4. Run race tests for concurrency or streaming changes.
5. Add regression tests for every bug fix in routing, translation, or streaming behavior.
6. Decoder and selector parsers should gain fuzz tests when practical.
7. Cross-protocol behavior must be verified with golden fixtures and stub providers.
8. Never claim a fix without test evidence or a reproducer.

High-value areas that always deserve tests:
- canonical request/event translation,
- capability negotiation,
- routing selector parsing,
- weighted routing and failover,
- B2BUA continuity and attempt lineage,
- stream cancellation,
- no-retry-after-first-output invariants,
- plugin isolation boundaries.

## File and package hygiene

- Keep core files small and cohesive.
- Avoid circular imports by design.
- Do not mix frontend codec logic, routing policy, and backend invocation in one package.
- Add package docs where the boundary is non-obvious.
- Keep tests near the package they validate unless a cross-package integration test is required.

## Git and editing rules

- Never use destructive git commands to wipe broad unreviewed changes.
- Revert only the exact files or hunks you intend to revert.
- Preserve user-authored changes unless explicitly asked to replace them.

## Reporting back to the user

- Never claim success unless you verified it.
- Be precise about what was tested and what was not.
- If you made an architectural trade-off, say what it was and why.
- If something is uncertain, say so plainly.
