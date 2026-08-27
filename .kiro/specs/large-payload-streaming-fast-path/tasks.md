# Implementation Plan

This plan is staged for a regression-sensitive brownfield codebase. The existing canonical path remains the oracle. No backend advertises wire support until replay, decode-QoS, planner, session/lifecycle, Call-dependency, response-state and conformance gates are green.

The final V1 architecture is:

```text
capture/shared preflight
  → one decode-admission grant
  → protocol semantic proof
  → bounded side-effect-free core PlanLargeBody
       decline: canonical Spec.Decode under SAME grant
       accept:  release grant → ExecuteLargeBody(plan, source)
  → one-way wire commit before BeginTurn
```

There is no core-owned `Canonicalize` callback and no expected canonical fallback after `BeginTurn`.

All tasks assume the implementation branch is first rebased onto current `main`. Review baseline: `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533.

## Execution Rules

- TDD/characterization first for every brownfield refactor.
- Never fabricate a partial `lipapi.Call`.
- Never decode canonically outside the current decode-admission authority merely because the fast path was considered.
- Never acquire a second decode permit for a core fast-path decline.
- `PlanLargeBody` is bounded and side-effect-free: no BeginTurn, A-leg/store/DB/provider/network I/O, client-body wait, spill I/O or arbitrary unbounded plugin work.
- Canonical fallback remains frontend-owned and happens before wire execution.
- Do not prune/reorder candidates or change race/sequential semantics to retain optimization.
- Do not add new request compression formats.
- First release remains default-off.
- Keep the low-level V1 replay/planner seam internal unless an actual external plugin consumer requires public API review.

---

## 1. Rebase, Revalidate, and Freeze Brownfield Oracles

- [ ] 1. Revalidate current architecture before production changes

- [ ] 1.1 Rebase onto current `main` and rerun the spec trigger checklist
  - Confirm typed `feature.Plane[T]`/manifest/generated frozen storage/FrozenPlaneSet remain authoritative.
  - Confirm `frontendpipe` still orders body read → shared preflight → `decodeqos.TryAdmit` → guarded `Spec.Decode` → post-decode/`AfterDecode` → frontend traffic → executor.
  - Confirm session carriers still flow through `sessionwire` and secure preparation still consumes a real Call.
  - Confirm route override remains post-A-leg keyed; if changed, re-evaluate its blocker classification.
  - Search new Call consumers, frontend `AfterDecode` side effects, billing callbacks and response writer dependencies.
  - _Validation: `go test ./internal/archtest/... ./internal/plugins/frontends/frontendpipe/... ./internal/core/runtime/...`_
  - _Requirements: 5, 6, 7, 18, 19, 20, 21_

- [ ] 1.2 Freeze canonical ingress and decode-admission behavior
  - Method/path/auth/content-type ordering for target frontends.
  - `reqbody.ReadAll` identity/gzip decoded limits and exact limit/+1 behavior.
  - Shared JSON malformed/trailing/boundary behavior.
  - Decode-admission weight, saturation, overweight, cancellation, panic-safe release and `Retry-After`.
  - Route-from-body-model ordering relative to preflight/admission/decode.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/... ./internal/plugins/frontends/decodeqos/... ./internal/plugins/frontends/reqbody/...`_
  - _Requirements: 1, 2, 3, 7_

