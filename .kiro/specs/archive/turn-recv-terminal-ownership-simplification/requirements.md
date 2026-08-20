# Requirements Document

## Introduction

Go-LIP shall simplify the post-open request execution path by replacing the current flattened `retryRecvStream` ownership model with a small `lipapi.EventStream` façade over cohesive private owners whose lifetimes match the state they manage. The change shall reduce mutable state coupling and future change blast radius while preserving all current request, retry, streaming, billing, accounting, secure-session, interleaved-thinking, and terminal semantics.

This is a structural simplification specification, not a feature specification. It shall not add new externally observable behavior, public SDK/configuration surface, routing semantics, billing semantics, or protocol behavior merely to justify the refactor.

The current `Executor.Execute` orchestration shell is explicitly not the primary target. The target begins at stream assembly after an initial backend attempt has been opened and covers receive-phase processing, replacement/failover coordination, current-attempt ownership, client event processing, and logical request terminalization.

## Boundary Context

- In scope: `retryRecvStream`, recv loop/handlers, current-attempt stream/B-leg/authority state, recv-phase recovery state, response/gate/tool/observer state, request/attempt terminal ownership, billing/metering/usage terminal integration, bare-Recv-context state preservation, and related architecture tests.
- Out of scope: redesign of `prepareRequest`, `prepareSubmitAndALegSecure`, `routePlanState`, `attemptOpenParams`, candidate admission/open semantics, selector grammar, billing domain models, secure-session domain models, OpenResponses protocol state machine, backend-plugin ABI, or public extension APIs.
- Chronological follow-up: `request-attempt-pipeline-state-simplification` owns the upstream preparation/attempt-state refactor after this spec establishes the downstream ownership seam.
- Existing authorities remain authoritative: `terminal.Owner`, usage-authority services, billing stores/services, B2BUA A/B-leg lifecycle, `ResourceLedger`/generation ownership, secure-session manager/recording policy, routing planner, interleaved-thinking state store, and canonical `lipapi` event semantics.

## Requirement 1: Evidence-First Ownership and Behavior Baseline

1.1. Before moving production state, add a deterministic ownership inventory for every direct field and synchronization primitive currently owned by `retryRecvStream`, classified at minimum as immutable request fact, current-attempt state, recovery/routing state, response-pipeline state, request-terminal state, or infrastructure/compatibility state.
1.2. Record the current receiver-method/file fan-out for `retryRecvStream` by responsibility, not only by LOC.
1.3. Add characterization coverage for initial receive success, multi-event streaming, EOF, explicit Close, caller cancellation, A-leg cancellation, timeout, backend panic mapping, recoverable pre-commit failure, non-recoverable post-commit failure, and replacement exhaustion.
1.4. Add characterization coverage for completion-gate buffering/draining, synthesized usage before `response_finished`, sideband usage evidence, tool-call finalization/classification, final stream observers, compaction observations, prompt-cache observations, and secure-session stream recording.
1.5. Add characterization coverage for hidden and visible interleaved-thinking streams, including A-leg hold/release, thinker/executor continuation, memo/cycle carryover, and recv-phase replacement.
1.6. Add characterization coverage proving one billing leg usage record per B-leg, one BillingCallID closure per logical incoming invocation, request/attempt authority exactly-once terminal behavior, and current settlement/release ordering on swallowed attempts.
1.7. Add scheduling-sensitive tests for `Close` racing blocked `Recv`, request terminalization racing attempt replacement, and cancellation/EOF arriving while sideband usage is being drained.
1.8. The baseline shall identify current duplicate or mirrored state authorities, especially output commitment, attempt terminal ownership/reset, finished state, and request facts recovered from `context.Context`.
1.9. The final implementation shall rerun the same inventory and demonstrate fewer direct responsibility clusters and fewer cross-domain receiver methods on the EventStream façade.
1.10. If the final architecture cannot demonstrate material ownership/coupling reduction while preserving behavior, implementation shall stop or re-scope rather than ship a file-only reorganization.

## Requirement 2: Preserve the EventStream and Concurrency Contract

