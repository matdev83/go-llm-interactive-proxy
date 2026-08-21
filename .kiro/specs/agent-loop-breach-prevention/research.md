# Research and Brownfield Gap Analysis

## Summary

Issue #426 asks Go-LIP to prevent unattended agent loops from dying when a backend stream ends incorrectly or a model emits a clean-looking terminal despite obviously unfinished work. Research across mature agent harnesses shows that there is no single reliable “continue” heuristic. The strongest production pattern is to treat completion as a gate: hold the candidate stop, distinguish transport recovery from semantic completion, use a separate bounded evaluator for ambiguous clean stops, and continue only when the evaluator can identify concrete unfinished work already authorized by the user.

The brownfield analysis also changes the naive implementation shape. Go-LIP already has transport-recovery, continuation, auxiliary-request, terminal-ownership, B2BUA, and canonical-stream infrastructure. The feature should compose those owners rather than create a second retry engine. Most importantly, the repository explicitly forbids silent retry/failover after client-visible output is committed. Therefore a post-output stream break must be modeled as **preserve-and-continue on a new B-side leg**, never as replay of the committed attempt.

## Source Context

- Feature request: <https://github.com/matdev83/go-llm-interactive-proxy/issues/426>
- Prior-art research comment: <https://github.com/matdev83/go-llm-interactive-proxy/issues/426#issuecomment-5370104629>
- Proposed architecture comment: <https://github.com/matdev83/go-llm-interactive-proxy/issues/426#issuecomment-5370121594>
- Project steering: `.kiro/steering/product.md`, `.kiro/steering/structure.md`, `.kiro/steering/testing.md`, `.kiro/steering/routing-and-orchestration.md`
- Project Kiro rules: `.kiro/rules/ears-format.md`, `gap-analysis.md`, `design-discovery-full.md`, `design-principles.md`, `design-review.md`, `tasks-generation.md`, `tasks-parallel-analysis.md`

## Current-State Investigation

### `internal/core/streamrecovery`

Current stream recovery is already a focused transport policy. Its `Config` owns enablement, idle timing, warning emission, and post-output behavior. `Policy` tracks client commitment and response completion and produces four broad decisions: pass through, recover pre-output, finish post-output, or surface failure.

The important gap is current post-output handling: EOF, idle timeout, or other transport failure after output commitment resolves through `finishPostOutput`, which synthesizes `response_finished` with finish reason `proxy_stream_recovered` (and optionally a warning). This hides a broken backend stream from the client but still sends a final response boundary, so an unattended client agent loop can terminate. Issue #426 requires an optional “continuation-eligible post-output interruption” decision rather than always manufacturing success.

**Constraint:** `streamrecovery` should remain the transport detector/policy. Agent Loop Guard should not duplicate its timers, replay rules, or pre-output retry configuration.

### `internal/core/terminal` and `pkg/lipsdk/terminal`

`terminal.Owner` is a CAS-owned exactly-once terminal state machine. It rejects retry/replacement when output has already been committed and linearizes one terminal owner. Existing terminal commands include normal finish, cancellation, timeout, EOF, swallowed attempt, backend open failure, and other attempt/request terminal causes.

**Constraint:** semantic stop verification must run before the logical request claims `normal_finish`. A B-side attempt may settle independently while the logical A-side request remains open for a continuation leg. The feature must not “undo” a request terminal claim or weaken the output-commit replacement prohibition.

### `pkg/lipsdk/continuation` / `internal/core/continuation`

Continuation records already retain input/output items, lineage, native references/requirements, materialized size, status, chain depth, previous ID, model/profile information, and provider-bound continuation identity. This is the correct substrate for preserving an interrupted canonical trajectory and opening a safe continuation without replaying committed output.

**Constraint:** new guard state should reference/derive from existing continuation lineage instead of building a parallel transcript store.

### `internal/core/auxreq` / `pkg/lipsdk/auxiliary`

Auxiliary requests already support an internal role, visibility, detached/default session mode, parent trace/A-leg/B-leg/branch lineage, plugin suppression, and depth guarding. They run through the normal runtime executor with internal scope/origin and inherited generation pins.

