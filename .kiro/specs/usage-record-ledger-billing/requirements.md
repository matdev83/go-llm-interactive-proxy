# Requirements Document

## Introduction

Go-LIP's billing architecture shall be based on a simple separation of concerns:

1. **Before upstream execution**, perform one synchronous affordability decision using a conservative maximum customer-charge estimate and create an atomic authorization hold.
2. **During execution**, do not rate usage, mutate financial balances, settle reservations, or reconstruct billing from stream events.
3. **At the terminal boundary**, persist one immutable **Turn Usage Record** for the A-leg. The record contains every billable B-leg and each B-leg's final provider/model usage and cost evidence.
4. **After execution**, process the Turn Usage Record deterministically, calculate customer charge and operator cost, post balanced journal transactions, release the authorization hold, and update read models.

The term **CDR** is deliberately not used in the target API. Although the processing pattern is analogous to telecom call-detail-record processing, Go-LIP will use the domain-neutral terms **Turn Usage Record** and **Leg Usage Record**.

The financial subsystem shall use classical double-entry bookkeeping. One durable journal contains transactions composed of two or more debit/credit entries; every transaction must balance exactly in one currency and book. Posted financial entries are immutable. Corrections are represented by reversal and replacement transactions rather than destructive edits.

For strict spend enforcement, every billing account has an explicit mode:

- **prepaid** — funded balance may be consumed down to exactly zero but never below zero;
- **postpaid** — balance may become negative only down to the configured individual credit limit.

Concurrent requests are protected by atomic pessimistic authorization holds. The materialized account row is optimized state for admission, but journal data and durable account metadata must be sufficient to rebuild and verify monetary state after a failure.

## Boundary Context

- **In scope:** Turn/Leg Usage Records; B2BUA A-leg/B-leg billing attribution; final provider usage/cost evidence; conservative maximum customer-charge estimation; prepaid and postpaid account semantics; individual postpaid credit limits; atomic concurrent authorization holds; durable Bun-backed billing persistence for SQLite/PostgreSQL; double-entry financial and authorization journals; immutable journal entries; point-in-time pre/post balance snapshots; post-turn customer charging and per-B-leg operator-cost accounting; recovery/rebuild from journal history; reconciliation/trial-balance checks; reporting; migration and deletion of stream-time billing machinery.
- **Out of scope:** payment-gateway/card integration, invoice rendering/delivery, tax/VAT calculation, debt collection workflows, FX conversion, bank reconciliation, token-by-token debiting, terminating an in-flight generation on a live monetary threshold, arbitrary billing DSLs, provider-specific billing code in core, or changing routing/no-retry-after-output semantics.
- **Trusted external monetary inputs:** top-ups, customer payments, refunds, credits, and administrative adjustments may be posted through narrow trusted billing application commands, but acquiring/settling those funds with external payment systems is outside this specification.
- **Supersession:** this specification supersedes the implementation direction in `.kiro/specs/usage-accounting-architecture-convergence/` and the initial `.kiro/specs/cdr-billing-and-prepaid-admission/` draft.
- **Primary ownership:** runtime owns execution; backend adapters own provider evidence extraction; `internal/core/billing` owns pricing/charging policy and use-case orchestration; `internal/infra/billingstore` owns Bun persistence and transaction mechanics; reporting is read-side projection.
- **Only live billing seam:** after routing has produced a side-effect-free candidate plan and before any provider/connector work, runtime may invoke affordability/hold authorization. Runtime's only post-execution billing responsibility is to durably hand off the sealed Turn Usage Record at the existing terminal boundary.
- **Revalidation triggers:** route-plan semantics, B2BUA lineage, provider final billing evidence, pricing/rating contracts, billing-account schema, journal schema, authorization-hold semantics, terminal ownership, or any reintroduction of stream-time financial mutation.

## Requirements

### Requirement 1: Keep Execution Billing-Blind

**Objective:** As a maintainer, I want execution and financial accounting separated, so that billing failures cannot corrupt streaming, retry, or B2BUA lifecycle behavior.

