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

**Build tags:** This repo does **not** use `//go:build integration` on `integration_test.go` files. Those files are **fast, deterministic** composed tests (`httptest` + stub executor/backends, no real provider network). They belong in the default `go test ./...` / `make test` suite so every PR exercises decode/handler/refclient wiring. If we add tests that hit real networks, long-lived containers, or shared external state, gate them with `//go:build integration` **or** `testing.Short()` skips and run them in a separate CI job.

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

## What to avoid

- brittle assertions tied to logging text or call counts only,
- tests that only prove mocks were invoked,
- protocol tests that ignore streaming order and termination,
- broad end-to-end tests with poor failure localization when a smaller contract test would suffice.

---
_Initial Go steering version: 2026-04-20_
_Reason: define the executable-spec culture for the Go rewrite and protect the streaming/routing boundaries that matter most._
