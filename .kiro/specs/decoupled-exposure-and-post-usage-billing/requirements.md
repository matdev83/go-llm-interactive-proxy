# Requirements Document

## Introduction

Go-LIP now has an authoritative usage-record billing engine with pessimistic monetary authorization holds, post-turn rating, a double-entry financial journal, and durable Bun-backed account state. The implementation is substantially safer than the prior stream-time accounting architecture, but its admission model still treats pessimistic affordability as a financial reservation: authorization creates durable holds, mutates `reserved_nano`, posts authorization-book journal entries, and later requires release/settlement recovery.

This specification replaces that model with a simpler telecom-style separation:

1. **credit screening** asks whether an account is plausibly able to afford another LLM call before expensive routing/rating work;
2. **exposure admission** asks whether settled financial headroom covers the pessimistic maximum of the new call plus every still-open call exposure;
3. **execution** performs no financial mutations and no exposure updates while the LLM call is running;
4. **terminal usage production** durably records immutable per-B-leg usage and one per-call closure record;
5. **post-usage billing** independently posts customer revenue/balance effects and provider COGS from those durable records.

Operational exposure is deliberately **not money**. It never produces a debit/credit journal transaction and never mutates the user's financial balance. One exposure row is inserted atomically when a call is admitted and remains unchanged until the post-usage customer settlement closes it. Because completed-but-unbilled calls retain the same open exposure, the admission formula is safe without a second active/unbilled state transition.

The design targets the smallest architecture that still proves hard prepaid/postpaid limits under concurrent calls and multiple proxy processes, while preserving at-least-once usage processing and at-most-once financial effects.

## Boundary Context

- **In scope:** two-stage affordability admission; per-call billing identity distinct from A-leg/session identity; pessimistic route/model cost calculation; atomic operational exposure admission; concurrent prepaid/postpaid safety; immutable per-B-leg and per-call usage records; durable Bun usage spool; post-usage customer settlement; independent provider-cost accounting; double-entry financial journal; zero-charge processing evidence; reconciliation/rebuild; removal of monetary holds/reserved balances/authorization journal paths; deletion of runtime billing aggregation/barrier/release machinery; host composition and reporting migration.
- **Out of scope:** token-by-token debiting; mid-generation spend termination; invoices/tax/payment-provider workflows; FX; changing provider protocols; changing retry/failover rules; changing non-money token/request quota enforcement; generic message buses/event sourcing/CQRS; arbitrary billing rule languages; guaranteeing survival of simultaneous total loss of every configured durable storage replica.
- **Boundary ownership:** `internal/core/billing` owns provider-neutral quote/rating/accounting policy; runtime owns call lifecycle and B2BUA execution but only invokes the two admission checks and terminal usage sink; backend adapters finalize provider evidence; `internal/infra/billingstore` owns Bun persistence and account-scoped atomicity; composition wires immutable catalogs and stores; reports are read-side consumers.
- **Hexagonal lens:** pure quote/rating functions are domain policy; admission and post-usage processing are app orchestration; runtime is a driving adapter; Bun is a driven adapter; runtimebundle is the composition root.
- **Revalidation triggers:** A-leg/session semantics, B-leg lineage, route-plan construction, provider final billing evidence, billing account schema, usage-record persistence, runtime terminal ownership, immutable snapshot catalog, authoritative billing host composition.
- **Supersession:** this spec supersedes the *admission/hold and runtime handoff direction* of the completed `usage-record-ledger-billing` and `billing-host-composition` specs. Their financial-journal, immutable-snapshot, B2BUA attribution, and trusted-provisioning results remain reusable unless explicitly replaced here.

## Requirements

### Requirement 1: Make Settled Money and Operational Exposure Separate Truths

**Objective:** As a maintainer, I want financial balance and unbilled call exposure to be separate concepts, so that affordability can be safe without turning call setup into accounting.

#### Acceptance Criteria

1.1. The authoritative customer financial balance shall be derived only from posted financial journal operations and durable account policy, not from call admission state.
1.2. Operational call exposure shall not create financial debit/credit journal entries.
1.3. Operational call exposure shall not mutate `balance_nano`, a replacement financial-balance field, or any financial ledger account.
1.4. The target architecture shall not require an authorization-book journal for pessimistic call admission.
1.5. The target architecture shall not require a materialized `reserved_nano` financial-account field for call admission correctness.
1.6. Reporting shall distinguish settled spend, open pessimistic exposure, and provider cost rather than presenting exposure as settled money.
1.7. Financial reconciliation/rebuild shall reconstruct settled balance without consulting open exposure rows.
1.8. Exposure reconciliation shall reconstruct outstanding exposure independently from open exposure rows without replaying financial journal entries.

