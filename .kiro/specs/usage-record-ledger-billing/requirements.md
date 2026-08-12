# Requirements Document

## Introduction

Go-LIP billing shall use a simple two-stage model: **authorize before execution, account after execution**. Runtime must not perform financial accounting while LLM output is streaming. Before upstream work, the system computes a pessimistic maximum customer charge and atomically places an authorization hold. At the existing terminal boundary it persists one immutable **Turn Usage Record (TUR)** for the A-leg containing immutable **Leg Usage Records (LURs)** for all B-legs. Post-turn processing validates the bound pricing/rate snapshots, calculates customer revenue and per-B-leg operator cost, posts balanced double-entry journal transactions, closes the hold, and updates rebuildable materialized account state.

The durable financial journal is the reconstructible monetary truth. Materialized balances and point-in-time snapshots exist for admission speed and diagnostics, but they must be verifiable from journal history. Strict monetary enforcement supports prepaid accounts, whose balance may reach but not cross zero, and postpaid accounts, whose balance may reach but not cross an individual negative credit floor.

## Boundary Context

- **In scope:** pessimistic pre-call authorization; prepaid/postpaid accounts; concurrent holds; immutable TUR/LUR evidence; B2BUA A-leg/B-leg attribution; deterministic rating; double-entry financial and authorization books; Bun-backed SQLite/PostgreSQL persistence; point-in-time account snapshots; idempotency; reconciliation/rebuild; reporting; migration away from stream-time accounting.
- **Out of scope:** payment-gateway/card integration, invoicing presentation, VAT/tax, FX, collections, live token-by-token debiting, in-flight monetary termination, generic ERP/chart-of-accounts framework, Kafka/CQRS/event sourcing, arbitrary billing DSLs, or routing/retry behavior changes.
- **Ownership:** runtime executes; backend adapters finalize provider evidence; `internal/core/billing` owns billing policy/use cases; `internal/infra/billingstore` owns Bun transaction mechanics; reports are read-side projections.
- **Only live financial seam:** after side-effect-free route planning and before provider/connector work, runtime may invoke authorization. During streaming there is no journal posting, balance mutation, rating, settlement, economic dedupe, or financial accumulator.
- **Terminology:** target APIs use Turn Usage Record / Leg Usage Record rather than telecom-specific CDR terminology.

## Requirements

### Requirement 1: Keep Execution Billing-Blind
**Objective:** As a maintainer, I want financial accounting isolated from LLM execution so billing failures cannot alter streaming or retry semantics.

#### Acceptance Criteria
1.1. Runtime shall perform no customer charging, provider-cost posting, journal posting, financial balance mutation, hold settlement, or financial reconciliation while provider output is streaming.
1.2. Before the first upstream network/process action for one A-leg, runtime may invoke exactly one synchronous affordability/authorization decision.
1.3. After the existing terminal owner finalizes the A-leg, runtime shall durably submit exactly one sealed TUR using an idempotent TUR identity.
1.4. Stream receive handlers shall not maintain running financial customer/operator totals.
1.5. Retry/failover, output commitment, cancellation, terminal CAS, continuity, and B-leg lineage shall remain execution concerns independent of post-turn billing.
1.6. Post-turn billing failure shall not change the already selected winning attempt or surfaced output.
1.7. Client-visible usage events may remain for protocol compatibility but shall not be authoritative financial input.
1.8. Architecture tests shall reject runtime stream-handler dependencies on journal posting, settlement, or post-turn rating.

### Requirement 2: Define Immutable, Replay-Safe Turn and Leg Usage Records
**Objective:** As a billing maintainer, I want stable immutable usage evidence aligned with B2BUA lineage so billing is replayable without reconstructing stream events.

