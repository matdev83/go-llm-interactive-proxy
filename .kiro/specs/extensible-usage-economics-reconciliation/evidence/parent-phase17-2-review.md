# Parent task 17.2 approval re-review (G1/G2 remediation)

## Current review verdict

**APPROVED. Task 17.2 may be checked; task 17.3 may begin.**

No remaining concrete task-local blocker was found. This verdict supersedes the historical rejected reviews below. Approval is for Migration Strategy step 4 only, not production V2 cutover, epoch fencing, rollback, or release certification. The reviewer changed only this report and did not change task status, source, tests, Git state or PRs.

### Final findings disposition

- **G1 closed:** explicit Partial now joins Unknown/Conflict/Unavailable and missing references in the aggregate incompleteness gate. Bare partial E or provider dependencies return partial/false for equal quantities and discrepancies while preserving exact per-item evidence. Complete controls remain matched/discrepant and complete. The test-only F2/F3 completeness correction removes the unjustified production carve-out.
- **G2 closed:** rounded-amount conflicts clear quantity, literal and amount on the transition itself; conflicted-side serialization is suppressed and the join is nil-safe. Fresh runner tests reproduce the original 1/1/2, 1/2/1 and 2/1/1 counterexamples both within one valuation and across three dependency valuations, compare persisted ResultJSON byte-for-byte, and reverse dependency declarations. Equal amounts, absent amounts and quantity conflicts retain controls. These tests were rerun three times.
- **F1 closed:** the explicitly composed observation-only journal sink persists immutable V2 evidence without ordinary usage rows, claims, provider-cost work or monetary intent. Same actual CallID/BLegID coexistence works in both arrival orders, including pre-existing exposure and reopen. Legitimate V1 scalar/provider/settlement activity remains independent and exact-once; it is not credited as a shadow effect. The actual journal implementation retains collision detection and stable replay identity. The no-post lifecycle checks authoritative seeded balances, current unit state and provider/journal state after eligible workers execute.
- **F2/F3/F4 closed:** nil-safe sticky merge, work-bound subject/scope validation, all-foreign value suppression, independent basis selection, payer, canonical charge/allocation coverage and effective measurement-context gates remain present. The minimal pure component reconciler executes through the existing job runner/dependency loader, persists queryable actual outcomes, defers unavailable dependencies and survives replay/reopen. It adds no monetary selection authority.
- **R2/R3 closed:** safe fixtures still run production normalization, the reference rater, shared normalization/persistence and readback against independently frozen expected lines. Mapping/tariff sensitivity affects actual results. The current unit reader queries authoritative state, detects an actual debit and propagates errors; it is not a historical grant snapshot.
- **Earlier clusters closed:** shared valuation normalization preserves allocation/pricing/context/version/provenance and rejects foreign outputs; bounded embedded/ref/combined work, typed-nil checks, immutable timestamps, concurrent/restart idempotency, checked subtraction and bounded comparison labels/reasons remain covered. RateAndPersist correctly promises durable idempotency for a pure deterministic rater, not an unsupported exactly-once invocation guarantee. The existing queued runner provides replay probes where applicable.
- **Boundary clean:** explicit opt-in host composition is usable without changing default startup; no public money options, provider-specific branching, newly owned goroutines or second posting path. No requirement from 17.3/17.4 was imported into this acceptance decision.

### Fresh mechanical verification