**Opportunity:** this is the correct execution path for a separate semantic completion verifier. The verifier request can be detached/internal, inherit parent lineage, disable the loop guard itself to prevent recursion, and use existing accounting/observability.

### Canonical and orchestration steering

Repository steering requires the universal `frontend -> canonical -> backend` architecture, streaming-first behavior, no core provider SDK leakage, explicit A/B leg continuity, and no silent failover after output commit. Current architecture work also emphasizes narrow consumer-owned interfaces and avoiding new god objects/service locators.

**Constraint:** continuation output must be stitched at canonical event/item level. Concatenating raw SSE/provider frames across attempts would violate protocol legality and provider neutrality.

### Related non-forwardable content work

A separate Kiro specification for non-forwardable conversation content exists, but its runtime API is not a current dependency that #426 can assume. Agent Loop Guard therefore must be implementable with current internal auxiliary/canonical request machinery. Its hidden recovery instruction must never be surfaced to A-side as user-authored content; future steering/non-forwardable infrastructure may later provide a cleaner representation without blocking this feature.

## External Prior Art

### Claude Code: Stop hooks and `/goal` — selected semantic pattern

Current Claude Code documentation exposes a mature completion gate:

- Stop hooks run when the main agent is about to finish and can block stopping with a reason that is fed back to the model.
- `stop_hook_active` tells a hook it is already running because of a previous stop block, preventing unbounded recursion.
- Claude Code imposes a hard cap after repeated blocked stops.
- `/goal` uses a separate small, fast model after each response to evaluate whether a completion condition is satisfied. A negative verdict starts another turn and the evaluator's reason guides that turn.

Sources:
- <https://code.claude.com/docs/en/hooks>
- <https://code.claude.com/docs/en/goal>

**Adopt:** provisional terminal, independent evaluator, reason-bearing continuation, explicit recursion/budget guard.

**Do not copy literally:** Claude's evaluator can rely on Claude Code's own goal/session semantics; Go-LIP must remain frontend-neutral and cannot assume an agent-specific goal artifact.

### oh-my-pi: unexpected-stop classifier — useful implementation and important failure evidence

oh-my-pi contains a direct unexpected-stop guard. It classifies nominal `stop` turns with a small model, retries boundedly, and includes handling for thinking-only stops. Its empty-completion recovery is careful to discard/retry only before meaningful output has been committed; after commitment, replay becomes unsafe. Its broader retry policy can preserve completed tool results and continue rather than re-executing side effects.

Relevant source areas:
- `packages/coding-agent/src/session/unexpected-stop-classifier.ts`
- `packages/coding-agent/src/session/turn-recovery.ts`
- `packages/ai/src/utils/empty-completion-retry.ts`
- `docs/non-compaction-retry-policy.md`
- <https://github.com/can1357/oh-my-pi/issues/6540>
- <https://github.com/can1357/oh-my-pi/issues/7499>
- <https://github.com/can1357/oh-my-pi/pull/7501>

Issue #6540 is especially important: clean final answers with future-looking “Next steps” language have been false-positively classified as unfinished, causing unwanted extra main-model turns. The shipped classifier is single-sample and can be nondeterministic. This is evidence that a permissive wording heuristic is not sufficient.

**Adopt:** bounded classification, small verifier, thinking-only evidence when canonical, pre-output replay safety, preserve completed tool results.

**Reject:** classifying generic offers/questions such as “Should I do that for you?” as evidence that the model—not the user—must speak next.

### Gemini CLI: next-speaker checker — useful classification, unsafe continuation wording

Gemini CLI has a `nextSpeakerChecker` that distinguishes a model-owned immediate next action from a direct user question or a complete answer. This next-speaker framing is useful. Current client code, however, synthesizes a literal `Please continue.` when the checker selects the model.

