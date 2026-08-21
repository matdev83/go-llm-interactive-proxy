# Requirements Document

## Introduction

Long-running agent sessions can terminate even though the user's requested work is not actually complete. The failure can be transport/protocol-level—for example, an EOF, reset, idle timeout, or empty completion—or semantic, where a backend emits a syntactically clean terminal marker while the model's own response shows that it intended to keep working. Today, a terminal event that reaches the A-side client can break the client agent loop permanently when the user is unattended.

This specification adds an opt-in **Agent Loop Guard** that prevents recoverable backend terminal candidates from becoming prematurely visible as final A-side termination. The guard distinguishes replay-safe transport recovery from post-output continuation and from semantic completion verification. It must bias toward avoiding false-positive autonomous continuation: uncertainty, user-choice boundaries, refusals, and unsupported recovery states end normally rather than inventing work or authority.

The feature is brownfield. Existing Go-LIP streaming, transport recovery, continuation, auxiliary request, terminal ownership, B2BUA lineage, billing, routing, and protocol invariants remain authoritative.

## Boundary Context

### In Scope

- opt-in interception of eligible terminal candidates before final A-side terminal publication;
- canonical classification of transport, protocol, and clean semantic stop causes;
- reuse of current pre-output transport recovery behavior;
- safe post-output continuation from retained canonical trajectory without replaying committed work;
- separate semantic verification of eligible clean terminal candidates;
- conditional internal continuation instructions that preserve existing user intent and authority;
- bounded continuation, progress detection, recursion prevention, terminal accounting, and observability;
- protocol-neutral behavior across supported frontends/backends.

### Out of Scope

- a generic task planner, workflow engine, autonomous objective discovery system, or agent orchestrator;
- changing provider-specific stop semantics at adapter boundaries;
- weakening the current prohibition on silent retry/failover after client-visible output is committed;
- inferring user permission, approval, choices, or authorization from a recovery event;
- requiring every frontend agent to adopt an explicit completion tool;
- redesigning billing, routing, B2BUA, continuation persistence, or extension architecture;
- implementing the separate non-forwardable conversation-content specification as a prerequisite.

## Requirements

### Requirement 1: Opt-In Activation and Backward Compatibility

**Objective:** As a proxy operator, I want loop-breach prevention to be explicitly enabled so that existing deployments do not acquire new autonomous behavior unexpectedly.

#### Acceptance Criteria

1.1. **Where** Agent Loop Guard is disabled, the proxy shall preserve the existing terminal, stream-recovery, retry, failover, continuation, and A-side publication behavior.

1.2. **When** Agent Loop Guard is enabled and an eligible backend terminal candidate is received, the proxy shall keep that candidate provisional until the configured guard decision resolves.

1.3. **While** a terminal candidate is provisional, the proxy shall not expose that candidate as a final A-side terminal marker, terminal event, or equivalent end-of-response signal.

1.4. **While** Agent Loop Guard is enabled, the proxy shall preserve streaming-first delivery of legal non-terminal output and shall not require buffering the complete response merely to decide whether its terminal is final.

1.5. **If** a condition is outside the feature's supported recovery or verification scope, the proxy shall preserve the applicable existing terminal/error behavior rather than attempt undefined autonomous recovery.

### Requirement 2: Canonical Terminal-Cause Classification

**Objective:** As a maintainer, I want terminal candidates classified by canonical facts so that recovery behavior is protocol- and provider-neutral.

#### Acceptance Criteria

2.1. **When** a backend attempt reaches a possible terminal boundary, the proxy shall distinguish at least clean normal completion, empty/near-empty normal completion, provider pause/deferred continuation, output/token limit, transport EOF/reset, idle timeout, partial tool-call state, refusal/content-filter termination, and client cancellation when the canonical protocol exposes those facts.

2.2. **When** a transport EOF/reset or idle timeout occurs, the proxy shall distinguish whether meaningful A-side output from that attempt has already been committed.

2.3. **When** a terminal cause is classified, the decision shall be based on canonical request/stream state rather than provider-name checks in core policy.

