# Implementation Plan

This plan is intentionally staged for a brownfield codebase. The first implementation wave must preserve the existing canonical path and create proof/ratchets before any backend advertises wire support. The revised V1 architecture uses a **single pre-`BeginTurn` wire commit point**; it does **not** require the original draft's secure-preparation split or post-identity canonical continuation.

All tasks assume the implementation branch is first rebased onto current `main`. The spec review baseline was `40168ce1f3890a1c86c22e898be9d264d63ccd72` (PR #533 merged).

## 1. Rebase, Revalidate Brownfield Assumptions, and Freeze the Canonical Oracle

- [ ] 1. Rebase before implementation and make architectural drift a hard gate

- [ ] 1.1 Rebase onto current `main` and rerun the spec revalidation checklist
  - Confirm `pkg/lipsdk/feature/plane_manifest.go`, `feature.Plane[T]`, generated frozen planes, and `FrozenPlaneSet` are still the active extension composition architecture.
  - Confirm `frontendpipe` still orders shared preflight → `decodeqos.TryAdmit` → guarded `Spec.Decode` → post-decode validation/`AfterDecode` → frontend traffic → executor.
  - Confirm current route-override ownership is still post-authoritative-A-leg; if this changes, re-evaluate the V1 pre-turn blocker.
  - Search for newly added `preparedRequest.call` / `lipapi.Call` consumers, frontend `AfterDecode` hooks, billing callbacks, and response encode dependencies.
  - If any revalidation trigger materially changed, update this spec before implementation rather than coding against stale assumptions.
  - _Validation: `go test ./internal/archtest/... ./internal/plugins/frontends/frontendpipe/... ./internal/core/runtime/...`_
  - _Requirements: 5, 6, 16, 17, 18, 19_

- [ ] 1.2 Add canonical ingress/decode-QoS characterization tests before touching `frontendpipe`
  - Freeze method/path/content-type behavior for target frontends.
  - Freeze `reqbody.ReadAll` max-body and gzip decoded-limit behavior.
  - Freeze `jsonguard`/jsonshape malformed/trailing/boundary behavior.
  - Freeze decode admission weight, saturation, overweight, cancellation, `Retry-After`, and exactly-once release.
  - Freeze route-from-body-model ordering relative to preflight/admission/decode.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/... ./internal/plugins/frontends/decodeqos/... ./internal/archtest/...`_
  - _Requirements: 1, 2, 3, 17_

- [ ] 1.3 Add lifecycle/authority characterization around ordinary `Executor.Execute`
  - Count `BeginTurn`, A-leg creation/fetch, request authority, billing call/exposure, B-leg allocation, terminal/finalization, and stream handoff on success/error/cancel.
  - Freeze route/failover/race candidate ordering and no-post-output-failover behavior.
  - Freeze route-override snapshot timing and conversation/local-turn/request-transform ordering.
  - This suite becomes the oracle for the later wire branch.
  - _Validation: `go test -race ./internal/core/runtime/...`_
  - _Requirements: 6, 9, 12, 19_

- [ ] 1.4 Inventory and characterize frontend response-state dependencies
  - For OpenAI Responses, record all inputs used by `responseIDForCall`, cancellation carrier, `BuildEncodeOpts`, stream/non-stream writers, streamdebug, and clock fallback.
  - For OpenAI Chat, record completion-ID/timestamp dependencies.
  - For OpenResponses, record `AfterDecode` → `prepareCreateState`, `createEncodeState`, continuation/store reservation/observer ownership, wrappers, and non-stream options.
  - Add tests proving which fields are protocol-semantic vs opaque/nondeterministic and which must remain exact for cancellation/correlation.
  - _Validation: `go test ./internal/plugins/frontends/openairesponses/... ./internal/plugins/frontends/openailegacy/... ./internal/plugins/frontends/openresponses/...`_
  - _Requirements: 14, 18_

- [ ] 1.5 Create the post-commit full-Call dependency inventory artifact/test
  - Search production runtime code for `preparedRequest.call`, `identity.ingressCall`, canonical baseline clones, and Call-typed callbacks used after the proposed V1 commit.
  - Include route planning/request-size/failover requirements, billing identity/policy/pricing/max-output/token estimators, `recvTurnFacts`, continuation support, interleaved-thinking recorders, terminal usage/session fields, and any additional current consumers.
  - Classify each as `pre-turn blocker`, `exact bounded fact`, or `response-only`.
  - Add a ratchet test/generated allowlist so a new post-commit Call use fails until explicitly classified.
  - _Validation: `go test ./internal/core/runtime/... ./internal/archtest/...`_
  - _Requirements: 12, 19_

## 2. Add Configuration and Provider-Neutral SDK Contracts

- [ ] 2. Define additive contracts with zero behavior change

- [ ] 2.1 Add `large_payload_fast_path` configuration with default-off semantics
  - Add `enabled`, `threshold_bytes`, `memory_spool_bytes`, `max_inflight_spool_bytes`, and `spool_dir`.
  - Validate positive/bounded relationships and spool directory during candidate generation/reload; preserve last-good config on invalid reload.
  - Do not alter existing `MaxRequestBodyBytes` defaults or frontend protocol limits.
  - Add config-doc examples explicitly describing spool plaintext and optimization-budget semantics.
  - _Validation: config/package tests + reload tests_
  - _Requirements: 1, 2, 13, 16_

- [ ] 2.2 Add `pkg/lipsdk/requestbody` provider-neutral types
  - Add immutable replay `Source`, `Span`, bounded `Metadata`, `CanonicalizeFunc`, `ExecutionRequest`, `ExecutionResult`, `ResponseFacts`, `ExecutionPath`, and optional `LargeBodyExecutorView` (or equivalent names).
  - Do not put raw headers, credentials, temp paths, arbitrary maps, frontend state, provider SDK types, or prompt contents in SDK/core metadata.
  - `ExecutionResult` must carry enough bounded authoritative facts for frontend response binding; EventStream-only is not sufficient.
  - Add compile-time package-boundary/arch tests preventing provider imports.
  - _Validation: `go test ./pkg/lipsdk/... ./internal/archtest/...`_
  - _Requirements: 1, 7, 13, 18_

- [ ] 2.3 Preserve the existing executor surface
  - Do not add mandatory methods to `lipsdk.ExecutorView` that would break external/manual executor implementations.
  - Frontends use an optional type assertion for the large-body seam; absence is canonical-only.
  - Add compatibility tests using a legacy executor that implements only current methods.
  - _Validation: `go test ./pkg/lipsdk/... ./internal/plugins/frontends/...`_
  - _Requirements: 1, 16_

## 3. Implement the Replayable Capture Source

- [ ] 3. Build and harden the pre-commit body source independently of JSON/protocol logic

- [ ] 3.1 Implement bounded RAM prefix with spill-to-private-temp-file
  - Use a fixed/reusable copy buffer; do not grow a full-body byte slice on the wire candidate path.
  - Enforce the existing decoded request limit while capturing identity bodies.
  - Keep every consumed byte recoverable for later canonical fallback.
  - Use unpredictable temp names and owner-only create semantics where supported.
  - _Validation: `go test -race ./internal/plugins/frontends/reqbody/... ./pkg/lipsdk/requestbody/...`_
  - _Requirements: 1, 2, 13_

- [ ] 3.2 Implement independent replay readers and nonblocking root close
  - Every `Open` starts at offset zero and has independent cursor/close ownership.
  - Root `Close` is idempotent, marks ownership closed, and final file deletion occurs when active readers reach zero; do not block indefinitely waiting for reader closure.
  - Cover Windows-style inability to delete an open file.
  - No cleanup goroutine and no goroutine per body chunk.
  - _Validation: `go test -race ./pkg/lipsdk/requestbody/...`_
  - _Requirements: 9, 13_

- [ ] 3.3 Add global logical spool reservation
  - Reserve known identity-body lengths up front when safe; reserve unknown/chunked incrementally.
  - Release exactly once on wire success, canonical fallback, parse error, cancellation, and file-I/O error.
  - Reservation exhaustion returns a bounded optimization decline; it is not a new client 413.
  - Add saturation/concurrency tests and metrics hook points.
  - _Validation: `go test -race ./internal/plugins/frontends/reqbody/...`_
  - _Requirements: 1, 13, 15_

- [ ] 3.4 Add fault-injection and leak tests
  - Temp create/write/read/remove failures, short reads/writes, cancellation during copy, EOF boundary, source open failure, reader leak simulation, and server/request timeout.
  - Assert no filename/body content appears in error/log/metric evidence.
  - _Validation: `go test -race ./internal/plugins/frontends/reqbody/... ./pkg/lipsdk/requestbody/...`_
  - _Requirements: 13, 16_

## 4. Add the Shared Streaming JSON Shape Scanner

- [ ] 4. Implement differential parity before protocol profiles

- [ ] 4.1 Implement incremental JSON lexical/shape validation
  - Match current shared limits for bytes/depth/tokens/root/object/array/key/string/number.
  - Handle split UTF-8, escapes/surrogates, number grammar, trailing/incomplete values, delimiters, and cancellation.
  - Retain only bounded selected tokens/offsets requested by observers; never ordinary giant strings.
  - _Validation: `go test ./internal/core/jsonshape/...`_
  - _Requirements: 3, 4_

- [ ] 4.2 Add differential/fuzz corpus against current slice preflight
  - Randomized valid/invalid JSON, every buffer split around multibyte/escape/number delimiters, very deep/wide structures at exact limits, giant strings, late metadata, duplicate keys, and trailing data.
  - Compare classification/counts rather than error text when current APIs do not promise text identity.
  - _Validation: `go test ./internal/core/jsonshape/...`; fuzz targets in CI-safe mode_
  - _Requirements: 3_

- [ ] 4.3 Add bounded top-level token-span observation primitives
  - Produce exact raw offsets for selected values such as the top-level model token without protocol field-name policy in shared core.
  - Test nested misleading keys and `"model"` text inside giant content.
  - _Validation: `go test ./internal/core/jsonshape/...`_
  - _Requirements: 4, 8_

## 5. Extend the Current Typed Plane Manifest With Request-Body Access Metadata

- [ ] 5. Use one composition architecture only

- [ ] 5.1 Add explicit `RequestBodyAccess` to canonical plane declarations
  - Add `Unclassified`, `CanonicalRequired`, `MetadataOnly`, and `ResponseOnly` (or equivalent) with `Unclassified` as zero.
  - Annotate **every** current production plane in `plane_manifest.go`; do not create named mirrors in `RequestRuntimeSnapshot`/runtimebundle.
  - Classifications must reflect actual current stage semantics, not desired fast-path coverage.
  - _Validation: `go test ./pkg/lipsdk/feature/...`_
  - _Requirements: 5_

- [ ] 5.2 Extend generator/frozen set to derive a bounded `AccessSummary`
  - Generate summary/occupied blocker IDs from the typed frozen plane representation with no hot-path reflection/map/type-assertion walk.
  - Runtime missing/unknown summary fails closed to canonical-required.
  - Publish through the generation/request runtime snapshot in a read-only form.
  - _Validation: `go test ./internal/archtest/... ./internal/infra/runtimebundle/... ./internal/core/extensions/...`_
  - _Requirements: 5, 12, 16_

- [ ] 5.3 Add declaration and runtime ratchets
  - Production manifest validation fails if any new plane is unclassified.
  - Runtime test proves unknown/uninitialized summary cannot become wire-safe.
  - Generator rebuild/parity tests stay deterministic.
  - _Validation: `go test ./pkg/lipsdk/feature/... ./internal/archtest/...`_
  - _Requirements: 5, 16_

## 6. Add Conservative `frontendpipe` Candidate Processing With Decode-QoS Parity

- [ ] 6. Add the ingress optimization lane without certifying a protocol yet

- [ ] 6.1 Add optional `WireProfile` plumbing and bounded frontend-owned wire state
  - Nil profile means canonical-only and must not allocate a spool/scanner.
  - Profile owns only semantic proof/extraction; no backend selection/provider I/O.
  - Keep raw headers/auth/session carriers at frontend boundary; pass only normalized bounded facts into core.
  - Provide request-local canonical state storage for fallback and bounded wire frontend state for response encoding.
  - _Validation: `go test ./internal/plugins/frontends/frontendpipe/...`_
  - _Requirements: 1, 4, 14, 18_

- [ ] 6.2 Preserve cheap canonical gates before capture
  - Order: handler auth/content-type/path → feature/profile/large-body-executor gate → frontend full-body traffic gate → known identity Content-Length below threshold → gzip-wave1 gate → capture.
  - Non-noop frontend ingress traffic requiring full `[]byte` selects current canonical path before spool so emission order remains unchanged.
  - Disabled/nil-profile/traffic-blocked/below-threshold cases must not create temp files or materially regress allocations.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/... ./internal/plugins/frontends/reqbody/...`_
  - _Requirements: 1, 2, 10, 12, 15_

- [ ] 6.3 Capture to EOF + shared streaming preflight, then preserve decode admission
  - During capture run only shared lexical/shape preflight and bounded field observation; do **not** perform expensive protocol semantic proof during client upload.
  - After final decoded size is known, call current `decodeqos.TryAdmit` with that exact byte weight.
  - Run profile semantic proof from a replay reader while the permit is held.
  - If profile declines at this point, materialize and run current `Spec.Decode` under current admission semantics; then follow ordinary post-decode/`AfterDecode`/traffic/executor flow.
  - Release admission exactly once on success/error/panic and preserve current saturation/error mapping.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/... ./internal/plugins/frontends/decodeqos/...`_
  - _Requirements: 3, 17_

- [ ] 6.4 Build trusted pre-turn `Canonicalize` callback
  - Reopen/materialize the immutable source and call the exact existing decoder/validation/`AfterDecode` path.
  - Populate the request-local canonical frontend state holder exactly once.
  - Callback must perform no executor/session/provider work itself.
  - It is only invoked before V1 `BeginTurn`/wire commit.
  - Add tests proving a late core eligibility decline yields the same canonical frontend state/error and no duplicate decode-side side effects.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/...`_
  - _Requirements: 1, 6, 18_

## 7. Refactor Frontend Response Encoding Onto Bounded Wire Response Facts

- [ ] 7. Make response behavior explicit before opening any wire backend

- [ ] 7.1 Add the generic `ExecutionResult`/wire response bridge to `frontendpipe`
  - On a wire result, combine frontend-owned `WireFrontendState` with provider-neutral `ResponseFacts` to build wrap/encode state.
  - On canonical fallback returned through `ExecuteLargeBody`, use the normal canonical state holder populated by `Canonicalize`.
  - Do not create a fake Call or put frontend state in core.
  - Keep existing canonical `WriteStream`/`WriteNonStream` behavior source-compatible.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/...`_
  - _Requirements: 18_

- [ ] 7.2 Refactor/characterize OpenAI Responses response facts
  - Preserve cancellation semantics using authoritative A-leg/session facts.
  - Define how response ID and timestamp are generated for wire execution; document any protocol-opaque difference and normalize it only in differential tests if allowed.
  - Add cancel-by-returned-ID tests for wire-mode response facts before the backend lane is enabled.
  - _Validation: `go test ./internal/plugins/frontends/openairesponses/... ./internal/stdhttp/...`_
  - _Requirements: 18_

- [ ] 7.3 Add architecture tests for response-state boundaries
  - Core/SDK cannot import frontend response-state types.
  - Wire frontend cannot satisfy response encoder by synthesizing `lipapi.Call`.
  - Certified profile must register/cover its response dependency inventory.
  - _Validation: `go test ./internal/archtest/...`_
  - _Requirements: 16, 18_

## 8. Implement Pre-`BeginTurn` Core Eligibility and the One-Way Commit Point

- [ ] 8. Avoid the original secure-preparation split in V1

- [ ] 8.1 Add side-effect-free static/generation eligibility
  - Check feature/profile, generated AccessSummary, core traffic/raw capture/redaction blockers, Call-only callbacks, and other generation-global authorities.
  - Treat configured post-A-leg route-override authority as V1 canonical-only unless a typed pre-turn contract exists.
  - Treat any stage whose wire safety can only be decided after content/session mutation as a blocker rather than starting a turn to inspect it.
  - Return bounded fallback reason; do not call `BeginTurn`.
  - _Validation: `go test ./internal/core/runtime/... -run 'LargeBody.*Eligibility|Commit'`_
  - _Requirements: 5, 6, 12, 19_

- [ ] 8.2 Build a conservative pre-turn selector/candidate superset
  - Compile selector with existing aliases/default backend and validate execution composition from exact profile facts.
  - Bind native models using generation-pinned resolver only where exact pre-turn semantics are available.
  - Enumerate every candidate that weighted-first/affinity/interleaved/recovery can later select from the configured selector; do not use post-A-leg state to prune the proof set.
  - If candidate set cannot be conservatively established, fallback before `BeginTurn`.
  - _Validation: `go test ./internal/core/runtime/... ./internal/core/routing/...`_
  - _Requirements: 6, 7, 9_

- [ ] 8.3 Implement the V1 commit state machine
  - All expected decline paths call `Canonicalize` then ordinary `Execute` **before** beginning the large-body logical turn.
  - Only after all core/backend prechecks pass may the wire execution branch call secure-session/A-leg preparation.
  - After commit, an unexpected “canonical content required” state is an internal invariant failure: abort/finalize once and never invoke a second `Execute`.
  - Add counters asserting canonical fallback has zero `BeginTurn`/A-leg/billing/provider side effects and wire commit has exactly one lifecycle.
  - _Validation: `go test -race ./internal/core/runtime/... -run 'LargeBody|CommitPoint|BeginTurn'`_
  - _Requirements: 1, 6, 16_

## 9. Close the Post-Commit Full-Call Dependency and Keep Stock Billing Useful

- [ ] 9. No wire branch may rely on a fake/partial canonical request

- [ ] 9.1 Introduce only the exact bounded internal `wireExecutionFacts` proven necessary by Task 1.5
  - Fields come from actual consumers: operation/delivery, selector/model/protocol requirements, bounded output control, body-size facts, trace/A-leg/session identity, etc.
  - Each field has a producer, consumer, and parity test.
  - Do not mirror the whole Call schema.
  - _Validation: `go test ./internal/core/runtime/...`_
  - _Requirements: 19_

- [ ] 9.2 Refactor route/capability derivations to exact-fact helpers
  - Add failover-requirement construction from exact `ProtocolRequirements`.
  - Extract selector/native-model/request-size helpers where semantics do not require content.
  - Canonical path should use the same helper with facts derived from its real Call where practical.
  - Never estimate tokens from raw body bytes.
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/core/capabilities/... ./internal/core/routing/...`_
  - _Requirements: 7, 12, 19_

- [ ] 9.3 Audit/refactor `recvTurnFacts`, continuation support, interleaved-thinking, and terminal usage
  - Metadata-only uses move to exact views/facts.
  - Content-requiring uses become pre-turn blockers.
  - Add an arch/AST ratchet that wire/post-commit functions cannot dereference `preparedRequest.call` except explicitly allowlisted canonical functions.
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/archtest/...`_
  - _Requirements: 6, 9, 19_

- [ ] 9.4 Make stock billing semantics explicit
  - Characterize standard `PrincipalSessionIdentity`, charge-policy/pricing/max-output inputs, exposure admission, terminal usage, and request-token estimator behavior.
  - Where exact bounded facts suffice, add a typed wire billing admission input/helper and make canonical stock billing derive the same input from Call.
  - Custom arbitrary Call callbacks remain a pre-turn blocker unless they opt into an exact wire contract.
  - If standard billing cannot yet be made wire-safe without scope explosion, document/test that as a fallback reason and ensure the final eligibility matrix states it clearly.
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/infra/billingadmission/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 12, 15, 19_

## 10. Add Backend Wire Proof and Streaming Model Rewrite

- [ ] 10. Define driven-adapter support before any backend opts in

- [ ] 10.1 Extend internal backend contract additively with `ResolveWireRequest` / `OpenWire`
  - Provider-neutral facts only; nil means canonical-only.
  - `ResolveWireRequest` performs no provider/network I/O and declares exact profile/operation/body/rewrite/parallel-open support.
  - Preserve external plugin ABI unless a separately versioned extension is intentionally designed.
  - Add contract tests for all current nil-wire backends.
  - _Validation: `go test ./internal/core/execbackend/... ./internal/plugins/backends/...`_
  - _Requirements: 7, 11, 16_

- [ ] 10.2 Implement token-span model splice reader
  - Validate source span/size, marshal replacement JSON token, and stream prefix/replacement/suffix.
  - Checked `int64` rewritten length.
  - Tests: same/longer/shorter/escaped model, nested misleading model, model text in giant content, late model, whitespace variants, invalid/ambiguous span, duplicate-model decline.
  - _Validation: `go test ./pkg/lipsdk/requestbody/... ./internal/plugins/frontends/reqbody/...`_
  - _Requirements: 4, 8, 13_

- [ ] 10.3 Prove wire support for the full conservative candidate superset
  - Query support for every possible candidate before V1 commit.
  - Any incompatible candidate, unsupported parallel mode, or rewrite failure causes canonical fallback before `BeginTurn`.
  - Never drop incompatible candidates/change weights/serialize a race to retain wire mode.
  - _Validation: `go test ./internal/core/runtime/...`_
  - _Requirements: 6, 7, 9_

## 11. Implement `ExecuteLargeBody` on the Existing Lifecycle/Attempt/Response Machinery

- [ ] 11. Wire execution only after Tasks 1–10 are green

- [ ] 11.1 Implement pre-turn proof → fallback-or-commit orchestration
  - Run static/access/Call-dependency/route-superset/backend proof with no turn side effects.
  - Fallback: invoke trusted `Canonicalize`, return/use ordinary `Execute`, and mark `ExecutionResult.PathCanonicalFallback` (or equivalent) for frontend state selection.
  - Wire: take ownership of source, cross one-way commit, begin one normal logical turn, and produce bounded `ResponseFacts` from authoritative runtime identity.
  - Close source on every terminal branch after replay is no longer needed.
  - _Validation: `go test -race ./internal/core/runtime/...`_
  - _Requirements: 1, 6, 7, 13, 18, 19_

- [ ] 11.2 Integrate wire provider opens into existing B-leg/attempt/recovery owner
  - Same B-leg allocation, attempt budget, TTFT, affinity, weighted-first, interleaved, failure history, stream recovery, and first-visible-output commit behavior.
  - Each credential/provider retry calls `Source.Open()` from zero and applies its candidate rewrite.
  - Response parser emits same canonical EventStream and uses existing stream assembler/terminal accounting.
  - No goroutine per chunk.
  - _Validation: `go test -race ./internal/core/runtime/... -run 'LargeBody|Retry|Failover|Race'`_
  - _Requirements: 9, 11, 12, 19_

- [ ] 11.3 Add commit/invariant failure tests
  - Unexpected post-commit content requirement aborts/finalizes one turn and never calls canonical `Execute`.
  - Attempt 1 pre-output failure → attempt 2 gets complete source; no attempt after first visible output.
  - Parallel attempts have independent readers and identical non-model bytes.
  - Cancellation closes attempt readers/source and preserves lifecycle/accounting.
  - _Validation: `go test -race ./internal/core/runtime/...`_
  - _Requirements: 6, 9, 13_

## 12. Certify Lane 1: OpenAI Responses → OpenAI-Compatible Responses

- [ ] 12. First production lane proves frontend response facts and backend raw HTTP together

- [ ] 12.1 Implement conservative OpenAI Responses semantic profile
  - Exact create endpoint only.
  - Validate supported input/tool/function/reasoning/text structures sufficiently to prove no canonical repair/drop/normalization beyond permitted model rewrite.
  - Canonical-only: proxy/session body metadata, malformed function history current decoder repairs/skips, unsupported aliases, unknown fields canonical encoder drops, duplicate protocol-owned names, and any unproven extra-body behavior.
  - Extract exact model/stream/operation/bounded output controls/protocol requirements/model span.
  - Run semantic verifier only under Task 6.3 decode admission.
  - _Validation: `go test ./internal/plugins/frontends/openairesponses/...`_
  - _Requirements: 3, 4, 14, 17_

- [ ] 12.2 Add OpenAI-compatible Responses wire backend open
  - Reuse current credential pool/env resolution/cooldown/auth-invalid behavior, base URL, shared HTTP client, identity headers, response parser, first-event peek, and failure classification.
  - Direct body streaming; disable/avoid hidden SDK retries because core owns replay.
  - Set valid content length after rewrite and omit stale encoding/transfer state.
  - _Validation: `go test -race ./internal/plugins/backends/openaicompat/...`_
  - _Requirements: 7, 8, 11_

- [ ] 12.3 End-to-end canonical-vs-wire conformance
  - Provider capture compares method/endpoint/relevant headers, effective JSON semantic value after model rewrite, stream mode, error classification, and canonical response events.
  - Frontend compare covers response/cancellation semantics; normalize only documented protocol-opaque IDs/timestamps.
  - Cover API-key retry, 4xx/429/5xx, transport failure before first event, stream/non-stream, route failover/race, cancellation by returned response ID, malformed/normalization fallback, and decode-admission saturation.
  - Only after this suite passes may the profile/backend advertise production wire support.
  - _Validation: `go test -race ./internal/plugins/frontends/openairesponses/... ./internal/plugins/backends/openaicompat/... ./internal/testkit/conformance/...`_
  - _Requirements: 1, 7, 9, 11, 14, 17, 18_

## 13. Certify Lane 2: OpenAI Chat Completions → OpenAI-Compatible Chat

- [ ] 13. Add Chat only after Lane 1 is green

- [ ] 13.1 Implement conservative Chat semantic profile
  - Preserve current role/message/tool/function/reasoning normalization rules and route-model semantics.
  - Canonical-only for malformed histories/aliases/unknown fields/duplicates/metadata cases whose canonical re-encode differs.
  - Extract exact bounded facts and model span under decode admission.
  - _Validation: `go test ./internal/plugins/frontends/openailegacy/...`_
  - _Requirements: 3, 4, 14, 17_

- [ ] 13.2 Add Chat wire backend support and response-state binding
  - Reuse OpenAI-compatible credential/client/parser/error machinery.
  - Resolve completion ID/timestamp without a fake Call; document any protocol-opaque difference.
  - Differential tests for stream/non-stream, tool calls, retries/failover, model rewrite, errors, and response envelope.
  - _Validation: `go test -race ./internal/plugins/frontends/openailegacy/... ./internal/plugins/backends/openaicompat/...`_
  - _Requirements: 7, 9, 11, 14, 18_

## 14. Certify Lane 3: OpenResponses HTTP Create With Explicit `store:false`

- [ ] 14. Do not treat default OpenResponses create as stateless

- [ ] 14.1 Refactor/characterize OpenResponses frontend state for a no-store wire subset
  - Initial subset: HTTP create, **explicit `store:false`**, absent `previous_response_id`, no compaction, no WebSocket.
  - Missing `store` is canonical because current default is true.
  - Build bounded wire frontend state for response ID/options/wrappers without continuation reservation/recorder state.
  - Prove no ordinary `AfterDecode` failure/side effect is shifted behind `BeginTurn` for this subset.
  - _Validation: `go test ./internal/plugins/frontends/openresponses/...`_
  - _Requirements: 6, 14, 18_

- [ ] 14.2 Implement OpenResponses semantic profile and compatible backend wire open
  - Preserve strict duplicate policy, supported create fields/limits, stream control, requirement derivation, endpoint/auth/client/parser/error behavior.
  - Unknown/unsupported controls and any `store:true`/continuation request are canonical-only.
  - _Validation: `go test -race ./internal/plugins/frontends/openresponses/... ./internal/plugins/backends/openresponsescompat/...`_
  - _Requirements: 7, 11, 14, 17_

- [ ] 14.3 Add OpenResponses differential conformance
  - Compare provider-effective JSON/endpoint/headers and canonical response events.
  - Verify response ID/wrappers/allowed-tool behavior for explicit no-store subset.
  - Assert missing/true store and previous-response requests never reach wire backend.
  - Cover retry/failover/cancel and malformed/duplicate inputs.
  - _Validation: `go test -race ./internal/plugins/frontends/openresponses/... ./internal/plugins/backends/openresponsescompat/... ./internal/testkit/conformance/...`_
  - _Requirements: 1, 9, 14, 18_

- [ ] 14.4 Treat `store:true`/continuation as a later certification, not incidental scope
  - Only add after exact reservation/response-ID/recorder/cleanup/lineage semantics are designed and tested.
  - Do not broaden this while fixing unrelated test failures.
  - _Requirements: 14, 18_

## 15. Add Gzip Support as a Separate Follow-Up Wave

- [ ] 15. Wave 1 is identity JSON only

- [ ] 15.1 Prove wave-1 gzip always selects canonical before spool/profile
  - No change to current decompression/limit/error behavior.
  - _Validation: frontend/reqbody gzip tests_
  - _Requirements: 10_

- [ ] 15.2 Optional later task: capture decoded gzip JSON with exact current limits
  - Use current bounded decompressor semantics.
  - Threshold/reservation are decoded bytes; never compressed Content-Length.
  - Provider sends identity JSON with stale Content-Encoding removed.
  - Re-run scanner/profile/backend conformance for gzip corpus.
  - _Depends: all enabled identity-body lanes stable first_
  - _Requirements: 10_

## 16. Observability, Benchmarks, and Realistic Eligibility Evidence

- [ ] 16. Measure the actual value and fallback surface

- [ ] 16.1 Add bounded metrics/traces
  - Considered/wire/fallback counts; static reason enum; body-size buckets; memory/file spool; active logical spool bytes; replay/rewrite counts; capture/preflight/proof/provider-open latency.
  - No model/backend/user/session IDs in metric labels; no body/path in telemetry.
  - _Validation: metrics cardinality/privacy tests_
  - _Requirements: 13, 16_

- [ ] 16.2 Add allocation/CPU/GC benchmarks
  - 32 KiB, 256 KiB, 1 MiB, 5 MiB, test-only 20 MiB.
  - Canonical disabled baseline vs wire lane; giant strings, late model, tools, malformed JSON, replay/failover.
  - Report allocs/op, B/op, CPU, file I/O, GC/heap, and provider-open latency.
  - Explicitly state full-body pre-commit validation means this is mainly a heap/GC/redundant-work optimization.
  - _Validation: benchmark commands documented in `research.md`/PR results_
  - _Requirements: 15_

- [ ] 16.3 Run concurrent load and spool-budget saturation
  - Include realistic session concurrency and slow-upload cases.
  - Prove decode permits are not held during upload.
  - Compare budget exhaustion behavior against disabled canonical baseline; do not claim the optimization budget bounds canonical heap fallback.
  - _Requirements: 13, 15, 17_

- [ ] 16.4 Publish realistic eligibility matrix
  - At minimum: extensions empty vs representative request plane occupied; route override reader off/on; stock billing off/on; frontend traffic off/on; sequential vs weighted/fallback/race selectors; each certified frontend/backend lane.
  - Require at least one realistic production-like configuration to actually hit the wire lane.
  - If stock billing still blocks, quantify/document it before declaring completion.
  - _Requirements: 5, 6, 12, 15, 19_

## 17. Final Regression Gate and Rollout

- [ ] 17. Do not enable profiles until all compatibility proof is green

- [ ] 17.1 Add architecture ratchets
  - No unclassified production plane.
  - No provider-name switch/provider SDK type in core/requestbody SDK.
  - No fake Call on wire branch.
  - No post-commit `preparedRequest.call` dereference beyond explicitly canonical allowlist.
  - No protocol semantic proof outside decode admission.
  - No expected canonical fallback introduced after V1 `BeginTurn` commit.
  - No route candidate pruning/reordering for eligibility.
  - _Validation: `go test ./internal/archtest/...`_
  - _Requirements: 5, 6, 16, 17, 19_

- [ ] 17.2 Run full quality gate
  - `go test ./...`
  - targeted `go test -race` for frontendpipe/requestbody/runtime/backend lanes
  - `go vet ./...`
  - repository lint/staticcheck commands required by CI
  - differential conformance suites
  - allocation/load evidence
  - _Requirements: all_

- [ ] 17.3 Keep rollout default-off and document operational caveats
  - Explicit opt-in for first release.
  - Document supported protocol/backend subsets and canonical-only triggers.
  - Document spool plaintext/storage requirements, decode-QoS behavior, fallback metrics, route-override limitation, and whether standard billing is wire-safe.
  - Profile broadening requires new conformance corpus in the same change.
  - _Requirements: 13, 14, 15, 16_
