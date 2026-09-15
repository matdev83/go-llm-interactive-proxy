# Refinement phase 1 execution and rebaseline

## Scope and provenance

- Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`
- Branch: `feat/b-leg-usage-economics`
- Rebaseline HEAD: `8eaa8e484b945da85df5e3a788f2e54c4ce2e7eb` (`feat(economics): migrate refined resource producers`)
- Refinement: `usage-economics-b-leg-multimodal-refinement`, approved and `ready_for_implementation`
- Parent work order: `extensible-usage-economics-reconciliation`, issue `#620`
- Original RED baseline: [`phase1-execution-baseline.md`](phase1-execution-baseline.md), captured at `d1847d5e2acdbb873779d94418b6347c92b0ab7d` before the phase-1 repair artifacts were added in `c985a102`.
- This pass is a test/fixture rebaseline only. No production source, task checkbox/status, commit, merge, or PR was changed.
- Existing phase-1 tests and the multimodal fixture were reused and refined. The original RED assertions were not weakened or rewritten to manufacture a current failure.

## Acceptance coverage reused

### Task 1.1: authority architecture

The existing coverage establishes the authority split at its observable boundaries:

- `internal/core/billing/call_usage_test.go:58` (`TestCallUsageRecordSealAssignsBillingCallKeyWithoutEmbeddingLegs`) seals call closure under `BillingCallID`, keeps only expected B-leg identity strings, and rejects embedded leg payloads.
- `internal/core/billing/call_usage_test.go:87` (`TestCallLegUsageRecordIsIndependentOfTURAndKeyedByCallPlusBLeg`) keeps inference evidence on the concrete B-leg identity.
- `internal/core/billing/call_id_test.go:97` and `:131` prove provider-cost keys are `BillingCallID + B-leg` and later calls on one A-leg/session receive distinct commercial operations.
- `internal/core/runtime/usage_economics_refinement_phase1_test.go:67` (`TestRefinementContinuationAfterDoneUsesFreshCallState`) executes two real terminal invocations. The second invocation reuses the A-leg but receives a new `BillingCallID` and B-leg, while each DONE closure freezes only its own call's B-leg.
- `internal/archtest/phase1_usage_economics_guards_test.go:19` and `:152` retain the public non-money construction boundary and single monetary-writer guard. `internal/archtest/phase1_a_leg_authority_test.go:21` structurally rejects direct `SubjectALeg` construction in core production code, while allowing A-leg correlation fields. Together with the runtime continuation test, this prevents an A-leg/session-final billing shortcut from becoming an authority path.

The prior RED source-boundary test remains in
`internal/core/runtime/usage_economics_refinement_phase1_test.go:24` and is now green at this HEAD because later V2 source-separated capture landed. Its original failure is preserved in the baseline evidence; it is not relabelled as a new RED.

### Task 1.2: retail selection and COGS characterization

- `internal/core/billing/usage_economics_refinement_phase1_red_test.go:67` (`TestRefinementRetailSelectionMatrixCharacterization`) covers completed surfaced/winner, explicit all-attributable retry/loser/winner, rejected/never-started exclusion, and interrupted latest-accepted selection.
- `internal/core/billing/usage_economics_refinement_phase1_red_test.go:128` (`TestRefinementProviderCostMatrixCharacterization`) covers independent operator-payable COGS for failed retry, race loser, and surfaced winner.
- `internal/core/billing/retail_selector_phase10_test.go:41`, `:103`, `:133`, and `:155` cover surfaced/winner, named outcomes, all-attributable, explicit cost pass-through, and stable interrupted ordering with frozen policy references.
- `internal/core/billing/cost_pass_through_phase10_test.go:41` covers provisional, final, pending, missing-cost, and trusted/incomparable provider-cost states.
- `internal/core/billing/usage_economics_refinement_phase1_red_test.go:14` remains the original call-scoped fixed-fee RED assertion. The later retail implementation now satisfies its unchanged `603` expectation.

The stale expectation in
`internal/infra/billingcompose/snapshot_independence_test.go:83` was corrected:
`TestResolveCallRatingFailoverSettlesSurfacedModelCard` now expects
`1003`, because `seedCatalog` enables a call-scoped request fee of `3` in
addition to the surfaced failover winner's `1000` input charge. The original
`1000` assertion was a stale test expectation, not an unresolved production
failure.

### Task 1.3: multimodal and continuation fixtures

- `pkg/lipsdk/metering/testdata/refinement_phase1_multimodal_vectors.json` contains twelve named vectors for image, audio, video, document, mixed/derived, continuation, pre-terminal revision, and post-terminal correction. Input/output directions retain native units, transform boundaries, provider/customer values, call IDs, A-leg/B-leg lineage, revision, terminal, and supersession metadata. The normative video pair is now provider-native `token` input and generated `second` output, with distinct rate and generation/tokenizer qualifiers.
- `pkg/lipsdk/metering/usage_economics_refinement_phase1_red_test.go:34` (`TestRefinementPhase1MultimodalFixtureInventory`) checks all twelve names, native units (including the valid video input token unit), non-empty transform qualifiers, transformed values/boundaries, distinct video rate qualifiers, lineage, revision, and the correction supersession link. It intentionally remains an inventory check; durable V2 execution/correction certification belongs to its dependent tasks.
- `internal/core/runtime/usage_economics_refinement_phase1_test.go:67` supplies the real same-A-leg continuation vector: call 1 reaches terminal closure, call 2 resumes through the returned continuation identity, and both closures carry independent `BillingCallID`/B-leg lineage.

The fixture and test coverage is traceable to the original phase-1 artifact list and results in [`phase1-execution-baseline.md`](phase1-execution-baseline.md), especially its changed-test inventory and RED command table. No additional acceptance vector is claimed here without a source fixture or existing regression anchor.