- [ ] 1.3 Freeze secure-session/A-leg and detached lifecycle behavior
  - Count principal/scope resolution, session-open stage, workspace resolve, `SecureSession.BeginTurn`, A-leg fetch, route-authority snapshot/barrier, request authority, billing, B-legs, terminal/finalization and stream handoff.
  - Freeze new/resume/denial/cancel/error ordering.
  - Characterize detached mode separately; V1 will initially block it unless explicitly implemented.
  - Freeze session header precedence and response-carrier behavior.
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/plugins/frontends/sessionwire/...`_
  - _Requirements: 6, 19, 20, 21_

- [ ] 1.4 Inventory frontend response-state dependencies
  - OpenAI Responses: response ID, cancellation carrier, model, timestamp, session response carriers, stream/non-stream writers and debug helpers.
  - OpenAI Chat: completion ID/model/timestamp/session response carriers.
  - OpenResponses: `AfterDecode`, `prepareCreateState`, no-store/store state, wrappers/options/observers.
  - Mark fields exact vs protocol-opaque/non-deterministic.
  - _Validation: target frontend tests_
  - _Requirements: 16, 19_

- [ ] 1.5 Create commit-onward full-Call dependency inventory + ratchet seed
  - Search secure/detached preparation, `preparedRequest.call`, `identity.ingressCall`, canonical baselines and Call-typed callbacks from the proposed wire commit onward.
  - Include routing/request-size/failover, billing/token/context, `recvTurnFacts`, continuation, interleaved-thinking, terminal/session and response helpers.
  - Classify each as `pre-plan blocker`, `exact bounded fact`, or `response-only`.
  - Seed AST/architecture allowlist so new uses fail until classified.
  - _Validation: `go test ./internal/core/runtime/... ./internal/archtest/...`_
  - _Requirements: 20_

- [ ] 1.6 Capture canonical performance baseline
  - 32 KiB, 256 KiB, 1 MiB, 5 MiB plus test-only raised-limit 20 MiB.
  - Record allocs/op, B/op, CPU and current decode/encode timing.
  - _Requirements: 17_

---

## 2. Configuration and Internal Provider-Neutral Contracts

- [ ] 2. Add additive zero-behavior-change plumbing

- [ ] 2.1 Add `server.large_payload_fast_path` configuration
  - `enabled`, `threshold_bytes`, `memory_spool_bytes`, `max_inflight_spool_bytes`, `spool_dir`.
  - Default-off; validate positive relationships and explicit spool directory on candidate generation/reload.
  - Preserve `MaxRequestBodyBytes` defaults.
  - Document spool plaintext and optimization-budget semantics.
  - _Validation: config + reload tests_
  - _Requirements: 1, 2, 15, 18_

- [ ] 2.2 Define an internal provider-neutral large-body package
  - Add `PolicySnapshot`, `ProfileID`, `Span`, immutable replay `Source`, bounded `Metadata`, `SessionIngressFacts`, `PlanDecision`/accepted `Plan`, `WireRequestFacts`, `RewritePlan`, `ExecutionResult` and `ResponseFacts`.
  - No raw headers, provider SDK types, temp paths, arbitrary maps or prompt contents.
  - Mark resume authority as sensitive and content-free telemetry must never expose it.
  - Prefer `internal/core/largebody` (or equivalent internal package), not `pkg/lipsdk/requestbody`.
  - _Validation: internal package tests + architecture import checks_
  - _Requirements: 8, 18, 19, 21, 22_

- [ ] 2.3 Keep public executor compatibility unchanged
  - Do not add mandatory methods to `lipsdk.ExecutorView`.
  - `frontendpipe` type-asserts the dynamic executor to an internal optional large-body interface.
  - Existing external/manual executors compile unchanged and remain canonical-only.
  - Add a legacy-executor compatibility test.
  - _Requirements: 1, 22_

- [ ] 2.4 Make ingress policy generation-pinned
  - Ensure threshold/spool settings used before capture come from the same immutable generation as later planning/execution.
  - Add reload test proving a long-lived `frontendpipe` `sync.Once` spec cannot retain stale fast-path policy unless its handler is generation-rebuilt.
  - _Requirements: 5, 18_

---

## 3. Replay Capture, Reservation, and Lossless Mid-Capture Continuation

- [ ] 3. Build capture independently of protocol/routing logic

- [ ] 3.1 Add bounded logical spool reservation manager
  - Known identity lengths may reserve safely up front; unknown/chunked reserve incrementally.
  - Overflow-safe concurrency; exact once release.
  - Reservation decline is optimization-only, never 413.
  - _Validation: `go test -race ./internal/plugins/frontends/reqbody/...`_
  - _Requirements: 15, 17_

- [ ] 3.2 Implement bounded RAM prefix + private temp spill
  - Fixed/reusable copy buffer; no full-body `bytes.Buffer` growth on candidate path.
  - Preserve every consumed byte.
  - Keep current input chunk owned until destination write fully succeeds.
  - On short write, record durable prefix and retain unwritten suffix.
  - Private non-user-derived temp name/permissions.
  - _Requirements: 2, 15_

- [ ] 3.3 Implement explicit canonical-continuation reader for in-progress capture
  - Stitch: retained memory/file prefix + current unwritten bytes + unread client stream.
  - Materialize under the existing identity-body request ceiling exactly once.
  - Use on reservation decline, file-create failure and recoverable partial write failure.
  - Never restart/re-read the client socket.
  - Byte-for-byte randomized chunk-boundary tests against direct canonical read.
  - _Requirements: 1, 15_

- [ ] 3.4 Implement immutable completed Source and independent readers
  - Offset-zero reader per `Open`; independent concurrent cursors.
  - Root Close idempotent/nonblocking; final delete after root closed + readers zero.
  - No cleanup goroutine; include Windows open-file deletion state tests.
  - _Requirements: 11, 15_

- [ ] 3.5 Fault-injection/leak/privacy tests
  - Reservation failure between chunks; create failure; short/partial writes; read/remove failure; cancellation; timeout; exact EOF/limit; reader leak simulation.
  - Assert no body/path/resume token leaks into diagnostics.
  - _Validation: `go test -race ./internal/plugins/frontends/reqbody/... ./internal/core/largebody/...`_
  - _Requirements: 15, 18_

---

## 4. Shared Low-Allocation JSON Scanner

- [ ] 4. Differentially prove shared safety parity

- [ ] 4.1 Implement incremental lexer/state machine
  - UTF-8, escapes/surrogates, number grammar, root/delimiters/trailing/incomplete, shared byte/token/depth/member/array/key/string/number bounds and cancellation.
  - No proportional retention of ordinary large strings.
  - _Validation: `go test ./internal/core/jsonshape/...`_
  - _Requirements: 3_

- [ ] 4.2 Add bounded selected-token/path/span observation
  - Exact raw offsets; nested key discrimination; bounded selected decoded values.
  - Scanner remains provider-neutral.
  - _Requirements: 4, 10_

- [ ] 4.3 Add differential/fuzz corpus against slice preflight
  - Every relevant buffer split, giant strings, deep/wide boundaries, invalid UTF-8/escapes/numbers, duplicates, trailing data and cancellation.
  - Compare stable classification/counts, not incidental error text.
  - _Requirements: 3, 17_

---

## 5. Extend Typed Plane Manifest With Request-Body Access Metadata

- [ ] 5. One classification system only

- [ ] 5.1 Add explicit access enum to canonical plane declarations
  - `Unclassified`, `CanonicalRequired`, `MetadataOnly`, `ResponseOnly` (equivalent names allowed).
  - Annotate every current production plane based on actual semantics.
  - Do not add a legacy named-field mirror.
  - _Requirements: 5_

- [ ] 5.2 Extend generator/frozen set to derive bounded AccessSummary
  - No request-path reflection/map/type-assertion walk.
  - Publish read-only generation summary; runtime unknown fails closed.
  - _Requirements: 5, 18_

- [ ] 5.3 Add declaration/generator/runtime ratchets
  - New production plane without classification fails.
  - Uninitialized summary never becomes wire-safe.
  - Generator parity deterministic after #533 architecture.
  - _Validation: `go test ./pkg/lipsdk/feature/... ./internal/archtest/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 5, 18_

