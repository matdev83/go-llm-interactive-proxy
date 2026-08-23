# Implementation Plan

## Execution Rules

- Implement this specification only after `turn-recv-terminal-ownership-simplification` has a green downstream ownership/race/parity certification. Depend on its semantic handoff contract, not exact private type names.
- Follow TDD: ordering/state-flow/error/rollback characterization and architecture RED gates precede production state migration.
- Keep every task independently reviewable and limited to no more than five concrete actions.
- Preserve secure-session, route-override, metering, request-authority, billing, routing, extension, B2BUA, parallel/interleaved, capability/transport, backend and downstream terminal semantics.
- Delete obsolete state carriers/translations in the same phase that replaces their authority; do not leave permanent compatibility bags.
- Do not broaden the implementation to selector grammar normalization, generation feature-surface projection, billing/security domain redesign, public SDK/ABI work, or a generic stage/DI/resource framework.

## Phase 1 — Freeze the Existing State Flow and Semantic Order

### Task 1.1 — Verify tranche 1 prerequisite and capture the state-carrier baseline

- Verify the downstream tranche exposes a certified semantic handoff for immutable request facts, current opened-attempt ownership and one route/recovery progress authority; record the actual private integration seam without making its names normative.
- Inventory authoritative/mirrored fields across `lipapi.Call`, request context, `preparedRequest`, `routePlanState`, `attemptOpenParams`, `attemptOpenResult` and downstream assembly.
- Count direct field-copy/projection assignments, context business-state re-reads, initial/retry translation sites, pointer-out fields and pre-handoff resource-cleanup sites.
- Add RED target assertions requiring `attemptOpenParams` deletion, one route-progress authority, one pre-handoff attempt-resource owner and material projection/translation reduction.
- Record a machine-readable baseline for final before/after certification.

_Requirements: 1.1–1.3, 1.9–1.10, 10.5–10.7, 11.1–11.3, 11.7, 11.11_

_Design: D1, D16, D17_

_Validation: the exact current state-flow debt and tranche 1 integration seam are captured before production migration._

### Task 1.2 — Freeze secure preparation, admission, context, and cleanup ordering

- Add a deterministic sequence fixture covering trace/principal/scope, session-open/workspace, secure `BeginTurn`, authoritative A-leg fetch, route-override snapshot/barrier, secret guard, metering ingress, request authority, submit/request stages, keep-warm, BillingCallID and A-leg scope.
- Add failure injection at each resource-owning preparation point and prove request authority/A-leg cleanup and last-good session behavior match current semantics.
- Characterize which facts are typed/local versus projected into context and which later core decisions currently re-read context.
- Pin client-forged versus proxy-authoritative session/A-leg behavior and workspace fail-open/fail-closed mapping.
- Pin current tracing/decision-evidence and extension-stage visibility sufficiently to detect accidental reordering or stale context projection.

_Requirements: 1.4, 2.2–2.6, 3.1–3.10, 4.1–4.6, 10.1–10.4_

_Design: D2–D5, testing strategy_

_Validation: security/accounting/order/cleanup invariants are explicit and green before freeze-point types are introduced._

### Task 1.3 — Freeze route progress, initial/retry parity, and final error precedence

- Characterize compiled selector/failover/request-size/affinity/RNG/budget facts separately from exclusions, `[first]`, TTFT consumption, failure history and interleaved cycle mutation.
- Run equivalent initial-open and recv-replacement scenarios and assert they advance the same logical route progress under identical injected RNG/health/affinity conditions.
- Add a table of no-eligible outcomes pinning transport, capability, admission, context-limit, transform-exclusion and parallel-failure causal precedence.
- Characterize route-override snapshot use, preferred candidates, sticky fallback, and request-bound model/native-view behavior across replacement/reload.
- Add RED structural assertions that final implementation has one progress/failure-history authority rather than route-plan plus recv copies.

_Requirements: 1.5–1.6, 5.1–5.10, 6.4–6.8, 9.7–9.8_

_Design: D6–D8, D12–D13, D16_

_Validation: route semantics and error precedence are frozen independently of the current state-bag representation._

### Task 1.4 — Freeze candidate evaluation, attempt resources, parallel arms, and winner commits