### Requirement 2: Identify Every Billable LLM Call Independently of A-leg and Session Identity

**Objective:** As a billing maintainer, I want one stable identity per client inference call, so that a long-lived A-leg/session can contain many independently billable calls.

#### Acceptance Criteria

2.1. The system shall define a proxy-owned `BillingCallID` or equivalent stable identity generated once per incoming billable LLM invocation.
2.2. A-leg ID shall remain session/B2BUA correlation and shall not be sufficient by itself as a customer-settlement idempotency key.
2.3. Session ID shall remain reporting/correlation metadata and shall not be sufficient by itself as a customer-settlement idempotency key.
2.4. All internal retries, failover alternatives, and parallel B-legs belonging to one incoming call shall share the same BillingCallID.
2.5. Each B-leg usage record shall be uniquely identified by BillingCallID plus B-leg identity.
2.6. Customer settlement shall be idempotent by account plus BillingCallID.
2.7. Reuse of one A-leg across multiple subsequent calls shall produce distinct billing call identities and distinct customer billing operations.
2.8. Request-local billing identity shall not be added to provider wire protocols or require provider SDK changes.

### Requirement 3: Add a Cheap Pre-routing Credit Screen

**Objective:** As an operator, I want obviously insolvent requests rejected before expensive routing/rating work, so that abusive or hopeless calls do not consume routing infrastructure.

#### Acceptance Criteria

3.1. After authentication/principal resolution and before route expansion, model pricing lookup, model maximum-output lookup, or token-estimation work, authoritative billing shall perform one cheap account credit screen.
3.2. The cheap screen shall use only indexed/materialized account state needed to determine account readiness, currency, settled balance, credit floor/limit, and configured minimum pre-route headroom.
3.3. The cheap screen shall perform no provider calls, connector process work, route candidate enumeration, tokenization, pricing resolution, usage-record writes, exposure inserts, journal writes, or account mutations.
3.4. The minimum pre-route headroom shall be a typed billing policy/config value; `0` shall permit zero-headroom accounts to reach detailed admission for potentially free calls, while a positive value may intentionally reject micro-headroom accounts earlier.
3.5. If settled headroom is below the configured minimum, the request shall be denied before routing.
3.6. If the account does not exist, is not ready, is `reconcile_required`, has a currency/configuration error, or the authoritative account store is unavailable, the cheap screen shall fail closed.
3.7. Passing the cheap screen shall not guarantee admission; the detailed post-route exposure check remains authoritative.
3.8. Cheap-screen failure shall expose stable internal error classifications suitable for frontend 402/403/503 mapping without leaking balance-store details.

### Requirement 4: Compute One Conservative Maximum Customer Charge After Routing

**Objective:** As a hard-credit operator, I want a deterministic pessimistic charge bound for the selected route plan, so that concurrent admission can be decided without live debiting.

#### Acceptance Criteria

4.1. After side-effect-free route planning and before the first provider/connector side effect, billing shall compute a finite `MaxCustomerCharge` for the new BillingCallID.
4.2. The calculation shall use immutable customer pricing and charging-policy snapshot identities that are later reused for customer rating.
4.3. Pessimistic input cost shall use deterministic input quantity estimation and shall assume the highest applicable non-cached/non-discounted input price when cache eligibility is uncertain.
4.4. Pessimistic output cost shall use `min(client_max, model_max)` when a valid client maximum lowers the model maximum; otherwise it shall use the model maximum.
4.5. Fixed/request/resource charges that can increase customer price shall be included.
4.6. For customer policies that charge one logical surfaced result, internal failover alternatives shall contribute the maximum possible one-call customer charge rather than the sum of operator retry costs.
4.7. For policies that can charge multiple parallel/pass-through B-legs, all potentially customer-chargeable legs shall be included.
4.8. Operator/provider cost shall not affect customer admission unless the customer policy explicitly passes that cost through.
4.9. Unknown required rates, unknown required quantities, unknown model output bounds, arithmetic overflow, or currency mismatch shall fail closed in strict credit mode unless an explicit conservative monetary ceiling covers the uncertainty.
4.10. The quote shall be side-effect free and unit-testable using plain Go values.

### Requirement 5: Admit Calls Using Atomic Operational Exposure, Not Monetary Holds

