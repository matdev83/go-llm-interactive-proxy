# Research & Design Decisions

## Summary

- **Feature**: Large-Payload Streaming Fast Path (#503)
- **Initial discovery**: Go-LIP around `8684666809f687ab43dc1393dc1f726a0b0161f7`, plus Bifrost large-payload/replay ideas.
- **Post-PR cross-check baseline**: `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533.
- **Verdict**: the original pre-commit replay/spool direction was correct, but the first spec draft was not implementation-ready. Cross-checking the current frontend, secure-session, routing, metering/accounting, extension, and response paths exposed several contracts that would otherwise either change behavior or make the optimization effectively unreachable in standard deployments.

The final V1 design is a **two-phase, same-decode-permit assessment followed by a one-way wire commit**. All expected declines happen before `BeginTurn`; canonical fallback uses the already-held decode permit; after commit the execution lane contains no expected canonical fallback. Frontends with the existing full-body pre-preflight route-selector callback remain canonical before capture unless a future bounded contract preserves that authority exactly.

---

## 1. Actual LLM Ingress Path

### Relevant sources

- `internal/plugins/frontends/frontendpipe/pipe.go`
- `internal/plugins/frontends/reqbody/body.go`
- `internal/plugins/frontends/decodeqos/admission_http.go`
- `internal/plugins/frontends/jsonguard/*`
- `internal/core/jsonshape/*`
- target frontend decoders

### Finding

The large LLM request hot path is not primarily `internal/jsonbody`. Shared create frontends currently:

1. read/decompress the full body with `reqbody.ReadAll`;
2. derive the header selector and, when configured, run `Spec.ResolveRouteSelector(r, body, pm)` on the complete `[]byte` body;
3. run shared JSON preflight;
4. acquire byte-weighted decode admission;
5. under that permit, apply `RouteFromBodyModel` defaulting when the selector is still empty and run protocol `Spec.Decode`;
6. release the permit;
7. apply authoritative session headers, validate, run `AfterDecode`, validate again;
8. derive canonical/stable request identity and emit frontend traffic;
9. enter `Executor.Execute(*lipapi.Call)`.

`reqbody.ReadAll` applies the body cap to the **decoded gzip stream**, which is a security/compatibility contract.

### Pre-preflight full-body route authority

`ResolveRouteSelector` is not merely an implementation convenience. Its current contract receives the fully materialized body and its output can override the header-derived selector **before** shared JSON preflight. Moving it after streaming preflight changes observable ordering, skipping it changes routing authority, and invoking it from replay by rebuilding a full `[]byte` body defeats the intended large-body retention improvement.

The initial OpenAI Responses lane does not configure this callback; it uses `RouteFromBodyModel`, which can be reproduced by bounded semantic proof under decode admission. Therefore a conservative V1 rule—configured legacy `ResolveRouteSelector` means canonical before capture—preserves correctness without making the first target lane dead. A future bounded route-resolution interface can be considered separately if a real frontend needs it, but it must preserve current header/body precedence and callback ordering.

### Decision

#503 belongs at shared frontend capture/proof plus explicit core/backend execution seams. The canonical path remains the fallback oracle. The fast-path candidate gate must also preserve the existing pre-preflight route authority: legacy full-body resolver presence is an early canonical disposition unless an explicit bounded equivalent exists.

---

## 2. Why Immediate Client→Provider Piping Is Not V1

Required route/model/session/protocol metadata can appear late in valid JSON. Malformed/over-limit requests currently fail before provider execution, and Go-LIP has request authorities that must be resolved before upstream commitment.

Therefore V1 still receives the complete client request before provider body open. Its expected benefit is:

- bounded body retention rather than one large heap slice;
- no full canonical request graph for certified requests;
- no provider request re-marshal;
- lower GC pressure under concurrent multi-MiB workloads;
- replay/failover directly from immutable source.

This is primarily a heap/GC/redundant-work optimization, not an early-TTFT feature.

---

## 3. Bifrost Ideas Worth Borrowing—and Ideas Not Safe to Copy

### Useful

- threshold before expensive whole-object decode;
- replayable raw request body carried through orchestration;
- compact metadata skeleton rather than full request object;
- model-token splice rather than full-body rewrite;
- separate compression layer;
- explicit backend raw/wire support.

### Unsafe to copy directly

- “large mode” that merely skips parser/hooks;
- substring/prefix model replacement;
- protocol-name equality as compatibility proof;
- bypassing accounting/session/guardrail/traffic semantics for large requests.

Go-LIP has materially richer canonical normalization, B2BUA/session state, route authority, extension planes/hooks, metering/accounting, and response ownership.

---

## 4. Streaming Shared JSON Validation

`encoding/json.Decoder.Token` is not sufficient as the core large-string solution because decoded string tokens can still allocate proportional to payload size.

The shared scanner should incrementally validate lexical structure and current limits while retaining only bounded selected facts/raw spans. It must handle arbitrary field ordering and all buffer-boundary cases.

The current slice preflight remains the differential oracle. Fuzz/property tests are required before profile enablement.

---

## 5. Decode QoS Requires Two-Phase Assessment Under One Permit

### Current contract

After any configured pre-preflight full-body route resolver and shared preflight, `frontendpipe` performs roughly:

```text
decodeqos.TryAdmit(ctx, DecodeAdmission, decodedBodyBytes)
  → decodeqos.Guard(RouteFromBodyModel defaulting + Spec.Decode)
```

The permit protects expensive protocol decoding; it is intentionally **not** held while the client uploads.

### First draft problem #1: proof during upload

Running the protocol semantic verifier while capturing the body would move expensive protocol work outside the existing decode concurrency authority.

### First draft problem #2: release then later fallback

A subtler issue appears if the wire semantic verifier succeeds, releases the permit, and core later discovers an eligibility blocker. Canonical fallback then needs a normal protocol decode. Re-entering `TryAdmit` can produce a new 429/503 because capacity changed after the first grant. That means merely *considering* the optimization can change a valid request outcome.

### Final decision

Use one permit:

```text
legacy full-body route resolver? → canonical before capture
  ↓ otherwise
capture + shared preflight
  ↓
TryAdmit(exact decoded bytes)
  ↓ permit held
protocol semantic proof + RouteFromBodyModel parity + canonical identity digest
  ↓
side-effect-free AssessLargeBody
  ├─ decline → materialize + existing Spec.Decode under SAME permit
  │            → release at existing boundary → canonical path
  └─ accept  → release permit → one-way wire commit → ExecuteLargeBody
```

`AssessLargeBody` must be bounded and pure because it extends decode-permit hold time. It cannot perform DB/store/provider I/O, `BeginTurn`, billing reservations, client-body waits, or arbitrary unbounded plugin callbacks.

This is the central architecture correction from the review.

---

## 6. Typed Extension Planes Are Current Architecture, but Hooks Are Still Separate

PR #533 landed the typed plane consolidation. Current main has:

- `pkg/lipsdk/feature/plane_manifest.go`;
- typed `feature.Plane[T]` declarations;
- generated/frozen plane storage and `FrozenPlaneSet`.

The old spec fork (“use manifest if available, otherwise classify legacy fields”) is stale. Request-body access metadata should extend the canonical plane declarations/generator.

However, `RequestRuntimeSnapshot` still separately owns `*hooks.Bus`. Submit hooks receive `*lipapi.Call` and can reject/mutate it. They are not implicitly represented by `plane_manifest.go`.

### Decision

Build one frozen **wire-eligibility summary** for the generation:

- typed plane access comes from the canonical plane manifest/generator;
- hook-chain occupancy/access is classified separately from the frozen hook bus;
- standard non-plane authorities (traffic, secure recorder, metering/accounting, route override, Call-shaped callbacks) also contribute bounded facts.

This is not a second plane architecture; it is a complete eligibility view over the brownfield runtime.

Unknown/unclassified always fails closed.

---

## 7. Frontend Response State Means EventStream-Only Is Insufficient

Current frontends use the canonical Call and/or decoded Extra after execution.

### OpenAI Responses

Response ID/cancellation carrier can depend on authoritative A-leg/session identity; timestamps and encode options also derive from Call-based helpers. An optimized request cannot become uncancellable.

### OpenAI Chat

Completion ID/timestamp currently use deterministic Call-based helpers.

### OpenResponses

`AfterDecode` creates `createEncodeState`, which participates in response ID/options, continuation/store behavior, wrappers, and non-stream handling.

### Decision

Wire execution returns canonical events plus bounded `ResponseFacts`. Frontend-specific state stays in the frontend. Do **not** create a partial Call just to satisfy old function signatures.

---

## 8. New Secure Sessions Need a Sensitive Resume-Token Response Carrier

Current secure preparation calls `BeginTurn`, obtains authoritative session/A-leg state, and for a new session copies the raw resume token into `call.Session` so the frontend can emit it back to the client.

A wire execution without a canonical Call would otherwise lose this response carrier.

### Decision

Wire result includes a separately marked sensitive session-response carrier containing only what frontend session response handling requires, such as:

- authoritative session ID;
- A-leg ID;
- new-session raw resume token.

The resume token is never sent to the provider, used as a metric label, or logged.

End-to-end tests must prove: wire first turn → returned session/resume carrier → successful resumed second turn.

---

## 9. Standard Secure-Session Recorder Is Not a Permanent Blocker

`runtimebundle` composes the secure-session client-turn recorder in standard secure-session operation. The recorder currently builds accepted-input records from `lipapi.NormalizedItems`, but the persisted shape is primarily:

- role;
- ordinal;
- content-part kinds/shape;
- bounded non-content metadata.

It does not need prompt text merely to record the input shape.

### Decision

Profiles produce a bounded `ClientTurnShape` equivalent to canonical normalized item shape for their certified subset. Wire execution feeds this into a sibling/common recorder helper.

This is important for practical eligibility: treating the recorder as a full-Call blocker would make standard secure-session deployments fall back despite the recorder not needing large content.

Semantic-fact memory is explicitly bounded; pathological huge item/part cardinality falls back.

---

## 10. Metering Checkpoints Currently Retain Full Calls and Would Defeat the Optimization

Current metering `checkpoint.Snapshot` stores:

```go
Public metering.Checkpoint
Call   lipapi.Call
```

Frontend ingress and backend ingress capture clone/sanitize the Call. The public ingress quantities initially derived from the Call are only request count and exact `MaxOutputTokens`; input-token counting is deferred.

The full Call is retained for later recount/rerate/widening logic.

### Consequence

A wire path that simply calls the existing checkpoint capture helpers would reconstruct/retain the same large canonical object graph #503 is supposed to avoid.

### Decision

Add wire-native frontend/backend ingress checkpoint evidence:

- same public correlation/scope/perspective/frontend/backend/model fields;
- deterministic request/checkpoint/fact identities;
- request count and exact max-output bound;
- A-leg/B-leg/attempt/session facts;
- immutable source digest + exact rewrite/attempt digest for replay/widening evidence.

No hidden canonical Call is retained.

When token accounting is disabled, this path is sufficient without tokenization.

When accounting/preflight requires exact input tokens, eligibility requires an exact raw/source/profile `WireCounter` (or equivalent provider-specific raw count contract). **Body bytes are not tokens.** If only `CountCall` exists, assessment declines before wire commit.

Stock billing/exposure behavior must be characterized separately rather than hidden behind “custom callback” language.

---

## 11. Canonical Stable Identity Is an Economic Correctness Contract

Current stable Call identity clones the canonical Call, clears Call.ID, JSON-marshals the canonical representation, and hashes it. Derived values feed:

- stable Call ID/token/timestamp;
- trace/correlation;
- frontend deterministic IDs/timestamps;
- metering checkpoint/fact/source identities;
- billing/request identities and retry idempotency.

A hash of the raw request body will differ whenever canonical decode normalizes representation (field order, omitted fields, escapes, aliases, session precedence, etc.). If wire and canonical modes assign different request IDs to the same semantic request, toggling/fallback/config changes can create a second economic identity namespace.

### Decision

Each certified profile produces an **exact canonical semantic identity digest** equivalent to the current post-frontend-decode/pre-core stable Call hash, without retaining large prompt strings.

The profile can incrementally decode/normalize supported fields and stream the canonical Call representation into a hash writer. `diag` may be refactored to derive stable ID/token/Unix from an already-computed digest while preserving all existing canonical outputs.

Differential tests compare profile digest against the fully decoded canonical Call for huge strings, Unicode/escapes, tools/messages, optional fields, model/selector, and session-header precedence.

If exact digest parity cannot be proven for a shape, that shape is canonical-only.

A capture/source digest is still useful for replay integrity, but it is a **different** identity and cannot replace canonical stable identity.

---

## 12. OpenResponses Defaults `store=true`

OpenResponses create currently initializes `store := true` and only changes it when the request explicitly provides a value. `AfterDecode` also prepares response/continuation state.

### Consequence

“No `previous_response_id`” is not enough to make an OpenResponses create request stateless.

### Decision

Move OpenResponses behind OpenAI Responses/Chat. Initial OpenResponses wire certification requires:

- HTTP create;
- **explicit `store:false`**;
- absent `previous_response_id`;
- no compaction;
- no WebSocket;
- bounded response state;
- proof that no normal `AfterDecode` error/side effect is shifted past wire commit.

`store:true`/continuation is a later certification only.

---

## 13. Deep Full-Call Dependencies Must Be Closed Before Wire Commit

Observed post-identity/full-Call dependencies include:

- route selector/request-size/failover requirements;
- capability derivation;
- secure-session recorder;
- frontend/backend metering checkpoints;
- token accounting/preflight;
- billing identity/policy/pricing/max-output/exposure;
- `recvTurnFacts` baselines;
- continuation support;
- interleaved-thinking recorder construction;
- terminal/session usage;
- traffic snapshots;
- response/debug helpers;
- stable identity helpers.

### Decision

Maintain a checked inventory/ratchet. Every dependency becomes one of:

1. exact bounded wire fact/view;
2. immutable source/digest/rewrite contract;
3. response-only behavior;
4. assessment blocker.

Standard always-composed secure recorder and metering receive explicit wire-native paths rather than being silently treated as permanent blockers.

No wire code receives a fake/partial Call.

---

## 14. Route Override: Presence Is Not a Viable Blocker

### Brownfield finding

Standard memory and Bun continuity stores implement the optional route-override store. `runtimebundle` installs `RouteOverrideReader` when the persistence store exposes that capability.

The current route override is intentionally snapshotted **after authoritative A-leg fetch**, because the lookup key is A-leg ID.

The previous intermediate spec revision proposed `RouteOverrideReader != nil => canonical`. That is safe but practically wrong: normal continuity/secure-session deployments would almost always expose the reader, making the fast path effectively dead.

### Final decision: late-route compatibility envelope

Keep the correct post-A-leg override timing, but pre-certify a generation-wide domain before wire commit.

For route override, derive that domain from the same generation validator used to accept/update selectors:

- known backend IDs;
- alias semantics;
- current execution-composition policy;
- model-selection domain accepted by that validator.

Ask every potentially selected backend for wire compatibility over that domain. If the domain cannot be closed, assessment declines before `BeginTurn`.

Because route-override model text may not be a finite model catalog, domain proof can require a backend to certify `AnyAcceptedModel`/equivalent same-wire semantics for the profile. A backend whose wire compatibility is model-specific cannot safely satisfy an unbounded override-model domain.

After wire commit, the existing route-override snapshot runs normally. Whatever selector it returns is guaranteed to remain inside the assessed envelope.

### Trade-off

A heterogeneous generation containing canonical-only/cross-protocol backends can make the route-override envelope too broad and cause conservative fallback even when the initial selector is compatible. This is preferable to a late fallback. Eligibility tests must quantify homogeneous vs heterogeneous deployments.

A future read-only/transactional route-authority preview could narrow the domain, but it is not required for V1.

---

## 15. Route Hints and Other Late Selector Authorities

Unlike the route-override store, current route-hint/provider extensions can receive a full Call. There is no safe assumption that they can be represented by the initial selector alone.

### Decision

Such authorities are canonical-required unless they expose an explicit bounded route-domain wire contract. This rule generalizes to future late selector mutation.

The optimizer never prunes/reorders candidates or disables routing semantics to remain eligible.

---

## 16. Backend Compatibility Needs Exact and Domain Proof

Same protocol/backend label is insufficient.

Backends expose pure provider-neutral wire proof for:

- exact profile/operation/delivery/protocol requirements;
- the immutable profile-derived **body mode** as an input to exact and domain proof;
- the immutable profile-derived **rewrite semantics** as an input to exact and domain proof;
- exact candidate model or late-route model domain when needed (finite set or universal accepted-model domain);
- parallel/race replay capability.

The rewrite contract describes what transformations are permitted (initially the scanner-proven top-level model-token splice with all other body bytes unchanged). `NeedsModelRewrite` is a compatibility **output** for a candidate; it cannot substitute for the input contract. If a backend needs a rewrite not allowed by the profile contract, or the exact model span/semantics are unavailable, compatibility is false and assessment declines.

Support resolution performs no provider network I/O. Nil/unknown/partial support is canonical-only.

`OpenWire` runs only after the one-way commit and reuses existing URL/credentials/client/parser/error machinery.

---

## 17. Model Rewrite

The scanner/profile records the exact raw span of the selected top-level model JSON token. Per candidate:

```text
prefix + json.Marshal(nativeModel) + suffix
```

A splice reader streams this without a second whole-body allocation. Rewrite length uses checked `int64` arithmetic. Duplicate/ambiguous/repaired model forms are canonical-only.

Each retry/parallel attempt obtains an independent offset-zero source reader.

---

## 18. Spool Resource Semantics

The global spool budget bounds **optimization-owned replay storage**, not total process memory. #503 also requires optimization-resource exhaustion not to become a new client error, so budget exhaustion falls back to the old canonical path—which may allocate the full body.

Operators must not interpret the spool budget as global OOM admission. Metrics/load tests should make this explicit.

`Source.Close` is idempotent/nonblocking with respect to active readers. Root close marks deletion pending; last tracked reader performs final cleanup. This avoids deadlock on leaked readers and accommodates Windows open-file deletion constraints.

---

## 19. Security and Privacy

Replay files may contain raw prompt/tool data and are treated as short-lived secrets:

- secure unpredictable temp files;
- protected spool directory/volume guidance;
- no body/path in telemetry;
- cancellation/error cleanup;
- explicit operator documentation.

Sensitive secure-session resume tokens are separate from request/backend facts and never logged/metric-labelled.

Client Authorization/hop-by-hop/stale encoding headers are never blindly forwarded. Existing backend credential selection remains authoritative. A configured pre-preflight route resolver is also an authority and is not skipped or reordered to manufacture fast-path eligibility.

---

## 20. Final Rollout Order

1. rebase + canonical ingress/route-resolution characterization + full dependency/identity inventory;
2. replay source + shared scanner;
3. exact canonical semantic identity digest;
4. typed-plane/hook/non-plane frozen eligibility summary;
5. frontend candidate path with legacy full-body route-resolver gate and **single decode permit**;
6. backend exact/domain proof with explicit body-mode/rewrite inputs + model rewrite;
7. secure-session wire views, recorder shape, sensitive response carrier;
8. wire-native metering/economic checkpoints and counting disposition;
9. side-effect-free `AssessLargeBody` + late-route domain proof;
10. remaining post-commit Call dependency closure;
11. `ExecuteLargeBody` integrated into existing lifecycle/attempt owner;
12. frontend response bridge;
13. OpenAI Responses lane;
14. OpenAI Chat lane;
15. OpenResponses explicit-no-store lane;
16. gzip follow-up;
17. performance/eligibility/observability evidence;
18. default-off rollout.

This order intentionally resolves default-runtime parity before raw backend enablement, minimizing the chance that coding agents produce a superficially fast path which either silently bypasses authorities or reconstructs the full Call later.
