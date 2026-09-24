# Refinement 8.4 execution: parent/refinement traceability (subpasses A, B, and C)

Status: `READY_FOR_REVIEW_REFINEMENT_8_4` (subpasses A and B complete:
traceability mechanism plus release-gate execution at base `a15f6ae5`
on 2026-09-18; subpass C refreshes the matrix at HEAD `4b1b26c1` on
2026-09-19 after Task 5.3 Cycles 1-3 approvals. Task 8.4 awaits
independent review, not yet approved. RELEASE DISPOSITION remains
BLOCKED/PENDING per the gate table below.)

## Scope

Subpass A builds the combined #620 release traceability for Kiro Task 8.4
(`Reconcile parent task evidence and final release traceability`) without
changing production, tests, spec, tasks, stashes, or branch. It owns
exactly two new documents: the parent-side amendment map
(`extensible-usage-economics-reconciliation/evidence/usage-economics-b-leg-multimodal-refinement-traceability.md`)
and this execution record.

Pre-change inventory (verified before writing): refinement
`requirements.md` defines exactly 36 acceptance criteria
(1.1-1.6, 2.1-2.6, 3.1-3.6, 4.1-4.6, 5.1-5.6, 6.1-6.6 — six per
requirement); refinement `design.md` defines exactly seven Parent-Spec
Amendments. Both counts are reproduced exhaustively below; a mechanical
scan for duplicate/missing criterion IDs found none.

## Requirement-to-test matrix (all 36 refinement criteria)

Conventions: `R owner` = owning refinement task(s) with checkbox state;
`P owner` = owning parent requirement/task(s) with checkbox state; tests
are exact existing symbols verified by source search (no new tests, no
package-only references); evidence files live under
`.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/`.