**Objective:** As an operator, I want concurrent calls admitted against one shared headroom without reserving financial balance, so that hard limits remain safe with less lifecycle state.

#### Acceptance Criteria

5.1. Detailed admission shall use `SettledHeadroom = Balance - CreditFloor`.
5.2. Detailed admission shall compute `OpenExposure` as the sum of immutable maximum-exposure amounts for every still-open exposure row of the account.
5.3. Admission shall require `SettledHeadroom >= OpenExposure + MaxCustomerCharge`.
5.4. When the condition holds, the store shall insert exactly one open exposure row for account + BillingCallID containing the pessimistic amount, currency, quote snapshot references, creation time, and diagnostic headroom/exposure basis.
5.5. Insertion of the exposure row and the affordability comparison shall be atomic for one account and safe across multiple proxy processes.
5.6. Concurrent admission and customer settlement for the same account shall serialize through one account-scoped database transaction/lock discipline so neither observes an impossible intermediate state.
5.7. The admission transaction shall not update financial balance or financial journal state.
5.8. A same-key same-fingerprint exposure replay shall be idempotent; a same-key different-payload replay shall be an integrity error.
5.9. An exposure row shall remain open and retain its original pessimistic amount until customer settlement closes it; normal runtime execution shall not decrement, inflate, renew, or otherwise mutate the amount.
5.10. The architecture shall not require a low-balance `concurrency=1` heuristic for correctness.

### Requirement 6: Enforce Prepaid and Postpaid Limits Under Concurrency

**Objective:** As an operator, I want prepaid funds and postpaid credit limits protected for arbitrary concurrent sessions, so that multiple calls cannot collectively outrun the account.

#### Acceptance Criteria

6.1. For prepaid accounts, `CreditFloor` shall be zero and detailed admission shall not permit total open pessimistic exposure to exceed the non-negative settled balance.
6.2. For postpaid accounts, `CreditFloor` shall equal negative configured credit limit and detailed admission shall not permit total open pessimistic exposure to exceed `Balance - CreditFloor`.
6.3. Negative or zero settled balances/headroom shall be handled by the same formulas without special financial reservation states.
6.4. Property/concurrency tests shall prove that if every final customer charge is less than or equal to its admitted maximum, arbitrary admission/settlement interleavings cannot push the financial balance below its credit floor.
6.5. The invariant shall hold across multiple Go-LIP processes sharing the same Bun database.
6.6. Account top-up/payment/adjustment/credit-policy transactions shall use the same account-scoped serialization discipline as detailed admission and settlement.
6.7. An external balance backend that cannot provide equivalent atomic account-scoped exposure admission shall not be eligible for strict hard-credit mode.
6.8. The implementation may optimize exposure aggregation later, but the baseline design shall prefer correctness and simplicity over maintaining a second mutable exposure-total counter.

### Requirement 7: Perform No Billing or Exposure Mutation During LLM Execution

**Objective:** As a runtime maintainer, I want the live model execution path independent of financial lifecycle state, so that billing complexity cannot destabilize streams or failover.

#### Acceptance Criteria

7.1. After detailed admission succeeds, runtime stream handlers shall not read or write account balance, journal state, exposure amount, customer charge, or provider COGS.
7.2. No per-token, per-chunk, heartbeat, renewal, partial-settlement, or mid-stream monetary/exposure update shall be required.
7.3. Billing shall not influence backend retry/failover, output commitment, terminal CAS, cancellation propagation, or no-retry-after-output rules after admission.
7.4. A provider stream may expose usage metadata during decoding, but adapters/runtime may retain it only as evidence for the terminal B-leg record; it shall not drive financial mutation.
7.5. Failure of the post-usage worker, financial journal, reports, or provider-cost reconciliation shall not terminate or alter an already-admitted in-flight call.
7.6. Authoritative billing database unavailability may deny new calls at either admission screen, but shall not create a new mid-call billing failure path for already-admitted calls.
7.7. Runtime shall not contain unused-hold release logic, hold renewal logic, or a financial distinction between `execution_not_started` and `settled` release reasons.

### Requirement 8: Persist Immutable Per-B-leg Usage and One Per-call Closure Record

**Objective:** As a billing processor, I want final call evidence durably produced at terminal boundaries, so that post-usage billing does not reconstruct economics from stream chunks.

#### Acceptance Criteria