2.1. The returned object shall remain compatible with `lipapi.EventStream` and all existing managed-stream behavior expected by frontends and wrappers.
2.2. `Recv` shall remain single-consumer: concurrent multiple-`Recv` use need not become supported.
2.3. `Close` shall remain safe to invoke concurrently with one `Recv` blocked on the active backend stream.
2.4. `Close` shall still reach the active backend attempt sufficiently to cancel/close it and unblock receive paths according to current semantics.
2.5. The refactor shall not introduce a permanent worker goroutine, background event pump, channel fan-out architecture, or scheduler merely to separate ownership.
2.6. Existing error classes and phase semantics for EOF, context cancellation, timeout, A-leg cancellation, upstream failure, negotiation/admission exhaustion, and post-output failure shall remain observationally equivalent.
2.7. Canonical event ordering and exactly-once client-visible event delivery shall remain unchanged.
2.8. The same logical request shall remain pinned to the request-bound generation/model/catalog views captured at execution time even when `Recv` receives a bare frontend context after a reload.
2.9. Detached terminal work shall continue to use bounded cleanup contexts and shall not be canceled merely because the original frontend request context ended.
2.10. The ownership decomposition shall define synchronization responsibility and lock-order rules so one collaborator does not require callers to hold another collaborator's internal mutex while invoking it.

## Requirement 3: Reduce `retryRecvStream` to a Small EventStream Façade

3.1. The final `retryRecvStream` (or replacement EventStream façade) shall primarily coordinate `Recv`/`Close` control flow and hold cohesive collaborator references rather than directly storing independent domain state for routing, billing, authority, metering, security, tools, interleaved thinking, prompt cache, and terminal settlement.
3.2. The façade shall not retain a broad `*Executor` in the final architecture solely as a service locator for recv/terminal behavior.
3.3. The façade shall not directly own mutable billing account/call closure state, request/attempt authority state, routing exclusion/rejection state, tool assembly/classification state, interleaved retry state, or request terminal state.
3.4. The façade may hold minimal transport-adapter state that is intrinsic to implementing `EventStream`, but each such field shall have one clear invariant and owner.
3.5. Cross-domain receiver methods currently attached to `retryRecvStream` shall migrate to the collaborator that owns their state unless a method is genuinely EventStream coordination.
3.6. No universal mutable `TurnContext`, `ExecutionBag`, `map[string]any`, generic keyed state registry, DI container, or service locator shall replace the current struct.
3.7. Private concrete collaborators are preferred. New interfaces require a real substitution/port boundary or test need and shall not be introduced merely to satisfy SOLID mechanically.
3.8. The final stream constructor shall receive or construct cohesive owners rather than copying dozens of unrelated fields from upstream phase objects into one flat struct.
3.9. Test-only direct construction of `retryRecvStream` shall migrate to focused fixtures/builders that construct valid collaborator owners; production code shall not retain weakly initialized zero-state paths solely for old tests where stronger invariants are practical.
3.10. An architecture test shall prevent reintroduction of direct cross-domain state onto the façade without intentional review.

## Requirement 4: One Explicit Immutable Receive-Turn Facts Boundary

4.1. Introduce one package-private immutable facts value for request-lifetime information that must survive from stream assembly through recv/replacement/terminal paths.
4.2. At minimum, the facts boundary shall cover trace/A-leg identity, immutable baseline call, request/exec views needed on bare `Recv`, route preferences where still needed downstream, secure-session turn identity, and pinned model-view identity/handles.
4.3. Stable owner references for metering, request authority, and billing call state may be referenced by the immutable facts value, but their mutable business state shall remain owned by their respective collaborator rather than embedded as mutable fields in the facts value.
4.4. Construction shall defensively clone mutable request data that must remain stable for the turn, including the canonical baseline call and route-preference slices where current behavior requires it.
4.5. Request facts shall be authoritative for recv-phase business behavior; `context.Context` may mirror them for existing SDK/extension APIs but shall not become the only source of truth.
4.6. Model registry/catalog/native-model bindings captured for the request shall remain frozen across recv-phase replacement and generation refresh.
4.7. The facts value shall not contain retry counters, exclusions, event accumulators, terminal booleans, lock-bearing state, or current B-leg resources.
4.8. The facts boundary shall remain private to runtime execution and shall not become a public SDK DTO.
4.9. A bare-context recv characterization test shall prove that all required request facts remain available without falling back to current live generation state.
4.10. The design shall avoid maintaining two independently mutable copies of the same request fact between the facts value and the stream façade.

