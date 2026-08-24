# Requirements Document

## Introduction

Issue #431 asks for economically safe A-leg cancellation in a B2BUA runtime: once the client-side A-leg is explicitly canceled or disappears, every related B-leg must stop promptly, future B-legs must not start, and cancellation must not destroy the usage evidence needed for per-leg financial accounting.

The existing code already satisfies important parts of that intent. PR #428 (`runtime-attempt-publication-ownership-convergence`) is now merged on `main` at merge commit `7a6c7532` and converges request/attempt ownership around a single attempt terminalization owner, ready-before-publication semantics, ready-owned unpublished cancellation, isolated parallel-arm reduction, and exactly-once B-leg settlement. This specification therefore does not redesign request lifecycle ownership. It formalizes the remaining cancellation launch-barrier, connector handshake, cancellation-result fidelity, provider-evidence preservation, and certification work on top of the merged #428 baseline.

## Boundary Context

- **In scope**: A-leg cancellation versus B-leg provider activation; cancellation of in-flight `Backend.Open`; concurrent cancellation of active B-legs; truthful cancellation mode/result propagation; backend-plugin in-band cancellation and bounded graceful-to-force fallback; backward-compatible connector ABI evolution; provider-only usage evidence preservation during cancellation; per-B-leg terminal billing correctness; cancellation TCKs, race/leak tests, and architecture ratchets.
- **Out of scope**: reimplementing PR #428 ownership convergence; selector grammar or routing policy changes; frontend API changes; canonical response/event redesign; monetary rating/journal redesign; enterprise/open-core separation; inventing provider cancel endpoints that do not exist; killing a shared connector process/connection when a per-attempt transport can be aborted; retry/failover after committed output.
- **Baseline**: implementation starts from merged PR #428 (`7a6c7532`) or a later descendant that preserves its `attemptSession.TerminalizeAttempt` single-owner, `readyAttempt` unpublished-cancellation, and ready-publication contracts. The prerequisite is satisfied at specification finalization.
- **Adjacent expectations**: existing B2BUA lineage, positive `AttemptSeq`, request-vs-attempt terminal lifetimes, BillingCallID-scoped usage records, provider-cost/customer-settlement separation, secure-session authority, and streaming-first execution remain authoritative.
- **Boundary ownership**: core owns A/B-leg orchestration and launch/cancel coordination; `pkg/lipsdk/backendplugin` owns the versioned connector cancellation contract; backend adapters/connectors own provider- or transport-specific physical cancellation; billing retains financial policy and durable post-usage settlement.
- **Revalidation triggers**: changes to PR #428 attempt terminal ownership/publication, `pkg/lipapi.ManagedEventStream`, backend-plugin ABI negotiation, B2BUA leg identity/sequence, terminal usage record semantics, or no-retry-after-output behavior require design revalidation.

## Requirements

### Requirement 1: Preserve the Post-#428 Attempt Ownership Baseline
**Objective:** As a runtime maintainer, I want cancellation hardening to extend the converged attempt lifecycle rather than create competing teardown paths, so that correctness improvements do not reintroduce split ownership.

#### Acceptance Criteria
1.1. The implementation shall preserve the merged PR #428 baseline: one production attempt terminalization authority, ready-owned unpublished cancellation, and ready-before-publication semantics.
1.2. When a B-leg succeeds, fails, is canceled, times out, is replaced, loses a race, or is denied publication, the system shall preserve exactly-once attempt terminal effects.
1.3. While an attempt terminalizes, request/A-leg terminal ownership shall remain logically separate except where existing request-terminal rules require completion.
1.4. When client-visible output has committed, cancellation handling shall not enable retry, failover, replacement, or duplicate provider activation.
1.5. The change shall preserve existing frontend wire behavior, canonical event ordering, selector semantics, secure-session decisions, and billing authority boundaries.
1.6. Where connector contract evolution is required, the change shall be additive and version-negotiated; it shall not require a breaking `pkg/lipapi` request/event redesign.

### Requirement 2: Linearize A-Leg Cancellation with B-Leg Provider Activation
**Objective:** As an operator, I want cancellation to win atomically against future B-leg launches, so that a canceled client request cannot start avoidable provider work.

