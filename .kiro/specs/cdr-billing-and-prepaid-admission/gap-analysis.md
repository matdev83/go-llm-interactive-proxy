# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis compares the CDR-first requirements against repository `main` at `269b9e8df0e9ed476d962c2327e1794f4b74bb83`.

The review focuses on:

- `internal/core/runtime` execution, retry, authority, terminal, and current accounting hooks;
- `internal/core/tokenaccounting`;
- `internal/core/accounting`;
- `internal/core/metering` and `pkg/lipsdk/metering`;
- `internal/core/usageauthority` and `internal/infra/usageauthority/authoritystore`;
- `pkg/lipsdk/economics`;
- backend sideband accounting evidence and `FinalizeBilling`;
- `internal/core/controlplane` economic reporting;
- routing candidate planning in `internal/core/routing`;
- the superseded `usage-accounting-architecture-convergence` spec.

Classifications:

- **Preserve** — useful existing machinery matches the CDR-first target.
- **Simplify** — useful implementation exists behind an unnecessarily broad contract.
- **Replace** — behavior remains required, but the current ownership/input model is wrong.
- **Delete** — no target-architecture responsibility remains.
- **Missing** — target capability does not exist.
- **Constraint** — compatibility/lifecycle rule that constrains migration.

## Brownfield Assets Worth Preserving

### Atomic reserve/settle storage mechanics

`internal/infra/usageauthority/authoritystore` already performs atomic clone/apply semantics, tracks `Consumed` and `Reserved`, rejects capacity exhaustion, and uses idempotent reservation/settlement source keys. The storage mechanics are valuable for concurrent prepaid safety even though the current generalized usage-authority application contract is much broader than needed.

The target should preserve or extract the **atomic money reservation primitive**, not preserve the whole current usage-authority orchestration API.

### Sideband provider billing evidence

`pkg/lipsdk/backendplugin` already separates host-only `AccountingEvidence` from client-visible canonical events and defines `FinalizeBilling`. This is close to the desired CDR boundary: provider adapters/connectors can finish one attempt's evidence without making the runtime interpret client usage events.

The target should normalize this into one final Attempt CDR evidence object.

### Pure checked accounting/rating primitives

`internal/core/accounting` and `pkg/lipsdk/economics` contain checked monetary arithmetic and versioned rating concepts. These are suitable building blocks for pessimistic estimates and post-turn rating if they are called from the billing bounded context rather than from stream handlers.

### Routing candidate plan

`internal/core/routing.ExpandFailoverGroups` already exposes ordered attempt groups and parallel legs before upstream work. This is the correct point to compute a conservative request exposure bound because the plan exists without executing providers.

### Terminal ownership

The runtime already has explicit terminal/terminal-work ownership. CDR persistence should use that terminal boundary rather than create a second execution coordinator.

## Gap Register

