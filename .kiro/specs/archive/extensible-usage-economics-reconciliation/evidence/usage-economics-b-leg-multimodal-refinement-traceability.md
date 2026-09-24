# #620 traceability: parent tasks under the B-leg/multimodal refinement

Normative precedence: parent spec `extensible-usage-economics-reconciliation`
remains authoritative except where the refinement
`usage-economics-b-leg-multimodal-refinement` explicitly narrows it
(`spec.json` `normative_precedence`: multimodal economic identity,
B-leg-rooted inference usage, session and terminal finality, incremental
economic processing, customer retail B-leg selection). For issue #620, where
parent implementation text conflicts with the amendment table below, the
refinement has precedence — but only within that listed scope. Historical
parent text is preserved, not rewritten.

Traceability base: branch `feat/b-leg-usage-economics` at `a15f6ae5`.
That SHA is the inventory base only, not a green release candidate.

## Amendment map (all seven refinement design amendments)

| # | Parent area | Refined interpretation (exact, design.md) | Parent owner task(s) | Implementation / test symbols | Evidence | Disposition at HEAD |
|---|---|---|---|---|---|---|
| 1 | Requirement 2 / D2-D3 | modality support explicitly includes direction and concrete media units/qualifiers | Parent 2.1-2.5 [x] | `metering.ComponentKey` direction/unit/schema; `TestPhase2V2_ComponentKeyDirectionQualifiersAndCanonicalBytes`, `TestPhase2V2_MultimodalNativeUnitsRoundTripFromFixture` (`pkg/lipsdk/metering`); `TestRefinement81_DirectionIsCanonicalIdentity`, `TestRefinement81_MultimodalDirectionRateCertification` (`internal/core/billing`) | `usage-economics-b-leg-multimodal-refinement/evidence/refinement8-1-execution.md` | PASS |
| 2 | Requirement 4 / Tasks 6.1-6.2 | measurement covers media transforms, not only tokenizer-like counting | Parent 6.1, 6.2 [x] | boundary accumulators; `TestPhase6LocalBoundaryRequiredCapabilityDeclinesFastPathBeforeAssessor` (`internal/core/runtime`); `TestRefinement81_InputTransformUsesProviderBoundRepresentation`, `TestRefinement81_OutputTransformPreservesProviderOrigin` (`internal/core/billing`) | `refinement8-1-execution.md` | PASS |
| 3 | Requirement 6 | all request-scoped inference usage is B-leg rooted; A-leg is projection | Parent 5.1-5.4 [x] | terminal ownership (`internal/core/runtime`, `internal/core/billing`); `TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject` (`internal/archtest`); `TestPhase2RepairProviderInferenceRequiresBLegAndAttemptLineage` (`pkg/lipsdk/metering`); `TestRefinement82BillingCallIDIsInvocationBoundary`, `TestRefinement83OperatorCOGSIncludesEveryPayableBLeg` | `refinement8-2-execution.md`, `refinement8-3-execution.md` | PASS |
| 4 | Requirement 8.1 / Task 10.1 | default inference retail basis is policy-selected B-leg quantities; customer-boundary inference usage is not an equal default authority | Parent 10.1 [x] | `SelectRetailBLegEvidence`, `RateSelectedRetailBLegs` (`internal/core/billing`); `TestPhase10RetailSelectorDefaultSelectsSurfacedWinnerOnly`; `TestRefinement83DefaultRetailSelectsWinnerOnly`, `TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts` | `refinement8-3-execution.md` | PASS |
| 5 | Requirement 10 / C6 | terminal is an execution checkpoint, not session economic finality; evidence can advance pre-terminal | Parent 5.x [x]; parent 19.2 [ ] (integrated race/restart gate still open) | C6 lifecycle/worker seams; `TestRefinement82RuntimeResumeKeepsTerminalOwnership`, `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider` (`internal/infra/runtimebundle`) | `refinement8-2-execution.md` | PASS (refinement vectors certified; parent 19.2 gate pending) |
| 6 | Task 9.5 / certification | add mandatory image/audio/video/file input-output vectors | Parent 9.5 [x] | reference rater vectors; `TestRefinement81_MultimodalDirectionRateCertification`, `TestRefinement81_MissingDetailNeverBecomesZeroOrTextTokens` (`internal/core/billing`) | `refinement8-1-execution.md` | PASS |
| 7 | Task 19 / release certification | add resume-after-DONE and incremental pre/post-terminal evidence vectors | Parent 19.1-19.3 [ ] (release certification not run) | `TestRefinement82RuntimeResumeKeepsTerminalOwnership`, `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider`, `TestRefinement82ProviderRevisionPostingsAdvancePerStage`, `TestRefinement82SharedRevisionScenarioSQLite`, `TestRefinement82SharedRevisionScenarioPostgresDirect` | `refinement8-2-execution.md` | PENDING (refinement vectors pass; parent Task 19 gate open) |