8.1. Every B-leg allocated for a BillingCallID shall produce one immutable terminal leg usage record with BillingCallID, A-leg/B-leg lineage, backend/provider/model, outcome, surfaced state, timestamps, and final provider billing evidence or explicit evidence-unavailable status.
8.2. Provider adapters may finalize cumulative usage/cost evidence at B-leg termination and shall not require the billing processor to interpret arbitrary stream samples.
8.3. Every BillingCallID shall produce one immutable call-closure record containing account/call/A-leg/session correlation, call outcome, bound customer pricing/policy references, and the expected terminal B-leg identities for that call.
8.4. The call closure may be persisted before or after individual leg records; the processor shall treat the call as complete only when every expected leg record is durably present.
8.5. Leg and call-closure persistence shall use stable keys plus canonical semantic fingerprints so identical replay is a no-op and conflicting replay is an integrity error.
8.6. Usage payloads shall not contain full prompts/completions, secrets, authorization headers, or provider SDK objects.
8.7. Terminal usage persistence shall be independent of customer balance mutation and shall not invoke rating or journal posting.
8.8. Failure to durably append terminal usage after client-visible output shall not alter the already-selected response; it shall trigger bounded detached retry/critical diagnostics.
8.9. Authoritative deployments shall require a durable usage spool implementation; an in-memory-only spool shall not satisfy the crash-survival/at-least-once billing guarantee.
8.10. The design shall document that simultaneous loss/unavailability of all configured durable replicas before any terminal record can be persisted is outside the guarantee rather than pretending exactly-once delivery can be synthesized from RAM.
8.11. The call-closure record shall be sealed only after the existing request terminal owner guarantees no additional B-leg can be allocated for that BillingCallID, and its expected B-leg set shall come from actually allocated legs for that call.

### Requirement 9: Bill the Customer At Least Once Logically and At Most Once Financially

**Objective:** As an operator, I want every completed call accounted for, including free calls, without duplicate charges.

#### Acceptance Criteria

9.1. The post-usage processor shall claim complete call records independently of runtime state.
9.2. Customer rating shall use the immutable pricing/policy references stored on the call record and reject snapshot mismatch.
9.3. Customer charging policy shall be evaluated from the complete set of B-leg records for the BillingCallID, preserving surfaced/failover/parallel semantics.
9.4. The processor shall persist one durable customer-billing operation for every complete call, even when customer charge is exactly zero.
9.5. Non-zero customer charge shall post a balanced double-entry financial transaction and update materialized settled balance in the same account transaction.
9.6. Zero customer charge shall not require artificial zero-value ledger entries, but the durable billing-operation record shall prove the call was processed.
9.7. Customer settlement identity shall be unique by account + BillingCallID, with identical replay returning the existing result and conflicting replay failing closed.
9.8. The customer settlement transaction shall close the corresponding open operational exposure atomically with the billing-operation record and any financial balance/journal mutation.
9.9. Closing an exposure shall not post an authorization/release journal transaction.
9.10. If actual customer charge exceeds the admitted pessimistic maximum or settlement would cross the credit floor, the account/call shall enter a reconciliation-required safety state rather than silently overrun the hard-credit invariant.

### Requirement 10: Account for Provider Cost Independently Per B-leg

**Objective:** As an operator, I want provider COGS accurate without making customer billing wait on internal cost reconciliation.

#### Acceptance Criteria

10.1. Every provider-accepted B-leg shall have an independent provider-cost processing identity based on BillingCallID + B-leg identity.
10.2. Authoritative provider-reported monetary cost shall be used when present and valid.
10.3. When authoritative monetary cost is absent but sufficient final quantities and an immutable operator-rate snapshot exist, provider cost shall be deterministically rated from those values.
10.4. A provider-rejected/never-started leg with explicit non-billable evidence may be recorded as reconciled zero cost.
10.5. A provider-accepted leg whose cost cannot be determined shall enter `unreconciled_cost` or equivalent retry/diagnostic state and shall never be silently posted as zero.
10.6. Provider-cost failure or delay shall not block an otherwise valid customer settlement or exposure closure.
10.7. Each non-zero provider cost shall post `inference_provider_cogs` (or equivalent debit) and provider payable/clearing credit as a balanced independent financial transaction.
10.8. Zero provider cost shall still receive a durable provider-cost operation marker without artificial zero journal entries.
10.9. Provider-cost posting shall be idempotent and order-independent relative to customer settlement and other B-leg cost postings.

### Requirement 11: Preserve Classical Double-entry Accounting for Actual Financial Operations

**Objective:** As a financial operator, I want every actual monetary mutation represented by balanced immutable postings, so that settled account state remains auditable and rebuildable.

#### Acceptance Criteria

