# Phase 4 execution evidence

Status: APPROVED

Scope: parent tasks 4.1-4.4, limited to the existing metering journal and
billing-store infrastructure. Runtime capture, provider adapters, rating,
posting, public host APIs, and task/status metadata were intentionally left
unchanged.

Execution window (UTC): 2026-09-12T22:38:21Z to 2026-09-12T23:33:29Z.

## TDD evidence

The focused schema tests were added before the implementation. The RED run
was:

```text
go test -run 'TestPhase4(V2ProjectionSchemaExists|EconomicsSchemaExists)$' ./internal/infra/metering/journalstore ./internal/infra/billingstore
```

The behavioral failure was the expected missing-schema failure:

```text
journalstore: SQL logic error: no such table: metering_components
billingstore: SQL logic error: no such table: billing_valuations
```

The implementation then added additive migrations and focused behavioral tests
for replay, generic component projection, exact round-trip, transaction
composition, rebuild, and bounded pagination.

The root review found that the original V2 PostgreSQL valuation-line DDL used
INTEGER for seven Go boolean fields. The repair RED run was:

```text
go test -count=1 -run '^TestBillingV2ValuationLineBooleanDDLAndRepairMigration$' ./internal/infra/billingstore
```

It failed because the generated PostgreSQL DDL did not contain BOOLEAN
definitions and the forward repair migration was not registered. The repair
now emits BOOLEAN NOT NULL DEFAULT FALSE in fresh PostgreSQL DDL and adds the
idempotent `20260912020000` migration. That migration drops legacy integer
defaults, converts each column with `USING (column::text IN ('1','true','t'))`,
and restores the FALSE default; SQLite records the migration as a no-op. The
line writer now supplies native PostgreSQL booleans while retaining SQLite's
0/1 representation.

## Implementation evidence

- `metering_facts` remains the canonical observation row family. V2 observation
  fields are additive; legacy V1 payloads, source keys, and fingerprints remain
  readable. `metering_components` is a generic projection for measures and
  reported charges, including aggregate charges with no invented component
  key.
- Observation identity is store-scoped canonical source-event identity plus
  revision. Exact replay is a no-op, changed identity/revision payloads return
  an identity collision, and a new revision appends.
- Valuations, valuation lines, reconciliations, and an economic work marker are
  additive billing-store records. Canonical JSON/fingerprint fields are
  immutable; query columns and line projections are rebuildable. Decimal
  coefficients/scales are stored as text/integer pairs and rounded money keeps
  explicit presence and currency.
- `AppendObservationEconomics` opens one local Bun transaction and composes the
  observation, projections, valuation, reconciliation, and work marker. It
  rejects a writer backed by another database handle and exposes failpoint
  hooks only in infrastructure tests.
- Rebuild APIs delete/recreate projections from canonical JSON, update only
  projection versions/query fields, and preserve canonical payloads and
  fingerprints. Cursors are filter-bound and hard-limited to 500 rows.

## Acceptance mapping

| Acceptance case | Evidence |
|---|---|
| SQLite upgrade preserves a V1 row and supports V2 | `TestPhase4SQLiteUpgradePreservesLegacyV1PayloadAndSourceKey` |
| Arbitrary namespaced measure and aggregate charge | `TestPhase4ObservationAppendProjectsArbitraryComponentsAndCharges` |
| Replay, conflict, revision append, store and tenant isolation | `TestPhase4ObservationReplayRevisionAndStoreIsolation`, `TestPhase4ObservationTenantFilterIsIsolated` |
| Atomic rollback and successful composition | `TestPhase4CompositionRollsBackCanonicalAndEconomicRows` |
| E/Q/P/S/R valuation JSON round-trip | `TestPhase4ValuationBasesRoundTrip` |
| Exact/pre-round and rounded money presence, aggregate charge shape | `TestPhase4ValuationAndReconciliationRoundTripAndReplay`, `TestPhase4ObservationAppendProjectsArbitraryComponentsAndCharges` |
| Rebuild after missing/stale projections | `TestPhase4ValuationRebuildAndBoundedPagination`, `TestPhase4RebuildRepairsStaleReconciliationProjection` |
| Stable bounded pagination and cursor/filter rejection | `TestPhase4ValuationRebuildAndBoundedPagination`, observation pagination assertions in `phase4_observation_store_test.go` |
| Existing V1 metering/billing tests | Focused package GREEN run below |

The E/Q/P/S/R test exercises the frozen contract validators and durable
canonical storage. It does not claim that runtime rating/posting for those
planes is implemented in this phase.

## Verification

GREEN commands:

```text
go test -count=1 ./internal/infra/metering/journalstore ./internal/infra/billingstore ./internal/testkit/dbparity
go test -count=1 -run '^TestBillingV2ValuationLineBooleanDDLAndRepairMigration$' ./internal/infra/billingstore
go test -count=1 ./internal/infra/billingstore
go test -count=1 ./internal/testkit/dbparity
go vet ./internal/infra/metering/journalstore ./internal/infra/billingstore ./internal/testkit/dbparity
gofmt -w internal/infra/billingstore/20260912010000_billing_v2_economics.go internal/infra/billingstore/20260912020000_billing_v2_line_booleans.go internal/infra/billingstore/v2_economics_store.go internal/infra/billingstore/phase4_postgres_bool_repair_test.go internal/infra/billingstore/phase4_postgres_bool_repair_integration_test.go
git diff --check
```

Results: all focused package tests passed; `go vet` passed; `git diff --check`
reported no whitespace errors.

PostgreSQL was attempted with `LIP_REQUIRE_POSTGRES=1` and the integration
tag. It is not certified GREEN: one direct journal probe timed out against the
configured remote, and the parity probe reached a shared schema containing an
older INTEGER `metering_components.value_present` definition while the current
logical schema requires BOOLEAN. The shared remote migration history prevented
the additive repair from rerunning. A fresh isolated billing PostgreSQL schema
was reachable for the narrow repair and append test:

```text
LIP_REQUIRE_POSTGRES=1 go test -tags integration -count=1 -run '^TestBillingV2PostgresLineBooleanRepairAndAppend$' ./internal/infra/billingstore
```

It passed after simulating the legacy INTEGER columns, rerunning the forward
repair migration, checking BOOLEAN/default/nullable metadata, and appending a
valuation whose line boolean projections were scanned successfully. The broad
shared-schema parity run remains uncertified; no broad PostgreSQL pass is
claimed.

Residual risks are limited to the intentionally out-of-scope runtime and
posting integration, and to PostgreSQL requiring a fresh/upgradeable schema
owned by the integration environment before parity and the boolean repair can
be certified.

Root review is APPROVED; see `phase4-review.md`.