2.4. **If** a terminal cause cannot be classified with sufficient confidence for autonomous recovery, the proxy shall choose a conservative terminal/error outcome and shall not create new model work solely to resolve the ambiguity.

2.5. **When** a terminal condition represents client cancellation, refusal/content filtering, or another explicit non-recoverable control outcome, the proxy shall not reinterpret it as unfinished agent work.

### Requirement 3: Pre-Output Transport Recovery

**Objective:** As a proxy user, I want transient failures before any visible output recovered transparently so that harmless backend faults do not break the agent loop.

#### Acceptance Criteria

3.1. **When** a transport failure occurs before meaningful A-side output from the affected attempt has been committed, the proxy shall delegate recovery to the existing configured pre-output recovery/retry/failover policy.

3.2. **While** a replay-safe pre-output recovery sequence remains in progress, the proxy shall not emit an intermediate A-side terminal event for the swallowed attempt.

3.3. **When** an empty or effectively empty clean completion is eligible for existing replay-safe recovery before output commitment, the proxy shall be able to discard that attempt and retry within the existing bounded recovery policy.

3.4. **If** the existing transport recovery policy is disabled or exhausted, the proxy shall surface the applicable final terminal/error exactly once.

3.5. **When** the client cancels the request, the proxy shall terminate recovery and shall not start or continue a hidden retry or continuation on the client's behalf.

### Requirement 4: Post-Output Interruption Safety

**Objective:** As a proxy user, I want interrupted work resumed without duplicated output or side effects after visible progress has already been delivered.

#### Acceptance Criteria

4.1. **When** meaningful A-side output from an attempt has been committed, the proxy shall not replay, silently fail over, or restart that committed attempt as if no output had occurred.

4.2. **When** a post-output transport interruption leaves a canonically resumable trajectory, Agent Loop Guard shall preserve the committed trajectory and may start a continuation from the last safe point without retracting prior A-side output.

4.3. **When** a continuation follows a post-output interruption, the proxy shall not duplicate already committed assistant content in the A-side stream.

4.4. **When** a completed tool call and its matching result are already part of the retained trajectory, recovery shall preserve those facts and shall not autonomously re-execute the completed side effect merely because the response stream later failed.

4.5. **If** recovery state contains incomplete tool-call arguments, an unresolvable provider-owned opaque/thinking continuation state, or another state that cannot be resumed without guessing or replaying unsafe work, the proxy shall not execute, fabricate, or blindly replay that state.

4.6. **If** safe post-output continuation is unavailable or its bounded recovery is exhausted, the proxy shall surface one final terminal/error outcome while preserving already committed output.

### Requirement 5: Semantic Verification of Eligible Clean Stops

**Objective:** As an unattended agent user, I want suspiciously premature but syntactically clean completions checked before they are allowed to stop my agent loop.

#### Acceptance Criteria

5.1. **When** Agent Loop Guard is enabled and an eligible clean normal completion occurs with no unresolved tool boundary, the proxy shall obtain an independent completion verdict before releasing the held A-side terminal.

5.2. **When** the verifier concludes that the requested work is complete, the proxy shall release the terminal without creating an additional model turn.

5.3. **When** the verifier concludes that concrete work already requested by the user remains unfinished and can proceed without additional user input, the proxy shall suppress the candidate terminal and start a bounded continuation.

5.4. **When** the next legitimate step requires user input, approval, permission, credentials, clarification, or a user choice, the proxy shall end normally so the user can respond and shall not synthesize that response.

5.5. **When** the session is genuinely blocked by an external condition that autonomous continuation cannot resolve, the proxy shall end normally rather than repeatedly ask the worker model to continue.

5.6. **If** semantic verification times out, fails, returns malformed output, or remains uncertain, the proxy shall treat the candidate as allowed to stop and shall not autonomously continue.

5.7. **Where** an explicit frontend completion signal is available and configured as trusted, the proxy shall treat that signal as strong completion evidence and may bypass semantic continuation for that clean stop while retaining transport-failure protection.

### Requirement 6: Conditional Recovery Without Scope or Authority Expansion

**Objective:** As a user, I want automatic continuation to resume only my unfinished request, never to invent follow-up work or pretend I authorized something new.

#### Acceptance Criteria

