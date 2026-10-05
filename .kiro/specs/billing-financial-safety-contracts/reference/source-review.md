# Billing adversarial review and implementation handoff

**Repository:** matdev83/go-llm-interactive-proxy  
**Reviewed revision:** b560dbff3a06dc44a324aa15a0245ccaed5f76cb  
**Review date:** 2 October 2026  
**Verdict:** NO-GO against the eleven requested financial contracts.

## Scope and evidence limits

This is a source-based review of the merged billing-critical paths associated with issue #620, issue #694, PR #659, PR #666, and PR #692. PR #692 contains the specification; its implementation PR #698 is also included. PR #698 merged as b583c7261680ad0244ac37237ec4583430a139e0. The assessment is pinned to the revision above, not to a moving branch or task-completion claims.

The review inspected admission, scalar/rich quote selection, route and thinker execution, terminal evidence, the terminal spool, asynchronous settlement, financial journals, accounting cutover, and frontend error classification. It does not certify every line of the very large PRs, every external billing binder, or an unspecified deployed configuration. No repository build, integration test, database stress test, or live-provider reproduction was executed. The failure traces below are deductions from the cited production functions, not claims of executed tests. Existing tests and reported CI results are not treated as proof.

Source references below are repository-relative paths at the pinned commit. Function names locate the relevant behavior. Posting ownership V1/V2 and scalar/component-based pricing are separate axes; do not assume that activating V2 automatically activates a rich, conservative quote.

## Contract assessment

| # | Requested contract | Assessment |
|---|---|---|
| 1 | Charge all attributable B-legs, including failed-but-billed work | Fails. Default selection excludes work; even all-attributable selection excludes some dispatched timeouts. Scalar missing evidence can disappear from customer charges. |
| 2 | User cannot spend beyond affordability | Not guaranteed. Reservation arithmetic is sound only if quotes are valid bounds; actual settlement can consume funds backing another request. |
| 3 | A 1,000-request burst cannot cause unfunded provider exposure | Not guaranteed. Serialized admission prevents a simple shared-balance race, but it does not repair underquoted or unbounded execution. |
| 4 | A running request exhausting funds immediately halts all same-account work | Not implemented in the reviewed stock monetary path. |
| 5 | Full cache-write input plus catalog maximum output before execution | Fails in the stock scalar quote path; client-cap and missing-catalog semantics also differ. |
| 6 | New requests subtract all existing pessimistic commitments | Mechanically present, but only as good as the recorded maxima. |
| 7 | Failed billing attempts persist and retry until successful | Fails both before first durable enqueue and after the settlement retry cap. |
| 8 | Database spikes preserve billing and eventual processing | Not warranted. Durable spool replay helps, but producer loss windows, finite settlement retries, and store-wide serialization remain. |
| 9 | Useful affordability error with completion-token allowance | Fails. New exposure-denial errors are not mapped to a useful financial response. |
| 10 | Double-sided accounting ledgers | Substantially implemented in inspected financial customer and provider posting paths. This does not prove every obligation reaches those paths. |
| 11 | Debit customer, record spend, record operator revenue | Atomic for successful positive customer settlements; not guaranteed for all incurred work. |

## What should be preserved

The exposure store takes transactional account locks and accounts for existing open exposures. The V2 admission path also pins posting ownership within that transaction. Monetary arithmetic uses integer amounts and checked operations; reserve token pricing rounds upward. Successful customer settlement journals the customer debit and usage-revenue credit together with the balance change, exposure closure, processing marker, and posting ownership completion. The provider journal has separate cost-of-service/payable entries. Already accepted terminal-spool records have durable replay and backoff.

The normal auxiliary client preserves the parent principal/scope, marks the internal origin, and re-enters the normal executor. Do not describe all auxiliary requests as bypassing admission. They remain subject to the same quote, settlement, and persistence weaknesses identified below.

Sources: `internal/core/billing/exposure.go`; `internal/infra/billingstore/exposure_store.go`; `internal/infra/billingstore/cutover_v2_admission_f3_store.go`; `internal/infra/billingstore/call_settlement.go`; `internal/infra/billingstore/provider_cost_store.go`; `internal/infra/billingspool/spool.go`; `internal/core/auxreq/client.go`.

# Findings

