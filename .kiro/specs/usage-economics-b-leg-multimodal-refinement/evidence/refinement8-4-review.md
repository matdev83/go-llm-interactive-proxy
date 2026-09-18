# Independent Task 8.4 review

## Review target and boundary

- Task: refinement Task 8.4, `Reconcile parent task evidence and final release traceability`.
- Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`.
- HEAD: `a15f6ae5655b026f6606b7e0114935620a3ddd61` (`a15f6ae5`), committed 2026-09-18.
- The two submitted documents explicitly identify this SHA/date as the inventory base and explicitly state that it is not an accepted release candidate. The same-future-RC rule requires every parent/refinement criterion and all required gates to pass on one later recorded SHA, with refinement Task 5.3 resolved.
- The initial review inventory contained exactly the two submitted documents and no tracked diff. This re-review changes only this review evidence; no implementation, test, spec, task, status, branch, stash, or history file is changed.

## Traceability and source reality checks

- Parent-Spec Amendments: 7 rows, matching the seven amendments in refinement `design.md`. Each row retains parent ownership, refined meaning, concrete symbols, evidence, disposition, and the declared refinement-only precedence; parent history is not rewritten.
- Refinement criteria: 36 rows, 36 unique IDs, no missing IDs, no duplicates, and zero malformed matrix rows. The status counts are 31 `PASS`, 2 `BLOCKED` (2.2 and 2.6), and 3 `PENDING` (3.6, 4.6, and 6.6).
- Exact named-symbol scan over both Task 8.4 documents found 47 distinct `Test...` symbols; 47/47 have an exact `func Test...` definition at this HEAD. Source bodies were spot-checked across every requirement group, including the direction/unit contracts, provider-bound/provider-origin transforms, B-leg authority guard, resource allocation, durable valuation/journal, retail selection, resumed-call ownership, pre-terminal revisions, and integrated COGS/retail persistence.
- Fresh focused executions of the cited matrix groups passed for `pkg/lipsdk/metering`, `internal/core/billing`, `internal/core/runtime`, `internal/infra/billingstore`, `internal/infra/metering/journalstore`, and `internal/infra/runtimebundle`. The direct authority test `TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject` also passed. The shared Postgres-specific refinement test was not forced independently because the repository parity gate reproduces the known shared-database schema blocker; its historical evidence remains correctly limited to that test's recorded run.
- The two blocked rows are not false passes: their only current authority test proves absence of authoritative A-leg subjects; the projection/report behavior is explicitly owned by blocked refinement Task 5.3. The three pending rows are not false passes: no dedicated TTL/retirement test exists for 3.6, no dedicated bounded-cost certification was run for 4.6, and no gate assertion exists for 6.6.

## Parent/refinement state and contradiction audit

- Parent checkbox inventory is 96 lines: 53 checked and 43 unchecked. Parent top-level Tasks 1 and 12-20 remain unchecked; the traceability documents do not mark any of them complete.
- Refinement Task 5.3 remains unchecked and explicitly `BLOCKED`; refinement Tasks 8.1-8.3 are checked; Task 8.4 remains unchecked in `tasks.md` because this review does not mutate task status.
- The parent requirements/design/tasks text consistently treats request inference as B-leg-rooted, A-leg/session state as continuity/projection rather than authority or finality, closure as B-leg/call-local, customer-boundary measurements as separately declared service tariffs, and resource/account economics as real non-B-leg subjects with explicit allocation. The scans found no contradictory parent wording requiring A-leg inference authority/finality, session-final settlement, customer-boundary inference as the default, merged multimodal units, or synthetic B-legs for resource costs. The refinement precedence rule is therefore correctly stated as a scoped rule for future conflicts, not a history rewrite.

## Gate verification retained from the initial review

The following commands were run independently at the stated HEAD during the initial review. The corrected documents now match those results. All failures below are in tracked pre-existing implementation/guard files; the Task 8.4 change is documentation-only. `make test-race` is the documented Windows skip.

| Command | Fresh result | Causal observation |
|---|---|---|
| `make parity-checks` | PASS, exit 0 | Contract, conformance, provider-profile, connector, and parity suites completed successfully. |
| `make quality-checks` | FAIL, exit 1 | Formatting/modules/build/vet and regex guard passed, but parallel `quality-checks:adhoc-goroutines` failed on tracked `internal/core/billing/economic_revision_worker.go` and `internal/infra/runtimebundle/observation_economic_bridge.go`; `quality-checks:lint` failed with 229 tracked issues; archtest also reproduced the baseline ratchets. |
| `go test ./internal/archtest -count=1 -run 'TestRequestAttemptStateTargetRatchetFailsIfTypeReappearsOnCurrentAST|TestBillingCoreStaysProviderAndPersistenceFree|TestPhase51AttemptSequenceAuthorityRatchets|TestCompactionContinuitySecurity_ContentFreePublicSurfaces|TestPackageTreeBudgetsExact|TestShrinkage_NetReductionMeetsRequirement115'` | FAIL, exit 1 | Reproduced the recorded direct-field-copy, package-budget/shrinkage, content-free-surface, provider-neutral-import, and attempt-sequence baseline failures. |
| `make test` | FAIL, exit 1 | `quality-checks-fast` stopped before `test-unit`; the same parallel adhoc-goroutine and 229-issue lint baseline failures were present. |
| `make test-db-parity` | FAIL, exit 1 | SQLite components passed; PostgreSQL failed at `TestDBParity_PostgresDirect/MigrationAndSchemaParity`: `metering_components.value_present` got `int4` but expected boolean. |
| `make qa` | FAIL, exit 1 | Stopped at the overall `quality-checks-fast` step; its parallel adhoc-goroutine and lint failures prevented later QA stages. |
| `make test-race` | PASS/skip, exit 0 | Windows reports race evidence unsupported; Linux CI remains required. |

## Re-review of the prior blocker

The prior evidence blocker is resolved in both corrected documents:

1. `refinement8-4-execution.md:142` now lists `adhoc-goroutines`, lint (229 issues), and archtest together; `:143` now states that `make test` stops before `test-unit` on the parallel adhoc/lint failures.
2. `refinement8-4-execution.md:146` now states that QA stops at `quality-checks-fast` and that adhoc/lint are parallel, with neither uniquely first. The parent-side summary at `usage-economics-b-leg-multimodal-refinement-traceability.md:42-56` agrees.
3. The gate table now uses canonical direct exit codes 0/1. The explanatory `:149-152` exit-2 reference is explicitly historical worker-capture context and states that canonical exit 1 is authoritative; no stale exit-2 gate value remains.
4. The DB parity row (`refinement8-4-execution.md:145`) still records the reproduced SQLite-pass/PostgreSQL `value_present` int4-vs-boolean baseline, and the test-race row (`:147`) accurately records the Windows skip. Release disposition and the explicit not-a-green-RC rule are unchanged.

## Mechanical scans

- Amendment rows: 7; matrix rows: 36; matrix columns: valid for all rows.
- Criterion validator: `CRITERIA_TOTAL=36`, `CRITERIA_UNIQUE=36`, `CRITERIA_MISSING=0`, `CRITERIA_DUPLICATES=0`, `STATUS_PASS=31`, `STATUS_BLOCKED=2`, `STATUS_PENDING=3`.
- Exact named-test validator: `NAMED_TEST_SYMBOLS=47`, `NAMED_TEST_FOUND=47`, `NAMED_TEST_MISSING=0`.
- `git diff --check` on tracked content: clean. `git diff --no-index --check` against each untracked submitted document emitted no whitespace diagnostics (exit 1 is only the expected no-index difference result).
- Marker scan for `TODO|TBD|FIXME|HACK|XXX`: clean. Narrow concrete secret-pattern scan: clean.
- Pre-review boundary: exactly the two submitted documents were untracked; no implementation/spec/task/status spillover was present.

## Review verdict

Task 8.4's traceability structure and corrected gate evidence satisfy the review boundary. The gate mechanism is approved; the overall #620/refinement release disposition remains blocked/pending.

VERDICT: APPROVED
BLOCKERS: none
TESTS: Corrected gate records match the independently reproduced canonical outcomes: parity-checks PASS; focused matrix certification groups PASS; authority guard PASS; quality-checks, focused archtest, make test, test-db-parity, and qa reproduce documented baseline failures; test-race Windows skip; traceability/symbol/markdown/marker/secret scans clean.
FILES_WRITTEN: .kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement8-4-review.md
RELEASE_DISPOSITION: BLOCKED/PENDING
RESIDUAL_RISKS: Parent Tasks 1 and 12-20 remain incomplete; refinement Task 5.3 remains blocked; criteria 3.6, 4.6, and 6.6 remain pending; Windows race evidence is unavailable; the repository quality and PostgreSQL parity baselines remain unresolved. This approval is for the Task 8.4 gate mechanism only, not #620 release acceptance.