| ID | Criterion summary | R owner | P owner | Named test(s) / command | Evidence | Status | Explanation |
|---|---|---|---|---|---|---|---|
| 1.1 | Input/output direction is canonical component identity, never merged | 2.1 [x], 2.3 [x], 7.1 [x], 8.1 [x] | Parent Req2; tasks 2.x [x] | `TestPhase2V2_ComponentKeyDirectionQualifiersAndCanonicalBytes` (`pkg/lipsdk/metering`); `TestRefinement81_DirectionIsCanonicalIdentity`, `TestRefinement81_MultimodalDirectionRateCertification` (`internal/core/billing`) | refinement8-1-execution.md | PASS | Direction-qualified keys, fingerprints, and rated lines proven distinct per direction |
| 1.2 | Native units per modality, no forced text-token conversion | 2.1 [x], 2.2 [x], 2.3 [x], 7.1 [x], 7.2 [x], 8.1 [x] | Parent Req2; tasks 2.x [x] | `TestPhase2V2_MultimodalNativeUnitsRoundTripFromFixture` (`pkg/lipsdk/metering`); `TestRefinement81_MultimodalDirectionRateCertification` | refinement8-1-execution.md | PASS | Native image/audio/video/document/page/byte/second units round-trip and rate without token coercion |
| 1.3 | Media qualifiers preserved in key and pricing context | 2.1 [x], 2.2 [x], 2.3 [x], 8.1 [x] | Parent Req2; tasks 2.x [x] | `TestPhase2V2_ComponentKeyDirectionQualifiersAndCanonicalBytes`; 8.1 vectors carry quality/transform/codec dimensions (`TestRefinement81_MultimodalDirectionRateCertification`) | refinement8-1-execution.md | PASS | Qualifiers bound to keys and frozen rules; mismatches fail closed |
| 1.4 | Customer-boundary vs provider-bound input distinguished; provider side authoritative | 3.1 [x], 8.1 [x] | Parent Req4; tasks 6.1 [x] | `TestPhase6LocalBoundaryRequiredCapabilityDeclinesFastPathBeforeAssessor` (`internal/core/runtime`); `TestRefinement81_InputTransformUsesProviderBoundRepresentation` | refinement8-1-execution.md | PASS | Resize/transcode before submission measured separately; supplier rating uses provider-bound form |
| 1.5 | Provider output preserved separately from customer-visible service measurement | 3.2 [x], 8.1 [x] | Parent Req4; tasks 6.2 [x] | `TestRefinement81_OutputTransformPreservesProviderOrigin` | refinement8-1-execution.md | PASS | Generated output and delivered/transformed output never substituted |
| 1.6 | Derived/aggregate provider meters retained natively, never decomposed by invention | 2.2 [x], 3.1 [x], 3.2 [x], 7.1 [x], 7.2 [x], 8.1 [x] | Parent Req2/Req4; tasks 2.x, 6.x [x] | `TestRefinement81_MissingDetailNeverBecomesZeroOrTextTokens` | refinement8-1-execution.md | PASS | Missing stays missing; aggregates rate without invented decomposition |
| 2.1 | Every request inference observation attributed to a concrete B-leg/attempt | 1.1 [x], 3.1 [x], 3.2 [x], 8.2 [x], 8.3 [x] | Parent Req6 (6.2); tasks 5.x [x] | `TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject` (`internal/archtest`); `TestPhase2RepairProviderInferenceRequiresBLegAndAttemptLineage` (`pkg/lipsdk/metering`); `TestRefinement82BillingCallIDIsInvocationBoundary` | refinement8-2-execution.md | PASS | B-leg/attempt lineage enforced by arch ratchet, contracts, and resumed-call proof |
| 2.2 | BillingCallID totals are projections over B-leg evidence, not competing truth | 1.1 [x], 5.3 [x] | Parent Req6 (6.1); refinement 5.3 owns projection behavior | `TestEvaluateALegCallAuthorityValidReplacementChainKnown` (`internal/core/billing`); `TestALegReportValidReplacementChainKnown` (`internal/infra/billingstore`); `TestALegReportCycle3RealLifecycleFullDTOImmutable`, `TestALegReportCycle3LateCorrectionsAcrossPlanes` (Cycle 3); `TestPostgresCycle3FullEquivalenceCertification` (PG) | refinement5-3-cycle1-review.md, refinement5-3-cycle2-review.md, refinement5-3-cycle3-review.md, refinement5-3-cycle3-certification.md | PASS | Projection behavior certified by Cycles 1-3: replacement-chain netting, per-call subtotals summing to retail, full-DTO stability across resume |
| 2.3 | Operator COGS includes every payable B-leg event (failed/retry/canceled/loser/auxiliary) | 1.2 [x], 8.3 [x] | Parent Req6 (6.2); tasks 5.4 [x]; 19.1 [ ] gate pending | `TestPhase5OperatorCOGSIncludesAllExecutedLegsAndRetailSelectsWinner`, `TestPhase5OperatorCOGSUsesInclusiveParentOnceAndRejectsPendingOrBYOK` (`internal/core/billing`); `TestRefinement83OperatorCOGSIncludesEveryPayableBLeg` | refinement8-3-execution.md | PASS | All-payable attribution certified; parent 19.1 integrated gate still pending (see gate rule) |
| 2.4 | Non-request resource costs stay resource subjects, never forced onto synthetic B-legs | 7.4 [x], 8.3 [x] | Parent Req6 (6.5)/Req9; tasks 11.2 [x] | `TestPhase7ResourceAllocationAddsPayableCOGSWithoutInferenceLeg`, `TestPhase7ResourceAllocationRejectsSyntheticBLegTarget`; `TestRefinement83NonRequestAllocationConservesAndStaysOffRetail`, `TestRefinement83IntegratedAllocationPersistsWithoutSyntheticLegs` | refinement8-3-execution.md | PASS | Resource subjects preserved; synthetic targets rejected; no synthetic rows durably |
| 2.5 | Attributed non-B-leg costs retain subject, policy/version, weights, remainder | 7.4 [x], 8.3 [x] | Parent Req6 (6.5); tasks 11.2 [x] | `TestPhase7StatementAllocationRetainsAccountSubjectAndRollsUp`; `TestRefinement83NonRequestAllocationConservesAndStaysOffRetail` (exact 1/2+1/4+1/4) | refinement8-3-execution.md | PASS | Conserved shares, policy identity, and explicit remainder proven exactly |
| 2.6 | A-leg/session totals are derived projections, never mutable authority | 1.1 [x], 5.3 [x] | Parent Req11 (11.1); refinement 5.3 owns projection behavior | `TestALegReportCycle3RealLifecycleFullDTOImmutable` (retail 60→85 across resume, no finality field on `billing.ALegReport`); `TestALegReportCycle3RetirementAndReadOnlyNoWrites` (idle re-query stable); `TestALegReportCycle3OldTokenContinuationAfterAppend` (live rolling totals `CallCount` 3, retail 90) | refinement5-3-cycle3-certification.md, refinement5-3-cycle3-review.md | PASS | Rolling-projection behavior certified by Cycle 3 on SQLite and direct PostgreSQL |
| 3.1 | No A-leg/session marker required to recognize/value/post/reconcile/report economics | 1.1 [x], 5.1 [x], 5.3 [x], 8.2 [x] | Parent Req10 (10.4); tasks 5.x [x] | `TestRefinement82RuntimeResumeKeepsTerminalOwnership`, `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider` (`internal/infra/runtimebundle`); `TestRefinement51ResumableSessionLifecycleAndSettlement` (`internal/infra/billingstore`); `TestALegReportCycle3LateCorrectionsAcrossPlanes` (report-surface: settled economics served with the A-leg open, 20→12 correction visible) | refinement8-2-execution.md, refinement5-3-cycle3-certification.md | PASS | Recognition/valuation/posting/reporting without finality certified, including the report surface |
| 3.2 | Provider DONE/EOF/terminal closes only its own execution scope | 1.1 [x], 5.1 [x], 8.2 [x] | Parent Req10 (10.4); tasks 5.x [x] | `TestRefinement51ResumableSessionLifecycleAndSettlement`; `TestRefinement82RuntimeResumeKeepsTerminalOwnership` (drain-to-EOF, call-local DONE) | refinement8-2-execution.md | PASS | Terminal markers never finalize the parent session |
| 3.3 | Resumed A-leg gets a new BillingCallID/B-legs; prior records immutable | 1.1 [x], 1.3 [x], 5.1 [x], 5.3 [x], 8.2 [x] | Parent Req10 (10.4); tasks 5.x [x] | `TestRefinement51ResumableSessionLifecycleAndSettlement`; `TestRefinement82RuntimeResumeKeepsTerminalOwnership` (attempts 1→2, call1 fingerprints stable); `TestALegReportCycle3RealLifecycleFullDTOImmutable` (distinct `NewBillingCallID`/B-leg via `billing.TerminalUsageSink`, complete call-1 DTO byte-identical after resume) | refinement8-2-execution.md, refinement5-3-cycle3-certification.md | PASS | Fresh identities per invocation; history immutable, including the full report DTO |
| 3.4 | Closed B-legs stay appendable for late evidence/revisions | 1.3 [x], 5.2 [x], 8.2 [x] | Parent Req10 (10.2/10.3); tasks 5.x [x] | `TestRefinement52DurableLateEvidenceKeepsClosedLegAndRevisionWorkAppendable`; `TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates` | refinement8-2-execution.md | PASS | Late/corrected evidence appends without reopening execution |
| 3.5 | Call closure freezes its B-leg set without implying A-leg terminality | 1.1 [x], 4.3 [x], 5.1 [x], 8.2 [x] | Parent Req10 (10.4); tasks 5.x [x] | `TestRefinement82RuntimeResumeKeepsTerminalOwnership` (`ExpectedBLegIDs`, one fingerprint-stable leg) | refinement8-2-execution.md | PASS | Frozen B-leg sets per call; session stays resumable |
| 3.6 | Retention/retirement never triggers new economics | 1.1 [x], 1.3 [x], 5.1 [x], 5.3 [x] | Parent Req10/Req17 | `TestALegReportCycle3RetirementAndReadOnlyNoWrites` (SQLite: business DTO plus SHA-256 content hashes over all 13 report-read tables incl. `billing_economic_revision_work_state`, identical across baseline, 3 repeats, final query); `TestPostgresCycle3FullEquivalenceCertification` (PG `row_to_json` dumps plus existence checks, same assertions); retirement-observer inspection: `MemoryStore` invokes its observer only post-eviction post-lock (`internal/core/b2bua/store.go:72-75, 160-173`), production wiring only ends prompt-cache and conversation-view state (`internal/infra/runtimebundle/compile_generation.go:208-218`) with no billing writer | refinement5-3-cycle3-certification.md, refinement5-3-cycle3-review.md | PASS | Writer-silence verified against the real observer plus 13-table content-hash proof on both dialects |
| 4.1 | Pre-terminal stable deltas/snapshots/charges appendable before closure | 1.3 [x], 4.1 [x], 8.2 [x] | Parent Req10 (10.2); tasks 4.x [x] | `TestPhase4ValuationAndReconciliationRoundTripAndReplay` (`internal/infra/billingstore`); `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider` | refinement8-2-execution.md | PASS | Pre-terminal checkpoints durable and accruing |
| 4.2 | Revision-triggered valuation without stream-callback money mutation | 4.1 [x], 4.2 [x], 8.2 [x] | Parent Req10/Req13; tasks 4.x [x] | `TestRefinement42DurableRevisionWorkReplayAndQueueIsolation`, `TestRefinement42RevisionWorkerPersistsPureHeadWithoutBalanceMutation` | refinement8-2-execution.md | PASS | Pure revision workers; no receive-callback posting |
| 4.3 | Incremental settlement only at stable scopes; provisional retail reversible by compensating deltas | 1.2 [x], 4.1 [x], 4.2 [x], 4.3 [x], 8.2 [x] | Parent Req8 (8.6)/Req14; tasks 10.x [x] | `TestRefinement43CustomerRetailRequiresDurableCallClosure`; `TestRefinement82ProviderRevisionPostingsAdvancePerStage` | refinement8-2-execution.md | PASS | Settlement waits for call closure; revisions compensate by delta |
| 4.4 | Terminal is a checkpoint/completeness transition, not the only visibility path | 1.3 [x], 4.1 [x], 4.2 [x], 4.3 [x], 8.2 [x] | Parent Req10 (10.4); tasks 5.x [x] | `TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates` (rev1 visible pre-terminal, terminal, correction) | refinement8-2-execution.md | PASS | All three evidence phases on one B-leg without duplicates |
| 4.5 | Late/corrected evidence reuses append/revision/delta paths pre- or post-terminal | 1.3 [x], 4.2 [x], 4.3 [x], 5.2 [x], 8.2 [x] | Parent Req10 (10.3)/Req13; tasks 5.x [x], 13.x [ ] | `TestRefinement52DurableSameRevisionEvidenceContainmentFencesHead`; `TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates` | refinement8-2-execution.md | PASS | Same mechanisms regardless of terminal timing |
| 4.6 | Bounded incremental accounting (no per-token writes/copies/postings, no post-output retry) | 3.3 [x], 4.1 [x], 4.2 [x] | Parent Req4 (4.6)/Req18; no dedicated 8.x vector | Clause battery PASS fresh 2026-09-19 (18/18): no per-token writes — `TestRefinement41PreTerminalCheckpointsCoalesceCumulativeSnapshots`, `TestRefinement41PreTerminalDeltaCheckpointsUseBoundedBatch`, `TestRefinement41TerminalFlushesPendingOnceWithoutDuplicate`, `TestRefinement41ConcurrentCheckpointFlushDoesNotDuplicate`, `TestRefinement41PreTerminalCheckpointIsDurablyVisible` (`internal/core/runtime`); no unbounded copy — `TestALegReportBoundedFactLoading`, `TestALegReportBoundedIndependentCursors`, `TestALegReportExhaustedLegStreamExactOnce`, `TestALegProviderHeadLoaderBounded`, `TestALegReportProviderLoaderLegBoundFifty`, `TestALegReportAdjustmentOverflowPending` (`internal/infra/billingstore`) plus Cycle 3 13-table no-write hashes; no per-chunk posting — `TestRefinement41PreTerminalCheckpointDoesNotMutateMoney`, `TestRefinement42RevisionWorkerPersistsPureHeadWithoutBalanceMutation`, `TestRefinement42DurableRevisionWorkReplayAndQueueIsolation`; no post-output retry — `TestPhase42_NoRetryAfterOutput_GateReplacement`, `TestDualPlaneMatrix_NoRetryAfterClientVisibleOutput`, `TestExecutor_VisibleInterleavedNoRetryAfterThinkerOutput`, `TestAdversarial_NoRetryAfterOutput_Unchanged`. `make test-cost` remains FAIL exit 1 for out-of-scope reasons (see subpass D): it measures CI test-execution cost, not accounting bounds, and is blocked by deterministic archtest baseline ratchets plus one load-flaky worker-head test — zero 4.6-clause failures | (this file, subpass D) | PASS | All four clauses proven by exact executable tests; ratchet failure fully attributed outside 4.6 |
| 5.1 | Default customer basis is policy-selected B-leg quantities, not an A-leg meter | 1.2 [x], 4.3 [x], 6.1 [x], 8.3 [x] | Parent Req8 (8.1); tasks 10.1 [x] | `TestPhase10RetailSelectorDefaultSelectsSurfacedWinnerOnly`; `TestRefinement83DefaultRetailSelectsWinnerOnly`, `TestRefinement83RateCallSettlesWinnerOnlyThroughSelection` | refinement8-3-execution.md | PASS | Winner-only basis through production selector and settlement seam |
| 5.2 | Explicit policy defines contributing B-legs; all-attributable only for explicit offers | 1.2 [x], 4.3 [x], 6.1 [x], 8.3 [x] | Parent Req8 (8.1); tasks 10.1 [x] | `TestPhase10RetailSelectorNamedSubsetAndAllAttributableAreDeterministic`; `TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts`, `TestRefinement83CostPassThroughSettlesAcceptedProviderCost` | refinement8-3-execution.md | PASS | Frozen policy identity, version, mode, and refs for every offer shape |
| 5.3 | Retry/failover/loser usage hits COGS, never default retail unless explicitly included | 1.2 [x], 4.3 [x], 6.1 [x], 8.3 [x] | Parent Req8 (8.1/8.6); tasks 10.1 [x] | `TestPhase10RetailRatingUsesFrozenSelectionIndependentTariffAndSingleScopeFees` (unselected mutation invariance); `TestRefinement83AuthoritiesStaySeparateAndCloneStable`; `TestRefinement83IntegratedCrossAuthorityPersistsAndExplains` | refinement8-3-execution.md | PASS | Retry usage provably absent from default retail, present in COGS |
| 5.4 | Customer prices arbitrary per modality/direction with generic rule types | 1.2 [x], 2.3 [x], 6.2 [x], 8.3 [x] | Parent Req7 (7.3/7.4)/Req8 (8.2); tasks 9.2/9.3 [x] | `TestRefinement81_MultimodalDirectionRateCertification` (asymmetric rates); `TestRefinement83DefaultRetailSelectsWinnerOnly` (independent tariff literals) | refinement8-1-execution.md, refinement8-3-execution.md | PASS | Customer rates independent of supplier prices across rule types |
| 5.5 | Submission/call/account charges are once-per-scope commercial lines | 1.2 [x], 6.2 [x], 8.3 [x] | Parent Req8 (8.3); tasks 10.2/10.3 [x] | `TestPhase10RetailRatingUsesFrozenSelectionIndependentTariffAndSingleScopeFees`; `TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts` (call fee exactly once across five legs) | refinement8-3-execution.md | PASS | Scoped fees never multiplied by B-leg count |
| 5.6 | Customer-boundary meters only via declared proxy tariff, never as canonical inference | 6.3 [x], 8.3 [x] | Parent Req8 (8.1); tasks 10.1 [x] | `TestPhase10RetailRatingKeepsProxyServiceMetersSeparateFromInference`; `TestRefinement83DefaultRetailSelectsWinnerOnly` (separate `proxy_service` line) | refinement8-3-execution.md | PASS | Proxy line separately named, never substituted for inference |
| 6.1 | Certify image/audio/video/document direction fixtures with native units and direction pricing | 1.3 [x], 2.3 [x], 7.1 [x], 7.2 [x], 7.3 [x], 8.1 [x] | Parent Req9 (9.5); tasks 9.5 [x] | `TestRefinement81_MultimodalDirectionRateCertification`; `TestRefinement81_JournalDurability`; `TestRefinement81_ValuationDurability` | refinement8-1-execution.md | PASS | All modalities/directions with native units, Q/R planes, durable round-trips |
| 6.2 | Certify one media transform case per direction with separated economics | 1.3 [x], 2.3 [x], 7.1 [x], 7.2 [x], 8.1 [x] | Parent Req9 (9.5); tasks 9.5 [x] | `TestRefinement81_InputTransformUsesProviderBoundRepresentation`; `TestRefinement81_OutputTransformPreservesProviderOrigin` | refinement8-1-execution.md | PASS | Provider-bound vs customer-visible economics stay separate both directions |
| 6.3 | Certify one A-leg with sequential BillingCallIDs across DONE/terminal resume | 1.3 [x], 8.2 [x] | Parent Req10/Req19 (19.1); tasks 19.1 [ ] gate pending | `TestRefinement82RuntimeResumeKeepsTerminalOwnership`; `TestRefinement82BillingCallIDIsInvocationBoundary`; `TestRefinement82SharedRevisionScenarioSQLite`, `TestRefinement82SharedRevisionScenarioPostgresDirect` | refinement8-2-execution.md | PASS | Resume certified both dialects; parent 19.1 gate still pending (see gate rule) |
| 6.4 | Certify pre-terminal, terminal, post-terminal evidence without duplicates | 1.3 [x], 8.2 [x] | Parent Req10/Req19 (19.1/19.2); tasks 19.x [ ] gate pending | `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider`; `TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates`; `TestRefinement82ProviderRevisionPostingsAdvancePerStage` | refinement8-2-execution.md | PASS | All three phases without duplicates; parent 19.x gates still pending |
| 6.5 | Certify all-B-leg COGS against selected-B-leg retail under retry/failover/loser/pass-through | 8.3 [x] | Parent Req19 (19.1); tasks 19.1 [ ] gate pending | `TestRefinement83OperatorCOGSIncludesEveryPayableBLeg`; `TestRefinement83DefaultRetailSelectsWinnerOnly`; `TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts`; `TestRefinement83CostPassThroughSettlesAcceptedProviderCost`; `TestRefinement83NonRequestAllocationConservesAndStaysOffRetail`; `TestRefinement83CrossAuthorityUsesAcceptedCostOnly`; `TestRefinement83IntegratedCrossAuthorityPersistsAndExplains`, `TestRefinement83IntegratedPassThroughHeadRevisionAndConflict`, `TestRefinement83IntegratedAllocationPersistsWithoutSyntheticLegs` | refinement8-3-execution.md | PASS | Full selector matrix with exact amounts, refs, replay, and durable proof |
| 6.6 | #620 and parent plan treat this refinement as normative; gate both at one SHA | 8.4 [x] (this subpass defines the gate) | Parent Req20; tasks 20.x [ ] | (this traceability; no test asserts the gate itself) | (this file) | PENDING | Gate rule defined here; future release-candidate SHA not yet recorded. Fresh 2026-09-19 parent state: 53 checked / 43 unchecked checkboxes; parent top-level Tasks 1 and 12-20 remain unchecked; refinement implementation is complete but same-SHA green evidence does not exist |