- PASS (exit 0): `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/infra/metering/journalstore -count=1` — core billing 0.410s, billingstore 173.683s, runtimebundle 70.050s, journalstore 3.282s.
- PASS (exit 0): `go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/archtest -run 'Phase172|F2|F3|F4|G1|G2' -count=1` — includes historical regressions, production fixtures, seeded no-post lifecycle and scoped architecture gate.
- PASS (exit 0): `go test ./internal/core/billing -run 'TestPhase172G[12]' -count=3`.
- PASS (exit 0): `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`.
- PASS (exit 0): canonical `go run ./internal/testkit/dbparity/cmd sqlite`, all ten registered packages.
- PASS (exit 0): configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`, `go test -tags=integration ./internal/infra/billingstore -run 'TestEconomicJob(Runner|Queue)PostgresDirect|TestQueryEconomicDetailReconciliationPostgresDirect|TestPhase171HistoricalV1MigrationPostgresDirect|TestPhase172' -count=1` — 117.684s, including actual F1 same-identity capture coexistence and persistence/reconciliation paths.
- PASS: `git diff --check`; `gofmt -l` across tracked-dirty plus untracked Go files emitted no paths. Placeholder scan found only the intentional invalid-currency string `XXX`, not an implementation placeholder. No introduced credential was found in inspected code.
- RED evidence VERIFIED: latest G1 logs show bare-partial matched/discrepant failures; G2 logs show differing persisted envelopes and leaked conflict quantities. Corresponding GREEN agrees with fresh reproduction. Earlier cluster/R1-R4/F1-F4 evidence and its limitations are recorded in the historical sections.
- Not run: whole-repository release QA or Windows race. The earlier runtime-resume suite failure did not recur in either of the last two full affected-suite runs. Repository 100-file ratchet remains a separately documented nonblocking pre-existing gate under the explicit 1000-file allowance; it was not bypassed or presented as passing.

### Nonblocking notes

The bound counts embedded observations and explicit references separately, so duplicate representations consume capacity twice; this is conservative rather than an escape. Comparison labels remain caller-supplied stable safe tokens, not a security classification system. Neither point blocks the approved task contract. Full monetary cutover/epoch and rollback certification remain assigned to subsequent tasks.

## Historical reviews (superseded)

# Parent task 17.2 latest re-review (F1-F4 remediation)

## Current verdict

**REJECTED. Task 17.2 may not be checked; dependent task 17.3 may not begin.**

The observation-only F1 boundary fixes the monetary isolation/identity defect. F3 binds every dependency to the work subject and scope. The original typed-nil, normalization/provenance, timestamp/replay, production-fixture and authoritative-unit-reader issues remain addressed. Two concrete reconciliation defects remain. This section supersedes all historical verdict detail below. Only this evidence report was edited; no source/test modifications or probe files were made.

### Important G1: bare partial valuations are promoted to complete agreement

`internal/core/billing/economic_comparison_reconciler.go:292-312` intentionally omits `CompletenessPartial` from `comparisonIncomplete`. The comment justifies this using synthetic F2/F3 fixtures, not the valuation contract. A supported E/Q pair with identical known input lines, identical subject/scope/context, one valuation `CompletenessPartial`, and no `MissingObservations` passes every gate and returns `status=matched, complete=true` at lines 122-131. This is a reachable accepted valuation: `pkg/lipsdk/economics/valuation.go` defines partial independently and validation does not require enumerated missing references. Production `component_rater.go:403-418` also marks partial for a failed sibling fixed rule/required input without requiring missing observation references. A missing classification or rule input need not have an observation reference to enumerate.

Anchors: requirements 2.6, 3.5, 6.3, 7.5 and 18.2; design C4 permits an individual known total to remain comparable while incomplete classification stays partial. This does not justify declaring the whole result complete. Keep known per-item comparisons, but downgrade the aggregate whenever a dependency explicitly reports partial unless there is an explicit, independently validated narrower complete coverage contract. The current implementation has none.

Required regression: reuse the F4 independent match runner control; change only one dependency's completeness to Partial and leave missing refs empty. Expect aggregate partial/false and retain the equal known component quantities. Repeat with a discrepancy and reversed dependencies. Correct synthetic fixture completeness rather than adding production exceptions to preserve their old expected results.

### Important G2: monetary duplicate conflicts still have order-dependent payloads

`economic_comparison_reconciler.go:393-401` sets `prior.conflict=true` on differing rounded amounts but does not clear `prior.value`/`literal`. By contrast the already-conflicting branch at lines 372-376 clears them upon the next known duplicate. `comparisonSide` at line 566 does not suppress conflicts, and `joinComparisonKey` serializes quantities before checking conflict.

Concrete source-traced counterexample: three distinct same-component local lines, all quantity 100, with rounded amounts 1, 1, 2. Processing this order leaves a conflicting entry with local quantity 100; processing 1, 2, 1 leaves a conflicting entry with no local quantity. Both are the same evidence multiset and both return conflict, but the persisted result JSON differs. Unique line IDs make these legitimate duplicate component lines; no nil or malformed decimal is required. The existing F2 permutation tests cover differing quantities, not equal quantities with differing amounts. The last amount-conflict branch therefore retains precisely the noncanonical state the quantity-conflict remediation removed.

Anchors: task 17.2 comparison acceptance, requirement 18.2 deterministic fixtures, design C4 pure frozen-input reconciliation. Canonicalize every conflict transition consistently (or exclude monetary disagreement from a quantity-only plane using an explicit contract). Add three-line permutations through the runner and compare complete persisted envelopes, not merely status. This finding is a direct branch trace, not a claim that an additional reviewer-authored test was executed.

### Closed findings and boundary assessment

- F1: `ShadowEvidenceSink.AppendObservations` uses the existing metering journal, not ordinary V1 usage rows or their processed/claim/provider-work state. Actual composed tests use the correct journal. Stable source references and collision/replay handling remain in the established transactional journal implementation. Same CallID/BLegID both arrival orders preserve V1 scalar rows and work; seeded exposure/provider/settlement controls execute separately. No shadow monetary intent, outbox relay, debit or suppression path was found in this composition. Explicit opt-in host composition is usable; default enabling is not required.
- F2: nil/known merge and differing-quantity conflict are now safe, and missing flags are sticky. G2 narrows the remaining deterministic-merge gap.
- F3: all-foreign matching dependencies are rejected before values are serialized. Work scope and subject ownership are bound; ignoring attempt child lineage within a B-leg does not change accounting ownership.
- F4: independent basis, payer, canonical charge/allocation coverage, effective qualifier/context and severe/missing completeness gates are now real. G1 remains; preserving a synthetic fixture is not a semantic exemption.
- R2 genuine Normalize/ReferenceRater/persistence/readback fixture and sensitivity controls remain intact. R3 reads current unit balances, tests a negative debit, propagates SQL errors and covers reopen.
- Shared valuation normalization preserves fields and rejects foreign identity. Typed-nil constructor checks, bounds, deterministic timestamps, replay/probes, comparator overflow and bounded safe reason classifications retain their earlier fixes. No public money option, provider switch, new goroutine or global registry was introduced.
- No epoch fencing, default cutover or rollback requirement from 17.3/17.4 is imposed by this review.

### Fresh mechanical evidence

- PASS, exit 0: `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... -count=1` (billingstore 139.145s, runtimebundle 54.340s). The previous runtime-resume suite failure did not recur in this run.
- PASS, exit 0: focused `go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/archtest -run 'Phase172|F2|F3|F4' -count=1`.
- PASS, exit 0: `go test ./internal/infra/metering/journalstore -count=1`.
- PASS, exit 0: `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`.
- PASS, exit 0: canonical `go run ./internal/testkit/dbparity/cmd sqlite`, all ten registered packages.
- PASS, exit 0: configured PostgreSQL, `LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'TestEconomicJob(Runner|Queue)PostgresDirect|TestQueryEconomicDetailReconciliationPostgresDirect|TestPhase171HistoricalV1MigrationPostgresDirect|TestPhase172' -count=1` (93.154s), including the new actual F1 both-arrival-order PostgreSQL test.
- PASS: `git diff --check`; `gofmt -l` on tracked-dirty and untracked Go files emitted nothing. Prior task-scoped architecture/placeholder/secret inspection remains applicable; new source contains no discovered placeholder or credential.
- RED/GREEN evidence: inspected latest F1-F4 logs alongside the earlier cluster/R1-R4 evidence reviewed below. F1 has an actual runtime RED (V1 closure no longer claimable) in addition to its compile RED; F2 has nil panic and order-difference RED; F3 foreign/scope RED; F4 missing/basis/payer/coverage/context RED. Corresponding focused GREEN agrees with fresh execution. These tests do not exercise G1 or G2.
- Not run: repository-wide release QA, Windows race (not practical here), or a new authored failing probe (reviewer ownership excludes test writes). The pre-existing 100-file ratchet remains nonblocking under the explicit 1000-file allowance, not silently overridden.

## Historical review (superseded)

# Parent task 17.2 final requested re-review (R1-R4 remediation)

## Latest verdict

**REJECTED. Task 17.2 may not be checked; dependent task 17.3 may not begin.**

The R2 production semantic fixtures and R3 authoritative unit read now address their prior blockers. The new capture-only API removes the previously demonstrated direct queue-to-payable path for an isolated shadow identity. However, its use of ordinary V1 identity rows can suppress legitimate V1 work, and the newly added reconciliation implementation has reachable correctness failures. The following findings supersede earlier open-finding lists retained below. Only this report was edited.

### F1. Critical: shadow-first capture consumes V1 identity and can permanently suppress its settlement

`internal/infra/billingstore/shadow_evidence_store.go:148` inserts a normal `usage_call_records` row with `claim_status = processed`, under the ordinary call key. Later `AppendCallUsage` sees the identical fingerprint and returns successful replay without making the row pending. Consequently this sequence succeeds but never lets the legitimate V1 worker settle:

1. Admit a legitimate V1 call and retain its exposure.
2. Deliver that call's closure to `CaptureShadowCall` before its ordinary terminal append.
3. Deliver the same closure to ordinary `AppendCallUsage`.
4. Run `ClaimCompleteCalls`: the call remains processed despite no settlement.

The leg path has the same ordering problem: `CaptureShadowLeg` inserts the ordinary leg key without provider work; a subsequent identical ordinary `AppendCallLegUsage` returns at its replay branch before enqueueing provider work. If the ordinary payload is V1 scalar and the shadow payload contains V2 observations, the two payloads instead conflict under the same key. Reversing arrival order does not give a usable general coexistence contract either: existing V1 scalar evidence cannot acquire V2 observations by immutable same-key replay.

The new attachment test uses a **different** B-leg (`b-c4-attached`) under an already-settled call. That avoids the collision by inventing another execution root and does not certify shadow observation of the same B-leg/closure. The existing-exposure case requested for review is not exercised for the shadow identity; the composed test still explicitly requires its exposure to be missing.

This violates Migration step 4's V1 ownership/coexistence and stable observation identity, and requirements 6.1/6.2/10.1/17.3. It is not a request for task 17.3's epoch fence. Keep shadow evidence in an observation/projection namespace that cannot preempt ordinary V1 terminal rows or claim state, or explicitly attach source-separated observations to the existing execution without pretending it has already settled. Test both append orders, same actual B-leg/call identity, V1 projection versus V2 evidence, existing exposure, and reopen. Successful shadow replay must not suppress or add legitimate V1 posting work.

### F2. Critical: missing-then-known duplicate component panics in the reconciler

`internal/core/billing/economic_comparison_reconciler.go:165` creates a plane entry with `value: nil` for a line with no Quantity, but leaves `missing` false. On a later dependency/line for the same role and component with a present quantity, `prior.missing || quantity == nil` is false, so line 175 calls `prior.value.Cmp(quantity)` on nil.

A missing quantity is valid in the valuation contract (LineItem validation only normalizes Quantity when non-nil); retaining it is an explicit feature of this reconciler. Two declared local dependencies can therefore contain respectively an unavailable component and an observed component, with valid distinct line IDs/revision identities. The actual job runner directly invokes ReconcileJob without a recovery boundary in `processReconciliation`, so this can panic its owner rather than preserve partial/conflict evidence. Reversing dependency order produces partial instead, making behavior order-dependent.

Initialize missing state consistently or use a total nil-safe merge; test missing-then-known, known-then-missing, two missing and conflict combinations through the runner. Requirements 1.5/3.5/10.1/18.2 and design C4 require explicit partial/conflict outcomes, never a panic.

### F3. Important: reconciler can attribute another subject's comparison to the work subject

`comparisonSubject` compares dependency valuation subjects only with one another, never with `work.Subject`. `comparisonEnvelope` then stamps `work.Subject` on the result. A reconciliation work for B-leg A with two correctly addressed, mutually matching E/Q dependency outputs for B-leg B therefore yields a matched result **attributed to A**. The surrounding loader checks exact valuation IDs and hashes, but `validateEconomicJobDependencyOutput` does not bind them to the reconciliation work subject. Envelope normalization only sees the already-rewritten subject and cannot reject it.

The new subject-mismatch test uses one A output and one B output; it misses the all-foreign-but-mutually-equal case. Compare every dependency's subject/scope to the work's declared comparison scope (or an explicitly supported allocation relation) before generating an envelope. Fail closed or return an explicit incomparable result, without representing B's evidence as A's. Requirements 6.2/12.1/16.3 and design C4/D1 anchor this finding.

### F4. Important: comparison discards completeness and independent-source semantics

The new reconciler never reads dependency `Valuation.Completeness`, `MissingObservations`, coverage or effective measurement context. Equal quantities for the retained lines produce `status: matched, complete: true` even if one valuation is explicitly partial with missing evidence. Concrete example: local valuation contains input=100 and a missing output observation; provider valuation contains input=100. The line union has one equal key, so `summarizeComparisonItems` upgrades the incomplete comparison to complete agreement.

It also assigns `BasisCustomerPolicy` to the local independent-measurement plane at line 196. Retail quantities may derive from policy-selected provider evidence, so matching such a customer valuation to provider quantities is not independent local-versus-provider corroboration. The full component key alone is not the approved comparability key: subject/payer, coverage and effective measurement context remain material under C4.

Preserve incomplete/missing coverage and restrict supported basis pairs to their actual semantics. Reuse established comparison contracts where possible instead of declaring a second reduced interpretation of reconciliation. Add a partial-valuation/equal-known-subtotal case and a provider-derived retail case; neither may become a complete independent match. Requirements 1.1/1.2/1.5/6.3/7.2/12.1 and design C4 require this behavior.

## Latest disposition and verification

- Original valuation preservation, typed nil, bounded work, deterministic CreatedAt/replay and comparator overflow/token validation fixes remain intact. Shared normalization is reused; no public money Options or new asynchronous owner was introduced.
- Prior R1: isolated no-post queue suppression is fixed, but same-execution coexistence remains blocked by F1. No epoch or rollback work is requested.
- Prior R2: fixed for this task. Fixtures now call production `normalize.Normalize`, frozen mapping/tariff inputs, `NewReferenceRater`, shared normalization, persistence and readback; independent expected line arithmetic and mapping/tariff sensitivity are present. The test no longer certifies a test-only multiplier.
- Prior R3: fixed. `CustomerUnitBalance` reads current scoped unit state; reserve/commit negative control changes 5 to 3, storage/cancellation errors propagate, and the composed snapshot reads this API. Missing entitlement is explicitly returned as `CustomerEntitlementMissing`, not asserted as observed zero (the method comment should accurately describe that existing behavior).
- Prior R4: runner/loader/computation/persistence/readback are now real. Missing and wrong-hash dependencies defer; multi-input, match/discrepancy, missing-side precedence, idempotency and reopen tests pass. F2-F4 prevent accepting the new implementation's broader claim of correct reconciliation.
- Cluster 1-4 RED/GREEN logs were reviewed in the preceding pass and retained as evidence. All new `phase17-2-r1` through `r4` RED/GREEN logs were inspected in this pass. R1 RED includes the actual 11-nano provider-payable journal and claimable closure; the revised isolated tests remove it. R3's negative control is meaningful. New green logs alone do not cover F1-F4.
- Fresh `go test ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/archtest -run Phase172 -count=1`: PASS (33.805s / 4.764s / 0.035s).
- Fresh `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`: PASS.
- Fresh canonical SQLite runner `go run ./internal/testkit/dbparity/cmd sqlite`: PASS across all ten registered component invocations.
- Fresh `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... -count=1`: core billing PASS (0.364s), billingstore PASS (140.491s), runtimebundle FAIL (49.190s), reporting `TestRefinement82RuntimeResumeKeepsTerminalOwnership: shutdown created journal transactions`. Its isolated rerun with `-run '^TestRefinement82RuntimeResumeKeepsTerminalOwnership$' -count=1` PASS (5.640s). This is an unresolved suite-sensitive observation, not claimed as a clean full-suite pass or conclusively attributed to this patch.
- Configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`: `go test -tags=integration ./internal/infra/billingstore -run 'TestEconomicJob(Runner|Queue)PostgresDirect|TestQueryEconomicDetailReconciliationPostgresDirect|TestPhase171HistoricalV1MigrationPostgresDirect|TestPhase172' -count=1`: PASS (83.316s). PostgreSQL-specific selected tests cover existing persistence behavior; Phase172 fixtures in that command remain SQLite-backed.
- `gofmt -l` on all changed/untracked Go files and `git diff --check`: CLEAN. Scoped archtest passed; prior unrelated full-arch baseline/LOC failures were not represented as green or rerun unnecessarily. Race, full QA and Windows test-cost were not run.
- The new capture-only and comparison-specific tests use SQLite. Including Phase172 in an integration-tagged run does not convert those fixtures into PostgreSQL tests; there is no claimed PostgreSQL-specific new shadow scenario. New SQL is dialect-neutral in syntax, but same-identity/race fixes should have explicit dual-dialect behavioral proof.
- F1-F4 are direct source-level reachable counterexamples, not newly executed reviewer tests; reviewer ownership prohibits source/test edits. No scope/status/Git mutation was performed.

