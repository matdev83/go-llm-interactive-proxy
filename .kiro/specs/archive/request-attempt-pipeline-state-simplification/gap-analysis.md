# Brownfield Requirements Gap Analysis

## Result

**PASS after requirements corrections.** The initial proposal to replace broad state bags and pointer-out mutation with typed phase products is directionally correct, but brownfield analysis found several sequencing and ownership constraints that must remain normative. The corrected requirements now distinguish authoritative phase facts from context projections, immutable route facts from mutable route progress, phase-owned observable effects from attempt-resource side effects, and pending versus committed interleaved/parallel transitions.

The largest correction is that the target cannot be a simplistic “pure pipeline.” Several existing stages are intentionally side-effectful and safety-wrapped. The simplification is therefore about **explicit authority, typed outcomes and rollback ownership**, not pretending every phase is pure.

Baseline: `main` at `c3b5c872e6e48b6b9c86ea3570530b4fb094767c`.

## Existing Brownfield Facts

- `Executor.Execute` is already a compact orchestration shell and should remain so.
- `preparedRequest` is a useful named phase product but evolves across multiple lifecycle moments and contains facts established at different times.
- `prepareSubmitAndALegSecure` establishes authoritative principal/session/workspace/A-leg identity before later submit/policy stages.
- route-override authority is deliberately snapshotted before route planning and has a deterministic barrier used by tests.
- frontend ingress metering is captured before submit mutation; request authority is then admitted and must be released if later preparation fails before handoff.
- BillingCallID is one logical-invocation identity shared across retries/failover/parallel B-legs.
- `routePlanState` mixes once-computed route facts with mutable retry/failure/interleaved history.
- `attemptOpenParams` mixes inputs with pointer-out mutation of failure/progress state.
- candidate attempt transforms and policy/evidence operations are observable and may be side-effectful; candidate evaluation is not wholly pure.
- candidate admission is safety/panic isolated and has explicit capability/transport/model/context semantics.
- B-leg allocation, attempt authority and backend open create resources/side effects requiring exact cleanup.
- parallel arms are independent resource attempts; loser cleanup and winner-only memo/route transition commit are required.
- no-eligible error precedence carries meaningful causal information across multiple rejected candidates.
- recv replacement must preserve the same route progress/attempt budget/interleaved continuity as initial open.

## Gaps and Required Corrections

### 1. `IdentityBoundTurn` must not falsely imply later admission

An early design sketch could freeze too much at the secure-session phase and label the result “PreparedTurn,” suggesting request authority, billing exposure or final transformed baseline already exists.

**Correction:** Requirement 3 explicitly limits the identity-bound product to facts actually established by that point. Request-admission/preparation is a separate boundary in Requirement 4.

### 2. Secure `BeginTurn` ordering is a security invariant

The proxy must not let client-forged session/A-leg identity reach submit/later policy stages as authoritative. `BeginTurn` establishes the authoritative session/turn and then A-leg continuity is fetched.

**Correction:** Requirements 3.2–3.6 pin this ordering. Refactoring into smaller helpers must not reorder it.

### 3. Route-override snapshot timing is intentional

The route override is snapshotted after authoritative A-leg establishment and before submit/request stages and route-plan construction, with an existing deterministic barrier.

**Correction:** Requirement 3.7 makes this snapshot a typed identity-bound fact and preserves the barrier. Later attempts use that snapshot rather than live re-read.

### 4. Metering-before-submit and request-authority cleanup are accounting invariants

Frontend ingress metering captures the request before submit mutation. Request authority is admitted afterward and explicitly released if a later prepare stage fails.

**Correction:** Requirements 4.1–4.3 preserve order; 4.7–4.8 replace the mutable `streamReturned` condition with one explicit pre-stream ownership guard instead of changing cleanup behavior.

### 5. Keep-warm foreground interruption and A-leg scope have deliberate points

`BeginRealTurn` and A-leg lifecycle start are not arbitrary convenience calls. Moving them merely to make phases symmetrical could change maintenance/foreground behavior and cleanup ownership.

**Correction:** Requirements 4.5–4.6 preserve current semantic ordering; the final design models ownership transfer rather than moving these calls for aesthetic reasons.

### 6. Billing exposure facts are not all available at preparation freeze

Billing account/pricing/charge-policy references may be stamped after successful exposure authorization, later than some other prepared-request facts.

**Correction:** Requirement 4.9 prohibits phase names/types from pretending later billing facts exist early. The design must model late-established immutable facts at their true boundary or attach them to the request owner when admitted.

### 7. Route state cannot be entirely immutable

`[first]` consumption, exclusions, budgets, failure history, affinity evolution and thinker cycle state legitimately change as attempts proceed.

**Correction:** Requirement 5 explicitly splits immutable route facts from mutable route progress. It also requires convergence with tranche 1's recovery owner so the split does not create duplicate progress state.

### 8. Removing pointer-out fields requires preserving error precedence

The pointer fields in `attemptOpenParams` are architecturally poor, but they carry real accumulated evidence used to surface the most useful final error.

**Correction:** Requirements 6.4–6.6 require one typed failure history/outcome with the same precedence. Pointer-out mutation is deleted without deleting semantics.

### 9. Candidate evaluation is not a pure function

Attempt transforms can access extension services/state, diagnostics/evidence are emitted, and panic isolation is observable. Calling the phase “pure evaluation” would create misleading expectations and pressure to hide effects.

**Correction:** Requirement 7.8 explicitly allows phase-owned observable effects while distinguishing them from attempt-resource ownership. The design uses typed outcomes rather than false purity.

### 10. Candidate transform exclusions are not backend-open rollback

