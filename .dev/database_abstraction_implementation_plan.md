# Migration to bun as Database Abstraction Layer

Introduce `uptrace/bun` as the dialect-portable DB abstraction, replacing raw `database/sql` in both store domains. Make all database settings configurable via config YAML. Preserve existing interfaces unchanged.

## Current State

| Store Domain | Package | Lines | Interface | Import Sites |
|---|---|---|---|---|
| B2BUA continuity | `internal/core/continuity/sqlitestore/` | ~370 | `b2bua.Store` (7 methods) | [store.go](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/continuity/store.go) (factory) |
| Secure sessions | `internal/core/securesession/adapters/sqlite/` | ~888 + 177 schema | `app.Store` (16 methods) + `SessionUsageRollup` | [secure_session.go](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/infra/runtimebundle/secure_session.go) (builder) |

Both stores use raw `database/sql` with `modernc.org/sqlite`. Driver registration happens via blank imports inside each store package.

### Key Constraints

- **Interfaces stay unchanged**: `b2bua.Store` and `app.Store` are consumer-owned ports — bun is an implementation detail.
- **Core never imports bun**: bun lives inside adapter packages and `internal/infra/db/`.
- **Existing SQLite stores remain**: they are not deleted, just supplemented. The config controls which store implementation is active.
- **Schema DDL stays as raw SQL**: partial unique indexes (`WHERE continuity_key != ''`), `AUTOINCREMENT`, and multi-statement migration scripts cannot be expressed via bun model tags alone.

---

## Proposed Changes

### Phase 1 — Foundation

#### [NEW] `internal/infra/db/doc.go`

Package-level documentation. Establishes that bun lives here and behind store adapters, never in core contracts.

#### [NEW] `internal/infra/db/bun.go`

Thin factory function:

```go
func NewBunDB(sqldb *sql.DB, dialect string) (*bun.DB, error)
```

- Maps `"sqlite"` → `sqlitedialect.New()`, `"postgres"` → `pgdialect.New()`.
- Takes a pre-opened `*sql.DB` (callers are responsible for driver registration).
- No global state, no side effects.

#### [NEW] `internal/infra/db/bun_test.go`

Unit tests: sqlite and postgres dialect wiring, nil rejection, unknown dialect error.

#### [NEW] `internal/infra/db/open.go`

Connection helpers for common backends:

```go
func OpenSQLite(path string) (*sql.DB, error)
func OpenPostgres(dsn string) (*sql.DB, error)
```

- `OpenSQLite` reuses the existing `modernc.org/sqlite` driver + pragma DSN pattern from `sqlitestore`.
- `OpenPostgres` uses `pgdriver` with the provided DSN.
- Both return `*sql.DB` only — the `→ bun.DB` wrapping is done by `NewBunDB`.

---

### Phase 2 — Config Layer

#### [MODIFY] [model.go](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/config/model.go)

Extend `ContinuityConfig` and `SecureSessionConfig` with new database fields:

```diff
 type ContinuityConfig struct {
     InMemory   bool   `yaml:"in_memory"`
     Store      string `yaml:"store"`
     SQLitePath string `yaml:"sqlite_path"`
+    // PostgresDSN is the connection string when store is "postgres".
+    PostgresDSN string `yaml:"postgres_dsn"`
     TTL        string `yaml:"ttl"`
     MaxLegs    int    `yaml:"max_legs"`
 }
```

```diff
 type SecureSessionConfig struct {
     Enabled    bool   `yaml:"enabled"`
     Store      string `yaml:"store"`
     SQLitePath string `yaml:"sqlite_path"`
+    // PostgresDSN is the connection string when store is "postgres".
+    PostgresDSN string `yaml:"postgres_dsn"`
     // ... rest unchanged
 }
```

> [!IMPORTANT]
> The `Store` field enum expands from `memory | sqlite` to `memory | sqlite | postgres`. Both `ContinuityConfig` and `SecureSessionConfig` gain the same field so they can independently target different backends (e.g. continuity in-memory + sessions in postgres).

#### [NEW] `internal/core/config/database.go`

Shared `DatabaseConfig` sub-struct (optional DRY approach if preferred over inline fields):

```go
type DatabaseConfig struct {
    // MaxOpenConns limits the number of open connections to the database. Zero = driver default.
    MaxOpenConns int `yaml:"max_open_conns"`
    // MaxIdleConns limits idle connections. Zero = driver default.
    MaxIdleConns int `yaml:"max_idle_conns"`
    // ConnMaxLifetime is a Go duration string. Empty = no limit.
    ConnMaxLifetime string `yaml:"conn_max_lifetime"`
    // ConnMaxIdleTime is a Go duration string. Empty = no limit.
    ConnMaxIdleTime string `yaml:"conn_max_idle_time"`
}
```