#### Acceptance Criteria
2.1. One versioned TUR shall represent one billable A-leg/logical turn and contain all B-legs created for that A-leg.
2.2. TUR durable identity shall include billing account identity plus stable A-leg/turn identity; request labels alone shall not be sufficient identity.
2.3. Every LUR durable identity shall include its parent TUR identity plus `BLegID`; attempt labels remain correlation only.
2.4. TUR shall include `ALegID`, account/principal identity, authorization identity, timestamps, terminal outcome, customer pricing/policy references, and ordered LURs.
2.5. LUR shall include `BLegID`, `ALegID`, backend/provider, effective model, attempt sequence, timestamps, outcome/surfaced state, final evidence, and operator-rate/evidence references.
2.6. Final evidence shall preserve absent versus authoritative-zero presence for quantities and provider monetary cost.
2.7. Sealed TUR/LUR payloads shall not contain prompts/completions, credentials, authorization headers, or provider SDK objects.
2.8. Once sealed, TUR/LUR payloads are immutable; worker claim/retry/result metadata shall live outside the sealed rows.
2.9. Every sealed TUR and LUR shall persist a versioned canonical semantic fingerprint over fixed immutable billing fields.
2.10. Same durable key plus same fingerprint shall be an idempotent replay; same key plus different fingerprint shall fail atomically as an integrity error.

### Requirement 3: Finalize Provider Evidence at the B-Leg Boundary
**Objective:** As a backend maintainer, I want one final billing-evidence result per B-leg so core billing never reconstructs financial truth from arbitrary chunks.

#### Acceptance Criteria
3.1. Adapters may observe usage/cost during decode but shall retain/normalize it privately for billing.
3.2. At B-leg termination the adapter/finalizer shall expose one final evidence result.
3.3. Repeated cumulative provider usage shall resolve to the final authoritative cumulative value before LUR sealing.
3.4. Existing connector `AccountingEvidence`/`FinalizeBilling` capabilities shall be reused where practical rather than creating a parallel connector billing protocol. When FinalizeBilling cannot express monetary cost, terminal TUR sealing may copy stream-observed `CostPresent` (including authoritative zero) onto the LUR; that merge is terminal evidence assembly, not stream-time financial settlement.
3.5. Missing provider cost may be represented as absent only when final normalized resource quantities and an exact operator-rate reference permit deterministic fallback rating.
3.6. Authoritative provider zero cost shall remain zero and shall not trigger fallback rating.
3.7. Evidence finalization shall not alter canonical content ordering, cancellation, or no-retry-after-output semantics.

### Requirement 4: Compute a Conservative Maximum Customer Charge
**Objective:** As a credit operator, I want a finite safe upper bound before upstream work so admitted calls cannot exceed account capacity.

#### Acceptance Criteria
4.1. After side-effect-free route planning and before upstream work, billing shall compute `MaxCustomerCharge`.
4.2. The bound shall use the same immutable customer pricing and charging-policy references later used for final rating.
4.3. Input exposure shall use deterministic preflight quantities and pessimistic non-discounted pricing where discounts are uncertain.
4.4. Output exposure shall use the lower valid client maximum when explicitly lower than the model/provider maximum; otherwise it shall use the model/provider maximum.
4.5. Fixed and non-token chargeable dimensions shall be included when they can increase customer charge.
4.6. If customer policy charges only the surfaced logical turn, internal failover cost absorbed by Go-LIP shall not unnecessarily multiply the customer hold.
4.7. If policy can charge multiple/parallel B-legs, the bound shall include all potentially customer-chargeable legs.
4.8. Different route candidates/models/rates shall be bounded using their own immutable rate data.
4.9. The estimator shall perform no provider network/process work and no account mutation.
4.10. Unknown/unbounded chargeable exposure, arithmetic overflow, currency mismatch, or missing required rate shall fail closed in strict mode unless an explicit conservative per-call ceiling covers it.

### Requirement 5: Model Prepaid and Postpaid Accounts Explicitly
**Objective:** As an operator, I want one signed-balance convention with clear floors so prepaid and postpaid credit behavior is auditable.