6.1. **When** the proxy starts a semantic continuation, the recovery instruction shall explicitly state that it is automated internal recovery and is not a new user request, approval, permission, or expansion of scope.

6.2. **When** the original requested work is already complete, the recovery instruction shall direct the worker to end normally without inventing, repeating, broadening, optimizing, or discovering additional work.

6.3. **When** concrete unfinished work remains, the recovery instruction shall constrain continuation to that existing work and the last safe point in the retained trajectory.

6.4. **When** further progress requires user input, approval, permission, credentials, clarification, or a choice, the recovery instruction shall direct the worker not to assume it and to end normally for user input.

6.5. **When** an automated recovery instruction is sent to a backend, the proxy shall not expose or persist it to the A-side as if it were a user-authored message.

6.6. **When** the verifier identifies a remaining objective, the proxy shall not treat suggestions, optional improvements, future possibilities, offers of help, or tasks assigned to the user as sufficient evidence of required unfinished work.

6.7. **When** a recovery event occurs, it shall not grant authority for a tool action that the existing user request and current policy did not already authorize.

### Requirement 7: Verification Evidence and False-Positive Resistance

**Objective:** As a proxy user, I want the completion check grounded in the actual task trajectory so that clean final answers are not misclassified from superficial wording.

#### Acceptance Criteria

7.1. **When** semantic verification runs, it shall consider the current user objective and relevant recent user instructions together with the candidate final assistant output and the available canonical tool/action trajectory.

7.2. **When** the candidate answer states that requested work is complete and available evidence does not contradict it, future-looking optional language such as “I can also…” shall not by itself require continuation.

7.3. **When** the candidate answer directly asks the user a question or offers an optional next action, that wording shall not by itself be treated as an unfinished executable objective.

7.4. **When** a “Next steps” section assigns actions to the user or merely recommends future work outside the current request, it shall not by itself trigger continuation.

7.5. **When** assistant text says it is about to perform an immediate in-scope action and the retained trajectory contains no evidence that the action occurred, the verifier shall be able to identify the stop as unfinished work.

7.6. **When** quoted or discussed text contains phrases such as “I’ll continue” but the assistant is not itself committing to that action, the verifier shall not classify the quotation alone as evidence that continuation is required.

### Requirement 8: Bounded Recovery and Progress Detection

**Objective:** As an operator, I want hidden recovery bounded and progress-aware so that the safety feature cannot create a new infinite loop or uncontrolled token spend.

#### Acceptance Criteria

8.1. **When** semantic continuation is enabled, the proxy shall enforce a configurable maximum number of hidden semantic continuation attempts per logical request.

8.2. **When** semantic verification is invoked, the proxy shall enforce a bounded verifier timeout and shall treat timeout as an allowed stop.

8.3. **When** successive continuation attempts reproduce materially equivalent final output, tool/action/error state, or recovery decisions without new canonical progress, the proxy shall detect no progress and stop further hidden continuation.

8.4. **When** a no-progress limit or semantic continuation limit is reached, the proxy shall release or surface exactly one final terminal/error outcome and shall not leave the logical request hanging.

8.5. **While** a verifier or recovery continuation is itself executing, the proxy shall prevent Agent Loop Guard from recursively applying to its own internal verifier operation.

8.6. **When** a continuation makes new canonical progress, the proxy may reset only the no-progress state justified by that new progress and shall not reset the logical request's maximum continuation budget.

### Requirement 9: Logical Request, Attempt, and Terminal Integrity

**Objective:** As a maintainer, I want hidden recovery to preserve Go-LIP's existing attempt/request ownership guarantees.

#### Acceptance Criteria

9.1. **When** an interrupted or semantically incomplete backend attempt is swallowed for continuation, that B-side attempt shall still reach exactly one attempt-level terminal settlement.

9.2. **While** Agent Loop Guard continues a logical request across hidden B-side legs, the A-side logical request shall remain open and shall preserve its request/session lineage.

9.3. **When** final completion, final failure, cancellation, or recovery exhaustion is reached, the A-side logical request shall terminalize exactly once.