Known public reports show why this is not suitable for Go-LIP:
- <https://github.com/google-gemini/gemini-cli/issues/5582>
- <https://github.com/google-gemini/gemini-cli/issues/13977>
- <https://github.com/google-gemini/gemini-cli/issues/13186>

A bare continuation message can be interpreted as fresh user intent after the original task is actually complete, encouraging unnecessary or invented work.

**Adopt:** distinguish MODEL-next from USER-next.

**Reject:** unconditional `Please continue.` as the hidden recovery instruction.

### OpenHands: explicit finish and stuck detection

OpenHands exposes explicit finish semantics and a `StuckDetector` that detects repeated actions/results/errors and alternating patterns. Public issues also document premature completion when assistant content is treated as finished without an explicit action and a state-race lesson: a preliminary FINISHED state cannot be authoritative if a Stop hook may still block completion.

Representative sources:
- <https://github.com/All-Hands-AI/OpenHands/issues/3992>
- <https://github.com/All-Hands-AI/OpenHands/issues/1351>

**Adopt:** explicit finish signals as strong evidence when available; progress/no-progress detection; terminal must remain provisional until the guard completes.

### Cline and Roo Code: conditional automated nudge

Cline/Roo's no-tool recovery message uses a useful three-way instruction: if the task is complete, use completion; if user input is required, ask/finalize for user input; otherwise proceed. It explicitly says the message is automated and should not be treated conversationally.

**Adopt:** condition recovery on actual unfinished work and explicitly deny the implication of new user intent.

### OpenAI Codex: transport retry separation

Codex maintains a dedicated stream retry/fallback state machine with bounded reconnect/backoff behavior and a clean separation between transport retries and normal turn completion.

**Adopt:** keep transport retry/reconnect policy separate from semantic completion verification. This matches Go-LIP's existing `streamrecovery` boundary.

## Architecture Pattern Evaluation

| Option | Benefits | Problems | Verdict |
|---|---|---|---|
| Heuristic-only detector | Cheap, deterministic, low latency | Misses nuanced unfinished work; wording-sensitive; hard to generalize across models | Useful only as an optional prefilter, not the authority |
| Always inject `Please continue` on clean stop | Simple | Expands scope after genuine completion; can create loops; user-intent ambiguity | Rejected |
| Require explicit frontend completion tool | Very reliable when available | Not portable; many frontends/models lack it | Use as strong optional evidence, not a universal requirement |
| Provisional terminal + separate bounded verifier + conditional continuation | Protocol-neutral; separates concerns; can preserve user-turn boundaries; aligns with mature harnesses | Adds latency/cost on verified stops; requires careful terminal ownership | **Selected** |
| Replay committed attempt after transport failure | Simple recovery implementation | Duplicates output/tool side effects; violates repository invariant | Rejected |

## Brownfield Gap Analysis and Requirements Repair

### Missing capabilities

1. No request-level semantic stop gate exists before normal final terminal publication.
2. Current post-output stream recovery manufactures a final success rather than allowing a safe continuation leg.
3. No guard-specific completion verifier exists, although `auxreq` provides the execution substrate.
4. No loop-guard progress fingerprint/budget exists.
5. No explicit policy binds trusted frontend completion signals to semantic guard behavior.

### Existing capabilities to reuse

1. Pre-output retry/failover and transport interruption classification: `internal/core/streamrecovery` plus current runtime recovery path.
2. Canonical retained trajectory and lineage: continuation packages.
3. Internal child-model execution and recursion suppression: auxiliary request packages.
4. Exactly-once attempt/request terminal ownership: terminal packages/runtime owners.
5. A/B leg accounting/traceability: current B2BUA, billing, authority, metering and tracing domains.

### Requirement repairs made after gap analysis

The initial feature concept was strengthened in four places:

1. **No duplicate transport-retry configuration.** Requirement 3 explicitly delegates pre-output replay/failover to the existing transport recovery policy.
2. **Post-output recovery is continuation, not retry.** Requirement 4 and 9 explicitly preserve the no-silent-failover-after-output-commit invariant.
3. **Verifier uncertainty is allow-stop.** Requirement 5 makes timeout/error/malformed/uncertain outcomes end normally, because a false-positive continuation can create unauthorized work or side effects.
4. **Hidden recovery is not user intent.** Requirement 6 makes the continuation instruction conditional, non-forwarded, and explicitly non-authorizing; a bare `Please continue` is prohibited by behavior rather than merely discouraged in design.