---

## 6. Add Frontend Candidate Processing and Single-Grant Protocol Proof

- [ ] 6. Add candidate ingress without enabling any protocol

- [ ] 6.1 Add optional internal WireProfile plumbing and frontend-owned wire state
  - Nil profile or missing internal large-body executor means canonical-only with no spool/scanner.
  - Profile owns protocol proof/extraction only; no backend selection/network.
  - Add bounded session ingress facts lifted from configured header names/precedence.
  - Raw headers stay in frontend; body-carried proxy/session metadata initially canonical-only.
  - _Requirements: 4, 16, 19, 21, 22_

- [ ] 6.2 Preserve cheap canonical gates before capture
  - Handler auth/content-type/path → feature/policy/profile/executor → frontend full-body traffic gate → known identity length below threshold → gzip wave-1 gate → capture.
  - Disabled/blocked/below-threshold requests must not create temp files or materially regress allocations.
  - _Requirements: 1, 2, 12, 14, 17_

- [ ] 6.3 Capture to EOF with shared streaming preflight
  - Use Task 3 continuation on mid-capture optimization decline.
  - Unknown/chunked final-below-threshold uses ordinary canonical path.
  - Shared JSON invalid/over-limit maps exactly as current frontend.
  - _Requirements: 1, 2, 3, 15_

