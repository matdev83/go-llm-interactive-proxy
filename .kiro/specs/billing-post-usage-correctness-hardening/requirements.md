# Requirements Document

## Introduction

The billing architecture merged by PR #340 correctly replaced monetary authorization holds with a cheap settled-credit screen, immutable operational call exposure, durable terminal usage records, and post-usage financial settlement. A post-merge review nevertheless found correctness and lifecycle defects in the bridge from the new `BillingCallID`-scoped usage model into legacy rating machinery.

This spec is the first of two ordered remediation specs. It is intentionally narrow and must be implemented before `billing-architecture-final-convergence`. Its purpose is to make the current architecture financially correct, bounded in memory, and safe to operate before the second spec removes the remaining legacy abstractions.

The target invariant for this spec is:

```text
one BillingCallID
  -> exact immutable B-leg identities + actual attempt sequence
  -> correct customer pricing for the actual backend/model leg(s)
  -> customer rating independent of provider-cost readiness
  -> bounded call-scoped runtime bookkeeping
  -> one idempotent customer settlement
```

No monetary hold lifecycle may be reintroduced as a shortcut.

## Boundary Context

- **In scope**: `CallLegUsageRecord` attempt sequence, backward-compatible persistence/read semantics, customer rating input and snapshot resolution, route/model customer pricing, customer/provider resolver separation, `billingTurnCollector` replacement with call-scoped state, terminal finalization single-flight ownership, B2BUA correctness tests, architecture ratchets, SQLite/PostgreSQL migration parity.
- **Out of scope**: deleting the legacy `TurnUsageRecord`/`LegUsageRecord` model, dropping old TUR/LUR tables, removing `ReservedNano` from all domain/schema surfaces, narrowing money-capable `UsageAuthority`, changing provider COGS journal sequencing, introducing a process-local terminal spool, changing prepaid/postpaid balance semantics, changing pessimistic exposure formulas, changing public protocol/plugin APIs.
- **Dependency**: this spec starts from main commit `811c6aa1e442ba15206ebcd9506d945c70911d7b` or a descendant that preserves PR #340 semantics.
- **Successor**: `billing-architecture-final-convergence` must not begin implementation until this spec is complete and its regression suite is green.
- **Core ownership**: `internal/core/billing` owns billing record/rating policy; runtime owns B2BUA execution and terminal record production; `internal/infra/billingstore` owns Bun persistence; `internal/infra/billingcompose` owns snapshot composition.
- **No public contract expansion**: fixes stay internal unless a later design validation proves an internal-only correction impossible.

## Requirement 1: Preserve the New Monetary Authority

**Objective:** As a maintainer, I want correctness fixes to preserve the converged billing authority, so that remediation cannot regress to the retired hold architecture.

### Acceptance Criteria

1.1. Authoritative customer credit admission shall remain `cheap credit screen -> route/quote -> atomic CallExposure admission`.

1.2. Customer balance mutation shall remain post-usage and shall occur only through idempotent financial settlement of one `BillingCallID`.

1.3. The implementation shall not recreate monetary authorization holds, `reserved_nano` spendable subtraction, authorization-book call-path postings, hold TTL/renewal, hold release, or hold remainder math.

1.4. Stream handlers shall not rate customer money, post financial journal entries, mutate billing account balances, or close operational exposure.

1.5. Provider COGS shall remain independently processable from customer settlement and shall not become a prerequisite for closing customer exposure.

1.6. Existing prepaid/postpaid credit-floor semantics, `actual <= admitted max`, double-entry customer posting, idempotency, and reconcile-required behavior shall remain unchanged except where required to fix incorrect leg/rate selection.

## Requirement 2: Preserve Actual B-Leg Attempt Order

**Objective:** As a billing operator, I want every persisted terminal B-leg to retain its real B2BUA attempt sequence, so that post-usage policy never guesses execution order from an opaque identifier or storage order.

### Acceptance Criteria

2.1. Every newly persisted `CallLegUsageRecord` shall carry the actual positive B-leg attempt sequence allocated by B2BUA.

2.2. Runtime shall copy the sequence from the authoritative `b2bua.BLegRecord.Seq`; it shall not derive sequence from `BLegID`, array position, lexical order, timestamp order, provider order, or completion order.