#### Acceptance Criteria

1.1. Runtime shall perform no customer charging, provider-cost posting, journal posting, financial balance mutation, hold settlement, or economic reconciliation while provider output is streaming.

1.2. Before the first upstream network request or connector-process action for one A-leg/turn, runtime may invoke exactly one synchronous affordability/authorization decision for that turn.

1.3. After the A-leg/turn reaches its existing terminal state, runtime shall durably submit exactly one sealed Turn Usage Record using an idempotent A-leg/turn identity.

1.4. Runtime receive/stream handlers shall not maintain running customer/operator money totals for financial settlement.

1.5. Retry/failover, output commitment, cancellation, terminal CAS, session continuity, and B-leg lineage shall remain execution concerns independent of post-turn billing processing.

1.6. A post-turn billing failure shall not change the already-selected winning attempt, surfaced output, or retry eligibility.

1.7. Client-visible usage objects/events may remain for wire compatibility, but financial billing shall not consume frontend-facing usage events as its source of truth.

1.8. Architecture tests shall fail if stream handlers directly depend on journal posting, balance mutation, or post-turn rating services.

### Requirement 2: Use Domain-Neutral Turn and Leg Usage Records

**Objective:** As a billing maintainer, I want one immutable business record for each completed A-leg and every B-leg it created, so that billing is deterministic and B2BUA-aware without telecom-specific naming.

#### Acceptance Criteria

2.1. The system shall define one versioned `TurnUsageRecord` identified by stable A-leg/turn/request identity.

2.2. `TurnUsageRecord` shall contain `ALegID`, billing account/principal identity, session/request correlation, authorization-hold identity, start/end timestamps, terminal outcome, and an ordered list of `LegUsageRecord` values.

2.3. Each `LegUsageRecord` shall contain `BLegID`, A-leg identity, sequence/attempt identity, backend/provider identity, canonical/effective model identity, timestamps, outcome, surfaced/winner status where applicable, and final billing evidence.

2.4. A single A-leg may contain zero, one, or multiple B-legs, including sequential failover and parallel attempts.

2.5. A user session may contain multiple A-legs; each billable A-leg shall produce its own Turn Usage Record and financial settlement boundary.

2.6. Final billing evidence shall preserve explicit presence for token/resource quantities and provider monetary cost so absent values remain distinct from authoritative zero.

2.7. Each B-leg shall bind the provider/model/rate snapshot identity necessary to reproduce operator-cost rating when provider cost is not authoritative.

2.8. The Turn Usage Record shall bind the customer pricing/charging-policy version used for the admission bound and final charge.

2.9. Usage records shall contain no prompts/completions, credentials, authorization headers, or provider SDK objects.

2.10. Once sealed, a usage record shall be immutable; identical replay is idempotent and conflicting replay is an integrity error.

### Requirement 3: Finalize Billing Evidence at the B-Leg Adapter Boundary

**Objective:** As a backend maintainer, I want one final evidence result per B-leg, so that core billing never reconstructs financial truth from arbitrary stream chunks.

#### Acceptance Criteria

3.1. Backend adapters may observe usage/cost metadata during provider decoding but shall retain/normalize it privately for billing.

3.2. At B-leg termination the adapter boundary shall expose one final attempt/leg billing evidence result.

3.3. Repeated cumulative usage from a provider shall resolve to the final authoritative cumulative value inside the adapter/finalizer rather than becoming multiple billable records.

3.4. Existing connector `AccountingEvidence` and `FinalizeBilling` capabilities shall be reused where practical instead of creating a parallel connector billing protocol.

3.5. If authoritative provider monetary cost is absent, final normalized usage/resource quantities shall be sufficient for post-turn operator-cost rating when a price snapshot exists.

3.6. Authoritative provider zero cost shall remain present zero and shall not trigger fallback rating.

3.7. Adapter evidence collection/finalization shall not change canonical content-event ordering, cancellation propagation, or no-retry-after-output semantics.

