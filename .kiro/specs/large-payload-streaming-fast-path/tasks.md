# Implementation Plan

This plan is staged for a regression-sensitive brownfield codebase. The canonical request path remains the oracle and no backend/profile advertises production wire support until the characterization, identity, economic, lifecycle, route-domain, and differential-conformance gates are green.

The final V1 control flow is:

```text
capture + shared streaming preflight
  → one existing byte-weighted DecodeAdmission permit
  → protocol semantic proof + canonical semantic identity digest
  → AssessLargeBody (pure/frozen; SAME permit still held)
       decline → canonical Spec.Decode under SAME permit → existing path
       accept  → release permit → one-way wire commit
  → ExecuteLargeBody
       BeginTurn/A-leg → late route authority inside pre-certified domain
       → existing attempt/retry/recovery owner → canonical EventStream
```

There is no core-owned canonicalization callback, no second decode-admission decision, and no expected canonical fallback after wire commit.

All implementation work must start from current `main`; review baseline was `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533.

## Execution Rules

- Characterization/TDD before each brownfield refactor.
- Never fabricate a partial `lipapi.Call`.
- Never run protocol semantic proof outside decode admission.
- Never release/reacquire decode admission because core assessment declines.
- `AssessLargeBody` is bounded and side-effect-free: no `BeginTurn`, A-leg/store/DB mutation, billing reservation, provider I/O, client-body wait, or arbitrary unbounded plugin work.
- After assessment accepts and the permit is released, expected fallback is forbidden.
- Do not drop/reorder route candidates, disable races/fallbacks, or weaken authorities to retain wire mode.
- Standard always-composed secure-session recording and metering must receive wire-native equivalents; they are not acceptable permanent blockers.
- First release remains default-off.

---

## 1. Rebase, Revalidate, and Freeze Canonical Oracles

- [ ] 1. Revalidate current architecture before production changes

- [ ] 1.1 Rebase onto current `main` and rerun the spec trigger checklist
  - Confirm `feature.Plane[T]`/manifest/generated frozen storage/FrozenPlaneSet remain authoritative for typed planes.
  - Confirm `RequestRuntimeSnapshot` still separately owns `hooks.Bus` and other non-plane runtime authorities.
  - Confirm `frontendpipe` ordering remains body read → shared preflight → `decodeqos.TryAdmit` → guarded `Spec.Decode` → session header application/Validate/`AfterDecode` → traffic → executor.
  - Confirm secure-session `BeginTurn`, route-override snapshot, secure recorder, metering checkpoints, accounting/billing, and response-carrier ownership remain as documented in this spec.
  - Search for new full-Call consumers and newly added selector/content authorities.
  - If any revalidation trigger changed materially, update the spec before implementation.
  - _Validation: `go test ./internal/archtest/... ./internal/plugins/frontends/frontendpipe/... ./internal/core/runtime/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 5, 6, 7, 14, 15, 18, 19, 22_

- [ ] 1.2 Freeze canonical ingress and decode-admission behavior
  - Characterize method/path/auth/content-type ordering, identity/gzip body limits, shared JSON failures, exact limit/+1 behavior, body-model route extraction, decode-admission weight/saturation/overweight/cancel/panic release, and `Retry-After` mapping.
  - Add a test fixture that can prove a considered fast-path request must not receive a second decode-admission decision on fallback.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/... ./internal/plugins/frontends/decodeqos/... ./internal/plugins/frontends/reqbody/...`_
  - _Requirements: 1, 2, 3, 6_

- [ ] 1.3 Freeze secure-session/A-leg/route-authority lifecycle
  - Count principal/scope/session-open/workspace stages, `BeginTurn`, A-leg creation/fetch, route-override snapshot/barrier, secure client-turn recording, B-legs, terminal/finalization, new-session resume-token return, resume/denial/cancel/error behavior.
  - Characterize standard memory and Bun continuity stores as route-override-capable compositions.
  - Characterize detached mode separately; keep it canonical until explicitly certified.
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/core/securesession/... ./internal/core/routeoverride/...`_
  - _Requirements: 6, 7, 14, 19_

- [ ] 1.4 Freeze frontend response/session-carrier behavior
  - OpenAI Responses: response ID, cancellation carrier, timestamp, model, session/resume response headers, stream/non-stream writers, debug helpers.
  - OpenAI Chat: completion ID/timestamp/model/session response carriers.
  - OpenResponses: `AfterDecode`, `prepareCreateState`, store/continuation state, wrappers/options/observers.
  - Mark exact vs protocol-opaque/non-deterministic fields.
  - _Requirements: 17, 18_

