# APPROVED — Task 5.3 Cycle 3 independent re-review

## Review verdict

**APPROVED.** Cycle 3 now closes the prior review blockers with concrete, discriminating SQLite and direct-PostgreSQL evidence. Task 5.3 may proceed to final closeout, subject to the residual risks below. No production or test file was changed by this review; only this review-evidence file was updated.

## Scope and normative basis

- Worktree: `go-llm-interactive-proxy-feat-b-leg-usage-economics`
- Branch: `feat/b-leg-usage-economics`
- Approved bases: Cycle 1 `0120bdf8`, Cycle 2 `f35f867c`
- Reviewed requirements: 2.2, 2.6, 3.1, 3.3, 3.6.
- Task boundary: Task 5.3 billing query seam/reports; no production implementation or schema change is in the Cycle 3 scope.
- Current status contains the two Cycle 3 tests, the implementer certification evidence, and this review evidence. There is no tracked diff.

The approved contract is a rolling `as_of` view, not a frozen cross-query snapshot: the design requires rolling views as of a revision/time and explicitly forbids A-leg finality (design.md:315-319). Requirements 2.6 and 3.3 require query-time projections and resumed calls with immutable prior records (requirements.md:60, 68-78).

## Prior-blocker recheck

### 1. Cursor/live-keyset contract — closed

`TestALegReportCycle3OldTokenContinuationAfterAppend` at `reports_aleg_cycle3_certification_test.go:271-404` obtains a real limit-1 token, appends call C through the production terminal sink, resumes the old token, and performs a fresh walk. It asserts:

- original calls and B-leg contributions appear exactly once across page 1 plus old-token continuation, with no omissions or duplicates;
- appended call C appears at most once on the continuation and exactly once on the fresh walk;
- continuation pages carry live scope totals (`CallCount` 3 and retail 90) and fresh non-decreasing `AsOf` values;
- the fresh walk contains all calls/legs exactly once and preserves the originals’ relative order.

This matches the implementation: each query opens its own rollback-only transaction, totals are scope-wide, and call/leg keysets are reevaluated from the cursor (`reports_aleg.go:79-93, 120-165`). The tests do not impose an invented frozen snapshot requirement.

### 2. Lifecycle and full prior-call immutability — closed

`TestALegReportCycle3RealLifecycleFullDTOImmutable` at `reports_aleg_cycle3_certification_test.go:406-470` uses `billing.TerminalUsageSink`, closes call/leg records with completed/winner outcomes, settles through `AdmitExposure` and `ApplyCallBillingResult`, captures the complete canonical call DTO including B-leg lineage/contributions, idles, then resumes the same A-leg with a new `BillingCallID` and B-leg. The complete earlier call DTO remains byte-identical in business fields; `AsOf` is checked separately; rolling retail/provider totals include both calls.

The independent runtime integration test `TestRefinement82RuntimeResumeKeepsTerminalOwnership` also passed and verifies the real Executor path creates distinct production BillingCallID/B-leg identities on the same A-leg, leaves the first call/B-leg unchanged, and keeps terminal ownership local (`internal/infra/runtimebundle/refinement82_resumable_session_integration_test.go:82-105, 155-345`).

### 3. Retirement/read-only and 13-table content proof — closed

`cycle3SnapshotHashes` at `reports_aleg_cycle3_certification_test.go:54-120` hashes canonical full-row contents, not just counts, for all 13 report-read billing tables, including `billing_economic_revision_work_state`. The SQLite read-only/idle test at `:472-532` compares business DTOs, fresh monotonic `AsOf`, and all 13 hashes across baseline, three repeats, and a final query. The PostgreSQL equivalent uses deterministic `row_to_json` dumps and existence checks (`reports_aleg_cycle3_certification_pg_test.go:33-52`) and repeats the same assertions at `:256-272`.

The actual runtime retirement seam was inspected: `MemoryStore` invokes its observer only after eviction and after releasing its lock (`internal/core/b2bua/store.go:72-75, 160-173`), while the production observer wiring only calls prompt-cache `EndSession` and conversation-view `DeleteALeg` (`internal/infra/runtimebundle/compile_generation.go:208-218`); it has no billing writer. The existing runtime resume/idle test passed, and the B2BUA retirement observer tests passed. Therefore writer-silence is a verified property of the real current observer, not a hypothetical billing retirement API. The full-row hashes catch same-count UPDATE/DELETE or insert mutations in the billing report tables.

### 4. Production customer correction/replay and cross-plane reconciliation — closed

`TestALegReportCycle3LateCorrectionsAcrossPlanes` at `reports_aleg_cycle3_certification_test.go:534-719` uses the production settlement seams and `postJournalTransaction` reversal writer. It proves customer 20→12, replay identity/sequence, stale identity conflict, second-reversal loser rejection, zero durable mutation for each rejected/replayed attempt, unchanged execution row counts, and stable earlier DTO contents. The same projection proves pass-through -20/+10 adjustments, provider 30→25 plus 20 child totals, recorded-zero sibling, exact retail/provider separation, lineage, and exact-once pagination.

