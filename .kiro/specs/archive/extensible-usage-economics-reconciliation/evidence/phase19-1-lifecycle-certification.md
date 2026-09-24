# Phase 19.1 lifecycle certification evidence

Task: `.kiro/specs/extensible-usage-economics-reconciliation/tasks.md` 19.1 —
certify the complete independent-economics lifecycle.
Branch: `feat/b-leg-usage-economics`. Baseline: `fe73f55b` (approved Phase 18,
clean worktree at start). Boundary: bounded integrated contracts only.
Validation: `make test-unit`; `make parity-checks`.
Requirements in scope: 1.1-16.6 plus 18.1-18.3 (99 criteria).
Migration/cutover and release-wide criteria (17.x, 18.4-18.6, 19.2-20.1) are
explicitly deferred by the task text and remain with 19.2-20.1.

## Gap finding

Matrix method: every design acceptance vector (design.md Testing Strategy,
23 rows) and every in-scope requirement was mapped to exact current test
names, verified to exist by declaration search (94 names, zero missing).
Result: unit/contract coverage existed for every vector, but **no integrated
test ran real-family evidence through the production collector, normalizer,
durable stores, reference rater, reconciler, settlement and query seams in
one lifecycle** — every integrated test hand-built `metering.Observation`
literals with synthetic mapping refs. That single gap is closed below. No
production behavior gap was found: five fail-closed behaviors probed during
certification (tokenizer guard, unknown-payer partial COGS, credit
rate-missing, V1-evidence provider-cost authority, submission-fee scope) all
match their specified contracts and the Phase 5/83/18 corpus, so no
production fix was required and no RED artifact persists.

## New integrated certification tests

File: `internal/infra/billingstore/phase19_1_lifecycle_certification_test.go`
(package `billingstore`, SQLite file-backed stores, no host, no direct SQL).
Production seams only: `openaiusage.ProviderEvidenceDraft` over wire-shaped
`lipapi.Event` payloads, `coremetering.ProviderEvidenceBuffer` drain with
trusted B-leg binding, `normalize.Normalize` with Anthropic-separate and
OpenAI-inclusive family mappings, `journalstore.AppendObservationsWithOutbox`
plus `GetObservation`, `billing.RateWithTariff` (E/Q),
`billing.RateProviderReported` (P), `billing.RateCall` (R),
`billing.CompareComponentQuantities`,
`billing.DecomposeMonetaryDiscrepancies`,
`billing.AttributeOperatorCOGS`,
`billing.SelectRetailBLegEvidence`, `AdmitExposure`, `ApplyProviderCost`,
`ApplyCallBillingResult`, `AppendValuation`, `AppendReconciliation`,
`CallExplanation`, `QueryEconomicDetail`.

1. `TestPhase191LifecycleFullEconomicsCertification` — OpenAI-family winner
   (110 in/200 out + image/audio native units + 1.32 USD aggregate charge)
   plus independent local measurement (100 in/200 out/10s audio) through
   journal round-trip; E=7.20, Q=7.44, P=1.32 with exact decomposition
   (metering effect 24/2, residual -612/2, end-to-end -588/2); all-leg COGS
   2.17 USD over winner/retry/loser/canceled-aux with never-started shell
   excluded; winner-only retail R=8.44 USD (frozen provider 7.44 inference
   basis + one 1.00 submission fee; competing local 7.20 stays in E/Q/P,
   reconciliation and query evidence, never a second R inference charge;
   R inference lines reference only the provider observation deterministically);
   idempotent settlement replay; `CallExplanation` + `QueryEconomicDetail`
   retain local/provider source separation.
2. `TestPhase191LifecycleResumeMissingAggregateCreditsSubmission` — same
   A-leg resume after DONE under a fresh BillingCallID (R 8.44 then 1.30,
   sealed call-1 fingerprint unchanged); attempted-missing stays unknown and
   partial while never-started stays excluded; aggregate-only 12 USD money
   without invented split; credit/time/synthetic-widget extensibility with
   fail-closed credit monetization (`ErrRateMissing`) and exact 725/3 subset
   rating.
