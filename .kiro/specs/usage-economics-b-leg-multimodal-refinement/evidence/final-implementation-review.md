APPROVED_IMPLEMENTATION_ARCHIVE_BLOCKED

# Final independent implementation review

Review date: 2026-09-19
Scope: `usage-economics-b-leg-multimodal-refinement`, HEAD `4b1b26c1`, branch `feat/b-leg-usage-economics`

## Signed verdict

- **Implementation verdict:** APPROVED for the refinement implementation scope. All 26 leaf tasks are checked, Task 5.3 is no longer blocked, the three Task 5.3 review cycles are approved, and the implementation-owned acceptance matrix is 35 PASS / 1 PENDING (36 unique criteria). The pending criterion is the external release/archive gate 6.6, not an unimplemented refinement leaf.
- **Requirement 4.6:** PASS is defensible from fresh executable evidence for each of its four clauses. The broad `make test-cost` failure remains a release-wide performance-gate failure, but its retained log has no 4.6 test failure and its two package-level failures are attributable to pre-existing archtest ratchets and an isolated runtimebundle load timeout, not this refinement.
- **Requirement 6.6 / archive:** BLOCKED/PENDING. The approved release rule requires the parent and refinement acceptance criteria plus the full release gates at one recorded release-candidate SHA before issue #620 can close and the spec can be archived. Parent work is incomplete, no such SHA is recorded, and this feature HEAD is not merged into `main`.

This is implementation approval only. It is not approval to mark the spec completed, close #620, or archive the spec.

## A. Implementation verdict

The implementation and review evidence support approval of the refinement itself:

- `tasks.md` contains exactly 26 leaf tasks: 26 checked, 0 unchecked. The parent Task 2 grouping remains unchecked, but its three leaf tasks are checked; this does not reduce the leaf count and is consistent with the task-file convention used by this refinement.
- Task 5.3 is checked and its stale `_Blocked` paragraph is removed. Cycle 1, Cycle 2, and Cycle 3 review artifacts each record `APPROVED` for their respective bounded scope. Cycle 3 closes the prior billing-boundary blockers with fresh SQLite and direct PostgreSQL evidence, including replacement-chain authority, immutable terminal usage, content-hash retirement/read-only behavior, correction/replay/stale/loser handling, and provider bounds.
- The current traceability/execution matrix has 36 unique requirement criteria, 35 `PASS`, 1 `PENDING`, and 0 `BLOCKED`. The sole pending row is 6.6. No row claims release or archival completion.
- The final validation artifact accurately separates refinement implementation completion from parent release/archive completion and leaves `spec.json` in its pre-archive state.

The implementation verdict therefore approves the 26-leaf refinement deliverable, subject to the external release gate documented below.

## B. Requirement 4.6 verdict

`PASS` is supported by executable tests, not inferred from the absence of a broad test-cost failure. The four required properties were independently exercised:

1. No per-token SQL writes: fresh checkpoint/coalescing tests pass, including cumulative snapshots, bounded delta batches, terminal flush idempotence, concurrent flush de-duplication, durable pre-terminal visibility, and the no-money-mutation assertion.
2. No unbounded response copy: fresh bounded-reader tests pass for fact loading, independent cursors, exhausted-leg exact-once behavior, provider-head loading, the 50-item provider-leg bound, and adjustment-overflow pending behavior.
3. No per-chunk monetary posting: fresh revision-worker tests pass and assert journal and account-balance write counts remain zero while pure head/revision work is persisted.
4. No retry/failover after client-visible output: fresh gate-replacement, dual-plane, interleaved-thinker-output, and adversarial no-retry tests pass. They assert that a second backend is not opened after output is visible and that the terminal decision remains committed.

The retained full-load test-cost JSON was also inspected directly. It contains 185,819 events and exactly two package-level failures: `internal/archtest` and `internal/infra/runtimebundle`. The archtest failure consists of 19 deterministic existing baseline-ratchet failures; there is no `internal/archtest` or `internal/infra/runtimebundle` source diff from the a15 baseline to this implementation HEAD. The runtimebundle worker-head timeout passes in a fresh isolated `-count=3` run. No 4.6 test family has a failure event in the retained log. This establishes the cause of the broad test-cost failure sufficiently for the 4.6 requirement, while leaving the broader performance gate itself non-green.

## C. Requirement 6.6 and archive verdict

6.6 remains `PENDING`, so the overall verdict is archive-blocked:

- The refinement Task 8.4 rule requires the parent and refinement criteria to pass at the same release-candidate SHA before #620 closes.
- The parent specification remains `tasks-generated` with `implementation_status: not_started`; its task file has 53 checked and 43 unchecked checkbox lines, including incomplete parent work in Tasks 1 and 12–20.
- The parent traceability record explicitly requires one future recorded RC SHA with the parent/refinement criteria and release gates green. No such same-SHA evidence exists.
- `4b1b26c1` is on the feature branch and `git merge-base --is-ancestor HEAD main` fails. There is no merged-main proof, no release-candidate SHA record, and no completed/archived spec metadata.