#### Acceptance Criteria
2.1. When an A-leg cancellation transition wins before a B-leg reaches provider activation, the system shall not invoke that B-leg's backend `Open`.
2.2. When a B-leg receives permission to cross provider activation before A-leg cancellation wins, the system shall make that in-flight launch visible to the A-leg cancellation authority before releasing the launch decision.
2.3. When A-leg cancellation occurs while a permitted backend `Open` is in progress, the system shall cancel the context used by that `Open` promptly.
2.4. If an in-flight `Open` returns after A-leg cancellation, the resulting attempt shall not become published/current and shall be terminalized exactly once.
2.5. The no-post-cancel-launch rule shall apply to initial execution, ordered failover, retries/replacements, delayed parallel/hedged arms, and interleaved continuations that allocate new B-legs.
2.6. If a B-leg identity was allocated before cancellation but provider activation never began, the system shall preserve lineage and terminal accounting semantics without reporting provider work as having started.
2.7. A cancellation check performed only before or only after `Backend.Open` shall not be considered sufficient unless tests prove the activation decision and cancellation transition are linearized against the same authority.

### Requirement 3: Cancel All Active B-Legs Promptly and Independently
**Objective:** As an operator, I want one slow or non-cooperative backend to be unable to delay cancellation of sibling B-legs, so that multi-leg requests stop wasting provider resources promptly.

#### Acceptance Criteria
3.1. When an A-leg becomes canceled, the system shall signal cancellation to every active B-leg and every in-flight B-leg launch belonging to that A-leg.
3.2. While multiple active B-legs exist, cancellation signaling to one B-leg shall not wait for another B-leg's graceful cancellation or cleanup to complete.
3.3. When a B-leg supports graceful provider/protocol cancellation, the system shall attempt that cancellation within a bounded per-attempt grace interval.
3.4. If a B-leg does not reach terminal state within the grace interval, the system shall force-abort that B-leg's per-attempt transport or execution context without terminating unrelated attempts sharing the connector instance or provider connection.
3.5. When `Recv`, explicit A-leg cancel, request-context cancel, race-loser cancellation, timeout, and `Close` race, the physical upstream cancel and close operations shall remain at-most-once at the attempt owner.
3.6. If one or more B-leg cancellation attempts fail or time out, the A-leg shall remain monotonically canceled and the system shall aggregate/report those failures without reviving work.
3.7. The cancellation implementation shall not hold A-leg/slot coordination locks while invoking backend, connector, billing, observer, authority, or store I/O.

### Requirement 4: Report Truthful Cancellation Mode and Progress
**Objective:** As an operator and reviewer, I want cancellation results to describe what actually happened, so that economic and operational audits do not mistake intent for successful provider cancellation.

#### Acceptance Criteria
4.1. When a backend has a real provider/application-level cancel operation and uses it, the resulting cancellation mode shall report provider cancellation.
4.2. When cancellation is achieved by request-context cancellation, HTTP stream reset/body close, gRPC Execute cancellation, WebSocket abort, subprocess interruption, or equivalent transport action, the resulting mode shall report transport cancellation unless a stronger provider action was actually performed.
4.3. When only local close semantics are available, the resulting mode shall report close-only cancellation.
4.4. Enqueuing or transmitting a cancel command locally shall not by itself count as connector/upstream acknowledgment or terminal completion.
4.5. The host shall distinguish at least: cancellation requested, connector cancellation outcome received (when supported), and B-leg terminal/forced-abort completion.
4.6. If graceful cancellation times out or fails, `CancelResult.Err` or the equivalent internal terminal result shall preserve that failure while retaining the mode of the cancellation action that was actually attempted.
4.7. A lifecycle wrapper around the physical backend stream shall propagate the physical stream's cancellation mode/error rather than unconditionally reporting a stronger mode.
4.8. Low-cardinality cancellation mode, phase, and cause class shall be available to tests and diagnostics without exposing prompt, credential, or unbounded user data.

### Requirement 5: Make Backend-Plugin Cancellation a Real, Backward-Compatible In-Band Contract
**Objective:** As a connector maintainer, I want the existing Execute cancellation frames to control the active attempt they belong to, so that cancellation is not merely simulated by tearing down the outer gRPC transport.