| ID | Severity | Class | Current finding | Required disposition |
|---|---:|---|---|---|
| G-01 | P0 | Replace | Runtime currently reinterprets usage during/after streaming for authority/customer settlement. | Move all charge/rating/settlement interpretation to sealed CDR processing. |
| G-02 | P0 | Delete | `tokenaccounting/streamusage.Reconstruct` builds billing-relevant usage from raw event streams. | Remove as a billing source; retain only if a non-billing consumer independently justifies it. |
| G-03 | P0 | Delete | Runtime `enrichUsageCost` mutates usage events with local prices. | Delete from execution path; rate only after CDR seal. |
| G-04 | P0 | Delete | Runtime tracks economic dedupe keys and remembered customer/operator usage. | Replace with final attempt evidence + CDR identity. |
| G-05 | P0 | Replace | Current monetary authority settlement accepts facts/exposure/lifecycle-stage inputs. | Settle one reservation from one Billing Result. |
| G-06 | P0 | Preserve/Simplify | Authority store already atomically subtracts reserved capacity and settles actual usage. | Extract/narrow to money reservation semantics for prepaid/credit enforcement. |
| G-07 | P0 | Missing | No first-class immutable `TurnCDR`/`AttemptCDR` contract exists. | Add one internal protocol-neutral contract. |
| G-08 | P0 | Partial | Backend plugins have sideband `AccountingEvidence` and `FinalizeBilling`, but runtime can still observe multiple raw evidence events. | Make adapter/attempt boundary expose one final billing evidence result. |
| G-09 | P0 | Missing | No single `MaxCustomerCharge` estimator is bound to the route plan and final customer charging policy. | Add deterministic pessimistic estimator. |
| G-10 | P0 | Missing | Strict prepaid admission is not expressed as one account-level compare-and-reserve against all outstanding monetary holds. | Add atomic account reservation service. |
| G-11 | P0 | Constraint | Failover/parallel routing may create multiple provider attempts; user charge policy may or may not expose those costs. | Estimate against customer charging policy, not blindly against operator retry cost. |
| G-12 | P0 | Missing | No explicit rule denies strict prepaid execution when a finite maximum charge cannot be proven. | Fail closed or require configured conservative ceiling. |
| G-13 | P0 | Missing | No simple durable post-turn CDR queue/state exists as billing handoff. | Add bounded sealed-CDR persistence and retry processing. |
| G-14 | P0 | Replace | Control-plane economics can derive reports from raw metering facts. | Read applied Billing Results/CDRs for authoritative spend. |
| G-15 | P1 | Partial | Client-facing usage events and `usage.Observer` exist but cannot preserve every billing presence semantic. | Keep as wire/telemetry only; remove from billing truth. |
| G-16 | P1 | Delete/Project | Legacy token ledger is directly written by runtime despite compatibility projection code elsewhere. | Project from CDR/Billing Result or delete after consumer inventory. |
| G-17 | P0 | Constraint | CDR persistence occurs after output may already be client-visible. | Failure must not alter execution; leave reservation held and retry terminal work. |
| G-18 | P0 | Missing | Crash between reservation and settlement needs deterministic hold recovery. | Keep hold until CDR settlement; stale cleanup only after execution deadline + grace/proof. |
| G-19 | P0 | Missing | No architecture test forbids stream handlers from owning billing/rating. | Add import/symbol ratchets. |
| G-20 | P1 | Constraint | Current `usageauthority` also owns non-money request/token/rate policies. | Do not delete unrelated quota/rate behavior blindly; separate monetary paths during migration. |
| G-21 | P1 | Partial | `metering.Fact` has strong replay/correction semantics. | Do not require it for billing; retain only where non-billing telemetry/audit still benefits. |
| G-22 | P0 | Constraint | The prior `usage-accounting-architecture-convergence` spec is present on main. | Mark this CDR-first spec as superseding its implementation direction. |
| G-23 | P1 | Missing | Low-balance concurrent safety strategy is not explicitly selected. | Select atomic pessimistic holds; reject concurrency=1 heuristic as correctness mechanism. |
| G-24 | P1 | Missing | There is no property test proving multi-process/concurrent reserve safety. | Add store contract and concurrency tests. |
| G-25 | P1 | Missing | No invariant handling exists for actual customer charge exceeding reserved max due estimate/catalog bugs. | Treat as bound violation, block further spend, surface diagnostics; never normalize as expected overage. |

## Requirements Review Round 1

**Decision: NO-GO**

The first CDR draft was too vague in three places.

### R1-A: "Reserve max cost" did not define which max

A route may contain failover and parallel legs. Reserving the sum of every possible provider bill would be unnecessarily restrictive if the customer is charged only for the surfaced logical turn; reserving only one leg would be unsafe for pass-through/multi-attempt charging policies.

**Remediation:**

- Requirements 4.2, 4.6, and 4.7 bind the estimate to the **customer charging policy**.
- Operator retry/failover cost remains an independent post-turn perspective.
- The estimator consumes the already-planned route/candidate structure and performs no provider work.

### R1-B: Concurrent prepaid safety was under-specified

A softswitch-style low-balance `concurrency=1` clamp can reduce risk but becomes heuristic when models have different maximum costs and multiple requests arrive simultaneously.

**Remediation:**

- Requirement 6 selects one correctness mechanism: every in-flight hard-enforced turn has an atomic pessimistic reservation.
- Spendable capacity includes all active reservations.
- Store-level atomicity, not an in-memory running-session count, arbitrates concurrent requests.
- The concurrency=1 threshold technique is explicitly rejected as the baseline correctness mechanism.

### R1-C: "Post-turn" did not define crash behavior

If CDR processing is simply fire-and-forget after response completion, a process crash can lose charges or release reservations incorrectly.

**Remediation:**

- Requirement 9 requires durable sealed-CDR persistence with a small processing state machine.
- Failed processing keeps the hold in place.
- Stale hold cleanup is conservative and waits for known execution expiry + grace.
- Processing/settlement is replay-safe by CDR/turn ID.

## Requirements Review Round 2

**Decision: GO FOR REQUIREMENTS QUALITY**

The revised requirements now establish:

1. one live monetary action: pre-upstream compare-and-reserve;
2. zero rating/settlement logic while streaming;
3. one immutable post-turn CDR;
4. one selected concurrent prepaid safety mechanism;
5. deterministic failure when a finite cost bound is unavailable;
6. separate customer charge and operator cost;
7. crash-safe post-turn processing without an event platform;
8. explicit deletion of stream-time economic machinery;
9. preservation of unrelated runtime/protocol safety boundaries.

## Key Brownfield Migration Principle

Do **not** build a CDR processor beside the existing stream-accounting path and leave both indefinitely.

The migration must use shadow comparisons only long enough to characterize equivalence, then remove old sources of truth. Architecture tests and task gates must make the final state mechanically provable.

## Requirements Quality Gate

**Decision: PASS**

The requirements are implementation-testable, select a single prepaid concurrency strategy, preserve current routing/stream safety, and materially simplify the ownership model.