3. `TestPhase191LifecycleMultimodalTransforms` — image/audio/video/document
   both directions via native OpenAI-family fixtures; provider-bound 12s
   audio versus delivered 10s separation; disjoint direction/unit keys; exact
   210396/5 direction-specific rating with one line per rule.

## Acceptance-vector matrix

V1 inclusive 11.6: `TestTCK_ActualNormalizerAndReducer`,
`TestNormalize_InclusiveInputUsesExactDisjointPartition` (+19.1 test 1 local
partition). V2 Anthropic separate: `TestNormalize_SeparateInputDoesNotSubtractCaches`
(+19.1 test 1). V3 reasoning-in-output: `TestNormalize_CacheLifetimesAndReasoningStayDisjoint`,
`TestPhase9ReferenceRater_ReasoningOutputIsMeaningfulWhenUnpriced`. V4
quantity-no-money: `TestPhase9ProviderReportedRatingDoesNotRequireLocalTariff`
(+19.1 test 1 Q-without-P-money on quantity plane). V5 aggregate 12:
`TestAssembleEconomicDetailAggregateOnly`,
`TestPhase172Cluster3EvidenceDrivenSemantics` (+19.1 tests 1-2 aggregate
charges). V6 E=1/Q=1.1/P=1.32: `TestMonetaryDiscrepancyDecomposesAcceptanceVector`
(+19.1 test 1 live E/Q/P decomposition). V7 gauges:
`TestProjectAccountWindows_GaugesArePartialResetScopedAndOrderIndependent`,
`TestCodexStream_PreservesConcurrentOutOfOrderWindowSnapshots`. V8 3+5+2=10:
`TestPhase5OperatorCOGSMissingAttemptIsPartialAndNeverStartedIsExcluded`,
`TestRefinement83OperatorCOGSIncludesEveryPayableBLeg` (+19.1 tests 1-2).
V9 replay/conflict: `TestApplyObservations_DeduplicatesExactReplayBeforeGraphValidation`,
`TestCallLegUsageReplayIdenticalIsNoopAndConflictIsIntegrityError`,
`TestAccountingCutoverExactReplayIsIdempotent` (+19.1 settlement replay).
V10 10-to-8 adjustment:
`TestPhase5OperatorCOGSAppliesChargeCorrectionOnce`,
`TestCorrectionRecoveryCertifyCorrectedAggregateStatementProducesOneIdempotentDelta`.
V11 shared allocation: `TestPhase7ResourceAllocationAddsPayableCOGSWithoutInferenceLeg`,
`TestAllocationConserve_ExactWeightsSharesAndDeterministicResidual`
(+19.1 allocation-free COGS with all-leg conservation). V12 one submission
fee: `TestPhase9ReferenceRater_AllDeclaredFixedFeesApplyOnce`,
`TestBindSubmissionIdentityKeepsOneToolLoopOnOneSubmission`,
`TestSQLiteSubmissionFeeSettlementIsClaimedOnceConcurrently` (+19.1 tests
1-2 fee-once totals). V13 0.125cr/1.250s:
`TestProviderDebit_RoundTripsAsRequestScopedObservation`,
`TestPhase2V2_DecimalNormalizesAndConvertsCheckedNanos` (+19.1 test 2).
V14 missing-vs-zero: `TestRefinementNeverStartedEvidenceMayRemainKnownZero`,
`TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED`,
`TestOperatorCostSelectionAttemptedMissingStaysUnknown` (+19.1 test 2). V15
USD/EUR no-FX: `TestPhase25_AggregateMoney_MixedCurrencyAndOverflow`. V16
crash/stale: `TestCutoverIntegratedCrashRestartNoDuplicates`,
`TestF3RestartBetweenAdmitTerminalAndClaimPost`
(+19.1 file-backed store lifecycle). V17 tokenizer provenance:
`TestPhase2V2_ObservationPreservesPresenceProvenanceAndDeepClone`,
`TestReconcileLocalEstimateDoesNotOverwriteProviderAuthoritativeUsage`
(+19.1 test 1 tokenizer_required guard). V18 multi-source/resets:
`TestApplyObservations_DeltaCumulativeGaugeAndResourceSemantics`,
`TestPhase11AccountWindowJournal_ResetEpochAndRestartRemainDistinct`. V19
4K-to-1024: `TestRefinement81_InputTransformUsesProviderBoundRepresentation`
(+19.1 test 3). V20 12s-to-10s:
`TestRefinement81_OutputTransformPreservesProviderOrigin` (+19.1 test 3
exact 12/10). V21 mixed multimodal:
`TestRefinement81_MultimodalDirectionRateCertification`,
`TestRefinement81_DirectionIsCanonicalIdentity`,
`TestNativeUsageMeasuresPreserveMultimodalDirectionAndUnits` (+19.1 test 3).
V22 resume-after-DONE: `TestRefinementContinuationAfterDoneUsesFreshCallState`,
`TestRefinement82RuntimeResumeKeepsTerminalOwnership`,
`TestRefinement51ResumableSessionLifecycleAndSettlement` (+19.1 test 2).
V23 retry+loser+winner: `TestRefinement83DefaultRetailSelectsWinnerOnly`,
`TestRefinement82RetryLoserExcludedFromRetailWinnerPostsCOGS` (+19.1 test 1).
Synthetic non-token: `TestPhase9ReferenceRater_AllNativeNonTokenDirectionsRemainDistinct`,
`TestQuantity_Validate_UnknownComponentRequiresSchema` (+19.1 test 2
widget). Connector negotiation: `TestAdapter_TransportNegotiation_StreamingConnector`,
`TestAdapter_TransportNegotiation_NonStreamingOnlyConnector`,
`TestAdapter_PluginFrame_DuplicateAndLateFrames`.
Full TSV: `phase19-1-lifecycle-certification.tsv` (this directory).