2.3. The B-leg sequence shall participate in the new-record semantic fingerprint and replay-conflict identity so that a same-key record with a different sequence is rejected.

2.4. The durable `usage_leg_records` representation shall persist the sequence explicitly and restore it without inference.

2.5. Within one `BillingCallID`, two persisted terminal legs shall not claim the same positive attempt sequence unless they are byte-for-byte/idempotently the same logical leg.

2.6. `ExpectedBLegIDs` may remain a canonical set for completeness checks, but its ordering shall have no financial meaning.

2.7. Any customer-leg selection rule that needs attempt order shall use the persisted B2BUA sequence.

2.8. Tests shall deliberately use opaque B-leg IDs whose lexical order is the reverse of execution order and prove identical billing to correctly ordered IDs.

## Requirement 3: Handle Pre-Fix Durable Leg Rows Safely

**Objective:** As an operator upgrading a brownfield deployment, I want existing usage records without sequence information to remain readable without silently inventing financial facts.

### Acceptance Criteria

3.1. Schema migration shall add sequence storage without rewriting opaque legacy B-leg IDs into guessed sequence values.

3.2. Existing durable leg rows that predate sequence persistence shall remain distinguishable from new rows with known sequence.

3.3. Replay validation for pre-fix rows shall preserve their existing semantic fingerprint contract; the upgrade shall not make all historical rows appear corrupt merely because the new field was absent.

3.4. New runtime appends after migration shall require a known positive B-leg sequence.

3.5. A legacy row with unknown sequence may be rated automatically only when the applicable customer policy is provably sequence-independent for that call, such as a completed call with an unambiguous surfaced leg or a policy that charges all accepted legs.

3.6. When sequence is required to choose a customer-billable leg and a legacy row lacks it, the post-usage processor shall fail closed into an explicit retry/reconcile-required state rather than sorting IDs or timestamps.

3.7. SQLite and PostgreSQL migrations shall have equivalent nullability, indexes/constraints, and read behavior.

## Requirement 4: Settle with the Correct Backend/Model Customer Pricing

**Objective:** As a customer, I want actual billing to use the price card associated with the B-leg(s) that the charge policy selects, so that admission and settlement agree across failover and mixed-model routes.

### Acceptance Criteria

4.1. Post-usage customer rating shall resolve customer pricing for every selected B-leg by its persisted backend/model identity.

4.2. Route/model pricing overrides used by pessimistic admission shall remain available to actual settlement through immutable/versioned pricing snapshots.

4.3. `CallRatingInput` or its equivalent shall carry the model-specific pricing needed by customer rating; the resolver shall not discard the `ModelCustomerPricing` set.

4.4. When no route/model override exists, the configured default customer pricing snapshot may be used.

4.5. When at least one route/model override exists, a selected leg without a resolvable applicable customer price shall fail rating explicitly; it shall not silently fall back to an unrelated model price.

4.6. The customer pricing reference stored with the admitted exposure/call shall continue to bind the immutable pricing generation used for rating.

4.7. Mixed-model failover and multi-leg policies shall independently rate each customer-billable leg using its effective card before summing the customer charge.

4.8. The rated customer total shall continue to be checked against the exact admitted maximum; mismatches caused by configuration/invariant defects shall remain reconcile-required rather than being clamped.

## Requirement 5: Decouple Customer Rating from Provider-Cost Readiness

**Objective:** As an operator, I want a missing provider-cost rate to affect provider COGS reconciliation only, so that customer settlement and operational exposure do not remain blocked by an unrelated operator-economics problem.

### Acceptance Criteria

5.1. Customer rating resolution shall require customer pricing and charge-policy snapshots only.

5.2. Customer rating shall not look up, validate, or require `OperatorRateSnapshot` values.

5.3. Missing, invalid, stale, or unreconciled operator-rate data shall not prevent an otherwise rateable customer call from settling and closing exposure.

5.4. Provider-cost resolution shall remain per-B-leg and may independently retry or remain unreconciled.

5.5. The snapshot catalog shall expose separate customer-rating and provider-cost resolution paths rather than one combined method whose failures couple the two perspectives.

5.6. Customer-rating types shall not carry unused operator-rate collections.

5.7. Tests shall prove customer settlement succeeds while operator-rate lookup fails for one or more B-legs, and that provider-cost work remains pending/unreconciled without changing the customer posting.