---

# Previous remediation re-review (historical)

## Current verdict

**REJECTED. Task 17.2 must remain unchecked; dependent task 17.3 must not begin.**

This re-review inspected all four remediation clusters, their RED/GREEN logs, the shared normalizer/nil helper, the new composed lifecycle and its actual financial effects. Only this report was changed. The original review is retained below as historical evidence; the dispositions in this section supersede its open-finding list.

## Current blocking findings

### R1. Critical: the supposedly no-post shadow lifecycle creates a provider payable

This is now demonstrated by the implementation's own passing composed test, not merely an untested concern. `internal/infra/runtimebundle/shadow_v2_isolation_test.go:347` provisions unrelated V1 activity, records a baseline, allocates a **new** `shadowCallID`, and inserts its leg/call exclusively through `ShadowV2Handle`. At line 477 it requires one new ordinary provider-cost work item; after `CallProviderCostWorker.ProcessOnce`, line 487 explicitly expects `baseline.CogsCount + 1` and an additional `provider_call_cogs` journal attributed to the new shadow call/B-leg.

The reachable chain is `ShadowV2Capture.CaptureLeg` (`shadow_v2_capture.go:149`) -> ordinary `DurableStore.AppendLeg` -> `AppendCallLegUsage` -> `provider_cost_work`, with `CaptureCall` providing the account-bearing closure -> the existing V1 worker -> `ApplyProviderCost` -> payable/COGS journal. There was no independently admitted/executed V1 call for that new shadow identity before shadow capture. With shadow disabled, that work item and journal do not exist. Calling the final implementation a V1 writer does not make a new shadow-originated payable a permitted V1 baseline effect.

