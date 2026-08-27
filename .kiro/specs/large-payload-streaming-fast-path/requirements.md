# Requirements Document

## Introduction

Issue #503 asks Go-LIP to reduce heap growth, allocation pressure, and redundant JSON work for large request bodies by forwarding a verified same-wire request without constructing the full canonical request tree. This is a brownfield optimization of already-supported request flows, not a new proxy mode and not native provider passthrough (#490).

Correctness has priority over optimization. The existing materialize → shared JSON preflight → decode admission → frontend decode/validation → canonical core → backend encode path is the behavioral oracle. A request may use the large-payload lane only when Go-LIP can prove, before any provider request body byte is committed, that doing so preserves every enabled authority and every relevant frontend/core/backend behavior. When proof is unavailable, incomplete, unsupported, or fails, the same request shall use the existing canonical path without weakening validation or silently disabling features.

This revision incorporates a post-PR brownfield audit against `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` (PR #533 merged). The typed extension-plane manifest/FrozenPlaneSet path is now the only target; decode QoS, frontend response-state dependencies, OpenResponses storage defaults, and post-identity `lipapi.Call` dependencies are explicit requirements. V1 uses a **one-way pre-`BeginTurn` eligibility commit point**: every expected optimization decline happens before logical-turn side effects, avoiding a high-risk secure-preparation split and post-identity canonical re-entry.

## Boundary Context

- **In scope**: optional large-payload ingress capture; bounded-memory pre-commit spooling; streaming JSON validation and bounded metadata extraction; decode-QoS-preserving semantic proof; explicit fast-path eligibility; same-wire backend capability proof; bounded token-aware model rewrite; retry/failover replay; frontend response-envelope parity; gzip-compatible follow-up support; cancellation/resource cleanup; metrics; differential conformance tests; allocation/load benchmarks; conservative rollout.
- **Out of scope**: native provider passthrough (#490); cross-protocol forwarding; removing or weakening canonical validation; changing default request-size limits; changing routing/failover semantics to make a request eligible; bypassing secret/DLP/guardrail policy; silently dropping hooks/observers/accounting; arbitrary new request content encodings; provider SDK types in core; broad capability-profile work owned by #495; expected post-`BeginTurn` canonical fallback in V1.
- **Boundary ownership**: shared frontend ingress owns capture/shared JSON validation, decode admission, protocol profile proof, frontend-only state, and canonical fallback; core/runtime owns authoritative eligibility, identity/session lifecycle, routing, billing/admission, attempt sequencing, and no-post-output-failover; backend plugins own wire-compatibility declaration and outbound HTTP construction; composition root freezes generation-scoped eligibility facts.
- **Revalidation triggers**: changes to JSON-shape rules (#434), frontend decode normalization or `AfterDecode`, decode admission, secure-session preparation, request/billing authority, request/attempt transforms, typed extension planes, routing/capability negotiation, traffic capture/redaction, failover/replay semantics (#476), backend wire contracts (#495), or frontend response-envelope/cancellation ownership.

## Requirements

### Requirement 1: Compatibility-First Activation and Lossless Fallback
**Objective:** Enabling the optimization must not silently bypass normal proxy behavior.

#### Acceptance Criteria
1. Disabled configuration shall execute every request through the existing canonical path with no spool/scanner/wire-path allocation beyond trivial configuration checks.
2. Any unknown, unsupported, unclassified, or false eligibility fact shall select canonical processing while the original decoded body remains replayable.
3. No upstream provider request body byte shall be committed before complete body validation and final pre-turn route/backend/authority eligibility proof.
4. If fast-path evaluation declines after consuming client bytes but before the V1 commit point, already-consumed and remaining bytes shall be presented to the canonical decoder exactly once and in order.
5. Optimization budget exhaustion, missing wire support, profile uncertainty, or recoverable spool failure shall not convert an otherwise valid canonical request into a new client error.
6. Existing HTTP/error mappings for malformed JSON, request-too-large, content type/encoding, authentication, decode admission, policy denial, and decode errors shall be preserved.
7. Existing `ExecutorView.Execute(ctx, *lipapi.Call)` remains supported and behaviorally unchanged for ordinary frontends/executors.
8. Expected fallback is an internal decision/result, not an error path and not error-level logging.

### Requirement 2: Threshold and Existing Request-Admission Semantics
**Objective:** The optimization must not broaden accepted traffic or create a second request limit.

#### Acceptance Criteria
1. A configurable decoded-size threshold controls consideration only; it never proves eligibility.
2. Known positive identity-body `Content-Length` below threshold shall take the canonical path without spooling.
3. Unknown/chunked length may enter bounded capture; if final decoded size is below threshold it shall materialize and continue canonically.
4. Each frontend's effective `MaxRequestBodyBytes` and protocol limits remain authoritative; this feature shall not change the current default request limit.
5. Configured limits above/below the default shall be honored exactly.
6. Exceeding the existing maximum shall produce the same request-too-large class/status and open no provider request.
7. `Content-Length` shall never be trusted as decoded length for compressed bodies.

### Requirement 3: Streaming JSON Safety Parity
**Objective:** Large bodies must receive the same shared JSON-shape protections without proportional heap growth.

#### Acceptance Criteria
1. The streaming scanner shall enforce the same normalized byte/depth/token/root/object/array/key/string/number bounds as current shared preflight.
2. It shall validate JSON string escapes, UTF-8, surrogate pairs, numbers, delimiters, incomplete/trailing values, and cancellation across buffer boundaries.
3. Ordinary large scalar content shall not be retained solely for validation.
4. Existing slice preflight shall remain the differential oracle; randomized/fuzz corpora shall compare pass/fail class and aggregate counts.
5. Shared scanner code shall remain provider-neutral and shall not duplicate protocol lexers.
6. Profile uncertainty shall select canonical decode rather than inventing a new approximation of a frontend error.

### Requirement 4: Bounded Metadata Extraction With Arbitrary Field Ordering
**Objective:** Routing/wire proof must not depend on fixed prefixes or field order.

#### Acceptance Criteria
1. Profiles shall locate required top-level fields irrespective of order and body size.
2. Selected values such as `model`, `stream`, bounded output-token controls, and protocol discriminators may be retained only within explicit bounds.
3. Exact raw byte offsets shall be available for bounded rewrites such as the top-level model token.
4. A nested key or matching text inside content shall never be mistaken for a selected top-level field.
5. Exceeding a metadata bound shall select canonical-only behavior, never truncation.
6. For OpenAI-style profiles, duplicate names in any protocol-owned object that canonical decode/re-encode would collapse or normalize shall be canonical-only unless that exact duplicate behavior is separately parity-certified; OpenResponses retains its stricter duplicate rejection behavior.

### Requirement 5: Explicit Generation-Pinned Eligibility for Every Request-Affecting Plane
**Objective:** Configured features must never disappear merely because a request uses the wire lane.

#### Acceptance Criteria
1. The current typed `pkg/lipsdk/feature` plane manifest/FrozenPlaneSet architecture is the sole production classification source; this spec shall not recreate a legacy named-field mirror.
2. Every declared production plane shall carry an explicit request-body access classification such as unclassified/canonical-required/metadata-only/response-only.
3. `unclassified` shall be a declaration/architecture-test failure for the production manifest and shall also fail closed to canonical-required at runtime as defense in depth.
4. Occupied request/content-mutating or content-inspecting planes without a typed wire contract shall block wire execution.
5. Response-only planes may remain active because provider responses continue as canonical events, subject to normal downstream parity tests.
6. Session/workspace/metadata planes require explicit proof that their inputs are available without request content; otherwise they block.
7. Generation reload shall pin the classification for the request lifetime.
8. A ratchet test shall fail whenever a new plane is added without an access classification.

### Requirement 6: One-Way Pre-Turn Wire Commit
**Objective:** V1 fallback must not require duplicate session/A-leg/accounting lifecycle or re-enter frontend decode after turn side effects.

#### Acceptance Criteria
1. Every **expected** optimization blocker shall be evaluated before `SecureSession.BeginTurn`, authoritative A-leg allocation/fetch side effects for the new turn, billing exposure admission, or provider open.
2. If an authority can reveal whether raw forwarding is safe only after A-leg/session state exists, V1 shall conservatively treat the presence of that authority as a pre-turn canonical blocker unless it has an explicit read-only pre-turn wire-safe contract.
3. A configured post-A-leg `RouteOverrideReader` is therefore canonical-only in V1 unless a later typed pre-turn proof is introduced; V1 shall not start a turn merely to discover that an override is active.
4. Route/affinity/weighted-first state that only selects among a known selector candidate set may remain active if wire compatibility is proven pre-turn for the conservative superset of every candidate that could later be selected.
5. When any pre-turn proof declines, core shall invoke trusted canonicalization and ordinary `Execute` while no large-body turn side effect has occurred.
6. Once all pre-turn proofs pass, the request crosses a one-way wire commit point. No **expected** canonical fallback occurs after `BeginTurn`; normal policy denials/provider failures are execution outcomes.
7. An unexpected post-commit invariant showing canonical content is required shall abort/finalize the single turn once and surface an internal parity diagnostic; it shall not invoke a second ordinary `Execute` or duplicate `BeginTurn`.
8. A future late-fallback/continuation optimization, if desired, requires a separate proof/spec and is not needed for #503 V1.

### Requirement 7: Explicit Same-Wire Backend Compatibility
**Objective:** Protocol name equality alone must never authorize raw forwarding.

#### Acceptance Criteria
1. Core shall invoke an explicit optional backend wire-compatibility contract for the exact frontend profile, operation, delivery mode, requirements, candidate model, and body mode.
2. Nil/zero wire support means canonical-only.
3. Every candidate in the conservative pre-turn reachable selector superset shall be proven compatible before the V1 wire commit point.
4. An incompatible possible fallback/race candidate shall cause whole-request canonical processing; the system shall not prune/reorder candidates to preserve the optimization.
5. Backend compatibility resolution shall not perform provider network I/O.
6. Backend proof shall be generation-pinned so a config reload cannot invalidate a request after commit.
7. External backend plugin ABI remains unchanged unless a separate compatibility extension is explicitly designed.

### Requirement 8: Safe Model Rewrite
**Objective:** Route/native-model rewriting must remain correct without materializing the body.

#### Acceptance Criteria
1. Rewrite shall use the scanner-recorded exact top-level model token span, never regex or bounded-prefix searching.
2. Replacement shall be a complete JSON token produced with normal JSON escaping.
3. The streaming splice reader shall emit prefix + replacement + suffix without constructing a second full body.
4. Span/size arithmetic shall be validated with checked `int64` math before provider open.
5. Ambiguous/duplicate/repaired model forms shall be canonical-only unless explicitly certified.
6. Different candidates may receive different native-model replacements while all other request bytes remain semantically unchanged.

### Requirement 9: Replay, Retry, Failover, and Race Semantics
**Objective:** Wire transport must preserve current retry/recovery ownership.

#### Acceptance Criteria
1. The captured source shall be immutable after capture and provide independent offset-zero readers.
2. Every provider/credential retry obtains a fresh reader and closes it on all exits.
3. Parallel/race routes may use the wire lane only when independent readers and every candidate in the pre-proven superset supports the exact route/body semantics.
4. Current attempt budgets, affinity/interleaved state, weighted/race ordering, first-event commitment, and pre-output recovery classification remain authoritative.
5. No new retry/failover may occur after client-visible output.
6. Replay tests shall prove byte-complete delivery after a failed first attempt and independent concurrent cursor state.

### Requirement 10: Compression Is Staged
**Objective:** Compression support must not weaken decoded-byte limits or broaden scope accidentally.

#### Acceptance Criteria
1. Wave 1 shall route gzip requests through the existing canonical `reqbody.ReadAll` behavior.
2. A later wave may capture the decoded identity JSON stream only after matching current compressed/decoded limit semantics.
3. Decoded-body capture shall strip stale outbound content-encoding state and send valid identity JSON.
4. Brotli/zstd/deflate support is not introduced by #503.
5. Compressed `Content-Length` shall not be used for decoded-size threshold or reservation proof.

### Requirement 11: Backend HTTP and Credential Semantics
**Objective:** Raw-body transport must reuse established backend security/transport policy.

#### Acceptance Criteria
1. Wire adapters shall reuse existing endpoint/base-URL resolution, credential selection/cooldown, shared HTTP client/TLS/proxy settings, response limits, parsers, and error classification.
2. Client `Authorization`, hop-by-hop headers, stale transfer/content-encoding headers, and frontend-only headers shall not be blindly forwarded.
3. Core remains the owner of retry; hidden SDK retries that replay bodies independently shall be disabled/avoided.
4. Outbound content length shall reflect the actual rewritten body when known; otherwise valid streaming HTTP semantics may be used.
5. Provider response parsing shall still emit canonical `lipapi.Event` streams.

### Requirement 12: Traffic, Guardrails, Conversation, and Accounting Are Authorities
**Objective:** The optimization must not bypass safety/observability/accounting features.

#### Acceptance Criteria
1. Frontend-owned ingress traffic capture that requires the full `[]byte` shall gate to canonical processing before spooling unless/until it gains a streaming-compatible contract.
2. Core/composed raw capture, request traffic observation/redaction, secret guards, DLP/content policy, request transforms, submit hooks, local turns, conversation projection, and similar content stages shall block unless explicitly parity-certified for wire facts/body streaming.
3. Any configured conversation/session feature whose content/route effect is not fully decidable before the V1 commit point shall block rather than create a post-turn fallback.
4. Monetary admission, account/pricing identity, charge policy, token estimation, and context-size policies shall not use byte-count approximations as substitutes for current semantics.
5. Response usage settlement and response-only observers continue from canonical provider events.
6. No wire path may disable an enabled authority merely to improve eligibility.

### Requirement 13: Replay-Spool Resource Safety and Confidentiality
**Objective:** Pre-commit replay must stay bounded, deterministic, and safe for prompt data.

#### Acceptance Criteria
1. Memory retained by capture shall be bounded by configured per-request memory spool bytes plus fixed scanner/copy buffers and bounded metadata.
2. Spill files shall use unpredictable temp names without user/session/model data and restrictive owner-only permissions where the platform supports them.
3. Spool paths/body prefixes shall not appear in normal logs/metrics/traces.
4. Global logical spool reservation shall be bounded and released exactly once on success/fallback/cancel/error.
5. Reservation exhaustion is an optimization decline, not a new 413; documentation/metrics shall explicitly state that canonical fallback may still allocate according to the pre-existing path and therefore this budget is not a global memory-admission guarantee.
6. `Source.Close` shall be idempotent and shall not deadlock waiting for leaked readers; it marks the root closed and final deletion occurs when outstanding tracked readers close. No cleanup goroutine is required.
7. File create/write/read/remove failures, cancellation, server timeout, and Windows-style open-file deletion constraints shall be injection/state tested.
8. Operator documentation shall state that spool files can contain request plaintext and describe `spool_dir`, filesystem/volume protection, retention lifetime, and threat-model implications.

### Requirement 14: Conservative Protocol Certification Matrix
**Objective:** Only request subsets proven equivalent to canonical decode→encode may use raw forwarding.

#### Acceptance Criteria
1. Each certified profile shall enumerate known fields/types, duplicate policy, required controls, normalization/repair triggers, protocol-requirement derivation, rewrite rules, frontend pre/post-decode side effects, and response-state dependencies.
2. Unknown/extra fields that canonical encoding would discard or normalize shall be canonical-only unless explicitly proven safe for that backend.
3. Malformed histories or aliases that current decoders repair/drop/normalize shall be canonical-only.
4. OpenResponses HTTP create is not eligible merely because `previous_response_id` is absent: current decode defaults `store=true`. The first OpenResponses wire certification shall require **explicit `store:false`**, absent `previous_response_id`, no compaction, and no WebSocket ingress unless storage/continuation and frontend `AfterDecode` parity are separately implemented and tested.
5. OpenAI Responses/Chat profiles shall conservatively decline proxy/session body metadata, legacy/alias forms, malformed tool/function history, and any frontend response-state dependency not covered by Requirement 18.
6. Profile broadening requires new differential corpus in the same change.
7. Protocol differential tests compare canonical-vs-wire provider-effective semantics and explicitly normalize only fields documented as protocol-opaque/non-deterministic.

### Requirement 15: Evidence-Based Performance and Eligibility
**Objective:** The feature must demonstrate real heap/GC benefit without becoming a permanently-falling-back code path.

#### Acceptance Criteria
1. Baselines and wire-path benchmarks shall cover 32 KiB, 256 KiB, 1 MiB, 5 MiB, and a test-only raised-limit 20 MiB body.
2. Record `allocs/op`, `B/op`, CPU time, capture/preflight/proof latency, provider-open latency, GC/heap behavior, and temp-file I/O.
3. Include concurrent load at realistic session counts and spool-budget saturation; compare against the disabled canonical baseline rather than claiming the spool budget itself prevents canonical heap pressure.
4. Include malformed/late-field/large-single-string and replay/failover workloads.
5. Publish an eligibility/fallback matrix for representative configurations, including standard extension planes, route-override authority on/off, and billing on/off.
6. At least one realistic supported configuration/lane shall actually reach wire execution in integration/load tests; a design that compiles but always falls back under normal composition is not considered complete.
7. Default threshold/memory-spool values are rollout defaults, not universal performance claims.
8. Performance claims shall state that V1 still receives and validates the complete request before provider open; the primary expected gain is heap/GC and redundant object/marshal work, not early provider TTFT.

### Requirement 16: Conservative Rollout, Diagnostics, and Architecture Ratchets
**Objective:** Operators must be able to enable/observe/revert the optimization safely.

#### Acceptance Criteria
1. Feature is default-off for the first release.
2. Metrics use bounded static labels only (frontend/profile/fallback-reason) and never backend/model/session/user IDs.
3. Traces/logs may record body size/spill/profile/rewrite/replay/fallback reason but never request content or spool path.
4. Architecture tests shall prevent provider-name switches in core, provider types in request-body SDK contracts, duplicate extension-plane classification systems, unclassified new planes, fake Calls, and expected fallback introduced after the V1 commit point.
5. Full QA/race/static checks and all canonical characterization suites must pass before enabling any production profile.
6. Spec/design shall be revalidated if main materially changes in any revalidation-trigger area before implementation begins.

### Requirement 17: Preserve Decode Admission / Decode QoS Exactly
**Objective:** Streaming proof must not bypass the existing byte-weighted concurrency guard around expensive protocol decoding.

#### Acceptance Criteria
1. The shared JSON lexical/shape pass may run while capturing the client body because current shared preflight precedes decode admission.
2. The implementation shall not hold a decode-admission permit while waiting on client network upload; this would change current permit occupancy and DoS characteristics.
3. After the complete decoded body size is known and shared preflight has passed, fast-path protocol semantic proof shall acquire `DecodeAdmission` with the same decoded-byte weight used by the canonical path.
4. The protocol semantic verifier shall run while that permit is held. A safe implementation may reopen the immutable source for a second low-allocation semantic pass rather than performing protocol proof during upload.
5. If the profile declines at this stage and canonical protocol decode is needed, that decode shall run under the same current admission/error/release semantics while still before executor entry.
6. Saturation/overweight/cancellation behavior and `Retry-After` mapping shall match current `decodeqos` tests for both canonical and wire-candidate requests.

### Requirement 18: Preserve Frontend Response/Envelope State Without a Fake Call
**Objective:** Bypassing canonical request materialization must not break response IDs, cancellation carriers, timestamps, wrappers, or frontend-specific response state.

#### Acceptance Criteria
1. A large-body execution API that returns only `lipapi.EventStream` is insufficient and shall not be the final contract if the frontend needs post-execution facts currently obtained from `*lipapi.Call` or `Decoded.Extra`.
2. The design shall define a bounded provider-neutral execution-result/response-facts contract (for example call/trace identity, authoritative A-leg/session identity, delivery/operation facts) and keep frontend-specific response state at the frontend boundary.
3. The wire path shall not fabricate a partial `lipapi.Call` solely to satisfy existing response encoders/loggers.
4. Each certified frontend shall inventory `BuildEncodeOpts`, `WrapStream`, response writers, cancellation endpoints, debug/trace calls, and `AfterDecode` state and either refactor them onto the bounded response contract or mark the profile canonical-only.
5. OpenAI Responses cancellation IDs shall remain correctly bound to the authoritative A-leg/session or otherwise retain equivalent cancellation semantics; optimized requests must not become uncancellable.
6. Opaque response IDs/timestamps may differ from the canonical implementation only when the protocol permits it and the certification explicitly documents/normalizes that field; semantic guarantees, uniqueness, correlation, cancellation/continuation behavior, and format remain required.
7. Pre-turn canonical fallback shall restore/use the frontend's ordinary canonical decoded/`AfterDecode` state exactly once; core shall not own arbitrary frontend/provider state.

### Requirement 19: Close Every Post-Identity `lipapi.Call` Dependency Before Wire Execution
**Objective:** The implementation must not discover late that downstream runtime machinery still requires canonical request content.

#### Acceptance Criteria
1. Before implementing wire execution, the project shall inventory every production read of `preparedRequest.call`, `identity.ingressCall`, canonical baselines, and `lipapi.Call`-typed callback used after the V1 wire commit.
2. Each dependency shall be classified as: pre-turn canonical blocker; exact metadata fact that can be added to a bounded wire-execution facts object; or response-only behavior covered by Requirement 18.
3. A ratchet/coverage test or generated inventory shall prevent new post-commit full-call dependencies from silently becoming wire-safe.
4. Routing, continuation support, interleaved-thinking setup, `recvTurnFacts`, billing identity/exposure, request-token/context estimation, terminal usage records, and any current full-call callback shall be included in this audit.
5. Stock billing composition shall be tested explicitly. If its policy/identity/max-output admission can be represented exactly from bounded wire facts, implement that typed path; otherwise billing-enabled requests remain canonical-only and the eligibility matrix must say so.
6. No consumer may receive a fake/partial canonical Call. If exact semantics cannot be represented without materialization, the request must be blocked before the V1 commit point.