#### Acceptance Criteria
5.1. Every enforced account shall have one mode (`prepaid` or `postpaid`) and one authoritative currency.
5.2. Signed `Balance` shall equal customer financial-account credits minus debits.
5.3. Prepaid `CreditFloor` shall be zero; strict authorization/settlement shall never produce `Balance < 0`.
5.4. Postpaid `CreditFloor` shall equal `-CreditLimit`; strict authorization/settlement shall never produce `Balance < -CreditLimit`.
5.5. `Spendable = Balance - CreditFloor - Reserved`.
5.6. Prepaid funding/top-up and postpaid payment shall credit the customer financial account using trusted monetary commands and balanced journal transactions.
5.7. Credit-limit changes shall be durable audited account-policy mutations, not fake money postings.
5.8. Unsafe credit-limit reduction shall be rejected or put the account into an explicit blocked administrative state.
5.9. Floating point shall not be used for authoritative money; exact fixed-point/integer nano-units with checked arithmetic shall be used.

### Requirement 6: Prevent Concurrent Overspend With Atomic Authorization Holds
**Objective:** As an operator, I want concurrent sessions/processes to share one balance safely without active-session scanning.

#### Acceptance Criteria
6.1. Every hard-enforced admitted A-leg shall own one pessimistic authorization hold until settlement or safe release.
6.2. Admission shall require `SpendableBefore >= MaxCustomerCharge`.
6.3. Successful hold creation shall prove `SpendableAfter = SpendableBefore - MaxCustomerCharge >= 0`.
6.4. Hold creation and materialized `Reserved` mutation shall be one durable atomic account transaction.
6.5. Concurrent holds on one account shall be serialized or protected by version CAS so accepted holds cannot collectively exceed spendable capacity.
6.6. Correctness shall hold across multiple Go-LIP processes sharing the same durable store.
6.7. The architecture shall not rely on a low-balance `concurrency=1` heuristic for correctness.
6.8. Insufficient capacity/store unavailability shall deny before upstream provider/connector work with a stable insufficient-credit/payment classification.
6.9. Hold identity shall be deterministic for account + TUR/A-leg; replay shall not reserve twice.

### Requirement 7: Use Classical Double-Entry Journal Transactions
**Objective:** As a financial operator, I want every monetary/exposure operation self-balancing and immutable so errors are detectable and history is reconstructible.

#### Acceptance Criteria
7.1. One durable journal model shall contain journal transactions and two or more postings per transaction.
7.2. Every transaction shall contain at least one debit and one credit and satisfy exact `sum(debits) == sum(credits)` within one book/currency.
7.3. Posted journal transactions/entries shall be immutable.
7.4. Corrections shall use reversal plus replacement transactions; destructive update/delete of posted money is prohibited.
7.5. Reversal shall carry `ReversalOf`; replacement shall carry `CorrectsTransactionID`; both shall share a `CorrectionGroupID` identifying the corrected transaction chain.
7.6. Correction references shall target an existing eligible transaction in the same account/book/currency, prohibit self-reference, and remain auditable across repeated corrections.
7.7. Every transaction shall persist a versioned semantic fingerprint; same source/idempotency key is a no-op only when the fingerprint matches, otherwise it is an integrity error.
7.8. Customer usage charge shall debit the customer financial account and credit usage revenue account(s).
7.9. Trusted funding/payment shall debit cash/payment-clearing and credit the customer financial account.
7.10. Every provider-billable B-leg cost shall debit inference/provider COGS and credit provider payable/clearing with B-leg/model/provider correlation.
7.11. Financial and authorization books shall balance independently.
7.12. Trial-balance queries shall prove aggregate debit/credit equality per book/currency/range.
7.13. Every account-correlated journal transaction shall receive a durable monotonically increasing `AccountSequence` allocated atomically under account locking/versioning; replay shall use this sequence, not wall-clock time.

### Requirement 8: Journal Authorization Holds Without Recognizing Revenue
**Objective:** As an accountant, I want pending exposure auditable without treating a hold as earned money.

