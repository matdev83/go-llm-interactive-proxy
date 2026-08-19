# Brownfield Design Validation

## Verdict

**GO after phase/transaction corrections.** The target architecture materially reduces upstream state translation and temporal coupling, but the first design pass required several corrections before implementation would be safe. The final design does not force every conceptual phase into a new struct, does not make context disappear, does not falsely label candidate evaluation pure, and does not centralize parallel-arm resources in one shared transaction.

The accepted design has four decisive simplification properties:

1. authoritative request facts are frozen at explicit security/preparation boundaries;
2. route facts and mutable route progress are separate, with one progress authority shared by initial and retry execution;
3. candidate rejection/failure is returned as typed state rather than pointer-out mutation;
4. B-leg/authority/backend-open resources remain under one per-attempt transaction until complete downstream handoff.

Baseline reviewed: `main` at `c3b5c872e6e48b6b9c86ea3570530b4fb094767c`.

## Validation Round 1 — Findings and Disposition

### Too many phase structs could increase translation — NO-GO / FIXED

The conceptual flow initially suggested a concrete struct for every step. Implemented literally, that could add more field-copy code than the current `preparedRequest`/`routePlanState` bags.

**Correction:** the design treats phase boundaries as normative but struct count as non-normative. Adjacent phases may share focused nested immutable values/owner references. D16 measures actual carrier/projection count and translation deletion.

### “Prepared” could imply billing facts exist too early — NO-GO / FIXED

Some immutable billing account/pricing/policy facts are stamped after later exposure admission, not at initial secure preparation.

**Correction:** D2/D5 freeze only facts established at their actual boundary. Later admission can enrich the request owner without pre-populating temporally invalid placeholders.

### Route state cannot be fully immutable — NO-GO / FIXED

`[first]`, exclusions, budgets, failure history, affinity/interleaved continuity and TTFT state change as attempts proceed.

**Correction:** D6 explicitly separates immutable `routeFacts` from mutable `routeProgress` and requires progress to converge with tranche 1 recovery ownership rather than duplicate it.

### Candidate evaluation is not pure — NO-GO / FIXED

Attempt transforms can use extension state/services; diagnostics/evidence are observable; panic isolation and interleaved shaping have staged effects.

**Correction:** D9 describes typed evaluation outcomes but explicitly permits phase-owned observable effects. The hard boundary is that resource-owning authority/B-leg/backend-stream side effects start only in D10.

### One group transaction for parallel arms would blur ownership — NO-GO / FIXED

Every parallel arm can independently allocate authority, B-leg and backend stream resources. A shared transaction could leak or misattribute losers.

**Correction:** D14 gives every arm its own attempt transaction. Group coordination chooses the winner and asks losers to rollback/terminalize exactly once.

### Pending memo/cycle effects could commit before a winner exists — NO-GO / FIXED

Interleaved shaping/planning can prepare effects that must be winner-only, especially under parallel routing.

**Correction:** D9/D15 model pending route/interleaved commit separately from evaluation/open. Authoritative winner publication is the commit boundary.

### Pointer-out removal could accidentally lose final error precedence — NO-GO / FIXED

The current pointer channels are poor architecture but they preserve real causal evidence across candidate failures.

**Correction:** D7/D13 introduce one typed failure history and one final causal-error selection authority. Existing precedence is characterized before migration.

### Context cannot simply be removed — NO-GO / FIXED

Hooks, extensions, evidence, principal/scope/session views, diagnostics and model/native bindings depend on context-shaped APIs.

**Correction:** D3 keeps context as cancellation/observability/SDK projection while removing it as a competing core business-state authority.

### Pre-stream cleanup needs ownership, not a new generic ledger — VALID / FIXED

The current `streamReturned` flag hides a real transfer boundary for request authority/A-leg cleanup, but replacing it with a generic resource framework would over-engineer the solution.

