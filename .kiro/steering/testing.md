# Testing and TDD (Steering)

## Core Testing Invariants

- **TDD by Default**: Red -> Green -> Refactor.
- **Specification Bundle (Recoverability)**: Executable tests + `testdata/` golden fixtures + canonical types (`pkg/lipapi`, `pkg/lipsdk`) + steering rules + scenario index ([`docs/spec-bundle-index.md`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/docs/spec-bundle-index.md)).
- **Composed Tests in Default Suite**: Untagged `*_test.go` files inside package dirs use `httptest` + stubs without external networks. They run in default `go test ./...` and `make test`. Tests marked with `//go:build integration` are environment-gated.
- **`goleak.VerifyTestMain`**: Mandatory in packages managing goroutines ([`runtime`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime), `stream`, `pluginreg`, `standardplugins`, `bedrock`, `stdhttp`, `connectors/*`).

---

## Build Tag & Environment Gating Rules

- **Default `go test ./...` / `make test`**: Fast, deterministic, composed tests only (no real network or database required).
- **`//go:build integration`**: Env-gated tests requiring real services (e.g. PostgreSQL via `LIP_TEST_POSTGRES_DSN`). Skip automatically when env var is unset.
- **`//go:build precommit`**: Non-blocking checks (hygiene in `internal/qa`, regression matrices in `internal/core/runtime`, reasoning HTTP matrix in `internal/stdhttp`). Executed in `make qa` / CI (`-tags=precommit,integration`).
- **PostgreSQL Pooler Gate**: Dual-plane Postgres tests require `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1` attestation; direct DSN is `LIP_TEST_POSTGRES_ADMIN_DSN`.

---

## High-Value Test Targets

- **Canonical Translation**: `pkg/lipapi` request/event decoding/encoding, dialect preservation, name-based tool classification (`ClassifyToolName` / `ToolEvent` correlation).
- **Contract TCKs**: Frontend, backend-family, and canonical-core kits under [`internal/testkit/contract`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/testkit/contract). Cartesian FE×BE completeness is retired; a bounded sentinel plus pinned historical inventory replace `AllCells()`.
- **OpenResponses API**: HTTP POST/SSE and WebSocket turn/continuation pipelines, allowed-tool filters ([`internal/plugins/frontends/openresponses`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openresponses)).
- **Authority & Stage Coordination**: Execution stage budgets, settle failures, provider isolation ([`internal/core/authoritycoord`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/authoritycoord)).
- **Control Plane Projections**: Ledger projections, metering bridges, readiness reports ([`internal/core/controlplane`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/controlplane)).
- **Usage-Record Billing**: Authorize/hold, TUR/LUR seal, post-turn rating/journal, catalog miss fail-closed, `ComposeBilling` injection ([`internal/core/billing`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/billing), `internal/infra/billingstore`). Dual-dialect Bun parity where the environment has Postgres.
- **Interleaved Reasoning**: Reasoning memo stores, shape sanitization, Codex native compaction ([`internal/core/interleavedthinking`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/interleavedthinking), `codexclientcompat`).
- **Durable Stores**: Dual-dialect Bun SQLite/PostgreSQL stores ([`internal/core/continuity/bunstore`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/continuity/bunstore), `securesession/adapters`), including A-leg route-override rows.
- **Routing & Resilience**: Selector parsing, model aliases, weighted groups, parallel races, TTFT budgets, `[first]`/`[thinker]`, pre-output failover swallowing, runtime A-leg routing overrides (in-flight isolation, generation reload).
- **Secure Sessions**: Authority validation, BeginTurn, resume denial, diagnostics redaction.

---

## Mocking & Boundary Rules

- Prefer `httptest.Server` and small stubs over mock frameworks.
- NEVER mock internal call graphs.
- Use fake clocks/stores/IDs for time or randomness.
- Real canonical types (`pkg/lipapi`) MUST be used in tests. Hide vendor SDK types behind adapter edges.
- Every bug fix MUST include a minimal regression test or fixture.

---

## Command Reference

- `make quality-checks` — Format, tidy, vet, ad-hoc goroutine allowlist, hot-path regex check, archtest guardrails.
- `make test` — Quality checks + default unit tests + parity checks.
- `make test-unit` — `go test -parallel=8 -timeout=10m ./...`
- `go test ./internal/archtest/...` — Architecture guardrail tests.
- `make parity-checks` — Conformance suite (`-tags=precommit,integration`).
- `make qa` — Quality checks + full tagged test pass + golangci-lint + govulncheck.
- Extension scalability evidence: `go test ./internal/archtest/...`, `go test ./internal/providerprofiles/...`, `go test ./internal/testkit/contract/...`, `go test ./pkg/lipsdk/backendplugin/contracttest`, and the bounded sentinel through `make parity-checks`. Use `go run ./internal/archtest/tools/changesurface/cmd -json` for the deterministic Git path report; provider-profile-only changes must also pass `make profile-only-check PROFILE_ONLY_BASE=HEAD` (CI runs the same ratchet when profile paths change).
