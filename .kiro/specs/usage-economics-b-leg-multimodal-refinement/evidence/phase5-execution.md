# Phase 5 execution evidence

Status: APPROVED

Scope: parent tasks 5.1-5.4, limited to B2BUA terminal evidence ownership,
source-separated V2 closure evidence, strict terminal durability propagation,
and the narrow operator-COGS/retail selection proof. Provider adapters, SDK V2
contract changes, generic rating/reconciliation workers, public host binding,
cutover and Phase 6 boundary measurement were not changed.

Execution window (UTC): 2026-09-12T23:55:46Z to 2026-09-13T02:49:20Z.

## TDD evidence

The focused Phase 1 behavioral REDs were reproduced before the implementation
work. The pre-change commands were:

```text
go test -count=1 -run '^TestRefinement' ./internal/core/billing
go test -count=1 -run '^TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED$' ./internal/core/runtime
go test -count=1 -run '^TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED$' ./internal/core/billing
```

They failed for the expected ownership defects: fixed charges were multiplied
per selected leg (`606` rather than `603`), an attempted leg with missing
provider evidence was returned as reconciled/present zero, and the V1
`FinalBillingEvidence` bridge overwrote stream cost with finalizer evidence.
The implementation added behavior-first Phase 5 tests before the corresponding
production repairs. The new tests cover source separation, replay/conflict,
drain ownership, strict post-output durability, all-leg COGS, partial
completeness, graph coverage, payer separation and durable V2 payload replay.

The trusted-store repair was also TDD-driven. Before the repair, these focused
commands failed at compile time because the runtime identity had no StoreID
field and the phase test draft had no storeID carrier:

```text
go test -count=1 ./internal/core/runtime -run 'TestPhase5TrustedStoreIDScopesV2ObservationToJournalStore|TestPhase5MissingTrustedStoreIDKeepsV1Only' -v
go test -count=1 ./internal/infra/runtimebundle -run 'TestComposeBillingUsesAuthoritativeStoreIDForBillingIdentity|TestComposeBillingPreservesCustomStoreIDResolver' -v
```

The first reported `draft.storeID undefined (type billingLegDraft has no field
or method storeID)`; the second reported `BillingIdentity.StoreID undefined`
and an unknown struct field. After the repair both commands passed, and the
runtime test proved store-a observations append to a store-a journal while a
store-b journal rejects them. A missing resolver keeps V1-only evidence without
the former `lip.runtime` synthetic StoreID.

## Implementation evidence

- `CallLegUsageRecord` carries an additive version-2 envelope containing
  bounded, canonicalized and cloned `metering.Observation` values, immutable
  observation references and visible source conflicts. Existing V1
  `FinalBillingEvidence` remains an explicitly labelled compatibility
  projection; it is not used to erase source-separated observations.
- Runtime conversion is one provider-neutral `lipapi.Event` mapping. It keeps
  canonical token presence, optional provider-reported cost, source,
  acquisition, authority, perspective, dedupe identity, and trusted
  BillingCallID/A-leg/B-leg/attempt lineage. The trusted durable StoreID is
  resolved once with request identity and carried into each B-leg observation;
  absent StoreID keeps the V1 projection without emitting fake V2 evidence. No
  provider wire payload is parsed; resource/account-window subjects are not
  assigned a synthetic B-leg.
- `attemptSession` retains a bounded source-tagged evidence set (1024 entries),
  clones it at terminal snapshot, deduplicates exact source replays, records
  changed same-identity payloads as conflicts, and drains it before closure,
  including duplicate/invalid terminal exits. Cancellation finalizer evidence
  is captured before the shared finalizer claim is consumed.
  Terminalization is still exactly-once per B-leg; a later invocation on the
  same A-leg keeps the existing fresh BillingCallID/B-leg behavior.
- Stream, sideband/accumulated and finalizer evidence are converted as
  separate observations. The V1 projection selects a compatibility scalar
  without destructive source merging. Receive paths remain free of journal,
  rating and money writes; the existing `TerminalUsageSink` remains the sole
  strict terminal append seam.
- Strict call-leg/call append errors are returned through terminal effects,
  use a detached bounded handoff context after request cancellation, and do
  not trigger upstream retry after output commitment. Existing append
  transaction/replay and pending-work mechanisms remain the durability owner;
  additive V2 fields persist in the existing JSON payload and legacy rows stay
  readable without schema changes.
- `AttributeOperatorCOGS` includes executed payable winner, retry, failed,
  canceled and loser B-legs, excludes never-started shells, keeps native
  currencies/payment parties separate, reports known subtotal plus partial
  completeness for unknown attempts, and applies SDK coverage/supersession
  validation plus the Phase 3 reducer before attribution. Inclusive covered
  child charges are not double-counted, superseded charge revisions replace
  their effective predecessor while retaining both immutable observations,
  pending/contradictory graphs are non-payable, and customer (BYOK) charges
  are excluded from operator payable. The narrow retail selector remains
  independent and defaults to the surfaced winner; fixed call-scoped charges
  are applied once per call.

## Acceptance mapping

