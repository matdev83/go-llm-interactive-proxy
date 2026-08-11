# Research Notes

## Objective

Identify the smallest architecture that gives Go-LIP financially rigorous prepaid/postpaid billing without putting accounting back into LLM stream execution.

The resulting model deliberately separates three things that were previously entangled:

1. **execution evidence** — what happened on each A-leg/B-leg;
2. **credit authorization** — whether the next A-leg may start;
3. **financial accounting** — what the completed usage means in customer revenue and provider cost.

The target is therefore not a telecom CDR subsystem copied literally. Telecom CDR processing is a useful analogy for post-completion rating, but Go-LIP terminology and identities should reflect its B2BUA/LLM domain.

## Current Repository Assets

### B2BUA lineage already expresses the correct economic topology

`pkg/lipapi` lineage already distinguishes `ALegID`, `BLegID`, attempt sequence, backend, effective model, timestamps, and attempt outcome. One user-facing A-leg can have multiple B-legs through failover or parallel routing.

Implication:

- one **Turn Usage Record (TUR)** should correspond to the A-leg/customer settlement boundary;
- one **Leg Usage Record (LUR)** should correspond to each B-leg/provider-cost boundary;
- session is an aggregation dimension, not a financial mutation boundary.

### Provider evidence already belongs at adapters/connectors

Backend adapters and executable connectors already have accounting-evidence/finalization concepts. Providers may emit repeated/cumulative usage while streaming, but the adapter can resolve this into one final normalized B-leg evidence object at termination.

This is preferable to letting runtime reconstruct provider economics from canonical stream events.

### Existing reservation machinery proves the required concurrency pattern

Current usage-authority stores already demonstrate the important atomic idea: account capacity can track both consumed and reserved exposure, and reserve operations can be performed transactionally.

The new architecture should keep the atomic-reservation property while removing generalized stream/fact/exposure settlement semantics from money billing.

### Bun DB infrastructure is already present

`internal/infra/db` provides Bun wrapping for SQLite/PostgreSQL plus PostgreSQL open/pool helpers. Several durable stores already use repository migration/test patterns.

There is no reason to introduce another ORM, ledger database product, or persistence abstraction for billing.

## Chosen Terminology

Use domain-neutral names:

- **Turn Usage Record (TUR):** immutable evidence for one completed A-leg/logical turn.
- **Leg Usage Record (LUR):** immutable evidence for one B-leg/provider attempt within the TUR.
- **Authorization Hold:** pessimistic exposure reserved before upstream work.
- **Billing Result:** deterministic post-turn customer/operator economic result.
- **Journal Transaction / Journal Entry:** immutable balanced financial/authorization postings.

The term **CDR** may appear only when explaining the historical telecom inspiration.

## Runtime Simplification

The live path should have exactly two billing touch points:

```text
before upstream:
route plan -> MaxCustomerCharge -> atomic authorization

after terminal:
seal TUR/LUR evidence -> durable handoff
```

Everything else is post-turn. Runtime does not need to:

- enrich usage events with prices;
- maintain customer/operator monetary totals;
- deduplicate economic stream samples;
- reconstruct usage for settlement;
- post journals;
- settle balances;
- interpret correction/replacement semantics.

This is the central simplification and must remain true even as the durable financial layer becomes rigorous.

## Pessimistic Authorization and Concurrent Credit Safety

### Account convention

Use one signed customer balance convention:

```text
Balance = customer financial credits - customer financial debits
CreditFloor = 0                      for prepaid
CreditFloor = -CreditLimit           for postpaid
Spendable = Balance - CreditFloor - Reserved
```

Prepaid example:

```text
funded 250 -> Balance +250
usage 40   -> Balance +210
floor       -> 0
```

Postpaid example:

```text
credit limit 100
start Balance 0
usage 40 -> Balance -40
floor    -> -100
```

### Correct admission invariant

A non-negative current spendable value is not enough. The requested authorization must fit:

```text
require SpendableBefore >= MaxCustomerCharge
SpendableAfter = SpendableBefore - MaxCustomerCharge
require SpendableAfter >= 0
```

