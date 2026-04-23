# Testing and TDD (Steering)

## Testing philosophy

This project treats tests as executable contracts.
The goal is not maximum test count. The goal is to make behavior, boundaries, and regressions explicit.

Core testing principles:
- TDD by default: red -> green -> refactor.
- Contract-first assertions: test observable behavior and protocol shape.
- Deterministic by default: avoid flaky timing and real network dependencies.
- Boundary-aware verification: prove that core, plugins, and hooks interact through contracts.
- Streaming is a first-class test target, not an afterthought.

## Suite topology

### Unit tests

Use package-local tests for:
- selector parsing,
- capability negotiation,
- canonical model validation,
- stream collectors,
- continuity stores and allocators,
- hook dispatch and ordering.

### Integration tests

**Alignment with generic golang-testing guidance:** Community materials often require `//go:build integration` on any file named `integration_test.go`. This repository **intentionally diverges** for the current `**/integration_test.go` set: they are in-process wiring tests only (see below). Prefer named `t.Run` subtests and `t.Parallel()` where safe; use `goleak.VerifyTestMain` in packages that spin goroutines in tests (see `internal/core/runtime`, `internal/core/stream`, `internal/pluginreg`, `internal/pluginreg/standardbundle`, `internal/plugins/backends/acp`, `internal/plugins/backends/bedrock`, `internal/stdhttp` (HTTP server and client wiring in tests); `pluginreg` and `standardbundle` ignore the OpenCensus stats worker started from a dependency `init`).

**Build tags:** This repo does **not** use `//go:build integration` on `integration_test.go` files. Those files are **fast, deterministic** composed tests (`httptest` + stub executor/backends, no real provider network). They belong in the default `go test ./...` / `make test` suite so every PR exercises decode/handler/refclient wiring. If we add tests that hit real networks, long-lived containers, or shared external state, gate them with `//go:build integration` **or** `testing.Short()` skips and run them in a separate CI job.

**`precommit` tag:** A small set of non-blocking checks (repo root hygiene under `internal/qa/`, and large executor regression matrices under `internal/core/runtime/`) use `//go:build precommit`. Default `make test` / `go test ./...` omits them; `make test-precommit-extra`, the git pre-commit quality gate, `make qa`, and CI unit tests run `go test -tags=precommit` so pushes still exercise them.

**`testing.Short` and `-short`:** The default `GO_TEST_FLAGS` in the Makefile and the CI unit-test step do **not** pass `go test -short`, and no test uses `if testing.Short() { t.Skip(...) }` today. The full default suite is fast enough for every PR. If you add tests that are intentionally slow or need external services, gate them with `testing.Short()` and document a second command (or restore `-short` on the relevant `go test` line) so `go test -short` skips the expensive work while the full suite still runs the rest.

Use composed tests with `httptest` and stub plugins/providers for:
- frontend decode -> core -> backend -> frontend encode flows,
- cancellation and timeout behavior,
- weighted routing and failover decisions,
- admin and diagnostics endpoints,
- B2BUA continuity behavior across multiple attempts.

### Conformance and golden tests

Use `testdata/` fixtures for:
- canonical event streams,
- protocol request/response payloads,
- selector parsing examples,
- capability mismatch errors,
- no-retry-after-first-output behavior.

Golden fixtures are especially important for cross-protocol translation and stream encoding.

### Race and fuzz tests

Required where practical for:
- stream pumps,
- cancellation-sensitive components,
- stores with shared mutable state,
- parsers and decoders,
- selector syntax and protocol payload normalization.

## High-value test targets in this codebase

Always prioritize tests for:
- canonical request and canonical event translation,
- frontend/backend matrix compatibility on the shared subset,
- routing selector syntax and weighted behavior,
- recoverable pre-output failure swallowing,
- failover attempt budgets,
- B2BUA A-leg continuity and B-leg attempt lineage,
- stream cancellation and keepalive behavior,
- plugin isolation boundaries,
- request and response hook ordering.

## Mocking and boundary guidance

- Prefer `httptest.Server` and small stubs over deep mocks.
- Avoid mocking internal call graphs.
- Fake clocks, stores, and id generators are encouraged when time or randomness matters.
- Use real canonical types in tests whenever possible.
- Provider SDKs should usually be hidden behind backend adapter seams in tests.
- Do not introduce interfaces only to satisfy mocks; prefer small fakes around real consumer-owned seams.
- Architecture tests should enforce ownership and dependency direction, not naming symmetry or textbook package taxonomy.

## Regression policy

Every bug fix in routing, translation, streaming, or continuity handling must add a regression test.
If a production issue is diagnosed from a captured interaction, distill it into a minimal fixture or reproducer.

Migration note:
- existing Python LIP captures can be mined into Go `testdata/` fixtures for parity checks,
- but the Go tests should assert the new canonical contracts, not Python internals.

## Canonical commands

Default commands:
- `go test ./...`
- `go test -race ./...`
- `go test -run TestName ./path/to/pkg`
- `go test -fuzz=Fuzz -run=^$ ./path/to/pkg`
- `go vet ./...`
- `staticcheck ./...`

Architecture and hygiene commands that should remain easy to run:
- `go test ./internal/archtest/...`
- `go test -tags=precommit ./internal/qa/... ./internal/core/runtime/...`

Performance smoke (not part of default PR gates unless you opt in): `make bench` runs benchmarks across core, stream, routing, diag, testkit, and frontend encoder packages; see `docs/performance-checks.md`. CI may upload weekly `make bench` output via `.github/workflows/benchmarks.yml` for manual `benchstat` comparison.

## What to avoid

- brittle assertions tied to logging text or call counts only,
- tests that only prove mocks were invoked,
- protocol tests that ignore streaming order and termination,
- architecture tests that fail because a concrete inbound service is used instead of an interface,
- architecture tests that force generic `ports` or `services` packages,
- broad end-to-end tests with poor failure localization when a smaller contract test would suffice.

---
_Initial Go steering version: 2026-04-20_
_Updated 2026-04-23: architecture-test guidance for pragmatic hexagonal enforcement and boundary-focused fakes._
_Reason: keep testing guidance aligned with the current small-core, ownership-first architecture direction._