- [ ] 1.5 Freeze deterministic request/economic identity
  - Characterize `diag.StableCallID`, `StableCallToken`, `StableUnix`, explicit Call.ID precedence, metering checkpoint/fact/source IDs, billing call IDs, trace IDs, and client-visible deterministic response fields.
  - Capture canonical fixtures with large strings, escapes/Unicode, tools/messages, model/selector, session-header precedence, and optional fields.
  - _Requirements: 15, 16, 18_

- [ ] 1.6 Build the complete full-Call/authority inventory and ratchet seed
  - Search `preparedRequest.call`, `identity.ingressCall`, canonical baseline clones, Call-retaining metering checkpoints, `lipapi.Call`-typed callbacks, `hooks.Bus`, `diag` stable helpers, token counters, billing inputs, traffic snapshots, response helpers, continuation/interleaved-thinking, and terminal closures.
  - Classify every production consumer as `bounded wire fact/view`, `source/digest contract`, `response-only`, or `assessment blocker`.
  - Seed an AST/architecture allowlist so new post-commit full-Call uses fail until classified.
  - _Requirements: 5, 13, 14, 15, 16, 19_

- [ ] 1.7 Capture canonical performance/eligibility baseline
  - 32 KiB, 256 KiB, 1 MiB, 5 MiB, and test-only 20 MiB bodies.
  - Record allocs/op, B/op, CPU, GC/heap, decode/encode time, and production-like composition details.
  - _Requirements: 21_

---

## 2. Configuration and Internal Provider-Neutral Contracts

- [ ] 2. Add zero-behavior-change plumbing

- [ ] 2.1 Add `server.large_payload_fast_path` configuration
  - `enabled`, `threshold_bytes`, `memory_spool_bytes`, `max_inflight_spool_bytes`, `max_semantic_fact_bytes`, and `spool_dir`.
  - Default off; validate relationships and spool directory during candidate generation/reload; preserve last-good generation on invalid reload.
  - Do not change existing request-size defaults.
  - Document plaintext spool and optimization-budget semantics.
  - _Requirements: 1, 2, 20, 22_

- [ ] 2.2 Add an internal provider-neutral large-body package
  - Define immutable replay `Source`, `Span`, bounded protocol `Proof`, `SessionInput`, bounded `ClientTurnShape`, canonical `IdentityDigest`, assessment request/result/stamp, wire request/domain facts, rewrite plan, execution result, response facts, and sensitive session-response carrier.
  - No raw arbitrary headers, provider SDK types, frontend-specific state, temp paths, prompt text, or unbounded maps.
  - Sensitive resume tokens get explicit types/handling and are excluded from normal formatting/telemetry.
  - Prefer an internal package unless a real external plugin consumer requires public API review.
  - _Requirements: 4, 6, 7, 8, 14, 16, 18, 22_

- [ ] 2.3 Keep public executor compatibility unchanged
  - Do not add mandatory methods to `lipsdk.ExecutorView`.
  - Frontend code type-asserts an internal optional two-phase interface such as `AssessLargeBody` + `ExecuteLargeBody`; absence means canonical-only.
  - Existing external/manual executors compile unchanged.
  - _Requirements: 1, 22_

- [ ] 2.4 Pin fast-path ingress policy to the same runtime generation
  - Threshold/spool/profile policy, assessment summary, backend proof, and execution must refer to one immutable generation.
  - Test reload during a large upload and between assessment/execution; a stale/mismatched stamp cannot execute.
  - _Requirements: 5, 6, 8_

---

## 3. Replay Capture, Reservation, and Resource Safety

- [ ] 3. Build replay independently of protocol/routing logic

- [ ] 3.1 Implement bounded logical spool reservation
  - Known identity lengths may reserve up front; unknown/chunked reserve incrementally with overflow-safe accounting.
  - Release exactly once on fallback/success/cancel/error.
  - Exhaustion is optimization decline, never a new 413.
  - _Requirements: 1, 20, 21_

- [ ] 3.2 Implement bounded RAM prefix + secure temp spill
  - Fixed/reusable copy buffer; no whole-body `bytes.Buffer` growth.
  - Preserve every consumed byte across create/write/short-write failures so canonical fallback remains lossless.
  - Private unpredictable names and restrictive permissions.
  - _Requirements: 1, 2, 20_

- [ ] 3.3 Implement lossless mid-capture canonical continuation
  - Stitch durable prefix + current unwritten suffix + unread client stream without restarting the socket.
  - Enforce the same identity-body ceiling exactly once.
  - Randomized chunk/fault tests compare byte-for-byte with direct canonical read.
  - _Requirements: 1, 2, 20_

