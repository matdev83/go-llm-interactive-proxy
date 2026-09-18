# Refinement 8.2 journalstore JSONB fix review

## Review verdict

STATUS: APPROVED

The remediated narrow fix safely removes raw JSON representation from replay
authority and preserves fail-closed durable identity/fingerprint semantics.

## Findings

- `observation_outbox.go:389-412` now decodes the stored canonical
  `metering.Observation`, runs `Canonical()` validation, checks store scope and
  duplicated `observation_id`/`observation_revision`, rejects missing
  fingerprints, and compares a recomputed full `Observation.Fingerprint()` to
  the durable fingerprint column. Malformed JSON and invalid observations
  preserve `metering.ErrInvalidObservation`; identity/fingerprint drift wraps
  `ErrIdentityCollision`.
- `observation_store.go:328-375,393-445` selects duplicated fact identity
  columns on lookup and post-conflict winner reads. The old raw payload compare
  and empty-fingerprint bypass are gone; both paths use the shared validator.
- `GetObservation` (`:772-795`), `ListObservations` (`:824-953`), and
  `RebuildObservationProjections` (`:1084-1147`) all use the same validated
  canonical decoder. Rebuild validates every row before deleting/recreating
  projections, preserving transaction rollback on failure.
- `ReplayFingerprint` is consulted only after stored full-fingerprint proof and
  only for the approved receipt/lineage-placement append replay. The existing
  `GetObservationRef` convenience check runs after `GetObservation` has already
  validated the durable full fingerprint, so it cannot admit corrupt storage.
- Focused tests cover SQLite exact replay, key-order/whitespace normalization,
  live PostgreSQL JSONB normalization, receipt/lineage replay, semantic
  collisions, malformed and semantically invalid payloads, missing/mismatched
  fingerprints, identity/revision drift, and Get/List/rebuild corruption.
  The implementation changes are private journalstore logic plus focused tests;
  no schema, migration, public API, provider, or dialect branch was added.

## Blockers

None.

## Verification

- PASS: `go test -count=2 ./internal/infra/metering/journalstore/ -run 'TestObservationFactJSONB|TestObservationOutboxJSONB|TestObservationJSONB'`.
- PASS: `go test -count=1 ./internal/infra/metering/journalstore/`.
- PASS: `go test -count=1 ./pkg/lipsdk/metering/ ./internal/core/metering/... ./internal/infra/billingstore/...`.
- PASS: `make test-db-parity-sqlite`.
- PASS (DSN present and `LIP_REQUIRE_POSTGRES=1`):
  `go test -tags=integration -count=1 ./internal/infra/metering/journalstore/ -run '^TestPostgresObservationOutbox_JSONBExactReplayIsIdempotent$'`.
- EXPECTED unrelated baseline failure: bounded live PG
  `go test -tags=integration -count=1 ./internal/infra/metering/journalstore/ -run 'Postgres'`
  fails only `TestDBParity_PostgresDirect*` because shared
  `metering_components.value_present` is `int4` while the migration/test
  expects boolean. The JSONB-focused test passes and this fix does not touch
  that schema or component insert path.
- PASS: `go vet ./internal/infra/metering/journalstore/...`.
- PASS: `gofmt -d` on the two implementation files and three focused test files
  produced no diff; trailing-whitespace and placeholder/secret scans are clean.
- ENVIRONMENTAL (no tests ran): `go test -race -count=1 ./internal/infra/metering/journalstore/ -run 'TestObservationFactJSONB|TestObservationOutboxJSONB|TestObservationJSONB'`
  cannot build because the Windows toolchain `cgo.exe` exits status 2.
- `git diff --check` was not rerun in this re-review because the parent
  instruction prohibits Git/status actions; the execution evidence reports it
  clean.

## Residual risks

- Race-detector evidence remains unavailable on this Windows toolchain and
  should be rerun in a working toolchain/CI environment.
- The shared PostgreSQL instance retains the independent `value_present`
  int4/boolean drift, so component-bearing live PG observations remain outside
  this fix's usable certification path.
- No timing-controlled test forces the actual concurrent post-conflict winner
  window; the source path is explicitly validated and deterministic winner
  envelope tests exercise its shared validator.