## F01 — P1: Default commercial selection contradicts all-attributable charging

**Sources:** `internal/core/billing/retail_selector.go` (`ResolveRetailSelectionPolicy`, `selectRetailLegInfos`, `selectInterruptedRetailLegInfos`); `internal/core/billing/rating.go` (`SelectRetailBLegs`, `rateCustomerCharge`).

The default surfaced-turn policy selects a surfaced/winning B-leg on success. For interrupted calls it selects one accepted leg, preferring a surfaced leg and otherwise the latest durable attempt. This is not a sum of all attributable execution. An all-attributable mode exists, but its existence does not make it the default or enforce it for this product.

The business model described in issue #620 deliberately separates all-leg operator costs from winner-oriented customer charging. That may be a valid product elsewhere, but it is incompatible with the requested contract. A failed first provider attempt followed by a successful second attempt can have two supplier costs and only one customer usage charge.

**Required change:** Introduce an explicit, frozen commercial contract for this deployment: customer scope is all attributable, potentially chargeable provider work. Enforce it at composition/startup, not through optional documentation. Preserve separate supplier COGS and customer tariffs; do not assume they have identical amounts. Never treat delivery success as the definition of chargeability. Do not activate broader charging before fixing the corresponding reservation bound.

## F02 — P1: Requests already dispatched to a backend can be recorded as NeverStarted

**Sources:** `internal/core/runtime/executor_open_attempt.go` (`run`/open path, `rollbackSimple`, `rollback`); `internal/core/runtime/attempt_session.go` (`TerminalizeAttempt`); `internal/core/runtime/billing_leg.go` (`billingLegRecord`); `internal/core/billing/retail_selector.go` (`selectRetailLegInfos`).

The open path sets `openInvoked` and `backendAttempted`, then invokes `be.Open`. Certain time-to-first-token timeout branches subsequently call rollback with `LegOutcomeNeverStarted`. The terminal record preserves the explicit outcome. The retail selector excludes NeverStarted before applying even all-attributable selection.

A timeout after dispatch is not evidence that the provider did no billable work. The provider can accept/process a request before the proxy receives the response. Even evidence recovered by a billing finalizer does not cause this explicit NeverStarted outcome to be corrected in the inspected terminal-record path.

**Counterexample to implement:** Use a controlled backend whose Open records acceptance, produces recoverable provider-billing evidence, and then exceeds TTFT. The call must not be declared free solely because the response did not arrive.

**Required change:** Separate dispatch state from execution/delivery outcome. NeverStarted must mean dispatch was provably not attempted. Use a liability-bearing `dispatch_unknown`/equivalent state for ambiguous transport failures. Positive authoritative provider evidence must not be excluded by a transport label. Persist request/dispatch IDs and retain the exposure until the ambiguity is reconciled.

## F03 — P1: Thinker and executor can both be marked surfaced, making default retail selection ambiguous

**Sources:** `internal/core/runtime/executor_settlement.go` (`finalizeResponseFinishedAuthority`); `internal/core/runtime/attempt_session.go` (`TerminalizeAttempt`); `internal/core/runtime/billing_leg.go` (`billingLegRecord`); `internal/core/runtime/interleaved_stream.go` (`recvThinker`); `internal/core/runtime/interleaved_open.go` (`openInterleavedExecutorContinuation`); `internal/core/billing/retail_selector.go` (`selectSurfacedWinner`).

Normal completion terminalizes the thinker attempt with normal-finish/winner evidence. Attempt terminalization marks a normal finish as SurfacedYes regardless of whether an outer interleaved wrapper consumes the output internally. The thinker-specific branch prevents premature request closure, which is correct, but it occurs after construction of the thinker leg record. The later executor also normally finishes as SurfacedYes under the same billing-call lineage. The default retail selector rejects multiple surfaced legs.

The hidden wrapper demonstrates that inner stream completion is not the same as delivery to the external customer. The source-derived failure is a normal thinker-plus-executor call producing two surfaced legs and then an ambiguous retail selection.

**Required change:** Model billability, successful execution, and client delivery as separate facts. Do not derive SurfacedYes from CommandNormalFinish. Carry an explicit trusted role and actual frontend-delivery attribution into the billing record. Under all-attributable charging both payable legs should charge independently, regardless of visibility. Preserve the existing guard that defers request closure until the executor finishes.