Requirement 17.3 has two simultaneous conditions: V1 remains the sole posting authority **and the new shadow system does not debit accounts or post provider payables**. Migration step 4 and task 17.2's completion criterion likewise require posting disabled. The test proves a violation of the second condition and then blesses it by changing the expected count. The absence of a customer exposure in this particular fixture also does not make ordinary claimable shadow closures a safe monetary boundary.

Smallest corrective boundary: bind shadow capture to a capture-only adapter that persists immutable V2 evidence/references and comparison records without creating ordinary provider-cost or customer-settlement work. Avoid changing ordinary V1 terminal behavior. If shadow observes existing V1 executions, preserve their trusted identity and attach shadow evidence to that execution without adding posting intent; do not fabricate a separate claimable billing call. Ensure shadow economic work is likewise consumed only by the no-post path. This is capture isolation, **not** task 17.3 epoch/cutover fencing.

Required regression: establish legitimate V1 money/unit/payable baseline; run shadow-only capture, call closure, rating and real reconciliation; drive all eligible existing workers; assert no new posting work for shadow identities and exact equality of authoritative account/unit/payable/journal state. Independently execute one legitimate V1 settlement and show that only its expected operation changes state. Repeat after reopen and with an existing V1 call's exposure present, so the proof does not depend on missing exposure.