The repaired requirements pass the EARS/testability gate and do not depend on the future non-forwardable-content implementation.

## Selected Design Decisions

### Decision 1: Hold only the terminal boundary, not the whole response

The proxy continues streaming legal deltas normally. It suppresses only a candidate terminal until the guard resolves. This preserves streaming UX and keeps memory proportional to existing continuation/evidence needs rather than full-response buffering.

### Decision 2: Two recovery classes, not one retry mechanism

- **Replay-safe pre-output transport failure:** existing `streamrecovery` / retry-failover path.
- **Post-output interruption or semantic premature stop:** retain canonical progress and, if safe/authorized, create a new continuation B-leg while the same A-side logical request remains open.

The second path is not a retry of the committed attempt.

### Decision 3: Separate verifier with structured multi-state verdict

The completion verifier should return one of:

- `ALLOW_STOP`
- `CONTINUE`
- `NEEDS_USER`
- `BLOCKED`
- `UNCERTAIN`

`CONTINUE` is actionable only if it includes a concrete remaining objective already present in the user's request and executable without new user input. All uncertainty defaults to stopping.

### Decision 4: Conditional continuation instruction as a second safety barrier

Even after a `CONTINUE` verdict, the worker receives an internal recovery instruction that tells it to independently stop if the work is already complete or if user input/permission is needed. This protects against verifier false positives and stale evidence.

### Decision 5: Explicit completion signals are evidence, not a new universal contract

Frontends with explicit finish/`attempt_completion` semantics can be trusted by policy to avoid unnecessary semantic verification. Unknown/generic frontends remain compatible because the verifier operates on canonical trajectory.

### Decision 6: Hard budgets and no-progress breaker

Bounded attempts, verifier timeout, recursion suppression, and trajectory fingerprints prevent the safety feature from becoming an autonomous infinite loop.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Verifier false positive | Extra work, scope expansion, unintended tool side effects | Asymmetric conservative verdict; concrete remaining objective required; conditional worker prompt; explicit completion evidence; no-progress cap |
| Verifier false negative | Agent loop can still stop prematurely | Better evidence window; optional future evaluation corpus/tuning; observability; explicit “all clean stops” mode |
| Extra latency/cost | Every verified clean stop incurs auxiliary call | Feature opt-in; small/fast auxiliary role; bounded timeout; trust explicit completion signals; retain room for later safe prefilter mode |
| Post-output duplicate side effects | Potentially severe | Never replay committed attempt; preserve completed tool result; refuse unsafe partial-tool continuation |
| Terminal race | Duplicate/early A terminal | Guard before logical `normal_finish`; retain existing terminal CAS owners; E2E race tests |
| Recursive verifier guard | Hidden loop | Disable guard for auxiliary verifier request plus explicit guard-active state |
| Raw protocol stitching | Malformed stream | Stitch canonical events/items only; adapter renders one legal A-side logical stream |
| Hidden instruction appears as user intent | Model scope/authority confusion | Explicit automated/non-authorizing wording; internal visibility; never A-side persistence as user-authored message |

## Revalidation Triggers

Re-run brownfield/design validation if implementation changes any of these facts:

- `streamrecovery` stops owning transport interruption classification or post-output behavior;
- the repository permits retry/failover after output commitment;
- `terminal.Owner` request/attempt ownership semantics change;
- auxiliary requests no longer support internal role/visibility, lineage, or plugin suppression;
- continuation lineage/materialization contracts change materially;
- a canonical non-forwardable steering API lands and supersedes hidden recovery injection;
- frontend protocol identity/item correlation rules change;
- billing/B2BUA/authority semantics for hidden continuation legs change;
- a standard explicit completion contract becomes universal across supported frontends.