## Requirement evidence (representative; full map in TSV)

1.x: tests 1-2 + `TestPhase9ProviderReportedRatingDoesNotRequireLocalTariff`,
`TestAccountingEvidenceToEventPreservesPresenceAndAuthoritativeZero`,
`TestAssembleEconomicDetailAggregateOnly`. 2.x: tests 2-3 +
`TestNativeUsageMeasuresPreserveMultimodalDirectionAndUnits`,
`TestPhase2V2_DecimalNormalizesAndConvertsCheckedNanos`,
`TestProviderDebit_RoundTripsAsRequestScopedObservation`,
`TestRefinement81_ValuationDurability`.
3.x: tests 1-2 + `TestTCK_ActualNormalizerAndReducer`,
`TestNormalize_*`, `TestCorrectionRecoveryCertifyCorrectedAggregateStatementProducesOneIdempotentDelta`.
4.x: test 3 + `TestRefinement81_Input/OutputTransform*`,
`TestPhase6LocalBoundaryRequiredCapabilityDeclinesFastPathBeforeAssessor`,
`TestReconcileLocalEstimateDoesNotOverwriteProviderAuthoritativeUsage`, and
— for 4.5 chunk invariance proper —
`TestPhase19R4ChunkInvariantWholeVsAdversarialPartitions`
(`internal/core/metering/phase19_r4_chunk_invariance_test.go`: whole output vs
five adversarial partitions incl. UTF-8 splits, equal canonical
observations/fingerprints/quantities, estimate-labelled tokens) plus
`TestPhase6BoundaryOutputIsChunkInvariantAndSeparatesCustomerEgress`. The
preflight inexact-tokenizer decline test
(`TestWireComposition_PreflightInexactTokenizerSemantics_DynamicAssessmentDeclinesUnderSamePermit`)
proves only that inexact semantics decline preflight; it is not chunk-invariance
evidence and is no longer cited for 4.5 (see TSV row 49).
5.x: tests 1-3 + `TestNativeUsageMeasures*`,
`TestAdapter_TransportNegotiation_*`, `TestAdapter_PluginFrame_DuplicateAndLateFrames`,
`TestAccountingEvidenceV2FromV1_*`. 6.x: tests 1-2 +
`TestPhase5OperatorCOGS*`, `TestRefinement83OperatorCOGS*`,
`TestPhase7ResourceAllocation*`, `TestPhase25_AggregateMoney_MixedCurrencyAndOverflow`.
7.x: tests 1-2 + `TestPhase9*`, `TestRefinement81_ValuationDurability`.
8.x: tests 1-2 + `TestRefinement83DefaultRetailSelectsWinnerOnly`,
`TestPhase9ReferenceRater_AllDeclaredFixedFeesApplyOnce`,
`TestBindSubmissionIdentityKeepsOneToolLoopOnOneSubmission`,
`TestSQLiteSubmissionFeeSettlementIsClaimedOnceConcurrently`,
`TestCheapCreditScreenAllowsConfiguredHeadroom`,
`TestCustomerUnitOperationReplayAndConflictUseStablePayloadIdentity`,
`TestEconomicJobQueueSQLiteBacklogAgeAndCounts`. 9.x: test 2 (credits) +
`TestProjectAccountWindows_GaugesArePartialResetScopedAndOrderIndependent`,
`TestCodexStream_PreservesConcurrentOutOfOrderWindowSnapshots`,
`TestQuotaPolicy_BindingResetAndAsOfHeadNeverSumGaugeSnapshots`. 10.x:
tests 1-2 + `TestApplyObservations_*`,
`TestCallLegUsageReplayIdenticalIsNoopAndConflictIsIntegrityError`,
`TestRefinementContinuationAfterDoneUsesFreshCallState`,
`TestCutoverIntegratedCrashRestartNoDuplicates`,
`TestF3RestartBetweenAdmitTerminalAndClaimPost`. 11.x: tests 1-2 +
`TestQueryEconomicDetailRetainsAggregatePlane`,
`TestRefinement81_ValuationDurability`,
`TestAccountingEvidenceV2FromV1_IsExplicitlyPartialAndLosslessForTokens`,
`TestQueryEconomicDetailPagination`,
`TestQueryStatementLinesPaginationAndIsolation` (11.4 pagination/isolation),
`TestPhase4ValuationRebuildAndBoundedPagination` and
`TestObservationOutboxJSONB_CorruptStoredRowsFailClosed` (11.5
rebuild-reproduces/drift-detected; see TSV rows 90-91).
12.x: test 1 + `TestMonetaryDiscrepancyDecomposesAcceptanceVector`
(+ reconciliation retention/store suites). For 12.4 versioned tolerances the
lifecycle test retains deltas only; the tolerance proof is
`TestPhase19R4VersionedToleranceCertification`
(`internal/core/billing/phase19_r4_tolerance_certification_test.go`: absolute
and relative max() semantics, boundary equality, v1/v2 policy content
versions with format-version rejection, zero denominators, typed
incomparable/partial/discrepant decisions, gross-vs-signed aggregates) plus
`TestReconciliationToleranceExactFormula`,
`TestReconciliationToleranceZeroDenominator`,
`TestReconciliationToleranceNoMatchingRuleAndUnitMismatch` and
`TestReconciliationTolerancePolicyValidation` (see TSV row 96). 13.x: test 2 (aggregate
statement shape) + `TestCorrectionRecoveryCertify*`,
`TestOperatorReportsStatementEvidenceEndToEnd`,
`TestPhase5OperatorCOGSAppliesChargeCorrectionOnce`. 14.x: tests 1-2
(admission/settlement paths) +
`TestAdapterStrictQuoteRejectsWhenAllRoutesLackCapabilityWithoutSideEffects`,
`TestExecutorStrictCapabilityDenialOpensNoProviderAndWritesNoExposure`,
`TestCheapCreditScreenAllowsConfiguredHeadroom`,
`TestEconomicJobQueueSQLiteBacklogAgeAndCounts`; 14.6 supplier isolation:
`TestPhase14ProviderCostProcessingTakesNoCustomerLock`,
`TestEconomicJobRunnerCustomerProceedsWhileProviderBacklogIncomplete`. 15.x: test files import
only SDK/core/infra (no provider SDK in core paths) +
`TestBuildWithBilling*` (6 names), `TestACP_externalConnectorModulesPresent`,
`TestRefinement4StockObservationToEconomicSettlement`. 16.x: tests 1-2
(query linkage) + `TestQueryEconomicDetailRetainsAggregatePlane`,
`TestAssembleEconomicDetailFinding2RedactedZeroShareDoesNotLeakOrExempt`,
`TestEconomicHealthSnapshotBacklogAgeExcludesTerminalHistory`,
`TestDedupeKeyForBLegIsScopedToBillingCallAndBLeg`; 16.2 sanitizer/lexemes/
access/retention: `TestSafeEvidenceSanitizerMarker` (optional marker
validation + hash binding), `TestSafeEvidenceAcceptsCanonicalEconomicLexemes`,
`TestNormalize_InclusiveInputUsesExactDisjointPartition` (versioned mapping ref
preserved with exact source lexemes),
`TestStatementImportRejectsSecretEvidenceWithoutDisclosure`,
`TestStatementImportUnauthorizedReadsNothing`,
`TestStatementImportScopeFailsClosed`,
`TestStatementImportForeignLineTenantIsScopeMismatchRed`,
`TestReconciliationRetentionRejectsForeignEvidenceAndCrossStoreLeak`,
`TestFinancialTablesRejectDeletes`; 16.6 retention linkage/raw-absent:
`TestRetentionLinkageSurvivesReopen`,
`TestRecovery174RetentionPrunePreservesLinkageAndRecovery`,
`TestRemediation174OptionalRawAbsencePreservesRecovery`. 18.1-18.3: tests 1-3
(new extensibility/retirement-safe lifecycle) +
`TestTCK_ActualNormalizerAndReducer`, `TestB2b3V2ActiveV2AllowedV1ReplayOnly`,
`TestALegReportCycle3RetirementAndReadOnlyNoWrites`,
`TestCurrentJournalRejectsRetiredAuthorizationBook`.