3.8. Generic runtime structural-value dedupe shall not be the billing identity mechanism for final B-leg evidence.

### Requirement 4: Compute a Conservative Maximum Customer Charge Before Upstream Work

**Objective:** As a credit operator, I want every admitted A-leg to have a finite pessimistic customer-charge bound, so that the account cannot exceed prepaid funds or a postpaid credit line.

#### Acceptance Criteria

4.1. After routing produces a side-effect-free eligible route plan and before upstream work, billing shall calculate `MaxCustomerCharge`.

4.2. The bound shall use the same versioned customer pricing and charging policy that will rate the completed Turn Usage Record.

4.3. Input exposure shall use deterministic preflight token/resource estimates and pessimistic non-discounted pricing when cache discounts or similar reductions are uncertain.

4.4. Output exposure shall use the lower of a valid explicit client maximum and the candidate model/provider maximum when the client maximum is lower; otherwise use the candidate maximum.

4.5. Every chargeable fixed/resource dimension that can increase customer charge shall be included in the bound.

4.6. If customer policy charges only the surfaced logical turn, internal provider retries that Go-LIP absorbs shall not unnecessarily multiply the customer authorization hold.

4.7. If customer policy may charge multiple B-legs, the bound shall cover all B-legs/parallel legs that can legitimately become customer-billable.

4.8. Different candidate models/rates in one route plan shall be bounded using their own rate information; the resulting hold shall cover the maximum customer-charge outcome allowed by the route/charging policy.

4.9. The estimator shall not perform provider network work, spawn connector processes, or mutate account/routing state.

4.10. If any chargeable route cannot be given a finite safe upper bound, strict prepaid/postpaid admission shall fail closed unless a conservative per-call monetary ceiling is explicitly configured.

4.11. All authoritative monetary calculations shall use checked integer/fixed-point arithmetic; overflow or currency mismatch shall fail deterministically.

4.12. The bound shall carry safe diagnostic basis: pricing/policy versions, input estimate, output ceiling, candidate/leg assumptions, and rate components.

### Requirement 5: Model Prepaid and Postpaid Accounts Explicitly

**Objective:** As an operator, I want clear prepaid/postpaid semantics, so that balance and credit-limit behavior is predictable and auditable.

#### Acceptance Criteria

5.1. Every billing account shall have exactly one mode: `prepaid` or `postpaid`.

5.2. Every billing account shall have one enforced currency; cross-currency spending within one account shall be rejected unless a future FX specification explicitly adds conversion.

5.3. The externally understandable signed customer balance shall be defined as customer-account credits minus debits.

5.4. A prepaid account funded with 250 currency units shall expose balance `+250`; charges shall decrease it toward `0`, and strict settlement/admission shall never allow it below `0`.

5.5. A postpaid account shall have a non-negative individual `CreditLimit`; an unfunded account starts at balance `0` and may decrease only to `-CreditLimit`.

5.6. A postpaid account with credit limit 100 and balance `-35` shall have 65 units of unreserved credit capacity before holds.

5.7. Account credit floor shall be `0` for prepaid and `-CreditLimit` for postpaid.

5.8. Spendable capacity shall be calculated as `Balance - CreditFloor - ActiveHolds`.

5.9. Prepaid top-up/funding shall increase signed balance; postpaid customer payment shall increase signed balance toward zero using the same customer-account credit posting direction.

5.10. Credit-limit changes shall be durable account-policy changes with audit history; they shall not be disguised as fake revenue/cash double-entry transactions.

5.11. A credit-limit decrease that would place current postpaid exposure below the new floor shall be rejected or require an explicit administrative override state that blocks new spending.

### Requirement 6: Prevent Concurrent Overspend With Atomic Authorization Holds

**Objective:** As an operator, I want all concurrent sessions to reserve pessimistic exposure atomically, so that aggregate concurrency cannot exceed available prepaid funds or postpaid credit.

#### Acceptance Criteria

6.1. Every concurrently admitted A-leg subject to hard monetary enforcement shall create its own pessimistic authorization hold.

