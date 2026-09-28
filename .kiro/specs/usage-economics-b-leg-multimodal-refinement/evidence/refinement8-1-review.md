# Task 8.1 re-review

Status: APPROVED

This is the remediation-round-1 re-review of exactly Task 8.1 in the shared
`feat/b-leg-usage-economics` worktree. The review changed only this evidence
file. The three certification files and the execution evidence remain
test/evidence-only; no production file, Task 5.3 artifact, stash, task
checkbox, spec status, or Git state was changed.

## Scope and source of truth

- Requirements 6.1 and 6.2 in `requirements.md:116-124` require all
  image/audio/video/document directions, provider-native units, direction
  pricing, and one transform in each direction.
- `tasks.md:220-226` additionally requires a durable round-trip and
  independently provable direction-specific rates.
- `design.md:256-277` defines the evidence lifecycle as B-leg to durable
  journal to economic worker to financial store. `design.md:289-301` requires
  the B-leg accumulator to emit durable observations through that journal /
  durability family.
- The execution evidence's stated nonblank counts are accurate: the files
  have total/nonblank line counts 696/668 (billing), 332/315 (journalstore),
  and 445/421 (billingstore). The apparent count discrepancy is only
  total-versus-nonblank counting.

## Mechanical results

Fresh focused runs, each with `-count=1`, passed twice:

- `go test ./internal/core/billing/ -run '^TestRefinement81' -count=1`:
  PASS (6 tests and 28 subtests).
- `go test ./internal/infra/metering/journalstore/ -run '^TestRefinement81' -count=1`:
  PASS (8 modality-direction subtests, 16 observations).
- `go test ./internal/infra/billingstore/ -run '^TestRefinement81' -count=1`:
  PASS (8 modality-direction subtests, 16 valuations).
- `go test ./internal/core/billing/ ./pkg/lipsdk/metering/ ./pkg/lipsdk/economics/ ./internal/infra/metering/journalstore/ ./internal/infra/billingstore/ -count=1`:
  PASS.
- `make parity-checks`: PASS, exit 0.
- `make test-db-parity-sqlite`: PASS, exit 0, including both touched stores.
- `go vet ./internal/core/billing/ ./internal/infra/metering/journalstore/ ./internal/infra/billingstore/`:
  PASS.
- `gofmt -l` over all three certification files: clean.
- Placeholder-marker scan over all five task/evidence files: CLEAN.
- Credential-pattern scan over all five task/evidence files: CLEAN.

The required broad gates still have the same unrelated baseline failures:

- `make test-unit`: FAIL, exit 1, on the pre-existing archtest ratchets and
  boundaries: request-attempt direct-field-copy 370 over 323, runtimebundle
  package/connector budget 13131 over 12567, internal/core budget 115814 over
  98581, process_services.go 342 over 341, billing's pre-existing lipapi
  import, compaction surface, attempt-sequence, shrinkage, and hexagonal
  baseline checks. All task-local packages pass. The budget scanner excludes
  `_test.go` files (`internal/archtest/budgets.go:191-203`), and no production
  file changed, so the three certification files cannot cause these failures.
- `make test-db-parity`: FAIL, exit 1, at the existing PostgreSQL-direct
  `metering_components.value_present` integer/int4 versus boolean mismatch in
  `internal/infra/metering/journalstore/dbparity_postgres_test.go:44`.
  No migration or production store file changed. Both store components were
  already present in `dbparity.DefaultCatalog()` (`catalog.go:72` and
  `catalog.go:273`), and SQLite parity passes.
- `go test -race ./internal/core/billing/ ./internal/infra/metering/journalstore/ ./internal/infra/billingstore/ -run '^TestRefinement81' -count=1`:
  unavailable because the local Windows `cgo.exe` toolchain exits 2 before
  package tests build; this is an environment limitation.

The RED equivalent remains VERIFIED: the clean base `797fbd55` had no
`TestRefinement81` files or exact test names, as recorded in
`refinement8-1-execution.md:89-110`.

## Production-seam audit

### Observation durability

`internal/infra/metering/journalstore/refinement81_multimodal_observation_durability_test.go:161-177`
opens a real file-backed SQLite database, constructs the production Bun
database, and calls production `journalstore.NewDurableStore`, which runs the
real migrations (`internal/infra/metering/journalstore/durable.go:370-385`).
The test calls `AppendObservations` twice for idempotent replay
(`:224-230`), closes the store and opens a new handle over the same file
(`:232-237`), then reads through production `GetObservation`,
`ListObservations`, and `ListObservationComponents` (`:251-323`). It asserts
fingerprint, subject/call/B-leg, origin/acquisition/authority/perspective,
boundary, component direction/unit/schema/dimensions, exact decimal
coefficient/scale/presence, projection identity, and source lineage. There is
no fake store or direct SQL query in the test.

### Valuation durability and rating

`internal/infra/billingstore/refinement81_multimodal_valuation_durability_test.go:234-250`
uses production `billingstore.NewDurableStore` and migrations, while
`:338-370` rates each vector with the production
`internal/core/billing.NewReferenceRater` and `ReferenceRater.Rate`. It calls
production `AppendValuation` (`:400-405`), closes/reopens the file-backed
store (`:408-413`), and reads through `GetValuation` (`:426-440`). The
readback fingerprint and validation ensure canonical production payload
survival, while `v81AssertFullLine` (`:253-312`) checks literal rule/status,
component identity and dimensions, native unit, quantity, unit price, amount,
USD currency/total, completeness, basis/perspective, scope, subject, and
observation lineage. No local rating formula or direct database persistence is
used.

The core certification's corresponding assertions are in
`internal/core/billing/refinement81_multimodal_direction_transform_certification_test.go:224-298`
and are exercised for all eight vectors at `:304-386`. The expected amounts
are literals; the helper only inspects production output. Text-only tariffs
fail closed, so the tests do not rely on media-to-text conversion.

### Transform boundaries

The input and output transform tests use production boundary/origin constants,
the production rater, distinct provider/customer quantities, and explicit
alone-versus-both ratings (`refinement81_multimodal_direction_transform_certification_test.go:392-530`).
The supplier result is unchanged when the customer representation is removed,
and source references exclude the customer-boundary observation. The journal
test durably stores both backend and frontend boundary observations for both
transform directions (`journalstore` test `:187-250`); the valuation test
rates and durably stores both image transform valuations with the selected
backend quantity (`billingstore` test `:372-405`). No provider-specific branch,
raw media, or local selector is present.

### Quality and boundary audit

The three files contain bounded eight-row fixture tables because they run in
three package boundaries; their helpers construct production DTOs or assert
production results and do not implement a second tariff/rating algorithm.
The duplicated tables are straightforward fixtures, not hidden domain models
or shadow selectors, and the focused tests fail if the production rater,
canonical identity, migrations, append/read APIs, projections, or durable
readback behavior breaks. Existing Task 7 provider-family/connector coverage
and the parent connector TCK are covered by the passing `make parity-checks`;
Task 8.1 does not spill into that ownership boundary.

## Verdict

No blocking finding remains after remediation. The two prior Important findings
are fixed by real store close/reopen coverage and direct literal full-line
assertions. The task is approved.