#### Acceptance Criteria
8.1. Non-zero holds/releases shall post in an `authorization` book separate from posted financial revenue/cost. A zero-exposure authorization (`MaxCustomerCharge = 0`) is an audited no-op hold: it persists identity/snapshots but creates no journal entries because authoritative journal postings are strictly positive.
8.2. For a non-zero hold, creation shall debit customer reserved exposure and credit an authorization contra account by the same amount.
8.3. Hold close/release shall post the exact opposite authorization entries for the amount closed.
8.4. Authorization postings shall not alter posted financial `Balance` or recognize revenue.
8.5. Materialized `Reserved` shall equal reconstructed open authorization exposure.
8.6. Explicit hold release shall require deterministic idempotency/source identity plus a closed reason such as `execution_not_started`, `settled`, `zero_charge`, `stale_safe_release`, or `operator_release`.
8.7. Same release identity with a different amount/reason shall be an integrity error.

### Requirement 9: Persist Billing Through the Existing Bun Database Abstraction
**Objective:** As a maintainer, I want durable billing to reuse existing database infrastructure rather than create another persistence stack.

#### Acceptance Criteria
9.1. Durable billing adapters shall use `internal/infra/db` and Bun.
9.2. Strict durable billing shall support repository Bun SQLite/PostgreSQL dialects and contract semantics where configured.
9.3. Bun/SQL handles, transactions, query builders, and driver errors shall not cross core billing ports.
9.4. Schema changes shall use repository migration conventions and deterministic migration IDs.
9.5. Durable schema shall include account master/materialized state, account-policy audit, authorization holds, immutable TURs, immutable LURs, `usage_record_processing`, journal transactions, journal entries, and bounded idempotency/repair state.
9.6. Database constraints shall enforce positive posting amounts, valid debit/credit direction, durable-key uniqueness, `(account_id, account_sequence)` uniqueness, and required foreign-key/correlation integrity where supported.
9.7. Strict billing shall not silently fall back to memory on durable DB failure.
9.8. SQLite/PostgreSQL store-contract tests shall prove equivalent financial invariants.

### Requirement 10: Capture Point-in-Time Account State
**Objective:** As an operator, I want before/after state with every customer-affecting operation for fast debugging while preserving journal truth.

#### Acceptance Criteria
10.1. Each customer-affecting operation shall record `BalanceBefore/After`.
10.2. It shall record `ReservedBefore/After` and `SpendableBefore/After`.
10.3. It shall record mode, currency, credit limit/floor, and account-state version before/after.
10.4. Snapshots shall be written in the same database transaction as journal/account mutation.
10.5. Snapshot values are redundant diagnostic evidence, not an independent source of financial truth.
10.6. Replay/reconciliation shall validate snapshots and identify the first inconsistent `AccountSequence`.

### Requirement 11: Account Correctly for B2BUA A-Legs and All Billable B-Legs
**Objective:** As an operator, I want customer revenue and provider cost attributed at their true B2BUA granularity.

#### Acceptance Criteria
11.1. TUR/customer settlement boundary shall be one A-leg/logical turn.
11.2. TUR shall retain every sequential, failed, swallowed, winning, and parallel B-leg created for that A-leg when billing evidence exists.
11.3. Operator cost shall be calculated independently for each provider-billable B-leg using that leg's provider/model evidence and bound operator-rate reference.
11.4. Failed/losing B-legs may create real COGS even when customer policy charges only the surfaced result.
11.5. Different B-legs may use different models/providers/rates without runtime financial instrumentation.
11.6. Customer charging policy shall explicitly select which logical/leg components are customer-billable.
11.7. Customer revenue and provider-cost journal entries shall preserve A-leg/B-leg/model/provider correlation where applicable.
11.8. Session-wide totals shall be read-side aggregation of A-leg settlements, never a mutable session financial accumulator.

### Requirement 12: Rate Sealed Usage Records Deterministically
**Objective:** As a billing maintainer, I want post-turn rating to be pure, version-bound, and testable with plain values.