### R2. Important: semantic certification still substitutes a test-only arithmetic engine

`shadow_v2_cluster3_test.go:209` defines `cluster3QuantityRater`, which sums all integer measures and multiplies by a rate. The reasoning fixture supplies one already-corrected **input** component (`DirectionInput`, `vendor:tokens`) with quantity 200; it contains neither output 200 nor included reasoning 50. No production inclusion normalizer or component rater computes the named reasoning fix. `V1Nano` remains a literal. `ExpectedBilledTokens` is another literal multiplied by the same rate. Cache partition, aggregate coverage and E/Q/P vectors from the previous fixture were removed.

The updated comparator correctly distinguishes a supplied expected amount from a different actual amount. The persistence path is real. Neither change certifies the named accounting semantics: reverting the production reasoning/cache inclusion logic would leave these tests green because they never call it. This remains a concrete task 17.2/requirement 18.2 coverage failure, rather than a request for broad new feature work.

Run the safe evidence through the relevant existing production normalizer/rater with frozen tariffs, retain an independently calculated expected result, persist and compare that actual result. For the reasoning vector, provide real output and included reasoning fields; prove changing the production inclusion behavior changes the verdict/test. Retain representative intended-fix and unexpected-drift cases for the task's approved semantics.

### R3. Important: the financial snapshot still cannot detect customer-unit debits

At `shadow_v2_isolation_test.go:324`, `c4CaptureFinancial` obtains `UnitAfter` by replaying the original grant and copying `replayed.After`. `customer_unit_store.go:258` decodes the original stored operation `ResultJSON`; it intentionally returns the historical grant result, not current unit-account state. A later debit can reduce the actual balance while every before/after snapshot still reports the original five-unit grant. Thus `require.Equal(baseline.UnitAfter, afterWorker.UnitAfter)` is insensitive to the very debit it purports to rule out.

Use a read of the authoritative unit balance/operation state, with errors propagated. Add a negative-control test that performs a debit after the baseline and verifies the financial snapshot changes. The old `shadowV2AccountBalance` helper also remains in `shadow_v2_capture_test.go` and still turns every query error into zero; the new composed account read is sound, but remove reliance on the old helper for acceptance evidence. This is required by the explicit no-unit-debit completion criterion.

### R4. Important: reconciliation evidence is dependency metadata, not a computed comparison

`cluster4RealReconciliation` and `c4RealReconciliation` now load and validate a persisted dependency, which is a genuine improvement. They then manually marshal its ID/hash and summed totals into `ResultJSON`. They do not run the existing reconciliation computation, compare independent evidence, or produce a comparison status/delta. With multiple dependencies the loop overwrites `linked`, so only the last valuation's totals reach the JSON. The normal lifecycle fixture has only one dependency and therefore cannot expose that issue.

