# Technology Stack (Steering)

## Core Stack Summary

- **Toolchain**: Go `1.26.6` pinned in `go.mod`.
- **HTTP**: `net/http` stdlib server and client primitives.
- **Logging**: `log/slog` with `samber/slog-*` multi-handler helpers.
- **JSON**: `encoding/json` stdlib. Presence semantics: [`internal/core/jsonpresence`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/jsonpresence). Size/shape preflight: [`internal/core/jsonshape`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/jsonshape) (8 MiB req / 256 KiB schema / 64 KiB args).
- **YAML Config**: `gopkg.in/yaml.v3`. Typed structs for core; raw subtrees to plugins.
- **Persistence**: `database/sql`, Bun ORM (`uptrace/bun` with `sqlitedialect` and `pgdialect`). SQLite: `modernc.org/sqlite`. Postgres: `pgdriver`/`pgx`.
- **Observability**: Prometheus metrics (`prometheus/client_golang`), OpenTelemetry OTLP HTTP tracing (`go.opentelemetry.io/otel`). Bounded cardinality ONLY — no raw prompts, secrets, or unbounded model IDs in metrics/log attributes.
- **Testing**: `testing`, `httptest`, `goleak` for goroutine checks.

---

## Runtime Composition Invariants

- **Zero Reflection/DI Containers**: Explicit constructors and composition roots (`cmd/lipstd`, `internal/infra/runtimebundle`).
- **No `init()` State**: Service setup, plugin registration, or runtime state in `init()` functions is **FORBIDDEN**.
- **Host Lifecycle**: `runtimebundle.BuildHost` manages immutable `GenerationRuntime` generations, reload coordination, and clean `Host.Close` shutdown.
- **Unified Frontend Pipeline**: ServeHTTP and SSE pumping unified behind [`internal/plugins/frontends/frontendpipe/`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe) and `stream.PumpSSE`.
- **Hybrid Backend Architecture (ADR 0008)**:
  - Essential backends: Statically linked into `cmd/lipstd`.
  - Optional backends: Executable gRPC IPC connectors under `connectors/` over versioned ABI (`pkg/lipsdk/backendplugin`). Never use Go native `plugin`.

---

## Provider SDK & Env Isolation

- **SDK Boundaries**: Provider Go SDKs (`openai-go`, `anthropic-sdk-go`, `genai`, `bedrockruntime`) MUST stay strictly inside backend plugin adapters. Never import vendor SDKs into `pkg/lipapi`, `pkg/lipsdk`, or `internal/core`.
- **Env Credentials**: Root `ResolveUpstreamAPIKeysFromEnv` supplies essential provider key pools only. Connectors consume plugin-local config/secrets.

---

## Concurrency & Context Standards

- `context.Context` MUST be the first parameter for external/I/O calls. Never store context in structs or substitute `context.Background()` mid-request.
- Goroutines MUST be explicitly owned with clean cancellation paths. Per-request ad-hoc goroutines are forbidden outside the allowed list.
- Never retry or failover after the first client-visible content event.

---

## Billing Persistence & Injection

- **Domain vs store**: `internal/core/billing` owns BillingCallID, quote/rating policy, immutable usage/exposure contracts, and financial journal commands. `internal/infra/billingstore` owns Bun SQLite/PostgreSQL mechanics. `internal/infra/billingcompose` owns the in-memory versioned snapshot catalog and identity mapping. `internal/infra/billingadmission` adapts cheap-screen plus operational-exposure admission.
- **Injection only**: Hosts open the durable store themselves, compose all four billing ports with `runtimebundle.ComposeBilling`, and pass `ProductionOptions` into `BuildHost`. Billing composition is all-or-none; partial ports fail before serving, and no YAML mode flag infers billing. Stock `lipstd` does not call `ComposeBilling`. Public `pkg/lipruntime.Options` stays non-money.
- **Catalog vs journal**: Snapshot **bodies** live in the process-local catalog (ID+Version). Exposure/call/usage records store immutable refs only. A missing referenced version fails closed at rating.
- **Billing separation**: Operational exposure is not settled money. Admission does not post a journal or mutate balance; customer settlement closes exposure after terminal usage, while provider COGS is an independent per-B-leg operation.
- **Sequence vs order**: the persisted positive `attempt_seq` (v2 fingerprint) is the authoritative B2BUA attempt order; `ExpectedBLegIDs` is a completeness set whose ordering has no financial meaning. Legacy rows keep `attempt_seq NULL` under the v1 contract and fail closed (`ErrBillingAttemptSequenceUnknown`) whenever order-based selection needs a sequence.
- **Snapshot independence**: customer rating resolves only customer pricing/policy/model cards; operator-rate lookup belongs solely to provider-cost resolution, so missing provider-cost data never blocks customer settlement or exposure close.
- **Call-scoped state**: runtime billing bookkeeping lives in one private `billingCallState` per `BillingCallID`/prepared request; the executor owns no lifetime-growing billing-call registry.
- **Leftover YAML**: `accounting.ledger.*` may parse but must not open. Production `accounting.authority` rejects monetary `budget` / `spend_cap` / `money_nano`.

