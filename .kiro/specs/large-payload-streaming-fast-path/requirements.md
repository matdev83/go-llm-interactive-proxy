# Requirements Document

## Introduction

Issue #503 asks Go-LIP to reduce heap growth, allocation pressure, and redundant JSON work for large request bodies by forwarding a verified same-wire request without constructing the full canonical request tree. This is a brownfield optimization of already-supported request flows, not a new proxy mode and not native provider passthrough (#490).

Correctness has priority over optimization. The existing materialize → shared JSON preflight → byte-weighted decode admission → frontend decode/validation → canonical core → backend encode path is the behavioral oracle. A request may use the large-payload lane only when Go-LIP can prove, before any provider request body byte is committed, that every enabled authority and every relevant frontend/core/backend behavior is preserved. Unknown or unsupported proof means canonical processing.

This revision is cross-checked against `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533. The typed extension-plane manifest/FrozenPlaneSet path is the only production classification source. The review also closes four additional brownfield gaps that are implementation blockers if left implicit:

1. canonical fallback after fast-path semantic proof must not decode outside `DecodeAdmission` or acquire a second, newly-failable permit;
2. wire execution cannot call current secure-session/A-leg preparation with a fake `lipapi.Call` and therefore needs explicit bounded session/identity facts plus a shared fact-only lifecycle primitive;
3. session request/response carriers, including sensitive resume authority, must be preserved explicitly;
4. spool-budget or spill-I/O decline after client bytes have already been consumed needs a concrete lossless canonical-continuation primitive.

V1 therefore uses a two-phase **plan then execute** seam. All expected eligibility decisions are completed while the original decode-admission grant is still available; canonical fallback remains frontend-owned. An accepted plan creates a one-way pre-`BeginTurn` wire commit. There is no expected canonical fallback after that point.

## Boundary Context

- **In scope**: optional large-payload capture; bounded-memory replay/spill; streaming shared JSON validation; bounded metadata/session extraction; decode-QoS-preserving semantic proof; side-effect-free core wire planning; same-wire backend proof; token-aware model rewrite; retry/failover replay; fact-based secure-session/A-leg preparation; frontend response/session-carrier parity; gzip follow-up; cancellation/resource cleanup; metrics; conformance/load evidence.
- **Out of scope**: native provider passthrough (#490); cross-protocol forwarding; weakening canonical validation; changing default request limits; changing route/recovery semantics merely to gain eligibility; bypassing guards/traffic/accounting; new compression formats; provider SDK types in core; broad #495 capability-profile work; expected post-`BeginTurn` canonical fallback; public stabilization of the low-level replay contract for external plugins in V1.
- **Boundary ownership**: frontend ingress owns capture, shared preflight, decode admission, protocol proof, normalized HTTP/session carriers, canonical fallback, frontend-only response state, and response writing; core owns side-effect-free wire planning, secure-session/A-leg lifecycle, routing, billing/admission, attempts, and canonical response events; backends own wire compatibility and outbound HTTP construction; runtime composition owns immutable generation-scoped eligibility/configuration.
- **Revalidation triggers**: changes to shared JSON rules (#434), `frontendpipe` ordering, decode admission, frontend `Decode`/`AfterDecode`, secure-session preparation, request/billing authority, typed planes, routing/recovery, traffic capture, backend wire contracts, session carriers, or frontend response/cancellation ownership.

## Requirements

### Requirement 1: Compatibility-First Activation and Canonical Oracle
**Objective:** Enabling the optimization must not silently change ordinary proxy behavior.

#### Acceptance Criteria
1. Disabled configuration shall execute every request through the existing canonical path with no spool/scanner/wire allocations beyond trivial immutable-gate checks.
2. Any unknown, unsupported, unclassified, or false eligibility fact shall select canonical processing while the body remains recoverable.
3. No provider request body byte shall be committed before complete body validation and an accepted pre-turn wire plan.
4. Expected optimization decline is an internal decision, not an error and not error-level logging.
5. Optimization budget exhaustion, missing wire support, profile uncertainty, or recoverable spill failure shall not turn an otherwise valid canonical request into a new client error.
6. Existing HTTP/error mappings for body limits, malformed JSON, content type/encoding, authentication, decode admission, frontend validation, policy denial, and executor errors shall remain authoritative.
7. Existing `lipsdk.ExecutorView.Execute(ctx, *lipapi.Call)` remains source-compatible and behaviorally unchanged.

### Requirement 2: Threshold and Existing Request-Admission Semantics
**Objective:** The optimization must not broaden accepted traffic or create a second request limit.

#### Acceptance Criteria
1. A configurable decoded-size threshold controls consideration only; it never proves eligibility.
2. Known positive identity-body `Content-Length` below threshold shall use the canonical path without spooling.
3. Unknown/chunked bodies may enter bounded capture; if final decoded size is below threshold they shall continue canonically.
4. Each frontend's effective `MaxRequestBodyBytes` and protocol limits remain authoritative; default limits are unchanged.
5. Configured limits above or below defaults shall be honored exactly.
6. Existing request-too-large classification/status shall be preserved and no provider shall be opened for an over-limit request.
7. Compressed `Content-Length` shall never be treated as decoded body length.

### Requirement 3: Streaming Shared-JSON Safety Parity
**Objective:** Large candidates must receive the same shared JSON hardening without proportional heap growth.

#### Acceptance Criteria
1. The streaming scanner shall enforce the same normalized byte/depth/token/root/object/array/key/string/number limits as current shared preflight.
2. It shall validate UTF-8, escapes/surrogates, numbers, delimiters, incomplete/trailing values, and cancellation across buffer boundaries.
3. Ordinary large scalar content shall not be retained solely for validation.
4. Existing slice preflight remains the differential oracle; randomized/fuzz corpora compare pass/fail class and aggregate counts.
5. Shared scanner code remains provider-neutral and contains no OpenAI/OpenResponses/backend policy.
6. Scanner/profile uncertainty falls back to the existing frontend decoder rather than inventing a new approximate client error.

### Requirement 4: Bounded Metadata Extraction With Arbitrary Field Ordering
**Objective:** Routing and protocol proof must not depend on prefixes or member order.

#### Acceptance Criteria
1. Profiles shall locate required top-level fields irrespective of legal order and body size.
2. Selected values such as model, stream, bounded output controls, and protocol discriminators may be retained only within explicit limits.
3. Exact raw byte offsets shall be available for bounded rewrites such as the top-level model token.
4. Nested keys or matching content text shall never be interpreted as selected top-level fields.
5. Metadata overflow means canonical-only; values are never truncated.
6. OpenAI-style duplicate protocol-owned members are canonical-only unless exact decode→encode parity is separately certified; OpenResponses retains its stricter duplicate-rejection behavior.

### Requirement 5: One Typed Extension-Plane Classification Architecture
**Objective:** Configured features must not disappear merely because a request uses the wire lane.

#### Acceptance Criteria
1. The current `pkg/lipsdk/feature` manifest/FrozenPlaneSet architecture is the sole production source of request-body access classification; no legacy named-field classification mirror shall be introduced.
2. Every production plane shall declare an explicit access class such as unclassified/canonical-required/metadata-only/response-only.
3. `unclassified` shall fail manifest/codegen/architecture validation and also fail closed at runtime as defense in depth.
4. Occupied request-content readers/mutators without a typed wire contract block wire planning.
5. Response-only planes may remain active because provider responses still become canonical events, subject to normal response-path parity.
6. Session/workspace/metadata planes are wire-safe only when their typed inputs are available from bounded non-content facts.
7. Reload publishes a new immutable classification generation; in-flight requests remain pinned to the generation they began with.
8. Adding a new plane without classification shall fail a ratchet test.

### Requirement 6: One-Way Pre-Turn Wire Commit
**Objective:** V1 must never need duplicate session/A-leg/accounting lifecycle or a second canonical execution.

#### Acceptance Criteria
1. Every expected optimization blocker shall be resolved before `SecureSession.BeginTurn`, new-turn A-leg side effects, billing exposure, or provider open.
2. Authorities whose wire-safety decision inherently requires post-A-leg/request-content state are V1 canonical blockers unless they gain an explicit read-only pre-turn contract.
3. A configured post-A-leg `RouteOverrideReader` is canonical-only in V1 unless such a contract is introduced.
4. Weighted-first/affinity/interleaved state may remain active only when planning proves wire compatibility for the conservative superset of every candidate it can later select.
5. A declined plan returns control to the frontend canonical path; core does not call frontend decoding itself.
6. An accepted plan establishes a one-way wire commit. There is no expected canonical fallback after `BeginTurn`.
7. An unexpected post-commit invariant requiring canonical content aborts/finalizes the single turn once, emits bounded parity diagnostics, and never invokes a second `Execute`.
8. Late canonical continuation is a separate future design, not required by #503 V1.

### Requirement 7: Preserve Decode Admission With a Single Grant
**Objective:** Fast-path proof and fallback must not bypass or race the current byte-weighted decode concurrency authority.

#### Acceptance Criteria
1. Shared lexical/shape scanning may run during client capture because current shared preflight precedes decode admission.
2. A decode permit shall never be held while waiting for client network upload.
3. After exact decoded body size is known and shared preflight passes, the candidate shall acquire the same `DecodeAdmission` weight as canonical decode.
4. Protocol semantic proof runs while that grant is held.
5. The final side-effect-free core `PlanLargeBody` decision shall be completed before releasing that grant so a plan decline can run the existing `Spec.Decode` under the **same** admission grant; there shall be no second `TryAdmit` race and no canonical decode outside admission merely because optimization was considered.
6. `PlanLargeBody` while a decode grant is held must be bounded and side-effect-free: no provider/network I/O, DB/store/A-leg/session reads, waiting on client bytes, unbounded plugin calls, or blocking filesystem work. If a required eligibility fact cannot satisfy this rule, it is an earlier canonical blocker.
7. Plan acceptance releases the decode grant exactly once before `ExecuteLargeBody` begins turn/provider work. Plan decline runs canonical `Spec.Decode` under the same grant, then releases it at the same logical frontend boundary as ordinary decode before `AfterDecode`/executor work.
8. Saturation, overweight, cancellation, panic-safe release, and `Retry-After` behavior shall match current `decodeqos` contracts. Tests shall also measure the bounded planning hold extension and reject regressions that materially reduce decode-admission capacity.

### Requirement 8: Side-Effect-Free Core Wire Planning
**Objective:** The frontend needs a deterministic final eligibility answer before committing the logical turn.

#### Acceptance Criteria
1. The optional internal large-body seam shall expose a planning operation conceptually equivalent to `PlanLargeBody(ctx, Metadata) -> PlanDecision/Plan` and a separate execution operation consuming only an accepted plan plus replay source.
2. Planning may inspect immutable generation configuration, typed-plane access summary, static callback capabilities, selector aliases/model registry, conservative candidate superset, and backend wire declarations.
3. Planning performs no BeginTurn/A-leg/billing/provider/network/store side effects.
4. A decline carries a bounded static reason and returns control to frontend canonical processing.
5. An accepted plan freezes or fingerprints every fact whose later drift could reveal a new expected blocker. `ExecuteLargeBody` shall treat plan/generation mismatch as an invariant error, not a canonical fallback.
6. The HTTP client cannot select a wire plan directly; the plan is derived only from trusted scanner/profile/runtime evidence.

### Requirement 9: Explicit Same-Wire Backend Compatibility
**Objective:** Protocol-name equality alone must never authorize raw forwarding.

#### Acceptance Criteria
1. Planning shall invoke an explicit optional backend wire-compatibility contract for exact profile, operation, delivery mode, requirements, candidate model, and body mode.
2. Nil/zero support means canonical-only.
3. Every candidate in the conservative pre-turn reachable superset shall be proven compatible before plan acceptance.
4. An incompatible fallback/race candidate causes whole-request canonical processing; candidates are not pruned/reordered/serialized to retain optimization.
5. Compatibility resolution performs no provider network I/O and is safe to execute under the bounded planning rule in Requirement 7.
6. Compatibility facts are generation-pinned.
7. External backend plugin ABI remains unchanged in V1 unless a separate versioned extension is deliberately designed.

### Requirement 10: Safe Token-Aware Model Rewrite
**Objective:** Native-model rewriting must remain correct without materializing the body.

#### Acceptance Criteria
1. Rewrite uses the scanner-recorded exact top-level model token span, never regex or prefix searching.
2. Replacement is a complete JSON token produced using normal JSON escaping.
3. A splice reader emits prefix + replacement + suffix without constructing a second full body.
4. Span/size arithmetic is checked using `int64` before provider open.
5. Ambiguous/duplicate/repaired model forms are canonical-only unless separately certified.
6. Different candidates may receive different native-model replacements while all other request bytes retain certified semantics.

### Requirement 11: Replay, Retry, Failover, and Race Semantics
**Objective:** Wire transport must preserve current recovery ownership.

#### Acceptance Criteria
1. A completed source is immutable and provides independent offset-zero readers.
2. Every provider/credential retry receives a fresh reader and closes it on all exits.
3. Parallel/race routes use wire mode only when independent readers and every pre-proven candidate support exact semantics.
4. Existing attempt budgets, affinity/interleaved/weighted ordering, first-event commitment, and recovery classification remain authoritative.
5. No new retry/failover occurs after client-visible output.
6. Tests prove byte-complete replay after failed attempts and independent concurrent cursors.

### Requirement 12: Compression Is Staged
**Objective:** Compression must not weaken decoded-byte limits or broaden scope accidentally.

#### Acceptance Criteria
1. Wave 1 routes gzip through existing canonical `reqbody.ReadAll` before any fast-path capture.
2. A later wave may capture decoded identity JSON only after reproducing current compressed/decoded limit and error semantics.
3. Decoded capture strips stale outbound content-encoding state.
4. Brotli/zstd/deflate are not introduced by #503.
5. Compressed `Content-Length` is never used as decoded threshold or spool reservation proof.

### Requirement 13: Backend HTTP and Credential Semantics
**Objective:** Raw-body transport must reuse established backend security/transport policy.

#### Acceptance Criteria
1. Wire adapters reuse existing endpoint/base-URL resolution, credential selection/cooldown, shared HTTP/TLS/proxy settings, response limits, parser, and failure classification.
2. Client Authorization, hop-by-hop, stale transfer/content-encoding, and frontend-only headers are not blindly forwarded.
3. Core remains retry owner; hidden SDK retries that independently replay bodies are disabled/avoided.
4. Outbound length reflects the actual rewritten body when known; otherwise valid Go HTTP streaming semantics are used.
5. Provider responses still produce canonical `lipapi.Event` streams.

### Requirement 14: Traffic, Guardrails, Conversation, Metering, and Accounting Remain Authorities
**Objective:** The optimization must not create a security, observability, or accounting blind spot.

#### Acceptance Criteria
1. Frontend ingress traffic requiring a complete `[]byte` gates to canonical processing before spool creation unless a separately reviewed streaming contract exists.
2. Core/composed raw capture, request traffic observation/redaction, secret guards, DLP, request transforms, submit hooks, local turns, conversation projection, and similar content stages block unless explicitly parity-certified for wire facts/body access.
3. Any session/conversation feature whose content/route effect is not fully decidable before plan acceptance blocks.
4. Monetary admission, account/pricing identity, charge policy, token estimation, and context-size policies shall not substitute body-byte approximations for existing semantics.
5. Response usage settlement and response-only observers continue from canonical provider events.
6. No enabled authority may be disabled merely to improve eligibility.

### Requirement 15: Replay-Spool Safety, Mid-Capture Recovery, and Confidentiality
**Objective:** Pre-commit replay must stay bounded and a recoverable optimization failure must never lose consumed bytes.

#### Acceptance Criteria
1. Capture retains at most configured per-request memory spool bytes plus fixed scanner/copy buffers and bounded metadata before spill.
2. Spill files use unpredictable non-user-derived names and restrictive owner-only permissions where supported.
3. Spool paths/body prefixes never appear in normal logs/metrics/traces.
4. Global logical spool reservation is bounded and released exactly once on wire success, canonical continuation, cancellation, or error.
5. Reservation exhaustion is an optimization decline, not a 413; docs state canonical fallback may still allocate according to pre-existing behavior and this budget is not global OOM admission.
6. Capture shall retain ownership of the **current unread/current-chunk bytes until the corresponding memory/file write succeeds**. Short/partial writes must not discard the unwritten suffix.
7. A dedicated canonical-continuation primitive shall concatenate the successfully retained prefix (memory/file), any current unwritten bytes, and the still-unread client body exactly once under the existing request-size ceiling. It may materialize the canonical `[]byte` because this branch has explicitly abandoned the optimization.
8. Mid-capture reservation decline or recoverable file create/write failure shall use that continuation primitive rather than restart/re-read the network body or return a new client error.
9. Root `Source.Close` is idempotent/nonblocking with respect to active readers; final deletion occurs when tracked readers close. No cleanup goroutine is required.
10. File create/write/read/remove failures, short writes, cancellation, timeout, and Windows open-file deletion behavior are injection/state tested.
11. Operator docs state that spool files can contain request plaintext and describe storage protection/lifetime/threat-model implications.

### Requirement 16: Conservative Protocol Certification Matrix
**Objective:** Only request subsets proven equivalent to canonical decode→encode may use raw forwarding.

#### Acceptance Criteria
1. Each profile enumerates fields/types, duplicate policy, required controls, normalization/repair triggers, protocol-requirement derivation, rewrite rules, header/session carriers, frontend pre/post-decode side effects, and response-state dependencies.
2. Unknown/extra fields discarded or normalized by canonical encoding are canonical-only unless explicitly proven safe for that backend.
3. Malformed histories or aliases repaired/dropped/normalized by current decoders are canonical-only.
4. OpenResponses first certification requires HTTP create, **explicit `store:false`**, absent `previous_response_id`, no compaction, and no WebSocket; missing `store` remains canonical because current default is true.
5. OpenAI Responses/Chat profiles conservatively decline body-carried proxy/session metadata, legacy/alias forms, malformed tool/function history, duplicate protocol-owned members, and unresolved response-state behavior.
6. Profile broadening requires new differential corpus in the same change.
7. Differential tests compare canonical-vs-wire provider-effective semantics and normalize only explicitly documented protocol-opaque/nondeterministic fields.

### Requirement 17: Evidence-Based Performance and Eligibility
**Objective:** The feature must demonstrate real heap/GC value and a realistic usable eligibility surface.

#### Acceptance Criteria
1. Baseline/wire benchmarks cover 32 KiB, 256 KiB, 1 MiB, 5 MiB, and test-only raised-limit 20 MiB bodies.
2. Record allocs/op, B/op, CPU, capture/preflight/proof/planning/provider-open latency, GC/heap behavior, and temp-file I/O.
3. Include realistic concurrency, slow uploads, decode-admission occupancy, and spool-budget saturation versus the disabled canonical baseline.
4. Include malformed/late-field/giant-string and replay/failover workloads.
5. Publish an eligibility matrix covering representative extension planes, route-override authority, stock billing, session resume carriers, frontend traffic, route strategies, and each certified lane.
6. At least one production-like configuration/lane shall actually reach wire execution; a permanently-falling-back implementation is incomplete.
7. Default threshold/spool values are rollout defaults, not universal performance claims.
8. Claims state that V1 still receives and validates the complete request before provider open; expected gain is heap/GC/redundant object/marshal work, not early-upload TTFT.

### Requirement 18: Rollout, Generation Pinning, Diagnostics, and Architecture Ratchets
**Objective:** Operators must be able to enable, observe, reload, and revert the optimization safely.

#### Acceptance Criteria
1. First release is default-off and explicit opt-in.
2. Fast-path ingress configuration, plane access summary, planner/backend proof, and execution must come from the same immutable runtime generation; a `frontendpipe` `sync.Once` cache shall not silently pin stale threshold/spool settings across reload unless the whole handler is generation-rebuilt.
3. Metrics use bounded static labels only and never backend/model/session/user IDs.
4. Traces/logs may record sizes/spill/profile/rewrite/replay/fallback reason but never body, spool path, resume token, or other session bearer authority.
5. Architecture tests prevent provider-name switches in core, fake Calls, duplicate plane-classification systems, unclassified planes, canonical decode after admission release due to optimization fallback, route candidate pruning, and expected fallback after wire commit.
6. Full QA/race/static/conformance suites pass before production profiles advertise support.
7. Spec is revalidated if a listed brownfield trigger changes before implementation.

### Requirement 19: Preserve Frontend Response and Session-Carrier Semantics Without a Fake Call
**Objective:** Skipping canonical request materialization must not break response IDs, cancellation, response headers, wrappers, or frontend state.

#### Acceptance Criteria
1. A wire execution result shall carry bounded provider-neutral authoritative response facts needed by the frontend; EventStream-only is insufficient where current writers depend on Call/Decoded state.
2. Frontend-specific state remains at the frontend boundary and never moves into core.
3. No partial/fake `lipapi.Call` is fabricated for response encoders, streamdebug, or cancellation code.
4. Each certified frontend inventories `BuildEncodeOpts`, `WrapStream`, response writers, `sessionwire.WriteResponseCarriers`, cancellation endpoints, debug/trace helpers, and `AfterDecode` state.
5. OpenAI Responses cancellation IDs remain bound to authoritative A-leg/session semantics; optimized requests remain cancellable.
6. Any secure-session response carrier currently emitted by the canonical path, including a resume bearer if/when present, is represented explicitly in sensitive response facts or the profile is canonical-only. Sensitive facts are never logged or metric-labelled.
7. Opaque IDs/timestamps may differ only when protocol-legal and explicitly normalized in conformance; uniqueness, correlation, cancellation/continuation behavior, and format remain required.

### Requirement 20: Close Every Full-Call Dependency At or After Wire Commit
**Objective:** Wire execution must not discover after plan acceptance that lifecycle/runtime machinery still requires canonical request content.

#### Acceptance Criteria
1. Before implementing wire execution, inventory every production Call dependency from the intended commit point onward, not only `preparedRequest.call`: include secure/detached preparation inputs, `identity.ingressCall`, canonical baselines, Call-typed callbacks, routing, billing, `recvTurnFacts`, continuation support, interleaved-thinking, terminal usage, and response helpers.
2. Each dependency is classified as pre-plan blocker, exact bounded fact shared with canonical helpers, or response-only behavior.
3. A ratchet/coverage test prevents a new post-commit full-Call dependency from silently becoming wire-safe.
4. Stock billing is tested explicitly. Exact fact-compatible standard billing may gain a typed wire path; arbitrary Call-shaped callbacks remain pre-plan blockers.
5. No consumer receives a fake/minimal Call. If exact semantics cannot be represented without materialization, planning declines.
6. Detached-session execution is canonical-only in V1 unless its Call dependencies and lifecycle are explicitly factored and parity-tested.

### Requirement 21: Exact Session/Identity Facts and Shared Fact-Only Turn Preparation
**Objective:** Wire mode must enter the same secure-session/A-leg lifecycle without using a canonical request shell.

#### Acceptance Criteria
1. Frontend profiles shall lift trusted header-derived session carriers using the same configured header names/precedence as current `sessionwire.ApplyAuthoritativeHeadersNamed`; raw `http.Header` does not cross into core.
2. Bounded request metadata shall include every exact non-content session/identity fact required to reproduce canonical BeginTurn semantics, including authoritative session ID/resume authority and any client session hint that the certified frontend currently supplies. Resume tokens are sensitive bearer data and must never be forwarded to backends or telemetry.
3. Body-carried session/proxy metadata remains canonical-only in initial OpenAI lanes unless the profile explicitly validates and reproduces current precedence/limits.
4. After an accepted plan, core shall use a shared fact-only secure-session/A-leg primitive (or an equivalently characterized common helper) for principal/scope, session-open stage, workspace resolution, `SecureSession.BeginTurn`, A-leg fetch, and other exact identity outputs needed by both canonical and wire branches.
5. Extracting that primitive is **not** permission for late canonical fallback: characterization tests must prove the ordinary canonical path retains its current ordering/error/lifecycle semantics, and wire execution has no canonical re-entry after BeginTurn.
6. Any content-dependent stage currently interleaved with secure preparation remains a pre-plan blocker unless separately given a wire contract.
7. Wire response facts include the authoritative A-leg/session outputs required by frontend cancellation/correlation/session headers.

### Requirement 22: Keep the V1 Low-Level Seam Internal Unless an External Consumer Requires It
**Objective:** A performance implementation detail should not become permanent public SDK surface without need.

#### Acceptance Criteria
1. The replay source, planner result, rewrite plan, and large-body optional executor seam shall live under an internal provider-neutral package for V1 unless a concrete supported external plugin consumer requires public exposure.
2. Built-in `frontendpipe`, runtime, and internal backends may share this internal contract; existing public `lipsdk.ExecutorView` remains unchanged.
3. External/manual executors that implement only current public interfaces remain canonical-only and source-compatible.
4. If later promoted to `pkg/lipsdk`, that promotion requires an explicit API/ABI review and versioning decision rather than occurring incidentally inside #503.