11.1. Every non-zero customer usage charge, funding/top-up, payment, trusted adjustment, provider COGS, and monetary reversal shall be represented by a journal transaction with at least one debit and one credit.
11.2. For each transaction/book/currency, the exact sum of debit amounts shall equal the exact sum of credit amounts.
11.3. Posted journal transactions and entries shall be immutable.
11.4. Corrections shall use explicit reversal and, where applicable, replacement transactions linked to the corrected transaction.
11.5. Monetary arithmetic shall use exact integer/fixed-point representation and checked overflow handling.
11.6. Materialized account balance shall be reproducible from opening balance/account policy plus customer-account financial journal postings.
11.7. Trial-balance/reconciliation tooling shall verify journal equality independently of usage/exposure state.
11.8. Credit-limit policy changes shall remain durable policy/audit events rather than fake monetary postings.

### Requirement 12: Keep Financial Balance Rebuild and Exposure Recovery Simple

**Objective:** As an operator, I want failures diagnosable and recoverable from durable data, so that no hidden runtime state is needed to repair accounts.

#### Acceptance Criteria

12.1. A maintenance reconciliation shall rebuild settled financial balance from the financial journal and compare it to the materialized account row.
12.2. The same reconciliation shall independently compute open exposure as the sum of open exposure rows for the account.
12.3. Exposure rows shall preserve diagnostic admission snapshots including settled balance/headroom and open-exposure basis observed at admission.
12.4. Financial operations shall preserve point-in-time balance-before/balance-after and account-policy/version evidence as required by the existing journal architecture.
12.5. Reconciliation mismatch, conflicting replay, impossible charge-vs-quote, or credit-floor violation shall transition the account to `reconcile_required` and block both admission stages.
12.6. A successful verified rebuild/reconciliation may return the account to ready state without rewriting immutable journal or usage evidence.
12.7. A stale open exposure shall not be automatically removed solely because a wall-clock TTL elapsed.
12.8. Stale-exposure recovery shall require positive durable evidence that the BillingCallID is no longer executable and has either a complete usage record ready for billing or an explicit operator-approved no-charge repair record.
12.9. Recovery actions that close exposure without customer charge shall be durable and idempotent by BillingCallID/recovery reason.

### Requirement 13: Remove the Existing Monetary Hold Lifecycle

**Objective:** As a maintainer, I want the new exposure model to replace rather than wrap the current hold model, so that long-term code complexity materially decreases.

#### Acceptance Criteria

13.1. Monetary call admission shall stop using the current `Authorization`/authorization-hold lifecycle as its correctness mechanism.
13.2. The target schema shall retire `authorization_holds` after migration/reconciliation proves no required open hold remains.
13.3. The target account schema shall retire `reserved_nano` (or stop using it for call billing and remove it after migration).
13.4. The target financial journal shall retire authorization-book hold/release postings from the normal call path.
13.5. `HoldReleaser`, `AuthorizationLookup`, `BillingAdmissionCleanup`, hold expiry/renewal, and execution-abort release branches shall be deleted when no longer required.
13.6. Post-usage customer settlement shall no longer load/validate a hold or calculate a hold remainder.
13.7. Current `BillingIdentity.AuthorizationID` and hold-bound rating lookup shall be removed or reduced to migration compatibility only; immutable call/usage snapshot refs become rating authority.
13.8. Architecture tests shall forbid reintroducing monetary hold/reserved-balance mutations into runtime admission.

### Requirement 14: Remove Runtime Billing Aggregation and Barrier State

**Objective:** As a runtime maintainer, I want terminal usage production to be simple append operations, so that B2BUA concurrency does not require a billing-specific in-memory mini-framework.

#### Acceptance Criteria

14.1. Runtime shall not require `billingTurnCollector.evidenceByALeg`-style financial evidence aggregation to construct the authoritative billing input.
14.2. Runtime shall not require a billing-specific parallel barrier map to decide when financial settlement may run.
14.3. Runtime shall append each terminal B-leg usage record independently and append one call-closure record from the existing request terminal owner.
14.4. The post-usage processor/store shall join call closure with expected leg identities; ordering of durable append delivery shall not affect correctness.
14.5. Billing-specific finalization dedupe shall be scoped to one B-leg terminal evidence extraction and shall not become customer/account state.
14.6. Runtime terminal usage handoff retry shall operate on immutable individual records rather than rebuilding a mutable TUR from remembered legs.
14.7. No usage-record persistence failure shall trigger provider retry/failover.
14.8. The final runtime billing surface shall be materially smaller than the current `BillingRuntime` hold/handoff/release/collector surface and shall contain only the two admission stages plus terminal usage sink/identity needed to emit records.

