APPROVED

# Parent Phase 1 closeout review

## Verdict

- **VERDICT:** APPROVED
- **SCOPE:** Parent `extensible-usage-economics-reconciliation` Task 1 and Tasks 1.1-1.4 only.
- **REVIEWED SHA:** `613498711ec9dbb873779d94418b6347c92b0ab7d`
- **PHASE 12:** May start. This is not a feature-GO, release, cutover, or archive approval.

## Findings by severity

### Critical

None.

### Important

None. The bounded completion claims are supported by fresh evidence, and the
known repository-wide failures are reported as non-green rather than hidden.

### Suggestion

The 69-row census guard proves path/anchor/cardinality/owner/disposition
structure, not semantic exhaustiveness. Keep the census explicitly understood
as the frozen implementation-start baseline: the current implementation has
intentionally changed the historical RED anchors (for example, rating and
provider-cost behavior), while those rows retain their historical RED
characterization. A future owner-boundary or producer change should add a
reviewed census mapping rather than relying on the structural guard alone.

### FYI

The closeout's wording that there is no semantic drift is safe only as an
inventory-boundary statement. The baseline-to-HEAD source diff includes
substantial expected implementation changes; the closeout's Section 1.2
correctly documents the historical RED-to-current-GREEN transition.

## Mechanical and semantic results

- The independent census check found 69 rows, the recorded category counts,
  69 unique `path#symbol_anchor` identities, no missing paths or anchors, no
  blank owner/protocol/disposition fields, and a parent-task marker on every
  disposition.
- `TestPhase1ProducerConsumerCensusIsExactAndDispositioned` and the complete
  `^TestPhase1` guard set passed.
- The historical baseline at `d1847d5e2acdbb873779d94418b6347c92b0ab7d`
  records the intended fixed-fee, attempted-missing-evidence, and destructive
  finalizer-source RED failures. The current downstream billing/runtime
  regression selections pass, including the tests whose names retain `_RED`.
- Frozen V1 payload/sideband/posting hashes remain recorded. V1 compatibility
  tests, metering/economics/backend-plugin compatibility tests, and SQLite DB
  parity pass.
- The fresh accounting benchmark completed with disabled zero writes and
  enabled one call/leg/observation write per iteration. It records observations
  only; no unsupported throughput target is claimed.
- Scoped guards pass for provider-neutral core branches, raw-content exclusion,
  single monetary writer/no shadow posting, version markers, the public-binding
  exception, and non-money `lipruntime.Options`. No budget override or source
  gate override was used.
- The parent task diff changes exactly the Task 1 and 1.1-1.4 checkboxes. No
  production files are changed by the closeout work.

## Fresh commands and results

All commands ran from the reviewed worktree on the reviewed branch.

| Command | Result |
| --- | --- |
| `go test -count=1 ./internal/archtest -run '^TestPhase1'` | PASS |
| `go test -count=1 ./internal/core/billing/... ./internal/core/metering/...` | PASS |
| Billing selected regression regex from the closeout (`go test -count=1 -run '^(TestRefinementRetailFixedFeeIsAppliedOncePerCall_RED|TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED|TestRefinementNeverStartedEvidenceMayRemainKnownZero|TestRefinementRetailSelectionMatrixCharacterization|TestRefinementProviderCostMatrixCharacterization|TestPhase10RetailRatingUsesFrozenSelectionIndependentTariffAndSingleScopeFees|TestRefinement83DefaultRetailSelectsWinnerOnly|TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts|TestRefinement83CostPassThroughSettlesAcceptedProviderCost|TestRefinement83AuthoritiesStaySeparateAndCloneStable)$' ./internal/core/billing`) | PASS |
| Runtime selected regression regex from the closeout (`go test -count=1 -run '^(TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED|TestRefinementContinuationAfterDoneUsesFreshCallState|TestRefinement51ResumableSessionLifecycleAndSettlement|TestRefinement82RuntimeResumeKeepsTerminalOwnership|TestRefinement82RuntimePreterminalCheckpointAdvancesProvider|TestRefinement52DurableLateEvidenceKeepsClosedLegAndRevisionWorkAppendable|TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates|TestRefinement41PreTerminalCheckpointDoesNotMutateMoney|TestRefinement41PreTerminalCheckpointIsDurablyVisible)$' ./internal/core/runtime ./internal/infra/runtimebundle ./internal/infra/billingstore`) | PASS |
| `go test -count=1 -run '^TestPhase1V1' ./internal/core/billing ./pkg/lipsdk/backendplugin ./internal/infra/billingstore` | PASS |
| `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./pkg/lipsdk/backendplugin/...` | PASS |
| `make test-db-parity-sqlite` | PASS |
| `go test -count=1 -run '^TestRefinementAccountingBaselineTerminalWrites$' ./internal/core/runtime` | PASS |
| `go test -run '^$' -bench '^BenchmarkRefinementAccountingBaseline$' -benchmem -benchtime=100x -count=3 ./internal/core/runtime` | PASS; expected disabled/enabled write counters observed |
| Census PowerShell structural recheck | PASS; 69 rows, zero issues |
| `git diff --check` and closeout whitespace check | PASS |

## Non-green gates and disposition

- `make test-unit` remains non-green because of the known repository
  architecture/ratchet baseline failures; relevant billing, runtime, storage,
  and frontend packages completed.
- `make test-cost TEST_COST_PARALLEL=1` collected the Windows anchor but the
  current-head run exited 3 on the same architecture baseline plus one
  load-sensitive runtimehost test. The isolated `-count=3` retest passed.
- PostgreSQL parity and Windows race evidence are unavailable in this closeout;
  SQLite is the only database parity gate marked green.
- `make quality-checks` and full `./internal/archtest/...` remain non-green on
  the listed baseline ratchets/lint/adhoc-goroutine findings.

These are accurately retained as pending or non-green certification residuals.
They do not block this Phase 1 baseline/guardrail closeout because Task 1.3
explicitly permits unavailable external DB/Windows evidence only as pending,
never green, and Task 1.4's completion criterion is the scoped guard contract.
They must not be used as release or feature-GO evidence.

## Changed review evidence file

Created only:

`.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/parent-phase1-closeout-review.md`

The pre-existing parent Task 1 checkbox diff and untracked
`parent-phase1-closeout.md` were preserved. No production source, task text,
commit, merge, rebase, stash, reset, archive, or PR operation was performed.

## Residuals

Phase 12 may begin, but later work still owns PostgreSQL/race certification,
repository-wide quality/test-cost remediation, current implementation
performance certification, migration/cutover, and final release evidence.
