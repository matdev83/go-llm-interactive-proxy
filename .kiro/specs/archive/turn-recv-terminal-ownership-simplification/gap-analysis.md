# Brownfield Requirements Gap Analysis

## Result

**PASS after requirements corrections.** The initial direction—decompose `retryRecvStream` by responsibility—was correct, but a naïve extraction would have been unsafe. Brownfield review found that the current object is carrying several deliberately pinned or exactly-once responsibilities that cannot simply be moved by category. The final requirements now distinguish immutable request facts, replaceable attempt state, recovery state, response-pipeline state, and request-terminal state by **lifetime and invariant**, not merely by package name.

The most important corrections were: keep attempt terminal ownership with the replaceable attempt rather than the logical request; make output commitment one request-lifetime authority; preserve bare-Recv-context model/session/metering/billing facts explicitly; preserve current replacement authority-release ordering; and treat one-Recv-plus-concurrent-Close as the governing concurrency contract.

Baseline: `main` at `c3b5c872e6e48b6b9c86ea3570530b4fb094767c`.

## Existing Brownfield Facts

- `Executor.Execute` is already a small orchestrator and should remain so.
- `assembleExecutorStream` manually rehydrates one `retryRecvStream` from `preparedRequest`, `routePlanState`, `attemptOpenResult`, context-carried state, and Executor services.
- `retryRecvStream` is spread across many files, so file splitting has already occurred without eliminating the shared ownership center.
- `Recv` is single-owner, while `Close` may race a blocked `Recv`.
- `streamTerminal` already provides CAS/once terminal mechanics around `terminal.Owner`, but request and attempt terminal owners are both stored on the logical stream.
- Request and attempt terminal scopes are distinct: request scope spans the A-leg; attempt scope is replaced when the B-leg changes.
- `tryReplacementIteration` releases a swallowed attempt's authority before opening/admitting the replacement to prevent reservation overlap/conflict.
- recv-phase replacement must preserve request-bound model registry/catalog/native-model views even if the caller passes a bare context after reload.
- metering/request-authority and secure-session facts are reattached during recv because frontend/auxiliary callers may not pass the original enriched context.
- billing identity/version references are captured at admission specifically to prevent terminal closure from re-resolving mutable pricing/policy.
- provider/operator usage evidence and client-visible synthesized usage are intentionally different views.
- hidden/visible interleaved streams may hold A-leg end ownership outside the base retry stream.
- tool finalization and prompt-cache observation have attempt-local aspects; interleaved cycle/memo state has request/recovery continuity aspects.

## Gaps and Required Corrections

### 1. Decomposing by package/domain name is insufficient

An early decomposition could group all `billing.*` fields together, all routing fields together, and all terminal fields together. That would still cross actual lifetimes. For example, B-leg billing evidence is attempt-scoped while BillingCallID closure is request-scoped.

**Correction:** every state item is classified first by lifetime/invariant—immutable request, current attempt, recovery, response pipeline, request terminal—and only then by domain integration. Requirements 3–8 now enforce that model.

### 2. Attempt terminal ownership cannot live in the request terminal owner

The current stream stores both request and attempt terminal owners and uses `resetAttemptTerminal` on replacement. Moving both into a new `TurnTerminal` would preserve the lifetime mismatch.

**Correction:** the current `AttemptSession` owns its attempt terminal. `TurnTerminal` owns only request-lifetime terminal state and composes with the current attempt when a command covers both scopes. Replacement naturally creates a fresh attempt terminal.

### 3. Output commitment needs one authority before state is split

Commitment constrains routing recovery, terminal command legality, authority state, secure recording failure handling, and post-output error mapping. If extraction leaves a boolean in multiple owners, races and drift become more likely.

**Correction:** Requirement 8 makes output commitment a single request-lifetime authority under logical terminal ownership. Other owners query/mark it through a narrow operation and do not mirror it.

### 4. Bare `Recv` contexts make some apparent duplication intentional

The stream currently retains `execctx.Views`, secure-turn state, metering holder, request authority, route preferences, and model bindings because callers may invoke `Recv` with a context lacking the original Execute enrichments.

**Correction:** introduce one immutable receive-turn facts boundary. Those facts remain explicitly owned for the request and may be projected back into a derived context for existing SDK calls. The requirement is not “remove all retained copies”; it is “one authoritative retained copy, no business reliance on arbitrary caller context.”

### 5. Model-view pinning is a correctness boundary, not accidental cache state

Bound registry/catalog/native-model views prevent recv-phase failover from consulting a new live generation after reload.

**Correction:** model bindings are part of immutable turn facts. The refactor must retain exact request-generation pinning and bare-context tests.

### 6. Replacement ordering couples attempt terminalization and recovery

The current path deliberately releases/finalizes the swallowed attempt reservation before replacement admission. A simplistic recovery controller that opens first and cleans old attempt later would regress quota/credit safety.

**Correction:** Requirement 5 defines attempt terminalization/authority release as part of replacing the current `AttemptSession`, and Requirement 6 prohibits recovery from bypassing that transition.

### 7. Response processing cannot own settlement just because it sees `response_finished`

`response_finished` triggers usage finalization and terminal effects, but the response pipeline is not the authority for request settlement/billing closure.

**Correction:** the pipeline produces final evidence and invokes the terminal owner. `TurnTerminal` remains the exactly-once authority. This prevents a new response-pipeline God object.

### 8. Provider-billable and customer-visible usage are intentionally different

The current code retains `lastAuthorityUsage` separately from reconstructed customer usage because the synthesized client event may intentionally omit provider-billable scopes.