- Characterize candidate clone/interleaved shaping, attempt transforms/exclusions, backend lookup, capability/transport/model/context admission, safety panic mapping and diagnostics/evidence order.
- Add failure injection around attempt authority, B-leg allocation/registration, backend open and post-open pre-handoff work, recording exactly which resources are cleaned/retained on each exit.
- Add scheduling-controlled parallel tests for handicaps, winner selection, loser cancellation/terminalization, aggregate failure and exact B-leg/authority/stream cleanup.
- Characterize pending interleaved memo/cycle/route effects and prove only the current winning arm/candidate commits them.
- Record the current `attemptOpenResult` to stream-assembly field reconstruction as a RED deletion target.

_Requirements: 1.7–1.8, 7.1–7.11, 8.1–8.11, 9.1–9.10_

_Design: D8–D11, D14–D16_

_Validation: candidate effects, attempt-resource ownership and parallel/interleaved commit behavior are behaviorally pinned._

## Phase 2 — Establish Identity and Prepared-Request Freeze Points

### Task 2.1 — Introduce the identity-bound turn product

- Add a private immutable identity-bound result containing stable trace/call identity, cloned authoritative call, principal/scope, workspace, secure-session/turn facts, authoritative A-leg/continuity facts and route-override snapshot.
- Refactor secure preparation so each fact is written once at its current semantic point and later core preparation consumes this typed result rather than re-deriving identity from client input/context.
- Preserve session-open/workspace/BeginTurn/A-leg/snapshot barrier order and all existing denial/evidence/diagnostic behavior.
- Add focused construction invariants rejecting temporally impossible partial identity-bound values in production paths.
- Make Phase 1 identity/security tests green without introducing later admission/route/backend placeholders.

_Requirements: 3.1–3.10, 11.4–11.5_

_Design: D2, D17_

_Validation: one explicit phase product is authoritative for proxy-bound identity/session/workspace/A-leg facts._

### Task 2.2 — Make context a projection of identity/preparation facts

- Add focused projector functions from identity/prepared facts into the existing context-based principal/scope/session/workspace/evidence/diagnostic seams.
- Replace later core business re-reads of already-authoritative identity facts with typed access while retaining context use at extension/SDK/external boundaries.
- Add tests that mutate/omit irrelevant caller context values and prove core identity decisions still use the typed authority while hooks see compatible projected views.
- Preserve cancellation/deadline propagation, tracing span parentage and existing safety/decision evidence behavior.
- Tighten the Phase 1 context re-read inventory for identity facts to the intended external/compatibility allowlist.

_Requirements: 2.4–2.6, 3.3, 3.8–3.10, 10.1–10.4, 10.10_

_Design: D3, D16, D17_

_Validation: context remains fully compatible but no longer competes with typed identity facts as core business authority._

### Task 2.3 — Introduce explicit pre-stream ownership/cleanup transfer

- Add a focused package-private ownership guard for admitted request authority and A-leg-scope cleanup obligations that currently depend on `streamReturned`/deferred temporal state.
- Wire request-authority admission, A-leg scope start and their current failure cleanup into that guard without reimplementing domain state machines.
- Add exactly-once handoff/close behavior so successful downstream ownership transfer disables pre-stream cleanup while every earlier failure releases/cancels/ends current resources.
- Preserve metering-before-submit, keep-warm, BillingCallID and submit/request-stage ordering from Phase 1 characterization.
- Delete `streamReturned`-style mutable cleanup authority as the guard becomes authoritative.

_Requirements: 4.1–4.8, 10.9, 11.3–11.5_

_Design: D4, D17_

_Validation: pre-stream request cleanup has one explicit owner/transfer boundary with unchanged domain behavior._

### Task 2.4 — Freeze one route-ready prepared turn

- Introduce/refine a private prepared-turn value containing the immutable attempt baseline, final request-wide views/route preferences, stable metering/request/billing owner references and identity-bound facts actually established by the route-ready point.
- Preserve BillingCallID as one logical-invocation identity while leaving billing account/pricing/policy facts to their true later exposure-admission boundary.
- Replace broad partially initialized `preparedRequest` mutations with construction of valid typed values/owners and migrate direct-construction tests to focused builders.
- Remove obsolete prepared fields/context mirrors once their replacement authority is green; retain only state genuinely needed for trace finalization/ownership where it belongs.
- Re-run all Phase 1 preparation/failure/cleanup tests before changing route/candidate internals.

_Requirements: 4.3–4.11, 10.1–10.4, 11.4–11.5_

_Design: D4–D5, D17_

_Validation: route/open code consumes one valid prepared request boundary rather than a temporally half-initialized bag._