Implementation completion must not be conflated with release/archive completion. The spec must remain unarchived until the parent work, same-SHA release evidence, and approved completion flow are satisfied.

## Findings by severity

### Important — archive documentation accuracy

`refinement8-4-review.md` contains a closeout sentence describing the `make test-cost` failure as “harness truncation (not a test failure).” That sentence is stale/inaccurate against the retained JSON log and the execution root-cause record: the run has two package-level failures, consisting of 19 deterministic archtest baseline failures and one runtimebundle worker-head timeout. The release disposition is still correctly blocked, and this does not invalidate the independently proven 4.6 PASS, but the wording should be corrected before final release/archive certification.

### Informational — known verification limits

- Windows race verification is unavailable in the recorded cycles and current environment.
- `make quality-checks`, full `make test`, full `make qa`, and full-repository PostgreSQL parity are not green release evidence for this branch: the retained records identify known baseline/out-of-scope failures. The focused refinement tests, direct PostgreSQL billingstore parity, SQLite parity, build, vet, and diff checks are green.
- The final review does not modify the stale review wording, parent tasks, spec metadata, or any production/test file because this review is restricted to the single final evidence artifact.

## Fresh commands and results

All commands below were run against the reviewed worktree unless noted otherwise.

| Check | Result |
| --- | --- |
| `go test -count=1 -run 'TestRefinement41PreTerminalCheckpointsCoalesceCumulativeSnapshots\|TestRefinement41PreTerminalDeltaCheckpointsUseBoundedBatch\|TestRefinement41TerminalFlushesPendingOnceWithoutDuplicate\|TestRefinement41ConcurrentCheckpointFlushDoesNotDuplicate\|TestRefinement41PreTerminalCheckpointIsDurablyVisible\|TestRefinement41PreTerminalCheckpointDoesNotMutateMoney' ./internal/core/runtime/...` | PASS |
| `go test -count=1 -run 'TestALegReportBoundedFactLoading\|TestALegReportBoundedIndependentCursors\|TestALegReportExhaustedLegStreamExactOnce\|TestALegProviderHeadLoaderBounded\|TestALegReportProviderLoaderLegBoundFifty\|TestALegReportAdjustmentOverflowPending' ./internal/infra/billingstore/...` | PASS |
| `go test -count=1 -run 'TestRefinement42RevisionWorkerPersistsPureHeadWithoutBalanceMutation\|TestRefinement42DurableRevisionWorkReplayAndQueueIsolation' ./internal/infra/billingstore/...` | PASS |
| `go test -count=1 -run 'TestPhase42_NoRetryAfterOutput_GateReplacement\|TestDualPlaneMatrix_NoRetryAfterClientVisibleOutput\|TestExecutor_VisibleInterleavedNoRetryAfterThinkerOutput\|TestAdversarial_NoRetryAfterOutput_Unchanged' ./internal/core/runtime/...` | PASS |
| `go test -count=1 -run 'TestEvaluateALegCallAuthorityValidReplacementChainKnown\|TestALegReportValidReplacementChainKnown\|TestALegReportCycle3\|TestALegReportPassThrough\|TestALegReportProvider' ./internal/core/billing/... ./internal/infra/billingstore/...` | PASS (fresh SQLite-focused Task 5.3 suite) |
| `$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -count=1 -run 'TestPostgresCycle3FullEquivalenceCertification\|TestPostgresProvider\|TestDBParity_PostgresDirect' ./internal/infra/billingstore/...` | PASS (105.297s) |
| `go test -count=3 -run '^TestRefinement4StockObservationToEconomicSettlement$' ./internal/infra/runtimebundle/...` | PASS (isolated load-flake retest) |
| `make test-db-parity-sqlite` | PASS |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `git diff --check` | PASS |
| Focused current `internal/archtest` baseline ratchet tests | FAIL with the documented pre-existing baseline failures; no relevant source diff from a15 to HEAD |
| Retained full-load `make test-cost` JSON inspection | FAIL at broad gate: 2 package-level failures; no 4.6 test failure; root cause documented above |
| `git merge-base --is-ancestor HEAD main` | FAIL, confirming no merged-main/release proof |

## Changed file for this review

Only this file was created by the final review:

`.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/final-implementation-review.md`

Existing user-authored modifications to the execution/review evidence, `tasks.md`, and `final-implementation-validation.md` were preserved. No production code, tests, parent artifacts, task status, spec metadata, commit, or archive state was changed.

## Residual risks

The refinement is implementation-complete at its own leaf-task boundary, but it is not release-complete. The parent reconciliation work and same-SHA release gates remain outstanding. Before archive, correct the stale test-cost description, record a single release-candidate SHA, rerun the required full release gates, verify merged-main delivery, and complete the approved Kiro archive workflow.