6.2. Admission shall compare `MaxCustomerCharge` with spendable capacity after all already-active holds.

6.3. Hold creation and materialized reserved-state mutation shall be one durable atomic database transaction.

6.4. Simultaneous holds against the same account shall be serialized or compare-and-swapped so the sum of accepted holds never exceeds spendable capacity.

6.5. Correctness shall hold across multiple Go-LIP processes sharing the same durable database.

6.6. The architecture shall not rely on `low balance => concurrency 1` as its correctness mechanism.

6.7. Insufficient capacity shall deny the request before provider/connector work with stable payment/insufficient-credit classification.

6.8. Hold identity shall be deterministic/idempotent for account + A-leg/turn; replay shall not reserve twice.

6.9. A hold shall bind account, A-leg/turn, amount, currency, pricing/policy versions, and expiry/deadline.

6.10. If execution never begins, the hold shall be released idempotently with an explicit reason.

6.11. Strict admission shall fail closed if the durable store cannot guarantee atomic hold semantics.

### Requirement 7: Record Every Monetary Operation as Balanced Double-Entry Journal Data

**Objective:** As a financial operator, I want classical double-entry accounting, so that every monetary operation is traceable and the books are mathematically self-checking.

#### Acceptance Criteria

7.1. The billing subsystem shall have one durable journal model containing journal transactions and two or more journal entries per transaction.

7.2. Every journal transaction shall contain at least one debit entry and one credit entry.

7.3. For each journal transaction, sum of debits shall equal sum of credits exactly within one currency and one journal book.

7.4. Posted journal entries shall be immutable; corrections shall use explicit reversing and replacement transactions linked to the original.

7.5. A transaction replay with the same idempotency/source key and identical semantics shall be a no-op; conflicting semantics shall be an integrity error.

7.6. Customer charges shall post to the customer's financial ledger account on the debit side and to usage-revenue account(s) on the credit side.

7.7. For prepaid accounts, debiting the customer financial account shall reduce the customer prepaid-liability credit balance; for postpaid accounts, the same debit direction shall increase customer accounts receivable.

7.8. Trusted prepaid funding/postpaid payments shall credit the customer financial account and debit a cash/payment-clearing account.

7.9. Every provider-billable B-leg operator cost shall post debit to inference/provider COGS and credit to provider-payable/clearing accounts, with B-leg/model/provider correlation.

7.10. One customer-charge transaction may contain multiple revenue credit entries to preserve per-B-leg/model/rate breakdown while remaining exactly balanced.

7.11. Journal transactions shall have stable operation/source references to Turn Usage Record, A-leg, and B-leg where applicable.

7.12. Trial-balance validation shall prove equal aggregate debits and credits per book/currency over any journal range.

### Requirement 8: Journal Authorization Holds Without Recognizing Revenue

**Objective:** As an accountant and maintainer, I want authorization holds fully auditable without pretending they are completed financial charges.

#### Acceptance Criteria

8.1. Authorization holds and releases shall be recorded as balanced entries in an `authorization` journal book separate from posted financial revenue/cost entries.

8.2. Creating a hold shall increase a customer reserved-exposure account and credit a corresponding authorization contra account by the same amount.

8.3. Releasing or settling a hold shall reverse the authorization exposure by the amount being closed.

8.4. Authorization-book entries shall not change the customer's posted financial balance or recognize usage revenue.

8.5. Active held amount shall be reconstructible from net authorization-book entries for the customer's reserved-exposure account.

8.6. Materialized `Reserved` state used for fast admission shall equal the reconstructed open authorization exposure.

8.7. Financial and authorization books shall each balance independently.

8.8. Settlement may atomically write multiple journal transactions grouped by one operation ID, but each transaction/book must balance independently.

### Requirement 9: Persist Billing With the Existing Bun Database Abstraction

**Objective:** As a maintainer, I want billing durability on the repository's existing database infrastructure, so that financial correctness does not introduce another persistence stack.