## Phase 3 — Converge Route State and Candidate Outcomes

### Task 3.1 — Split immutable route facts from the single mutable route-progress authority

- Refactor route compilation to produce immutable selector/failover/request-size/affinity/RNG/limit facts and initialize the route-progress/recovery owner established by tranche 1.
- Move exclusions, attempt/TTFT consumption, `[first]`, interleaved cycle state and failure history out of broad route-plan storage into that one mutable progress authority.
- Route initial and replacement execution through the same progress object and remove duplicate recv/route-plan state mirrors as they become unused.
- Preserve route override snapshot, sticky/preferred/weighted/parallel/thinker behavior and request-bound model/native views exactly.
- Keep selector parsing/grammar and current routing planner functions unchanged.

_Requirements: 5.1–5.10, 10.5–10.8, 11.4_

_Design: D6, D8, D12, D17–D18_

_Validation: immutable route configuration and mutable attempt history are visibly separate with one initial/retry authority._

### Task 3.2 — Replace pointer-out failure mutation with one typed history

- Introduce one typed failure-history value owned by route progress for capability, transport, admission, context-limit, transform-exclusion and parallel failure evidence.
- Refactor planning/evaluation call boundaries to return/update typed rejection results instead of taking pointers to caller-local failure fields.
- Centralize final no-eligible causal-error selection over that history and make the complete Phase 1 precedence matrix green.
- Add architecture tests rejecting pointer-out attempt-pipeline inputs while allowing documented owner/service references that are not output channels.
- Remove corresponding pointer fields and update logic from the old parameter path as each caller migrates.

_Requirements: 6.2, 6.4–6.6, 6.9, 7.11, 9.7, 11.1–11.4_

_Design: D7, D13, D16–D17_

_Validation: all candidate failure evidence has one typed authority and no callee mutates six independent caller outputs._

### Task 3.3 — Introduce typed candidate plans and evaluation outcomes

- Add a typed candidate/group plan over the existing routing planner, including pending route/interleaved transition facts but no B-leg/authority/backend-stream resources.
- Add accepted/rejected candidate evaluation outcomes covering baseline clone, interleaved shaping, attempt transforms, backend resolution, route identity pinning and capability/transport/model/context admission.
- Preserve extension side effects, diagnostics/evidence and panic isolation explicitly; do not force candidate evaluation into a false pure-function abstraction.
- Route candidate exclusion/rejection back into the single route progress/failure history with unchanged budget/eligibility behavior.
- Add focused tests proving rejected evaluation creates no attempt transaction resources and accepted evaluation contains only facts required to open the attempt.

_Requirements: 6.3–6.5, 7.1–7.11_

_Design: D8–D9, D13, D17_

_Validation: planning/evaluation boundaries are explicit typed outcomes without resource ownership or pointer-out mutation._

### Task 3.4 — Introduce one typed open-next coordinator for initial and retry modes

- Add one private `openNext`/equivalent coordinator consuming prepared facts, route facts/progress and explicit initial/retry mode rather than a reconstructed universal parameter bag.
- Wire the initial Execute path and tranche 1 replacement opener seam to this same coordinator behind temporary adapters while attempt resource internals are migrated in Phase 4.
- Prove both callers advance the same route progress/failure history and preserve retry-only thinker/memo suppression semantics.
- Reduce the old `attemptOpenParams` construction sites to one temporary internal compatibility translation, preventing new fields from being added there.
- Add an architecture RED gate requiring that translation/type to disappear in Phase 5.

_Requirements: 5.4–5.6, 6.1–6.3, 6.7–6.10, 10.5–10.8_

_Design: D12–D13, D16–D17_

_Validation: one semantic initial/retry pipeline exists before resource ownership is moved, with old params isolated for deletion._

## Phase 4 — Establish Attempt Transactions and Complete Downstream Handoff

### Task 4.1 — Introduce one attempt transaction for authority/B-leg/backend resources

- Add a private attempt transaction that begins only after candidate evaluation accepts and owns attempt authority/reservation, B-leg allocation/registration, backend managed stream and associated pre-handoff cleanup capabilities.
- Move current partial-failure cleanup branches into idempotent transaction rollback/terminalization using existing usage-authority/B2BUA/terminal owners rather than copied state.
- Preserve candidate admission/backend panic isolation, attempt logging/evidence and billing/usage ordering at their current semantic boundaries.
- Add fault-injection tests for every acquisition/open step proving no resource leak, double cleanup or cleanup of a resource never acquired.
- Make a successful transaction transferable exactly once and inert after ownership handoff.

