# Final Phase 18 acceptance

VERDICT: APPROVED. Tasks 18.1, 18.2 and parent18 may be checked complete; Phase19 may start. This verdict supersedes all earlier rejection sections below.

The final diff fixes all four task-owned lint findings: both census readers check deferred Close errors, evidence parsing uses FieldsSeq, and V2 evidence detection uses ContainsFunc without changing its predicate or evidence-version fence. Prior financial and disposition blockers remain closed.

Fresh final verification:

- PASS: owner/scalar/ID-only/binding mismatch and legitimate component/pass-through controls, Phase18/Phase1 guards, BillingHostLoop, stock observation settlement and production F3 runtime pipeline: `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/archtest/... -run 'OwnerFinal|Phase18|Phase1ProducerConsumer|TestBillingHostLoop|TestRefinement4StockObservationToEconomicSettlement|TestF3RuntimeBundleV2PipelineAfterLegalActivation' -count=1 -p=1` (exit 0).
- Reran the exact scoped golangci-lint command from the previous review. It still exits 1 for unrelated existing debt, but now reports no diagnostics for the Phase18 guards or changed rating/worker/store/resolver/pipeline paths; all four reported blockers are gone.
- PASS: affected billing/archtest vet, formatting of the two final touched files and `git diff --check`.
- The immediately preceding full sequential affected-package run passed, including billingstore and runtimebundle; parity and connector targets also passed. The final changes are limited to the inspected lint transformations, so those results remain relevant without claiming a second full run.

No concrete remaining task-owned blocker was identified. Known unrelated quality/architecture debt remains disclosed below; approval does not waive Phase19 verification, Windows race limitations or external PostgreSQL coverage.

Skills used: kiro-review, golang-code-audit, golang-testing, golang-architecture and codegraph. Only this evidence file was edited by the reviewer.

---

# Earlier valuation re-review (superseded)

VERDICT: REJECTED. The financial correctness blockers from earlier reviews are closed. The remaining acceptance blocker is task-owned lint failures; this section supersedes prior verdict details below.

## Important: new Phase 18 code fails the configured quality gate

Fresh scoped `golangci-lint run ./internal/archtest/... ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingcompose/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...` reports these new Phase 18 diagnostics:

- `internal/archtest/phase18_2_migration_disposition_test.go:200` and `:235`: unchecked `file.Close` errors (`errcheck`).
- `internal/archtest/phase18_2_migration_disposition_test.go:287`: use `strings.FieldsSeq` (`modernize`).
- `internal/core/billing/call_rating.go:505`: use `slices.ContainsFunc` (`modernize`).

Minimum remediation: handle or explicitly discard the two read-only close errors using the repository convention, and apply the two indicated idiomatic transformations. Rerun the focused guards, billing tests, formatting and scoped lint. Do not relax lint configuration. The unrelated pre-existing lint, goroutine allowlist, import-closure and architecture-budget failures are not rejection grounds.

## Verified correctness and evidence

- PASS: fresh full sequential affected suites: `go test ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingcompose/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... -count=1 -p=1`, exit 0. Billingstore completed in 133.004s and runtimebundle in 69.713s. This includes owner/decorated resolver, ID-only and binding mismatch zero-effect cases, valid component/pass-through replay, V1 drain, Phase17 recovery/cutover, real host loop and stock economic settlement.
- PASS: fresh Phase18/Phase1 architecture guards; `make parity-checks`, including connector module targets; affected-package vet; gofmt of changed tracked/untracked Go files; `git diff --check`.
- FAIL: `make quality-checks`, including the concrete new lint failures above and known unrelated debt. Generated planes, formatting, build and vet portions pass.
- Actual source now validates the complete V2 customer valuation and its call/account, payer, currency, amount, policy/tariff, input hash and fingerprint binding at resolver, generic worker and direct/transactional store boundaries. An arbitrary valuation ID no longer permits scalar settlement. V1 ownership still selects frozen historical rating despite additive V2 observations.
- Reviewed the current diff, RED artifact and corrected census/guard paths. The 69-row disposition remains classified as 25 native V2, 42 safe projections, one removed and one explicitly unsupported strict offer. No additional reachable financial fallback or material false disposition was identified.
- Windows race and external PostgreSQL topology were not exercised. Phase19 release-wide verification remains future work.
- Skills used: kiro-review, golang-code-audit, golang-testing, golang-architecture and codegraph. Only this review evidence file was edited.