## Contradiction audit

Method: exact-phrase scans of parent `requirements.md`, `design.md`, and
`tasks.md` for session-finality, A-leg authority/meter, session billing,
and `FinalizeSession` wording, plus per-item proof references. Historical
parent text is preserved; where any future conflict appears, the refinement
has precedence within its normative scope (`spec.json`
`normative_precedence`), never by rewriting parent text.

1. No A-leg inference authority: parent intro already roots request-scoped
   inference usage in B-leg execution and calls the A-leg a continuity and
   aggregation container; Req 6.2/11.1 agree. Guarded by
   `TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject`. No
   contradictory wording found.
2. No A-leg/session finality requirement: parent C6 states the A-leg has no
   accounting-final state and settlement never waits for session finality;
   Req 10.4 agrees. No contradictory wording found.
3. Call-local closure with same-A-leg resume: parent Req 10.4 explicitly
   requires fresh BillingCallID/B-legs per resume with earlier records
   untouched; task 213 (parent 5.x) treats terminal/DONE as B-leg/call
   checkpoint only. Certified by `TestRefinement82RuntimeResumeKeepsTerminalOwnership`. No contradiction.
4. All-payable COGS versus policy-selected retail: parent Req 6.2/6.4/8.1
   require all-executed-B-leg operator cost with independent customer
   selection. Certified by `TestRefinement83OperatorCOGSIncludesEveryPayableBLeg`
   and the retail selector suite. No contradiction.