- [ ] 6.4 Acquire one decode-admission grant and run semantic profile proof
  - Exact final decoded byte weight.
  - Permit is never held during upload.
  - Profile proof uses Source.Open under the grant.
  - Do **not** release the grant yet on profile success; Task 8 final planner still must run.
  - Profile decline invokes Task 6.5 under the same grant.
  - _Requirements: 7, 16_

- [ ] 6.5 Add canonical decode fallback helper that reuses the currently-held grant
  - Materialize completed source and call exact current `Spec.Decode` while the original grant remains held.
  - Release once after Decode, then run existing header application/Validate/`AfterDecode`/traffic/executor flow.
  - No second `TryAdmit` and no unguarded canonical Decode.
  - Add saturation/cancel tests proving consideration of fast path cannot create a second admission rejection.
  - _Requirements: 1, 7, 19_

---

## 7. Add Backend Wire Declaration and Rewrite Primitives

- [ ] 7. Define pure backend proof before core planning uses it

- [ ] 7.1 Extend internal `execbackend.Backend` additively with wire declaration/open fields
  - `ResolveWireRequest` pure/config-derived; `OpenWire` post-commit only; nil means canonical-only.
  - External backend plugin ABI unchanged.
  - No provider-name switches in core.
  - _Requirements: 9, 13, 22_

- [ ] 7.2 Implement token-span streaming model rewrite
  - Validate span/source size; `json.Marshal(nativeModel)` replacement; prefix/replacement/suffix reader; checked length.
  - Cover escaped/longer/shorter/late/nested/misleading/duplicate cases.
  - _Requirements: 4, 10, 15_

- [ ] 7.3 Add backend-contract purity tests
  - Existing backends with nil declaration remain canonical-only.
  - `ResolveWireRequest` cannot perform provider network I/O and returns bounded reason/support facts.
  - _Validation: `go test ./internal/core/execbackend/... ./internal/plugins/backends/...`_
  - _Requirements: 8, 9, 18_

---

## 8. Implement Bounded Side-Effect-Free `PlanLargeBody` Under the Same Decode Grant

- [ ] 8. Final expected eligibility happens before permit release/BeginTurn

- [ ] 8.1 Add internal optional planner/executor interface
  - `LargeBodyPolicy`, `PlanLargeBody`, `ExecuteLargeBody` (equivalent names allowed).
  - Accepted Plan is immutable/generation-pinned; HTTP caller cannot construct/select it.
  - No canonical callback in the core contract.
  - _Requirements: 6, 7, 8, 22_

- [ ] 8.2 Implement static/generation blockers
  - Plane AccessSummary, core raw/request traffic, Call-only callbacks, content stages, route-override authority, detached session mode, unsupported generation/profile/body mode.
  - All evaluation pure/bounded under decode permit.
  - _Requirements: 5, 6, 8, 14, 20_

- [ ] 8.3 Build conservative selector/candidate superset and resolve wire support
  - Existing aliases/defaults/execution composition/native-model facts.
  - Every candidate weighted-first/affinity/interleaved/recovery may later use.
  - Query pure backend wire support for all candidates; no pruning/reordering.
  - If finite safe superset cannot be established, decline.
  - _Requirements: 6, 9, 11_