#### Acceptance Criteria

9.1. Durable billing storage shall use `internal/infra/db` and Bun rather than introducing another database abstraction or ORM.

9.2. Billing storage shall support the repository's Bun-supported SQLite and PostgreSQL dialects where strict billing durability is configured.

9.3. Bun/SQL handles, transaction objects, query builders, and driver errors shall not cross the billing store port into core policy.

9.4. Schema changes shall use the repository's existing Bun migration conventions and deterministic migration IDs.

9.5. Required durable tables shall include billing account master/state, account-policy audit events, authorization holds, Turn Usage Records and B-leg rows, journal transactions, journal entries, and processing/idempotency state.

9.6. Journal transaction and entry rows shall be append-only after posting except for explicitly non-financial processing metadata allowed by the design.

9.7. Database constraints shall enforce positive amounts, valid debit/credit directions, stable uniqueness/idempotency keys, and valid foreign-key/correlation relationships where supported.

9.8. The store shall use transaction isolation/row locking/version CAS sufficient to protect same-account reserve and settlement races on SQLite and PostgreSQL.

9.9. Strict billing mode shall not silently fall back to memory if durable database opening, migration, or readiness fails.

9.10. Database errors exposed to operators shall remain secret-safe and shall not leak DSNs or credentials.

### Requirement 10: Capture Point-in-Time Account State Around User-Affecting Operations

**Objective:** As an operator, I want before/after credit snapshots on every user-affecting operation, so that debugging does not require mentally replaying the entire ledger.

#### Acceptance Criteria

10.1. Every user-affecting journal operation shall record signed customer balance before and after the operation.

10.2. The same operation shall record reserved amount before/after and spendable/available capacity before/after.

10.3. Snapshot data shall include account mode, currency, applicable credit limit/floor, and account-state version needed to interpret the values.

10.4. Reserve operations shall normally leave posted balance unchanged while changing reserved/available snapshots.

10.5. Usage-charge operations shall reduce signed balance by actual customer charge and close/release the associated hold in the same overall settlement transaction boundary.

10.6. Funding/payment operations shall increase signed balance and record corresponding pre/post snapshots.

10.7. Snapshot values shall be produced inside the same database transaction as the journal/account mutation, not by an eventually consistent follow-up query.

10.8. Snapshots are diagnostic evidence and shall be validated against the reconstructible journal state; they shall not become an independent source of financial truth.

### Requirement 11: Rate and Account for B2BUA A-Legs and All Billable B-Legs

**Objective:** As an operator, I want accounting to match Go-LIP's B2BUA topology, so that every provider leg and model cost can be traced without conflating customer charge and operator cost.

#### Acceptance Criteria

11.1. One Turn Usage Record shall represent one billable A-leg/turn and shall contain all B-legs created for that A-leg.

11.2. Operator cost shall be calculated independently for every provider-billable B-leg using that B-leg's provider/model evidence and bound rate snapshot.

11.3. Sequential failed/swallowed B-legs may carry real provider cost and shall be included in operator COGS even when customer policy charges only the winning/surfaced leg.

11.4. Parallel B-legs that incurred provider cost shall each be represented and costed independently.

11.5. Different B-legs in one A-leg may use different models/providers/rates without requiring runtime financial instrumentation.

11.6. Customer charging policy shall explicitly determine which logical turn/leg components are customer-billable.

11.7. Customer revenue journal entries shall preserve A-leg correlation and B-leg correlation for each leg-attributable charge component.

11.8. Provider COGS/payable journal entries shall preserve B-leg, backend/provider, model, and rate/evidence-source correlation.

11.9. A session-level report shall aggregate multiple A-leg settlements as a read model; user session is not the atomic journal transaction boundary.

11.10. Gross-margin reporting shall be derivable by comparing customer revenue and operator cost for the same A-leg and its B-legs without re-reading live runtime state.

### Requirement 12: Process Sealed Usage Records Deterministically After Execution

