# Design Validation Review

## Review Method

The CDR-first design was validated as a brownfield architecture replacement against:

- root/Kiro architecture rules;
- repository `main` at `269b9e8df0e9ed476d962c2327e1794f4b74bb83`;
- current routing failover/parallel candidate planning;
- current backend sideband accounting evidence and `FinalizeBilling`;
- current usage-authority reservation/settlement store;
- runtime terminal ownership;
- token-accounting/metering/control-plane economic paths;
- all 107 acceptance criteria in `requirements.md`;
- all gaps in `gap-analysis.md`.

Validation focused on simplicity, concurrency safety, crash recovery, compatibility, and whether any old stream-time accounting responsibility would remain authoritative after migration.

## Round 1

**Decision: NO-GO**

### Issue 1: The initial estimator bounded provider cost rather than customer charge

A route can incur cost on swallowed/losing attempts that the product may choose not to charge to the customer. Reserving all provider exposure would unnecessarily lock prepaid funds; reserving only the first provider would be unsafe for pass-through/multi-attempt plans.

**Resolution**

The estimator now binds to the same versioned **customer charging policy** used after the CDR:

- surfaced-logical-turn charging reserves one logical customer maximum;
- policies that can charge multiple attempts reserve all potentially customer-chargeable legs;
- operator retry/failover cost remains post-turn operator economics.

**Traceability:** 4.2, 4.6, 4.7, 7.3–7.5.

### Issue 2: Low-balance concurrency=1 was still presented as an equal production option

Carrying two correctness mechanisms would make implementation and testing more complex.

**Resolution**

Select one mechanism: atomic outstanding holds. Dynamic concurrency=1 is rejected from the baseline design.

**Traceability:** Requirement 6, D3–D4.

### Issue 3: Final provider evidence was not sufficiently separated from client usage

If CDRs were built from frontend/canonical usage events, the design would recreate runtime usage reconciliation.

**Resolution**

Provider adapters/attempt finalization own billing evidence. Client-visible usage remains a separate wire projection.

**Traceability:** Requirement 3, 1.7, D6.

## Round 2

**Decision: NO-GO**

### Issue 1: "Post-turn processing" could still lose charges on crash

A purely in-memory callback after response completion is not sufficient.

**Resolution**

Seal and durably append the CDR at the existing terminal boundary. A tiny pending/processed/retryable/terminal-error state model drives replay-safe processing. No message bus is required.

**Traceability:** Requirement 9, D7.

### Issue 2: Stale reservations could be released while an LLM call is still running

A reservation TTL alone is unsafe.

**Resolution**

Stale cleanup requires the request/turn to be known inactive **and** the maximum request deadline plus safety grace to have elapsed. Processing failures keep funds reserved.

**Traceability:** 9.5–9.6.

### Issue 3: Actual charge could exceed the pessimistic reservation

Treating overage as a normal settlement case would invalidate the prepaid safety guarantee.

**Resolution**

`actual > reserved` is now an invariant failure: record diagnostics, block further strict-prepaid spending on the affected account/path until recovery policy resolves the condition, and never silently accept it as expected overage.

**Traceability:** 8.3–8.4, D9.

### Issue 4: Existing `metering.Fact` could remain a second billing source

Retaining it as an equal authoritative input would preserve the old architecture.

**Resolution**

Metering facts may survive only for independent telemetry/audit consumers or as a projection **from** Billing Results. They do not feed balance mutation.

**Traceability:** 10.1–10.6, 11.8, D11.

## Round 3

### Architecture Simplicity Review

**PASS**

The production path contains four conceptual operations:

1. estimate;
2. reserve;
3. record CDR;
4. process/settle.

Runtime touches only #2 before upstream and #3 at terminal.

No generic event bus, workflow engine, CQRS layer, live economic reducer, or dynamic concurrency controller is required.

### Concurrent Prepaid Safety Review

**PASS**

Invariant:

```text
remaining = funded_limit - consumed - reserved
```

All reservations are store-atomic. Every active strict-prepaid turn owns a pessimistic hold. Therefore accepted concurrent turns cannot collectively exceed funded capacity as long as final customer charge is bounded by its reservation.

Multi-process behavior is covered because store transactions, not Go-process memory, own the invariant.

### Runtime Isolation Review

**PASS**

- pricing/rating does not execute in stream receive handlers;
- balance mutation occurs only at preflight reserve and post-turn settle/release;
- provider evidence is finalized at the adapter/attempt boundary;
- billing cannot trigger retry/failover;
- canonical client usage is not billing truth.

### SOLID Review

**Single Responsibility — PASS**

- runtime executes;
- adapters finalize provider evidence;
- billing estimates/rates/settles;
- store owns atomic state;
- control-plane projects results.

**Open/Closed — PASS**

New price plans or typed customer charge policies extend the billing bounded context rather than modifying stream handlers.

**Liskov — PASS**

Balance-store implementations must satisfy one explicit atomic reservation contract. Backend evidence implementations satisfy one final-attempt evidence contract.

**Interface Segregation — PASS**

The design uses two small persistence seams plus narrow pricing/policy dependencies instead of the current broad authority service.

**Dependency Inversion — PASS**

Billing depends on normalized evidence and narrow stores, not concrete provider SDKs or database handles.

### Hexagonal Review

**PASS**

- driving adapter: runtime preflight and CDR worker;
- domain/app center: `internal/core/billing`;
- driven adapters: balance/CDR/pricing stores;
- provider SDKs remain edge-only;
- reporting is read-side projection.

### Brownfield Compatibility Review

**PASS WITH MIGRATION GATE**

Before deletion:

- preserve current client usage wire behavior;
- preserve routing/failover/parallel semantics;
- preserve provider finalization behavior;
- shadow CDR results against current accounting on representative scenarios;
- preserve non-money usage/rate-limit rules until separately migrated.

The prior `usage-accounting-architecture-convergence` spec is superseded for implementation direction.

### Testing Review

**PASS**

High-value proofs:

- pure max-cost and CDR calculation tables;
- property tests for reservation arithmetic;
- real-store concurrent reserve tests;
- cross-process/dialect store contract tests where supported;
- crash/replay settlement sequences;
- architecture tests forbidding stream-time billing ownership.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

The CDR-first design is materially simpler than both the current implementation and the superseded convergence spec. It preserves only the real-time behavior required for prepaid safety—one pessimistic compare-and-reserve before upstream work—and moves actual usage accounting/rating/settlement to completed immutable turn records.

## Implementation Gate

Implementation remains approval-gated:

1. requirements approval;
2. design approval;
3. tasks approval;
4. `ready_for_implementation: true`.

Implementation must begin with characterization and failing tests before production ownership changes.