### Requirement 15: Reuse Bun Persistence Without Creating Another Framework

**Objective:** As a maintainer, I want a minimal durable implementation using existing database infrastructure, so that simplification is not replaced with infrastructure novelty.

#### Acceptance Criteria

15.1. Durable account, operational exposure, usage spool, billing-operation, and financial-journal storage shall reuse `internal/infra/db` and Bun SQLite/PostgreSQL support.
15.2. No second ORM, generic event bus, Kafka requirement, workflow engine, CQRS framework, or generic event-sourcing framework shall be introduced.
15.3. Detailed exposure admission shall serialize by account using database transaction/locking semantics supported by both SQLite and PostgreSQL.
15.4. The baseline implementation shall compute open exposure from indexed open rows rather than maintaining a second mutable aggregate counter unless measured evidence demonstrates a required optimization.
15.5. Exposure indexing shall support bounded account-scoped `SUM(open max_exposure)` and call-ID lookup.
15.6. Usage spool indexing shall support idempotent CallID/BLegID appends, complete-call joins, bounded pending claims, and account/session diagnostics.
15.7. Database driver/Bun types shall not cross core billing ports.
15.8. Authoritative billing startup shall fail closed when required exposure/usage-spool/financial store capabilities are unavailable; stock non-billing host behavior shall remain unchanged.

### Requirement 16: Preserve Reporting, Diagnostics, and Separation of Economic Perspectives

**Objective:** As an operator, I want reports to explain both customer and provider economics without deriving money from runtime state.

#### Acceptance Criteria

16.1. Customer spend reports shall read posted customer billing operations/financial journal, not open exposure amounts.
16.2. Open-exposure diagnostics shall report call identity, age, pessimistic amount, quote refs, account/session/A-leg correlation, and safe admission basis without prompt/completion content.
16.3. Provider-cost reports shall read independent per-B-leg provider-cost operations and expose unreconciled cost separately.
16.4. Session/A-leg reports may aggregate many BillingCallIDs but shall not treat session/A-leg as the financial settlement key.
16.5. One-call diagnostics shall correlate cheap-screen outcome, detailed quote, exposure admission, leg records, call closure, customer billing operation, provider-cost operations, and journal transaction IDs.
16.6. Reporting/projection failure shall not alter customer billing idempotency or exposure closure.
16.7. Legacy token/metering observers may remain telemetry-only but shall not become an alternate balance-mutation path.

### Requirement 17: Make the Simplification Provable

**Objective:** As a maintainer, I want tests and architecture ratchets proving the smaller ownership model, so that complexity does not grow back.

#### Acceptance Criteria

17.1. Pure quote and customer/provider rating tests shall run without runtime, HTTP, provider SDKs, or database mocks.
17.2. Real-store contract tests shall cover SQLite and, where configured, PostgreSQL parity for account locking, concurrent exposure admission, idempotent usage append, settlement, and replay conflict.
17.3. Concurrency/property tests shall cover simultaneous admissions, customer settlements, top-ups/adjustments, and postpaid credit floors.
17.4. Failure tests shall cover account-store outage at cheap screen, outage at detailed admission, usage-spool append retry, processor crash/replay, provider-cost unreconciled state, and reconcile-required recovery.
17.5. B2BUA tests shall cover failover, parallel branches, rejected/never-started legs, surfaced winner semantics, and different per-leg provider/model rates.
17.6. Tests shall prove one long-lived A-leg/session can produce multiple distinct BillingCallIDs and customer billing operations.
17.7. Tests shall prove zero-charge calls and zero-cost B-legs create durable processed-operation evidence without duplicate journal entries.
17.8. Architecture tests shall forbid runtime stream handlers from importing/invoking financial journal posting, customer settlement, provider-cost settlement, or account balance mutation.
17.9. Architecture tests shall forbid an authorization-book/reserved-balance hold lifecycle from returning to the normal call path after deletion.
17.10. Migration shall include explicit symbol/file/schema deletion ratchets for the retired hold/release and runtime-collector paths.
17.11. The final defined billing-convergence production surfaces shall have a net reduction in production source lines versus the pre-spec main baseline; test/docs/migration lines shall not be used to mask growth.
17.12. The final documentation shall show one authoritative flow: `cheap credit screen -> route/quote -> atomic exposure admit -> execute -> durable terminal usage -> customer settlement + exposure close -> independent provider-cost posting`.