---

## Database & PgBouncer Standards

- **Dual-Dialect Parity Invariant**: All 8 production persistence families (continuity, secure sessions, control-plane ledger, usage authority, concurrency authority, metering journal, terminal work, billing) share Bun driven adapters for SQLite and PostgreSQL.
- **Authoritative Parity Catalog**: Registered in package `internal/testkit/dbparity` via `dbparity.DefaultCatalog()` without runtime registry overhead. Enforces logical contract, schema invariant, and migration history equivalence across engines.
- **Fail-Closed Direct PostgreSQL**: Mandatory parity execution (`make test-db-parity` / `make test-db-parity-postgres-direct`) requires `LIP_REQUIRE_POSTGRES=1` with direct DSN `LIP_TEST_POSTGRES_DSN` (runner accepts fallback to `LIP_TEST_POSTGRES_ADMIN_DSN`) and fails closed on missing/unhealthy service with secret-safe output.
- **Dual Roles & Migration Alias**: Admin/migration connection (`LIP_TEST_POSTGRES_ADMIN_DSN` or migration alias `LIP_MIGRATION_POSTGRES_DSN`) vs runtime DML connection (`LIP_TEST_POSTGRES_DSN`).
- **Transaction Pooler Rules**: When running pooled gates (`make test-authority-postgres-pooled` with `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`, runtime pooler DSN `LIP_TEST_POSTGRES_DSN`, and admin DSN `LIP_TEST_POSTGRES_ADMIN_DSN`):
  - FORBIDDEN: `SET search_path`, session GUCs, temporary tables, SQL PREPARE/DEALLOCATE, advisory locks.
  - REJECT: `transaction_pool` + `auto_migrate` combination.
  - Shared bounded pools managed at runtimebundle composition root.
- **Topology & Specialized Gates**: Specialized direct/distributed authority (`test-authority-postgres-direct`), pooler (`test-authority-postgres-pooled`), migration (`test-postgres-migrations`), and billing convergence gates remain separate from repository-wide direct parity and require appropriate topology/attestation.

---

## Canonical Verification Commands

- `make quality-checks` — Format, tidy, vet, ad-hoc goroutine allowlist, hot-path regex check, archtest guardrails.
- `make test` — Quality checks (`quality-checks-fast`) + default unit tests (`test-unit`) + parity checks (`parity-checks`).
- `make test-unit` — `go test $(GO_TEST_FLAGS) ./...` (fast in-memory/composed unit tests; configurable via `GO_TEST_FLAGS` or `LIP_TEST_PARALLEL`).
- `make test-db-parity` — Sequential repository-wide SQLite and direct PostgreSQL parity gate (derived from `internal/testkit/dbparity` via `dbparity.DefaultCatalog()` across all 8 components).
- `make test-db-parity-sqlite` — Canonical SQLite database parity tests across all registered components.
- `make test-db-parity-postgres-direct` — Repository-wide fail-closed direct PostgreSQL parity (direct/admin DSN; Make sets `LIP_REQUIRE_POSTGRES=1`).
- `make parity-checks` — Full parity matrix: contract TCKs, protocol conformance (`-tags=precommit,integration`), connector parity, and bounded sentinel (configurable via `GO_TEST_FLAGS`).
- `make qa` — Quality checks (`quality-checks-fast`) + full tagged test pass (`-tags=precommit,integration`) + linter (`make lint`: `golangci-lint` preferred, `staticcheck` fallback) + `govulncheck` (`make vuln`) + static release gates (`backend-plugin-release-gates-static`, `test-openresponses-compliance-static`).
