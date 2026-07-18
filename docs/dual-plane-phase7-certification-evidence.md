# Dual-plane Phase 7 certification evidence

Factual archive for `dual-plane-economics-and-concurrency-production-readiness` Phase 7 / task **7.5** (requirements 13.1–13.12).

**Branch:** `feat/dual-plane-readiness-phase-7`
**Host:** Windows 10 (`GOOS=windows`, `GOARCH=amd64`), 2026-07-18 local run
**PR / Actions on this branch:** none (`gh pr list --head feat/dual-plane-readiness-phase-7` → `[]`; no recorded workflow runs to cite)

## Posture (conditional)

| Claim | Status |
|-------|--------|
| Available local implementation gates | **PASS** where executed; Windows race and local pooled are deferred/fail-closed as detailed below |
| OSS foundation `EconomicControlReady` (unconditional) | **Not claimed** until Linux strict race **and** pooled PostgreSQL CI (or equivalent attested pooler) results exist for this branch |
| Commercial billing / payments / invoices / tax | **Out of scope** (never claimed) |

Until the deferred CI gates below have recorded PASS evidence for this change set, treat readiness as **conditional local green**, not certified `EconomicControlReady`.

## Environment posture (no secrets)

| Variable | Present | Notes |
|----------|---------|-------|
| `LIP_TEST_POSTGRES_DSN` | yes | Ambient direct Neon-class endpoint (value not recorded) |
| `LIP_TEST_POSTGRES_ADMIN_DSN` | yes | Same topology family as runtime DSN (value not recorded) |
| `LIP_MIGRATION_POSTGRES_DSN` | no | Migrations target falls back to admin DSN |
| `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER` | unset / not `1` | Correct for direct-only local DSNs |
| `LIP_REQUIRE_POSTGRES_POOLER` | unset | Set by `make test-authority-postgres-pooled` itself |

Local host has **no** transaction-pooler endpoint. Attestation must not be set to `1` against a direct DSN.

## Gate outcomes (this run)

| Gate | Command | Result | Evidence |
|------|---------|--------|----------|
| Focused Phase 7 | `go test -count=1 -run 'TestPhase71_\|TestPhase72_\|TestPhase73_\|TestPhase74_\|TestApplyGenerationBoundVersion\|TestOwner_UnknownSecond' ./internal/core/runtime/ ./internal/core/terminal/ ./pkg/lipsdk/controlplane/ ./internal/qa/` | **PASS** | packages `ok` |
| Metadata-only bound-version fallback | `go test -count=1 ./internal/core/runtime/ -run TestApplyGenerationBoundVersion_MetadataOnlyPublishMetadataFallback` | **PASS** | uses `PublishMetadata` only (no deprecated `Publish`) |
| Makefile PG gate contract | `go test -count=1 ./internal/testkit/ -run TestMakefile_AuthorityPostgres` | **PASS** | requires `./internal/infra/terminalwork/workstore` on direct + pooled; `-skip 'Pooled'` / `-run 'Pooled'` |
| Quality | `make quality-checks` | **PASS** | EXIT=0, ~19.5s |
| Unit / default test | `make test` | **PASS** | EXIT=0, ~43.4s |
| Parity | `make parity-checks` | **PASS** | EXIT=0, ~0.9s |
| Race (Windows) | `make test-race` | **PASS (skip)** | EXIT=0; prints Windows race no-op; **not** race-green |
| Race (Linux authoritative) | `bash scripts/race-check.sh --strict` | **deferred** | configured in `.github/workflows/race-fuzz-nightly.yml`; no local Linux / no branch CI run |
| PG migrations | `make test-postgres-migrations` | **PASS** | EXIT=0, ~2.0s |
| PG direct | `make test-authority-postgres-direct` | **PASS** | EXIT=0, ~31.8s; includes `workstore` (~7.0s) with `-skip 'Pooled'` |
| PG pooled (local) | `make test-authority-postgres-pooled` | **FAIL-CLOSED** (expected) | EXIT≠0; throws without `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`; **not** a pooled PASS |
| PG pooled (CI-required) | `make test-authority-postgres` in PR `qa.yml` with PgBouncer + attestation | **FAIL then remediate in flight** | PR #181 run `29634914224` failed on fresh migrate `20260718000000` (`42P07`); fix pushed — re-check pending |
| Fuzz smoke | `FUZZTIME=200ms make test-fuzz` | **PASS** | EXIT=0, ~70.5s (includes dual-plane fuzz targets) |
| Extra fuzz | `go test -fuzz=FuzzLeaseSet_OccupiesCapacity -fuzztime=5s -run '^$' ./internal/infra/concurrencyauthority/leasestore/` | **PASS** | EXIT=0 |
| Enterprise archtest | `go test -count=1 ./internal/archtest/ -run EnterpriseModule` | **PASS** | EXIT=0 |
| AcquireSet bench smoke | `go test -count=1 -run '^$' -bench BenchmarkMemoryAcquireSetFiveSlotHundredContenders -benchtime=200ms ./internal/infra/concurrencyauthority/leasestore/` | **PASS** | EXIT=0 |
| check-config | `go run ./cmd/lipstd check-config --config config/examples/dogfood-local-stub.yaml` | **PASS** | EXIT=0 |
| Full QA | `make qa` | **PASS** | EXIT=0, ~53.9s (`QA:0` path; lint + vuln clean) |

