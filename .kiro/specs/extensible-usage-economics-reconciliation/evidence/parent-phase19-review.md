# Phase 19 final re-review

- VERDICT: APPROVED
- TASK: 19.1, 19.2, 19.3 and parent 19
- Authorization: check all four task checkboxes and start Phase 20. This is Phase 19 acceptance, not release-wide QA or Windows cost-ratchet approval.
- Reviewed source: `feat/b-leg-usage-economics`, baseline `fe73f55baa57801be40a62cffedb2fe70d6327f7`, current tracked binary-diff hash `c717f63a2ae44db31b045f2ca9e9dabbad8829c4`, plus the new Phase 19 files. Earlier rejected reviews below are historical and superseded.

## Final disposition

Both remaining provenance blockers are resolved. Retail source selection now uses an explicit component mask over immutable original observations; reduction retains original correction/supersession references. The mixed provider-output/local-output-plus-image case bills output once and image once (6.10), and actual journal lookups resolve the valuation and line references to their original full payloads. The linked correction case also resolves and preserves its predecessor hash. `RateCustomerPolicyObservation` validates the original supplied reference set and input-set hash before selection; wrong hashes and conflicting reference payload hashes produce the typed mismatch error.

The previously accepted claim fairness, PostgreSQL migration/index parity, lifecycle retail 8.44, chunk-partition invariance, tolerance, and metadata-boundary remediations remain intact. The traceability matrix is supporting navigation, not a substitute for the inspected assertions and integrated production-boundary tests. No remaining concrete Phase 19 blocker was found.

## Fresh final-tree verification

- `go test -count=1 -run 'Phase19|Phase191|Phase192|Phase193'` across core billing, core metering, runtime, billingstore, SDK metering and metering journalstore: PASS, including provenance, lifecycle, deterministic claim fairness and traceability guards.
- Exact `TestCleanupStatementsJournalUsesStoreScopedFilters` and `TestAssertNoSearchPathInDualPlanePostgresSources`: PASS. Inspected ordered five-table store-scoped cleanup assertions and shared-schema PostgreSQL upgrade test; no test exclusion or weakened predicate.
- Full `go test -count=1 ./internal/core/billing ./internal/infra/metering/journalstore ./internal/testkit`: PASS (0.401s, 1.653s, 1.371s).
- Required live PostgreSQL direct metering parity: PASS (3.778s). Explicit int4-to-boolean upgrade integration test: PASS (5.675s).
- Real pooler rerun with explicit hostname-derived pooler DSN, pooler attestation and requirement enabled: `go test -tags=integration -count=1 -run 'TestPhase34_PostgresPooled|TestPostgresPooled_' ./internal/infra/metering/journalstore`: PASS (29.595s). This supplements the previous verbose independent run confirming all index-negative and concurrency contracts execute without skips.
- WSL `UbuntuOld`, Go 1.26.6 linux/amd64, CGO enabled: fresh `go test -race -count=1 -run 'TestPhase19R1|TestPhase19R2|TestSQLiteClaimCompleteCallsYieldsLargeIncompletePrefix'` across core billing, billingstore and journalstore: PASS (1.215s, 10.390s, 1.312s), no race report.
- Fresh Windows `-benchmem -benchtime=20x -count=2` accounting baseline, 1/5 MiB canonical paths and terminal spool benchmarks: PASS. Disabled accounting reports zero terminal calls/legs/observations, 768-769 allocations in the baseline; enabled reports exactly one of each, 880 allocations. At 5 MiB, enabled allocation bytes remain about 19 KiB above disabled, rather than scaling accounting retention with body size. Terminal append/deliver counts remain bounded and drain to zero. Short benchmark counts are confirmation, not a new statistical latency claim.
- Scoped `go vet`, all changed/new Go-file `gofmt -l`, and `git diff --check`: clean.

## Windows cost and residual gate attribution

Inspected retained raw corrected-run logs under `C:/Users/Mateusz/AppData/Local/Temp/opencode/p193-final-fix-testcost-run1`, the measurement JSON and orchestration console. The console identifies measured snapshot `a335dff40905f8bcefec45a208315a341bc7f8d6`; the current tracked source diff independently matches the recorded `c717f63a...` identity. The throwaway snapshot checkout was already deleted, so its original untracked-file byte comparison is retained implementer evidence, not a comparison repeated by this reviewer. Current new source files were inspected and exercised directly.

