# Design Validation Review

## Review Method

The billing design was revalidated as a brownfield architecture replacement against repository `main`, Kiro/Go architecture rules, current B2BUA lineage, backend final billing evidence, runtime terminal ownership, usage-authority reservations, Bun database infrastructure, and the revised requirements/design.

A NO-GO is required for any unresolved financial-integrity, concurrency, recovery, B2BUA-attribution, replay, or runtime-boundary ambiguity.

## Prior Architecture Review

Earlier rounds established the following durable direction:

1. execution remains billing-blind after one pre-upstream authorization;
2. actual billing operates on completed Turn/Leg Usage Records;
3. every concurrent hard-credit turn owns a pessimistic atomic hold;
4. customer revenue and provider cost are distinct;
5. classical double-entry journal history is reconstructible truth;
6. materialized balances are rebuildable projections;
7. Bun SQLite/PostgreSQL infrastructure is reused;
8. one A-leg may contain multiple financially relevant B-legs.

Those decisions remain valid.

## External Review Round — CodeRabbit

CodeRabbit posted ten actionable comments. All ten were treated as substantive rather than stylistic and the design was reopened to NO-GO until resolved.

### 1. TUR/LUR replay identity was underspecified

**Finding:** request/attempt labels alone could collide or permit ambiguous provider-cost replay.

**Resolution:**

```text
TURKey = BillingAccountID + stable Turn/ALeg identity
LURKey = TURKey + BLegID
CustomerSettlementSourceKey = customer-settlement:v1 + TURKey
ProviderCostSourceKey = provider-cost:v1 + LURKey
```

A closed cost-source discriminator is allowed only when one B-leg can legitimately produce multiple independent costs. TUR/LUR rows persist versioned semantic fingerprints. Same key + same fingerprint is idempotent; same key + different fingerprint is an integrity error.

**Decision:** PASS.

### 2. Affordability invariant omitted the requested hold amount

**Finding:** `Spendable >= 0` before authorization does not prove the new reservation fits.

**Resolution:** admission now requires:

```text
SpendableBefore >= MaxCustomerCharge
SpendableAfter = SpendableBefore - MaxCustomerCharge >= 0
```

**Decision:** PASS.

### 3. Replacement correction linkage was incomplete

**Finding:** `ReversalOf` alone identifies the reversal but not the replacement transaction's target.

**Resolution:** corrections now carry `ReversalOf`, `CorrectsTransactionID`, and shared `CorrectionGroupID`. References must target existing eligible transactions in the same account/book/currency and may not self-reference.

**Decision:** PASS.

### 4. Journal replay relied on wall-clock timestamps

**Finding:** timestamps are not a total durable order under concurrency or clock skew.

**Resolution:** every account-correlated journal transaction receives atomically allocated monotonic `AccountSequence`; multi-transaction settlement receives a contiguous deterministic range. `(account_id, account_sequence)` is unique. `RecordedAt` is audit metadata only.

**Decision:** PASS.

### 5. Rating snapshot identities were not validated before calculation

**Finding:** supplying an arbitrary snapshot with numerically identical rates could make replay non-reproducible.

**Resolution:** customer pricing/policy references must match both TUR and authorization. Every fallback-rated LUR must resolve its exact sealed operator-rate reference. Identity mismatch fails before rating/posting.

**Decision:** PASS.

### 6. Immutable TUR payload was conflated with mutable processing state

**Finding:** worker status/claims could violate the sealed-record immutability promise.

**Resolution:** TUR/LUR rows are immutable evidence. Claim, lease, retry, safe-error, status, and result-reference fields live in separate `usage_record_processing` state keyed by TUR durable key/fingerprint.

**Decision:** PASS.

### 7. Unique idempotency keys did not prove semantic equality

**Finding:** same source key with a changed monetary payload could incorrectly become a no-op.

**Resolution:** journal transactions and usage records persist versioned canonical semantic fingerprints. Idempotent replay is accepted only after atomic fingerprint comparison; same key with different semantics is an integrity error.

**Decision:** PASS.

### 8. Store contract omitted trusted financial operations

**Finding:** funding existed conceptually, but payment, adjustment, and explicit hold-release commands were missing from the narrow store contract.

**Resolution:** the store contract now explicitly includes `PostFunding`, `PostPayment`, `PostAdjustment`, and `ReleaseAuthorization` plus reversal/reconciliation operations. Release requires closed reason code and deterministic idempotency identity. There is still no generic arbitrary-posting API.

