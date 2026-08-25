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

## Respect git workflow: No work on main

Keep `main` branch clean. It should be only a PR merge receiver, never a merge donor.

- Before changing files, check the current worktree and branch. If already inside a non-`main` worktree dedicated to the user's task, work there; do not create another worktree.
- Otherwise, create a sibling worktree from `main` with an adequately named `fix/`, `spec/`, or `feat/` branch and work there, never on `main`.
- Run `codegraph init` from the root of every newly created worktree before implementation. When reusing a dedicated worktree, run it only if `.codegraph/` is absent.

The source-change gate limits a commit or PR to **100 modified `*.go` files** (100 changed files gate on Go sources). Skill, catalog, and documentation paths do not consume this gate. Split large Go refactors so they stay reviewable and mergeable. Pre-commit, the recommended pre-push hook, and PR CI apply the same limit to staged paths or the branch vs its merge base. Default `go test ./internal/qa` also fails when the worktree has more than 100 dirty `*.go` files (no override). Admin override for hooks/CI only: `LIP_ALLOW_LARGE_CHANGE=1` for one command, `git config lip.allowLargeChange true` locally, or the `allow-large-change` PR label in CI. Do not use `--no-verify` to skip this check; that also skips secret scanning.

## Skill Loading

- Architecture/package boundary/feature design/constructors/DI: `golang-architecture`.
- Tests/conformance/regressions/benchmarks/testify: `golang-testing`.
- Streaming/concurrency/cancellation/goroutines: `golang-concurrency`.
- Code quality/formatting/naming/safety/lint/simplification: `golang-code-quality`.
- Performance/profiling/diagnostics/observability/troubleshooting: `golang-performance-diagnostics`.
- Error classification/wrapping/mapping/panic recovery: `golang-error-handling`.
- Modern Go/generics/iterators/data structures/Swiss Tables: `golang-data-modernize`.
- CLI/gRPC/database adapters/pools: `golang-services-adapters`.
- Ecosystem libraries/samber toolkit/Go modules: `golang-ecosystem-libraries`.
- CI workflows/matrices/godoc/ADRs: `golang-ci-docs`.
- Strict maintainability & SOLID audit: `golang-code-audit`.
- Security/injection/cryptography/secret redaction: `golang-security`.
- Architecture, call paths, implementations, dependency direction, or blast radius: `codegraph`.
- PR submission, sequential merge delivery, CI babysitting, merged-main verification, or worktree cleanup: `lip-pr-delivery`.
- Repo steering overrides generic skill defaults.

### Canonical Skill Catalog

- `.agents/skills/` is the single tracked catalog for Codex, OpenCode, Pi, Antigravity CLI, Cursor, and Codebuff. Git worktrees receive the complete catalog automatically.
- `.agents/catalog.json` is the curated inventory. Add or update a skill there in the same change as its directory.
- Do not create same-name copies under `.codex/skills`, `.cursor/skills`, `.kiro/skills`, `.opencode/skills`, or `.pi/skills`; duplicate discovery has agent-specific precedence and causes drift.
- Keep portable frontmatter to `name`, `description`, optional `license`, and optional `metadata`. The folder name and frontmatter `name` must match.
- Run `pwsh -NoProfile -File scripts/check-agent-skill-catalog.ps1` after catalog changes.

## Architecture Guardrails