The authoritative cost command did not complete a green ratchet: head test-unit exited nonzero. Its raw failure keys contain the documented 16 archtest tests, two QA tests and one runtimebundle eventual-condition timeout, with neither former testkit failure. Independently reran archtest and QA in the retained clean pristine `fe73f55b` checkout (verified HEAD and empty status): exactly those 16 and two baseline tests fail there too. These include architecture/LOC/baseline-policy failures, not merely timing-budget failures; they remain explicit Phase 20 work, not a waived gate. The runtimebundle timeout is the existing two-second polling assertion for a late correction; fresh isolation with `-count=3` passed (2.888s). Retained loaded failure plus isolated success does not prove absence of all load sensitivity, and Phase 20 must retain this risk.

Thus section 12.6 supersedes the earlier task-owned testkit failures honestly; no new deterministic task-owned cost-gate failure remains. Full Windows ratchet and release-wide QA are still red/not certified and must be completed in their assigned downstream scope. No budgets, allow flags or verification hooks were relaxed by this review.

Reviewer edited only this report; no source, tests, task status, commits or PR state changed.

## Previous remediation re-review (historical)

- VERDICT: REJECTED
- TASK: 19.1, 19.2, 19.3 and parent 19
- Scope: current uncommitted remediation over `fe73f55b`.
- Phase 20 is not authorized by this review. The original findings below are historical; the remaining blockers are the two concrete regressions introduced by the R1 repair described here.

## Remaining blockers

### R1-A. Important: partial source selection creates references to observations that were never persisted

`internal/core/billing/retail_rating.go:752` removes selected measures from a cloned observation while retaining its original observation ID and revision. `:137-152` then calls `Observation.Ref` on that modified payload. `pkg/lipsdk/metering/observation.go:1125-1129` includes the entire replay payload hash in the reference. Consequently the reference is not the reference of the immutable observation in the journal.

Concrete reachable input: one provider observation has output-token quantity 200; the local observation of the same B-leg contains that same output-token component plus a local-only image component. Source selection correctly retains the provider output and the local-only image, but it changes the local observation from two measures to one and recomputes its hash under the same ID/revision. Retail valuation input/line references now identify this unpersisted one-measure version. `journalstore.GetObservationRef` (`observation_store.go:815-819`) loads the original two-measure record and rejects the new reference with `ErrIdentityCollision` / `observation payload hash mismatch`. Existing R1 tests discard the entire local observation, so they do not exercise this partial-retention case.

This breaks the immutable source linkage required by requirements 2.6, 7.1, 10.1 and 11.2. It can also break supersession links that still reference the original hash.

Minimal remediation: perform quantity/source selection on the reduced component projection or an explicit selection mask, retaining the original immutable observation payloads and references. Do not rewrite payloads under existing observation identities. Add a mixed-component regression that persists both original observations, rates the selected components once, and resolves every emitted input/line reference against the original journal successfully. Cover a linked correction as well.

### R1-B. Important: incremental customer rating silently discards an invalid supplied input-set identity

`internal/core/billing/component_rater.go:149-152` clears `ObservationRefs` and `InputSetHash` before the input is validated. This happens whenever observations are present, even with a single provider observation and no competing local channel. Previously `rateRetailValuation` reached `verifyInputSetHash`; `component_rating_contract.go:161-162` explicitly returns `ErrInputSetHashMismatch` when the caller's supplied hash differs from the canonical input set. The new wrapper erases that supplied hash, so a valid observation with a wrong nonempty input-set hash is now rated successfully instead of rejected.

Minimal remediation: validate the original envelope and verify its supplied references/input-set hash before deriving the selected rating view. Derive a separate selected identity only after this verification. Add a regression through `RateCustomerPolicyObservation` using a valid one-observation input and a deliberately mismatched supplied hash; require the typed mismatch error. Include conflicting supplied reference payload hashes so filtering cannot erase integrity failures.

## Remediation accepted and fresh verification

