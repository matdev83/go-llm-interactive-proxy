# Refinement 8.3 execution: COGS-versus-retail selector certification

Status: `READY_FOR_REVIEW_REFINEMENT_8_3` (subpasses A, B, and C
complete: domain selector/allocation certification plus durable integrated
cross-authority proof with restart and explanation. Task 8.3 awaits
independent review, not yet approved.)

## Scope

This record certifies Kiro Task 8.3 (`Run COGS-versus-retail selector
certification`) for spec `usage-economics-b-leg-multimodal-refinement`,
requirements 6.5, 2.3, 2.4, 2.5, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6 across
subpasses A (selector/inference retail), B (cost pass-through and
non-request allocation), and C (durable integrated proof).

Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`,
branch `feat/b-leg-usage-economics`. Task 8.3 depends on approved Task 8.2.

Out of scope (untouched): production code, existing tests, tasks.md,
spec.json, stashes, Task 5.3, staging/commits/branch. This is a
certification/test subpass. No production `*.go` file was changed.

Subpass A certifies all-operator-payable B-leg COGS versus default
winner-only and explicit retry-inclusive retail selection, including
retry, failover parallels (race loser), parallel-loser, and
canceled-but-payable attempts, with direction-qualified multimodal
quantities. The two authorities remain separate and explainable from
immutable contribution/observation refs.

## Requirement-to-test matrix (subpass A)

One BillingCallID fixture (`ref83Fixture`): failed retry `b-retry` (seq 1),
race loser `b-loser` (seq 2), canceled-but-payable `b-canceled` (seq 3),
surfaced winner `b-winner` (seq 4), customer-payer failed `b-byok` (seq 5),
never-started shell `b-shell` (seq 6). Every executed leg carries one
provider observation with direction-qualified multimodal measures plus one
aggregate reported charge (operator payer, except BYOK customer payer).

| Required case | Certifying test(s) | What is asserted |
|---|---|---|
| 2.3 all-payable COGS incl. failed/retry/canceled/loser; excl. never-started/non-payable/BYOK | `TestRefinement83OperatorCOGSIncludesEveryPayableBLeg` via `AttributeOperatorCOGS` | exact 7.50 USD subtotal (1.25+0.75+2.00+3.50), known+payable, included keys exactly the four payable legs, excluded exactly BYOK+shell, no unknowns/pending, every included observation fingerprints and resolves a replay-stable ref |
| 5.1/5.2 default winner-only basis | `TestRefinement83DefaultRetailSelectsWinnerOnly` via `SelectRetailBLegEvidence` | exactly `b-winner` selected, frozen policy ref `retail-policy/v10`, mode/reason `surfaced_winner`, basis independent, complete V2 capability, exactly the winner observation ref |
| 5.3 retry/loser usage excluded from default retail; 5.4 asymmetric rates; 5.5 single call fee; 5.6 proxy line separate | same test via `RateSelectedRetailBLegs` | exact 2.97 USD (0.97 winner quantities + 1.00 call fee + 1.00 proxy); 5 inference + 1 commercial + 1 proxy lines; no unselected ref in any inference line; proxy line separately named `proxy_egress_bytes` with kind `proxy_service`, never as inference; summary total equals charge; deterministic replay |
| 5.2/5.3 explicit retry-inclusive policy with frozen version | `TestRefinement83RetryInclusiveRetailSelectsAttributableAttempts` | all-attributable selection is exactly the five executed legs in attempt order with frozen policy identity; exact 5.77 USD (winner 0.97 + retry 0.30 + loser 0.80 + canceled 1.60 + BYOK 0.10 + 1.00 fee + 1.00 proxy); call fee appears exactly once across five legs; every attributable observation present in retail lines |
| 5.1 settlement through production seam | `TestRefinement83RateCallSettlesWinnerOnlyThroughSelection` via `RateCall` | exact 1.97 USD through the settlement seam (same winner quantities + fee, no proxy input); non-empty fingerprint; replay-stable |
| 6.5/2.3 authority separation + clone immutability | `TestRefinement83AuthoritiesStaySeparateAndCloneStable` | unselected retry quantity mutation changes neither COGS nor default retail; unselected retry charge mutation changes COGS but not retail; cloned call/legs reselect and re-rate to identical selection identity, fingerprint, valuation ID, and charge |
| Multimodal direction-qualified quantities (2.3-2.5 context, 6.1 reuse) | all tests share the fixture | input image, output image, input audio seconds, output audio seconds, document input pages as distinct direction/component/unit keys; no merged direction/unit; customer prices are independent literals, never derived from provider charges |

## Requirement-to-test matrix (subpass B)

Subpass B reuses the subpass A fixture read-only and adds an authoritative
provider cost (`lur-83`/`valuation-83`/rev 3, 7.50 USD, reconciled) plus one
genuine $12.00 prompt-cache resource allocation (winner 1/2, retry 1/4,
explicit 1/4 remainder, exactly conserved).

| Required case | Certifying test(s) | What is asserted |
|---|---|---|
| 5.2 explicit pass-through basis, policy/version-bound, settled via production seam | `TestRefinement83CostPassThroughSettlesAcceptedProviderCost` via `RateCall` | selection stays winner-only (no silent all-usage retail); exact 7.50 USD charge distinct from 1.97/5.77 retail meanings; no tariff consulted; final settlement retains policy ref/version/bound, 10.00 bound with 7.50 posted, full provider-cost identity and stable semantic fingerprint; source input unmutated; deterministic replay |
| 5.2 missing/untrusted/incomparable costs fail closed or stay bounded | `TestRefinement83CostPassThroughMissingCostStaysBounded` via `RateSelectedRetailBLegs` | pending posts zero with bound retained; provisional posts bound; untrusted → `ErrCostPassThroughProviderUntrusted`, EUR → `ErrCostPassThroughCurrencyMismatch`, over-bound → `ErrCostPassThroughBoundExceeded` |
| 2.4/2.5 conserved non-request allocation, source-preserving, off retail | `TestRefinement83NonRequestAllocationConservesAndStaysOffRetail` via `ConserveAllocation` + `AttributeOperatorCOGSWithAllocations` | exact shares 1/2+1/4+1/4 with stable fingerprints; exact 16.50 USD subtotal (7.50 legs + 6.00 + 3.00, remainder retained not posted); included legs unchanged; lines keep source resource/account/period, allocation/policy/target identity, `InferenceEligible` false; identical lines over a smaller leg set (never multiplied); winner-only selection and 1.97 USD retail unchanged |
| 6.5/5.2-5.5 cross-authority proof | `TestRefinement83CrossAuthorityUsesAcceptedCostOnly` | operator 16.50 = payable B-leg 7.50 + conserved allocation 9.00; pass-through 7.50 uses accepted selected cost only (never the allocation, never unselected retry/loser cost); legs unmutated; all amounts USD |

## Requirement-to-test matrix (subpass C — durable integrated proof)

One file-backed billing store (`ref83` identity, restart via close/reopen
of the same files through `openRefinement82FileBillingStore`). Call-A
carries failed retry `b-retry` ($1.25, image-in ×3, operator-payable),
surfaced winner `b-winner` ($3.50, multimodal: image in/out, audio seconds
in/out, document pages, operator-payable), never-started shell `b-shell`
(no evidence, excluded), and failed `b-byok` ($5.00 customer-payer charge,
excluded from operator COGS). Call-B carries one winner leg for the
pass-through head. No continuity/session APIs are used.

| Required case | Certifying test(s) | What is asserted |
|---|---|---|
| 6.5/2.3 durable all-payable COGS; 5.1/5.2 winner-only vs retry-inclusive differ | `TestRefinement83IntegratedCrossAuthorityPersistsAndExplains` (domain seams `AttributeOperatorCOGS`, `SelectRetailBLegEvidence`, `RateCall`) | COGS exact 4.75 USD with included keys exactly retry+winner and excluded exactly BYOK+shell; winner-only selection `[b-winner]`; `RateCall` exact 1.97 USD; retry-inclusive selects retry+winner+byok; every posting derived from the COGS included set with per-leg operator-charge amounts (shell fails `ErrUnreconciledCost`, BYOK derivation fails closed) |
| 6.5 exact per-leg contribution refs durable | same test via `GetCallLegUsage`, `CallExplanation`, file close/reopen | every included leg's exact `ObservationRef` (store/observation/revision/hash) matches the durable leg row and the explanation leg; shell row carries no observation lineage; identical ref tuples after restart |
| 6.5/2.3 multimodal lineage to durable explanation; restart; no session finality | same test via `GetCallLegUsage`, `CallExplanation`, file close/reopen | winner observation with all five direction-qualified quantities read back; explanation holds 4 legs with exact customer/provider operation snapshots (source keys, fingerprints, kinds, currencies) and exact journal lineage; result customer 1.97 + provider 4.75; post-restart account/legs/fingerprint/operations identical plus full journal-transaction equality (IDs, source keys, kinds, turn/B-leg/correction lineage, currencies, sequences, ordered entries/amounts) |
| Durable journals/heads/balances keep refs; replay duplicates nothing | same test via `AppendCallLegUsage`, `ApplyProviderCost`, `ApplyCallBillingResult`, `JournalTransactions`, `GetAccount` | 2× `provider_call_cogs` (1.25+3.50), exactly 1× `customer_call_settlement` (1.97), balance 8.03, identical replay `Replayed`, conflicting provider payload fails closed |
| 5.2 pass-through lineage persisted; replay idempotent; revision/conflict closed | `TestRefinement83IntegratedPassThroughHeadRevisionAndConflict` via `RateCall` + `ApplyCallBillingResult` + `ApplyCostPassThroughRevision` + `CallExplanation` + close/reopen | initial head from the `RateCall` result (final 2.00, LUR/valuation/rev-1/hash + policy ref/version/bound retained, `OriginalTransactionID` bound to the settlement journal); identical settlement replay; durable explanation exposes the exact customer operation snapshot (source key, head-bound fingerprint, kind, currency); identical revision replays; rev-2 posts exactly −0.50 with exact adjustment source key; older revision stale; same-revision change → `ErrCostPassThroughRevisionConflict`; balance 8.50; exactly one adjustment journal; unrelated $12 allocation nowhere in the 2.00 charge; post-restart operation snapshot, result, full journal-transaction equality, replay invariance (set unchanged), balance, and journals identical |
| 2.4/2.5 allocation persists subject/policy/weights/remainder; once; no synthetic B-leg; never retail inference | `TestRefinement83IntegratedAllocationPersistsWithoutSyntheticLegs` via `AppendAllocation`, `GetAllocation`, `ListAllocations`, `ListCallLegUsage` | byte-exact canonical round-trip; shares 1/2+1/4+1/4; source/policy identity; listing contains the record; exactly the 2 executed legs durably (no synthetic row); winner-only selection over persisted legs unchanged |

## RED / GREEN (subpass C)

RED (coverage gap, honestly described):

```text
go test ./internal/infra/billingstore/ -run 'TestRefinement83Integrated' -count=1 -v
# testing: warning: no tests to run; ok [no tests to run]
```

RED (first integrated run — three test-fixture expectation errors, no production defect):

```text
# AdmitExposure: policy has no chargeable dimensions (winner policy lacked Include flags)
# AdmitExposure: insufficient safety margin for Max 100 USD on a 10.00 balance
# balance 8.03, want 98.03 (test arithmetic used a 100.00 balance; the account holds 10.00)
```

All three were fixture/arithmetic corrections with no production change;
the 8.03 balance additionally confirmed provider COGS postings never touch
the customer balance (journals showed exactly 1.97 + 1.25 + 3.50).

GREEN:

```text
go test ./internal/infra/billingstore/ -run 'TestRefinement83Integrated' -count=1 -v
# PASS all 3 tests
go test ./internal/infra/billingstore/ -run 'TestRefinement83Integrated' -count=5
# ok (15.366s)
go test ./internal/core/billing/ ./internal/infra/billingstore/ -run 'TestRefinement83' -count=3 -shuffle=on
# ok billing, ok billingstore
```

RED (final review remediation — new exact assertions, production-true
failure, no production defect): the shell cannot-post proof first hit
`ErrProviderCostRevisionAuthority` instead of the asserted
`ErrUnreconciledCost`, because an unauthoritative result fails the
authority guard before the monetary guard. Isolated by claiming authority
without a reconciled amount, which reaches `ErrUnreconciledCost` exactly.
The added BYOK leg rippled COGS-excluded, inclusive-selection,
explanation, and restart counts as predicted.

GREEN (final remediation — new journal-equality assertions passed first
run; the gap was missing coverage, behavior already correct):

```text
go test ./internal/infra/billingstore/ -run 'TestRefinement83Integrated' -count=1 -v
# PASS all 3 tests
go test ./internal/infra/billingstore/ -run 'TestRefinement83Integrated' -count=5
# ok
go test ./internal/core/billing/ -run 'TestRefinement83RetryInclusive' -count=1
# PASS
go test ./internal/core/billing/ -run 'TestRefinement83RetryInclusive' -count=5 -shuffle=on
# ok
go test ./internal/core/billing/ ./internal/infra/billingstore/ -run 'TestRefinement83' -count=3 -shuffle=on
# ok billing, ok billingstore
```

RED (coverage gap, honestly described — no TestRefinement83 coverage existed):

```text
go test ./internal/core/billing/ -run 'TestRefinement83' -count=1 -v
# testing: warning: no tests to run; ok [no tests to run]
```

RED (subpass B coverage gap — no pass-through/allocation certification existed):

```text
go test ./internal/core/billing/ -run 'TestRefinement83Cost|TestRefinement83NonRequest|TestRefinement83CrossAuthority' -count=1 -v
# testing: warning: no tests to run; ok [no tests to run]
```

RED (first certification run — two test-side expectation errors, no production defect):

```text
go test ./internal/core/billing/ -run 'TestRefinement83' -count=1 -v
# --- FAIL: TestRefinement83OperatorCOGSIncludesEveryPayableBLeg:
#   included legs sorted [b-canceled b-loser b-retry b-winner], want attempt order
# --- FAIL: TestRefinement83DefaultRetailSelectsWinnerOnly:
#   retail lines = 7, want 6 (proxy-service line also present in combined valuation)
```

Both were wrong test expectations against correct production semantics:
`AttributeOperatorCOGS` sorts `IncludedLegKeys`, and the combined retail
valuation embeds the proxy-service line alongside inference/commercial
lines. Expectations corrected with no production change.

GREEN (subpass B — first run green with exact-amount assertions; no
production defect, no expectation repair needed):

```text
go test ./internal/core/billing/ -run 'TestRefinement83Cost|TestRefinement83NonRequest|TestRefinement83CrossAuthority' -count=1 -v
# PASS all 4 tests
go test ./internal/core/billing/ -run 'TestRefinement83Cost|TestRefinement83NonRequest|TestRefinement83CrossAuthority' -count=5 -shuffle=on
# ok
go test ./internal/core/billing/ -run 'TestRefinement83' -count=1
# ok (all 9 subpass A+B tests)
go test ./internal/core/billing/ -run 'TestRefinement83' -count=3 -shuffle=on
# ok
```

## Validation gates

- Focused subpass A tests `-count=1` -> PASS (5 tests); `-count=5 -shuffle=on` -> ok.
- Focused subpass B tests `-count=1` -> PASS (4 tests); `-count=5 -shuffle=on` -> ok.
- Focused subpass C tests `-count=1` -> PASS (3 tests); `-count=5` -> ok (15.366s).
- Review remediation: remediated retry-inclusive test `-count=1` and `-count=5 -shuffle=on` -> ok; remediated integrated tests `-count=1` and `-count=5` -> ok; all `TestRefinement83` billing+billingstore `-count=3 -shuffle=on` -> ok both packages; full `billing/...` + `billingstore/...` suites -> PASS; `make parity-checks` -> exit 0.
- Final journal-equality remediation: integrated `-count=1` and `-count=5` -> ok; retry-inclusive `-count=5 -shuffle=on` -> ok; all-83 `-count=3 -shuffle=on` -> ok; suites PASS; `make parity-checks` exit 0; `go vet`, `gofmt -l`, `git diff --check`, scans clean.
- `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/...` -> PASS (post-remediation).
- Cited runtimebundle 8.2 posting pair `-count=1 -shuffle=on` -> ok (5.234s; not re-run in remediation — unaffected test-only scope).
- `make parity-checks` -> exit 0, no FAIL (post-remediation rerun).
- `make test-unit` -> FAIL isolated to `internal/archtest` baseline ratchets only (shrinkage/AST/budget plus `TestBillingCoreStaysProviderAndPersistenceFree` via a transitive lipapi edge through tracked packages; this task adds test-only files with no new imports, so the failure pre-exists at HEAD and is unrelated to Task 8.3).
- `go test -count=1 ./internal/core/billing/...` -> PASS.
- Relevant economics packages `go test -count=1 ./pkg/lipsdk/economics/ ./pkg/lipsdk/metering/ ./internal/core/metering/...` -> PASS (all 9 packages ok).
- `go vet ./internal/core/billing/... ./internal/infra/billingstore/...` -> clean; `gofmt -l` on all three new files -> clean; `git diff --check` -> clean; placeholder/secret scan -> clean.
- Dirty paths: only `internal/core/billing/refinement83_selector_certification_test.go`, `internal/core/billing/refinement83_cost_allocation_certification_test.go`, `internal/infra/billingstore/refinement83_integrated_economics_test.go`, plus this evidence file. No production edit, no existing-test edit (shared phase10/tariff/selection/allocation/8.2-opener helpers reused read-only), no stash/spec/tasks/branch action.

## New versus cited evidence (review traceability)

Newly proven by Task 8.3 (this task's three test files, all assertions
executed here): the COGS-vs-retail selector matrix, retry-inclusive frozen
tuple with replay, pass-through settlement/bounds/fail-closed behavior,
conserved allocation with remainder, and the durable integrated
cross-authority flow (postings, settlements, heads, revisions, allocation
readback, explanation, restart) — every amount, ref, and replay claim in
the matrices above.

Cited, not re-proven (existing suites executed as regression context where
noted, never copied as Task 8.3 proof): 8.1 multimodal direction/valuation
durability (`refinement81_*`, valuation `AppendValuation`/`GetValuation`
round-trips), 8.2 resumable-session/posting/restart evidence (runtimebundle
posting pair re-executed green; shared revision scenario and barrier suites
not rerun), Phase 10 selector/rating/pass-through unit and store behavior
(reused helpers and `cost_pass_through_phase10` store coverage), Phase 5/7
COGS/allocation contracts (reused `phase5CostLeg`-family helpers and
`phase11_allocation_*` persistence semantics).

## Deferred to subpass C (explicitly not claimed)

RESOLVED by subpass C — section kept for traceability; the store/integrated
proof above replaces every deferral except final independent review.

## Skips (honest)

- No A-leg meter or session-finality dependency is exercised or asserted; the fixture uses one BillingCallID with no continuity/session APIs.
- No all-payable-loser provider policy beyond the failed/canceled/loser legs above; provider-payable failed-then-abandoned scope beyond this fixture stays with retail-selector scope per Task 8.2 narrowing.
- No race-detector run: Windows toolchain skip is canonical; repeated `-count=5 -shuffle=on` execution is the local stability evidence.

## Files changed

- Kept (subpass A, untouched): `internal/core/billing/refinement83_selector_certification_test.go`.
- Kept (subpass B, untouched): `internal/core/billing/refinement83_cost_allocation_certification_test.go`.
- Added (subpass C): `internal/infra/billingstore/refinement83_integrated_economics_test.go` (durable cross-authority proof; reuses the 8.2 file-backed store opener read-only).
- Updated: `.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement8-3-execution.md` (this file; READY_FOR_REVIEW).