Every concurrently admitted A-leg holds its own pessimistic maximum. The store atomically serializes/version-checks same-account authorization, so aggregate concurrent exposure cannot exceed prepaid funds or the postpaid line across multiple Go-LIP processes.

This is simpler and stronger than dynamically changing a user to concurrency=1 at low balance.

## Why Double-Entry, and What It Means Here

Classical double-entry does **not** require two independently synchronized databases. The safer implementation is one journal transaction containing two or more postings that must balance exactly:

```text
sum(debits) == sum(credits)
```

Two books use the same engine:

### Financial book

Customer usage:

```text
Dr Customer Financial Account
Cr Usage Revenue
```

Funding/payment:

```text
Dr Cash / Payment Clearing
Cr Customer Financial Account
```

Provider-billable B-leg:

```text
Dr Inference COGS
Cr Provider Payable / Clearing
```

### Authorization book

Hold:

```text
Dr Customer Reserved Exposure
Cr Authorization Contra
```

Release/close reverses the authorization postings.

This keeps pending exposure auditable without recognizing revenue before usage occurs.

## Materialized State vs Journal Truth

Fast authorization needs a materialized account row containing at least balance, reserved exposure, version, and safety status. That row is not unrecoverable monetary truth.

Verified journal replay plus durable account policy must reconstruct:

```text
Balance = financial customer credits - debits
Reserved = authorization reserved-exposure debits - credits
Spendable = Balance - CreditFloor - Reserved
```

Point-in-time before/after balance/reserved/spendable snapshots are intentionally redundant. They improve incident diagnosis and identify the first inconsistent operation, but rebuild does not trust them as financial source input.

## Durable Financial Identity

### Why request/attempt labels are insufficient

A financial idempotency key must encode the business operation, not merely whichever runtime label happened to be available.

Chosen durable keys:

```text
TURKey = BillingAccountID + stable Turn/ALeg identity
LURKey = TURKey + BLegID
CustomerSettlementSourceKey = customer-settlement:v1 + TURKey
ProviderCostSourceKey = provider-cost:v1 + LURKey
```

If one B-leg legitimately creates multiple independent provider charges, a closed typed cost-source discriminator can extend the provider source key.

### Semantic fingerprint

Uniqueness of a key alone cannot distinguish a legitimate retry from a changed payload. TUR/LUR records and journal transactions therefore persist a versioned canonical semantic fingerprint over fixed immutable business fields.

```text
same key + same fingerprint      -> idempotent replay
same key + different fingerprint -> integrity failure
```

The fingerprint excludes mutable processing state, insertion timestamps, leases, and DB metadata.

## Deterministic Replay Ordering

Wall-clock timestamps are audit metadata, not a safe total order. Equal timestamps, clock skew, or DB query ordering can make first-mismatch diagnostics nondeterministic.

Every account-correlated journal transaction therefore receives an atomically allocated monotonic `AccountSequence`. Reconciliation/rebuild orders by that sequence. `(account_id, account_sequence)` is unique.

A settlement that produces several account-correlated transactions obtains a deterministic contiguous sequence range under the same account transaction.

## Corrections

Posted entries are immutable. Corrections use explicit reversal/replacement linkage:

```text
original T1
reversal T2: ReversalOf = T1
replacement T3: CorrectsTransactionID = T1
T2/T3: CorrectionGroupID = same correction group
```

References must exist, be in the valid account/book/currency scope, not self-reference, and form an auditable chain if corrected again later.

## Exact Economic Snapshot Binding

Authorization and final rating must use the same economic version identities, not merely numerically similar rates.

Before calculation:

- customer pricing snapshot ref must equal the ref bound in both TUR and authorization;
- customer charging-policy ref must equal TUR and authorization;
- each LUR that needs fallback operator rating must resolve the exact operator-rate ref sealed in that LUR.

A different snapshot identity is rejected even when its prices happen to be numerically equal. This prevents mutable-current-price drift and makes replay reproducible.

## B2BUA Economics

Customer and operator accounting have different granularities:

```text
A-leg/TUR:
  customer settlement / revenue

B-leg/LUR:
  provider cost / COGS
```