- Core owns orchestration, routing (including A-leg routing overrides), failover, and B2BUA continuity.
- Provider semantics stay in adapters/plugins.
- Core must not import provider SDKs or concrete plugins.
- No pairwise protocol translators; use protocol <-> canonical adapters only.
- Streaming is primary; non-streaming collects the canonical stream.
- No transparent retry/failover after first downstream content event.
- Capability mismatches fail explicitly; never silently drop required semantics.
- Request/response mutation belongs behind hooks/extensions, not core branching.
- Terminal decisions flow through one core chokepoint with a single exclusive provider slot; core never imports or branches on concrete providers (e.g., Agent Loop Guard); removal restores generic no-provider behavior.
- Use explicit construction/registration; no DI containers, reflection registries, globals, or Go native `plugin` in v1.
- Hybrid backends ([ADR 0008](docs/adr/0008-hybrid-backend-connector-plugins.md)): essential builtins are static; optional backends are executable gRPC connectors under `connectors/` (manifest-driven discovery). Do not add optional connectors to essential/`standard_table` fixed tables. Core keeps orchestration/B2BUA.
- Compatible-provider growth is data-driven (`internal/providerprofiles`) plus contract TCKs, not a Cartesian frontend×backend product or a new in-process backend per vendor.
- Runtime billing has two seams only: cheap credit screen before route expansion, then atomic operational exposure admission after quote; terminal ownership appends BillingCallID-scoped usage. No stream-time rating, journal I/O, or token-ledger money writes.
- Public `pkg/lipruntime.Options` stays non-money. Internal hosts inject billing through `runtimebundle.ComposeBilling` into `BuildHost` `ProductionOptions`. Stock `lipstd` does not invent billing accounts.

## Package Zones

- `pkg/lipapi/`: canonical request/event/capability/error contracts, including name-based tool classification.
- `pkg/lipsdk/`: plugin SDK, facades, registration contracts (including `backendplugin` ABI).
- `pkg/lipruntime/`: public host/reload facade. `Options` stays non-money.
- `internal/core/`: runtime orchestration, routing, continuity, streams, hooks/extensions, config, diagnostics.
- `internal/core/routeoverride/`: A-leg latest-wins routing-override state and ports.
- `internal/core/billing/`: BillingCallID, quote/exposure policy, immutable usage contracts, post-usage rating, journal commands (no SQL, no provider SDKs).
- `internal/plugins/frontends/`: OpenResponses, OpenAI Responses, OpenAI legacy, Anthropic, Gemini frontends.
- `internal/plugins/backends/`: essential hosted/custom-compatible adapters and shared helpers only; essential kinds in `internal/standardplugins` (`EssentialBackendBundle` / tables). Optional connectors are not root-module packages.
- `connectors/`, `connector-support/`: independent modules for optional executable backend plugins and shared connector support; host-side connector runtime in `internal/infra/backendplugins/`.
- `internal/plugins/features/`: official feature and reference plugins.
- `internal/pluginreg/`, `internal/standardplugins/`, `internal/infra/runtimebundle/`, `internal/stdhttp/`: standard distribution composition and discovery registration.
- `internal/infra/billingstore/`, `internal/infra/billingcompose/`, `internal/infra/billingadmission/`: Bun journal, snapshot catalog, admission adapter.
- `internal/providerprofiles/`: declarative compatible-provider catalog compiled onto protocol-family adapters.
- `internal/refbackend/`, `internal/refclient/`, `internal/testkit/`: test-only emulators, reference clients, stubs, fixtures, contract TCKs (`internal/testkit/contract`).
- `internal/archtest/`, `internal/qa/`: architecture and hygiene gates.

## Kiro Specs

- Specs are opt-in only: `/kiro:*`, explicit `.kiro/specs/...`, or explicit spec-driven request.
- Use specs for new features, breaking changes, architecture changes, protocol/plugin contract changes, routing semantics, or unclear requirements.
- Direct-code small bug fixes, docs, narrow tests, and trivial maintenance.
- Spec flow: `spec-init` -> `requirements` -> `design` -> `tasks` -> `impl`.
- If an active spec is clearly in scope, do not code before approved `requirements.md` and `design.md` in `spec.json`.
- Active work lives in `.kiro/specs/{feature}/`. Completed and superseded specs move to `.kiro/specs/archive/` (`phase: completed` or `superseded`, `completed: true`, `ready_for_implementation: false`). Do not leave finished specs in the active tree.
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