**Decision:** PASS.

### 9. Unrateable provider cost had no safe terminal state

**Finding:** a provider-billable B-leg without authoritative cost or sufficient fallback inputs could silently disappear from COGS.

**Resolution:** it enters explicit `unreconciled_cost`. The system must not synthesize zero cost, omit the leg, or mark the TUR fully processed. The authorization remains conservatively held until repair/reconciliation policy resolves processing.

**Decision:** PASS.

### 10. `reconcile_required` was design prose rather than acceptance behavior

**Finding:** account blocking/re-enable semantics were not testable requirements.

**Resolution:** requirements now mandate atomic transition to `reconcile_required` on reconstruction/integrity failure, fail-closed hard-credit authorization while blocked, and explicit audited successful reconciliation/rebuild as the only re-enable path.

**Decision:** PASS.

## Accounting Integrity Review

**Decision: PASS**

The final design explicitly proves these invariants:

1. each journal transaction has debit and credit postings;
2. transaction debits equal credits exactly;
3. posted journal and sealed TUR/LUR evidence are immutable;
4. financial and authorization books balance independently;
5. prepaid balance cannot cross zero;
6. postpaid balance cannot cross `-CreditLimit`;
7. a new hold requires `SpendableBefore >= MaxCustomerCharge`;
8. same durable key is idempotent only for the same semantic fingerprint;
9. replay uses durable `AccountSequence`, not time;
10. correction reversal/replacement links are explicit and referentially valid;
11. final rating uses the exact pricing/policy/rate identities bound at authorization/evidence finalization;
12. provider-billable unrateable legs cannot silently become zero/omitted COGS;
13. journal replay reconstructs materialized monetary state;
14. integrity/reconciliation failures block hard-credit admission until verified repair.

## B2BUA Review

**Decision: PASS**

- TUR/customer settlement boundary is one A-leg.
- LUR/provider-cost boundary is one B-leg.
- sequential failover, swallowed failures, winners, and parallel B-legs remain visible.
- each B-leg may use a different provider/model/rate.
- failed/losing B-legs can generate operator COGS independently of customer policy.
- session totals are read-side aggregation, not a write transaction boundary.

## Runtime/Hexagonal Review

**Decision: PASS**

- runtime has only pre-upstream authorization and terminal TUR handoff;
- no stream-time journal/rating/settlement ownership;
- provider SDKs remain adapter-edge only;
- core billing uses narrow value/store/snapshot contracts;
- Bun transaction mechanics stay in infrastructure;
- reporting is read-side.

No DI container, service locator, generic event bus, CQRS/workflow platform, or billing DSL is introduced.

## Database/Recovery Review

**Decision: PASS**

Strict durable billing reuses `internal/infra/db` and Bun-backed SQLite/PostgreSQL adapters. Durable data separates immutable usage evidence from mutable processing state. Journal transactions carry account sequence and semantic fingerprint. Reconciliation can rebuild signed balance/reserved/spendable from verified journal + account-policy history without rewriting posted journal entries.

## Testing Review

**Decision: PASS**

Required high-value proofs include:

- pure max-charge and TUR/LUR calculation tables;
- exact snapshot identity mismatch tests;
- same-key/same-fingerprint vs same-key/different-fingerprint replay tests;
- real-store concurrent authorization tests;
- correction-link validation;
- AccountSequence uniqueness/order under concurrency;
- `unreconciled_cost` failure tests;
- journal trial-balance/property tests;
- corrupted replay/snapshot/materialized-state tests proving `reconcile_required`, authorization block, and verified re-enable;
- architecture tests forbidding stream-time financial ownership.

## Simplicity Review

**Decision: PASS**

The financial model is rigorous but bounded. Production concepts remain:

1. Billing Account
2. Authorization Hold
3. Turn Usage Record
4. Leg Usage Record
5. Billing Result
6. Journal Transaction
7. Journal Entry
8. Usage Record Processing State

There is one journal engine, one durable store boundary, and one post-turn calculation path. Added complexity protects financial integrity rather than execution orchestration.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

All ten CodeRabbit actionable findings are incorporated into requirements/design. The review improved identity, replay determinism, correction semantics, snapshot binding, failure states, and store completeness without moving financial logic back into LLM streaming.

Implementation remains gated by Kiro approvals and must start RED-first with characterization/store-contract tests.