Example:

```text
A-leg A1
  B1 Model X / Provider P -> failed, provider cost 0.02
  B2 Model Y / Provider Q -> succeeded, provider cost 0.07
```

Both B1 and B2 may create COGS. Customer policy may still charge only the surfaced logical result. Therefore operator cost cannot be inferred from customer revenue and failed legs must not disappear.

## Unrateable Provider Cost

For every provider-billable LUR:

1. use authoritative provider cost when present, including explicit zero;
2. otherwise rate sufficient final resource quantities using the exact bound operator-rate snapshot;
3. otherwise mark processing `unreconciled_cost`.

The system must not silently omit COGS, synthesize zero, or mark the TUR fully processed when a required provider cost cannot be reconciled.

## Immutable Evidence vs Processing Workflow

TUR/LUR rows are sealed evidence. Worker state must be separate:

```text
usage_record_processing
  pending
  processing
  retryable
  processed
  unreconciled_cost
  terminal_error
```

This preserves evidence immutability while allowing leases/retries/status transitions.

A generic workflow/event engine is unnecessary; a bounded durable processing table and in-process worker/terminal-work mechanism are sufficient.

## Safety State: reconcile_required

A financial system should not continue hard credit admission after it has evidence that account state cannot be trusted.

Journal-balance failure, conflicting replay fingerprint, invalid correction linkage, impossible snapshots, sequence/reconstruction failure, or materialized-state mismatch transitions the affected account to:

```text
reconcile_required
```

While blocked:

- new hard prepaid/postpaid authorizations fail closed before upstream work;
- read/reconciliation/repair remains available;
- the system does not guess a balance.

Only an explicit audited successful reconciliation/rebuild can restore `ready`.

## Trusted Monetary Operations

The billing store needs explicit business operations beyond usage settlement:

- funding/top-up;
- postpaid payment;
- controlled adjustment;
- authorization release;
- correction reversal/replacement.

These are narrow trusted commands with typed reasons, idempotency identity, fingerprints, balanced postings, and before/after snapshots. There should be no generic arbitrary-posting API exposed to runtime or plugins.

## Rejected Alternatives

### Stream-time accounting

Rejected because evidence arrival timing does not require financial interpretation timing. It couples finance to retry/stream/cancellation and creates multiple mutable truth paths.

### Low-balance concurrency=1 as primary safety mechanism

Rejected because atomic holds already solve the exact shared-balance race without an extra concurrency state machine.

### Two separately synchronized debit/credit ledgers

Rejected because one balanced transaction with multiple postings is simpler and atomic.

### Treating holds as financial revenue

Rejected because authorization exposure is not earned money. Keep it in a separate authorization book.

### Generic event sourcing / Kafka / CQRS / workflow framework

Rejected as unnecessary for a bounded post-turn billing worker and durable journal.

### Session-wide billing accumulator

Rejected because session is not the transaction boundary. One session may contain many A-legs, each with many B-legs.

### Generic chart-of-accounts/ERP framework

Rejected. Implement the small closed set of ledger accounts/commands needed by Go-LIP and extend only when a concrete feature requires it.

## Migration Consequences

The safe migration sequence is:

1. characterize current financial/wire/B2BUA behavior;
2. shadow immutable TUR/LUR evidence;
3. implement Bun journal/account store and contract tests;
4. add pessimistic authorization and concurrency proofs;
5. shadow exact-snapshot post-turn rating;
6. cut customer settlement/provider COGS to journal path;
7. cut reports and certify rebuild/reconciliation;
8. delete stream-time financial accounting and add architecture ratchets.

The target intentionally leaves non-money request/token/rate-limit policy outside the billing rewrite unless separately migrated.

## External Review Result

CodeRabbit's ten actionable findings were all valid and improved the design. They are now explicitly represented in requirements/design/tasks: durable TUR/LUR financial identity, requested-amount admission invariant, correction linkage, AccountSequence, exact snapshot identity, separate processing state, semantic replay fingerprints, complete trusted store commands, `unreconciled_cost`, and `reconcile_required` block/re-enable behavior.