## Requirement 5: Current Attempt State Has One `AttemptSession` Owner

5.1. Introduce a package-private current-attempt owner whose lifetime corresponds to one active or terminalizing B-leg attempt.
5.2. The attempt owner shall own the current backend `ManagedEventStream`, B-leg record, selected candidate, attempt authority lifecycle, and attempt-terminal owner.
5.3. Attempt-local accounting, per-B-leg tool-call assembly/finalization state, and attempt-local prompt-cache observation/control shall be owned by the attempt owner or an explicitly nested attempt-local collaborator, not by the logical request stream.
5.4. Replacing a backend attempt shall create/swap a new attempt owner rather than overwriting scattered B-leg/candidate/authority/tool/prompt fields on the logical request stream.
5.5. The prior attempt shall reach a terminal state before or as required by current semantics before the replacement becomes authoritative; in particular, swallowed attempt authority shall be finalized/released before replacement admission where the existing safety contract requires it.
5.6. Attempt terminal ownership shall reset naturally by replacing the attempt owner. The logical request owner shall not need a `resetAttemptTerminal`-style mutation of request-lifetime state.
5.7. A caller that has already snapshotted an old attempt for concurrent Close/terminal work shall be able to complete against that old attempt without accidentally operating on the replacement.
5.8. Attempt owner cleanup/terminalization shall be idempotent and race-safe under the supported one-Recv-plus-concurrent-Close model.
5.9. Sideband usage evidence belonging to an attempt shall be drained before that attempt is discarded on EOF, error, cancellation, or replacement.
5.10. Existing B-leg attempt logging, outcome evidence, billing leg recording, and authority settlement shall remain associated with the correct B-leg after replacement.
5.11. Interleaved thinker/executor role and pending memo effects that are genuinely attempt-local shall be bound to the correct attempt; request-lifetime cycle/memo continuity remains with recovery/request state as appropriate.

## Requirement 6: Retry and Failover State Has One Recovery Owner

6.1. Introduce a package-private recovery controller/state owner for mutable information used to decide recv-phase replacement.
6.2. It shall own selector/session routing state, excluded candidates, attempt and TTFT budgets, request-size routing estimate, affinity identity/state, prior negotiation/transport/admission failures, context-limit exhaustion, transform-exclusion state, and last parallel failure where those remain required downstream.
6.3. Interleaved retry/cycle state and retry-specific suppressions that determine replacement candidate planning shall have one authoritative home under recovery/request continuity rather than being duplicated across the stream and attempt-open parameters.
6.4. Recovery shall query output commitment from the single request-terminal authority rather than keep a competing commitment boolean.
6.5. Recovery shall not own billing settlement, metering finalization, secure-session recording buffers, client event accumulation, or request terminal lifecycle.
6.6. The existing upstream route planner and candidate-open algorithm remain authoritative in this tranche. Recovery may call a narrow adapter to the current open seam but shall not reimplement selector grammar or candidate admission.
6.7. The final EventStream façade shall not reconstruct a giant `attemptOpenParams` directly from its own fields. A replacement request shall be produced from the recovery owner plus immutable request facts/current-attempt terminal outcome.
6.8. Error-precedence behavior when no candidate is eligible shall preserve current negotiation, transport, admission, context-limit, transform-exclusion, and parallel-failure semantics.
6.9. Sticky affinity clearing/reselection and preferred-candidate behavior shall remain unchanged.
6.10. Recovery decisions shall remain deterministic under injected RNG/tests and retain current first-request/interleaved cycle semantics.
6.11. The refactor shall not add a second routing state machine or duplicate `routing.ExpandFailoverGroups` semantics.

## Requirement 7: Response Event Processing Has One Pipeline Owner