## F04 — P1: The stock quote is not the requested pessimistic upper bound

**Sources:** `internal/infra/runtimebundle/billing_compose.go` (`ComposeBilling`); `internal/infra/billingadmission/adapter.go` (`Quote`, `maxChargeInput`, `chargeRoutes`); `internal/core/billing/estimate.go` (`EstimateMaxCustomerCharge`, route estimate, `ceilingOrError`).

Stock ComposeBilling supplies ordinary pricing, policy, model-max resolver, and optional static ceiling. It does not supply the adapter's rich BaseTariff/ComponentBounds/ModelTariff configuration. The scalar route estimate prices input at the ordinary input rate, not an explicit full cache-write bound. Full cache miss does not imply that ordinary input price covers cache creation.

The scalar estimator also reduces output to a smaller client cap when one is supplied; it requires a model maximum rather than implementing the requested optional client-cap fallback when the catalog maximum is absent. A smaller strictly enforced cap can be economically sensible, but that is a different policy from reserving catalog maximum first.

The conservative-ceiling escape path accepts a nonnegative configured fixed amount when strict estimation otherwise fails. A cost-pass-through SafeBound similarly represents a configured assertion. A number named “conservative” is not a proof that actual provider execution cannot exceed it; zero is not rejected by the generic nonnegative ceiling check.

**Required change:** Build a typed quote from the actual effective request, a versioned catalog/capability snapshot, and the frozen tariff. Include cache-write TTL/semantics, maximum output, any separately charged reasoning/modalities/resources, and fixed fees at the correct scope. Bind the quote to the exact request/route that may execute. Reject missing bounds unless an explicit fallback is both configured and enforced at the provider boundary.

Do not blindly add ordinary input and cache-write charges: define whether a provider's cache-write price is inclusive or a surcharge. Reservation should maximize the tariff over valid billable component combinations; actual billing should use observed components without overlap double counting. Keep the bounded rich rater rather than adding another independent interpretation of cache semantics.

## F05 — P1: Execution can differ from the request and cardinality that were quoted

**Sources:** `internal/core/runtime/billing_admission.go` (`billingRoutePlanInput`, `billingRequestSize`); `internal/infra/billingadmission/adapter.go` (`collectPlannedLeaves`, `chargeRoutes`); `internal/core/runtime/executor_open_attempt.go` (`evaluateCandidate`, final backend ingress); `internal/core/runtime/recovery_controller.go` (`newReplacementOpener`); `internal/core/runtime/interleaved_open.go` (`shapeAttemptCall`, `openInterleavedExecutorContinuation`).

Financial admission quotes the prepared request and selector leaves. Candidate shaping and plugin attempt transforms happen later. The thinker/executor continuation refreshes memo steering and can send a larger executor prompt. Runtime replacement/continuation opens use the already admitted billing facts. The quote enumerates selector leaves; it does not receive a formal execution manifest proving allowed repeated attempts, continuation count, prompt growth, and remaining liability.

There are per-attempt preflight and usage-authority admissions. Those must not be mistaken for financial exposure top-ups: monetary accounting is explicitly separated from those non-money authority operations.

It is not necessary to assert that every retry repeats the same leaf to establish the defect. A thinker memo appended to a later executor request already invalidates a bound based on the original request size unless that growth was reserved in advance.

**Required change:** Reserve a bounded execution envelope initially, and require every provider-open path to verify that its final request and attempt allocation fit that envelope. Before any enlargement or additional chargeable attempt, atomically enlarge the account reservation or refuse dispatch. Reuse the existing quote/admission domain rather than introducing an unrelated quota approximation. The standard auxiliary client should continue re-entering Execute; preserve durable parent-child financial lineage and require fresh atomic funding for each child.

## F06 — P1: An actual charge can consume another live request's reserved funds

**Sources:** `internal/infra/billingstore/call_settlement.go` (`ApplyCallBillingResult` and its transactional implementation); `internal/core/billing/account.go` (`ApplyBalanceDelta`); `internal/core/billing/call_post_usage_worker.go` (`ProcessOnce`).

Settlement detects whether actual charge exceeds the call's maximum, but successful settlement only requires that the debit respect the account's ordinary credit floor. It does not preserve funds reserved for sibling calls. The worker discards successful settlement result details, including the breach flag.

