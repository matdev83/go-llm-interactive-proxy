# Agent Rules

## Identity

- Go implementation of LIP: small core, explicit plugins, runnable `cmd/lipstd` distribution.
- Describe Go behavior only; mark Python-era/future behavior explicitly.
- Python sibling repo, when needed: `C:\Users\Mateusz\source\repos\llm-interactive-proxy`.
- Product: universal translation, routing, and control plane for AI clients.

## Source Of Truth

- Steering is durable project memory: `.kiro/steering/`.
- Fast package map: `.kiro/steering/structure.md`.
- API/translation rules: `.kiro/steering/api-standards.md`.
- Routing/failover/B2BUA rules: `.kiro/steering/routing-and-orchestration.md`.
- Tech/tooling rules: `.kiro/steering/tech.md`.
- Test policy: `.kiro/steering/testing.md`.
- Do not add changelog, `_Updated`, `_Reason`, timestamp, or history footers to steering or agent instructions; git tracks history.

## Work Rules

- TDD by default: test/interface first, implementation second.
- Smallest correct diff wins; avoid speculative abstractions.
- Ask only when intent materially changes the result and repo context cannot resolve it.
- Never claim success without direct verification evidence.
- Preserve user-authored changes; never use destructive git commands unless explicitly requested.

## Skill Loading

- Architecture/package boundary/feature design: `golang-hexagonal-architecture`, `golang-design-patterns`.
- Constructors/lifecycle/interfaces/facades: add `golang-dependency-injection` or `golang-structs-interfaces`.
- Tests/conformance/regressions: `golang-testing`; add `golang-stretchr-testify` for testify code.
- Streaming/concurrency/cancellation: `golang-concurrency`, `golang-context`.
- Error/security/observability/database/CLI/performance/lint/deps/docs/troubleshooting: load the matching `golang-*` skill.
- Simplification/refactor-only: `go-simplify`.
- Repo steering overrides generic skill defaults.

## Architecture Guardrails

- Core owns orchestration, routing, failover, and B2BUA continuity.
- Provider semantics stay in adapters/plugins.
- Core must not import provider SDKs or concrete plugins.
- No pairwise protocol translators; use protocol <-> canonical adapters only.
- Streaming is primary; non-streaming collects the canonical stream.
- No transparent retry/failover after first downstream content event.
- Capability mismatches fail explicitly; never silently drop required semantics.
- Request/response mutation belongs behind hooks/extensions, not core branching.
- Use explicit construction/registration; no DI containers, reflection registries, globals, or Go native `plugin` in v1.

## Package Zones

- `pkg/lipapi/`: canonical request/event/capability/error contracts.
- `pkg/lipsdk/`: plugin SDK, facades, registration contracts.
- `internal/core/`: runtime orchestration, routing, continuity, streams, hooks/extensions, config, diagnostics.
- `internal/plugins/frontends/`: OpenAI Responses, OpenAI legacy, Anthropic, Gemini frontends.
- `internal/plugins/backends/`: provider/local/compatible backend adapters; exact bundle in `internal/pluginreg/standard_table.go`.
- `internal/plugins/features/`: official feature and reference plugins.
- `internal/pluginreg/`, `internal/infra/runtimebundle/`, `internal/stdhttp/`: standard distribution composition.
- `internal/refbackend/`, `internal/refclient/`, `internal/testkit/`: test-only emulators, reference clients, stubs, fixtures.
- `internal/archtest/`, `internal/qa/`: architecture and hygiene gates.

## Kiro Specs

- Specs are opt-in only: `/kiro:*`, explicit `.kiro/specs/...`, or explicit spec-driven request.
- Use specs for new features, breaking changes, architecture changes, protocol/plugin contract changes, routing semantics, or unclear requirements.
- Direct-code small bug fixes, docs, narrow tests, and trivial maintenance.
- Spec flow: `spec-init` -> `requirements` -> `design` -> `tasks` -> `impl`.
- If an active spec is clearly in scope, do not code before approved `requirements.md` and `design.md` in `spec.json`.
- Kiro guide: `.kiro/AGENTS.md`.

## Verification

- Focused test: `go test -run TestName ./path/to/pkg`.
- Default unit: `make test-unit`.
- Quality gate: `make quality-checks`.
- Full default: `make test`.
- Cross-frontend/backend or protocol matrix: `make parity-checks`.
- Wide/release-grade change: `make qa`.
- Concurrency/streaming change: run race where practical; `make test-race` skips on Windows.
- Fuzz parser/decoder changes where practical: `make test-fuzz` or targeted `go test -fuzz=FuzzName$ -fuzztime=30s -run=^$ ./path`.

## Go Conventions

- Prefer stdlib; add dependencies only when they reduce complexity/risk.
- Keep public `pkg/lipapi` / `pkg/lipsdk` contracts minimal, documented, and versionable.
- Define small interfaces where consumed; constructors return concrete types unless exposing stable SDK/plugin contracts.
- Every I/O boundary takes `context.Context`; do not store contexts in structs.
- Own goroutines/channels/cancellation explicitly; avoid per-request handler goroutines.
- Return wrapped errors; frontends map internal errors to wire shapes.
- Keep config typed; pass plugin config as raw subtrees to factories.
- Preserve empty-vs-null JSON semantics; use `internal/core/jsonpresence` when presence matters.
- Use forward-slash git pathspecs on Windows.

## Reporting

- State changed files and verification run.
- State skipped tests or uncertainty plainly.
- Mention architectural trade-offs only when relevant.