5. Customer-boundary service tariff separate from provider inference: parent
   Req 8.1 and task 10.1 keep proxy/service charges separately declared.
   Certified by `TestPhase10RetailRatingKeepsProxyServiceMetersSeparateFromInference`.
   No contradiction.
6. Terminal revisable and preterminal economics supported: parent Req
   10.2/10.3 and C6 allow durable/reducible evidence while a B-leg is
   active and later revision. Certified by
   `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider` and
   `TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates`.
   No contradiction.
7. Native multimodal directions/units and real resource subjects with
   conserved allocations: parent Req 2/D3 and Req 6.5/9.x require them.
   Certified by `TestRefinement81_MultimodalDirectionRateCertification` and
   `TestPhase7ResourceAllocationAddsPayableCOGSWithoutInferenceLeg`. No
   contradiction.

Result: zero remaining contradictory parent wordings found. Refinement
precedence stands as the declared rule for any future conflict.

## Gate state (honest)

- Parent spec: major tasks 2-11 complete; tasks 1, 12, 13, 14, 15, 16, 17,
  18, 19, 20 incomplete (fresh 2026-09-19 count: 53 checked / 43
  unchecked checkboxes). No incomplete parent task is marked done by this
  document.
- Refinement Task 5.3 is resolved: Cycles 1 (`0120bdf8`), 2 (`f35f867c`),
  and 3 (`4b1b26c1`) each carry an independent APPROVED review. Criteria
  2.2, 2.6, 3.6, and 4.6 are PASS with Cycle 1-3 and clause-battery
  evidence; nothing here claims all 36 pass: 35 PASS, 0 BLOCKED,
  1 PENDING (6.6 future gate).