**Objective:** As a billing maintainer, I want post-turn rating to be deterministic and isolated from runtime, so that accounting can be tested with plain values.

#### Acceptance Criteria

12.1. The billing processor shall accept a sealed Turn Usage Record plus immutable pricing/charging-policy snapshots and produce a deterministic `BillingResult`.

12.2. Core calculation tests shall run with plain Go values without runtime, HTTP servers, backend processes, or goroutines.

12.3. Customer charge and operator cost shall be separate outputs.

12.4. Authoritative provider monetary cost shall remain operator evidence and shall not automatically become customer charge unless policy explicitly passes it through.

12.5. Missing provider monetary cost may be rated from final B-leg quantities and that leg's bound rate snapshot.

12.6. Explicit authoritative zero monetary cost shall not trigger fallback rating.

12.7. The processor shall use checked arithmetic and deterministic currency validation.

12.8. Billing Result shall contain charge/cost components, A-leg/B-leg attribution, price/policy versions, and explanation data sufficient for replay/debugging.

12.9. `ActualCustomerCharge > AuthorizedMaxCustomerCharge` shall be an invariant failure, not normal postpaid overage.

12.10. A failed/cancelled turn whose customer policy yields zero charge shall still preserve any operator B-leg costs and release the full customer hold.

### Requirement 13: Settle Charge, Hold, Journal, and Materialized State Atomically

**Objective:** As an operator, I want one durable settlement boundary, so that crashes cannot produce a charged account without journal evidence or journal evidence without matching account state.

#### Acceptance Criteria

13.1. Applying one Billing Result shall execute inside one Bun database transaction for the affected customer account.

13.2. Settlement shall lock/version-check the customer materialized account state before applying financial mutation.

13.3. Settlement shall post the balanced customer financial charge transaction, close/reverse the authorization hold, update materialized balance/reserved state, record pre/post snapshots, and mark customer settlement applied atomically.

13.4. Provider-cost journal transactions for the Turn Usage Record shall be idempotent by B-leg/source identity and shall be persisted durably as part of processing.

13.5. Settlement replay for an already-applied Turn Usage Record shall not debit customer balance or post provider costs twice.

13.6. If any journal transaction fails balance validation, the whole customer settlement transaction shall roll back.

13.7. Prepaid settlement shall enforce resulting balance `>= 0`.

13.8. Postpaid settlement shall enforce resulting balance `>= -CreditLimit`.

13.9. Resulting spendable capacity after settlement shall be recomputed from balance, floor, and remaining holds inside the transaction.

13.10. If processing fails before atomic settlement commits, the hold shall remain active and no partial customer financial posting shall be visible.

### Requirement 14: Rebuild and Verify Account State From Durable Journal Data

**Objective:** As an operator, I want the ledger to be the recoverable monetary truth, so that a corrupted/materialized account row can be recreated after failure.

#### Acceptance Criteria

14.1. Signed customer balance shall be reconstructible from ordered posted journal entries on the customer's financial ledger account.

14.2. Active reserved amount shall be reconstructible from ordered authorization-book entries or equivalent immutable hold-journal state.

14.3. Spendable capacity shall be reproducible from reconstructed balance, durable account mode/credit limit, and reconstructed holds.

14.4. The system shall provide an offline/admin reconciliation operation that replays one account's journal and compares calculated state with materialized account state.

14.5. A repair/rebuild operation shall be able to replace materialized balance/reserved/version state from verified journal results under an exclusive maintenance/locking boundary.

14.6. Rebuild shall never rewrite or delete posted journal entries.

14.7. Before/after snapshots shall be checked during replay and inconsistencies shall identify the first offending transaction.

14.8. Trial-balance failure, conflicting replay, missing transaction linkage, or impossible snapshot transition shall fail reconciliation loudly.

14.9. Reconciliation shall be deterministic across SQLite and PostgreSQL for identical journal data.

14.10. Materialized account state shall be treated as an optimization/cache for admission and queries, not an unrecoverable independent source of monetary truth.

### Requirement 15: Persist and Recover Usage-Record Processing Simply