- [ ] 8.4 Integrate planner into the held-decode-grant frontend flow
  - Profile success → `PlanLargeBody` while same grant held.
  - Plan decline → Task 6.5 canonical `Spec.Decode` under that grant.
  - Plan accept → freeze plan, release grant once, then frontend calls `ExecuteLargeBody` (with normal pre-request keepalive behavior where applicable).
  - Planning performs no BeginTurn/A-leg/store/DB/provider/network/spill I/O.
  - Measure planner hold time and decode-admission capacity regression.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/... ./internal/core/runtime/... ./internal/plugins/frontends/decodeqos/...`_
  - _Requirements: 6, 7, 8, 17_

---

## 9. Add Fact-Only Turn Preparation and Close All Commit-Onward Call Dependencies

- [ ] 9. No fake Call may be needed after an accepted plan

- [ ] 9.1 Finalize exact wire/session/identity fact inventory
  - Header-derived authoritative session ID/resume token/client session hint using canonical precedence.
  - Trace/call identity policy for wire mode; document internal-only differences if full-Call stable hashing cannot/need not be reproduced.
  - Client user agent or other header-derived fields only if actual consumers require them.
  - Sensitive bearer fields never telemetry/backend.
  - _Requirements: 19, 20, 21_

- [ ] 9.2 Extract a shared fact-only secure BeginTurn/A-leg primitive
  - Characterization first.
  - Share principal/scope, session-open stage, workspace resolution, `SecureSession.BeginTurn`, A-leg fetch, route-authority snapshot/barrier and authoritative identity outputs between canonical and wire branches.
  - Canonical path continues its existing content stages in the exact old order after the shared primitive.
  - This extraction must not enable late canonical fallback.
  - _Validation: `go test -race ./internal/core/runtime/... -run 'Prepare|Secure|BeginTurn|LargeBody'`_
  - _Requirements: 6, 21_

- [ ] 9.3 Keep detached execution canonical-only in V1
  - Add explicit planner blocker/test unless a separate exact fact-only detached implementation is added in this task with parity evidence.
  - _Requirements: 20, 21_

- [ ] 9.4 Refactor routing/capability derivations onto exact facts
  - Protocol requirements helper, selector/native-model helpers, request-size only when exact.
  - Canonical path uses same helpers where practical.
  - No token estimates from body bytes.
  - _Requirements: 9, 14, 20_

- [ ] 9.5 Make stock billing semantics explicit
  - Characterize principal/account identity, pricing/charge policy, max-output exposure, request-token/context estimation and terminal settlement.
  - Add a shared typed exact-fact billing input where possible; custom Call callbacks remain planner blockers.
  - If standard billing still blocks, document/test it and surface it in eligibility matrix.
  - _Requirements: 14, 17, 20_

- [ ] 9.6 Close `recvTurnFacts`, continuation, interleaved-thinking and terminal Call dependencies
  - Exact metadata moves to bounded facts; content uses become pre-plan blockers.
  - Activate AST/architecture ratchet forbidding wire/post-commit canonical Call dereferences outside canonical allowlist.
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/archtest/...`_
  - _Requirements: 20_

---

## 10. Implement `ExecuteLargeBody` on Existing Lifecycle/Attempt/Response Machinery

- [ ] 10. Execute accepted plans only

- [ ] 10.1 Validate accepted plan/generation and transfer Source ownership
  - Mismatch/invalid plan is an invariant error, not canonical fallback.
  - Release/close source on every terminal branch after replay is no longer needed.
  - Produce bounded authoritative ResponseFacts.
  - _Requirements: 6, 8, 15, 19, 21_

- [ ] 10.2 Start one fact-only logical turn and integrate wire attempts
  - Use Task 9 shared BeginTurn/A-leg primitive.
  - Preserve existing B-leg allocation, attempt budget, TTFT, affinity, weighted-first, interleaved, failure history and response stream assembler.
  - Each credential/provider retry opens Source at byte zero and applies candidate rewrite.
  - Provider parser still emits canonical events; no goroutine per body chunk.
  - _Requirements: 9, 11, 13, 14, 20_

- [ ] 10.3 Add invariant/retry/race/cancel tests
  - Unexpected post-commit canonical requirement → one turn abort/finalize, no second Execute.
  - Attempt 1 pre-output failure → complete attempt 2 body.
  - No retry after first visible output.
  - Parallel cursors independent.
  - Cancellation closes readers/source and preserves lifecycle/accounting.
  - _Validation: `go test -race ./internal/core/runtime/...`_
  - _Requirements: 6, 11, 15_

---

## 11. Frontend Response and Session-Carrier Bridge

- [ ] 11. Wire response must not need a fake Call

