# Brownfield Design Validation Review

## Review Method

Validated against current `main` after the merged usage-record billing implementation/refactors and billing host composition, with focus on prepaid/postpaid hard limits, multi-process concurrency, long-lived A-leg/session semantics, B2BUA failover/parallel legs, post-output failure behavior, usage durability, exactly-once financial effects, double-entry/rebuild, dependency direction, and actual deletion rather than layering.

## Round 1 — NO-GO

### “No financial reservations” alone did not prevent concurrent overspend

A pure read-only check of balance + current calls races across proxy processes.

**Resolution:** one minimal operational `CallExposure` row is inserted under the same account lock used by customer settlement. It is explicitly not financial state and creates no journal entry/account balance mutation.

Core proof:

```text
SafetyMargin = Balance - CreditFloor - OpenExposure
```

Admission reduces margin by `Max`; settlement changes it by `Max - Actual >= 0`.

### Completed-but-unbilled usage could disappear from affordability

An `active -> finished` exposure transition would create a window where usage is neither settled nor exposed.

**Resolution:** exposure remains OPEN unchanged from admission until customer settlement atomically charges and closes it.

### A-leg identity was not a billable-call identity

A-leg is a long-lived continuity/session anchor.

**Resolution:** one proxy-owned BillingCallID per inference invocation; A-leg/session are correlation only.

## Round 2 — NO-GO

### Runtime TUR assembly retained too much billing-specific state

An all-legs TUR still required remembered evidence and parallel barriers.

**Resolution:** persist B-leg terminal records independently; terminal owner persists one call closure with final expected B-leg IDs; storage joins in any delivery order.

### Usage delivery durability was overstated

A process-local outbox cannot prove at-least-once after process loss.

**Resolution:** authoritative billing requires a durable Bun usage spool. Memory spool is tests/non-authoritative only. Total loss/unavailability of every durable replica before any append succeeds is explicitly outside the guarantee.

### Pre-provider abort cleanup could recreate release logic

**Resolution:** runtime never normally closes exposure. Even zero-use/pre-provider failures emit terminal usage; post-usage customer processing records zero and closes exposure.

## Round 3 — NO-GO

### Provider COGS still blocked customer settlement

Current all-or-nothing settlement can strand customer credit on an internal provider-cost problem.

**Resolution:** customer settlement and provider-cost posting are independent idempotent operations. Customer settlement closes exposure as soon as customer rating is valid; provider COGS can remain `unreconciled_cost` and retry.

### Cheap gate could make free routes impossible

**Resolution:** `MinPreRouteHeadroom` is explicit policy. Zero allows detailed routing at zero headroom; positive values are an intentional product/operator optimization.

### TTL-only exposure cleanup was unsafe

**Resolution:** stale exposure can close only with positive durable proof that execution cannot continue plus complete usage or an explicit operator-approved no-charge repair operation.

## Round 4 — PASS / GO FOR DESIGN READINESS

### Financial correctness — PASS

- settled balance is journal-derived only;
- admission does not mutate financial account/journal state;
- non-zero money remains double-entry;
- zero outcomes remain durably processed without zero journal entries;
- balance rebuild is independent of exposure reconstruction.

### Hard-credit concurrency — PASS

All admissions, customer settlements and balance/credit-policy mutations for one account serialize on the same account row. The SafetyMargin proof covers prepaid/postpaid accounts and arbitrary `Actual <= Max` settlement order.

### Runtime isolation — PASS

Synchronous request-plane responsibilities are limited to:

1. cheap account screen before routing;
2. detailed quote + operational exposure insert after routing;
3. immutable terminal usage append.

No financial journal/customer/provider settlement belongs to stream handlers.

### B2BUA identity — PASS

BillingCallID scopes one incoming invocation. A-leg/session remain long-lived correlation. Every B-leg is keyed under the call, so failover/parallel/provider-model diversity is naturally represented.

### Post-output semantics — PASS

Terminal usage persistence failure cannot trigger provider retry/failover or alter selected output.

### Storage/recovery — PASS

Bun provides the account-scoped atomicity and durable usage acceptance point. No additional ORM/event framework is required.

### Simplicity review — PASS

Required lifecycle concepts are limited to Account, immutable quote refs, BillingCallID, open exposure, terminal leg/call records, customer operation, provider-cost operation and financial journal.

Removed concepts include monetary hold, reserved financial balance, authorization book, hold remainder/release/renewal/expiry, runtime financial evidence barrier/TUR rebuilder, and provider-cost-complete prerequisite for customer charging.

### SOLID / hexagonal review — PASS

Runtime executes/emits facts; billing core calculates policy; Bun owns transactions; provider adapters own evidence; financial journal owns money; reports are read-side. Interfaces remain at real read/write boundaries.

### Brownfield migration — PASS WITH CUTOVER GATES

Shadow calculation/comparison is permitted, but no long-lived dual monetary authority. One generation/config selects the active hard-credit mechanism. Legacy open holds must be reconciled before retirement.

### Testing/provability — PASS

The design contains direct property tests for SafetyMargin, explicit multi-call A-leg tests, usage replay tests, independent customer/provider accounting tests, and architecture symbol/import/deletion ratchets.

## Requirements Traceability

All 17 requirements have design ownership:

| Requirement | Validation owner |
|---|---|
| 1 | financial vs exposure boundary |
| 2 | BillingCallID |
| 3 | pre-route gate |
| 4 | max quote |
| 5–6 | exposure store + SafetyMargin |
| 7 | runtime isolation |
| 8 | durable leg/call spool |
| 9 | customer settlement |
| 10 | provider cost |
| 11 | financial journal |
| 12 | reconciliation/recovery |
| 13–14 | deletion/contraction |
| 15 | Bun persistence |
| 16 | reporting |
| 17 | TDD/architecture ratchets |

## Implementation Gate

This is a specification-only PR. Requirements/design/tasks approvals remain false and `ready_for_implementation` remains false until maintainers approve this replacement direction.