Do not check tasks 18.1, 18.2 or parent18 complete or start Phase19 until the four task-owned lint diagnostics are resolved.

---

# Earlier owner-interaction re-review (superseded)

VERDICT: REJECTED. This section supersedes the prior verdict details below. R1 (V1 shadow/drain regression) is fixed. R2 now rejects missing owner-aware interfaces and empty valuations at both worker and store, but its component validation still accepts a scalar result wearing an arbitrary ID.

## Remaining Important blocker: a valuation ID is treated as proof of component rating

`internal/core/billing/call_rating.go:118`, `ValidateCallRatingResultForOwner`, checks only that `CustomerValuation.ID` is nonempty (unless pass-through is present). It does not validate the valuation or bind its amount to `CustomerCharge`. Consequently the new worker/store checks accept `economics.Valuation{ID: "f3-component-v1"}`: version zero, no subject, no basis, no input identity, no components and no totals. The existing public `economics.Valuation.Validate` rejects this object immediately at `pkg/lipsdk/economics/valuation.go:821` because its version is absent.

This is not a hypothetical malicious resolver. The remediation itself changed the former scalar-positive F3 stubs to return precisely that object in `internal/infra/billingstore/cutover_v2_pipeline_f3_test.go:137-146`. The resulting amount still comes directly from `s.charge`; nothing is component-rated. The newly added `TestOwnerFinalDValidV2ComponentAndPassThroughSucceed/component` (`internal/infra/billingstore/phase18_owner_final_test.go:181`) uses that object, directly calls the real V2 store, and asserts a 70-nano debit and one journal. I ran this test and `TestF3LegalEmptyActivationThenFreshV2Pipeline` freshly; both pass. Thus the same scalar financial producer remains accepted after adding a string label, and the purported valid-component positive control is not a valid valuation.

Minimum remediation: require a structurally valid, complete customer component valuation at the generic V2 boundary and bind its customer/call/currency/rounded-total semantics to the posted result; retain the separately validated explicit pass-through path. Reuse the existing valuation contract instead of inventing an ID marker. Replace label-only stubs with valid bounded component valuations, and add negative cases for an ID-only valuation and a valid valuation whose amount/subject does not match the settlement. They must leave balance, journal and exposure unchanged. Preserve the now-working V1 shadow/drain and real stock V2 component controls.

## Fresh verification for this revision

- PASS: `TestBillingHostLoop`, `TestRefinement4StockObservationToEconomicSettlement`, `TestF3RuntimeBundleV2PipelineAfterLegalActivation`, run together with `-count=1 -parallel=1` (7.109s). This closes prior R1.
- PASS: all `TestOwnerFinal*` plus the F3 legal V2 store pipeline (0.357s); old/decorated scalar and direct empty-valuation cases fail closed as intended, but the label-only positive control proves the remaining blocker.
- PASS: fresh billing owner/R1 domain tests (0.021s), Phase18/Phase1 guards, and `make parity-checks` including its connector checks.
- PASS: affected-package `go vet`, gofmt check of all tracked/untracked changed Go files, and `git diff --check`. Changed-line placeholder scan is clean.
- Read the new RED artifact and inspected actual source/test diffs. No source/tests/task/status/Git changes made by the reviewer.
- Sequential broad affected-package verification was started with `-count=1 -p=1` (session 96580); completed results/final outcome are recorded when available. No unsupported full-suite success claim is made.

Do not check 18.1, 18.2 or parent 18 complete, or start Phase 19, until the component-result fence rejects the concrete ID-only counterexample.

---

# Earlier independent Phase 18 re-review (superseded)