_Requirements: 8.1–8.9, 11.3, 11.9_

_Design: D10, D17_

_Validation: all pre-handoff B-leg/authority/backend-stream side effects have one transaction owner and exact rollback semantics._

### Task 4.2 — Replace partial open results with one complete downstream handoff

- Define the complete semantic opened-attempt handoff required by tranche 1 and construct it directly from a successful attempt transaction.
- Transfer backend stream, B-leg/candidate, attempt authority and necessary pending commit/attempt-local facts in one ownership operation rather than field-by-field downstream reconstruction.
- Transfer pre-stream request ownership at the same successful logical boundary and prove upstream guards/transactions become inert afterward.
- Remove downstream assembly translations that rebuild attempt ownership from `attemptOpenResult` plus route/prepared fields as the new handoff is adopted.
- Add double-handoff, failed-handoff and downstream-construction failure tests proving exactly one side owns cleanup at every point.

_Requirements: 8.4–8.11, 10.5–10.9, 11.3–11.4_

_Design: D10–D12, D17_

_Validation: one coherent opened-attempt ownership package crosses the upstream/downstream boundary exactly once._

### Task 4.3 — Give every parallel arm an independent transaction

- Convert parallel candidate opening so each arm owns an independent attempt transaction with its own B-leg, authority and backend stream lifecycle.
- Preserve handicap/winner/cancellation behavior while making group coordination publish exactly one winning handoff and invoke rollback/terminalization on every loser.
- Feed loser/arm failures into the existing aggregate parallel failure history without allowing group code to manually close individual resource fields.
- Add scheduling tests for winner publication racing loser open completion/cancellation and verify no transaction remains live after group resolution.
- Preserve sticky affinity/preferred candidate and attempt evidence attribution for each arm.

_Requirements: 8.3–8.9, 9.1–9.3, 9.7–9.10, 11.9_

_Design: D14, D17_

_Validation: parallel routing has precise per-arm resource ownership and exactly-once loser cleanup._

### Task 4.4 — Make interleaved/route pending effects winner-committed and complete initial/retry convergence

- Represent pending memo/cycle/route transitions separately from candidate/transaction resources and commit them only after a non-parallel attempt or parallel winner becomes authoritative.
- Prove losing/failed arms cannot consume memo budget, advance cycle continuity or persist winner-only route state while preserving thinker/executor suppression semantics.
- Route both initial Execute and recv replacement through the completed candidate evaluation/attempt transaction/handoff pipeline with no behavioral branch duplication.
- Remove the temporary tranche 1 replacement-open translation to old parameter bags once both paths use the typed pipeline.
- Re-run Phase 1 initial/retry/parallel/interleaved/error-precedence matrices under targeted race/scheduling coverage.

_Requirements: 6.7–6.8, 7.9, 9.4–9.10, 10.8, 11.9_

_Design: D12, D14–D15, D17_

_Validation: route/interleaved progress commits only at the authoritative winner boundary and initial/retry logic is one implementation._

## Phase 5 — Delete Old Bags and Certify the Simplification

### Task 5.1 — Delete `attemptOpenParams` and obsolete prepared/route/open state translations

- Delete `attemptOpenParams` and all production construction/translation code, pointer-out fields and compatibility helpers once the typed pipeline is authoritative.
- Delete or materially shrink `routePlanState`, `attemptOpenResult` and `preparedRequest` portions that only duplicate the new route/progress/handoff/prepared authorities.
- Remove obsolete context business re-reads and field-by-field downstream assembly copies identified by the Phase 1 baseline.
- Migrate tests away from direct construction of retired broad bags into focused phase/owner fixtures.
- Confirm `Executor.Execute` remains compact and explicit rather than absorbing deleted implementation details.

_Requirements: 2.1, 6.1–6.10, 8.10–8.11, 10.3–10.10, 11.4–11.5_

_Design: D3, D11–D12, D17_

_Validation: the old parameter/state-bag topology is removed rather than wrapped or renamed._

### Task 5.2 — Activate state-flow, pointer-out, ownership, and scope ratchets