**Source-derived numerical trace:** A prepaid account has $10. Two calls reserve $5 each. Call A incurs $8. Its debit leaves $2, which respects the zero floor, so it can settle while call B still has a $5 reservation. B then incurs $5, but its debit fails. Cash staying nonnegative did not prevent unfunded provider work.

On insufficient funds the settlement path marks the account reconcile_required and returns without the normal customer/revenue posting. Subsequent settlement requires AccountReady. This stops admission/settlement activity; it does not account for the already incurred obligation or cancel active requests.

**Required change:** Prevent overruns before dispatch using an enforced execution bound. If an invariant breach nevertheless occurs, freeze new account work and broadcast cancellation. Keep incurred obligations financially visible even when collection fails: distinguish prepaid cash, committed exposure, receivable/debt, and account admission state. Never solve this by silently capping the invoice at the reservation or by consuming sibling reservations without recording the deficit. A non-ready account must still permit safe settlement, debt recording, refunds, and reconciliation.

## F07 — P1: No account-wide monetary exhaustion cancellation is wired

**Sources:** `internal/infra/runtimebundle/billing_compose.go`; `internal/core/billing/call_post_usage_worker.go`; `internal/infra/billingstore/call_settlement.go`; `internal/core/runtime/executor_settlement.go`; `internal/core/runtime/turn_terminal.go`.

The reviewed monetary path admits and settles calls. It does not connect a financial-exhaustion decision to a same-account registry/broadcast of all active A-legs and B-legs. Existing A-leg cancellation and non-money usage-authority mechanisms are not an account-wide financial breaker. Ignoring settlement breach results further prevents reaction even after the problem is detected.

**Required change:** Add an account financial state/epoch, a same-account active-execution registry, and a durable cancellation outbox. A denied financial extension for running work or actual bound breach must atomically freeze admission and advance the epoch. All running children, siblings, retries, and continuations must receive cancellation; all provider-open paths must check the current authorization/epoch. Registration and launch must be fenced against a concurrent freeze. Multi-instance deployments require propagation and lease/partition behavior, not merely an in-process map.

“Immediately” should be made operationally precise: no new dispatch after a committed fence, immediate local cancellation dispatch, measured bounded propagation, and a prepaid bound for any provider-side cancellation tail. Closing a socket is not proof that remote billing ceased. Strict operation must deny backends whose possible liability cannot be bounded.

## F08 — P1: Terminal producer failures can occur before any durable retry obligation exists

**Sources:** `internal/core/runtime/billing_call_closure.go` (`handoffBillingTurn`); `internal/core/runtime/billing_leg.go` (`appendIndependentCallLegStrict`); `internal/core/runtime/billing_admission.go` (`appendExposureAbortClosure`); `internal/core/runtime/attempt_session.go` (`TerminalizeAttempt`); `internal/core/runtime/stream_terminal.go` (`Terminalize`); `internal/core/terminal/owner.go` (`Claim`); `internal/infra/billingspool/spool.go`.

Terminal billing uses a bounded append attempt. The spool retries records after a successful durable enqueue, but an enqueue can fail due to capacity, disk, or database errors before a row exists. The terminal owner has already claimed execution; later terminal calls observe the prior outcome rather than rerunning effects. Attempt evidence is drained during terminalization. A mutex and a success flag do not create a durable retry owner for a failed first append.

Some early failure branches are even weaker: call-record sealing errors log and return nil; invalid independent leg inputs can log and return nil; the canonical exposure-abort helper discards its append error. These are not truthful successful monetary handoffs.

**Counterexample to implement:** Admit, dispatch, and incur cost. Make the first terminal sink commit fail. Allow a second Close/terminal invocation and restart the process. Demonstrate that a durable obligation still exists and is eventually collected. The current terminal/spool architecture does not establish that guarantee.

**Required change:** Persist a dispatch/financial obligation before provider execution and attach replayable terminal completion to it. Reserve durable queue capacity before accepting spend. Preserve exact payload/recovery state until a durable consumer acknowledges it. Decouple client-facing terminal completion from completion of financial work. Never return success on monetary validation/sealing failure; quarantine the obligation with an actionable reason. Keep the account's unresolved exposure until the liability is resolved.

