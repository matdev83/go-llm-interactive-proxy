# Research and Discovery Notes

## Baseline findings

Current `main` already provides the hard parts worth retaining: exact fixed-point money arithmetic; immutable customer/operator pricing snapshots; pure pessimistic max-cost estimation; provider-neutral B-leg evidence; Bun SQLite/PostgreSQL persistence; immutable double-entry financial journal with fingerprints/account sequence; prepaid/postpaid balance semantics; post-turn worker, reconciliation, reporting and trusted account provisioning.

The remaining complexity comes mainly from modelling pessimistic admission as a **financial hold** and then making runtime/settlement clean that hold up.

## Telecom pattern translated to Go-LIP

```text
financial balance = settled usage only
admission risk    = pessimistic max of every admitted call not yet customer-settled
```

A softswitch's active-call table is operational occupancy, not an accounting debit. Go-LIP can model the same thing with one immutable `CallExposure` row per admitted call.

## Chosen exposure lifecycle

```text
ABSENT -> OPEN(max) -> CLOSED
```

OPEN means only that the call was admitted but its customer billing operation has not posted. There is no normal `active -> completed -> unbilled` transition. The max remains counted after completion until settlement closes it, eliminating the credit window where completed usage is neither settled nor exposed.

## Concurrency proof

```text
SafetyMargin = Balance - CreditFloor - Sum(OpenExposure)
```

Admission requires `SafetyMargin >= NewMax` and inserts `NewMax` under an account-scoped lock.

Settlement atomically reduces Balance by `Actual` and closes exposure `Max`, with `Actual <= Max`:

```text
MarginAfter = MarginBefore + (Max - Actual) >= MarginBefore
```

Thus valid settlement cannot worsen the hard-credit margin.

## Why no exposure aggregate counter initially

A mutable counter would be another projection needing increment/decrement/replay/rebuild. Baseline uses indexed `SUM` over open rows under the account lock. Add a counter only if measurement proves per-account row cardinality is a bottleneck.

## Two-stage admission

**Stage 1:** one cheap account read before routing: readiness, settled balance/floor and typed `MinPreRouteHeadroom`. No pricing, tokenizer, routing or provider work. Zero threshold permits zero-headroom accounts to reach detailed routing for free routes; positive threshold intentionally rejects micro-headroom calls sooner.

**Stage 2:** reuse the existing max-cost estimator after routing. Pessimistic input assumes no uncertain cache discount; output uses client cap if lower else model max; surfaced-turn policy takes max candidate customer charge; multi-leg/pass-through policy sums chargeable legs. Operator retry cost does not affect customer admission unless explicitly passed through.

The quote ends in operational exposure admission, not a financial hold.

## Billing identity nuance

A-leg is a long-lived continuity/session anchor and cannot safely be the customer-settlement key.

```text
BillingCallID = one proxy-owned ID per incoming inference invocation
ALegID        = session/B2BUA correlation
SessionID     = reporting correlation
BLegID        = provider-attempt correlation

customer key = account + BillingCallID
provider key = BillingCallID + BLegID
```

All retries/parallel B-legs for one client call share BillingCallID.

## Removing the runtime TUR collector

Instead of remembered B-leg evidence and parallel barriers:

1. each B-leg terminal appends one immutable leg record;
2. the request terminal owner appends one call closure with exact expected B-leg IDs after no further B-leg can be allocated;
3. storage claims the call only when every expected leg row exists.

Delivery order is irrelevant and runtime needs no financial evidence barrier.

## At-least-once usage versus exactly-once money

```text
terminal usage delivery: at least once
financial effect:        at most once
```

Stable keys/fingerprints make duplicate usage safe; unique customer/provider operation keys prevent duplicate money. Authoritative mode therefore requires a **durable usage spool**. Memory outbox is test/non-authoritative only. Simultaneous total loss/unavailability of all durable replicas before any terminal append succeeds is explicitly outside the guarantee.

## Customer billing should not wait for provider COGS

Target flow:

```text
complete call -> customer rating -> customer operation + balance update + exposure close

same legs     -> independent provider-cost operations -> COGS or unreconciled_cost
```

An internal provider-cost problem cannot strand customer credit once customer rating is valid.

## Zero-charge calls

A free call still gets a durable customer-billing operation proving it was processed, but no artificial zero debit/credit entry. Same for zero provider cost.

## Stale exposure recovery

TTL alone is unsafe. Exposure may close outside normal settlement only when durable evidence proves the call cannot continue and either complete usage exists or an explicit operator-approved no-charge recovery operation is created.

## Rejected alternatives

- **Low-balance concurrency=1:** useful without atomic occupancy but unnecessarily serializes cheap calls and still needs multi-process coordination.
- **Full concurrency lease subsystem:** brings TTL/renew/release/heartbeat semantics the exposure model does not need.
- **Rename holds to exposures:** keeping authorization journal/reserved balance preserves the same complexity.

## Migration direction

1. characterize current outcomes and deletion baseline;
2. add BillingCallID + durable leg/call spool in shadow;
3. shadow cheap screen + exposure decisions;
4. cut hard-credit admission to exposure rows;
5. cut customer settlement to call operation + exposure close;
6. decouple provider COGS;
7. reconcile/retire holds, reserved state and authorization book;
8. delete runtime collector/release compatibility and ratchet architecture.

## Target vocabulary

Keep: Account, Pricing/Policy Snapshot, BillingCallID, CallExposure, LegUsageRecord, CallUsageRecord, CustomerBillingOperation, ProviderCostOperation, FinancialJournal.

Delete from normal call flow: Monetary Hold, Reserved Balance, Authorization Journal Book, Hold Remainder/Release/Expiry, Runtime Financial Evidence Barrier, Runtime TUR Rebuilder, provider-cost-complete prerequisite for customer billing.