**Correction:** D4 uses one focused `preStreamGuard`/equivalent that coordinates only existing pre-handoff cleanup obligations and delegates domain semantics to existing owners.

### Secure and accounting stage order cannot be normalized for symmetry — VALID / FIXED

`BeginTurn`, route-override snapshot, secret guard, metering ingress, request authority, submit mutation, BillingCallID, keep-warm and A-leg scope each have brownfield ordering constraints.

**Correction:** D2–D5 make freeze/ownership points explicit and require Phase 1 characterization to remain the authority for exact relative ordering.

### Attempt transaction must not duplicate domain state — VALID / FIXED

A transaction containing copied B2BUA/authority state would create another owner rather than simplify ownership.

**Correction:** D10 owns references/capabilities for resources it acquired and coordinates existing domain owners. It does not reimplement usage-authority/B2BUA state machines.

### Tranche 2 must not depend on unmerged private names from tranche 1 — VALID / FIXED

The implementation order is chronological, but the spec PRs need independent reviewability.

**Correction:** D1/D11 define semantic ownership transfer. Exact private downstream type names are adaptation details at one handoff constructor.

### Selector normalization would contaminate the refactor — VALID / FIXED

The routing planner has a separate sticky/normal traversal simplification opportunity, but mixing it into this pipeline change would make parity failures difficult to localize.

**Correction:** D8/D18 consume current selector semantics and explicitly defer grammar/traversal convergence.

## Validation Round 2 — Architecture Checks

### Chronological prerequisite — PASS

The design requires tranche 1 to be green before implementation, but does not require its exact internal names. This avoids simultaneously changing both sides of the lifecycle boundary.

### Security identity boundary — PASS

Authoritative principal/session/workspace/A-leg facts are frozen after current secure-session sequencing. Client-forged session/A-leg values remain non-authoritative.

### Route-override snapshot — PASS

The snapshot/barrier remains at the current point and is carried as a fact rather than re-read live.

### Request metering/authority cleanup — PASS

Metering-before-submit and request-authority release on later pre-stream failure remain explicit. `preStreamGuard` replaces only integration ownership, not domain logic.

### Context authority — PASS

Context remains fully usable for extension APIs and cancellation, but typed phase values are the core business authority. No requirement forces a public API rewrite.

### Prepared request validity — PASS

The final prepared value is not designed as a broad partially initialized bag. Facts unavailable until later admission are represented later.

### Route-state ownership — PASS

Immutable route facts and mutable progress are distinct. Progress converges with the recovery owner from tranche 1, preventing initial/retry divergence.

### `attemptOpenParams` removal — PASS by design

No replacement mega-input is required. D16 explicitly gates deletion and pointer-out absence.

### Failure history/error precedence — PASS

Typed history preserves current transport/capability/admission/context/transform/parallel causal precedence.

### Candidate evaluation effects — PASS

The design is honest about extension/evidence effects but keeps resource acquisition outside evaluation. This gives a meaningful rollback boundary without misleading purity claims.

### Attempt transaction — PASS

Authority/B-leg/backend stream remain locally owned until handoff and are rolled back/terminalized together on failure. Existing domain state machines remain authoritative.

### Parallel ownership — PASS

Per-arm transactions provide clear loser cleanup and attribution. Group coordination does not own individual arm resources directly.

### Interleaved commit — PASS

Pending memo/cycle/route effects are separate from evaluation and commit only for the authoritative winner.

### Initial/retry convergence — PASS

D12 gives both paths one typed pipeline and one route-progress authority. Retry is an explicit mode/state, not a second implementation.

### Downstream handoff — PASS

One complete opened attempt is transferred; field-by-field reconstruction into a stream God object is prohibited.

### Public/ABI/routing scope — PASS

No public API, selector grammar, provider-specific behavior, billing/security domain redesign or backend ABI change is needed.

### Framework avoidance — PASS

No generic stage engine/container/service locator is required. Explicit private functions/types preserve debuggability of security-sensitive order.