## F09 — P1: Non-money authority errors can prevent monetary call closure

**Source:** `internal/core/runtime/executor_settlement.go` (`finalizeResponseFinishedAuthority`, `settleRequestAuthorityWithFrontendEgress`).

The request terminal callback returns immediately when non-money request-authority/frontend-egress settlement fails. Only after that operation succeeds does it call handoffBillingTurn. A failed frontend-egress append or durable-pending quota operation can therefore prevent creation of the monetary call closure, even when backend work occurred and leg evidence exists. The terminal owner does not ordinarily re-execute a claimed callback.

**Required change:** Model the terminal work as independent durable obligations. Attempt monetary handoff regardless of a non-money effect failure, and retain both errors/work states. Merely replacing the early return with errors.Join is useful but insufficient unless first monetary enqueue failure itself is recoverable. No unrelated quota or diagnostic failure may suppress the financial obligation.

## F10 — P1: Settlement stops automatically retrying after 20 attempts

**Source:** `internal/infra/billingstore/call_usage_store.go` (`completeCallClaimMaxAttempts`, `ClaimCompleteCalls`, `RetryCompleteCall`).

The complete-call settlement retry limit is 20. Calls exceeding it become reconcile_required. Settlement-reconcile errors can go directly to that status. Ordinary claims include pending and expired claimed work, not reconcile_required. Durable retention of the row is good, but it is not retry-until-success.

**Required change:** Retry transient storage/availability errors without a terminal attempt cap, using capped exponential backoff and jitter. Keep claim leases and idempotent operations. Semantic invalidity or permanently missing evidence should become a durable reconciliation case with a named owner, repair criteria, alert, and explicit requeue path—not a forgotten dead letter. Separate account admission state from the ability to post already incurred debt.

## F11 — P1, scalar compatibility path: Missing failed-leg usage can be silently omitted

**Source:** `internal/core/billing/rating.go` (`acceptedCustomerLegs`, `rateCustomerCharge`, `chargeLeg`).

Scalar selection excludes legs lacking accepted evidence. For a selected failed/interrupted leg, chargeLeg can skip an absent quantity because strictEvidence is only enabled for a completed surfaced leg. Absent output usage is then not distinguished from a known zero output charge. This is a customer collection gap when a provider can bill a failed request whose final usage was not received.

**Required change:** Represent each required billable dimension as known-zero, known-positive, pending/unavailable, disputed, or not-applicable. An accepted/dispatched request with missing usage must create a reconciliation obligation, not a successfully complete zero charge. Recover provider request/invoice evidence and post idempotent adjustments. Apply the principle to the legacy path as long as that path remains enabled; do not assume component evidence magically repairs every scalar compatibility decision.

## F12 — P2, explicit contract failure: Affordability denials become opaque errors

**Sources:** `internal/core/billing/exposure.go` (`EvaluateAdmit`); `internal/infra/billingadmission/adapter.go`; `internal/plugins/frontends/execerr/execerr.go`.

The new exposure path returns ErrExposureInsufficient. The inspected frontend classifier handles older insufficient-spendable/account-not-ready cases but not this new error. The new condition can fall through to HTTP 500/internal error. Even the recognized path says only insufficient credit rather than giving the requested completion-token allowance.

**Required change:** Return a typed affordability result built from the atomic admission snapshot. Include currency, allowed credit, balance, other open commitments, this request's input/fixed bound, output cap/source, estimated affordable output, and a stable machine code. Map it consistently across frontend protocols, streaming preflight, and large-body/wire execution. Use a financial denial status such as 402 and distinguish it from rate limiting and storage unavailability.

For a simple single-model linear tariff:

    H = balance - credit_floor - sum(other_open_pessimistic_commitments)
    I = this_request_cache_write_input_bound + fixed_and_other_nonoutput_bounds
    affordable_output = max(0, floor((H - I) / per_output_token_price))

Admission requires `H >= I + selected_max_output * per_output_token_price`.

The expression balance minus other commitments minus this entire new maximum is a surplus/deficit; it is not the numerator for calculating affordable output, because that would subtract maximum completion cost twice. For nonlinear pricing or multi-leg routes, find the largest output limit whose full quote fits H instead of claiming a misleading scalar formula.

## F13 — P2 capacity risk: Every store's monetary transactions serialize on a cutover row