## Fresh verification

Commands below were run at the rebaseline HEAD above.

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 -run '^TestRefinement|^TestPhase1' ./internal/core/billing/... ./internal/core/runtime/... ./internal/archtest/...` | 0 | Named refinement and phase-1 billing/runtime/architecture tests pass. |
| `go test -count=1 -run '^TestRefinement|^TestPhase1' ./pkg/lipsdk/metering/...` | 0 | Named multimodal inventory and phase-1 metering tests pass. |
| `go test -count=1 -run '^TestRefinementRetailFixedFeeIsAppliedOncePerCall_RED$' ./internal/core/billing` | 0 | Original `603` fixed-fee assertion is now green after later retail implementation. |
| `go test -count=1 -run '^TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED$' ./internal/core/billing` | 0 | Original attempted/missing-evidence assertion is now green after later provider-cost handling. |
| `go test -count=1 -run '^TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED$' ./internal/core/runtime` | 0 | Original source-separation assertion is now green after later V2 capture. |
| `go test -count=1 -run '^TestRefinementContinuationAfterDoneUsesFreshCallState$' ./internal/core/runtime` | 0 | Real two-invocation continuation/terminal closure characterization passes. |
| `go test -count=1 -run '^(TestRefinementRetailSelectionMatrixCharacterization|TestRefinementProviderCostMatrixCharacterization|TestRefinementNeverStartedEvidenceMayRemainKnownZero)$' ./internal/core/billing` | 0 | Selection, all-leg COGS, and never-started known-zero characterizations pass. |
| `go test -count=1 -run '^TestRefinementPhase1MultimodalFixtureInventory$' ./pkg/lipsdk/metering` | 0 | Twelve-vector inventory passes. |
| `go test -count=1 -run '^(TestPhase1|TestUsageAuthority)' ./internal/archtest/...` | 0 | Phase-1 authority/writer/census and usage-authority architecture guards pass. |
| `go test -count=1 ./pkg/lipsdk/metering/... ./internal/core/runtime/...` | 0 | Task 1.3 focused metering/runtime packages pass. |
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingcompose/...` | 0 | Billing and composition tests pass after correcting the stale fixed-fee expectation to `1003`. |
| `make parity-checks` | 0 | Core, frontend, backend, metering, compatible-parity, conformance, and changed connector parity checks pass. |
| `git diff --check` | 0 | No whitespace errors. |

The structural A-leg guard command
`go test -count=1 ./internal/archtest -run '^TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject$'`
also passed. It is intentionally syntactic: it blocks direct core construction
of a metering `SubjectALeg`, while Task 5 remains the owner of deeper runtime
closure/rolling-report behavior.

The broader package command
`go test -count=1 ./internal/core/runtime/... ./internal/core/billing/... ./internal/archtest/...`
also ran and exited `1`, but its failures are pre-existing branch-wide ratchets outside this test/fixture boundary: request-attempt AST baseline (`369 > 323`), billing import boundary (`lipapi`), package/line-complexity budget drift, hexagonal migration baseline drift, compaction-continuity field baseline drift, and the malformed `GOWORK=off` import-path plan. The named phase tests above remain green.

## RED_PHASE_OUTPUT

`RED_PHASE_OUTPUT` is historical evidence, not a newly fabricated failure. The
original failing commands and outputs are preserved in
[`phase1-execution-baseline.md`](phase1-execution-baseline.md):

- `go test -count=1 -run '^TestRefinement' ./internal/core/billing` exited `1`: fixed-fee assertion expected `603` and observed `606`; attempted missing provider evidence observed `AmountPresent=true Reconciled=true Nano=0` (baseline lines 35–37 and 237).
- `go test -count=1 -run '^TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED$' ./internal/core/runtime` exited `1`: `mergeStreamCostOntoLeg` copied stream cost into finalizer evidence (baseline lines 73–75 and 240).
- `go test -count=1 -run '^TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED$' ./internal/core/billing` exited `1`: V1 returned reconciled/present zero instead of unreconciled evidence (baseline lines 246–250).

## Original RED evidence and current gaps

The original RED outcomes are not inferred from current green runs. They are
recorded in the historical baseline linked above and were not rerun as current
REDs:

- fixed fee: expected `603`, observed `606` (baseline lines 35–37 and 237);
- attempted missing provider evidence: observed reconciled/present zero (baseline lines 35–37 and 250);
- finalizer/stream source mixing: the V1 bridge copied stream cost into finalizer evidence (baseline lines 73–75 and 240).

Those assertions now pass at the rebaseline HEAD because later implementation phases changed the covered behavior. This file therefore records a rebaseline, not a claim that those historical REDs still reproduce.

Remaining risks and gaps are explicit:

1. The twelve multimodal vectors are intentionally fixture/inventory coverage in phase 1. Durable pre-terminal append, revision-triggered valuation, post-terminal correction, and rolling A-leg reports require their later task owners and are not recertified by this artifact.
2. The structural A-leg guard blocks direct core `SubjectALeg` construction but does not prove every possible semantic/session-final path; Task 5 remains the dependency for behavioral closure and rolling-report certification.
3. The broad runtime/billing/architecture command still exposes unrelated branch-wide baseline ratchet failures listed above. They are not silently treated as phase-1 passes.
4. No race or Windows test-cost claim is made in this rebaseline. The prior Windows cgo/race and test-cost limitations remain recorded in [`phase1-execution-baseline.md`](phase1-execution-baseline.md).

## Worktree result

The only changes from the preflight-clean worktree are this evidence file, the strengthened inventory test, the normative multimodal fixture update, the stale-expectation test correction, and the structural architecture test. No production implementation Go file, task status, or unrelated user change was modified.