> [!NOTE]
> **Decision point for review:** Should connection pool settings live as a shared sub-struct (`database:` top-level) or inline on each store config? The shared approach avoids duplication when both stores target the same postgres instance. Inline gives per-store tuning. **Recommendation: shared `database:` block** since the `*sql.DB` can be shared between stores when they point to the same server.

This gives a config surface like:

```yaml
# New top-level database section (optional, only needed for postgres or tuning)
database:
  max_open_conns: 25
  max_idle_conns: 10
  conn_max_lifetime: 30m
  conn_max_idle_time: 5m

continuity:
  store: postgres            # memory | sqlite | postgres
  # sqlite_path: ./data/continuity.db   # only when store: sqlite
  postgres_dsn: "postgres://user:pass@localhost:5432/lip?sslmode=disable"  # only when store: postgres

secure_session:
  enabled: true
  store: postgres            # memory | sqlite | postgres
  postgres_dsn: "postgres://user:pass@localhost:5432/lip?sslmode=disable"
  # ...
```

#### [MODIFY] [validate.go](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/config/validate.go)

- `validateContinuityStores`: extend store enum to include `"postgres"`, validate `postgres_dsn` when store is `"postgres"` (non-empty, no obvious injection), reject `ttl`/`max_legs` for postgres same as sqlite.
- `validateSecureSession`: extend store enum to include `"postgres"`, validate `postgres_dsn`, accept `audit_durability: durable` for store `"postgres"`.
- `validateDatabase` (new): validate pool settings if `database:` block is present.

#### [MODIFY] [config.yaml](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/config/config.yaml)

Add commented-out examples for postgres and database pool settings.

---

### Phase 3 — Continuity bun Store

#### [NEW] `internal/core/continuity/bunstore/store.go`

Implements `b2bua.Store` using `*bun.DB`:

| Method | bun technique | Notes |
|---|---|---|
| `ResolveALeg` | `RunInTx` + `NewSelect().Model()` + `NewUpdate()` | Same transactional semantics as sqlitestore |
| `CreateALeg` | `RunInTx` + `NewDelete()` + `NewInsert()` | Deletes prior continuity key entry first |
| `FetchALeg` | `NewSelect().Model()` + `NewUpdate()` | Touch `last_seen_at_unix` on read |
| `SetWeightedFirstConsumed` | `NewUpdate().Set()` | Check `RowsAffected` for not-found |
| `NextBLeg` | `RunInTx` + seq increment + `NewInsert()` | Transactional sequence allocation |
| `RecordAttempt` | `NewInsert().On("CONFLICT ...")` | bun generates dialect-correct upsert |
| `LoadAttempts` | `NewSelect().Model(&[]rows)` | Struct slice scan; order by seq ASC |

**DDL migration**: Raw SQL statements (same as sqlitestore) executed via `db.ExecContext`.

- Uses `BIGINT` instead of `INTEGER` for unix timestamps (PostgreSQL-friendly).
- Partial unique index (`WHERE continuity_key != ''`) works on both SQLite and PostgreSQL.
- `AUTOINCREMENT` is SQLite-only — not used in the continuity schema (uses TEXT PKs).

Row models are package-private structs with `bun:` tags. The `bun.BaseModel` embed carries the table name.

#### [NEW] `internal/core/continuity/bunstore/store_test.go`

Full test suite mirroring `sqlitestore` tests:
- `TestNew_nilDB`, `TestNew_inMemory`
- All 7 `Store` methods: roundtrip, not-found, upsert, monotonic B-leg seq
- Uses in-memory SQLite for speed (same as current tests)

#### [MODIFY] [store.go (continuity factory)](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/continuity/store.go)

Add a `"postgres"` case in `OpenStore`:

```go
case "postgres":
    dsn := strings.TrimSpace(cfg.PostgresDSN)
    if dsn == "" {
        return nil, fmt.Errorf("continuity: postgres_dsn required when store is \"postgres\"")
    }
    sqldb, err := db.OpenPostgres(dsn)    // from internal/infra/db
    if err != nil { ... }
    bunDB, err := db.NewBunDB(sqldb, db.DialectPostgres)
    if err != nil { ... }
    return bunstore.New(bunDB)
```

The `"sqlite"` case can optionally be migrated to also go through bun (via `bunstore`), or stay on the raw sqlitestore — **user decision point** below.

---

### Phase 4 — Secure Session bun Store

#### [NEW] `internal/core/securesession/adapters/bunstore/store.go`

Implements `app.Store` (16 methods) + `SessionUsageRollup` using `*bun.DB`.

This is the largest workpiece (~900 lines of raw SQL → ~600-700 lines of bun). Key techniques:

| Method group | bun approach |
|---|---|
| `Create` | `NewInsert().Model()` — struct-scanned insert |
| `LoadByID/FP/ALeg` | `NewSelect().Model()` with JSON fields scanned via `scanRecord` helper |
| `TouchActivity` | `NewUpdate()` with `CASE WHEN` for monotonic touch |
| `AppendAttemptTrace` | `RunInTx` + `NewInsert()` + `NewUpdate()` for attempt_count |
| `UpdateAttemptOutcome` | `RunInTx` + `NewUpdate()` on trace + session |
| `AppendTranscript` | `RunInTx` + policy check + insert + upsert turn |
| `AddUsage` | `RunInTx` + update totals + insert usage row + update accounting JSON |
| `Summary` | `NewSelect()` with conditional `WHERE` clauses + subquery for turn_count |
| `ListAttemptEvidence` | Two `NewSelect()` queries (traces + usage GROUP BY) merged in Go |
| `CheckReadiness` | Trivial pass-through (same as current) |
| `UsageTokenTotals` | `NewSelect().Column("usage_in", "usage_out")` |

**Schema migration**: Uses the same raw DDL as the current `schema.go`, with one adjustment:

- `AUTOINCREMENT` (SQLite-only) → `BIGSERIAL` (PostgreSQL) for the `id` column in `lip_secure_attempt_traces` and `lip_secure_usage`. The migration function will detect the dialect and use the appropriate SQL.

#### [NEW] `internal/core/securesession/adapters/bunstore/schema.go`

DDL migration that is dialect-aware:

```go
func migrate(ctx context.Context, db *bun.DB) error {
    // detect dialect for AUTOINCREMENT vs BIGSERIAL
    // run CREATE TABLE IF NOT EXISTS statements
    // run upgrade columns (same as current)
}
```

#### [NEW] `internal/core/securesession/adapters/bunstore/store_test.go`

Full test suite + contract test integration:

```go
func TestBunStore_Contract(t *testing.T) {
    storecontract.RunAll(t, func() app.Store {
        return newTestBunStore(t)
    })
}
```

This reuses the existing [storecontract.RunAll](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/securesession/storecontract/contract.go) harness — the same 12+ behavioral checks that the memory and sqlite stores already pass.

#### [NEW] `internal/core/securesession/storecontract/bun_contract_test.go`

Register the bun store adapter in the existing contract test suite (alongside the existing memory and sqlite entries).

---

### Phase 5 — Composition Root Wiring

#### [MODIFY] [secure_session.go](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/infra/runtimebundle/secure_session.go)

Add `"postgres"` case in `buildSecureSessionRuntime`:

```go
case "postgres":
    dsn := strings.TrimSpace(ss.PostgresDSN)
    if dsn == "" { ... }
    sqldb, err := db.OpenPostgres(dsn)
    bunDB, err := db.NewBunDB(sqldb, db.DialectPostgres)
    bunSt, err := bunstore.New(bunDB)
    // wire bunSt as app.Store, same pattern as current sqlite case
```

> [!TIP]
> Optionally, also route the `"sqlite"` case through `bunstore` (via `NewBunDB(sqldb, "sqlite")`) to eliminate the raw-SQL sqlite adapter from the runtime path. This would give a single code path for both dialects. The old `adapters/sqlite` package remains for backward compatibility and tests but is no longer the default wired path.

#### [MODIFY] [build.go](file:///c:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/infra/runtimebundle/build.go)

- If a shared `DatabaseConfig` is added, apply pool tuning (`SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`, `SetConnMaxIdleTime`) to `*sql.DB` before wrapping with bun.
- Close bun.DB in the `closers` slice.

---

### Phase 6 — Shared Connection (Optional Optimization)

When both `continuity.store` and `secure_session.store` are `"postgres"` with the same DSN, the composition root can share a single `*sql.DB` / `*bun.DB` instance. This avoids opening two connection pools to the same server.

```go
// pseudocode in runtimebundle.Build
if cfg.Continuity.PostgresDSN == cfg.SecureSession.PostgresDSN {
    // reuse the same bun.DB for both stores
}
```

This is an optimization pass, not required for correctness.

---

### Phase 7 — Tests & Verification

#### Test plan

| What | How | Package |
|---|---|---|
| infra/db factory | Unit tests (sqlite, postgres dialect, nil/unknown) | `internal/infra/db/` |
| bunstore continuity | Full Store interface coverage (in-memory SQLite) | `internal/core/continuity/bunstore/` |
| bunstore secure session | `storecontract.RunAll` (in-memory SQLite) | `internal/core/securesession/adapters/bunstore/` |
| Config validation | Extend existing validation tests for `"postgres"`, `postgres_dsn`, `database:` | `internal/core/config/` |
| Composition wiring | Extend `runtimebundle` tests to cover postgres store path | `internal/infra/runtimebundle/` |
| Existing tests untouched | `make test` passes — no regression | repo-wide |
| Architecture guards | No bun imports in `pkg/lipapi`, `pkg/lipsdk`, `internal/core/` (except store adapters) | `internal/archtest/` or `internal/qa/` |

