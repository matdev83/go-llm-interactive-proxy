# Testing and TDD (Steering)

## Core Testing Invariants

- **TDD by Default**: Red -> Green -> Refactor.
- **Specification Bundle (Recoverability)**: Executable tests + `testdata/` golden fixtures + canonical types (`pkg/lipapi`, `pkg/lipsdk`) + steering rules + scenario index ([`docs/spec-bundle-index.md`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/docs/spec-bundle-index.md)).
- **Composed Tests in Default Suite**: Untagged `*_test.go` files inside package dirs use `httptest` + stubs without external networks. They run in default `go test ./...` and `make test`. Tests marked with `//go:build integration` are environment-gated.
- **`goleak.VerifyTestMain`**: Mandatory in packages managing goroutines ([`runtime`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime), `stream`, `pluginreg`, `standardplugins`, `bedrock`, `stdhttp`, `connectors/*`).

---

## Build Tag & Environment Gating Rules

- **Default `make test-unit` vs `make test`**:
  - `make test-unit` runs `go test $(GO_TEST_FLAGS) ./...` (fast in-memory and composed unit tests using `httptest` and stubs, without external network or database dependencies; excludes `//go:build precommit` and `//go:build integration`). Parallelism defaults to machine core count and is configurable via `GO_TEST_FLAGS` (or `LIP_TEST_PARALLEL`).
  - `make test` composes `quality-checks-fast`, `test-unit`, and `parity-checks` for comprehensive local verification.
  - `make parity-checks` encompasses the full parity scope: contract TCKs (`internal/testkit/contract`, `internal/providerprofiles`, `pkg/lipsdk/backendplugin/contracttest`, `internal/testkit/compatibleparity`), protocol conformance matrices (`internal/testkit/conformance` with `-tags=precommit,integration`), external connector parity (ACP, OpenRouter, hosted compatible), and the bounded sentinel (`TestBoundedSentinel`).
  - `make qa` executes `quality-checks-fast`, the full tagged test pass `qa-tests` (`-tags=precommit,integration`), static analyzers (`lint` with `golangci-lint` preferred and `staticcheck` fallback; `govulncheck` via `vuln`), and static release gates (`backend-plugin-release-gates-static`, `test-openresponses-compliance-static`). It verifies code and architecture without requiring external database services (unconfigured external DB integration tests skip).
- **Database Dialect Parity Gate**: Canonical repository-wide parity runner derived dynamically from package `internal/testkit/dbparity` via `dbparity.DefaultCatalog()` (8 component families). Executed via `make test-db-parity` (or `make test-db-parity-sqlite`, `make test-db-parity-postgres-direct`). Mandatory direct PostgreSQL mode requires `LIP_REQUIRE_POSTGRES=1` with direct DSN `LIP_TEST_POSTGRES_DSN` (runner accepts fallback to `LIP_TEST_POSTGRES_ADMIN_DSN`) and fails closed on missing/unhealthy service, redacting credentials.
- **`//go:build integration`**: Env-gated tests requiring real services (e.g. PostgreSQL via `LIP_TEST_POSTGRES_DSN`). In ad-hoc unit runs, tests skip automatically if env vars are unset; in mandatory parity / authority gates, missing configuration fails closed.
- **`//go:build precommit`**: Non-blocking checks (hygiene in `internal/qa`, regression matrices in `internal/core/runtime`, reasoning HTTP matrix in `internal/stdhttp`). Executed in `make qa` / CI (`-tags=precommit,integration`).
- **Specialized PostgreSQL Topology Gates**:
  - `make test-authority-postgres-direct`: Direct PostgreSQL runtime proof for authority/lease/journal/workstore (`LIP_REQUIRE_POSTGRES=1`). Direct DSN in `LIP_TEST_POSTGRES_DSN` (or admin DSN).
  - `make test-authority-postgres-pooled`: Transaction-pooled runtime proof requiring runtime pooler DSN `LIP_TEST_POSTGRES_DSN`, admin DSN `LIP_TEST_POSTGRES_ADMIN_DSN`, and explicit topology attestation `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1` (Make sets `LIP_REQUIRE_POSTGRES_POOLER=1`).
  - `make test-postgres-migrations`: Applies and verifies dual-plane PostgreSQL migrations using `LIP_MIGRATION_POSTGRES_DSN` (with fallback to admin/runtime DSNs).
  - `make test-authority-postgres`: Aggregate direct + pooled proof + migrations (`LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1` required).
  - Billing convergence (`make billing-convergence-certify`): Deep domain financial invariants and schema verification.
- **PR CI & Hygiene Integration**:
  - CI job `db-parity` in `.github/workflows/ci.yml` starts an ephemeral direct PostgreSQL container (`postgres:17-alpine`), runs canonical `make test-db-parity` for test-relevant changes, emits an explicit bypass for changes classified as non-test-relevant by `scripts/ci-scope.sh`, and feeds into the required `repo-hygiene` aggregate status check (`if: always() && needs.db-parity.result != 'success'`).
  - PR QA in `.github/workflows/qa.yml` runs fast preflight, hygiene, provider profile ratchet, vet, and architecture guardrails; it does not run full tagged integration or postgres authority suites.

---

## High-Value Test Targets