The malformed ID/version/subject/basis/hash tests meaningfully cover the durable envelope validator, and GetReconciliation proves retrieval. The missing-dependency assertion directly calls the store loader; it does not demonstrate the composed shadow reconciliation lifecycle refuses or defers missing inputs. Migration step 4 says to run valuation/reconciliation shadow mode. Demonstrate a real pure reconciler over the loaded frozen inputs, persist/query its actual result, and test a missing/foreign dependency through that composed path. This can reuse the existing economic job/reconciliation seams; no new framework is required.

## Disposition of original blockers

| Original finding | Current assessment |
| --- | --- |
| 1: stripped valuation/provenance and overwritten source identity | Fixed. Shadow calls the shared full-clone normalizer; allocation/provenance retention and foreign-subject rejection are covered. Added perspective/scope checks preserve the same trust boundary. |
| 2: bound bypass | Fixed as an upper-bound check: embedded and ref slots are checked before normalization at every work entry point; leg references are checked too. See the nonblocking representation note below. |
| 3: typed nil | Fixed. Shared `IsNilPort` checks nil-capable reflect kinds without invoking methods, and constructors check every port including V1Settlement; pointer/function tests pass. It is a stateless local validation helper, not a registry. |
| 4: fabricated/non-stable time | Fixed. Shared normalization uses immutable work time. No-probe repeats, overlapping calls and real file reopen pass; the inaccurate no-rerate promise has been removed. Duplicate pure calculation is not itself a duplicate monetary effect. |
| 5: semantic fixture proof | Still open as R2. Comparator arithmetic now has an independent expected input, but the fixture bypasses the actual semantics. |
| 6: overflow/unsafe-text acceptance | Original concrete counterexamples fixed. Nonnegative amounts and checked subtraction prevent wrap; strict token validation and non-echoing errors reject the reported free-form cases. Labels remain trusted synthetic identifiers, not a general content/credential sanitizer. |
| 7: vacuous no-post proof | Still open and strengthened to a demonstrated financial violation (R1) plus a blind unit snapshot (R3). Account/journal reads and nonzero provisioning are improved. |

Nonblocking representation note: counting observations plus refs is conservative but representation-dependent. Normalize adds refs for embedded observations, so raw one-observation work can pass MaxObservations=1 while its normalized equivalent fails with a count of two. Do not call this a count of distinct observations; consider canonical distinct input counting if callers need normalization-transparent limits. This does not reopen the original underbounded-work blocker.

The handle remains a manually callable explicit seam; default startup enablement is not required. No new public money Options, provider switches, asynchronous owners, epoch state or rollback behavior were demanded. The normalizer changes are within the necessary shared domain boundary. Exact rater output ID/version are still assigned by the pre-existing worker normalization contract; semantic subject/input identities are validated before assignment.

## Fresh re-review verification