- [ ] 11.1 Add generic frontendpipe wire response context
  - Combine frontend-owned `WireFrontendState` with bounded core ResponseFacts.
  - Preserve normal holdalive/error/wrap/write ownership.
  - Existing canonical writers remain source-compatible.
  - _Requirements: 19_

- [ ] 11.2 Refactor/characterize OpenAI Responses response facts
  - Response ID/cancellation carrier from authoritative A-leg/session facts.
  - Model/timestamp semantics.
  - Session response carriers equivalent to canonical writer; sensitive resume authority never telemetry.
  - Wire response cancellation-by-returned-ID tests.
  - _Requirements: 19, 21_

- [ ] 11.3 Add OpenAI Chat response context
  - Completion ID/model/timestamp/session response carriers without fake Call.
  - _Requirements: 19, 21_

- [ ] 11.4 Add boundary/privacy ratchets
  - Core cannot import frontend state.
  - Frontend cannot synthesize Call to satisfy writers.
  - Resume tokens cannot enter logs/metrics/backend headers.
  - _Validation: `go test ./internal/archtest/... ./internal/plugins/frontends/...`_
  - _Requirements: 18, 19, 21_

---

## 12. Certify Lane 1: OpenAI Responses → OpenAI-Compatible Responses

- [ ] 12. First production lane

- [ ] 12.1 Implement conservative Responses profile
  - Exact create endpoint; validate supported input/tool/function/reasoning/text structures.
  - Canonical-only: body proxy/session metadata, repair-sensitive malformed history, aliases, unknown discarded fields, duplicate protocol-owned names, unresolved response/session facts.
  - Extract exact model/stream/output controls/protocol requirements/model span/session header facts.
  - Semantic proof only under Task 6.4 decode grant.
  - _Requirements: 4, 7, 16, 21_

- [ ] 12.2 Implement OpenAI-compatible Responses `OpenWire`
  - Reuse current credential pool/env/cooldown/auth-invalid behavior, base URL, shared HTTP client, response parser/first-event/error classification.
  - Direct body reader; no hidden SDK retry; valid rewritten Content-Length; no client auth/hop-by-hop/stale encoding forwarding.
  - _Requirements: 9, 10, 13_

- [ ] 12.3 Canonical-vs-wire end-to-end conformance
  - Provider method/endpoint/headers/effective JSON/model/stream/error events.
  - Frontend response/cancellation/session-carrier semantics.
  - API-key retry, 4xx/429/5xx, transport pre-output failure, stream/non-stream, failover/race, session resume, malformed fallback, decode-admission saturation.
  - Advertise wire support only after suite is green.
  - _Validation: target frontend/backend/conformance tests under `-race`_
  - _Requirements: 1, 7, 9, 11, 13, 16, 19, 21_

---

## 13. Certify Lane 2: OpenAI Chat → OpenAI-Compatible Chat

- [ ] 13. Add only after Lane 1 is stable

- [ ] 13.1 Conservative Chat semantic profile
  - Current role/message/tool/function/reasoning normalization and route-model semantics.
  - Canonical-only malformed/alias/unknown/duplicate/body-session cases with decode→encode differences.
  - Exact bounded facts/model span/session header facts under decode admission.
  - _Requirements: 4, 7, 16, 21_

- [ ] 13.2 Chat wire backend + response conformance
  - Reuse OpenAI-compatible credential/client/parser machinery.
  - Completion response context without fake Call.
  - Stream/non-stream, tool calls, retries/failover/race, model rewrite, session resume/headers and errors.
  - _Requirements: 9, 11, 13, 19, 21_

---

## 14. Certify Lane 3: OpenResponses HTTP Create With Explicit `store:false`

- [ ] 14. Default create is not stateless

- [ ] 14.1 Characterize/refactor no-store frontend state
  - HTTP create; **explicit `store:false`**; no previous response; no compaction/WebSocket.
  - Missing `store` canonical because default is true.
  - Bounded response ID/options/wrapper state; no continuation reservation/recorder.
  - Prove no `AfterDecode` failure/side effect moved behind BeginTurn.
  - _Requirements: 6, 16, 19_

