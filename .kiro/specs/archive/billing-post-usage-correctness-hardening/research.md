# Research and Current-State Findings

## Scope and Baseline

This research analyzes merged main at `811c6aa1e442ba15206ebcd9506d945c70911d7b` (`feat(billing): decouple exposure and post-usage settlement (#340)`) and the post-merge review that produced a NO-GO decision.

The prior refactor successfully established the correct monetary ownership direction:

```text
cheap credit screen
 -> route + pessimistic quote
 -> atomic CallExposure
 -> execute without money mutation
 -> terminal usage records
 -> post-usage customer settlement
 -> independent provider COGS
```

The new spec must preserve that direction. The defects below come from incomplete adaptation around the new usage records, not from the exposure model itself.

## Finding R1 — B-Leg IDs Are Opaque

`internal/core/b2bua/ids.go` generates B-leg IDs from random bytes (`b_` + 32 hex characters). They contain no sequence information.

Consequence: a durable record that drops `b2bua.BLegRecord.Seq` cannot later reconstruct attempt order from `BLegID`.

## Finding R2 — New Leg Records Drop Real Sequence

`internal/core/runtime/billing_leg.go` constructs a legacy `LegUsageRecord` with the real B2BUA `Seq`, but `appendIndependentCallLeg` converts it into `CallLegUsageRecord` without sequence.

`internal/core/billing/call_usage.go` likewise defines no sequence field on `CallLegUsageRecord`.

This is data loss at the new authoritative usage boundary.

## Finding R3 — Expected B-Leg IDs Are Canonicalized as a Set

`CallUsageRecord.Seal()` sorts `ExpectedBLegIDs`; `billingTurnCollector.freezeAllocatedBLegs` also sorts the IDs.

That is correct for completeness identity, but it means slice order is intentionally not execution order.

## Finding R4 — RateCall Re-Invents Sequence

`internal/core/billing/call_rating.go` converts `[]CallLegUsageRecord` back to legacy `[]LegUsageRecord` and assigns `Seq = index + 1`.

`internal/core/billing/rating.go` uses highest `Seq` when a non-completed call has accepted evidence but no surfaced leg. Therefore current settlement can select a leg according to opaque-ID lexical order rather than execution order.

## Finding R5 — Model-Specific Customer Prices Are Computed Then Dropped

`internal/infra/billingcompose/catalog.go` can produce `[]ModelCustomerPricing` for backend/model overrides.

`JoinRatingResolver.ResolveCallRating` calls `SnapshotsFor(...)` but discards the model-pricing return value.

`CallRatingInput` does not expose model pricing, so `RateCall` ultimately uses only the default customer pricing card.

Admission and settlement can therefore price the same route using different effective cards.

## Finding R6 — Customer Resolution Is Coupled to Operator Rates

`SnapshotCatalog.SnapshotsFor` resolves both customer pricing/policy and every referenced operator-rate snapshot.

`ResolveCallRating` uses that combined method even though customer `RateCall` does not consume `OperatorRateSet`.

A missing provider COGS rate can therefore block customer settlement and leave operational exposure open.

## Finding R7 — Executor-Global Billing Collector Retains Completed Calls

`billingTurnCollector` is stored on long-lived `Executor` and contains:

- `allocatedByCall`
- `frozenByCall`
- `legTimesByCall`
- `finalizeByKey`

The post-merge tree has no normal production lifecycle that removes all completed-call entries. `evictFinalizeCache` is defined but not part of a complete call-state release protocol.

The ownership is wrong even if local delete calls could patch individual maps: call bookkeeping should die with the call.

## Finding R8 — Existing Durable Rows Need Compatibility, Not Guessing

Because pre-fix rows have no sequence and B-leg IDs are random, a migration cannot recover true ordering safely. The upgrade must preserve readability and fingerprint compatibility while refusing to invent sequence when a rating policy needs it.

## Split Decision

Two specs are preferable to one large remediation:

1. **`billing-post-usage-correctness-hardening`** — fix wrong-charge and memory-boundedness defects while preserving the current architecture.
2. **`billing-architecture-final-convergence`** — after correctness is proven, delete the old TUR/LUR bridge and remaining economic architecture.

This sequencing prevents a large deletion refactor from obscuring correctness fixes.

## Design Principles Derived from Research

- Preserve facts at the earliest authoritative boundary; never reconstruct B2BUA order later.
- Treat completeness set ordering and execution ordering as separate concepts.
- Customer and operator economics share correlation, not readiness dependencies.
- Runtime bookkeeping should be owned by request lifetime, not executor lifetime.
- Brownfield compatibility must never fabricate financial facts.
- Spec 1 may retain temporary legacy rating adapters if they are made correct; Spec 2 is responsible for deleting them.
