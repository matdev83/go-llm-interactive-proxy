# Phase 3 execution evidence

- Scope: parent tasks 3.1-3.4 (pure normalization, source-scoped V2 reduction, replay/correction integrity, reusable metering TCK).
- Worktree/branch: `go-llm-interactive-proxy-feat-b-leg-usage-economics` / `feat/b-leg-usage-economics`.
- Baseline: `2bd80f25`.
- Measurement window: `2026-09-12T21:21:02.7307853Z` to `2026-09-12T21:28:35.8697196Z` UTC.
- Owned Go files: 8 at start and 8 at end (six domain/replay files and two TCK files; tests included).

## TDD RED

Before production files existed, the focused command
`go test -count=1 ./internal/core/metering/normalize ./internal/core/metering/aggregate`
failed as intended: normalize reported no non-test Go files, while aggregate behavior-first tests reported missing `ApplyObservations`, `ApplyFacts`, `ErrAmbiguousCumulative` and `ErrIdentityConflict` entry points. This was a compile-time RED with the behavioral assertions already written; final acceptance comes from those assertions passing after implementation, not from claiming an observed assertion-level RED.

## Acceptance coverage

| Acceptance | Executable coverage |
| --- | --- |
| Inclusive 1000/read 600/write 100 => uncached 300; separate input 300; missing/negative partitions; cache lifetime and reasoning separation; synthetic 11.6 oracle | `normalize.TestNormalize_InclusiveInputUsesExactDisjointPartition`, `TestNormalize_SeparateInputDoesNotSubtractCaches`, `TestNormalize_UnknownOperandAndNegativeResidualNeverInventZero`, `TestNormalize_CacheLifetimesAndReasoningStayDisjoint`; TCK `inclusive_partition_and_source_evidence` |
| Exact V2 component keys, native media direction/unit/qualifiers, independent origin/acquisition/subject/stream/charge scopes, versioned mapping/evidence | `aggregate.TestApplyObservations_ReducesByFullKeyAndIndependentSourceScope`; TCK `multimodal_native_directions_and_units`; normalizer evidence/mapping assertions |
| Delta add, present-field cumulative/replacement, gauge, correction, resource values, absent-field preservation and immutable prior evidence | `aggregate.TestApplyObservations_DeltaCumulativeGaugeAndResourceSemantics`, `TestApplyObservations_ReplacementAndCorrectionKeepUnrelatedComponents`, `TestApplyObservations_CorrectionReplacesEffectiveChargeAndRetainsPriorEvidence`, `TestApplyFacts_PreservesLegacySignedMoneyCorrectionSemantics`; existing V1 correction/replacement tests |
| Source-event/revision identity, full-payload replay conflict, receipt retry no-op, quantity non-dedup, declared ordering and ambiguous cumulative rejection | `replay.TestDeduplicate_ReceiptRetryIsNoopButChangedPayloadConflicts`, `TestDeduplicate_QuantityEqualityDoesNotDeduplicateDistinctEvents`, `TestDeduplicate_IdentityRetainsIndependentOriginAndAcquisition`; aggregate replay/ambiguity test |
| Coverage/supersession graph validation, pending unresolved references, no charge recomputation and trusted V1 bridge | `aggregate.TestApplyObservations_RejectsAmbiguousCumulativeAndPreservesPendingCoverage`, `TestApplyFacts_UsesV2ReductionWithoutChangingV1FactIdentity`, correction charge test; Phase 2 SDK graph/TCK tests |
| Reusable family TCK with image/audio/video/resource, qualifiers, unknown bounded evidence, presence and aggregate coverage | `internal/testkit/contract/metering.TestTCK_ActualNormalizerAndReducer` and its three subtests |

## Final verification

- `go test -count=1 ./internal/core/metering/... ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/testkit/contract/metering` — PASS (including the V1 signed-money bridge regression).
- `go test -count=1 -v ./internal/testkit/contract/metering ./internal/core/metering/normalize ./internal/core/metering/replay ./internal/core/metering/aggregate` — PASS; all listed TCK, normalization, replay and aggregate cases passed.
- `go test -count=1 -shuffle=on ./internal/core/metering/normalize ./internal/core/metering/replay ./internal/core/metering/aggregate ./internal/testkit/contract/metering` — PASS.
- `go vet ./internal/core/metering/normalize ./internal/core/metering/replay ./internal/core/metering/aggregate ./internal/testkit/contract/metering ./pkg/lipsdk/metering ./pkg/lipsdk/economics` — PASS.
- `gofmt -d` over all eight owned Go files — no output.
- `git diff --check` — PASS for tracked content; untracked files were checked by gofmt and tests.

## Interruption and residual risk

- The prior worker turn ended with an infrastructure stream-disconnect/model-access error. It was an orchestration interruption, not an implementation or test failure; work resumed in the same worktree without reset/revert.
- Full-root, database, cost, and race gates were intentionally not run; this phase is pure domain/TCK work and the execution brief deferred those gates.
- No runtime, provider adapter, SQL, rating engine or PR changes were made. Root review is APPROVED; see `phase3-review.md`.