#### Acceptance Criteria
12.1. The calculator shall accept sealed TUR, bound authorization, immutable customer pricing/policy snapshot, and exact operator-rate snapshots needed by LURs.
12.2. Before rating, customer pricing/policy references shall exactly match both TUR and authorization references.
12.3. Every LUR requiring fallback operator rating shall resolve the exact operator-rate reference sealed in that LUR; numerically equal but differently identified snapshots are not substitutes.
12.4. Snapshot identity mismatch shall fail deterministically before rating/posting.
12.5. Customer charge and operator cost shall be separate outputs.
12.6. Authoritative provider cost shall remain operator evidence and shall not automatically become customer charge unless policy explicitly passes it through.
12.7. Explicit authoritative zero provider cost shall remain zero.
12.8. If a provider-billable LUR lacks authoritative cost and cannot be rated from sufficient final quantities plus its exact bound rate snapshot, processing shall enter explicit `unreconciled_cost`; it shall not omit/zero COGS or mark the TUR fully processed.
12.9. `ActualCustomerCharge > AuthorizedMaxCustomerCharge` shall be an invariant failure, not normal overage.
12.10. Core calculation shall be unit-testable without runtime, HTTP, DB, provider process, or goroutine.
12.11. Customer charge shall bill observed provider-accepted usage under the bound charge policy regardless of turn outcome (`completed`, `canceled`, `failed`, or `unknown`). Outcome must not grant a free ride when the downstream provider accepted work.
12.12. Input-token customer charges shall apply when input quantity evidence is present (provider accepted the prompt). Absent input evidence means rejection or never-started and shall not invent input charges.
12.13. Completion/output-token customer charges shall apply when output quantity evidence is present, including user cancel and connectivity loss after generation started. Interrupted turns shall skip missing optional dimensions instead of failing closed solely because the stream ended early.
12.14. For non-completed turns, customer rating shall include provider-accepted legs even when `Surfaced=no`, so connectivity cancels cannot avoid charges for tokens the provider already generated.

### Requirement 13: Settle Customer Charge Atomically
**Objective:** As an operator, I want charge, hold close, snapshots, and account state to commit together so crashes cannot create split financial truth.

#### Acceptance Criteria
13.1. Applying one Billing Result shall run in one Bun transaction scoped to the customer account.
13.2. Settlement shall lock/version-check materialized account state.
13.3. The transaction shall post balanced customer financial charge entries, close the authorization hold, update materialized balance/reserved/version, record snapshots, and mark customer settlement applied atomically.
13.4. Provider-cost postings shall use durable source identity including billing account + TUR + `BLegID` plus a closed cost-source discriminator only when one B-leg legitimately produces multiple independent costs.
13.5. Provider-cost source identity shall persist a semantic fingerprint; conflicting replay shall fail.
13.6. Settlement replay shall not charge customer or post provider cost twice.
13.7. Resulting prepaid balance shall be `>= 0`; resulting postpaid balance shall be `>= -CreditLimit`.
13.8. Failure before commit shall leave the hold intact and expose no partial customer mutation.

### Requirement 14: Rebuild and Reconcile Account State From Journal History
**Objective:** As an operator, I want durable journal history to recreate account state and fail closed when integrity cannot be proven.

#### Acceptance Criteria
14.1. Rebuilt signed balance shall derive from customer financial-account credits minus debits ordered by `AccountSequence`.
14.2. Rebuilt reserved exposure shall derive from authorization-book postings ordered by `AccountSequence` (or an equivalent immutable hold sequence sharing the same deterministic account order).
14.3. Rebuilt spendable shall equal rebuilt balance minus durable credit floor minus rebuilt reserved exposure.
14.4. Reconciliation shall validate every transaction balances, semantic fingerprint/idempotency consistency, correction linkage, sequence uniqueness/continuity rules, snapshots, and materialized account state.
14.5. Rebuild shall never rewrite/delete posted journal entries.
14.6. Maintenance-locked rebuild may replace only materialized balance/reserved/version/status from verified journal/policy history.
14.7. Identical journal history shall rebuild deterministically on SQLite and PostgreSQL.
14.8. Any journal invariant, replay-integrity, impossible-snapshot, correction-link, or reconstruction failure shall atomically transition the affected account to `reconcile_required`.
14.9. While `reconcile_required`, all new hard prepaid/postpaid authorizations shall fail closed before upstream work; read/reconciliation/repair operations remain available.
14.10. The account may leave `reconcile_required` only through an explicit audited reconciliation/rebuild that verifies journal balance/linkage/fingerprints, reconstructs state, validates/repairs the materialized row, and records the successful status transition.