The direct PostgreSQL integrated test repeats this production correction/replay/stale/loser sequence and cross-plane totals at `reports_aleg_cycle3_certification_pg_test.go:198-255`, with the same 13-table hash proof and earlier DTO stability.

### 5. Direct PostgreSQL material equivalence and provider poison/bounds — closed

`TestPostgresCycle3FullEquivalenceCertification` at `reports_aleg_cycle3_certification_pg_test.go:54-272` mirrors the old-token continuation, terminal sink/resume, full prior DTO stability, customer reversal/replay fencing, pass-through positive/negative revisions, provider multi-child correction, known-zero, totals, lineage, and 13-table read-only checks. The direct PostgreSQL provider suite (including 50→9 bound, fanout, malformed/foreign lineage, duplicate subjects, poison, and zero cases) also passed. This is materially equivalent evidence, not a smoke-only PG query.

### 6. Inherited adversarial matrix and reporting semantics — closed

The full `internal/infra/billingstore` package run passed, covering inherited customer, pass-through, provider, child, cursor, malformed, padded, foreign, cross-scope, duplicate, stale, replay, overflow, pending, known-zero, and unknown fail-closed cases. The report has no A-leg finality marker; `AsOf` is independently observed as a refreshed output timestamp rather than erased as an untested snapshot field.

## Mechanical results

Passed:

- `go test -count=1 -run 'TestALegReportCycle3' ./internal/infra/billingstore/` — PASS (0.455s).
- `go test -count=5 -shuffle=on -run 'TestALegReportCycle3' ./internal/infra/billingstore/` — PASS (0.784s).
- `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` — PASS (billing, billingstore, all stdhttp packages).
- `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=1 -run 'TestPostgresCycle3FullEquivalenceCertification' ./internal/infra/billingstore/` — PASS (25.533s).
- `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=3 -run 'TestPostgresCycle3FullEquivalenceCertification' ./internal/infra/billingstore/` — PASS (74.929s).
- `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=1 -run 'TestPostgresProvider' ./internal/infra/billingstore/` — PASS (28.893s).
- `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=1 -run 'TestDBParity_PostgresDirect' ./internal/infra/billingstore/` — PASS (73.841s).
- `make test-db-parity-sqlite` — PASS (all registered components).
- `go test -count=1 -run '^TestRefinement82RuntimeResumeKeepsTerminalOwnership$' ./internal/infra/runtimebundle/` — PASS (5.362s).
- `go test -count=1 -run 'TestMemoryStore.*Retirement|TestMemoryStoreLoadAttemptsNotifiesStaleRetirement' ./internal/core/b2bua/` — PASS (0.324s).
- `go build ./...` — PASS.
- `go vet ./...` and `go vet -tags=integration ./internal/infra/billingstore/` — PASS.
- `gofmt -d` on both Cycle 3 test files — clean.
- `git diff --check` — PASS.
- Placeholder scan (`TODO/FIXME/TBD/HACK/XXX`) — CLEAN.
- Hardcoded-secret pattern scan — CLEAN.

Known out-of-scope failure:

- `LIP_REQUIRE_POSTGRES=1 make test-db-parity-postgres-direct` fails only in the pre-existing `internal/infra/metering/journalstore` component: `metering_components.value_present` is PostgreSQL `int4` while the expected category is boolean. Billingstore, and every other component reached before that failure, passed. `git status` shows no tracked changes and no metering file is in the Cycle 3 scope; this mismatch is outside Task 5.3 and was not repaired.

Unavailable with direct evidence:

- `go test -race -count=1 -run 'TestALegReportCycle3RealLifecycleFullDTOImmutable' ./internal/infra/billingstore/` cannot build because the local Windows cgo tool exits status 2 before package tests. No race result is claimed.

## Findings by severity

- **Critical:** none.
- **Important:** none. The three prior blockers are closed by fresh, discriminating evidence.
- **Suggestion:** add a future runtime TTL/eviction integration test with controllable clock if the stock host gains a testable clock seam; current production observer source is already writer-silent and the report hashes protect all 13 billing tables.
- **FYI:** Cycle 3 RED notes are honest characterization/scratch controls rather than a production defect fix; no production behavior changed in this certification-only cycle. That is appropriate for this closeout evidence, but it should not be presented as a production RED-to-GREEN implementation change.

## Boundary and residual-risk review

The Cycle 3 files remain test/evidence-only and stay within the Task 5.3 billing report boundary. No production imports, schema, runtime, provider, task-status, branch, or PR files were changed.

Residual risks are limited to unavailable Windows race verification, the stock runtime’s lack of a controllable TTL clock for a direct eviction integration test, and the unrelated repository-wide PostgreSQL parity mismatch in metering. None blocks Task 5.3 closeout under the approved rolling `as_of` contract.

## Summary

Cycle 3 is independently verified and bounded; Task 5.3 may proceed to final closeout.
