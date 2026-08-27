# Research & Design Decisions

## Summary

- **Feature**: Large-Payload Streaming Fast Path (#503)
- **Initial discovery snapshot**: Go-LIP around `8684666809f687ab43dc1393dc1f726a0b0161f7`, plus Bifrost large-payload implementation/research.
- **Post-PR cross-check snapshot**: Go-LIP `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533.
- **Result of cross-check**: the pre-commit spool/replay direction remains correct, but the original draft was **not implementation-ready without changes**. The review found live architecture drift plus three missing contracts (decode QoS, frontend response state, and deep post-identity Call dependencies) and one unsafe protocol assumption (OpenResponses default storage). The final design also simplifies V1 by eliminating post-`BeginTurn` canonical fallback.

## Final Decisions

1. Keep **bounded-memory pre-commit capture + low-copy replay** rather than immediate client→provider piping.
2. Preserve shared JSON validation during capture, but perform expensive protocol semantic proof in a second replay pass under the existing byte-weighted decode admission.
3. Use the current typed extension-plane manifest/FrozenPlaneSet as the sole request-body access-classification architecture.
4. Never send a fake/minimal `lipapi.Call` through the normal executor.
5. Do not make `ExecuteLargeBody` return only `lipapi.EventStream`; return bounded authoritative response facts needed by frontend response/cancellation encoding.
6. Close every post-commit full-Call dependency before opening a wire backend; content-dependent consumers are pre-turn blockers.
7. In V1, resolve every **expected** optimization blocker before `BeginTurn`. Once the wire path starts a logical turn, it does not fall back to canonical execution.
8. Prove wire compatibility for the conservative superset of every candidate a selector may later choose; do not prune/reorder routes to retain eligibility.
9. Reorder protocol rollout: OpenAI Responses first, OpenAI Chat second, OpenResponses explicit `store:false` third.
10. Keep gzip canonical in wave 1.

---

## 1. Actual Go-LIP Ingress Path

### Sources

- `internal/plugins/frontends/frontendpipe/pipe.go`
- `internal/plugins/frontends/reqbody/body.go`
- `internal/plugins/frontends/decodeqos/admission_http.go`
- `internal/plugins/frontends/jsonguard/*`
- `internal/core/jsonshape/*`
- `internal/plugins/frontends/routeselect/*`
- `internal/jsonbody/*`

### Findings

The issue's early pointer to `internal/jsonbody` is not the main LLM request hot path. Shared create frontends flow through `frontendpipe` and currently:

1. read/decompress the whole request with `reqbody.ReadAll`;
2. run shared JSON preflight;
3. acquire byte-weighted decode admission;
4. run protocol `Decode` while holding the permit;
5. release the permit;
6. apply authoritative session headers, validate, and run `AfterDecode`;
7. emit frontend ingress traffic with a complete body;
8. call the canonical executor.

`reqbody.ReadAll` applies the body cap to the decompressed gzip stream. That decoded-limit behavior is a security contract, not an implementation detail.

### Implication

#503 belongs primarily in `frontendpipe`/request-body capture plus explicit core/backend contracts. It should not rewrite `internal/jsonbody` or each frontend independently.

---

## 2. Why Immediate Client→Provider Streaming Is Not Safe Here

Required metadata can appear at the end of valid JSON, and Go-LIP currently rejects malformed/over-limit/protocol-invalid requests before provider execution. Core route/session/guardrail authorities can also change whether raw forwarding is acceptable.

Therefore the safe first implementation must finish client-body receipt/validation and eligibility proof before upstream body commitment. The intended gain is mostly:

- bounded request-body heap;
- no giant canonical request graph for eligible requests;
- no provider request re-marshal;
- lower GC pressure under concurrent multi-MiB requests;
- replay without reconstructing the body.

It is **not** primarily an early-provider-open/TTFT feature.

---

## 3. Bifrost: Useful Ideas vs Unsafe Ideas to Copy

The initial research inspected Bifrost's large-payload mode and related router/provider plumbing.

### Useful concepts

- threshold before expensive full parsing;
- replayable raw body carried through orchestration;
- small metadata skeleton rather than full request object;
- streaming model replacement rather than full re-marshal;
- decompression as a separable layer;
- explicit provider-side raw-body support.

### Unsafe to copy directly into Go-LIP

- “large mode” that simply skips parsers/hooks;
- raw substring/prefix model replacement;
- assuming same provider/protocol means same semantics;
- bypassing canonical safety/accounting/session stages because the body is large.

Go-LIP has materially more canonical normalization, extension planes, session/B2BUA state, billing, conversation behavior, and response semantics than a thin router.

---

## 4. Shared Streaming JSON Scanner

Current shared preflight is a slice-oriented oracle and `encoding/json.Decoder.Token` is not sufficient as the new large-string implementation strategy because decoded string tokens can themselves allocate proportional to payload size.

The new scanner should incrementally validate lexical structure and shared limits while retaining only bounded selected values/spans. Differential/fuzz tests against the existing preflight are mandatory.

Arbitrary field ordering must work. Route/model extraction cannot assume the relevant key appears in a bounded prefix.

---

## 5. Decode QoS Was Missing From the Original Fast-Path Flow

### Current contract

`frontendpipe` performs shared preflight, then:

```text
decodeqos.TryAdmit(ctx, DecodeAdmission, int64(len(body)))
  → decodeqos.Guard(Spec.Decode)
```

The admission permit protects protocol decode, not client upload or shared preflight.

### Problem in the original draft

The draft allowed the streaming profile observer to perform semantic protocol proof while capturing the client body. That would move expensive protocol work outside `DecodeAdmission`; under load, wire candidates could bypass a configured byte-weighted decode concurrency authority.

Holding the permit during client upload would be wrong in the other direction: slow clients could occupy scarce permits for arbitrarily longer than canonical requests.

### Decision

Use two passes for a wire candidate:

1. capture + shared lexical/shape validation to EOF;
2. acquire decode admission using exact final decoded bytes;
3. reopen the replay source and perform low-allocation protocol semantic proof under the permit.

If the profile declines there, materialize and run the existing protocol `Decode` under the normal admission contract. The extra source read is cheaper/safer than losing QoS or rebuilding the full object graph on the success path.

---

## 6. Typed Extension-Plane Architecture Drift

The original spec was written while the extension-plane consolidation was still in flight and included a conditional design: use typed plane metadata if the manifest exists, otherwise classify legacy named snapshot fields.

That fork is now stale. PR #533 landed the request-shaping migration and current `main` has:

- `pkg/lipsdk/feature/plane_manifest.go`;
- typed `feature.Plane[T]` declarations;
- generated/frozen plane storage and `FrozenPlaneSet`.

### Decision

Request-body access classification must extend the canonical plane declaration/generator. A zero `Unclassified` state is rejected by production manifest validation/architecture tests; runtime unknown additionally fails closed to canonical-required.

Do not add a second named `AccessSummary` mirror maintained manually beside the manifest.

---

## 7. EventStream-Only Return Is Not Enough

### Brownfield evidence

Current frontend response construction still depends on the canonical request object or frontend decoded state after execution.

#### OpenAI Responses

`internal/plugins/frontends/openairesponses/handler.go` uses the decoded Call for response ID construction and timestamps. `responseIDForCall` can encode A-leg/session identity into a cancellation carrier; optimized requests must remain cancellable.

#### OpenAI Chat

Completion ID and fallback timestamp are derived from deterministic Call-based helpers.

#### OpenResponses

`AfterDecode` prepares `createEncodeState`, which participates in response IDs, continuation/store behavior, wrappers, and non-stream output handling.

### Decision

`ExecuteLargeBody` returns an `ExecutionResult` containing canonical events plus bounded provider-neutral `ResponseFacts` such as authoritative call/trace/A-leg/session/operation/delivery facts. Frontend-specific state remains in `frontendpipe`/the frontend.

Do not create a partial Call simply to keep old response function signatures compiling.

Protocol-opaque IDs/timestamps may differ only when the specific protocol permits that and differential conformance explicitly normalizes the field; cancellation/correlation/continuation semantics are not optional.

---

## 8. OpenResponses Default `store=true` Invalidates the Original First-Lane Assumption

`internal/plugins/frontends/openresponses/decode.go` initializes:

```go
store := true
if wireParam.Store != nil {
    store = *wireParam.Store
}
```

So a create request with no `previous_response_id` can still be stateful by default. Its `AfterDecode` path prepares response/continuation state.

### Decision

OpenResponses is moved behind the OpenAI-style lanes. The first OpenResponses certification requires:

- HTTP create;
- **explicit `store:false`**;
- absent `previous_response_id`;
- no compaction;
- no WebSocket;
- a bounded frontend response-state bridge;
- proof that no normal pre-executor `AfterDecode` failure/side effect has been shifted behind `BeginTurn`.

`store:true`/continuation becomes a separate later certification.

---

## 9. Deep Post-Identity `lipapi.Call` Dependencies

The original design correctly avoided a fake Call but underestimated how broadly the real Call remains used after initial preparation.

Observed examples include:

- `executor_route_plan.go`: selector, request-size estimate, failover requirements;
- `billing_admission.go`: account identity, charge policy/pricing refs, exposure input, token estimation, terminal session fields;
- `executor_prepare_request.go`: canonical/ingress baselines and `recvTurnFacts`;
- `executor_assemble_stream.go`: continuation capability and interleaved-thinking recorder construction.

### Decision

Before wire provider work, create a production dependency inventory/ratchet. Every dependency is classified as:

- pre-turn canonical blocker;
- exact bounded wire fact shared with canonical helpers;
- response-only behavior.

No wire/post-commit code may dereference `preparedRequest.call` after this closure unless it is explicitly canonical-only.

### Stock billing note

Production billing composition constructs a real `BillingExposureAdmission` adapter, and its current public input embeds a full Call. This cannot be dismissed as an exotic custom callback. The implementation must explicitly test whether stock billing can be represented by exact bounded facts; if not, billing-on remains a documented blocker and the eligibility matrix must show the impact.

---

## 10. Why V1 Drops Post-`BeginTurn` Canonical Continuation

The original draft proposed splitting canonical executor preparation into an identity phase and a later canonical continuation so dynamic eligibility could fallback after A-leg/session state was established.

The cross-check found that this makes several contracts collide:

1. canonical protocol decode is normally completed under decode admission before executor entry;
2. frontend `AfterDecode` may fail or mutate state before executor entry;
3. a fresh ordinary `Execute` would duplicate lifecycle/accounting unless a broad preparation split is exact;
4. a fallback decode-admission rejection after `BeginTurn` would change externally observable ordering.

### Pre-turn alternative

V1 instead treats any authority whose wire-safety decision cannot be made without post-A-leg state as a conservative blocker.

A concrete example is `RouteOverrideReader`: current code snapshots override state **after authoritative A-leg fetch** because the lookup is keyed by A-leg ID. In V1, a configured route-override authority therefore blocks the optimization unless a future read-only pre-turn contract is introduced.

For selector-local state such as weighted-first/affinity, wire compatibility is proven for the conservative superset of every candidate in the selector. Later session state may choose/order a subset, but cannot reveal an unproven backend.

### Result

All expected declines happen before `BeginTurn`:

```text
capture/proof → frontend state → extension/callback blockers → route superset → backend proof
  ├─ decline → Canonicalize + ordinary Execute
  └─ pass    → one-way wire commit → BeginTurn → normal lifecycle/attempts
```

After commit, an unexpected request-content dependency is an internal parity bug: abort/finalize once, never start a second execution.

This removes a large, high-risk repo-wide executor refactor from #503 V1 and is the main scope/complexity improvement from the review.

---

## 11. Backend Compatibility and Model Rewrite

Same protocol name is insufficient. Each backend must explicitly prove the exact frontend profile/operation/delivery/requirements/body mode before the wire commit point.

Core proves **every candidate in the conservative reachable selector superset**. It never drops an incompatible fallback/race branch to keep wire mode.

Model rewrite uses the scanner-recorded exact top-level token span and a streaming splice:

```text
prefix + json.Marshal(candidateNativeModel) + suffix
```

No regex/prefix scan and no second full-body allocation.

Every attempt/credential retry obtains a fresh replay reader. Existing core retry/no-post-output-failover ownership remains authoritative.

---

## 12. Duplicate/Normalization Policy

Raw forwarding preserves syntactic forms that canonical decode→encode may normalize. Profiles must therefore be conservative about:

- unknown fields discarded by canonical encoders;
- aliases/legacy forms;
- malformed tool/function histories that current adapters repair/skip;
- duplicate keys that canonical decode/re-encode collapses;
- numeric/string/control representations whose canonical semantics are not proven equivalent.

OpenResponses keeps its strict duplicate behavior. For OpenAI-style profiles, duplicate protocol-owned members are canonical-only unless exact behavior is separately certified.

Profile broadening requires differential corpus in the same change.

---

## 13. Spool Resource Semantics

The global spool byte budget bounds **optimization-owned replay storage**, not total process memory. Requirement #503 also says an otherwise valid request should not fail merely because the optimization budget is unavailable; therefore budget exhaustion falls back to the old canonical path.

This means operators must not misread `max_inflight_spool_bytes` as a global OOM defense. Load tests compare saturation to the disabled canonical baseline and metrics expose the fallback reason.

`Source.Close` should be idempotent/nonblocking with respect to readers; root close marks deletion pending and the last tracked reader performs final cleanup. This avoids deadlock if a reader is leaked and works with Windows open-file deletion constraints.

---

## 14. Security/Privacy Notes

Replay files can contain raw prompt/tool data. Secure temp naming/mode is necessary but not sufficient for every threat model. Documentation must cover:

- short lifetime;
- protected `spool_dir`/volume;
- no content/path in telemetry;
- cancellation/error cleanup;
- optional future encrypted spool only if deployment threat models justify its cost.

Client Authorization/hop-by-hop/stale encoding headers are never blindly forwarded. Backend credential selection and existing shared HTTP/TLS/proxy configuration remain authoritative.

---

## 15. Performance Validation

Required sizes: 32 KiB, 256 KiB, 1 MiB, 5 MiB, plus a test-only raised-limit 20 MiB body.

Measure:

- allocs/op and B/op;
- CPU;
- GC cycles/pause/heap under concurrency;
- temp-file I/O;
- capture/shared-preflight/protocol-proof/provider-open latency;
- replay/failover cost;
- malformed/late-field/giant-string cases;
- spool-budget saturation.

Publish a realistic eligibility matrix including extension planes, frontend traffic, route override, billing, and route strategy. A feature that compiles but always falls back under normal composition is not complete.

---

## 16. Recommended Rollout Order

1. infrastructure/characterization only, no backend advertises support;
2. replay source + shared scanner + decode-QoS parity;
3. typed-plane access classification;
4. frontend response-state bridge;
5. pre-turn core eligibility and full-Call dependency closure;
6. backend proof/model rewrite/replay integration;
7. OpenAI Responses lane;
8. OpenAI Chat lane;
9. OpenResponses explicit-no-store lane;
10. gzip follow-up;
11. default-off production rollout with evidence.

This order prioritizes correctness and isolates failures so coding agents do not face a simultaneous repo-wide breakage event.