- Original duplicate-output example is fixed: all new Phase 19 focused tests pass, including the lifecycle R=8.44 expectation while both evidence channels remain present. This does not resolve the partial-component reference case above.
- Original claim-prefix SQL deadline rewrite is removed. The durable `(next_claim_at, sealed_at, call_id)` ordering and deterministic clock tests establish progress beyond the expired one-second window. The retained `phase19-r2-claim-prefix-red.txt` shows the new deterministic test failing on the old order.
- Metering-journal PostgreSQL direct parity freshly passed: `go run ./internal/testkit/dbparity/cmd postgres-direct -component metering-journal`, exit 0, 3.695s.
- Explicitly attested real pooler rerun freshly passed with `search_path=public`, `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1` and `LIP_REQUIRE_POSTGRES_POOLER=1`: `go test -tags=integration -v -count=1 -run 'TestPhase34_PostgresPooled|TestPostgresPooled_' ./internal/infra/metering/journalstore`, exit 0, 28.298s. All 13 index-negative subtests and the concurrent/replay/DML-after-admin-close contracts ran; no skips. No credentials were printed.
- The new chunk-invariance, versioned tolerance, independent serialization-cap and supersedes-cap tests contain the relevant behavioral assertions and pass. The traceability guard passes, although its whole-file marker searches are a structural guard, not independent semantic proof of every mapped requirement.
- Fresh full affected packages passed with `go test -count=1`: core billing (0.461s), core metering (0.045s), runtime (7.182s), billingstore (164.769s), metering journalstore (3.234s), SDK metering (0.085s).
- Fresh focused `go test -count=1 -run 'Phase19|Phase191|Phase192|Phase193'` across those six packages passed.
- Fresh scoped `go vet`, changed/new Go-file `gofmt -l`, and `git diff --check` passed.
- Supported WSL environment checked live: `UbuntuOld`, Go 1.26.6 linux/amd64, CGO_ENABLED=1. Fresh `go test -race -count=1 -run 'TestPhase19R1|TestPhase19R2|TestSQLiteClaimCompleteCallsYieldsLargeIncompletePrefix' ./internal/core/billing ./internal/infra/billingstore` passed: core billing 1.246s, billingstore 18.895s, exit 0, no race report.
- The retained Windows test-cost measurements still identify snapshot `c7c619ab`, predating the remediation changes. They remain historical measurements, not a completed final-tree certification. Refresh the affected evidence after the remaining fixes; this review did not rerun the full Windows cost ratchet or release-wide QA while the integrity blockers remain.
- Reviewer changed only this report; no source, tests, task status, commits or PR state were modified.

## Original review (historical)

- VERDICT: REJECTED
- TASK: 19.1, 19.2, 19.3 and parent 19
- Reviewed baseline: `fe73f55baa57801be40a62cffedb2fe70d6327f7`, with the uncommitted Phase 19 changes.
- Do not check these tasks or authorize Phase 20 on this certification.

## Blocking findings

### 1. Critical: the new lifecycle test certifies two inference charges for competing measurements of the same work

`internal/infra/billingstore/phase19_1_lifecycle_certification_test.go:606-610` explicitly expects retail R = 7.44 provider-derived usage + 7.20 independently measured usage + 1 submission fee = 15.64. The winner contains both observations of the same B-leg. Both report the same 200 output tokens; the one output-token tariff is consequently charged twice. The fixture policy selects the surfaced winner and independent retail, but declares no separate proxy-service charge or commercial rule authorizing the addition of competing source measurements. Retaining both observations for reconciliation is not sufficient authority to add both to customer inference usage.

This is an actual executed result: the fresh `TestPhase191LifecycleFullEconomicsCertification` passed with that 15.64 expectation. The source path is `RateSelectedRetailBLegs` -> `selectedRetailObservations` -> `retailQuantityGroups` -> `rateRetailValuation`; the fixture supplies both source observations to the winner. Requirements 1.1/1.4 and 8.1, plus design C3's explicitly selected inference quantities and separately declared service charges, require preserving evidence without turning an additional measurement channel into another inference charge.

Remediation: make the frozen retail quantity/source selection explicit, preserve competing evidence for E/Q/P and reconciliation, and add a RED regression proving that attaching the independent measurement of already represented output cannot double the ordinary inference charge. Update this integrated expectation to the selected contractual quantity basis. Do not merely delete one source from the integrated fixture.

### 2. Important: the claim-prefix test now manufactures the scheduler state needed to pass

`internal/infra/billingstore/call_usage_store_test.go:146-147` rewrites the production-created `next_claim_at` values to one hour in the future between the two claims. The production code fixes `now` once at the start of `ClaimCompleteCalls` (`call_usage_store.go:300`) and gives every incomplete row a deadline of that same start time plus one second (`:413`). Each invocation resets its pagination cursor. When a bounded scan exceeds one second, the next invocation can revisit the same earliest 256 incomplete rows indefinitely and never reach the complete call behind them.

The retained RED 1/2 evidence already demonstrates the failing scan under race instrumentation. Extending those deadlines by direct SQL removes the condition responsible for that failure; it does not establish production forward progress. This undermines the task's restart/concurrency certification even though the edited test now passes.

Remediation: retain an executable regression for scan durations exceeding the yield window, make bounded production scanning progress independently of that timing assumption, and remove the post-scan SQL rewrite. Use a deterministic clock/hook or another bounded deterministic test arrangement rather than a long sleep.

### 3. Important: a mandatory persistence component remains uncertified, and pooled failure attribution is inaccurate