#### Acceptance Criteria
5.1. When a negotiated connector receives `CLIENT_FRAME_KIND_CANCEL` after `START` on an active Execute stream, the connector-side execution helper shall consume that frame and apply it to the already-open upstream attempt.
5.2. The host shall propagate a bounded cancellation deadline on the cancel frame using the existing deadline field or its versioned equivalent.
5.3. When the connector processes cancellation, it shall emit a sequenced cancellation outcome describing acknowledgment and the actual cancellation mode when the negotiated protocol version supports that field.
5.4. When the canceled upstream attempt reaches terminal state, the connector shall emit at most one terminal frame with cancellation/failure semantics consistent with the attempt outcome.
5.5. `CLOSE_INPUT` shall remain distinct from cancellation; ordinary input closure shall not be misreported as provider cancellation.
5.6. The cancellation handshake extension shall use an additive backend-plugin protocol minor/feature gate so older external connectors remain negotiable.
5.7. When the handshake feature is not negotiated, the host shall fall back deterministically to per-attempt transport cancellation and shall not claim provider acknowledgment or provider cancellation mode.
5.8. Automatic gRPC/transport retries shall remain disabled for Execute and cancellation paths.
5.9. Connector cancellation shall act on the same Execute attempt rather than opening a second provider attempt solely to perform cancellation.

### Requirement 6: Preserve Provider-Billable Sideband Evidence During Cancellation
**Objective:** As a billing operator, I want provider accounting evidence generated before or during cancellation to remain attached to the B-leg that incurred it, so that teardown cannot erase cost evidence.

#### Acceptance Criteria
6.1. When a B-leg produces provider-only usage/accounting sideband evidence before terminalization, the system shall retain that evidence on the producing attempt even if no canonical output was surfaced.
6.2. While graceful cancellation is in progress, accounting frames received before terminal completion or force-abort shall remain drainable and eligible for terminal financial evidence.
6.3. When terminalization collects sideband evidence, it shall attribute it to the explicit B-leg owner and shall not re-read a mutable current-attempt slot that may already reference a replacement.
6.4. Duplicate sideband evidence carrying the same dedupe key shall not be counted twice across normal receive, cancellation, close, replacement, or terminal-drain paths.
6.5. Provider-only accounting evidence shall remain internal and shall not become a client-visible canonical response event.
6.6. Cancellation of the client/request context shall not prevent bounded evidence drain and terminal accounting work from running on an appropriate detached cleanup context.
6.7. If the physical transport is force-aborted, evidence already received before the abort shall remain usable; missing later evidence shall remain explicitly unavailable rather than inferred.

### Requirement 7: Produce Exactly One Correct Terminal Financial Record per Provider-Incurring B-Leg
**Objective:** As a billing operator, I want every B-leg that may have incurred provider work to close with the best available evidence, so that losing or canceled attempts are not silently under-accounted.

#### Acceptance Criteria
7.1. Every allocated B-leg shall produce exactly one terminal leg record or an idempotent replay, including never-started, swallowed, canceled, timed-out, parallel-loser, late-open, failed, and surfaced-winner outcomes.
7.2. When a B-leg may have incurred provider-billable work, its terminal record shall use the best available per-leg evidence regardless of whether completion tokens were surfaced to the A-leg.
7.3. Authoritative `FinalizeBilling` evidence shall take precedence when available; provider-reported sideband evidence shall be used as fallback/augmentation according to existing presence and cost-merge rules; weaker local estimates shall remain lower authority.
7.4. If no trustworthy usage/cost evidence is available, the terminal record shall mark evidence unavailable/estimated according to existing contracts and shall never invent authoritative zero usage or zero cost.
7.5. Backend `FinalizeBilling` shall remain at-most-once per B-leg within one `BillingCallID`, including cancellation races and parallel-loser cleanup.
7.6. Terminal financial work shall use the persisted B2BUA B-leg identity and positive attempt sequence; it shall not infer ordering from B-leg IDs, timestamps, or completion order.
7.7. Cancellation hardening shall not add stream-time customer rating, balance mutation, journal posting, or monetary hold semantics.
7.8. Customer settlement and provider COGS reconciliation shall retain their existing independent post-usage ownership; missing provider-cost readiness shall not block otherwise valid customer settlement.
7.9. Call closure shall freeze the exact expected B-leg set only after the runtime can no longer allocate another B-leg for that `BillingCallID`.

### Requirement 8: Preserve Recovery, Error, and Client-Visible Semantics
**Objective:** As a client integrator, I want stronger cancellation without changing successful-stream behavior or recovery rules, so that hardening is not a protocol regression.