| Acceptance case | Evidence |
|---|---|
| Stream and differing finalizer usage/cost survive together | `TestPhase5TerminalEvidenceRetainsStreamAndFinalizerSources` |
| Exact sideband replay is one observation; changed payload is visible | `TestPhase5TerminalEvidenceReplayAndConflictAreVisible` |
| B-leg drain cannot leak evidence to a later B-leg | `TestPhase5AttemptEvidenceDrainIsIndependentOfLaterBLeg`, `TestPhase5DuplicateTerminalExitDrainsLateEvidence` |
| Retry/failed/loser/winner COGS totals 3+5+2 while retail selects winner | `TestPhase5OperatorCOGSIncludesAllExecutedLegsAndRetailSelectsWinner` |
| Unknown attempted cost is partial; never-started shell is excluded | `TestPhase5OperatorCOGSMissingAttemptIsPartialAndNeverStartedIsExcluded` |
| Inclusive parent/child, pending graph, contradictory graph and BYOK behavior | `TestPhase5OperatorCOGSUsesInclusiveParentOnceAndRejectsPendingOrBYOK`, `TestPhase5OperatorCOGSRejectsContradictoryCoverage`, `TestPhase5OperatorCOGSMarksPendingSupersessionIncomplete` |
| Effective charge correction replaces the superseded amount once | `TestPhase5OperatorCOGSAppliesChargeCorrectionOnce` |
| Strict sink failure after output does not retry provider and preserves context-independent handoff | `TestPhase5StrictTerminalDurabilityFailureDoesNotRetryProviderInference`, `TestBillingAppendRetryOutputPersistenceFailureAfterSuccess` |
| Existing V1 storage remains compatible while V2 survives append/replay | `TestSQLiteAppendCallLegUsagePersistsV2EvidenceAndKeepsReplayStable` and existing billing-store V1 suite |
| Trusted StoreID scopes runtime V2 observations; missing resolver stays V1-only | `TestPhase5TrustedStoreIDScopesV2ObservationToJournalStore`, `TestPhase5MissingTrustedStoreIDKeepsV1Only`, `TestComposeBillingUsesAuthoritativeStoreIDForBillingIdentity`, `TestComposeBillingPreservesCustomStoreIDResolver` |
| No stream-time money/journal write regression | `TestRuntimeStreamHandlersStayOffJournalRatingSettlement`, `TestPhase51HoldDeletionAndNoStreamMoneyRatchetsStayActive` |
| Same A-leg later calls retain independent closure identities | existing `TestExecuteResumeSameALegAllocatesDistinctBillingCallIDs` and billing call-ID contract tests |

## Verification

Focused GREEN commands:

```text
go test -count=1 ./internal/core/billing/...
go test -count=1 ./internal/core/runtime/...
go test -count=1 ./internal/infra/billingstore/...
go test -count=1 ./internal/core/runtime -run '^TestPhase5_' -v
go test -count=1 ./internal/core/billing -run 'Phase5|Operator|Retail|BYOK|Coverage|Supersession' -v
go test -count=1 ./internal/infra/billingstore -run '^TestSQLiteAppendCallLegUsagePersistsV2EvidenceAndKeepsReplayStable$' -v
go test -count=1 ./internal/infra/runtimebundle -run 'TestComposeBillingUsesAuthoritativeStoreIDForBillingIdentity|TestComposeBillingPreservesCustomStoreIDResolver' -v
go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase4ObservationAppendProjectsArbitraryComponentsAndCharges|TestPhase4ObservationReplayRevisionAndStoreIsolation' -v
go test -count=1 ./internal/archtest -run 'TestRuntimeStreamHandlersStayOffJournalRatingSettlement|TestPhase51HoldDeletionAndNoStreamMoneyRatchetsStayActive|TestRequestAttemptStateBaselineMatchesCurrentAST|TestRequestAttemptStateRatchetsPassOnCurrentCode|TestRequestAttemptStateTargetRatchetFailsIfTypeReappearsOnCurrentAST' -v
go vet ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/infra/metering/journalstore/... ./internal/archtest/...
git diff --check
```

Results: the billing, runtime and billing-store package suites passed; the
Phase 5 tests, V2 persistence replay, trusted StoreID/runtimebundle checks and
journalstore store-isolation checks passed; `go vet` and `git diff --check`
passed. The required combined command was also run:

```text
go test -count=1 ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingstore/... ./internal/archtest/...
```

The billing, runtime and billing-store suites passed in the latest combined
invocation. It was not a full repository GREEN result because archtest
retained these baseline failures unrelated to this phase:

- forbidden `pkg/lipsdk/economics/contracts.go: type Rater`;
- `internal/core` line-complexity budget over the existing ceiling;
- malformed `GOWORK=off` root module graph import path containing a space.

Those baseline failures were recorded rather than weakened or hidden. The
targeted no-stream-money and runtime AST contract checks passed, including the
request-attempt direct-copy ratchet after StoreID propagation was reduced to
the existing budget.

## Residual risks and boundaries

Phase 5 fails closed for native resource/account-window observations at the
B-leg closure boundary and does not fabricate a B-leg or zero. The positive
conserved allocation contract is explicitly owned by parent task 11.2 and is
intentionally not implemented here; those native subjects remain unattributable
until that contract is available. Provider-specific parsing, multimodal
producer migration, generic V2 rating, reconciliation workers and cutover
remain intentionally unimplemented.

Owned Go-file count: 35 (modified or added in this worktree; no worker commits).

Root review is APPROVED; see `phase5-review.md`.