## Requirement-to-Design Trace

| Requirement | Primary design coverage |
|---|---|
| R1 prerequisite/baseline | D1, D16, D17, testing strategy |
| R2 orchestration/public behavior | principles, D3, D12, D16, D18 |
| R3 identity-bound phase | D2, D3 |
| R4 request preparation/ownership | D4, D5 |
| R5 route facts/progress | D6, D8, D12 |
| R6 remove params/pointer-outs | D7, D12, D13, D16 |
| R7 candidate plan/evaluation | D8, D9, D13 |
| R8 attempt transaction | D10, D11 |
| R9 parallel/interleaved | D14, D15 |
| R10 context/downstream handoff | D1, D3, D11, D12 |
| R11 simplification/TDD | D16–D18, testing strategy |

## Design Rule Coverage Check

- D1 chronological semantic dependency — actionable.
- D2 identity-bound freeze — actionable.
- D3 context projection — actionable.
- D4 pre-stream ownership guard — actionable.
- D5 frozen prepared turn — actionable.
- D6 route facts/progress — actionable.
- D7 typed failure history — actionable.
- D8 candidate plan — actionable.
- D9 candidate evaluation outcome — actionable.
- D10 attempt transaction — actionable.
- D11 complete opened-attempt handoff — actionable.
- D12 common initial/retry pipeline — actionable.
- D13 failure/rejection flow — actionable.
- D14 parallel per-arm ownership — actionable.
- D15 winner-only pending commit — actionable.
- D16 architecture ratchets — actionable.
- D17 phased migration — reflected in tasks.
- D18 deferred scope — architecture-gated.

All D1–D18 rules can be implemented after tranche 1 without requiring selector normalization or another architecture project.

## Simplification Review

The final design rejects:

1. a universal `TurnState` replacing existing bags;
2. mandatory struct-per-phase ceremony;
3. pointer-out failure state hidden behind another wrapper;
4. treating context as the sole business data store;
5. pretending candidate evaluation is completely pure;
6. a generic resource transaction/ledger replacing B2BUA/authority owners;
7. one parallel-group resource owner for all arms;
8. committing interleaved effects before winner selection;
9. separate initial and retry attempt pipelines;
10. selector grammar/normalization work in the same change;
11. a generic stage/pipeline framework;
12. success criteria based only on file size.

The design is a GO only if implementation actually deletes old translations/bags. If it layers phase wrappers on top of retained `attemptOpenParams`/`routePlanState` mirrors, final certification is NO-GO.

## Implementation Risks to Pin With Tests

- secure BeginTurn moves after a stage that consumes client-forged continuity;
- route override is re-read after the snapshot point;
- metering captures the post-submit mutated request instead of ingress;
- request authority leaks on pre-stream failure;
- keep-warm/A-leg lifecycle ordering changes unintentionally;
- prepared type contains placeholders that are silently invalid until later stages;
- initial and retry route progress diverge;
- error precedence changes when pointer-outs are removed;
- transform exclusion accidentally allocates attempt resources;
- attempt transaction leaks B-leg/authority/stream on partial failure;
- transaction and downstream owner both believe they own cleanup after handoff;
- parallel loser commits memo/cycle state;
- loser cleanup races winner publication;
- candidate panic isolation/diagnostics are bypassed;
- context projections go stale relative to typed facts;
- `attemptOpenParams` is replaced with an equally broad `openRequest` mega-bag;
- final code adds more phase-copy assignments than it deletes.

## Final Gate

**GO FOR TASK GENERATION.** The corrected design provides explicit authority/rollback boundaries and a measurable deletion target while preserving brownfield security, accounting, routing and parallel/interleaved semantics. Its chronological dependency on tranche 1 is clean and semantic, so the two spec PRs remain independently reviewable.

Implementation remains approval-gated by `spec.json` and must not begin until tranche 1's downstream ownership/race/parity gates are green.