**Sources:** `internal/infra/billingstore/cutover_serialization_store.go` (`loadAccountingCutoverLocked`, `ensureAndLockAccountingCutoverTx`); `internal/infra/billingstore/exposure_store.go`; `internal/infra/billingstore/cutover_v2_admission_f3_store.go`.

PostgreSQL ordinary financial transactions take FOR UPDATE on the per-store cutover marker. With one store per production database this serializes different accounts' admissions and postings. Admission also loads and decodes all open exposures for an account. A growing active-call burst consequently creates increasing per-admission work.

This is an inspected contention mechanism, not a measured throughput limit or a claim that 1,000 requests necessarily crash the server. Coupled with finite producer timeouts and finite settlement retries, it weakens the database-spike survival story.

**Required change:** Preserve cutover fencing but separate ordinary shared access from exclusive activation, for example through correctly ordered shared/exclusive PostgreSQL row/advisory locking. Keep account-local serialization for money. Maintain a transactional reserved-total projection that can be rebuilt from the exposure ledger instead of repeatedly decoding all rows. SQLite remains a single-writer database: use short transactions, a bounded writer queue, durable spool capacity, and backpressure rather than pretending it has PostgreSQL concurrency. Benchmark before setting capacity claims.

## F14 — P1 coverage blocker: Stock V2 rejects all routes touching OpenAI-native usage mappers

**Sources:** `internal/infra/runtimebundle/build_executor.go` (`bindUnsupportedV2NativeUsage`); `internal/standardplugins/custom_backends.go` (`UsesOpenAINativeUsageMapper`); `internal/infra/billingadmission/adapter.go` (`guardUnsupportedV2NativeUsage`).

The stock V2 composition deliberately rejects planned routes containing OpenAI Chat/Responses native usage mapper kinds, including custom-compatible variants. The restriction is based on backend kind, not only whether a particular request uses audio. A planned fallback leaf can therefore make a route inadmissible.

This is a valid fail-closed safeguard, not an unsafe bypass. It nevertheless means this implementation cannot be presented as complete billing across the advertised API/backend combinations.

**Required change:** Supply a frozen capability/units proof for the actual request and tariff, or explicitly keep unsupported combinations disabled. Narrow support only when native quantities, overlaps, revisions, and upper bounds are certified. Never remove the guard merely to make smoke tests pass.

# Assessment of #692 / #694 / #698

The reviewed overlap-advisory implementation reports uncertain component relationships without modifying money. The assessor uses deterministic traversal and bounded report capacity; the catalog path validates/compiles publication material before publishing it. These are useful properties within the requested advisory feature.

No additional independently confirmed release blocker is asserted here specifically against the new advisory algorithm. That is not a full proof of every algorithmic edge case. More importantly, an advisory is not a financial upper bound, a chargeability policy, a durable delivery mechanism, or proof that partially overlapping tariff components are disjoint. The completion of this narrow feature cannot establish the eleven wider financial contracts.

Sources: `internal/core/billing/component_rater_advisory.go`; `internal/core/billing/component_rater_overlap.go`; `internal/infra/billingcompose/catalog.go`; PR #698 metadata.

# Implementation plan

## Phase A: Freeze the actual product contract and repair the financial bound

Adopt all-attributable customer selection for the intended paid deployment, but do not activate it before compatible funding is enforced. Define the customer tariff separately from supplier COGS. Specify which backend operations are chargeable, how ambiguous dispatch is handled, and whether catalog maximum or a strictly enforced client cap controls admission.

Replace the scalar stock quote configuration with a single bounded component quote used by admission and pre-open verification. Quote the final backend-effective request, including transformations or a proven upper bound on them. Preserve catalog/model/tariff versions, cache semantics, output bounds, applicable fees, and an execution-plan fingerprint. Unknown or unsupported dimensions must cause pre-dispatch refusal in strict mode.

A useful financial invariant is:

    posted_collectible_funds_minus_floor >= sum(unsettled_committed_liability_bounds)

A quote is safe only if every allowed downstream provider execution remains inside its allocation. Track remaining exposure, not merely elapsed calls or output already delivered. Complete usage that has not yet been settled still counts as a commitment.

