# Phase 4 review

- Verdict: APPROVED.
- Scope: parent tasks 4.1–4.4 only.
- Boundary: existing metering-journal and billing-store infrastructure, their migrations/tests, and canonical dbparity registration. No runtime, provider, rating, posting, public host or cutover behavior is implied.

## Requirement review

- V2 observations append into the existing canonical `metering_facts` family and project arbitrary measures/reported charges into generic component rows without changing legacy V1 payloads or fingerprints.
- Store-scoped source identity plus revision supports exact replay, conflicting-payload rejection and later revisions. Query filters/cursors are store-bound and capped at 500.
- Immutable valuation, line and reconciliation records preserve canonical JSON, E/Q/P/S/R basis, exact decimals and money presence; projections rebuild deterministically from canonical data.
- The infra-only transaction seam composes observation, projections, economics and work intent on one Bun transaction and the failpoint test proves rollback.
- SQLite and PostgreSQL migrations preserve equivalent boolean semantics. Root review found and remediated seven PostgreSQL valuation-line columns that initially used INTEGER; a subsequent migration repairs already-created schemas.

## Fresh verification

- `go test -count=1 ./internal/infra/metering/journalstore ./internal/infra/billingstore ./internal/testkit/dbparity` — PASS.
- `make test-db-parity-sqlite` — PASS across the canonical catalog.
- `go vet ./internal/infra/metering/journalstore ./internal/infra/billingstore ./internal/testkit/dbparity` — PASS.
- `LIP_REQUIRE_POSTGRES=1 go test -tags integration -count=1 -run '^TestBillingV2PostgresLineBooleanRepairAndAppend$' ./internal/infra/billingstore` — PASS against a fresh isolated schema.

Broad shared-schema PostgreSQL parity was not certified because that external schema has stale earlier development migration state and one probe timed out. The phase has direct isolated PostgreSQL proof for the changed billing migration; full topology certification remains a later required gate. No other requirement-blocking finding remains.