## Requirement 6: Bound Runtime Billing State to Active Calls

**Objective:** As a proxy operator, I want completed calls to release all billing-only in-memory state automatically, so that a long-lived executor consumes memory proportional to active work rather than lifetime traffic.

### Acceptance Criteria

6.1. Billing-call bookkeeping shall be owned by one request/`BillingCallID`-scoped state object, not by an executor-global map keyed by every historical call.

6.2. The call-scoped state may retain allocated B-leg identities/sequences, terminal timing bounds, and per-B-leg finalization single-flight state only while that call can still produce terminal billing records.

6.3. Retry, failover, parallel arms, and hidden interleaved continuations belonging to one incoming invocation shall share the same call-scoped state.

6.4. A later incoming invocation on the same A-leg/session shall receive a distinct state object and distinct `BillingCallID`.

6.5. When terminal ownership makes the call immutable and the final terminal usage append attempt has been handed off, no executor-global reference shall retain that call-scoped state.

6.6. Finalization single-flight entries shall become unreachable with their call state; correctness shall not depend on manually evicting keys from a process-lifetime cache.

6.7. The implementation shall remove or reduce `billingTurnCollector` so that no lifetime-growing `allocatedByCall`, `frozenByCall`, `legTimesByCall`, or `finalizeByKey` map remains on `Executor`.

6.8. A stress regression shall execute a large sequence of calls through one executor and prove retained billing bookkeeping is O(active calls/legs), not O(total completed calls).

6.9. Race tests shall cover Recv/Close, parallel loser finalization, request terminalization, and call-state release without data races or use-after-terminal behavior.

## Requirement 7: Preserve Terminal Usage and Failure Semantics

**Objective:** As a runtime maintainer, I want the fixes to preserve terminal ownership and no-retry guarantees, so that billing corrections cannot alter client-visible stream behavior.

### Acceptance Criteria

7.1. Every allocated B-leg shall still produce exactly one terminal leg record or an idempotent replay of that record, including rejected, never-started, failed, canceled, swallowed, parallel-loser, and surfaced-winner outcomes.

7.2. The call-closure record shall still freeze the exact expected B-leg identity set only after no further B-leg may be allocated for that `BillingCallID`.

7.3. Usage-record persistence failure after client-visible output shall not trigger provider retry, failover, duplicate B-leg allocation, or client-visible success-to-error rewriting.

7.4. Backend `FinalizeBilling` extraction shall remain at most once per B-leg within one active call state even if multiple terminal paths race.

7.5. Final provider billing evidence and fallback stream evidence shall retain explicit presence/authority semantics; correctness fixes shall not infer absent usage/cost as authoritative zero.

7.6. Existing durable append replay/outbox behavior remains in scope only as required to preserve current guarantees; redesign of the terminal spool belongs to the successor spec.

## Requirement 8: Prove the Correction Before Architecture Cleanup

**Objective:** As a reviewer, I want the corrected implementation characterized independently from the later deletion refactor, so that the simplification pass starts from a known-correct baseline.

### Acceptance Criteria

8.1. RED tests shall first reproduce the reversed-BLegID sequence bug and the route/model pricing bug on current main.

8.2. RED tests shall prove customer settlement currently fails when an unrelated operator-rate snapshot is missing, before the resolver split is implemented.

8.3. RED tests shall demonstrate process-lifetime growth of current executor billing-call maps or otherwise characterize the retained-state defect before replacement.

8.4. Focused pure billing tests shall cover completed surfaced calls, failed/canceled calls with no surfaced leg, charge-all policy, failover, mixed-model pricing, and zero-charge calls.

8.5. Real-store tests shall cover sequence persistence, legacy sequence absence, replay conflict, claim/join order independence, and SQLite/PostgreSQL parity.

8.6. Architecture tests shall forbid reintroducing financial meaning from lexical B-leg ordering, customer dependence on operator-rate resolution, and executor-global lifetime-growing billing-call maps.

8.7. Existing hold-deletion and no-stream-money ratchets shall remain active and green.

8.8. Final verification shall include focused billing/runtime/store tests, architecture tests, default unit tests, quality checks, and targeted race tests where supported.

8.9. Completion of this spec shall establish the implementation baseline for `billing-architecture-final-convergence`; the successor may simplify internals but shall preserve all behaviors proven here.