The operator-protection statement also needs pricing discipline: sufficient customer retail credit does not prove supplier costs are covered if tariffs intentionally underprice providers. Either certify the retail/provider relationship or assign explicit operator-funded subsidy/risk budgets. Do not mislabel intended subsidies as funded customer exposure.

## Phase B: Enforce the bound at the provider-open boundary

Keep existing A-leg/B-leg ownership and allocation mechanisms, but add a mandatory financial authorization result to every provider-open path. An attempt may consume a slice of an existing bounded plan or obtain an atomic top-up. Repeated attempts, thinker continuations, memo expansion, plugin transforms, tool-triggered auxiliary calls, and parallel legs must not open on the strength of an unrelated original scalar estimate.

Authorization should bind account, billing call, B-leg, request/plan fingerprint, quote version, reserved amount, output limit, account epoch, and dispatch identity. Host-owned immutable evidence, rather than client-supplied metadata, must authorize these fields. Reject reuse for another leg or modified request. Define behavior for provider SDKs that internally retry or redirect: either expose each economic attempt or disable unaccounted automatic retries in strict mode.

## Phase C: Durable dispatch and completion ownership

Before handing the request to the provider, persist enough identity and liability state to recover a crash immediately after dispatch. Give terminal completion a durable owner independent of request/stream object lifetime. Reuse the existing spool where possible, but close the pre-enqueue hole by reserving capacity and retaining a dispatch recovery record.

Suggested state vocabulary, adapted to existing schema rather than blindly creating a parallel subsystem:

    reserved -> dispatch_intent -> dispatched_or_unknown -> evidence_pending
             -> rated -> posted -> settled

The provider dispatch side effect cannot be made atomically transactional with a local database. Recovery therefore needs stable provider request/idempotency keys where supported and an explicit ambiguity state otherwise. Never replay an ambiguous provider request simply because a local response was lost. Billing delivery can be at-least-once while financial effects are effectively exactly-once through existing journal keys/fingerprints.

A request must not be forgotten because an observer failed, a quota settlement failed, the client disconnected, an append timed out, or a terminal callback had already won. Keep every unresolved liability visible until evidence or a documented explicit write-off resolves it.

## Phase D: Make settlement and exhaustion behavior financially honest

Retain the atomic customer journal/balance/exposure/processed transaction. Extend it to represent collection deficits and incurred receivables rather than blocking all accounting when prepaid cash is insufficient. Account-not-ready must block new spending but not prevent posting known historical obligations, refunds, or corrections.

On a bound breach or failure to fund required continuing work, atomically freeze admission and enqueue account cancellation. Consume the settlement breach result instead of discarding it. Fence all newly launching siblings and propagate cancellation to all processes holding account work. Preserve enough reservation for provider-side work that cannot be stopped instantly.

## Phase E: Repair retry and recovery liveness

Remove retry exhaustion as an automatic terminal state for transient database failures. Use capped exponential backoff, jitter, bounded worker concurrency, and lease-based claims. Distinguish a retryable operational failure from invalid evidence requiring repair; retain both durably. Reconciliation cases require owner, reason, age, next action, and a requeue transition. Do not repeatedly invent charges from corrupt evidence.

Recovery must inventory at least: open exposures without closures; dispatched legs without terminal evidence; closures with missing expected legs; rated but unposted calls; committed journals without acknowledged worker completion; stale claims; exhausted/reconcile-required work; and conflicting evidence revisions. Do not release orphaned exposure just because a wall-clock TTL expired.

## Phase F: Performance, frontend behavior, and compatibility

Reduce cutover lock contention without weakening epoch fencing. Use account-local reserved totals and bounded database work. Expose financial denials consistently. Publish a supported frontend/backend/evidence matrix and enforce it at runtime. A deliberately blocked combination is safer than an unbounded accepted one, but must not be advertised as implemented paid support.

# Independent acceptance scenarios

These scenarios should be implemented with independent counters and durable database inspection. Do not merely assert internal mock expectations or reuse the estimator under test as its own oracle.