Fresh `go run ./internal/testkit/dbparity/cmd postgres-direct -component metering-journal` exits 1: `metering_components.value_present` is `integer`/`int4`, while the catalog expects boolean. I reproduced the identical failure in the clean retained `p193-pristine-fe73f55b` checkout and verified its HEAD and clean status. It is pre-existing, but this component implements the observation persistence boundary being certified by task 19.2 and requirement 18.4. It is not an unrelated release-only architecture budget.

The pooled raw log `%TEMP%/opencode/ph192-journalstore-pooled.log` also does not support the evidence document's implication that the failures are all the direct int4/boolean mismatch. It reports `verify postgres metering_components subject index: missing`, and the negative index test also fails to drop a constraint-backed index with SQLSTATE 2BP01. These failures prevent the pooled financial contracts from executing successfully. A fresh pooled probe in this review skipped because this process lacks explicit topology attestation; that skip is not passing evidence.

Remediation: resolve the schema/catalog/index contract mismatches and rerun the canonical metering-journal direct and supported pooled contracts. Correct the evidence to distinguish the actual failure causes. Unrelated baseline lint/LOC/QA ratchets can remain for Phase 20; failed task-owned persistence certification cannot.

### 4. Important: matrix completeness is numeric, but several mappings do not prove their requirement

The TSV has 124 data rows, but concrete entries are mismatched:

- Row 49 / requirement 4.5 maps chunk invariance to `TestWireComposition_PreflightInexactTokenizerSemantics_DynamicAssessmentDeclinesUnderSamePermit`. Its body (`wire_metering_composition_test.go:677`) supplies one stub wire source and checks preflight decline. It never partitions a stream or compares whole-output versus per-chunk measurements.
- Row 96 / requirement 12.4 maps versioned absolute/relative tolerances and zero denominators solely to `TestPhase191LifecycleFullEconomicsCertification`. That test checks +2 audio discrepancy, incomparable token counts and monetary differences; it does not exercise versioned tolerances or a zero denominator.
- The 19.3 metadata evidence says six over-limit variants and certification of the normalized 64 KiB cap, but `TestPhase193BoundedObservationOverLimitFailsClosed` contains five variants, none exceeding the serialization cap independently of the entry/dimension limits.

Remediation: map these criteria to actual named passing tests that assert the required behavior, or add the missing bounded tests. Audit the rest of the matrix by assertions rather than declaration existence, and correct the metadata evidence's count and cap claim.

## Mechanical verification and limits

- PASS, exit 0: `go test -count=1 -run 'TestPhase19' ./internal/infra/billingstore ./internal/core/runtime ./pkg/lipsdk/metering` (billingstore 6.070s).
- PASS, exit 0: complete runtime, SDK metering, billingspool and core billing packages with `go test -count=1`.
- PASS, exit 0: focused two Defer concurrency regressions and edited claim-prefix test. The latter passing result has the limitation in finding 2.
- PASS, exit 0: `BenchmarkPhase193MultiMiBCanonicalAccounting`, `-benchmem -benchtime=20x -count=2`. Disabled terminal metrics are all zero; enabled metrics are one leg observer, one leg append and one call append. Allocation-count delta is approximately constant across 1 MiB and 5 MiB; byte counts remain noisy.
- PASS: scoped `go vet` for runtime/billingstore/billingspool/SDK metering, `gofmt -l` on changed/new Go files, and `git diff --check`.
- Placeholder scan of changed/new Go files: no TODO/TBD/FIXME/HACK/XXX matches. No concrete hardcoded-secret issue observed in the inspected diff.
- FAIL, exit 1: fresh metering-journal PostgreSQL direct parity in both candidate and pristine baseline, as above.
- SKIP: fresh explicitly named pooled test, verbose output confirms missing topology attestation.
- Inspected retained full billingstore race output (1134.694s), runtimebundle race output (157.868s), RED evidence, pooled failure log and raw multi-MiB benchmark output. These retained logs do not replace the failed persistence certification or repair the masked scan regression.
- A full fresh `make test-unit`, `make qa`, WSL race rerun and third Windows `make test-cost` were not run after concrete blockers were established. The existing test-cost runs abort before complete head cost measurement. Their 295s billingstore time versus 19s anchor is not accepted here as proof of either a Phase 19 regression or a safe baseline deferral; additional matched attribution is needed before making that claim.
- No source, test, task-status, Git history or PR changes were made by this reviewer. Only this review report was added.

Summary: useful focused coverage and small performance fixes are present, but the current evidence certifies a duplicated retail inference charge, masks scan starvation, leaves required PostgreSQL/pooler contracts red, and contains assertion-level traceability gaps.
