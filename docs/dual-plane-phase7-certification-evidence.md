# Dual-plane Phase 7 certification evidence

Factual archive for `dual-plane-economics-and-concurrency-production-readiness` Phase 7 / task **7.5** (requirements 13.1–13.12).

**Branch:** `feat/dual-plane-readiness-phase-7`
**Head SHA (certified):** `4de4bbf28b14b21561c80862f3199f493dac22b1`
**Host (local gates):** Windows 10 (`GOOS=windows`, `GOARCH=amd64`), 2026-07-18
**PR:** [#181](https://github.com/matdev83/go-llm-interactive-proxy/pull/181)

## Posture (certified)

| Claim | Status |
|-------|--------|
| Available local implementation gates | **PASS** where executed; Windows race remains a documented no-op; local pooled remains fail-closed without pooler attestation |
| OSS foundation `EconomicControlReady` (unconditional) | **PASS / certified** on head `4de4bbf` after Linux strict race + fuzz and attested PgBouncer pooled PostgreSQL CI both recorded SUCCESS |
| Commercial billing / payments / invoices / tax | **Out of scope** (never claimed) |

Mandatory remote gates for this change set are green. The OSS foundation may truthfully mark `EconomicControlReady`. This does **not** claim commercial billing readiness.

## Environment posture (no secrets)

| Variable | Present | Notes |
|----------|---------|-------|
| `LIP_TEST_POSTGRES_DSN` | yes | Ambient direct Neon-class endpoint (value not recorded) |
| `LIP_TEST_POSTGRES_ADMIN_DSN` | yes | Same topology family as runtime DSN (value not recorded) |
| `LIP_MIGRATION_POSTGRES_DSN` | no | Migrations target falls back to admin DSN |
| `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER` | unset / not `1` | Correct for direct-only local DSNs |
| `LIP_REQUIRE_POSTGRES_POOLER` | unset | Set by `make test-authority-postgres-pooled` itself |

Local host has **no** transaction-pooler endpoint. Attestation must not be set to `1` against a direct DSN. Authoritative pooled PASS is CI-only (below).

## Gate outcomes

| Gate | Command | Result | Evidence |
|------|---------|--------|----------|
| Focused Phase 7 | `go test -count=1 -run 'TestPhase71_\|TestPhase72_\|TestPhase73_\|TestPhase74_\|TestApplyGenerationBoundVersion\|TestOwner_UnknownSecond' ./internal/core/runtime/ ./internal/core/terminal/ ./pkg/lipsdk/controlplane/ ./internal/qa/` | **PASS** | packages `ok` (local) |
| Metadata-only bound-version fallback | `go test -count=1 ./internal/core/runtime/ -run TestApplyGenerationBoundVersion_MetadataOnlyPublishMetadataFallback` | **PASS** | uses `PublishMetadata` only (no deprecated `Publish`) |
| Makefile PG gate contract | `go test -count=1 ./internal/testkit/ -run TestMakefile_AuthorityPostgres` | **PASS** | requires `./internal/infra/terminalwork/workstore` on direct + pooled; `-skip 'Pooled'` / `-run 'Pooled'` |
| Quality | `make quality-checks` | **PASS** | EXIT=0, ~19.5s (local) |
| Unit / default test | `make test` | **PASS** | EXIT=0, ~43.4s (local) |
| Parity | `make parity-checks` | **PASS** | EXIT=0, ~0.9s (local) |
| Race (Windows) | `make test-race` | **PASS (skip)** | EXIT=0; prints Windows race no-op; **not** race-green |
| Race (Linux authoritative) | `bash scripts/race-check.sh --strict` | **PASS** | [workflow_dispatch run 29636530168](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/29636530168) on `4de4bbf`; job `race-fuzz` step **Race detector** SUCCESS |
| PG migrations | `make test-postgres-migrations` | **PASS** | EXIT=0, ~2.0s (local); also covered in PR QA |
| PG direct | `make test-authority-postgres-direct` | **PASS** | EXIT=0, ~31.8s (local); includes `workstore` with `-skip 'Pooled'` |
| PG pooled (local) | `make test-authority-postgres-pooled` | **FAIL-CLOSED** (expected) | EXIT≠0 without `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`; **not** a pooled PASS |
| PG pooled (CI-required) | `make test-authority-postgres` in PR `qa.yml` with PgBouncer + attestation | **PASS** | [PR #181 QA run 29636531519](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/29636531519) on `4de4bbf`; `qa-run` + `qa` SUCCESS; step **PostgreSQL authority integration proof** SUCCESS (attested PgBouncer) |
| Fuzz smoke (local) | `FUZZTIME=200ms make test-fuzz` | **PASS** | EXIT=0, ~70.5s (includes dual-plane fuzz targets) |
| Fuzz smoke (Linux CI) | `FUZZTIME=6s make test-fuzz` | **PASS** | [run 29636530168](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/29636530168) step **Release gates (fuzz smoke)** SUCCESS |
| Extra fuzz | `go test -fuzz=FuzzLeaseSet_OccupiesCapacity -fuzztime=5s -run '^$' ./internal/infra/concurrencyauthority/leasestore/` | **PASS** | EXIT=0 (local) |
| Enterprise archtest | `go test -count=1 ./internal/archtest/ -run EnterpriseModule` | **PASS** | EXIT=0 |
| AcquireSet bench smoke | `go test -count=1 -run '^$' -bench BenchmarkMemoryAcquireSetFiveSlotHundredContenders -benchtime=200ms ./internal/infra/concurrencyauthority/leasestore/` | **PASS** | EXIT=0 |
| check-config | `go run ./cmd/lipstd check-config --config config/examples/dogfood-local-stub.yaml` | **PASS** | EXIT=0 |
| Full QA (local) | `make qa` | **PASS** | EXIT=0, ~53.9s (`QA:0` path; lint + vuln clean) |
| Full QA (PR) | GitHub Actions workflow **QA** | **PASS** | [run 29636531519](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/29636531519): `changes`, `qa-run`, `qa` all SUCCESS |

## Failures and remediations recorded for Phase 7.5

| Issue | Remediation |
|-------|-------------|
| PR #181 QA `make test-authority-postgres`: `lipstd migrate` failed at metering `20260718000000` with `relation "metering_facts_store_source_event_key_key" already exists (SQLSTATE=42P07)` | Root cause: baseline DDL already creates that UNIQUE constraint; Up only caught `duplicate_object`/`unique_violation`, but PostgreSQL raises `duplicate_table` (`42P07`) for the existing index/relation. Fix: catch `duplicate_table` in the DO block. RED: unit SQL guard + Postgres integration idempotency re-run of Up after Migrate. Local proof: unit + `make test-postgres-migrations` + journalstore collision integration + `make quality-checks` + `make qa` PASS. Historical fail run: `29634914224`. |
| PR #181 QA run `29635473410` (after 42P07 fix): migrate + direct PASS; pooled journalstore timed out 10m on `TestPhase3_PostgresPooledJournalContracts/store_id_isolation` | Root cause: `openSharedPooledJournalStore` took non-reentrant `pooledJournalTestMu`; `store_id_isolation` calls open + openPeer → self-deadlock. Fix: reentrantTBMutex keyed by `*testing.T`. RED: nested Lock / nested openShared tests. |
| Independent review: workstore missing from Makefile PG gates | Added `./internal/infra/terminalwork/workstore` to `test-authority-postgres-direct` and `test-authority-postgres-pooled` (Windows + Unix); contract tests enforce inclusion; direct keeps `-skip 'Pooled'`, pooled keeps `-run 'Pooled'` |
| Independent review: metadata-only bound-version fallback untested | Added `TestApplyGenerationBoundVersion_MetadataOnlyPublishMetadataFallback` using `PublishMetadata` only |
| Independent review: evidence claimed readiness while gates `_pending_` | Evidence rewritten with actual EXIT codes; unconditional `EconomicControlReady` withdrawn until CI PASS; restored here after green remote gates |
| Earlier cert: pooled hang under ambient direct DSN | `EvaluateDualPlanePostgresGate` always requires pooler attestation; Makefile pooled target fails closed without `=1` |
| Earlier cert: ledgerstore `ListByAttempt` length drift | Unique IDs + DELETE cleanup in ledgerstore Postgres integration test |
| Linux strict race run `29636161699` FAIL | Distinct race (2 reports, same address): heartbeat wrote `LeaseSetReleaseAcceptErr` while `TestPhase6Remediation_FailClosedRecordsAcceptFailure` polled it before `ctx.Done`. Ownership: heartbeat records accept err then `cancelRequest`; observers must wait on cancel. Fix: `awaitFailClosedCancel` + focused regression; drop redundant heartbeat reassignment. Re-dispatch on `4de4bbf`: run `29636530168` PASS. |

## CI evidence (authoritative remote gates)

### Pooled PostgreSQL (PR QA) — PASS

`.github/workflows/qa.yml` job `qa-run` (Linux):

- Services: `postgres:17-alpine` + `edoburu/pgbouncer` (`POOL_MODE: transaction`)
- Step **PostgreSQL authority integration proof** sets:
  - `LIP_TEST_POSTGRES_DSN` → PgBouncer `:6432`
  - `LIP_TEST_POSTGRES_ADMIN_DSN` / `LIP_MIGRATION_POSTGRES_DSN` → direct `:5432`
  - `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`
  - runs `make test-authority-postgres` (migrations + direct + pooled, including workstore under `-run 'Pooled'`)

**Certified run:** [29636531519](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/29636531519) on head `4de4bbf` — `qa-run` SUCCESS, `qa` SUCCESS, step **PostgreSQL authority integration proof** SUCCESS.

Historical failures on the same PR (retained): `29634914224` (migrate 42P07); `29635473410` (pooled mutex deadlock).

### Linux strict race + fuzz (nightly / workflow_dispatch) — PASS

`.github/workflows/race-fuzz-nightly.yml`:

- `bash scripts/race-check.sh --strict`
- `make test-fuzz` with `FUZZTIME=6s`

**Certified run:** [29636530168](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/29636530168) on head `4de4bbf` — job `race-fuzz` SUCCESS (Race detector + Release gates fuzz smoke).

Historical failure (retained): `29636161699` (fail-closed accept-err poll race) — remediated before re-dispatch.

Local Windows `make test-race` is a documented no-op and must not be cited as race-green.

## Task 7.5 checkbox decision

Marked **checked** in `.kiro/specs/dual-plane-economics-and-concurrency-production-readiness/tasks.md`.

Reason: the full mandatory matrix passed, including Linux strict race (`29636530168`) and attested PgBouncer pooled PostgreSQL via PR QA (`29636531519`). The archive truthfully marks OSS foundation `EconomicControlReady` while retaining the explicit non-claim of commercial billing.

## Explicit non-claims

- No commercial billing readiness (payments / invoices / tax remain out of scope)
- No local pooled PostgreSQL PASS (local remains fail-closed without attestation)
- No Windows race-green (Linux CI is authoritative)