9.4. **When** competing terminal, cancellation, close, or recovery outcomes race, existing terminal ownership rules shall determine one authoritative outcome without duplicate settlement or duplicate A-side terminal publication.

9.5. **When** output has already been committed, hidden continuation shall not be represented as a retry/replacement that weakens the existing post-commit retry prohibition.

9.6. **When** hidden recovery creates additional backend legs, billing, metering, authority, and B2BUA evidence for those legs shall remain attributable and shall not be merged in a way that conceals actual backend work.

### Requirement 10: Protocol-Neutral Streaming Continuity

**Objective:** As a frontend user, I want recovery to remain valid for my protocol and not produce malformed or provider-specific stitched streams.

#### Acceptance Criteria

10.1. **When** Agent Loop Guard suppresses an intermediate backend terminal, the A-side output shall remain a legal stream for the active frontend protocol.

10.2. **When** output from a continuation leg is exposed to the same logical A-side response, canonical item ordering, identifiers, tool-call correlation, and terminal semantics shall remain valid for the frontend protocol.

10.3. **When** a backend uses provider-native resumable state, core guard policy shall consume only normalized continuation capabilities/facts and shall not depend directly on provider SDK types.

10.4. **If** a frontend/backend combination cannot legally continue the existing logical stream, the proxy shall choose a supported final terminal/error behavior rather than concatenate raw protocol frames into an invalid response.

10.5. **When** non-streaming clients are served by collection over the canonical stream, they shall observe the same final guard decision semantics as streaming clients.

### Requirement 11: Observability and Privacy

**Objective:** As an operator, I want to understand recovery decisions and cost without leaking conversational content into telemetry.

#### Acceptance Criteria

11.1. **When** Agent Loop Guard evaluates a terminal candidate, the proxy shall expose bounded-cardinality telemetry for the canonical cause and final guard outcome.

11.2. **When** semantic verification runs, telemetry shall expose verifier latency and usage/cost evidence available through existing accounting mechanisms without recording prompt or response bodies in metrics.

11.3. **When** hidden retry or continuation is attempted, telemetry shall preserve the relationship among the logical A-side request and affected B-side legs using existing trace/lineage identifiers.

11.4. **When** recovery is suppressed for replay safety, unsupported state, explicit completion, no progress, cancellation, or exhausted budget, the proxy shall expose a bounded reason suitable for diagnostics.

11.5. **When** internal verifier/recovery instructions are used, operator diagnostics shall identify them as internal recovery activity rather than user-authored traffic while respecting existing sensitive-data logging policy.

### Requirement 12: Regression and Acceptance Safety Matrix

**Objective:** As a maintainer, I want a concrete regression matrix for common false-stop and false-continuation cases so that later refactors preserve behavior.

#### Acceptance Criteria

12.1. **When** a clean stop follows an assistant statement equivalent to “Let me run the tests next” and no test action occurred, semantic verification shall be able to continue the existing task.

12.2. **When** a clean stop follows a complete answer equivalent to “Done; tests pass,” semantic verification shall allow the stop when the trajectory does not show unfinished requested work.

12.3. **When** a clean stop ends with a user-directed question equivalent to “Would you like me to do X?”, Agent Loop Guard shall not synthesize user approval and shall allow the interaction to return to the user.

12.4. **When** a complete answer includes optional improvements or a user-owned “Next steps” list, those additions shall not by themselves trigger continuation.

12.5. **When** a pre-output transport EOF is recoverable, the proxy shall complete the bounded existing recovery path without leaking an intermediate A-side terminal.

12.6. **When** a post-output transport failure occurs, the proxy shall not replay already committed output or re-execute an already completed matching tool side effect.

12.7. **When** a transport interruption occurs during incomplete tool arguments, the proxy shall not execute guessed arguments or blindly replay the committed attempt.

12.8. **When** semantic verification fails or times out, the proxy shall allow the held terminal rather than manufacture a continuation.

12.9. **When** repeated recovery produces no canonical progress or reaches its maximum semantic continuation budget, the proxy shall emit exactly one final terminal/error outcome.

12.10. **When** any supported frontend protocol is exercised end-to-end, no provisional backend terminal shall become observable as the final A-side terminal before the guard has reached its final allow/abort decision.
