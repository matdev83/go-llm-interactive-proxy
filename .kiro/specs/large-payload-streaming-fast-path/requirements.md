# Requirements Document

## Introduction

Issue #503 asks Go-LIP to reduce heap growth, allocation pressure, and redundant JSON work for large request bodies by forwarding a verified same-wire request without constructing the full canonical request tree. This is a brownfield optimization of already-supported request flows, not a new proxy mode and not native provider passthrough (#490).

Correctness has priority over optimization. The existing materialize → shared JSON preflight → decode admission → frontend decode/validation → canonical core → backend encode path is the behavioral oracle. A request may use the large-payload lane only when Go-LIP can prove that all enabled authorities and relevant frontend/core/backend semantics are preserved. Unknown or unproven behavior always selects the existing canonical path.

This revision was cross-checked against `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533. The audit identified contracts that the original draft did not model strongly enough: the typed plane architecture, the still-separate hook bus, decode QoS, deterministic request/economic identity, secure-session recorder/session-response carriers, full-Call retention in metering checkpoints, standard route-override availability, frontend response state, OpenResponses `store=true` defaulting, lossless mid-capture recovery, and deep post-commit Call dependencies.

V1 therefore uses a **two-phase executor seam with a one-way wire commit**:

1. capture + shared JSON validation;
2. acquire the existing decode-admission permit;
3. run low-allocation protocol semantic proof and canonical semantic identity derivation under that permit;
4. while the same permit is still held, run a bounded side-effect-free core wire-eligibility assessment;
5. if assessment declines, run the ordinary canonical protocol decoder under the same permit and continue on the unchanged canonical path;
6. if assessment succeeds, release the permit and begin wire execution; from that point no expected canonical fallback is allowed.

This keeps all expected optimization declines before `SecureSession.BeginTurn`, avoids duplicate lifecycle/accounting effects, and avoids a fallback-induced second decode-admission decision.

## Boundary Context

- **In scope**: optional large-payload capture; bounded-memory replay/spill; lossless mid-capture canonical continuation; streaming shared JSON validation; bounded protocol proof and canonical identity digest; decode-QoS parity; side-effect-free pre-turn eligibility; typed extension/hook/non-plane authority classification; late-bound route-authority compatibility envelopes; exact/domain same-wire backend proof; bounded model rewrite; secure-session/metering wire views; retry/failover replay; frontend response/session-carrier parity; gzip follow-up; cancellation/resource cleanup; metrics; differential conformance; allocation/load benchmarks; conservative rollout.
- **Out of scope**: native provider passthrough (#490); cross-protocol raw forwarding; weakening canonical validation; changing default request-size limits; changing route/failover semantics to manufacture eligibility; bypassing secret/DLP/guardrail/accounting/traffic authorities; arbitrary new content encodings; provider SDK types in core; broad capability-profile work owned by #495; expected post-commit canonical fallback in V1; incidental public stabilization of the low-level replay/assessment API.
- **Boundary ownership**: frontend ingress owns capture, shared JSON validation, decode admission, protocol proof, exact canonical fallback decode, and frontend-only response state; core/runtime owns side-effect-free assessment, secure-session/B2BUA lifecycle, route authority, metering/accounting, attempts, and terminal ownership; backend plugins own exact/domain wire compatibility and HTTP construction; composition freezes generation-scoped eligibility summaries.

## Requirements

### Requirement 1: Compatibility-First Activation and Exact Fallback
**Objective:** Enabling the optimization must not silently change normal request behavior.

#### Acceptance Criteria
1. Disabled configuration shall use the existing canonical path with no spool/scanner/wire allocation beyond trivial configuration checks.
2. Any unknown, unsupported, unclassified, or false eligibility fact shall select canonical processing.
3. No provider request body byte shall be committed before complete body validation and successful side-effect-free wire assessment.
4. If wire consideration declines after client bytes were consumed but before wire commit, the retained/captured input shall feed the canonical path exactly once and in original order.
5. Optimization spool-budget exhaustion, missing wire support, profile uncertainty, or recoverable spill failure shall not turn an otherwise valid canonical request into a new client error.
6. Existing HTTP/error mappings for malformed JSON, request-too-large, content type/encoding, authentication, decode admission, policy denial, and protocol decode errors shall be preserved.
7. Existing `ExecutorView.Execute(ctx, *lipapi.Call)` remains supported and unchanged.
8. Expected fallback is a bounded internal disposition/metric, not error-level logging.

### Requirement 2: Existing Body Limits and Threshold Semantics
**Objective:** The feature must not create a second request-admission policy.

#### Acceptance Criteria
1. A configurable decoded-size threshold controls consideration only; it never proves eligibility.
2. Known positive identity-body `Content-Length` below threshold shall take the canonical path before spooling.
3. Unknown/chunked requests may be captured; if final decoded size is below threshold they shall materialize and continue canonically.
4. Existing frontend `MaxRequestBodyBytes` and protocol limits remain authoritative and defaults remain unchanged.
5. Configured limits above/below defaults shall be honored exactly.
6. Existing request-too-large status/error behavior shall remain and shall open no provider request.
7. Compressed `Content-Length` shall never be treated as decoded length.

### Requirement 3: Streaming Shared JSON Safety Parity
**Objective:** Large bodies receive the same shared JSON-shape protections without proportional heap retention.

#### Acceptance Criteria
1. The incremental scanner shall enforce the same normalized byte/depth/token/root/object/array/key/string/number limits as current shared preflight.
2. It shall validate UTF-8, escapes/surrogate pairs, number grammar, delimiters, incomplete/trailing values, and cancellation across buffer boundaries.
3. Ordinary large scalar content shall not be retained solely for validation.
4. Existing slice preflight remains the differential oracle; fuzz/random corpora compare pass/fail class and aggregate counts.
5. Shared scanning code remains provider-neutral; protocol field semantics live at frontend profiles.
6. Scanner/profile uncertainty selects canonical decode rather than an approximate new frontend error.

### Requirement 4: Bounded Protocol Proof With Arbitrary Field Ordering
**Objective:** The wire lane must prove canonical semantics without assuming metadata is early in the body.

#### Acceptance Criteria
1. Profiles shall find required top-level fields irrespective of order and body size.
2. Profiles may retain only explicitly bounded semantic facts such as model, stream mode, exact output controls, session/control carriers, protocol requirements, item-shape metadata, and raw rewrite spans.
3. Exact raw offsets shall be recorded for rewrites such as the top-level model token.
4. Nested keys or matching text inside user content shall never be mistaken for protocol metadata.
5. Exceeding a semantic-fact bound shall select canonical processing, never truncation.
6. Duplicate protocol-owned names that canonical decode/re-encode would collapse or normalize are canonical-only unless exact duplicate behavior is separately certified; OpenResponses retains its stricter duplicate rejection behavior.
7. Profile proof shall include the certified-subset `Call.Validate`/frontend normalization semantics required before executor entry.

### Requirement 5: One Generation-Pinned Wire-Eligibility Summary
**Objective:** Every production authority that can invalidate raw forwarding must be represented explicitly without creating a second extension architecture.

#### Acceptance Criteria
1. For typed extension planes, `pkg/lipsdk/feature/plane_manifest.go` / `feature.Plane[T]` / `FrozenPlaneSet` is the sole source of plane declarations; request-body access metadata shall extend that architecture rather than recreating named plane mirrors.
2. Each production plane shall declare an explicit access class such as unclassified/canonical-required/metadata-only/response-only/wire-contract-required.
3. `unclassified` shall fail declaration/codegen/architecture tests and shall fail closed at runtime.
4. The frozen generation shall derive a bounded `WireEligibilitySummary` from typed plane access plus **non-plane authorities that remain production architecture**, including hook-bus occupancy, frontend/core traffic facilities, secure-session recorder capability, metering/accounting mode, route-override capability, and other Call-shaped runtime callbacks discovered by the dependency inventory.
5. Legacy `hooks.Bus` shall not be assumed covered by the plane manifest. Occupied submit/request-part/tool chains that can inspect or mutate canonical requests are canonical-required unless given an explicit typed wire contract; response-only chains may remain active when proven response-only.
6. The summary shall be built at composition time without hot-path reflection, request-path map walks, or arbitrary plugin invocation.
7. Config reload shall pin the summary for request lifetime.
8. Ratchet tests shall fail when a new plane/hook/non-plane request authority is added without a wire classification.

### Requirement 6: Two-Phase Assessment and One-Way Wire Commit
**Objective:** Expected fallback must happen before turn side effects and without a second decode-admission decision.

#### Acceptance Criteria
1. After whole-body shared preflight, the frontend shall acquire the current byte-weighted `DecodeAdmission` permit and retain it through protocol semantic proof **and** side-effect-free core assessment.
2. `AssessLargeBody` (or equivalent) shall perform no `BeginTurn`, A-leg creation/fetch, DB/store read or mutation, billing reservation, provider/network I/O, client-body wait, spill I/O, or arbitrary unbounded plugin work.
3. If profile proof or core assessment declines, the existing canonical `Spec.Decode` shall execute while the same admission permit is still held; no release/reacquire cycle is allowed.
4. After canonical decode, the permit releases at the same logical boundary as today and the normal post-decode/`AfterDecode`/traffic/`Execute` path continues.
5. If assessment succeeds, the permit releases and the request crosses a one-way wire commit. `ExecuteLargeBody` may then begin the normal secure-session/A-leg lifecycle.
6. No **expected** post-commit condition may require canonical fallback. A newly discovered content dependency after commit is an internal invariant failure: finalize/abort the one turn once; never start a second ordinary `Execute`.
7. Assessment and execution use the same immutable generation/executor composition; execution revalidates an assessment stamp or equivalent immutable facts and treats disagreement as an invariant bug, not fallback.
8. The additional permit hold introduced by assessment shall be measured and bounded; assessment may iterate only generation-bounded data structures and pure backend declarations.
9. A future late-fallback mechanism requires a separate design; it is not required for #503 V1.

### Requirement 7: Late-Bound Route Authorities Need a Pre-Certified Domain
**Objective:** Standard route-override support must not make the fast path dead, while post-A-leg routing must not create a late fallback.

#### Acceptance Criteria
1. Mere presence of `RouteOverrideReader` shall **not** automatically block V1: standard continuity stores expose this capability and runtime composition wires it when available.
2. Before wire commit, core shall prove a generation-pinned **late-route compatibility envelope** covering every selector/backend/execution-mode/model domain that a post-commit route authority can legally choose under the current generation.
3. For session route overrides, the envelope shall derive from the same generation validator/known-backend/execution-composition policy that accepts override selectors. Because override model text is not necessarily a finite catalog, a backend whose wire compatibility is model-dependent may need to prove an `any accepted model`/equivalent universal model contract or make the envelope ineligible.
4. After `BeginTurn`, the normal authoritative route-override snapshot/barrier and affinity/weighted-first/interleaved selection may run unchanged because every possible outcome is already inside the proven envelope.
5. If the complete late-route domain cannot be proven wire-compatible, assessment shall decline before `BeginTurn`.
6. Route-hint or future selector-mutating authorities that can produce outcomes outside the exact initial selector set require their own bounded route-domain contract; otherwise they are canonical-required.
7. Eligibility/load evidence shall include homogeneous and heterogeneous backend generations so the cost of conservative route-domain proof is visible.

### Requirement 8: Explicit Same-Wire Backend Compatibility
**Objective:** Protocol/backend naming alone must never authorize raw forwarding.

#### Acceptance Criteria
1. Backends expose optional provider-neutral wire proof for the exact frontend profile, operation, delivery mode, protocol requirements, body mode, rewrite semantics, and candidate model.
2. Backends additionally expose a universal/domain proof when needed by late-bound route authorities; nil/unknown/partial proof means canonical-only.
3. Exact initial-route candidates and every member of any required late-route domain shall be proven before wire commit.
4. Incompatible candidates shall cause assessment decline; core shall not prune/reorder candidates, disable fallback/race, or otherwise change routing semantics to keep wire mode.
5. Compatibility resolution performs no provider network I/O and is generation-stable.
6. External backend plugin ABI remains unchanged unless a separately versioned capability extension is deliberately introduced.

### Requirement 9: Safe Model Rewrite
**Objective:** Native-model binding must remain correct without a second full request body.

#### Acceptance Criteria
1. Rewrite uses the scanner-recorded exact top-level model token span, never regex/prefix search.
2. Replacement is a complete JSON token encoded with normal JSON escaping.
3. A splice reader emits prefix + replacement + suffix without constructing a second full body.
4. Span/size arithmetic uses checked `int64` math before provider open.
5. Duplicate/ambiguous/repaired model forms are canonical-only unless separately certified.
6. Each candidate may receive its own native-model replacement while all other request bytes remain semantically unchanged.

### Requirement 10: Replay, Retry, Failover, and Race Semantics
**Objective:** Raw transport must stay inside the existing attempt/recovery authority.

#### Acceptance Criteria
1. Captured source is immutable after EOF and provides independent offset-zero readers.
2. Each provider/credential retry obtains and closes a fresh reader.
3. Parallel/race attempts require independent readers and pre-certified backend/body compatibility for every possible candidate.
4. Existing attempt budgets, affinity/interleaved state, weighted/race ordering, failure classification, B-leg lifecycle, first-event commitment, and pre-output recovery remain authoritative.
5. No new retry/failover occurs after client-visible output.
6. Replay tests prove complete second-attempt bytes and independent concurrent cursors.

### Requirement 11: Compression Is Staged
**Objective:** Compression support must not weaken decoded-byte protections.

#### Acceptance Criteria
1. Wave 1 routes gzip through the existing canonical `reqbody.ReadAll` path.
2. A later wave may capture the decoded identity JSON stream only after matching current compressed/decoded limit behavior.
3. Decoded-body wire requests remove stale outbound content-encoding state.
4. Brotli/zstd/deflate are not introduced by #503.
5. Compressed `Content-Length` is never used for decoded threshold/reservation proof.

### Requirement 12: Backend HTTP and Credential Semantics
**Objective:** Wire transport reuses established backend security/transport ownership.

#### Acceptance Criteria
1. Wire opens reuse endpoint/base-URL resolution, credential selection/cooldown, shared HTTP client/TLS/proxy policy, response limits/parsers, and failure classification.
2. Client Authorization, hop-by-hop headers, stale transfer/content-encoding headers, and frontend-only control headers are never blindly forwarded.
3. Core owns retry; hidden SDK retries that independently replay request bodies are disabled/avoided.
4. Outbound length reflects the actual rewritten body when known; otherwise valid streaming HTTP semantics are used.
5. Provider responses still become canonical `lipapi.Event` streams.

### Requirement 13: Content, Policy, Hook, and Traffic Authorities Remain Authoritative
**Objective:** Large bodies shall not bypass safety, feature, or observation stages.

#### Acceptance Criteria
1. Frontend ingress traffic features requiring a full body shall select canonical handling before spooling unless a streaming/wire contract exists.
2. Active secret guards, raw capture/redaction, submit hooks, request/request-part/pre-request/attempt transforms, conversation projection/steering, tool-catalog request changes, local-turn content logic, compaction preservers, or similar Call/content stages block unless explicitly wire-certified.
3. Response-only observers/gates may remain active if canonical provider events preserve their inputs/semantics.
4. Metadata-only session/workspace stages may remain active only with exact bounded inputs.
5. No authority may be disabled or skipped merely to make a request eligible.

### Requirement 14: Secure-Session Parity Without a Canonical Call
**Objective:** The standard secure-session lifecycle must remain usable on the wire path.

#### Acceptance Criteria
1. Wire facts shall carry the exact bounded client/session authority inputs that canonical `BeginTurn` receives, sourced through normal frontend/header/body precedence. Sensitive resume tokens never reach backends or telemetry.
2. Initial profiles may conservatively reject body-carried LIP session metadata while still supporting authoritative LIP session/resume headers; any supported body metadata must match current precedence exactly.
3. Standard secure-session recording shall not be blocked merely because the recorder is normally composed. Protocol proof shall produce a bounded **normalized client-turn shape** equivalent to `lipapi.NormalizedItems` for the certified subset (role/ordinal/content-part kinds and other recorder-required non-content facts).
4. If normalized item/part metadata exceeds configured semantic-fact bounds, the request shall be canonical.
5. Wire execution shall call the existing recorder through an equivalent bounded `ClientTurnRecordInput` path; prompt text shall not be materialized solely for this recorder.
6. New-session `BeginTurn` response carriers—including authoritative session ID, A-leg ID, and the raw resume token returned for a new session—shall return to the frontend through a **sensitive response-carrier contract** and preserve current session-header semantics.
7. Sensitive response carrier values are never metrics labels/log fields and are released with request lifetime.

### Requirement 15: Metering, Accounting, and Billing Need Wire-Native Evidence
**Objective:** Standard economic checkpoints must not silently retain a full Call and erase the memory benefit.

#### Acceptance Criteria
1. The wire path shall not call current Call-cloning frontend/backend metering checkpoint helpers merely to satisfy existing APIs.
2. Add wire-native frontend/backend ingress checkpoint capture from exact bounded facts: request/economic identity, scope, frontend/backend/attempt/A-leg/session correlations, model, request count, and exact max-output quantity.
3. Wire checkpoint storage shall not retain a hidden full canonical Call. Widening/retry integrity shall instead use immutable source/proof digests plus bounded rewrite/attempt evidence.
4. When token accounting/preflight is disabled, wire checkpointing shall remain fully usable without tokenization.
5. When configured accounting/context/billing requires input-token counting, eligibility requires an exact wire counting contract for the profile/backend/source. Raw body byte count is never substituted for tokens.
6. Stock billing/exposure/price/max-output/account identity paths shall be explicitly characterized. Exact bounded inputs should share helpers with canonical execution; arbitrary custom Call callbacks remain canonical blockers unless they opt into a wire contract.
7. Economic/metering facts, reservations, settlement, terminal usage, and retry idempotency occur exactly once and preserve current failure policy.
8. Eligibility evidence shall report accounting/billing on/off separately rather than hiding a production blocker.

### Requirement 16: Deterministic Request and Economic Identity Parity
**Objective:** Wire execution must not create a second identity namespace for the same logical request.

#### Acceptance Criteria
1. Inventory all production uses of `diag.StableCallID`, `StableCallToken`, `StableUnix`, Call.ID-derived checkpoint IDs, billing call IDs, response IDs/timestamps, trace IDs, and dedupe/source IDs.
2. A certified profile shall produce an **exact canonical semantic identity digest** equivalent to the current post-frontend-decode/pre-core canonical Call identity for its supported subset, without retaining proportional prompt content.
3. The canonical-digest implementation shall be differential-tested against the current `diag` hash over fully decoded canonical Calls for large strings, escapes/Unicode, tool/message shapes, session/header precedence, route selector/model, and all certified optional fields.
4. Helpers may derive current stable Call ID/token/timestamp from that digest so wire trace/economic identity and deterministic frontend fields remain path-stable.
5. A raw-body-only hash that differs from canonical stable identity is not sufficient for economic identity.
6. Caller/provider-supplied explicit IDs retain current precedence.
7. If exact canonical identity cannot be generated for a request shape, the profile shall decline to canonical processing.

### Requirement 17: Conservative Protocol Certification Matrix
**Objective:** Only request subsets proven equivalent to canonical decode→encode may use raw forwarding.

#### Acceptance Criteria
1. Each profile enumerates supported fields/types, duplicates, normalization/repair triggers, requirement derivation, session precedence, normalized recorder shape, identity digest rules, rewrite rules, frontend pre/post-decode side effects, and response dependencies.
2. Unknown/extra fields that canonical encoding discards or normalizes are canonical-only unless explicitly proven safe for the exact backend.
3. Malformed histories/aliases that current decoders repair/drop/normalize are canonical-only.
4. OpenResponses create is not eligible merely because `previous_response_id` is absent: current decoder defaults `store=true`. First certification requires **explicit `store:false`**, absent `previous_response_id`, no compaction, and no WebSocket unless continuation/storage parity is separately implemented.
5. OpenAI Responses/Chat initial profiles conservatively reject proxy/session body metadata, normalization-sensitive aliases/histories, and unresolved frontend response dependencies.
6. Profile broadening requires differential corpus in the same change.
7. Canonical-vs-wire tests compare provider-effective semantics and normalize only fields explicitly documented as protocol-opaque/non-deterministic.

### Requirement 18: Preserve Frontend Response and Session-Carrier State
**Objective:** Skipping the canonical Call must not break response IDs, cancellation, timestamps, wrappers, or session continuity.

#### Acceptance Criteria
1. Wire execution returns an `ExecutionResult` containing canonical events plus bounded provider-neutral response facts; EventStream-only is insufficient.
2. Response facts shall include authoritative request/trace identity, A-leg/session facts, operation/delivery, and a separately marked sensitive session-response carrier when required.
3. The wire path shall not fabricate a partial `lipapi.Call` to satisfy existing response encoders/loggers.
4. Each certified frontend inventories `BuildEncodeOpts`, `WrapStream`, response writers, cancellation endpoints, stream/debug helpers, session response carriers, and `AfterDecode`/Extra state; each dependency is refactored to bounded facts/frontend-owned state or blocks the profile.
5. OpenAI Responses cancellation IDs remain bound to authoritative A-leg/session semantics; wire requests must remain cancellable by the returned ID.
6. Deterministic response IDs/timestamps derived from canonical Call hashes use Requirement 16's equivalent digest unless the protocol explicitly treats the field as opaque and conformance documents a deliberate difference.
7. Frontend-specific state stays at the frontend boundary; core contracts do not import provider/frontend state.

### Requirement 19: Close Every Post-Commit Full-Call Dependency
**Objective:** No runtime component may accidentally force canonical materialization after wire commit.

#### Acceptance Criteria
1. Before wire execution implementation, create a production inventory of every read of `preparedRequest.call`, `identity.ingressCall`, canonical baseline clones, Call-retaining checkpoints, `lipapi.Call`-typed callbacks, and `diag` helpers used after the intended commit.
2. Known entries include routing/request-size/capability requirements, secure-session recorder input, frontend/backend metering checkpoints, accounting/token estimators, billing admission/identity/policy, `recvTurnFacts`, continuation support, interleaved-thinking recorders, terminal usage/session fields, traffic snapshots, and response helpers.
3. Every entry is classified as exact bounded wire fact/view, wire source/digest contract, response-only behavior, or pre-assessment canonical blocker.
4. Standard normally composed components such as secure-session recorder and metering checkpoint require an explicit wire-native path rather than being silently classified as permanent blockers.
5. A ratchet/AST/architecture test shall prevent new post-commit full-Call dependencies from being treated as wire-safe implicitly.
6. No consumer receives a fake/partial canonical Call. If exact semantics cannot be represented without materialization, assessment declines before wire commit.
7. Detached-session execution remains canonical-only until its Call/lifecycle dependencies are separately represented and parity-tested.

### Requirement 20: Replay-Spool Resource Safety, Lossless Recovery, and Confidentiality
**Objective:** Pre-commit replay stays bounded, deterministic, recoverable, and safe for prompt data.

#### Acceptance Criteria
1. Memory retained by capture is bounded by configured memory-spool bytes plus fixed scanner/copy buffers and bounded semantic facts.
2. Spill files use unpredictable names without user/session/model data and owner-only permissions where supported.
3. Spool paths/body prefixes do not appear in normal logs/metrics/traces.
4. Global logical spool reservation is bounded and released exactly once on success/fallback/cancel/error.
5. Reservation exhaustion is an optimization decline, not a new 413. Documentation states canonical fallback may still allocate according to the pre-existing path, so this budget is not global OOM admission.
6. Capture shall retain ownership of the **current input chunk/unwritten suffix until the corresponding memory/file write fully succeeds**. A short/partial write shall never discard bytes already read from the client.
7. On mid-capture reservation decline or recoverable create/write failure, a dedicated canonical-continuation reader shall expose: successfully retained memory/file prefix + current unwritten suffix + still-unread client body, exactly once and under the existing request-size ceiling.
8. Mid-capture canonical continuation shall never restart or reread the client socket; randomized chunk/fault tests shall compare its resulting bytes with a direct canonical read.
9. `Source.Close` is idempotent and shall not deadlock waiting for leaked readers; root close marks deletion pending and final removal occurs when tracked readers close. No cleanup goroutine is required.
10. File create/write/read/remove failures, cancellation, timeout, short writes, and Windows open-file deletion behavior receive injected/state tests.
11. Operator docs state that spool files can contain plaintext prompt data and describe `spool_dir`, volume/filesystem protection, and lifetime.

### Requirement 21: Evidence-Based Performance and Practical Eligibility
**Objective:** The feature must measurably reduce heap/GC and actually execute in realistic compositions.

#### Acceptance Criteria
1. Benchmarks cover 32 KiB, 256 KiB, 1 MiB, 5 MiB, and a test-only raised-limit 20 MiB request.
2. Record allocs/op, B/op, CPU, capture/shared-preflight/protocol-proof/assessment/provider-open latency, GC/heap behavior, and temp-file I/O.
3. Include concurrent realistic session counts, slow uploads, spool-budget saturation, malformed/late-field/giant-string cases, and replay/failover.
4. Verify decode permits are never held while waiting for client upload; measure the additional bounded hold across side-effect-free assessment.
5. Publish an eligibility matrix for typed extension planes, hook chains, frontend/core traffic, standard secure-session recorder, metering, accounting/billing, route override with homogeneous vs heterogeneous backend generations, sequential/fallback/race selectors, and each certified lane.
6. At least one normal secure-session + metering production-like composition shall reach wire execution. A design that compiles but is blocked by standard normally-on components is incomplete.
7. Measure local spool/device I/O and provider-open latency separately from heap savings; default spool thresholds/budgets must remain evidence-adjustable rather than assumed optimal.
8. Performance claims shall state that V1 still receives/validates the full request before provider open; the principal gain is heap/GC and avoided canonical object/marshal work, not early TTFT.

### Requirement 22: Conservative Rollout, Diagnostics, API Boundary, and Architecture Ratchets
**Objective:** Operators can enable, inspect, and revert the optimization safely without accidentally stabilizing a premature public API.

#### Acceptance Criteria
1. Feature is default-off in the first release.
2. Metrics use bounded static labels only and never backend/model/session/user IDs.
3. Logs/traces may record body size, spill/profile, rewrite/replay, assessment result, and fallback reason but never body content, spool path, or resume token.
4. Architecture tests prevent provider-name switches in core, provider types in large-body contracts, duplicate typed-plane classification systems, unclassified request authorities, fake Calls, expected fallback after wire commit, and protocol proof outside decode admission.
5. The V1 replay/proof/assessment/rewrite seam shall remain in an internal provider-neutral package unless a concrete supported external plugin consumer requires an explicit API/ABI/versioning review; existing public `lipsdk.ExecutorView` remains unchanged.
6. External backend/frontend/manual executor contracts remain source-compatible and canonical-only unless separately extended through a deliberate versioned capability.
7. Full QA/race/static checks, canonical characterization suites, identity-digest differential tests, and protocol differential tests pass before any production profile advertises wire support.
8. The spec/design shall be revalidated if main materially changes in frontend decode/admission, secure-session lifecycle, metering/accounting, extension/hook composition, routing authority, backend contracts, or response encoding before implementation begins.