### Requirement 15: Recover Post-Turn Processing Without Mutating Usage Evidence
**Objective:** As an operator, I want crash-safe post-turn billing without a generic workflow/event platform.

#### Acceptance Criteria
15.1. Sealed TUR/LUR evidence shall be durable before billing handoff is complete.
15.2. Mutable processing state shall live in `usage_record_processing` keyed by TUR durable key/fingerprint, not inside sealed TUR/LUR rows.
15.3. Processing state shall distinguish pending, processing, retryable, processed, `unreconciled_cost`, and terminal/invariant error with bounded metadata.
15.4. Claim/retry shall be idempotent across crashes before/after settlement.
15.5. Processing failure shall normally leave the hold intact unless safe non-execution/release is proven.
15.6. Stale hold cleanup shall require the A-leg to be known inactive plus maximum execution lifetime and safety grace. TTL-only reclaim of `expires_at` shall not release reserved exposure.
15.7. Operators shall have bounded paginated queries for stuck usage records, holds, journal transactions, and reconcile-required accounts.

### Requirement 16: Make Reporting Read Journal and Processed Usage Records
**Objective:** As an operator, I want dashboards/audits to agree with financial posting rather than reinterpret raw streams.

#### Acceptance Criteria
16.1. Customer balance/spend reports shall derive from journal/account projections, not raw stream events or raw metering facts.
16.2. Operator-cost reports shall derive from B-leg cost postings and processed LUR evidence.
16.3. Customer revenue and provider cost shall remain separate reporting perspectives.
16.4. Per-turn explanation shall link TUR/LURs, authorization, Billing Result, journal transactions, and point-in-time snapshots.
16.5. Trial-balance/reconciliation report shall expose debits, credits, imbalance, sequence/fingerprint/linkage failures, and materialized-state mismatch safely.
16.6. Legacy token/metering views may remain one-way telemetry/read projections but shall never feed financial balance/journal decisions.

### Requirement 17: Delete Stream-Time Financial Machinery and Keep Architecture Provable
**Objective:** As a maintainer, I want the new architecture to replace old financial paths rather than layer on top of them.

#### Acceptance Criteria
17.1. Financial settlement shall stop using stream-event reconciliation as its source.
17.2. Per-usage-event runtime price enrichment shall leave the financial path.
17.3. Runtime economic dedupe maps, customer/operator financial accumulators, and financial-finalized flags shall be removed when TUR/LUR identity is authoritative.
17.4. Monetary settlement shall not accept raw usage-event arrays, raw metering facts, exposure bases, or stream lifecycle classifications as normal financial input.
17.5. Direct runtime financial/token-ledger writes shall be retired. Leftover `accounting.ledger.*` YAML may be accepted for compatibility and shall not be opened or required to have live sqlite/postgres paths. Production `accounting.authority` YAML shall reject monetary `budget` / `spend_cap` / `money_nano` rules; token quota and rate-limit rules remain. Authoritative Bun billing is enabled only when composition injects store, admission, identity, and rating resolvers.
17.6. `metering.Fact` may remain for telemetry/audit but shall not be required to reconstruct customer financial balance.
17.7. `pkg/lipapi` shall remain free of customer balance, credit limit, journal, and pricing concepts.
17.8. Provider SDK/wire types shall remain at adapter edges and shall not enter core billing.
17.9. No DI container, service locator, dynamic Go plugin, reflection registry, generic financial event bus, Kafka/CQRS requirement, or billing DSL shall be introduced.
17.10. Architecture tests shall enforce the final flow: `route plan -> max-charge authorize -> execute -> TUR/LUR -> deterministic rating -> double-entry settlement -> journal/reports`.
17.11. Migration shall use RED -> GREEN -> REFACTOR characterization/shadow tests and delete obsolete paths after cutover.