## Gate state (honest)

- Parent spec: major tasks 2-11 complete; tasks 1, 12, 13, 14, 15, 16, 17, 18, 19, 20 incomplete (53 checked / 43 unchecked checkbox lines in parent `tasks.md`, subtasks included). No incomplete parent task is marked done by this document.
- Refinement Task 5.3 (`Make A-leg reports rolling as_of projections`) remains BLOCKED after two debug rounds: the report-side proof boundary still accepts incomplete settlement/head/fence/evidence as known economics (blocked annotation in refinement `tasks.md`; attempts preserved in stashes, which this document does not touch).
- Refinement Tasks 8.1, 8.2, 8.3 are approved (`[x]` in refinement `tasks.md`); 8.4 is in progress (traceability subpass A only).
- Task 8.4 may establish the release gate while overall #620 remains BLOCKED/PENDING.

Release rule for #620: the issue cannot close until every parent and
refinement acceptance criterion is green on one recorded future
release-candidate SHA, the full validation gates (`make test`, `make qa`,
`make test-db-parity`, `make test-race` where applicable) pass there,
and refinement Task 5.3 is resolved. Current HEAD `a15f6ae5` is the
traceability base, not a green release candidate.

Gate execution (subpass B, base `a15f6ae5`, 2026-09-18, each inside a
20-minute bound, no timeouts, ~84GB disk free throughout; canonical exit
codes adopted as authoritative, replacing the worker's exit-2 capture
observations): `make parity-checks` PASS (exit 0); `make quality-checks`
FAIL (exit 1) — formatting/modules/build/vet/regex passed, parallel
`adhoc-goroutines` (tracked `economic_revision_worker.go`,
`observation_economic_bridge.go`), `lint` (229 tracked issues), and
`internal/archtest` baseline ratchets failed; `make test` FAIL (exit 1)
— `quality-checks-fast` stopped before `test-unit` with the same parallel
adhoc/lint failures; `make test-db-parity` FAIL (exit 1) — SQLite passed,
PostgreSQL `value_present` int4-vs-boolean baseline; `make qa` FAIL
(exit 1) at `quality-checks-fast`, whose adhoc and lint guardrails fail in
parallel (neither uniquely first); `make test-race` PASS/skip (exit 0,
Windows; cited from the independent review's fresh run). No failure was
fixed or re-run; all are pre-existing tracked baseline issues outside
this docs-only task. Full per-gate table lives in
`refinement8-4-execution.md`. RELEASE DISPOSITION stays BLOCKED/PENDING.

## Post-merge closeout (2026-09-24)

Superseding addendum; the historical status above is preserved. The parent spec
completed through implementation PR #659: head
`e0001f113b661bdab5477a875c595def3d090a66`, merged as
`fc8f01f982f566b87593215895d8ced6f6811ca5` on 2026-09-24T01:13:56Z, with all 37
attached checks SUCCESS on the exact head and the merge tree byte-identical to
the head.

At the merged release candidate:

- Every parent and refinement implementation checkbox is `[x]`; parent tasks 20
  and 20.2 and refinement parent Task 2 are checked.
- Amendment rows 5 and 7 (parent Task 19.x gates) are no longer pending: the
  parent release gates certified at `237ab606` and the merged-head CI certify
  them at the merged baseline.
- Refinement Task 5.3 is resolved through Cycles 1-3 approval. Refinement
  requirement 6.6 (pre-implementation normative integration) is satisfied: #620
  and the parent execution plan treated the refinement as normative before
  implementation, and the merged implementation follows the refined
  B-leg/multimodal/session semantics. The same-SHA release certification belongs
  to parent Task 20 (37 green checks on head `e0001f11`, merged `fc8f01f9`) and
  the archive completion contract, not to 6.6.
- Issue #620 remains OPEN until the archive PR merges; issue #398 remains OPEN
  for other prerequisites.
- Residual risks are unchanged (advisory style lint debt, Windows race skip with
  green Linux race CI, pooler not rerun, POSIX advisory path syntax-only).