7.1. Introduce a package-private response pipeline owner for mutable state that converts backend canonical events into client-facing canonical events.
7.2. The pipeline shall own customer-visible event/text accumulation, internal usage-evidence dedupe, completion-gate buffers/drains, recovery drain, and tool event classification/finalization queues that span receive calls.
7.3. The pipeline shall preserve current mandatory client-facing preflight and synthesized-usage ordering around `response_finished` for direct, recovery-drain, and gate-drain paths.
7.4. Provider/operator usage evidence and client-visible usage reconstruction shall remain distinct views; a synthesized client event shall not erase provider-billable evidence needed for settlement.
7.5. Completion gates shall retain current buffering limits, overflow/live behavior, finish handling, and hook ordering.
7.6. Tool-call finalization and classification shall retain argument bounds, repair/finalizer order, per-tool correlation, and attempt/turn reset semantics.
7.7. Final-stream observation, traffic observation, compaction observation, and usage observation shall retain current fail-open/fail-closed policy and ordering.
7.8. Secure-session stream recording may be invoked by the response pipeline where event recording is its responsibility, but mandatory recorder failure policy and replacement legality shall be decided through the security/terminal authorities rather than duplicated pipeline booleans.
7.9. The pipeline shall not decide route selection, acquire/release usage authority, close a billing call, or end the A-leg.
7.10. Client accumulators and observer state shall be concurrency-safe for supported Close-versus-Recv terminal snapshots without introducing broad cross-owner locking.
7.11. A response pipeline test matrix shall cover normal events, tool events, usage, gate buffering/draining, recovered streams, terminal errors, and concurrent terminal snapshots.

## Requirement 8: Logical Request Terminal State Has One `TurnTerminal` Authority

8.1. Introduce a package-private logical request terminal owner spanning the A-leg lifetime.
8.2. Output commitment shall have exactly one request-lifetime authority. `MarkCommitted` shall be idempotent, and recovery/attempt/response code shall query or receive that authority rather than maintain independent commitment flags.
8.3. The request terminal owner shall own the request-scoped `terminal.Owner` wrapper and request finished/terminal-result publication.
8.4. Attempt terminal ownership shall remain with the current `AttemptSession`; request terminalization may compose with the current attempt according to `sdkterminal.Command` scope but shall not store a resettable attempt terminal as request-lifetime state.
8.5. Request-authority terminalization, request-level metering finalization, BillingCallID closure, and A-leg end ownership shall converge through the logical terminal owner or explicitly delegated terminal effects with exactly-one semantics.
8.6. Per-B-leg billing recording shall remain associated with the corresponding attempt and shall be deduplicated even when request and attempt terminal effects race or overlap.
8.7. Durable terminal-work behavior, including `work_pending` versus settled/released progression, shall retain the existing `terminal.Owner` semantics.
8.8. `Close`, cancel, timeout, EOF, normal finish, partial error, swallowed attempt, and gate-replacement commands shall retain current scope legality and error mapping.
8.9. A committed gate-replacement rejection shall still freeze request/billing closure where current behavior requires it without illegally re-running terminal effects.
8.10. A-leg end shall occur exactly once unless an interleaved outer coordinator intentionally owns the final end; that hold relationship shall be explicit rather than inferred from scattered stream flags.
8.11. Terminal effects shall continue to run under detached bounded cleanup context where required and shall publish one shared terminal outcome to losing concurrent claimants.
8.12. No response-pipeline or recovery owner shall independently set a final `finished` flag that can diverge from terminal ownership.

## Requirement 9: Preserve Security, Billing, Accounting, and Interleaved Semantics