#### Verification commands

```bash
make quality-checks                  # gofmt, mod tidy, build, vet
make test                            # full test suite
go test -count=1 ./internal/infra/db/...
go test -count=1 ./internal/core/continuity/bunstore/...
go test -count=1 ./internal/core/securesession/adapters/bunstore/...
go test -count=1 ./internal/core/securesession/storecontract/...
go test -count=1 ./internal/core/config/...
go test -count=1 ./internal/infra/runtimebundle/...
```

---

## Open Questions

> [!IMPORTANT]
> **Q1: Shared `database:` config block vs inline fields?**
> Should connection pool tuning live as a top-level `database:` section (shared by all stores using the same DSN) or duplicated inline on each store config? **Recommendation: shared top-level block** — simpler for operators, avoids drift.

> [!IMPORTANT]
> **Q2: Route existing `sqlite` case through bun too?**
> Once `bunstore` exists, should the `"sqlite"` config option also use `bunstore` (via `NewBunDB(sqldb, "sqlite")`) rather than the old raw-SQL `sqlitestore`? This gives one code path instead of two. **Recommendation: yes**, but keep old `sqlitestore` compilable for comparison and fallback.

> [!WARNING]
> **Q3: PostgreSQL integration testing gates.**
> All unit tests use in-memory SQLite. Real PostgreSQL integration tests require a live database. Options:
> - **CI-only**: GitHub Actions service container with `postgres:16` (add to `.github/workflows/qa.yml`)
> - **Local**: `docker compose` dev helper (add `docker-compose.dev.yml`)
> - **Skip by default**: guard with `LIP_TEST_POSTGRES_DSN` env var; skip when unset
>
> **Recommendation: env-gated + CI service container.** No mandatory local postgres install.

> [!NOTE]
> **Q4: Migration strategy for existing SQLite databases.**
> Existing users with `store: sqlite` and data on disk are unaffected — the `sqlitestore` adapter stays. When switching to `store: postgres`, they would need to migrate data manually (or we provide a `lipstd migrate` command later). This is **out of scope** for the initial bun integration but should be documented.

---

## File Change Summary

| Status | Path | Effort |
|---|---|---|
| **[NEW]** | `internal/infra/db/doc.go` | S |
| **[NEW]** | `internal/infra/db/bun.go` | S |
| **[NEW]** | `internal/infra/db/open.go` | S |
| **[NEW]** | `internal/infra/db/bun_test.go` | S |
| **[NEW]** | `internal/core/continuity/bunstore/store.go` | M |
| **[NEW]** | `internal/core/continuity/bunstore/store_test.go` | M |
| **[NEW]** | `internal/core/securesession/adapters/bunstore/store.go` | **L** |
| **[NEW]** | `internal/core/securesession/adapters/bunstore/schema.go` | M |
| **[NEW]** | `internal/core/securesession/adapters/bunstore/store_test.go` | M |
| **[NEW]** | `internal/core/securesession/storecontract/bun_contract_test.go` | S |
| **[NEW]** | `internal/core/config/database.go` (if shared block) | S |
| **[MODIFY]** | `internal/core/config/model.go` | S |
| **[MODIFY]** | `internal/core/config/validate.go` | S |
| **[MODIFY]** | `internal/core/config/validate_test.go` | S |
| **[MODIFY]** | `internal/core/continuity/store.go` | S |
| **[MODIFY]** | `internal/infra/runtimebundle/secure_session.go` | S |
| **[MODIFY]** | `internal/infra/runtimebundle/build.go` | S |
| **[MODIFY]** | `config/config.yaml` | S |
| **[MODIFY]** | `go.mod` / `go.sum` | auto |

**Total new code**: ~1,500–1,800 lines (store impls + tests + config + infra).
**Existing code modified**: ~150 lines across 6 files.
**No files deleted.**

---

## Execution Order

```mermaid
graph TD
    P1["Phase 1: infra/db foundation"]
    P2["Phase 2: Config layer"]
    P3["Phase 3: Continuity bunstore"]
    P4["Phase 4: Secure session bunstore"]
    P5["Phase 5: Composition root"]
    P6["Phase 6: Shared connection (optional)"]
    P7["Phase 7: Tests & verification"]

    P1 --> P3
    P1 --> P4
    P2 --> P3
    P2 --> P4
    P3 --> P5
    P4 --> P5
    P5 --> P6
    P5 --> P7
    P6 --> P7
```

Phases 3 and 4 can be parallelized since they are independent store domains. Phase 2 (config) can also be done alongside Phase 1 since they don't share files.