**Objective:** As an operator, I want post-turn billing to survive crashes without requiring a generic event framework.

#### Acceptance Criteria

15.1. A sealed Turn Usage Record shall be persisted durably before terminal billing handoff is considered complete.

15.2. Processing state shall distinguish pending, processing/retryable, processed, and terminal/invariant-error outcomes with a bounded state model.

15.3. A simple in-process worker/poller or existing terminal-work mechanism may process pending records; Kafka/CQRS/workflow frameworks are not required.

15.4. Claim/retry shall be idempotent and safe after crashes at any point before or after settlement.

15.5. A failed processor shall leave the authorization hold intact unless the system can prove execution never happened and safe release conditions are satisfied.

15.6. Stale hold cleanup shall require the A-leg not to be active plus maximum execution lifetime and safety grace.

15.7. Operators shall have bounded paginated queries for stuck usage records, holds, journal transactions, and reconciliation failures.

15.8. No unbounded historical billing state shall be retained only in process memory.

### Requirement 16: Make Reports and Audits Read the Journal and Processed Usage Records

**Objective:** As an operator, I want reporting to agree exactly with charging and the double-entry ledger.

#### Acceptance Criteria

16.1. Authoritative customer balance/spend reports shall derive from journal/account state rather than raw stream usage or raw metering facts.

16.2. Operator-cost reports shall derive from B-leg cost journal entries and processed usage-record evidence.

16.3. Customer revenue and provider cost shall remain separate ledger/account classes and reporting perspectives.

16.4. Per-turn explanation shall link Turn Usage Record, B-leg records, authorization hold, Billing Result, financial journal transaction(s), authorization transaction(s), and pre/post account snapshots.

16.5. Reports shall expose prepaid/postpaid mode, current signed balance, credit limit, reserved amount, and spendable capacity without exposing secrets.

16.6. A trial-balance/reconciliation report shall be available per currency/book with debits, credits, and imbalance.

16.7. Legacy token/metering views that still have consumers shall become one-way projections and shall not feed financial decisions.

16.8. `pkg/lipsdk/usage.Observer` and metering infrastructure may remain telemetry surfaces but shall not be authoritative for customer balances or journal posting.

### Requirement 17: Delete Stream-Time Financial Machinery and Keep Boundaries Small

**Objective:** As a maintainer, I want the new architecture to replace old billing paths, so that double-entry rigor does not become another layer on top of existing complexity.

#### Acceptance Criteria

17.1. Financial settlement shall stop using stream-event reconciliation (`tokenaccounting/streamusage.Reconstruct` or equivalent) as its source.

17.2. Per-usage-event runtime price mutation such as `enrichUsageCost` shall leave the financial path.

17.3. Runtime economic dedupe maps, remembered customer/operator usage totals, and billing-finalized flags shall be removed when Turn/Leg Usage Record identity is authoritative.

17.4. Monetary settlement shall no longer accept raw usage-event arrays, raw metering facts, exposure bases, or streaming lifecycle classifications as normal financial input.

17.5. Direct runtime financial/token-ledger writes shall be retired.

17.6. `metering.Fact` may survive for telemetry/audit but shall not be required to reconstruct customer financial balance.

17.7. `pkg/lipapi` shall remain protocol-neutral and shall not gain customer balance, credit limit, journal, or pricing concepts.

17.8. Provider SDKs/wire types shall remain at adapter edges and shall not enter `internal/core/billing`.

17.9. No DI container, generic service locator, dynamic Go plugin, reflection registry, generic economic event bus, or user billing DSL shall be introduced.

17.10. Implementation shall prefer deleting obsolete paths/packages/fields over permanent dual-write compatibility architecture.

17.11. Architecture tests shall enforce the final flow: `route plan -> max-charge authorize -> execute -> Turn Usage Record -> deterministic rating -> double-entry settlement -> journal/report`.

17.12. Implementation shall follow RED -> GREEN -> REFACTOR and retain characterization tests until each old path is deleted.
