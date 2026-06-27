# Rules for coding agents

## Project identity

Go-based LLM Interactive Proxy.

This repository is the Go implementation of LIP (LLM Interactive Proxy) with a small core, explicit plugin boundaries, and a runnable standard distribution (`cmd/lipstd`). The sibling Python app (GitHub: https://github.com/matdev83/llm-interactive-proxy) remains useful historical and migration context, but docs and code claims in this repo must describe Go behavior unless explicitly marked as Python-era or future work.

Whenever needed or user refers to the LIP repo, you can access the Python sibling repo directly at `C:\Users\Mateusz\source\repos\llm-interactive-proxy`.

This project is meant as a universal translation, routing, and control plane for AI clients.

Non-negotiable product traits:
- small core,
- plugin-first features,
- frontend support for OpenAI Responses, legacy OpenAI-compatible, Anthropic, and Gemini APIs,
- backend support for hosted providers, OpenAI-compatible/local runtimes, agent-specific backends, custom-compatible rows, and ACP,
- cross-API translation through a canonical request model and canonical event stream,
- streaming-first execution,
- core-owned routing, failover, and B2BUA-like continuity handling.

## General rules

- Follow TDD approach. Interface changes and tests must come first. Code changes follow. Not the other way.
- While creating code or making fixes *ALWAYS* create extensive set of tests and run them and make sure the code is passing them before reporting back to the user.
- Thrieve for simplicity. Create smallest possible set of changes which satisfies requirements and user's request.
- Ask questions is user intention is not fully clear. Don't guess.

## Go skill loading expectations

Before non-trivial Go work, load the most relevant available skill(s) instead of relying on memory alone:

- Architectural decisions, new feature design, package-boundary changes, or extension/seam work: load `golang-hexagonal-architecture` and `golang-design-patterns`; add `golang-dependency-injection` or `golang-structs-interfaces` when constructor, lifecycle, interface, or facade design is involved.
- Test creation, regression coverage, conformance work, or test repair: load `golang-testing`; add `golang-stretchr-testify` when touching testify-based tests.
- Concurrency, streaming, cancellation, or goroutine ownership: load `golang-concurrency` and/or `golang-context`.
- Error handling, security, observability, database, CLI, performance, lint/style, dependency, documentation, or troubleshooting work: load the matching `golang-*` skill before editing those areas.
- Simplification/refactor-only work: load `go-simplify` and keep the diff smaller than the explanation.

Available Go-focused skills in this environment include: `go-simplify`, `golang-benchmark`, `golang-cli`, `golang-code-style`, `golang-concurrency`, `golang-context`, `golang-continuous-integration`, `golang-data-structures`, `golang-database`, `golang-dependency-injection`, `golang-dependency-management`, `golang-design-patterns`, `golang-documentation`, `golang-error-handling`, `golang-grpc`, `golang-hexagonal-architecture`, `golang-lint`, `golang-modernize`, `golang-naming`, `golang-observability`, `golang-performance`, `golang-popular-libraries`, `golang-project-layout`, `golang-safety`, `golang-samber-do`, `golang-samber-hot`, `golang-samber-lo`, `golang-samber-mo`, `golang-samber-oops`, `golang-samber-ro`, `golang-samber-slog`, `golang-security`, `golang-stay-updated`, `golang-stretchr-testify`, `golang-structs-interfaces`, `golang-testing`, and `golang-troubleshooting`.

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

- `cmd/lipstd/` — standard distribution binary that wires official plugins into the runtime.
- `pkg/lipapi/` — stable canonical request, event, capability, and error contracts.
- `pkg/lipsdk/` — stable plugin SDK and registration contracts for plugins outside the repo.
- `internal/core/` — orchestration (`runtime/`), routing, continuity + B2BUA store seams, stream engine, hook bus (`hooks/`), capabilities, config, HTTP/admin wiring, diagnostics helpers (`diag/`).
- `internal/plugins/frontends/` — official frontend API adapters (`openairesponses/`, `openailegacy/`, `anthropic/`, `gemini/`) plus shared wire/decode/session/routing helpers.
- `internal/plugins/backends/` — official backend adapters. The standard bundle currently includes OpenAI Responses, legacy OpenAI-compatible, Anthropic, Gemini, Bedrock, ACP, OpenRouter, NVIDIA, Hugging Face, OpenAI Codex, OpenCode Go/Zen, Ollama (`ollama` / `ollama-cloud`), llama.cpp, LM Studio, vLLM, `localstub`, and custom OpenAI/Anthropic-compatible backend kinds; exact registration lives in `internal/pluginreg/standard_table.go` and the mandatory distribution subset in `pkg/lipsdk/standard_bundle.go`.
- `internal/plugins/features/` — official feature plugins, compatibility hook plugins, and reference/proof implementations.
- `internal/pluginreg/`, `internal/infra/runtimebundle/`, `internal/stdhttp/` — registration tables and composition (`cmd/lipstd` → runnable HTTP server).
- `internal/infra/` — HTTP client tuning, structured logging helpers, Prometheus/OpenTelemetry wiring, DB helpers, model catalog/registry, routing health, token accounting/tokenizers, auth events, clocks, ids, and other non-codec adapters.
- `internal/testkit/` — provider stubs, stream harnesses, fixture loaders, fake clocks, and builders.
- `cmd/lipstd/testdata/` — operator JSON goldens for `routes` / `inventory` (see `docs/dogfood-local.md`); update when `golden_normalize_test.go` fails after intentional shape changes.
- `internal/refbackend/`, `internal/refclient/` — spec emulators and reference SDK clients for tests only (must not appear on production wiring paths).
- `internal/safecast/` — small shared numeric conversion helpers.
- `internal/qa/`, `internal/archtest/` — repo hygiene tests and architecture import/budget guardrails.
- `testdata/` — golden protocol payloads, event streams, selector fixtures, and migration captures.
- `docs/` — architecture notes, operator docs, migration notes, release gates, performance checks.
- `.kiro/` — steering and spec artifacts.

For a fuller package-by-package map (including where to edit for a given change), use [`.kiro/steering/structure.md`](.kiro/steering/structure.md).

### Windows Git paths

Use forward slashes in pathspecs (`internal/core/...`). On case-insensitive volumes, avoid paths that differ only by case from existing files. This clone sets `core.protectNTFS` locally to reduce accidental duplicate-path issues. When editing under `cmd/lipstd/`, keep that forward-slash form in the editor so tooling does not open a second buffer for the same file as `cmd\lipstd\...`.

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
- Steering: `.kiro/steering/` (see index below)
- Kiro workflow guide: `.kiro/AGENTS.md`
- Templates and rules: `.kiro/settings/`

### Steering index

Short guide to `.kiro/steering/` (enduring project memory; not spec-specific):

- [`product.md`](.kiro/steering/product.md) — product promise, capability pillars, current direction, non-goals.
- [`api-standards.md`](.kiro/steering/api-standards.md) — canonical middle, streaming-first, errors, versioning; frontend/backend compatibility surfaces.
- [`routing-and-orchestration.md`](.kiro/steering/routing-and-orchestration.md) — core-owned routing, failover, B2BUA pre-output recovery, attempt lineage, hook seams.
- [`structure.md`](.kiro/steering/structure.md) — repository zones, package map, where to change code by intent.
- [`tech.md`](.kiro/steering/tech.md) — stack, composition roots, provider SDK policy (SDKs only in backend plugins), concurrency.
- [`testing.md`](.kiro/steering/testing.md) — TDD philosophy, suite topology, build tags (`precommit`), `goleak`-enabled packages, high-value test targets, canonical commands, benchmark smoke references.

### Spec numbering vs requirement numbering

In archived specs such as `.kiro/specs/archive/go-core-reimplementation-v1/`, **task IDs** in `tasks.md` (for example task **10.1** = OpenAI Responses **backend** plugin) are unrelated to **requirement IDs** in `requirements.md` (for example requirement **10.1** = request-part **hooks**). Always use the filename (`tasks.md` vs `requirements.md`) to disambiguate.

### Backend protocol plugins (spec Task 10.x)

When following historical backend task guidance in `.kiro/specs/archive/go-core-reimplementation-v1/tasks.md` or a newer backend spec:

- **Emulator-first:** deliver the matching reference backend emulator task **10.0.x** before the corresponding `internal/plugins/backends/*` connector; use it for spec-faithful, deterministic tests (see `tasks.md` section 10 and 10.0).
- **Gates:** each backend task (10.1, 10.2, …) depends on its **10.0.n** emulator being completed and the spec cross-check recorded.
- **Tests:** include streaming/event coverage, usage propagation where applicable, and **multimodal** mapping tests per the spec (Requirement 15.8 and Task 10 bullet text).
- **SDKs:** use official vendor Go SDKs only inside backend plugins (see `.kiro/steering/tech.md`; OpenAI: `openai-go`). Do not import provider SDKs from `internal/core`, `pkg/lipapi`, or `pkg/lipsdk`.

## Quick start commands

Prefer repo-defined scripts or make targets:

- `make quality-checks` — gofmt, `go mod tidy` drift guard, `go build`, `go vet`
- `make test` — quality checks, `go test -parallel=8 -timeout=10m ./...`, and `make parity-checks` (default unit pass omits `//go:build precommit`; parity checks compile the conformance package with `-tags=precommit,integration`)
- `make test-precommit-extra` — `go test -tags=precommit` over `internal/qa` and `internal/core/runtime` (repo hygiene + executor regression matrices); used by the pre-commit gate via merged `test-staged` (see `LIP_TEST_PRECOMMIT` in `scripts/test-staged.*`); CI full suite uses `go test -tags=precommit,integration ./...` (see below)
- `make qa` — quality checks, **one** full `go test -tags=precommit,integration ./...`, `golangci-lint` (or `staticcheck`), `go tool govulncheck` (pinned in `go.mod`)
- `make test-race` — skipped on Windows (`scripts/race-check.ps1`); on Linux/macOS runs `scripts/race-check.sh`. CI runs strict race on Ubuntu (`.github/workflows/qa.yml`).
- `make test-fuzz` — short native fuzz smoke over all release-gate fuzz targets (`FUZZTIME` per target, default `500ms`; see `docs/release-gates.md`). Optional committed seeds live under each package’s `testdata/fuzz/FuzzName/` using the `go test fuzz v1` file format ([testdata/fuzz/README.md](testdata/fuzz/README.md)).
- `make parity-checks` — same as `go test -parallel=8 -timeout=10m -tags=precommit,integration ./internal/testkit/conformance/...` (FE×BE matrix + parity suites; see `docs/conformance-matrix-evidence.md`); use before push when you touch cross-frontend/backend behavior.
- `make bench` — benchmark smoke across hot packages (see [`docs/performance-checks.md`](docs/performance-checks.md)). Optional CI uploads weekly/manual runs via `.github/workflows/benchmarks.yml` for offline `benchstat` comparison.
- `make hooks-install` — enable `.githooks/pre-commit` (`core.hooksPath=.githooks`; runs staged secret scan then quality gate when `.go` is staged)
- `go test -run TestName ./path/to/pkg`
- `go test -fuzz=FuzzName$ -fuzztime=30s -run=^$ ./path/to/pkg` — suffix `$` matches one fuzz function when a package defines several `Fuzz*` targets
- `go run ./cmd/lipstd --config ./config/config.yaml`

CI runs [`.github/workflows/qa.yml`](.github/workflows/qa.yml): `make quality-checks`, `go test -parallel=8 -tags=precommit,integration ./...` (includes `internal/testkit/conformance/...` and optional-Postgres tests, which skip unless `LIP_TEST_POSTGRES_DSN` or `LIP_MANAGED_POSTGRES_DSN` is set; no separate parity step), release-gate fuzz smoke (`make test-fuzz` with `FUZZTIME=6s`), strict Linux race (`scripts/race-check.sh --strict`), `golangci-lint` v2, and `go tool govulncheck`.

### Go build and test caching

Go keeps compiled packages and test binaries in the **build cache** (`GOCACHE`, see `go env GOCACHE`); the **module download cache** is `GOMODCACHE` (`go env GOMODCACHE`). Nothing in this repo disables those defaults. GitHub Actions uses `actions/setup-go` with `cache: true` and `cache-dependency-path: go.sum` so CI restores module and build cache between runs. Race builds (`go test -race`) use separate cache entries from non-race builds. `scripts/race-check.sh` passes `-count=1` so race runs always execute tests; `make test-fast` / `test-staged.*` do not, so unchanged packages can report `(cached)` on repeat runs.

### Go modules and Bun upgrades

- Prefer merging Dependabot **patch** groups for routine bumps (see [`.github/dependabot.yml`](.github/dependabot.yml)).
- When upgrading **`github.com/uptrace/bun`**, bump every direct `github.com/uptrace/bun/...` module in `go.mod` to the **same version** in one change (`bun`, `dialect/pgdialect`, `dialect/sqlitedialect`, `driver/pgdriver` today), then `go mod tidy`.

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
- Do not add `go` in request handlers, frontend encoders, or other per-call hot paths; prefer long-lived workers and stream-scoped pumps (see `stream.Keepalive` and quality gate `scripts/check-adhoc-goroutines.*`). If you must introduce a new root goroutine outside tests, extend the allowlist there with a short justification in the PR.

### Error handling

- Return errors, do not panic in request paths.
- Wrap errors with `%w` and preserve classification metadata.
- Frontends are responsible for mapping internal errors to protocol-specific error shapes.
- Recoverable pre-output failures must carry enough metadata for routing and diagnostics.

### Configuration

- Keep config structs typed and explicit.
- Do not allow plugin config to leak into core config structs.
- Core passes plugin-specific raw config blobs into plugin factories.
- Continuity/B2BUA persistence is core-configured: sample [`config/config.yaml`](config/config.yaml) defaults to in-memory `continuity.store`; optional SQLite uses `internal/core/continuity/sqlitestore/` (driver registration lives there, not only in `main`).
- Optional observability: Prometheus metrics and OpenTelemetry tracing are gated under config (see commented `observability:` block in the sample config); wiring lives under `internal/infra/metrics` and `internal/infra/tracing`.

## Testing standards

Recoverability of behavior is defined by a **specification bundle** — tests, `testdata/` fixtures, stable `pkg/lipapi` / `pkg/lipsdk` contracts, and steering or parity specs — not by `_test.go` files alone. See [`.kiro/steering/testing.md`](.kiro/steering/testing.md).

### Coverage discipline (risk-based, no line-percent goals)

- Prefer locking **invariants** (routing, streaming, B2BUA, capability mismatch, no-retry-after-output) over broad coverage; see [`docs/testing-coverage-priorities.md`](docs/testing-coverage-priorities.md) for hotspot triage and gap hypotheses.
- When changing **cross-protocol** or FE×BE matrix surfaces, run **`make parity-checks`** locally before merge (integration-tagged conformance under `internal/testkit/conformance`).
- For release-grade or wide behavioral merges, prefer **`make qa`** so precommit matrices and optional integration tags run once.

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

## Go style (project conventions)

- **Line length ~120+**: break at semantic boundaries where practical. When splitting embedded JSON/SSE
  in tests or emulators, re-check brace matching; a single long line is better than a broken stream.
  Prefer [testdata/](./testdata/) for very large wire dumps when a fixture is easier to read than a megastring.
  (CI does not yet run `golangci-lint` `lll` repo-wide; enable and tune path excludes incrementally when ready.)
  **External style reviews**: treat the JSON/SSE single-line exception above as intentional for those literals so
  mechanical ~120-character wrapping does not break fragile streams.
- **Slices in JSON and returned values**: prefer explicit empty initialization (`s := []T{}` or `make`) when
  a slice is stored, returned, or serialized, so `null` never appears in JSON for “empty list”. Short-lived
  **append-only** local buffers may use `var s []T` and `append` (idiomatic Go) when the value never escapes.
- **JSON presence vs null**: when a wire shape must preserve “field absent” vs explicit `null` vs empty containers, reuse `internal/core/jsonpresence` patterns instead of one-off nullable types at every call site.
- **SQL driver import**: the SQLite driver is side-effect-registered in
  [internal/core/continuity/sqlitestore/](./internal/core/continuity/sqlitestore/) (not only in `main`);
  see the package and import comments there.

## Git and editing rules

- Never use destructive git commands to wipe broad unreviewed changes.
- Revert only the exact files or hunks you intend to revert.
- Preserve user-authored changes unless explicitly asked to replace them.

## Reporting back to the user

- Never claim success unless you verified it.
- Be precise about what was tested and what was not.
- If you made an architectural trade-off, say what it was and why.
- If something is uncertain, say so plainly.