VERDICT: REJECTED. Tasks 18.1, 18.2 and parent 18 remain unchecked; Phase 19 is not authorized. This re-review supersedes the earlier findings below.

The stock resolver's V2 scalar fallback is now removed: the new divergent-quantity production F3 test passes, and owner-aware V2 rating returns component valuation. The corrected inventory now joins exact census row identity and has materially stronger source checks. However, two concrete customer-settlement defects remain.

## R1 — Important: V1-owned calls with additive V2 capture can no longer settle or drain

`internal/core/billing/call_rating.go:221-222` rejects a V1 owner whenever V2 retail observations exist. Even when those observations do not qualify for retail, `rateV1DrainCall` at line 301 calls `WriterVersionForLeg` and rejects any V2-format envelope. This confuses the evidence format with the durable monetary owner. Shadow capture is explicitly additive while the V1 writer remains authoritative (requirements 17.3–17.4, Migration Strategy steps 4 and 6); already-admitted V1 calls may carry V2 observations and must retain their frozen V1 charge/replay semantics.

The normal runtime already attaches V2 observations in `billingLegRecord` before cutover. The production worker now supplies the correct durable V1 owner, and the new checks consequently block the existing host loop. Fresh focused sequential execution of `TestBillingHostLoop`, `TestRefinement4StockObservationToEconomicSettlement` and the new `TestF3RuntimeBundleV2PipelineAfterLegalActivation` yields: first two FAIL waiting for customer settlement, F3 PASS. The stock observation test's failure shows `customerTransactions=[]`; before this remediation it passed in the prior review's focused sequential run. This is a reachable task-owned regression, not the earlier broad parallel timeout attribution.

Minimum repair: use the durable V1 claim to authorize frozen legacy drain/replay even when additive V2/shadow observations are present. Preserve the original evidence and its hashes; do not strip observations or rewrite ownership to make the test pass. Keep V2-owned work component-only. Replace the new `TestPhase18R1V1OwnerCannotConsumeV2Evidence` assertion with coverage proving V1 ownership preserves historical charge despite divergent additive V2 observations, and run both real host-loop controls plus V2 production settlement.

## R2 — Important: the V2 result fence is optional and absent from the actual posting boundary

`internal/core/billing/call_post_usage_worker.go:312-317` uses an optional type assertion for `OwnerAwareCallRatingResolver`, then calls the old resolver with no owner when that interface is absent. The production constructor at line 73 and `ProductionOptions.BillingCallRatingResolver` still accept this old interface. Neither the worker before `ApplyCallBillingResult` nor `internal/infra/billingstore/call_settlement.go` invokes `ValidateCallRatingResultForOwner`; its only production caller is the concrete stock `JoinRatingResolver`.

Thus a supported injected resolver, including a decorator exposing only the original interface, can return a scalar result and the production V2 claim/worker/store path posts it. Fresh `TestF3LegalEmptyActivationThenFreshV2Pipeline` still passes: its `f3RatingStub` returns 120 nanos with no `CustomerValuation`, and `NewCallPostUsageWorkerWithCutover` debits the account under V2 ownership. This is the original counterexample at the actual generic boundary, now bypassing the new stock-resolver-only check. Row 52's claim that the valuation fence is enforced by `resolveCallRatingWithOwner` is therefore still false.

Minimum repair: enforce V2 result validation at the generic worker/posting boundary regardless of resolver implementation, and either require owner-aware resolution for production or fail closed for V2 when the old interface cannot express it. Preserve old pure test doubles through explicitly non-production construction, not a permissive production fallback. Add a V2 scalar-only injected/decorated resolver regression proving zero balance/journal/exposure effects, plus V1 drain and legitimate component/pass-through controls.

## Re-review verification