An extension transform may exclude a candidate before any B-leg/authority/backend stream exists. Treating every rejection as an attempt transaction would allocate or clean resources too early.

**Correction:** candidate evaluation returns rejection before the attempt transaction begins. The transaction starts only when resource-owning attempt work is needed.

### 11. B-leg/authority/backend open need one rollback boundary

Current open paths have several early exits. A refactor that returns individual resources to the caller before open succeeds would spread cleanup responsibility further.

**Correction:** Requirement 8 gives these side effects one pre-handoff transaction owner. Success transfers the complete opened attempt; failure/loser cleanup remains local and exactly once.

### 12. Parallel arms need independent transactions

Parallel candidate arms can each allocate B-leg/authority/backend resources. A shared group transaction would make loser cleanup and attribution ambiguous.

**Correction:** Requirement 9.1 requires one transaction per arm, with group-level winner selection above them.

### 13. Interleaved memo/cycle effects have a winner-only commit boundary

Parallel and thinker/executor shaping may prepare pending memo/route transitions. Committing during evaluation would let losing arms mutate logical request continuity.

**Correction:** Requirements 7.9 and 9.4–9.5 require pending transitions and explicit winner commit after authoritative open.

### 14. Context remains required even when it stops being business authority

Existing hooks, extension services, principal/scope/session views, evidence, model/native bindings and diagnostics consume context values.

**Correction:** Requirement 10 preserves context as cancellation/observability/compatibility projection. The refactor removes business-state rediscovery, not the context API.

### 15. Initial and retry paths cannot diverge while tranche 1 recovery is authoritative

If this spec creates a new initial-only route progress object while recv replacement keeps a different recovery owner, the state duplication becomes worse.

**Correction:** Requirements 5.4–5.5 and 6.7 require one route-progress authority and one typed initial/retry pipeline. Integration is semantic so exact tranche 1 private names are not required.

### 16. Selector normalization is tempting but orthogonal

The current planner has duplicated sticky/normal grammar traversal, but changing selector interpretation while changing state/attempt ownership would expand semantic risk and review scope.

**Correction:** Requirements 5.10 and 11.12 explicitly defer selector grammar/sticky normalization. This spec consumes current planner semantics.

### 17. Direct test construction can perpetuate broad bags

Tests may instantiate `preparedRequest`, `routePlanState` or `attemptOpenParams` directly. Retaining every old field for fixture convenience would prevent meaningful simplification.

**Correction:** tasks must migrate tests to focused builders/phase fixtures and architecture gates shall require deletion of obsolete bags rather than compatibility preservation.

### 18. State-product proliferation can be as bad as one mega-bag

The conceptual target diagram shows multiple phase products. Implementing each arrow as a large struct with mostly copied fields could increase translation code.

**Correction:** requirements are about explicit freeze/ownership boundaries, not a mandatory struct count. Adjacent phases may use focused nested immutable values or owner references when that reduces copying. Final state-flow/projection metrics are acceptance evidence.

## Brownfield Compatibility Matrix

| Existing subsystem / authority | Required treatment |
|---|---|
| `Executor.Execute` | remains explicit compact orchestrator |
| secure-session `BeginTurn` | unchanged authority/order |
| principal/scope/workspace resolution | unchanged semantics; facts become explicit |
| route-override snapshot/barrier | unchanged point/behavior |
| metering ingress | unchanged pre-submit semantic point |
| request authority | unchanged admit/release authority |
| BillingCallID/billing exposure | unchanged logical identity and convergence semantics |
| B2BUA A-leg/B-leg | unchanged identity/lifecycle authority |
| extension hooks/transforms | unchanged APIs/order; context projection retained |
| model registry/catalog/native views | request-bound/pinned behavior retained |
| routing selector/planner | unchanged grammar/semantics |
| affinity / `[first]` / budgets | one mutable route-progress authority |
| capability/transport admission | unchanged safety/negotiation semantics |
| `attemptOpenParams` | deleted |
| parallel arms | per-arm attempt transaction; current behavior retained |
| interleaved thinking | pending/winner commit made explicit; domain unchanged |
| tranche 1 downstream owner | receives complete opened attempt via semantic handoff |
| public SDK/config/backend ABI | unchanged |

## Corrected Required Invariants

1. Authoritative secure-session/A-leg identity is established before later request stages consume it.
2. Route-override snapshot timing and metering/request-authority ordering remain unchanged.
3. Pre-stream request resources have one cleanup owner until handoff.
4. Typed phase facts—not arbitrary context values—are authoritative for core business decisions.
5. Immutable route facts and mutable route progress are distinct.
6. Initial and retry execution share one route-progress authority and typed open pipeline.
7. `attemptOpenParams` and pointer-out failure mutation disappear.
8. Failure/error precedence survives in one explicit typed history/outcome.
9. Candidate evaluation may have observable phase effects but does not own B-leg/authority/backend resources before the attempt transaction.
10. One attempt transaction owns resource side effects until complete downstream handoff.
11. Parallel arms own independent transactions; losers clean exactly once.
12. Interleaved/route pending transitions commit only for the authoritative winner.
13. No selector grammar, billing/security domain, extension API or backend ABI redesign is introduced.
14. Final state/projection/translation surface must shrink materially.

## Requirements Correction Status

The final `requirements.md` incorporates all material brownfield corrections above. Requirements 3–4 make security/admission freeze points explicit; Requirement 5 prevents false immutability and duplicate recovery state; Requirements 6–7 replace pointer-out mutation without losing error/effect semantics; Requirements 8–9 provide exact resource and winner/loser ownership; Requirement 10 preserves context compatibility while removing it as a competing business authority; Requirement 11 makes actual state-flow simplification measurable.

**Requirements quality gate: PASS.**