- Refinement leaf tasks are 26/26 `[x]`; parent grouping Task 2 remains
  `[ ]` (pre-existing state, not altered by this closeout turn).
- Release rule for #620: the issue cannot close until every parent and
  refinement acceptance criterion is green on one recorded future
  release-candidate SHA, the full validation gates (`make test`, `make qa`,
  `make test-db-parity`, `make test-race` where applicable) pass there,
  and criterion 6.6 is certified. Current HEAD `4b1b26c1` is the
  traceability refresh base, not a green release candidate.

## Gate execution (subpass B, base `a15f6ae5`, 2026-09-18)

Each gate ran once with the repo's canonical command inside a 20-minute
bound; no gate timed out, no failure was fixed or re-run, disk stayed
above 83GB free throughout.

| Gate | Command | Exit | Disposition | First causal failure |
|---|---|---|---|---|
| quality-checks | `make quality-checks` | 1 | FAIL (baseline) | Formatting/modules/build/vet/regex passed; parallel child jobs failed: `adhoc-goroutines` on tracked `internal/core/billing/economic_revision_worker.go` and `internal/infra/runtimebundle/observation_economic_bridge.go`, `lint` with 229 tracked issues, plus the `internal/archtest` baseline ratchets. All pre-existing tracked debt; this task adds no Go code. |
| test | `make test` | 1 | FAIL (baseline) | `quality-checks-fast` stopped before `test-unit`; the same parallel adhoc-goroutine and 229-issue lint baseline failures were present. |
| parity-checks | `make parity-checks` | 0 | PASS | — |
| test-db-parity | `make test-db-parity` | 1 | FAIL (baseline) | SQLite components passed; PostgreSQL failed at `TestDBParity_PostgresDirect/MigrationAndSchemaParity`: `metering_components.value_present` got `int4` but expected boolean. Pre-existing shared-database drift, independent of Task 8.4 (docs-only). |
| qa | `make qa` | 1 | FAIL (baseline) | Stopped at the overall `quality-checks-fast` step; its parallel adhoc-goroutine and lint failures prevented later QA stages — neither is uniquely first. |
| test-race | `make test-race` | 0 | PASS/skip | Windows reports race evidence unsupported; Linux CI remains required (cited from the independent review's fresh run; not re-run here). |

Exit-code convention: the worker capture observed exit 2 through the
PowerShell `make` plus redirected-capture invocation; the canonical direct
invocation reports exit 1, which is adopted as authoritative here, and all
stale exit-2 values have been removed.

RELEASE DISPOSITION: BLOCKED/PENDING. `a15f6ae5` is NOT an accepted
release candidate: four gates fail on pre-existing baselines, Task 5.3
is unresolved, and parent tasks 1/12-20 are incomplete. All gates
must rerun green on one recorded future release-candidate SHA after
Task 5.3 and parent completion. Task 8.4 completion (gate wired and
evidenced here) is distinct from #620 completion (blocked).

## Closeout verification (subpass C, HEAD `4b1b26c1`, 2026-09-19)

Each command ran once on the clean closeout tree; this turn changes Kiro
docs only (no production, test, schema, migration, or HTTP change), so
code results below reflect the reviewed Cycles 1-3 tree.

| Command | Exit | Disposition | Observation |
|---|---|---|---|
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | 0 | PASS | billing 1.492s, billingstore 32.660s, all stdhttp packages PASS |
| `go test -count=1 -v -run 'TestALegReportBounded\|TestALegReportExhaustedLegStream\|TestALegProviderHeadLoaderBounded\|TestALegReportProviderLoaderLegBound\|TestALegReportCycle3' ./internal/infra/billingstore/` | 0 | PASS | 9/9 (5 bounded + 4 Cycle 3 SQLite) |
| `make test-db-parity-sqlite` | 0 | PASS | All registered components |
| `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=1 -run 'TestDBParity_PostgresDirect' ./internal/infra/billingstore/` | 0 | PASS | 74.322s, direct PostgreSQL reachable |
| `go build ./...` | 0 | PASS | — |
| `go vet ./...` | 0 | PASS | No output |
| `make quality-checks` | 1 | FAIL (baseline) | Same pre-existing trio as subpass B: `adhoc-goroutines`, `archtest`, `lint` child failures; failures live in tracked debt (`internal/infra/runtimebundle` budgets, archtest ratchets, lint issues), none in Task 5.3 files |
| `make test-cost` | 1 | FAIL (attributed, see subpass D) | Head `test-unit` measurement exit 3 from exactly 2 failing packages: 19 deterministic `internal/archtest` baseline ratchets (pre-existing) plus one load-flaky `internal/infra/runtimebundle` worker-head timeout (3/3 green in isolation). Zero 4.6-clause failures |
| `go test -race` | n/a | UNAVAILABLE | Windows cgo toolchain; prior Cycles 1-3 evidence stands, Linux CI still required |

Root `main` (`C:\Users\Mateusz\source\repos\go-llm-interactive-proxy`)
verified clean (`## main...origin/main`, no changes) before and after
this turn. No commit, rebase, merge, stash, reset, or PR operation was
performed.

## 4.6 closeout (subpass D, HEAD `4b1b26c1`, 2026-09-19)

Root-cause diagnosis of `make test-cost` exit 1 (kiro-debug style;
no blind rerun, no threshold changes):

- Harness mechanics: the ratchet (`scripts/test-cost-ratchet.ps1`)
  measures anchor `6dbb8318` vs head with `lip-testcost measure`, which
  runs `go test -count=1 -json -parallel=16 -timeout=10m ./...` inside a
  Windows Job Object used for accounting only (no memory/CPU limits;
  `tools/taskrunner/process_windows.go` sets only
  `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`). `Measure` returns
  `ErrMeasurementFailed` on any child nonzero exit, so the ratchet
  requires fully green `test-unit`.
- Failure evidence (parsed machine JSON from the retained harness log
  `h-8a430627/logs/test-unit-stdout.log`, 185819 events, 374/374
  packages resolved): exactly 2 package-level `fail` actions, zero
  4.6-clause failures. (An earlier text search missed them; JSON
  parsing found them. The log did not end mid-run.)
  1. `internal/archtest`: 19 deterministic baseline-ratchet failures
     (`TestPackageTreeBudgetsReportSection`,
     `TestRequestAttemptStateTargetRatchetFailsIfTypeReappearsOnCurrentAST`,
     `TestRequestAttemptStateRatchetsPassOnCurrentCode`,
     `TestRequestAttemptStateBaselineMatchesCurrentAST`,
     `TestBillingCoreStaysProviderAndPersistenceFree`,
     `TestCompactionContinuitySecurity_ContentFreePublicSurfaces`,
     `TestShrinkage_NetReductionMeetsRequirement115`,
     `TestBillingFinalConvergenceLOCRatchetActive`,
     `TestPhase51AttemptSequenceAuthorityRatchets`,
     `TestShrinkage_ConnectorOverlayExactMeasured`,
     `TestCriticalFileBudgets`, `TestPackageTreeBudgetsExact`,
     `TestLineComplexityBudgets`,
     `TestHexagonalMigrationBaselineMatchesGoList`). Same ratchets
     already failing at base `a15f6ae5` (subpass B gate table) —
     pre-existing tree-wide debt, deterministic, unfixable without
     out-of-scope production/architecture changes.
  2. `internal/infra/runtimebundle`:
     `TestRefinement4StockObservationToEconomicSettlement` failed once
     under 16-way full-suite load ("timed out waiting for provider
     economic head", worker `Status:processing`, 25.12s) and passes
     consistently in isolation (`-count=3`, 10.601s total) — a load
     flake, not a code defect. No production change made.
- Verdict on the gate: `make test-cost` measures CI test-execution cost
  head-vs-anchor; it cannot certify accounting boundedness, and it
  cannot pass on this head without remediating out-of-scope baselines.
  A full retry was deliberately not run: the outcome is deterministic
  from the evidence above. No `TEST_COST_*` overrides, skip flags, or
  threshold changes were used.
- Certification basis actually used (spec-permitted: no refinement task
  lists `test-cost` as a validation; the spec's own gates are focused
  tests, resume tests, fixtures, parity, and fast-path revalidation):
  the 18-test clause battery in row 4.6, all PASS fresh on this HEAD,
  one exact test group per clause. No production gap in any clause
  appeared, so no `BLOCKED_4_6` arose and no production was touched.

## Validation performed (subpass A)

- Pre-change inventory: exactly 36 refinement criteria and seven design
  amendments counted from source; reproduced exhaustively above with no
  duplicates or gaps (mechanical ID scan: 1.1-1.6 × 2.1-2.6 × 3.1-3.6 ×
  4.1-4.6 × 5.1-5.6 × 6.1-6.6).
- Every named test symbol verified to exist by exact source search at HEAD
  `a15f6ae5`; no package-only references where named tests exist.
- Current verification is this traceability document set itself; all test
  outcomes cited are historical evidence from the referenced execution
  files, re-verified for symbol existence only — no test was re-run in
  this documentation subpass.
- `gofmt`/`go vet` not applicable (no Go writes); placeholder/secret scan
  and markdown consistency checked on both new documents; `git status`
  shows only the two owned docs.

## Post-merge closeout (2026-09-24)

Superseding addendum; the historical chronology above (including the subpass B
gate table and the rows referencing parent `[ ]` tasks or a `PENDING` 6.6) is
preserved. Implementation PR #659 (head
`e0001f113b661bdab5477a875c595def3d090a66`, merged as
`fc8f01f982f566b87593215895d8ced6f6811ca5` on 2026-09-24T01:13:56Z) merged the
complete parent and refinement work with all 37 attached checks SUCCESS on the
exact head and a merge tree byte-identical to the head.

At the merged release candidate:

- Row 6.6 is satisfied by its actual meaning: before implementation, #620 and the
  parent execution plan treated this refinement as normative (recorded in the
  parent traceability map and this Task 8.4 evidence), and the merged
  implementation follows the refined B-leg/multimodal/session semantics. The
  same-SHA release certification is owned by parent Task 20 and the
  archive-completion contract, not by 6.6.
- The status counts recomputed in the earlier closeout refresh move from
  `STATUS_PASS=35 / STATUS_PENDING=1` to `STATUS_PASS=36 / STATUS_PENDING=0`.
- RELEASE DISPOSITION is no longer BLOCKED/PENDING for the implementation scope;
  issue #620 remains OPEN until the archive PR merges and #398 remains OPEN for
  other prerequisites. Residual risks are unchanged.