9.1. Secure-session authoritative IDs and recording policies remain unchanged; this refactor changes integration ownership only.
9.2. Mandatory secure-session recording failure before commitment shall preserve current fail-closed/recovery behavior.
9.3. Mandatory recording failure after output commitment shall continue to prohibit replacement where current policy requires it and shall surface the same post-output failure semantics.
9.4. Billing identity captured after exposure admission shall remain stable through bare-context `Recv` and shall not be re-resolved from mutable live configuration during terminal closure.
9.5. BillingCallID remains one per incoming invocation across retries, failover alternatives, parallel B-legs, and interleaved continuation according to existing billing convergence rules.
9.6. Exactly one terminal usage/call-closure record shall be emitted per logical billing call, and exactly one leg usage record per B-leg where current rules require it.
9.7. Request and attempt usage-authority reservations shall never be double-settled, double-released, leaked, or overlapped contrary to current replacement ordering.
9.8. Metering checkpoints and customer/operator evidence shall remain available to terminal paths even when the frontend context is canceled or bare.
9.9. Hidden/visible interleaved thinker wrappers shall retain A-leg hold semantics, thinker recording, continuation construction, cycle cursor updates, and memo visibility rules.
9.10. Prompt-cache observation/control shall follow the active attempt and shall not accidentally carry an attempt-local controller/source across a replacement unless current semantics explicitly require it.
9.11. Keep-warm integration shall retain current foreground-turn and terminal/session interactions without becoming a new owner of request terminal state.
9.12. No domain redesign of billing, accounting, usage authority, secure session, prompt cache, or interleaved thinking is permitted in this specification.

## Requirement 10: Explicit Synchronization and Race Safety

10.1. Each extracted mutable owner shall document which goroutine(s) may mutate it and which operations may race with `Close`.
10.2. Locks/atomics shall be owned by the component whose invariant they protect; the implementation shall not retain one shared giant mutex across all extracted owners.
10.3. Cross-owner calls shall not require undocumented lock ordering. If lock nesting is unavoidable, the ordering shall be explicit and covered by scheduling tests.
10.4. Swapping the current attempt shall be atomic/coherent from the perspective of `Recv` and concurrent `Close` so `Close` cannot partly cancel one attempt while terminal effects act on another.
10.5. Output commitment and request terminalization shall be race-safe and exactly once.
10.6. Event/customer evidence snapshots taken by terminal paths shall be coherent with one-Recv mutation and concurrent Close.
10.7. Billing leg dedupe and billing call closure shall remain race-safe without exposing billing mutexes to unrelated response/recovery code.
10.8. Context-cache optimization, if retained, shall be isolated from business state and shall not be used as an implicit synchronization authority.
10.9. Run targeted race tests for receive/close/replacement/terminal/usage-recording paths on supported CI platforms.
10.10. Add leak/deadlock tests or bounded scheduling tests for any new goroutine or waiting primitive; the default design should require no new long-lived goroutine.

## Requirement 11: Structural Simplification and Architecture Ratchets

11.1. Add an architecture metric/test that counts or classifies the EventStream façade's direct state responsibilities and rejects reintroduction of flattened cross-domain fields without an explicit baseline update/review.
11.2. Add an architecture metric/test for broad runtime reachability so the final façade cannot regain a direct `*Executor` service-locator dependency.
11.3. Add an ownership matrix test or checked-in evidence mapping request facts, current-attempt state, recovery state, response state, and request-terminal state to exactly one owner.
11.4. Remove obsolete stream receiver methods, forwarding wrappers, reset helpers, duplicate flags, and flat fields as each owner becomes authoritative; do not preserve dead compatibility layers indefinitely.
11.5. The final affected production runtime surface shall show net deletion of state-handoff/forwarding code. A net production growth outcome is a default NO-GO unless design review demonstrates a substantial invariant reduction that cannot be expressed with less code.
11.6. File count reduction is not required and file count growth is not evidence of simplification. Review shall prioritize number of owners, mutable fields, cross-domain method fan-in, and synchronization boundaries.
11.7. The implementation shall not weaken architecture, race, billing, secure-session, conformance, protocol, or parity tests to satisfy the new design.
11.8. No new public config field, CLI flag, SDK API, backend-plugin ABI field, protocol-specific compatibility branch, or provider-specific routing rule shall be added for this refactor.
11.9. No generic stage engine, actor framework, reactive graph, DI container, service locator, or reflection-based event dispatcher shall be introduced.
11.10. `Executor.Execute` shall remain a compact orchestration shell and shall not absorb recv/terminal details removed from the stream.
11.11. The implementation shall leave upstream `preparedRequest`/`routePlanState`/candidate-open simplification to the following specification except for minimal adapters required to construct the new downstream owners.
11.12. Final review shall compare the before/after ownership map, direct state clusters, synchronization boundaries, and affected production diff and issue a GO/NO-GO based on real structural simplification rather than code movement.