- [ ] 3.4 Implement immutable completed source and independent readers
  - Offset-zero reader per `Open`; concurrent readers independent.
  - Root `Close` idempotent/nonblocking; final file deletion after root closed + readers zero.
  - No per-chunk/cleanup goroutine; cover Windows open-file deletion semantics.
  - _Requirements: 10, 20_

- [ ] 3.5 Compute a source/replay digest during capture
  - Digest decoded identity bytes incrementally for replay/widening integrity.
  - Explicitly distinguish it from the canonical semantic identity digest in Task 5.
  - _Requirements: 15, 16, 20_

- [ ] 3.6 Fault-injection/leak/privacy tests
  - Reservation/create/short-write/read/remove failures, cancellation, timeout, exact EOF/limit, reader leak simulation.
  - Assert no body/spool path/resume token in logs/metrics/errors.
  - _Validation: `go test -race ./internal/plugins/frontends/reqbody/... ./internal/core/largebody/...`_
  - _Requirements: 20, 22_

---

## 4. Shared Low-Allocation JSON Scanner

- [ ] 4. Differentially prove shared safety parity

- [ ] 4.1 Implement incremental shared JSON lexer/state machine
  - UTF-8, escapes/surrogates, number grammar, root/delimiters/trailing/incomplete values, shared byte/token/depth/object/array/key/string/number bounds, and cancellation.
  - No proportional retention of ordinary large strings.
  - _Requirements: 3_

- [ ] 4.2 Add bounded path/token/span observation
  - Exact raw spans for selected values; nested-key discrimination; bounded decoded values/events.
  - Keep protocol semantics out of shared scanner.
  - _Requirements: 4, 9_

- [ ] 4.3 Add differential/fuzz corpus against current slice preflight
  - Buffer splits around UTF-8/escapes/numbers, exact limits, giant strings, deep/wide JSON, duplicates, malformed/trailing data, and cancellation.
  - Compare stable classification/counts rather than incidental error wording.
  - _Validation: `go test ./internal/core/jsonshape/...` plus fuzz targets_
  - _Requirements: 3_

---

## 5. Implement Exact Canonical Semantic Identity Without a Full Call

- [ ] 5. Identity parity is a prerequisite, not response cosmetics

- [ ] 5.1 Refactor `diag` to derive stable outputs from an already-computed canonical sum
  - Preserve every existing canonical `StableCallID`, token, and Unix output byte-for-byte.
  - Add internal helpers such as `StableCallIDFromSum`/equivalent; canonical path may continue to compute the sum from Call.
  - No behavior change yet.
  - _Requirements: 16_

- [ ] 5.2 Define the streaming canonical-semantic hash writer contract
  - For a certified profile, emit/hash the exact canonical Call representation produced after frontend decode/session-header precedence and before core mutation, with `Call.ID` cleared as current stable hashing requires.
  - Process large string contents incrementally; do not retain them solely for hashing.
  - Be explicit about canonical struct field ordering, omitted/zero fields, JSON string escaping, arrays/maps, normalization, and supported optional controls.
  - _Requirements: 4, 16, 17_

- [ ] 5.3 Differentially prove identity equivalence
  - Decode the same request canonically and compare wire-profile sum plus derived ID/token/Unix.
  - Include huge strings, Unicode/escapes/HTML-sensitive content, tool/function/message shapes, session headers, selector/model, and every certified optional field.
  - If any supported shape cannot reproduce the canonical sum exactly, narrow the profile.
  - _Validation: identity fuzz/differential tests_
  - _Requirements: 16, 17_

- [ ] 5.4 Prove downstream economic identity parity
  - For the same logical request, canonical and wire paths must produce identical request/trace IDs and deterministic metering fact/source/checkpoint identities.
  - Explicit caller-supplied IDs retain current precedence.
  - _Requirements: 15, 16, 18_

---

## 6. Build One Frozen Wire-Eligibility Summary Across Planes, Hooks, and Standard Authorities

- [ ] 6. One typed-plane declaration system, but complete brownfield eligibility coverage

- [ ] 6.1 Extend canonical typed plane declarations with request-body access metadata
  - Add zero `Unclassified` plus `CanonicalRequired`, `MetadataOnly`, `ResponseOnly`, `WireContract` (equivalent names allowed).
  - Annotate every production plane from actual semantics.
  - Extend generator/frozen storage; no manually duplicated named plane mirror.
  - _Requirements: 5_

