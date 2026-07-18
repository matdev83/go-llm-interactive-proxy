---
type: reference
title: Testing Strategy
description: Test philosophy, suite topology, build tags, commands, and high-value test targets.
stack: [go]
tags: [testing, tdd, conformance, qa]
status: active
---

# Testing Strategy

## Philosophy

Tests are executable contracts. Goal: make behavior, boundaries, and regressions explicit. Not maximum count.

## Specification Bundle (Recoverability)

1. **Tests** - executable behavior, invariants, regressions
2. **Committed fixtures** - `testdata/` goldens, migration JSON, canonical event streams
3. **Stable contracts** - `pkg/lipapi`, `pkg/lipsdk`
4. **Steering & parity specs** - `.kiro/steering/`, `.kiro/specs/`
5. **Scenario registry** - `docs/spec-bundle-index.md` with SB- IDs and doc/test cross-checks

## Suite Topology

### Unit Tests
Package-local table-driven tests with named subtests. Use `t.Parallel()` when safe. Prefer `Example...` for stable contracts.

### Integration Tests
Composed tests with `httptest` + stub plugins/providers. Fast, deterministic, no real network. No `//go:build integration` tag on `integration_test.go` files - they run in default `go test ./...`.

### Conformance & Golden Tests
`testdata/` fixtures for canonical event streams, protocol payloads, selector parsing, capability errors, no-retry behavior.

### Race & Fuzz
Race detector required for stream pumps, cancellation-sensitive components, stores with shared state. Fuzz for parsers, decoders, selectors, protocol payload normalization.

## Build Tags

| Tag | Purpose | Default Suite |
|---|---|---|
| `precommit` | Hygiene checks + executor matrices | No (`make test-precommit-extra`) |
| `integration` | Env-gated PostgreSQL tests | No (requires `LIP_TEST_POSTGRES_DSN`) |
| (no tag) | All fast, deterministic tests | Yes |

## Make Targets

```
make quality-checks   # fmt, tidy, build, vet, archtest
make test             # quality-checks + unit + parity
make test-unit        # go test -parallel=8 -timeout=10m ./...
make parity-checks    # conformance (-tags=precommit,integration)
make test-fuzz        # short fuzz smoke (nightly CI FUZZTIME=6s)
make test-race        # race detector (Linux nightly CI; skipped on Windows)
make test-precommit-extra  # hygiene + executor matrices
make qa               # quality + full test + lint + vuln
make bench            # benchmark smoke
```

## Architecture Guardrail Tests

`internal/archtest/` enforces architectural invariants via AST inspection and dependency analysis:

- **Line complexity budgets** — tree-level and critical-file budgets prevent re-bloating gravity wells.
- **Hexagonal migration baseline** — requires zero `exception` entries; all packages `aligned` or retired.
- **Composition-root rules** — no `sync.Once` + standard-bundle install, no package-level registries, no `pluginreg.Default` in runtimebundle.
- **Executor construction invariant** — `TestExecutorConstructionNoPostConstructionMutation` ensures no post-construction field mutations on `runtime.Executor` after `NewExecutor`; all fields must pass through `ExecutorConfig`.
- **Policy diagnostics isolation** — `PolicyDiagnosticsEnabled` must not be referenced from frontend/stdhttp.
- **Core boundary rules** — core must not import concrete plugins, vendor SDKs, or stdhttp.
- **Public contract isolation** — `pkg/lipapi` and `pkg/lipsdk` must not depend on internal packages or vendor SDKs.

## High-Value Test Targets

- Canonical request/event translation
- Frontend/backend matrix compatibility
- Routing selector syntax, weights, parallel races, TTFT, circuit breaker
- Pre-output failure swallowing
- B2BUA A-leg continuity + B-leg lineage
- Secure-session BeginTurn, resume denial, redaction
- Stream cancellation, keepalive, panic isolation
- Extension stage ordering, immutable snapshots
- Executor construction invariant (no post-construction mutation)