- Make the Phase 1 state-carrier/projection inventory green with material reductions in overlapping bags, copy assignments, context business re-reads, initial/retry translation layers and pre-handoff cleanup sites.
- Activate AST/architecture gates proving `attemptOpenParams` absence, no pointer-out attempt inputs, one route-progress/failure-history authority and one pre-handoff attempt transaction owner.
- Reject universal mutable turn bags, generic stage/pipeline registries, service locators, DI/reflection frameworks and selector grammar changes introduced by this refactor.
- Require net deletion in the affected production state-handoff/parameter-translation surface unless final design review records a concrete stronger invariant that cannot be expressed more simply.
- Tighten architecture budgets/allowlists only in the simplifying direction and delete temporary migration adapters not required by the final semantic handoff.

_Requirements: 1.9–1.10, 2.7–2.8, 6.9–6.10, 11.1–11.7, 11.12_

_Design: D16–D18_

_Validation: machine-readable architecture evidence proves fewer authorities/translations, not merely more named types._

### Task 5.3 — Run race, domain, routing, protocol, and repository regression gates

- Run targeted race/scheduling coverage for pre-stream guard transfer, route-progress mutation, attempt transaction rollback/handoff, parallel winner/loser resolution and downstream attempt ownership.
- Run runtime Executor/retry, routing/affinity, B2BUA, usage-authority/accounting, billing, secure-session, model/catalog, extension, prompt/interleaved and backend suites.
- Run frontend/protocol conformance/TCK/parity suites proving canonical request/event, negotiation, failover, tool/usage, cancellation and OpenResponses behavior are unchanged.
- Run formatting, vet/lint, architecture, quality/parity and supported race gates without weakening assertions or skips.
- Record platform limitations accurately and do not claim unrun evidence.

_Requirements: 2.2–2.10, 8.3–8.9, 9.1–9.10, 11.8–11.10_

_Design: testing strategy, D16–D17_

_Validation: all affected domain/concurrency/protocol/repository gates are green after the state-topology refactor._

### Task 5.4 — Perform final state-flow review and close tranche 2

- Compare the final state-flow graph to Phase 1 and issue GO/NO-GO based on authoritative representation count, projection/copy paths, pointer-out removal, route-progress ownership, cleanup/transaction boundaries, affected production deletion and change blast radius.
- Remove any remaining phase wrapper that only copies fields without enforcing a freeze/ownership invariant, and reject an equally broad replacement `openRequest`/`TurnState` bag.
- Confirm secure-session/order, route override, metering/request authority, billing, selector semantics, extension APIs, parallel/interleaved behavior, backend ABI and tranche 1 downstream semantics remained unchanged.
- Confirm selector normalization, generation feature-surface projection and other deferred architecture candidates remain outside the implementation diff and are documented as independent future work.
- If state/projection metrics or behavioral gates do not demonstrate real simplification, re-scope/revert rather than declaring success based on file movement or type names.

_Requirements: 1.10, 2.1–2.10, 11.6–11.12_

_Design: D16–D18_

_Validation: tranche 2 ends with a materially simpler request-to-attempt state topology and no hidden expansion into unrelated architecture work._

## Completion Status

- [x] All implementation tasks completed and verified on `origin/main` @ `b763a772`
- [x] Task completion gate satisfied: every checkbox is `[x]` — this spec uses heading form (no checkbox list, 0/0 mechanical). Completion evidenced by typed pipeline implementation, deleted state-bag topology, and arch ratchets in main.
- [x] Implementation PR(s) merged:
  - PR #416 `refactor(runtime): simplify request attempt pipeline state` - head `7c03443996ce437d307ec71fa184ae7d1bb02f4d` merged as `4b6c40b39ec6d2d3939676f046883288e1a04572` on 2026-08-20T20:52:09Z - https://github.com/matdev83/go-llm-interactive-proxy/pull/416
  - PR #423 `fix(runtime): close post-transfer assemble ownership gap (specs 378/379 P1)` - head `4553bd2a7a8e3ec6dfb733d948a66d3d79b286d2` merged as `75bafd4d131d216af24775413901f20c7ff4394e` on 2026-08-21T09:36:11Z - https://github.com/matdev83/go-llm-interactive-proxy/pull/423
- [x] Required focused tests and architecture gates pass on merged baseline (see PR CI)
- [x] No successor-only removals falsely marked complete; successor scope documented as deferred (see spec project_description)
- [x] `spec.json` updated: `phase=completed`, `completed=true`, `ready_for_implementation=false`, `updated_at=2026-08-23T22:00:00+02:00`