- [ ] 6.2 Classify the separate legacy `hooks.Bus`
  - Use frozen chain occupancy (`HookChainLengths` or generated equivalent), not an assumption that hooks are planes.
  - Submit/request-part and ambiguous Call-mutating chains are canonical-required unless they gain a typed wire contract.
  - Response-only chains can remain active only after characterization.
  - _Requirements: 5, 13_

- [ ] 6.3 Add non-plane standard-runtime eligibility facts
  - Frontend/core traffic/raw capture/redaction, secure-session recorder capability, metering mode, token accounting/preflight/billing/counting capability, route-override capability, detached mode, and Call-shaped custom callbacks from Task 1.6.
  - Standard secure recorder and metering must advertise their wire-native paths once Tasks 9–10 land; do not encode them as permanent blockers.
  - _Requirements: 5, 13, 14, 15, 19_

- [ ] 6.4 Generate/publish a bounded `WireEligibilitySummary`
  - Composition-time/frozen generation only; no request-path reflection/map walk or arbitrary plugin execution.
  - Unknown/unclassified fails closed.
  - _Requirements: 5, 6_

- [ ] 6.5 Add declaration and dependency ratchets
  - New typed plane, hook category, or non-plane request authority without classification fails CI.
  - Frozen summary generation is deterministic and generation-pinned.
  - _Validation: `go test ./pkg/lipsdk/feature/... ./internal/archtest/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 5, 19, 22_

---

## 7. Add Frontend Candidate Processing and Single-Permit Semantic Proof

- [ ] 7. Add candidate ingress without certifying a production protocol yet

- [ ] 7.1 Add optional profile plumbing and bounded frontend-owned wire state
  - Nil profile/missing two-phase executor means canonical-only with no spool/scanner.
  - Profile owns protocol proof, canonical identity digest, normalized client-turn shape, session/body precedence facts, and rewrite spans only.
  - No backend selection/network in profile.
  - _Requirements: 4, 14, 16, 17, 18_

- [ ] 7.2 Preserve cheap canonical gates before capture
  - Handler auth/path/content-type → feature/profile/executor → frontend full-body traffic gate → known identity length below threshold → gzip wave-1 gate → capture.
  - Disabled/blocked/below-threshold requests create no temp file and do not materially regress allocations.
  - _Requirements: 1, 2, 11, 13, 21_

- [ ] 7.3 Capture to EOF + shared streaming preflight
  - Use Task 3 continuation for mid-capture declines.
  - Unknown/chunked final-below-threshold goes canonical.
  - Shared invalid/over-limit mapping stays exact.
  - _Requirements: 1, 2, 3, 20_

- [ ] 7.4 Acquire exactly one decode-admission permit
  - Use exact final decoded byte weight.
  - Permit is never held during client upload.
  - Run protocol semantic proof, normalized recorder-shape extraction, and canonical semantic identity hashing from a replay reader while the permit is held.
  - On profile decline, materialize and run current `Spec.Decode` under this same permit.
  - _Requirements: 4, 6, 14, 16, 17_

- [ ] 7.5 Keep the permit held across `AssessLargeBody`
  - Invoke Task 11's pure assessor before releasing the permit.
  - Assessment decline materializes and runs current `Spec.Decode` under the same permit—never a second `TryAdmit`.
  - After canonical Decode, release at today's boundary and continue normal Validate/`AfterDecode`/traffic/Execute.
  - Assessment accept releases once and crosses the one-way wire commit.
  - Add saturation/concurrency tests proving no fallback-induced 429/503 race.
  - _Requirements: 1, 6_

---

## 8. Add Backend Exact/Domain Wire Proof and Model Rewrite Primitives

- [ ] 8. Define pure backend capability before assessment uses it

- [ ] 8.1 Extend internal backend contract additively
  - Pure `ResolveWireRequest` for exact candidate facts and pure `ResolveWireDomain` (or equivalent) for finite/`AnyAcceptedModel` late-route domains.
  - `OpenWire` is post-commit only.
  - Nil/unknown means canonical-only; no external plugin ABI change unless separately versioned.
  - _Requirements: 7, 8, 12, 22_

- [ ] 8.2 Implement streaming model-token splice
  - Exact scanner span + JSON-encoded replacement + checked rewritten length.
  - Cover same/longer/shorter/escaped model, late model, nested misleading text, duplicate/invalid span.
  - _Requirements: 9, 20_

- [ ] 8.3 Prove backend declaration purity
  - Support resolution has no provider/network I/O, DB/store access, mutable session reads, or unbounded work.
  - Domain proof states exact supported execution modes and model domain.
  - _Validation: backend contract tests_
  - _Requirements: 6, 8_

---

## 9. Add Secure-Session Wire Views and Sensitive Response Carriers

- [ ] 9. Preserve standard secure-session behavior without materializing prompt text

- [ ] 9.1 Build exact bounded `SessionInput`
  - Reproduce current session/resume/client-session/continuity header/body precedence.
  - Initial OpenAI profiles may reject body-carried LIP session metadata while supporting authoritative LIP session/resume headers.
  - Sensitive resume token never reaches backend facts or telemetry.
  - _Requirements: 14, 17_

- [ ] 9.2 Add wire `BeginTurn`/identity helpers sharing canonical logic
  - Refactor only fact-based secure-session construction/binding needed by both paths; do not split the entire executor merely for fallback.
  - Preserve new/resume/denial/workspace/session-opener semantics for certified metadata-only stages.
  - _Requirements: 6, 14, 19_

- [ ] 9.3 Add bounded secure-recorder input from `ClientTurnShape`
  - Produce `ClientTurnRecordInput` equivalent to canonical `lipapi.NormalizedItems` shape: role/ordinal/part kinds and required non-content facts.
  - Do not retain prompt text solely for recorder.
  - Semantic-fact budget overflow falls back at profile/assessment stage.
  - Differential tests compare canonical and wire recorder inputs for certified corpus.
  - _Requirements: 14, 19, 21_

- [ ] 9.4 Return sensitive session response carrier
  - Capture authoritative session ID, A-leg ID, and new-session raw resume token from existing `BeginTurn` result.
  - Frontend emits the same session/resume response headers as canonical path.
  - Add first-turn→resume-next-turn E2E test using a wire first turn.
  - Assert resume token never appears in logs/metrics/debug output.
  - _Requirements: 14, 18, 22_

---

## 10. Replace Full-Call Metering With Wire-Native Economic Checkpoints

- [ ] 10. Standard metering must not erase the optimization

- [ ] 10.1 Add wire-native frontend-ingress checkpoint capture
  - Construct the same public metering checkpoint from stable request identity, scope/frontend, request count, exact max-output bound, timestamps, and post-BeginTurn A-leg/session correlation.
  - Do not clone/store a canonical Call.
  - Preserve deterministic checkpoint/fact/source identities.
  - _Requirements: 15, 16, 19_

- [ ] 10.2 Add wire-native backend-attempt checkpoint capture
  - Attempt/B-leg/backend/model correlation plus source digest and exact rewrite/attempt digest for immutable/widening evidence.
  - No hidden full Call retained for retry/rerate.
  - Refactor widening checks to common bounded evidence where semantics permit.
  - _Requirements: 10, 15, 19_

- [ ] 10.3 Preserve no-accounting metering path first
  - With accounting/token preflight disabled, wire checkpoints shall fully work without tokenization.
  - Prove standard secure-session + metering composition can reach wire mode before adding counting complexity.
  - _Requirements: 15, 21_

- [ ] 10.4 Add explicit wire token-count capability or pre-assessment fallback
  - Introduce an exact `WireCounter`/source-count contract only where provider/profile semantics support it.
  - Never substitute body bytes for tokens.
  - If configured accounting/context preflight has only `CountCall`, assessment declines while the same decode permit is held.
  - _Requirements: 6, 15, 21_

- [ ] 10.5 Characterize/refactor stock billing/exposure inputs
  - Principal account identity, pricing/charge policy, max-output, exposure/reservation, settlement, terminal usage, and idempotency.
  - Exact bounded values share helpers with canonical path; arbitrary custom Call callbacks stay assessment blockers unless they implement a typed wire contract.
  - Economic facts/reservations occur exactly once after wire commit.
  - _Requirements: 15, 19_

---

## 11. Implement Side-Effect-Free `AssessLargeBody` and Late-Route Compatibility Envelopes

- [ ] 11. This is the last expected fallback point

- [ ] 11.1 Implement optional two-phase assessor/executor interface
  - `AssessLargeBody(ctx, proof) -> Assessment` and `ExecuteLargeBody(ctx, acceptedAssessment, source) -> ExecutionResult` (equivalent names allowed).
  - Assessment contains opaque generation-bound stamp/facts only; frontend cannot synthesize backend plan internals.
  - No canonical callback in core contract.
  - _Requirements: 6, 22_

- [ ] 11.2 Add side-effect sentinels around assessment
  - Tests panic/fail if assessment calls `BeginTurn`, A-leg/store/DB writes, route-override store reads, billing reservation, provider/network I/O, waits on client body, or invokes arbitrary unbounded Call plugins.
  - Measure assessment latency under held decode permit.
  - _Requirements: 6, 21, 22_

- [ ] 11.3 Assess frozen plane/hook/non-plane blockers
  - Consume Task 6 summary, secure/metering/counting capabilities, profile/body mode, and complete Task 1.6 dependency inventory.
  - Unknown => decline.
  - _Requirements: 5, 6, 13, 14, 15, 19_

- [ ] 11.4 Prove the exact initial selector candidate set
  - Reuse current aliases/default-backend/execution-composition policy and generation-fixed native-model semantics.
  - Cover sequential/fallback/race/weighted selector candidates without pruning/reordering.
  - _Requirements: 7, 8, 10_

- [ ] 11.5 Build the late-bound route-override compatibility envelope
  - Do **not** block merely because `RouteOverrideReader` exists.
  - Derive all selector/backend/execution-mode/model outcomes the current generation's route-override validator can legally produce.
  - Reuse known-backend and execution-composition policy from the real route-override generation validator.
  - Where override model text is not a finite catalog, require backend domain proof such as `AnyAcceptedModel`; otherwise decline.
  - _Requirements: 7, 8_

- [ ] 11.6 Handle other late selector authorities conservatively
  - Route-hint/selector-mutating authorities that receive full Call are blockers unless they expose an explicit bounded route-domain wire contract.
  - _Requirements: 5, 7, 13, 19_

- [ ] 11.7 Prove all exact and domain backend compatibility
  - Any incompatible member causes decline before permit release.
  - Add homogeneous all-same-wire and heterogeneous-generation tests; actual post-BeginTurn override changes within the envelope must execute without fallback.
  - _Requirements: 7, 8, 21_

- [ ] 11.8 Bind/validate assessment stamp
  - Execution must use same immutable executor/generation/proof identity; pure recomputation disagreement is invariant failure, not fallback.
  - _Requirements: 6, 8_

---

## 12. Close Remaining Post-Commit Full-Call Dependencies

- [ ] 12. No wire execution code may need a fake request

- [ ] 12.1 Introduce only exact bounded runtime wire facts proven by Task 1.6
  - Route selector/model/protocol requirements, max-output, request/trace identity, secure-session/A-leg facts, source/rewrite digests, etc.
  - Do not mirror the Call schema.
  - _Requirements: 19_

- [ ] 12.2 Refactor routing/capability/request-size helpers where exact semantics are metadata-only
  - Failover requirements from exact protocol facts.
  - Request-size estimate only when an exact bounded contract exists; otherwise assessment blocker.
  - Canonical path should use common fact helpers where practical to prevent drift.
  - _Requirements: 7, 8, 19_

- [ ] 12.3 Refactor `recvTurnFacts`, continuation/interleaved-thinking, traffic snapshots, terminal/session helpers
  - Metadata-only uses become bounded views; content uses become assessment blockers; response-only uses remain on canonical events.
  - _Requirements: 13, 14, 19_

- [ ] 12.4 Enforce AST/architecture ratchet
  - Wire post-commit functions cannot dereference `preparedRequest.call`, clone a Call, invoke unclassified Call callbacks, or call stable identity helpers that require a full Call.
  - _Validation: `go test ./internal/archtest/... ./internal/core/runtime/...`_
  - _Requirements: 19, 22_

---

## 13. Implement `ExecuteLargeBody` Inside Existing Lifecycle/Attempt Machinery

- [ ] 13. Wire execution only after Tasks 1–12 are green

- [ ] 13.1 Cross the one-way commit and begin exactly one logical turn
  - Validate assessment stamp, take source ownership, run wire secure-session preparation, route override snapshot/barrier, request authority/economic admission, and build authoritative response/session facts.
  - No expected canonical fallback branch exists.
  - _Requirements: 6, 7, 14, 15, 18, 19_

- [ ] 13.2 Integrate wire opens into existing B-leg/attempt/recovery owner
  - Same B-leg allocation, attempts, TTFT, affinity/weighted-first/interleaved, credential retry, failure history, first-event commitment, failover/race semantics, and terminal accounting.
  - Each retry opens source from zero and applies per-candidate model rewrite.
  - Provider response parser emits the same canonical EventStream.
  - _Requirements: 8, 9, 10, 12, 15_

- [ ] 13.3 Add post-commit invariant/cancel/replay tests
  - Unexpected content dependency finalizes/aborts one turn and never invokes ordinary `Execute`.
  - Attempt 1 pre-output failure → attempt 2 gets complete bytes; no failover after first visible event.
  - Parallel readers independent; cancellation closes readers/source and preserves lifecycle/economic cleanup.
  - Actual route override chosen after BeginTurn remains within assessed domain.
  - _Requirements: 6, 7, 10, 20_

---

## 14. Build Frontend Response-State Bridge

- [ ] 14. Preserve protocol/session response semantics without a Call

- [ ] 14.1 Refactor shared frontend response context
  - Combine frontend-owned wire state + `ExecutionResult.ResponseFacts` + sensitive `SessionResponseCarrier` for wrapping/encoding.
  - Canonical path remains source-compatible.
  - No frontend/provider state moves into core and no partial Call is synthesized.
  - _Requirements: 18_

- [ ] 14.2 Wire deterministic response identity to canonical semantic digest
  - OpenAI Responses/Chat deterministic IDs/timestamps use Task 5 stable sum helpers where current canonical behavior does.
  - Normalize differences only for fields proven protocol-opaque.
  - _Requirements: 16, 18_

- [ ] 14.3 Preserve cancellation and secure-session response carriers
  - Returned OpenAI Responses ID remains cancellable using authoritative A-leg/session semantics.
  - New-session response includes exact session/resume headers and next request can resume.
  - _Requirements: 14, 18_

- [ ] 14.4 Add frontend/core boundary ratchets
  - Core/internal largebody package cannot import frontend/provider response-state types.
  - Sensitive response carrier cannot be stringified into normal telemetry.
  - _Requirements: 18, 22_

---

## 15. Certify Lane 1: OpenAI Responses → OpenAI-Compatible Responses

- [ ] 15. First production lane proves the full architecture

- [ ] 15.1 Implement conservative OpenAI Responses semantic profile
  - Exact create endpoint; exact model/stream/max-output/protocol requirements; bounded normalized recorder shape; exact canonical identity digest; model span.
  - Initial canonical-only triggers: body-carried LIP session/proxy metadata, duplicate protocol-owned names, unknown fields canonical encode drops, repair-sensitive aliases/histories, unsupported controls, semantic-fact overflow.
  - _Requirements: 4, 14, 16, 17_

- [ ] 15.2 Implement OpenAI-compatible Responses backend exact/domain wire proof and `OpenWire`
  - Reuse existing URL/auth/credential pool/cooldown/shared client/parser/error classification; core owns retries.
  - Domain proof must support route-override envelope only when same-wire/model semantics are genuinely universal for the declared domain.
  - _Requirements: 7, 8, 9, 12_

- [ ] 15.3 End-to-end canonical-vs-wire conformance
  - Compare provider method/path/relevant headers, effective JSON semantics after rewrite, stream mode, errors, canonical response events, stable request/response identity, cancellation, session headers/resume, secure recorder input, metering facts, retry/failover/race behavior.
  - Include decode-admission saturation and assessment decline under same permit.
  - Only after this suite passes may the lane advertise wire support.
  - _Requirements: 1, 6, 7, 10, 14, 15, 16, 17, 18_

---

## 16. Certify Lane 2: OpenAI Chat Completions → OpenAI-Compatible Chat

- [ ] 16. Add Chat only after Lane 1 is green

- [ ] 16.1 Implement conservative Chat proof/digest/recorder shape
  - Preserve current message/tool/function/reasoning normalization; canonical-only for malformed/alias/unknown/duplicate shapes that canonical re-encode changes.
  - _Requirements: 4, 14, 16, 17_

- [ ] 16.2 Add backend support and response parity
  - Reuse compatible wire transport; preserve completion ID/timestamp from canonical semantic digest, session carriers, retries/failover, and provider errors.
  - _Requirements: 8, 10, 12, 18_

- [ ] 16.3 Differential conformance before enabling lane
  - Same provider/economic/secure-session/response criteria as Lane 1.
  - _Requirements: 17, 18, 21_

---

## 17. Certify Lane 3: OpenResponses HTTP Create, Explicit No-Store Only

- [ ] 17. Do not treat default OpenResponses create as stateless

- [ ] 17.1 Refactor/characterize bounded no-store frontend state
  - Initial subset: HTTP create, **explicit `store:false`**, no `previous_response_id`, no compaction, no WebSocket.
  - Missing `store` is canonical because decoder defaults it to true.
  - Prove no `AfterDecode` error/side effect is shifted after wire commit.
  - _Requirements: 14, 17, 18_

- [ ] 17.2 Implement OpenResponses proof/digest and compatible backend wire support
  - Preserve strict duplicate policy, field limits, requirements, endpoint/auth/client/parser/error behavior.
  - `store:true`/continuation/unknown controls remain canonical.
  - _Requirements: 4, 8, 12, 16, 17_

- [ ] 17.3 Differential conformance
  - Provider-effective JSON, response state/IDs/options, secure-session/metering identity, retry/failover/cancel.
  - Assert missing/true store never reaches wire backend.
  - _Requirements: 17, 18_

- [ ] 17.4 Treat `store:true`/continuation as a separate later certification
  - Requires exact reservation/response-ID/recorder/cleanup/lineage parity; do not expand incidentally.
  - _Requirements: 17_

---

## 18. Gzip Follow-Up Wave

- [ ] 18.1 Prove wave-1 gzip always remains canonical before spool/profile
  - Preserve current decoded-limit/error behavior.
  - _Requirements: 11_

- [ ] 18.2 Optional later task: decoded gzip replay source
  - Reuse current bounded decompression semantics; thresholds/reservations are decoded bytes; remove stale outbound encoding.
  - Rerun scanner/profile/identity/backend differential suites for gzip corpus.
  - _Requirements: 11_

---

## 19. Observability, Performance, and Practical Eligibility Evidence

- [ ] 19. Measure actual value and fallback surface

- [ ] 19.1 Add bounded metrics/traces
  - considered / profile-proven / assessment-eligible / wire / canonical counts; static decline reason enum; body-size buckets; memory/file spill; replay/rewrite counts; capture/preflight/proof/assessment/provider-open latency; active spool bytes.
  - No backend/model/user/session IDs in labels and no body/path/resume token in telemetry.
  - _Requirements: 20, 22_

- [ ] 19.2 Benchmark allocation/CPU/GC and assessment permit hold
  - Required sizes plus giant strings, late model, tools, malformed JSON, replay/failover.
  - Measure allocs/op, B/op, CPU, GC/heap, file I/O, full capture→provider-open latency, and the additional decode-permit duration for pure assessment.
  - Confirm permit is never held during client upload or network/store I/O.
  - _Requirements: 6, 21_

- [ ] 19.3 Concurrent load + spool-budget saturation
  - Realistic session counts, slow uploads, races/fallback, budget saturation.
  - Compare saturation against disabled canonical baseline; do not claim spool budget globally bounds canonical heap.
  - _Requirements: 20, 21_

- [ ] 19.4 Publish realistic eligibility matrix
  - Empty/occupied typed planes; hook chains; frontend/core traffic; standard secure recorder; wire-native metering; accounting/billing off/on; route override in homogeneous same-wire vs heterogeneous generations; sequential/fallback/race; each protocol lane.
  - Require at least one normal secure-session + metering production-like configuration to execute wire path.
  - Quantify blockers rather than hiding them.
  - _Requirements: 5, 7, 14, 15, 21_

---

## 20. Final Architecture/Regression Gate and Rollout

- [ ] 20.1 Add final architecture ratchets
  - No unclassified production plane/hook/non-plane request authority.
  - No provider-name switch/provider SDK type in core large-body contracts.
  - No protocol semantic proof outside decode admission.
  - No side effect in `AssessLargeBody`.
  - No second decode admission on assessment decline.
  - No expected canonical fallback after wire commit.
  - No fake/partial Call or wire checkpoint retaining a full Call.
  - No raw-body hash substituted for canonical stable identity.
  - No late route authority can select outside assessed domain.
  - No route pruning/reordering for eligibility.
  - No new post-commit full-Call dependency outside explicit canonical-only allowlist.
  - _Requirements: 5, 6, 7, 15, 16, 19, 22_

- [ ] 20.2 Run full quality gate
  - `go test ./...`
  - targeted `go test -race` for frontendpipe/reqbody/runtime/secure-session/metering/backends
  - `go vet ./...`
  - repository lint/staticcheck commands required by CI
  - identity digest differential/fuzz suites
  - protocol differential conformance suites
  - performance/load/eligibility evidence
  - _Requirements: all_

- [ ] 20.3 Keep rollout default-off and document caveats
  - Explicit opt-in first release.
  - Document certified protocol/backend/model/route domains and canonical-only triggers.
  - Document spool plaintext/storage, decode-QoS assessment hold, economic/counting blockers, route-domain conservatism, and fallback metrics.
  - Profile/domain broadening requires conformance evidence in the same change.
  - _Requirements: 17, 20, 21, 22_