- `go test ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/archtest -run Phase172 -count=1`: PASS (25.397s / 6.010s / 0.038s). This includes the current test that explicitly confirms the forbidden extra shadow payable, so green is not acceptance.
- `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`: PASS.
- `go run ./internal/testkit/dbparity/cmd sqlite`: PASS, all ten registered component invocations.
- Configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`: `go test -tags=integration ./internal/infra/billingstore -run 'TestEconomicJob(Runner|Queue)PostgresDirect|TestQueryEconomicDetailReconciliationPostgresDirect|TestPhase171HistoricalV1MigrationPostgresDirect' -count=1`: PASS (64.780s). Existing persistence contracts, not a PostgreSQL shadow-isolation proof.
- `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... -count=1`: PASS (0.357s / 157.578s / 53.358s).
- `gofmt -l` for all changed/untracked Go files and `git diff --check`: CLEAN. Untracked whitespace is covered by gofmt, not git diff.
- All cluster 1-4 RED/GREEN files under `%TEMP%/opencode` inspected. Clusters 1-3 contain meaningful failures for the reported defects. Cluster 4 shows a financial count failure and then green with the new payable expected; that is not successful red-to-green remediation of no-post behavior. Its envelope/probe error regressions are meaningful separately.
- Full archtests were not repeated because the original run already established unrelated baseline/LOC failures; the task-specific archtest was rerun and passed. Full QA, race and Windows test-cost were not run. No production or test file was edited by the reviewer.

---

# Original review (historical; dispositions above supersede its open findings)

## Review Verdict

- VERDICT: REJECTED
- TASK: 17.2, Run V2 capture and rating in no-post shadow mode.
- BASELINE: `e6355d0c`, branch `feat/b-leg-usage-economics`; seven untracked implementation/fixture files inspected. Review writes only this evidence file.
- SCOPE: Migration Strategy step 4. Task 17.3 epoch/cutover fencing, task 17.4 rollback, and task 18.2 producer retirement are not prerequisites imposed by this review.
- SUMMARY: The explicit synchronous seam performs real durable operations, but its valuation adapter destroys required input/provenance information, its configured bounds are bypassed, and its semantic/no-post certification does not establish the requested behavior.

## Blocking findings

### 1. Critical: shadow valuation normalization changes the meaning of real rater results

`internal/infra/billingstore/shadow_v2_capture.go:246` reconstructs a valuation using only lines, totals, completeness and time. It drops `AllocationCoverageRefs`, `Rater`, `RaterContent`, `Tariff`, `TariffContent`, `Policy`, `PolicyContent`, qualifier snapshots/effective qualifiers, payer, missing observations, and charge coverage. It also replaces the draft's subject, basis, perspective and observation references without checking whether they matched the work.

Concrete source counterexamples:

- Supply valid allocation-aware work and a valid rater output covering exactly its allocation references. Shadow removes those references. `AppendEconomicRevisionResult` rejects the result because its retained allocation set no longer equals the immutable work claim. Thus the accepted allocation path cannot run through shadow.
- Supply a draft for a different subject/basis/input set with otherwise valid lines and totals. Shadow assigns the intended work's identity to those amounts before the durable validator can detect the discrepancy. The established `internal/core/billing/economic_revision_worker.go:normalizeRevisionValuation` instead checks the draft subject, basis and observation hash, preserves the full clone, validates allocation coverage, and rejects mismatches.
- Valid tariff/context provenance is absent from the persisted shadow result. Consequently shadow cannot certify the same component-rating semantics as the existing worker.

This violates task 17.2's real V2 valuation/comparison requirement and design D4/C3, plus requirements 2.6, 7.1/7.4 and 18.2. Remediation: use one validated normalization contract preserving the complete valuation, with negative tests for foreign results and positive tests for allocation/provenance retention. Do not make storage more permissive to accept the stripped result.

### 2. Important: configured observation bounds are ineffective for economic work

At `shadow_v2_capture.go:197`, rejection requires too many observations **and zero normalized references**. `EconomicRevisionWork.Normalize` derives and stores `Input.ObservationRefs` from the observations. A valid work envelope with two observations and `MaxObservations: 1` therefore bypasses this condition. Ref-only envelopes are not counted. `RateAndPersist` and `AppendReconciliation` do not check the configured bound at all, so a caller can bypass `AppendWork` as well.

Task 17.2 exposes a bounded capture/rating seam; design Security and Performance and requirement 18.5 require those limits to be real. Validate the effective input count before costly normalization/rating, consistently across callable entry points; test embedded, ref-only and combined input cases.

### 3. Important: typed-nil ports pass constructors despite the fail-closed contract

`NewShadowV2Capture` and `ComposeShadowV2Capture` use interface `== nil` checks. A `(*DurableStore)(nil)` assigned to `V1Settlement` passes the supposed coexistence proof. A typed-nil pointer implementing `PostUsageRater`, whose `Rate` dereferences the receiver, passes construction and panics at `shadow_v2_capture.go:233`. A typed-nil function adapter has the same problem if its method invokes the function.

These are ordinary reachable Go interface values, not malformed reflection state. The constructor explicitly documents rejection of typed nil; C7 also requires incomplete/typed-nil bindings to fail closed. Add checks for all nil-capable accepted port kinds and constructor tests, including V1Settlement. The current test covers only an untyped nil terminal/rater/settlement.

### 4. Important: fabricated/non-stable timestamps undermine replay correctness

`shadow_v2_capture.go:252` accepts the rater's wall-clock `CreatedAt`, or manufactures `time.Unix(1_700_172_000, 0)` if it is missing. The existing worker anchors processing metadata to immutable `work.CreatedAt` for precisely this reason.

The result probe is optional. With a conforming result-store wrapper exposing only `AppendEconomicRevisionResult`, two calls re-rate and produce different timestamps under the same stable valuation ID, yielding a durable identity conflict. Even with the concrete probe, concurrent calls can both observe no result and reach the rater before either persists. The current logical-restart test neither closes/reopens the store nor counts rater invocations.

Migration step 4 requires stable stored identities, and requirements 10.1/18.2 require replay proof. Anchor time to immutable work; preserve the established worker's validated identity semantics. Either require/enforce the promised no-repeat execution capability or narrow that promise and prove duplicate calculation cannot change the durable result. Test actual reopen and overlapping calls.

### 5. Important: semantic fixtures certify labels, not V1/V2 accounting behavior

`TestPhase172ShadowV2CompareClassifiesFixVsDrift` reads precomputed V1/V2 amounts and an expected verdict from the same JSON fixture. No V1 or V2 rater computes those amounts. Both capture test raters return an empty partial valuation. `CompareShadowV2Semantics` classifies **every** nonzero difference carrying any allowlisted reason as an intended fix, irrespective of magnitude or direction.

Counterexample: change the reasoning vector's V2 amount from 200000000 to 900000000 while leaving `reasoning-in-output-once`; it remains `expected_fix`. A severe regression in the named fix is thereby accepted. This fails task 17.2's explicit intended-fix versus accidental-drift comparison and requirement 18.2. Execute the safe fixture inputs through actual relevant rating/normalization paths, assert the expected monetary/component result, and classify deviations from that expectation as drift.

### 6. Important: comparator arithmetic and claimed safe text handling are not fail-closed

At `shadow_v2_compare.go:110`, `V2Nano - V1Nano` can overflow. For accepted input `V1Nano = -9223372036854775808`, `V2Nano = 9223372036854775807`, the signed result wraps to -1 rather than rejecting an unrepresentable delta. The API accepts signed aggregates without a range restriction.

The name validator accepts arbitrary bounded text apart from CR/LF/NUL and echoes it into Detail. `Name: "Bearer abc123"` is accepted and echoed, despite the seam documentation claiming that credentials/free-form content fail closed. Whitespace other than the four listed characters can also enter a reason-code error. Safe fixtures do not prove unsafe inputs are rejected.

Use checked/exact subtraction and the project's established safe-label validation (or accurately constrain the API to trusted prevalidated labels). Add overflow and unsafe-label cases. Relevant contracts: task 17.2 safe comparisons, requirements 2.3/16.1, design Security and Performance.

### 7. Important: required financial-isolation evidence is vacuous in material places

Both monetary doubles are constructed and discarded rather than injected or exercised. The account `shadow-acct-172` is never provisioned with a nonzero balance. The balance helpers return zero for **any SQL error**, so an invalid query, closed database or missing table can masquerade as an unchanged balance. The runtimebundle test measures no provider payable/cost state; the billingstore test checks provider revision head counts but not existing payable amounts or legacy provider cost records.

This matters because `CaptureLeg -> DurableStore.AppendLeg -> AppendCallLegUsage` inserts ordinary `provider_cost_work`; `CaptureCall` inserts an ordinary claimable closure. The legacy worker may process those queues, which can be valid V1 ownership, but the test never runs that coexistence lifecycle. Merely checking that the input contains a settlement interface does not establish a running V1 authority or absence of V2-derived posting.

This finding does not demand task 17.3's epoch fence or claim that a forbidden debit was mechanically observed. It identifies missing evidence for task 17.2's explicit completion criterion. Seed nonzero money/unit/payable state, execute the actual composed shadow lifecycle with V1 settlement active and the relevant post-turn processing, query all actual affected financial state, and make probe errors fail tests. Include call closure and reconciliation, not only leg/work/rating.

## Additional observations and scope decisions

- The exported handle is a usable manually invoked synchronous orchestration seam; absence of default enablement alone is not a blocker. No public money Options, provider switches or new goroutines were introduced.
- `CaptureLeg` verifies the V2 format/projection but never checks observation StoreID against its configured StoreID. Under the supplied DurableStore, leg sealing validates lineage but storage places the envelope in ordinary usage tables without applying the shadow StoreID. Add a foreign-store capture regression and bind/validate concrete scope consistently. `CaptureCall` is a common closure DTO without a V2 evidence-version field; requiring a nonexistent V2 call discriminator would be an invented requirement.
- The durable reconciliation adapter checks work kind, ID, revision, subject, basis and input hash. It is storage, not a reconciler: this seam accepts a caller-authored result and does not resolve dependency outputs or compute a comparison. The current test's `{"status":"matched"}` envelope demonstrates storage only. Remediation of findings 1/5/7 should demonstrate real dependency-derived reconciliation and query contents.
- All concrete port implementations inspected keep direct financial mutation outside result/reconciliation appends. No direct debit call appears in the three new production files. The static denylist passes but is not a transitive no-post proof.
- Source counterexamples above were established by direct code inspection; no additional source/test probe files were created because reviewer ownership is limited to this report.

## Mechanical evidence

- `go test ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/archtest -run Phase172 -count=1`: PASS; 14.645s, 0.140s, 0.041s.
- `go test ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/archtest/... -count=1`: billingstore PASS (152.599s), runtimebundle PASS (59.643s); archtest FAIL (25.624s).
- Full archtest failures include existing core A-leg subject/import assertions, connector overlay/shrinkage/LOC budgets, CallUsageRecord field baseline, request-attempt state baselines, attempt-sequence ratchet and hexagonal import baseline. The seven new files do not change the referenced existing definitions. The new runtimebundle file contributes to an already exceeded package budget; no claim that the whole gate is green. No baseline checkout was mutated to rerun these failures.
- `go run ./internal/testkit/dbparity/cmd sqlite` (canonical Make target command): PASS across all ten registered component invocations.
- With configured PostgreSQL and `LIP_REQUIRE_POSTGRES=1`: `go test -tags=integration ./internal/infra/billingstore -run 'TestEconomicJob(Runner|Queue)PostgresDirect|TestQueryEconomicDetailReconciliationPostgresDirect|TestPhase171HistoricalV1MigrationPostgresDirect' -count=1`: PASS (63.578s). This certifies existing relevant persistence paths; there is no new PostgreSQL-specific shadow lifecycle test.
- An initial PostgreSQL regex invocation without `-tags=integration` selected no tests; it is not counted as verification. An exploratory nonexistent `tools/dbparity` path failed, then the actual Make target runner was used.
- `go vet ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`: PASS.
- `gofmt -l` on all six Go files: CLEAN. `git diff --check`: PASS (implementation is untracked, so this is not a whitespace proof for those files; gofmt covers Go formatting).
- Production-file placeholder/credential spot check: no TODO/TBD/FIXME/HACK/XXX or literal credential matches. Safe fixture carries no actual credentials. This is not proof of runtime text sanitization.
- `%TEMP%/opencode/phase17-2-red.txt`: inspected. Runtimebundle includes expected undefined proposed API failures, but billingstore output is dominated by missing test helpers and truncates before a meaningful production acceptance failure. RED evidence is partial, not a convincing behavioral red run. Green log inspected and independently reproduced by the focused run above.
- Race testing, repository-wide quality/unit/QA, Windows test-cost and full PostgreSQL catalog were not run; this is a bounded task review, not release certification.
- CodeGraph reported six pending added files and missed the new filenames; direct current reads were used for those files and stale existing sources. FFF is bound to another repository root, so concrete worktree reads/searches were used when its results could not answer this worktree's question.

## Required next action

Keep task 17.2 unchecked and do not start dependent task 17.3. Remediate the concrete valuation, bound, nil, replay and comparison defects; replace the vacuous isolation evidence with an actual composed lifecycle; then rerun task-local review. No task/spec status or Git operation was performed by this reviewer.