- **Canonical Translation**: `pkg/lipapi` request/event decoding/encoding, dialect preservation, name-based tool classification (`ClassifyToolName` / `ToolEvent` correlation).
- **Contract TCKs**: Frontend, backend-family, and canonical-core kits under [`internal/testkit/contract`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/testkit/contract). Cartesian FE×BE completeness is retired; a bounded sentinel plus pinned historical inventory replace `AllCells()`.
- **OpenResponses API**: HTTP POST/SSE and WebSocket turn/continuation pipelines, allowed-tool filters ([`internal/plugins/frontends/openresponses`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openresponses)).
- **Authority & Stage Coordination**: Execution stage budgets, settle failures, provider isolation ([`internal/core/authoritycoord`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/authoritycoord)).
- **Control Plane Projections**: Ledger projections, metering bridges, readiness reports ([`internal/core/controlplane`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/controlplane)).
- **Usage-Record Billing**: cheap credit screen, quote/exposure admission, immutable BillingCallID-scoped leg/call records, post-usage customer settlement, independent provider-cost posting, catalog miss fail-closed, and `ComposeBilling` injection ([`internal/core/billing`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/billing), [`internal/infra/billingstore`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/infra/billingstore)). Dual-dialect Bun parity is exercised when PostgreSQL is configured; integration tests skip unless the configured DSN is available.
- **Interleaved Reasoning**: Reasoning memo stores, shape sanitization, Codex native compaction ([`internal/core/interleavedthinking`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/interleavedthinking), `codexclientcompat`).
- **Durable Stores**: Dual-dialect Bun SQLite/PostgreSQL stores ([`internal/core/continuity/bunstore`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/continuity/bunstore), `securesession/adapters`), including A-leg route-override rows.
- **Routing & Resilience**: Selector parsing, model aliases, weighted groups, parallel races, TTFT budgets, `[first]`/`[thinker]`, pre-output failover swallowing, runtime A-leg routing overrides (in-flight isolation, generation reload).
- **Attempt Lifecycle & Cancellation**: TerminalizeAttempt at-most-once convergence, ReadyAttempt-gated publication, A-leg cancellation vs B-leg activation races, terminal evidence draining ([`internal/core/runtime`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime), [`internal/core/leglifecycle`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/leglifecycle)).
- **Terminal Decisions**: Shared chokepoint single-flight and authoritative pass-through, bounded evidence projection, policy admission snapshots ([`internal/core/runtime`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime), [`internal/plugins/features/agentloopguard`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/features/agentloopguard)).
- **Conversation View & Local Turns**: Anchor/tag-before-release races, `never_backend` exclusion classification, frozen-view reassertion before backend open, no inference fallback after local-turn failure ([`internal/core/conversationview`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/conversationview)).
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
- `make test` — Quality checks (`quality-checks-fast`) + default unit tests (`test-unit`) + parity checks (`parity-checks`).
- `make test-unit` — `go test $(GO_TEST_FLAGS) ./...` (fast in-memory/composed unit tests; configurable via `GO_TEST_FLAGS` or `LIP_TEST_PARALLEL`).
- `make test-db-parity` — Sequential repository-wide SQLite and direct PostgreSQL parity gate (derived from `internal/testkit/dbparity` via `dbparity.DefaultCatalog()` across all 8 components).
- `make test-db-parity-sqlite` — Canonical SQLite database parity tests across all registered components.
- `make test-db-parity-postgres-direct` — Repository-wide fail-closed direct PostgreSQL parity (direct/admin DSN; Make sets `LIP_REQUIRE_POSTGRES=1`).
- `make test-authority-postgres-direct` — Direct PostgreSQL runtime proof for authority/lease/journal/workstore (`LIP_REQUIRE_POSTGRES=1`).
- `make test-authority-postgres-pooled` — Transaction-pooled runtime proof (requires runtime pooler DSN `LIP_TEST_POSTGRES_DSN`, admin DSN `LIP_TEST_POSTGRES_ADMIN_DSN`, and `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`).
- `make test-authority-postgres` — Aggregate direct + pooled proof + migrations (pooled attestation required).
- `make test-postgres-migrations` — Apply and verify dual-plane PostgreSQL migrations.
- `go test ./internal/archtest/...` — Architecture guardrail tests (including database parity discovery).
- `make parity-checks` — Full parity matrix: contract TCKs, protocol conformance (`-tags=precommit,integration`), connector parity suites, and bounded sentinel (configurable via `GO_TEST_FLAGS`).
- `make qa` — Quality checks (`quality-checks-fast`) + full tagged test pass (`-tags=precommit,integration`) + linter (`make lint`: `golangci-lint` preferred, `staticcheck` fallback) + `govulncheck` (`make vuln`) + static release gates (`backend-plugin-release-gates-static`, `test-openresponses-compliance-static`).
- Extension scalability evidence: `go test ./internal/archtest/...`, `go test ./internal/providerprofiles/...`, `go test ./internal/testkit/contract/...`, `go test ./pkg/lipsdk/backendplugin/contracttest`, and the bounded sentinel through `make parity-checks`. Use `go run ./internal/archtest/tools/changesurface/cmd -json` for the deterministic Git path report; provider-profile-only changes must also pass `make profile-only-check PROFILE_ONLY_BASE=HEAD` (CI runs the same ratchet when profile paths change).