## Failures and remediations recorded for Phase 7.5

| Issue | Remediation |
|-------|-------------|
| PR #181 QA `make test-authority-postgres`: `lipstd migrate` failed at metering `20260718000000` with `relation "metering_facts_store_source_event_key_key" already exists (SQLSTATE=42P07)` | Root cause: baseline DDL already creates that UNIQUE constraint; Up only caught `duplicate_object`/`unique_violation`, but PostgreSQL raises `duplicate_table` (`42P07`) for the existing index/relation. Fix: catch `duplicate_table` in the DO block. RED: unit SQL guard + Postgres integration idempotency re-run of Up after Migrate. Local proof: unit + `make test-postgres-migrations` + journalstore collision integration + `make quality-checks` + `make qa` PASS. |
| PR #181 QA run `29635473410` (after 42P07 fix): migrate + direct PASS; pooled journalstore timed out 10m on `TestPhase3_PostgresPooledJournalContracts/store_id_isolation` | Root cause: `openSharedPooledJournalStore` took non-reentrant `pooledJournalTestMu`; `store_id_isolation` calls open + openPeer → self-deadlock. Fix: reentrantTBMutex keyed by `*testing.T`. RED: nested Lock / nested openShared tests. Task 7.5 left unchecked pending green PR QA pooled run. |
| Independent review: workstore missing from Makefile PG gates | Added `./internal/infra/terminalwork/workstore` to `test-authority-postgres-direct` and `test-authority-postgres-pooled` (Windows + Unix); contract tests enforce inclusion; direct keeps `-skip 'Pooled'`, pooled keeps `-run 'Pooled'` |
| Independent review: metadata-only bound-version fallback untested | Added `TestApplyGenerationBoundVersion_MetadataOnlyPublishMetadataFallback` using `PublishMetadata` only |
| Independent review: evidence claimed readiness while gates `_pending_` | This document rewritten with actual EXIT codes; unconditional `EconomicControlReady` withdrawn |
| Earlier cert: pooled hang under ambient direct DSN | `EvaluateDualPlanePostgresGate` always requires pooler attestation; Makefile pooled target fails closed without `=1` |
| Earlier cert: ledgerstore `ListByAttempt` length drift | Unique IDs + DELETE cleanup in ledgerstore Postgres integration test |

## CI configuration (authoritative for deferred gates)

### Pooled PostgreSQL (PR QA)

`.github/workflows/qa.yml` job `qa-run` (Linux):

- Services: `postgres:17-alpine` + `edoburu/pgbouncer` (`POOL_MODE: transaction`)
- Step **PostgreSQL authority integration proof** sets:
  - `LIP_TEST_POSTGRES_DSN` → PgBouncer `:6432`
  - `LIP_TEST_POSTGRES_ADMIN_DSN` / `LIP_MIGRATION_POSTGRES_DSN` → direct `:5432`
  - `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`
  - runs `make test-authority-postgres` (migrations + direct + pooled, including workstore under `-run 'Pooled'`)

**This branch:** PR #181 open. QA runs: `29634914224` FAIL (migrate 42P07); `29635473410` FAIL (pooled mutex deadlock after migrate green). Further fix pushed — re-check pending.

### Linux strict race + fuzz (nightly)

`.github/workflows/race-fuzz-nightly.yml`:

- `bash scripts/race-check.sh --strict`
- `make test-fuzz` with `FUZZTIME=6s`

Local Windows `make test-race` is a documented no-op and must not be cited as race-green.

## Task 7.5 checkbox decision

Left **unchecked** in `.kiro/specs/.../tasks.md`.

Reason: task deliverable requires the full mandatory matrix (including Linux strict race and pooled PostgreSQL) to pass so the archive can **truthfully** mark `EconomicControlReady`. Local work proves fail-closed pooled behavior and CI wiring, but does **not** substitute for executed Linux race or attested pooler PASS on this branch. Checkbox may be flipped only after those CI results are archived here (or an equivalent attested local pooler run).

## Explicit non-claims

- No commercial billing readiness
- No local pooled PostgreSQL PASS
- No Windows race-green
- No unconditional `EconomicControlReady` until deferred CI evidence exists