- [ ] 14.2 Implement profile + compatible backend wire open
  - Strict duplicate behavior, supported controls/limits, stream control, requirements, endpoint/auth/client/parser/error parity.
  - `store:true`, continuation and unsupported controls canonical-only.
  - _Requirements: 7, 9, 13, 16_

- [ ] 14.3 Differential conformance
  - Provider-effective semantics + canonical events + no-store response state.
  - Missing/true store and previous-response never reach wire backend.
  - Retry/failover/cancel/malformed/duplicates/session carriers where supported.
  - _Requirements: 1, 11, 16, 19_

- [ ] 14.4 `store:true`/continuation remains later certification
  - Only after reservation/response-ID/recorder/cleanup/lineage parity is explicitly designed.
  - _Requirements: 16, 19_

---

## 15. Gzip Follow-Up

- [ ] 15.1 Prove wave-1 gzip always selects canonical before capture/profile
  - Preserve current decompression limit/error semantics exactly.
  - _Requirements: 12_

- [ ] 15.2 Optional later wave: decoded gzip replay source
  - Threshold/reservation use decoded bytes, never compressed Content-Length.
  - Same scanner/profile/planner/backend conformance; provider sends identity JSON with stale Content-Encoding removed.
  - _Requirements: 12_

---

## 16. Observability, Benchmarks, and Eligibility Evidence

- [ ] 16. Measure both benefit and fallback surface

- [ ] 16.1 Add bounded metrics/traces
  - considered/wire/fallback reason; size buckets; memory/file spool; active logical reservation; replay/rewrite; capture/preflight/profile-proof/**plan**/provider-open latency.
  - No backend/model/user/session IDs in metric labels; no body/path/resume token in telemetry.
  - _Requirements: 15, 18_

- [ ] 16.2 Allocation/CPU/GC benchmarks
  - 32 KiB, 256 KiB, 1 MiB, 5 MiB, test-only 20 MiB; giant string, late model, malformed, replay/failover, mid-capture fallback.
  - Disabled canonical vs eligible wire.
  - _Requirements: 17_

- [ ] 16.3 Decode-admission and concurrent-load evidence
  - Slow uploads prove no permit held during upload.
  - Measure bounded planner extension to permit occupancy and saturation capacity.
  - Spool-budget saturation compared to disabled canonical baseline.
  - _Requirements: 7, 15, 17_

- [ ] 16.4 Publish realistic eligibility matrix
  - Empty vs representative request planes; route override off/on; detached mode; stock billing off/on; frontend traffic off/on; session resume carriers; sequential/weighted/fallback/race; each certified lane.
  - At least one production-like composition must hit wire execution.
  - _Requirements: 5, 6, 14, 17, 20, 21_

---

## 17. Final Architecture and Regression Gates

- [ ] 17.1 Activate ratchets
  - No unclassified production plane or duplicate classification mirror.
  - No provider-name/provider-SDK leakage in core large-body contracts.
  - No public SDK promotion of replay/planner types without explicit review.
  - No fake Call.
  - No canonical Decode outside/reacquired after the original grant due to optimization decline.
  - `PlanLargeBody` has no BeginTurn/store/DB/provider/network/spill side effects.
  - No non-allowlisted post-commit Call dereference.
  - No session bearer telemetry/backend forwarding.
  - No candidate pruning/reordering for eligibility.
  - No expected canonical fallback after wire commit.
  - _Validation: `go test ./internal/archtest/...`_
  - _Requirements: 5, 6, 7, 8, 18, 20, 21, 22_

- [ ] 17.2 Full quality gate
  - `go test ./...`
  - targeted `go test -race` for frontendpipe/reqbody/runtime/backends
  - `go vet ./...`
  - repository lint/staticcheck/parity checks required by CI
  - protocol differential conformance
  - allocation/load/decode-admission evidence
  - _Requirements: all_

- [ ] 17.3 Default-off rollout documentation
  - Supported lanes and canonical-only triggers.
  - Spool plaintext/storage requirements and budget semantics.
  - Decode-QoS single-grant behavior and bounded planner hold extension.
  - Route-override/detached limitations.
  - Session/resume carrier behavior.
  - Whether standard billing is wire-safe.
  - Profile broadening requires conformance corpus in the same change.
  - _Requirements: 15, 16, 17, 18_