**Correction:** Requirement 7 explicitly preserves dual evidence views. Simplification may improve ownership but may not collapse semantically distinct evidence.

### 9. Tool and prompt-cache state are not all request-scoped

The per-B-leg tool assembler and prompt-cache source/controller can depend on the active backend attempt. Carrying them through a replacement as request-global state is incorrect.

**Correction:** attempt-local tool assembly and prompt-cache source/controller move with `AttemptSession`. Response-level tool classification that truly spans events may remain in the response pipeline. Tests must prove replacement reset/carry behavior.

### 10. Interleaved-thinking state has split lifetimes

Thinker/executor role and some memo effects are candidate/attempt-specific, while cycle cursor and continuation state persist across recv replacement and sometimes across an outer wrapper.

**Correction:** design requires explicit placement by lifetime. The spec does not redesign interleaved thinking; it preserves the established store/wrapper authority and only moves integration state to the matching owner.

### 11. A-leg end can be owned by an outer interleaved coordinator

Current `holdALegEnd` prevents the base stream from ending the A-leg while a hidden/visible interleaved wrapper coordinates a combined logical request.

**Correction:** A-leg terminal ownership must explicitly support delegation/hold to the existing outer coordinator. The refactor may replace the boolean with an explicit ownership mode, but must preserve the exact behavior.

### 12. Existing direct-construction tests can obstruct stronger invariants

Many runtime tests historically construct internal stream structs directly. Preserving arbitrary zero-value validity can force nil checks and lazy initialization into production types.

**Correction:** migrate tests to focused fixtures/builders as collaborators are introduced. Keep only zero-value tolerance required by genuine production contracts; do not keep broad weak initialization solely for old tests.

### 13. Removing `*Executor` immediately can create a worse service bag

The stream currently reaches many services through Executor. Replacing this with `recvServices{...50 fields...}` would only rename the dependency supermarket.

**Correction:** each owner receives the exact operations it needs. A temporary narrow replacement-open adapter is allowed during migration because the following spec redesigns the upstream attempt pipeline. Generic service bags are prohibited.

### 14. Lock extraction can worsen deadlock risk

Today locks are numerous but mostly local to stream fields. Splitting state into collaborators without defining call/lock boundaries can introduce nested cross-owner locking.

**Correction:** Requirements 2 and 10 make synchronization ownership normative. No caller holds one owner's private mutex while invoking another owner unless an explicit documented order and scheduling test proves it safe.

### 15. LOC cannot be the sole simplification gate

Some strong ownership types may add small amounts of code while deleting state forwarding and reducing invariants. Conversely, merely moving methods can produce a negative LOC delta without improving ownership.

**Correction:** final review uses responsibility count, state ownership, broad dependency reachability, direct cross-domain receiver methods, lock boundaries, and affected production deletion together. Net production growth is a default NO-GO, but LOC alone is not sufficient evidence.

## Brownfield Compatibility Matrix

| Existing subsystem / authority | Required treatment |
|---|---|
| `Executor.Execute` | remain thin; no recv/terminal logic moves back into it |
| `routing` planner/selector | unchanged semantics; called through existing open seam |
| `attemptOpenParams` / open pipeline | temporarily adapted; redesigned only by following spec |
| B2BUA A-leg lifecycle | unchanged authority; terminal owner coordinates End/Cancel |
| B-leg identity/attempt | moves behind current `AttemptSession`; semantics unchanged |
| `terminal.Owner` | reused as terminal state-machine authority |
| usage authority | unchanged domain authority; owner placement simplified |
| billing convergence | unchanged domain architecture and persistence semantics |
| metering/token accounting | unchanged calculations/evidence semantics |
| secure session | unchanged identity/recording authority |
| model registry/catalog | request-bound views remain pinned |
| completion gates | unchanged ordering/buffering semantics |
| tool finalization/classification | unchanged semantics; state placed by lifetime |
| prompt cache | unchanged semantics; attempt-local controller/source follows attempt |
| interleaved thinking | unchanged store/wrapper semantics |
| final stream observers / compaction | unchanged extension semantics |
| public SDK/config/backend ABI | no changes |

## Corrected Required Invariants

1. The EventStream façade is an adapter, not the aggregate owner of every turn concern.
2. Immutable request facts have one explicit request-lifetime home.
3. One active B-leg corresponds to one `AttemptSession` with its own attempt terminal/authority/resources.
4. Replacing an attempt replaces the attempt owner; it does not reset scattered request fields.
5. Retry/failover mutable state has one recovery owner.
6. Client-event transformation/accumulation has one response-pipeline owner.
7. Output commitment and request terminalization have one request-lifetime terminal authority.
8. Request and attempt terminal lifetimes remain distinct.
9. Old attempt authority is finalized/released at the same safety point relative to replacement admission as today.
10. Bare-context `Recv` cannot lose request-bound model/session/billing/metering facts.
11. One-Recv-plus-concurrent-Close remains the supported concurrency model.
12. No generic mutable turn bag or service locator replaces the current God object.
13. Final code must delete old state/forwarding surfaces rather than dual-write indefinitely.

## Requirements Correction Status

The final `requirements.md` incorporates all material brownfield corrections above. In particular, Requirements 4–8 now model lifetimes explicitly; Requirements 2 and 10 pin concurrency; Requirement 9 protects recently converged billing/security/accounting/interleaved semantics; and Requirement 11 makes structural simplification measurable without reducing the exercise to file length.

**Requirements quality gate: PASS.**