#### Acceptance Criteria
8.1. When cancellation wins, the runtime shall not treat the cancellation as a recoverable pre-output backend failure that opens another candidate.
8.2. When cancellation or force-abort occurs after client-visible commitment, the runtime shall not retry/fail over and shall preserve existing partial-output/error semantics.
8.3. When a parallel winner is selected, loser cancellation shall not change the winner's canonical event order, commitment, affinity, interleaved memo, or terminal outcome.
8.4. When normal execution completes without cancellation, cancellation-handshake changes shall not add extra client-visible events or alter response semantics.
8.5. When an older connector uses the transport fallback path, transport death caused by intentional cancellation shall be classified as cancellation rather than an unrelated retryable provider failure.
8.6. Cancellation shall not allocate an additional B-leg, duplicate a provider request, or reopen a terminal attempt.
8.7. Existing secure-session, hook, completion-gate, prompt-cache, and compaction semantics shall remain unchanged except for receiving the same terminal outcome through the converged attempt owner.

### Requirement 9: Keep Cancellation Observable, Bounded, and Secret-Safe
**Objective:** As an operator, I want enough cancellation telemetry to prove behavior without creating high-cardinality or sensitive observability surfaces.

#### Acceptance Criteria
9.1. When cancellation is requested, diagnostics shall be able to identify A-leg/B-leg lineage, cancellation cause class, actual cancel mode, and whether graceful acknowledgment, terminal completion, timeout, or force-abort occurred.
9.2. Metrics labels shall use bounded enumerations and shall not include raw prompts, secrets, arbitrary cancel detail strings, or unbounded model/provider identifiers contrary to steering.
9.3. Cancellation diagnostics shall not log connector secrets, provider credentials, raw request bodies, or provider-private payloads.
9.4. No new public admin/API surface shall be required solely to satisfy observability; existing logs/metrics/test evidence may carry the proof.
9.5. When the connector cancellation feature is absent or downgraded, diagnostics shall expose the fallback class without treating it as a protocol violation.

### Requirement 10: Certify Cancellation and Economics Adversarially
**Objective:** As a reviewer, I want tests that fail on the current gaps and prove the final contracts across races and connector generations.

#### Acceptance Criteria
10.1. RED tests shall reproduce the post-cancel delayed-parallel launch window against the post-#428 baseline before the launch barrier is implemented.
10.2. RED tests shall prove cancellation during an in-flight backend `Open` reaches the Open context and prevents late publication.
10.3. Tests shall cover multiple active B-legs where one cancellation blocks/ignores graceful shutdown and prove siblings receive cancellation promptly.
10.4. Tests shall prove exactly-once physical cancel/close, attempt terminalization, B-leg release, `FinalizeBilling`, terminal leg append, and call-closure behavior under concurrent `Recv`, explicit cancel, context cancel, timeout, race-loser cleanup, and `Close`.
10.5. Backend-plugin tests shall cover negotiated handshake success, negative acknowledgment, missing acknowledgment, late terminal, cancel deadline expiry, force-abort, server-frame ordering, and legacy connector fallback.
10.6. The connector contract TCK shall prove that cancellation reaches the actual active upstream managed stream, not merely that a cancel frame was transmitted.
10.7. Billing tests shall cover cancellation with canonical usage, provider-only sideband usage, `FinalizeBilling` success, unsupported finalization, finalizer failure, duplicate sideband keys, and no-usage evidence.
10.8. Backend-family lifecycle tests shall certify truthful expected `CancelMode` behavior without requiring a provider-native cancel endpoint where the protocol only supports transport abort.
10.9. Repeated scheduling and supported race/leak tests shall report no data races, deadlocks, stuck B-leg goroutines, duplicate terminal effects, or leaked connector Execute pumps.
10.10. Architecture tests shall reject reintroduction of raw-stream A-leg registration after #428, non-linearized provider activation, alternate attempt terminal owners, cancellation-mode fabrication, and response-slot-based terminal evidence attribution.
10.11. Final verification shall include focused runtime/leglifecycle/backendplugin/adapter tests, connector contract tests, `internal/archtest`, `make quality-checks`, `make test-unit`, `make parity-checks`, and targeted race/goleak execution where supported.