## Deferrals (permitted by task text)

19.2 (dual-dialect/pooler, restart/lifecycle races), 19.3 (enabled/disabled
overhead, `make test-cost`, benchmark refresh), 17.x/18.4-18.6/20.x
(migration/cutover/release gates). Postgres-gated tests were not run here;
SQLite parity suites pass via `make test-unit`.

## Validation

- `go test -run TestPhase191Lifecycle -v -count=1
  ./internal/infra/billingstore/` — PASS (3/3).
- `go test -count=1 ./internal/infra/billingstore/` (full affected
  package, incl. Phase 17/18 shadow/cutover/retirement guards) — PASS.
- `go test -count=1 ./internal/core/billing/
  ./internal/infra/metering/journalstore/` — PASS.
- `make parity-checks` — PASS (exit 0, zero FAIL lines).
- `go vet`, `gofmt -l`, `git diff --check` — clean on changed files.
- `make test-unit` — FAILs only on baseline-owned gates, reproduced
  identically with the 19.1 files moved away (clean `git status` otherwise):
  archtest LOC/shrinkage/migration-baseline ratchets stale after Phases
  5-18 growth, two timing-sensitive runtimebundle relay tests that pass in
  isolation (2.26s/4.88s vs 19s/25s timeouts under full-suite load), and two
  qa gates (Phase 8 census disposition text from Task 18.1; a direct
  `go list` call in a Phase-18 archtest file). The 19.1 package
  (`billingstore`) passes inside the same run. Full failing output preserved
  at `%TEMP%\opencode\phase19-1-test-unit-baseline.txt`; passing runs at
  `%TEMP%\opencode\phase19-1-green.txt`. No RED file: no production behavior
  gap was found, so no RED test persists and `--no-verify` was never used.