- PASS: fresh Phase18/Phase1 census guards; fresh core billing/runtime and billingcompose package tests.
- PASS: new Phase18R1 domain/resolver tests and production F3 component test.
- PASS: focused store cutover, recovery, rollback and V1 drain controls (26.542s); the passing scalar-stub V2 pipeline is evidence for R2, not proof of retirement.
- PASS: `make parity-checks`, including its connector module targets; generated planes, gofmt, build, vet and `git diff --check`.
- FAIL: focused sequential host loop and stock observation/customer settlement, as detailed in R1. An additional isolated `go test ./internal/infra/runtimebundle -run '^TestBillingHostLoop$' -count=1 -parallel=1` reproduced the failure in 10.111s.
- The broad fresh package command with `-parallel=1` remained running at handoff (exec session 84420; billingstore and runtimebundle still active). Its completed billing/runtime/billingcompose results passed; no full-package success is claimed for the remaining packages.
- FAIL: `make quality-checks` reports existing allowlist, lint and budget failures. No budget relaxation is proposed. These are not the rejection basis; the current production core line count also grew from the previous review, so the earlier blanket statement of no core-budget increase no longer applies.
- RED artifacts `phase18-r1-scalar-red.txt` and `phase18-disposition-red.txt` were read and correspond to the attempted repairs.
- No new placeholder or concrete secret was identified in changed-line inspection. No source, test, task, status or Git mutation was made by this reviewer.

---

# Prior independent Phase 18 review (superseded)

- VERDICT: REJECTED
- TASK: 18.1, 18.2, parent 18
- Baseline: `8e4767e47768fef70a816b1c4671fdd69122be7b`; reviewed the uncommitted changes on `feat/b-leg-usage-economics`.
- Do not check these tasks complete or start Phase 19 on this evidence.

## Required remediation

### 1. Important: the live scalar customer financial path was not retired

`internal/core/billing/call_rating.go:161` selects the component rater only when V2 quantity evidence exists **and** the tariff is not tagged legacy scalar. At line 192 it otherwise calls `rateCustomerCharge`, which reads `leg.Evidence.InputTokens`/`OutputTokens` and calls `exactTokensAtRate` in `rating.go`. This is a monetary producer, not a historical reader or nonfinancial projection.

The path remains reachable from the production `JoinRatingResolver.ResolveCallRating` (`internal/infra/billingcompose/resolver.go:29`), wired by `buildProcessBillingRuntime`. The production token-carrying customer worker calls that resolver at `internal/core/billing/call_post_usage_worker.go:210`, then posts its result with the claimed owner at line 228. It does not restrict the scalar rater to a durably pinned V1 draining call. The V2 posting fence validates owner/epoch/identity, not whether that result came from the component rater. `PutPricing` still materializes the legacy tag, so the selector is operational rather than dead compatibility code.

Fresh mechanical confirmation: `TestRateCallLegacyScalarTariffIgnoresV2BoundaryQuantityEvidence` passes and explicitly asserts a 310 USD-nano charge from V1 scalar evidence despite V2 boundary observations. The existing `TestF3LegalEmptyActivationThenFreshV2Pipeline` also passes; its production worker posts a scalar `CallRatingResult` without a component valuation under a V2 claim. Together with the production resolver path, these demonstrate that an owner token alone does not retire the scalar financial engine.

The new `TestPhase18NoScalarOnlyLiveRating` checks only the removed supplier helper and catalog lookup. It passes while this customer scalar monetary path remains. Disposition row 52 inaccurately calls `RateCall` a historical reader. This violates task 18.1 and requirement 15.6/Migration Strategy step 8.

Minimum repair: make new V2-owned work consume the component rating/valuation path, including explicitly mapped legacy tariff semantics where supported; prevent missing V2 evidence or the legacy tag from silently selecting the scalar live engine. If a V1 drain/replay rater is still required, isolate and gate it by the durable V1 ownership contract. Add a regression through the production resolver, V2 claim and settlement boundary, plus a guard covering the customer path; retain a separate V1 drain/recovery regression.

### 2. Important: the final producer/consumer certification contains false dispositions and its guard cannot detect them

The new disposition artifact claims native V2 behavior for concrete anchors that still operate exclusively on V1 facts:

- Row 7: `internal/core/metering/aggregate/aggregate.go:29`, `Apply([]metering.Fact)`. Its result uses `map[string]int64` keyed by scalar component name, not complete V2 component keys. It does not consume V2 observations.
- Row 8: `internal/core/metering/reconcile/reconcile.go:43`, `Stream`. It queries V1 facts and invokes the preceding V1 aggregator; it does not reconcile complete V2 source/component identities.
- Row 68: `pkg/lipsdk/controlplane/economics_report_from_facts.go:13`, `DualPlaneReportInputsFromFacts([]metering.Fact)`. It constructs scalar token/money report inputs, not the claimed public V2 detail binding.
- Row 33 identifies `finalizeBillingResponseToEvent` as consuming negotiated V2 observations, but `internal/infra/backendplugins/adapter/finalize_billing.go:15` only projects six optional scalar counters from `response.Usage` into `lipapi.Event`; the V2 owner must be cited separately.
- Row 52 labels the reachable monetary rater described in finding 1 as a historical reader.

All 69 path/anchor associations were mechanically compared with the original census and the current files; that association check passes, but does not prove the claimed semantics. The new test at `internal/archtest/phase18_2_migration_disposition_test.go:56-85` checks row count, unique arbitrary first-column strings, allowed status and whether evidence contains `.go`, `retired` or `history`. It neither joins each disposition to its census row nor validates the referenced replacement/consumer. The false claims above therefore pass the final certification guard today.

Minimum repair: audit and correct the dispositions against actual consumers. Classify retained V1 query projections accurately and name their separate V2 replacement/owner, demonstrating that no live financial consumer depends on the projection. Correct row 52 only after finding 1 is fixed. Make the guard join exact census identity/path/anchor, resolve replacement references, and pair financially significant classifications with behavior tests. Task 18.2 requires this proof, not a 69-row self-description.

## Mechanical results

- PASS: focused Phase 18, census, Phase 8 and independence architecture guards, freshly run with `-count=1`.
- PASS: `go test ./internal/core/billing/...` and fresh targeted scalar-characterization/retirement tests.
- PASS: `go test ./internal/core/runtime/... -count=1`.
- PASS: full `internal/infra/billingstore/...` (201.346s in the broad run); focused fresh cutover/recovery/rollback suite (26.319s).
- PASS: full `internal/infra/billingcompose/...`.
- PASS: `make parity-checks`, including the target's connector module checks, exit 0.
- PASS: quality target's generated planes, formatting, module check, build and vet steps. `git diff --check` is clean. Changed-line placeholder/credential-name scan found no new matches; no concrete hardcoded secret was identified. Scope remains within billing/runtime, owning adapters, guards and task evidence.
- FAIL, not used as these findings: `make quality-checks` reports existing goroutine allowlist and architecture/package/line budget failures. The named goroutine files and critical `process_services.go` are unchanged by Phase 18; the changed core/runtime production diff does not increase the reported core budget. These do not justify relaxing the budgets.
- Full runtimebundle verification failed twice under broad parallel execution (71.871s and 69.570s), with integration deadline/outbox-relay failures including `TestRefinement4StockObservationToEconomicSettlement`. A focused rerun with `-count=1 -parallel=1` of that test, `TestRefinement82RuntimeResumeKeepsTerminalOwnership`, `TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably`, `TestRecovery174*` and `TestVerifyBillingAccountingStartupWiring` passed in 20.443s. Full billingstore also passed on the second run (198.194s). These timeouts are not asserted to be Phase 18 regressions without baseline attribution; the full runtimebundle suite cannot be reported green.
- RED evidence files were read: the provider fallback test and retirement guards failed before changes, and the disposition guard failed when the artifact was absent. The RED log additionally contains two money-splice tests that are absent from the final tree; their initial failure is not evidence that the final implementation fixed that behavior.
- No connector production files changed. Windows race, PostgreSQL topology parity, full release QA and final performance certification were not run for this task-local rejection.

## Summary

Supplier fallback removal is real, but Phase 18 cannot be accepted while a production scalar customer rater remains and the final migration inventory misstates current implementations.