| Scenario | Independent acceptance condition |
|---|---|
| 1,000 concurrent requests, balance covers only N full bounds | No more than N authorized liabilities are dispatched; compare actual provider-open counter with durable funding. Repeat across processes. |
| Two accounts in parallel | No account cross-charge or cross-cancel; investigate store-wide lock latency independently. |
| Two $5 reservations on a $10 account, one actual cost $8 | Such overrun is prevented by execution limits, or creates an explicit invariant breach, cancellation, and visible liability—not silent theft of sibling funds. |
| Full cache miss and each supported cache-write TTL | Quote covers all input under the actual write-price semantics plus the chosen maximum output. |
| Missing model catalog maximum | Deny before dispatch, or use only the explicitly enabled, enforced fallback. |
| Smaller user cap and larger catalog cap | Behavior matches the frozen product policy and the exact cap transmitted to the provider. |
| Final candidate transform increases prompt | Re-quote/top-up or refuse before provider Open. |
| Thinker output enlarges executor prompt | Both charges attributed; second prompt fully funded; no duplicate surfaced-winner ambiguity. |
| Retry/failover before first visible output | Every dispatched economic attempt retains its liability; no unquoted multiplicity. |
| Failed Open after provider acceptance | Not NeverStarted; provider charge reconciles and customer obligation remains visible. |
| Parallel winner plus losing billable requests | Customer all-attributable total and operator COGS independently include all payable work. |
| Failed stream without final usage | Unknown is not zero; retain evidence-pending obligation and reservation/reconciliation state. |
| Synchronous and detached auxiliary requests | Correct parent account, independent funded dispatch, durable financial lineage. |
| Account freezes while sibling is registering/opening | Epoch/launch fence prevents a new unauthorized backend dispatch. |
| Account exhaustion on one of several processes | All processes deny further account launches and cancel ongoing account work within the stated operational bound. |
| Client cancels or connection disappears | Monetary work survives request-context cancellation and bills incurred provider work. |
| First terminal append fails | Liability survives repeated Close and process restart even before a terminal spool row exists. |
| Non-money frontend-egress/quota settlement fails | Monetary closure is still durably scheduled. |
| Database outage spans more than 20 attempts | All retained valid billing work resumes and posts after recovery without manual resurrection. |
| Spool capacity/disk exhaustion | New unfunded work is denied before provider dispatch; existing obligations remain recoverable. |
| Crash after provider dispatch, before terminal handoff | Recovery identifies the ambiguous dispatch and retains its bound; no blind duplicate provider execution. |
| Crash after journal commit, before worker ACK | Replay posts no duplicate debit/revenue and closes the same obligation. |
| Provider correction or late invoice | Idempotent adjustment with original linkage; no silently lost correction or double charge. |
| Price/catalog refresh during an active call | Frozen contractual pricing remains reproducible; no underfunded reinterpretation. |
| Unsupported native usage/modalities/API combination | Clear pre-dispatch rejection until the combination is certified. |
| Financial rejection through each frontend and wire path | Useful stable financial code, appropriate status, and affordable-token explanation where meaningful. |

A release check should reconcile, for every authorized backend attempt, its account, parent request, reserved bound, dispatch state, evidence state, customer charge, operator cost, journal identifiers, and residual unresolved liability. Sum-based checks should use independent backend/provider observations, not solely the application's own “processed” flags.

# Rollout and repair of existing data

Do not simply switch the selection policy on historical completed calls. Existing calls are bound to their admitted policy and pricing. Changes to customer charging require explicit versioning and appropriate commercial handling; silent retrospective repricing is not an engineering shortcut.

Inventory current open exposures and reconcile_required work first. Identify NeverStarted records that have dispatch evidence, accepted provider IDs, positive provider usage/cost, or TTFT-after-Open traces. Identify thinker/executor calls with multiple surfaced legs. Identify missing closures and calls that were zero-rated due to absent failed-leg quantities. Preserve original records; append audited corrections or reconciliation decisions rather than rewriting immutable evidence.

Ship new admission bounds, dispatch fences, and durable recovery before enabling broader all-leg retail charging. Then enable the new frozen policy only for newly admitted calls. Keep deliberately unsupported V2 combinations blocked until their proofs and independent fault scenarios pass.

## Bottom line

The system has valuable accounting foundations, but a correct double-entry transaction at the end of the pipeline does not guarantee a financially correct pipeline. The release blockers are the mismatch between quoted and executable liability, the exclusion/misclassification of chargeable work, loss or suspension of billing obligations, and the missing account-wide exhaustion response. Completing an overlap-advisory feature does not close those gaps.
