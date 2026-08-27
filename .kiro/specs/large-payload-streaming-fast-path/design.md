# Design Document

## Overview

This feature adds an optional proof-gated large-request path for already-supported JSON create operations. Eligible requests are captured into a replayable source with bounded RAM, validated to EOF, semantically proven under the existing byte-weighted decode-admission authority, planned against immutable core/backend facts, and only then committed to a same-wire backend path without constructing the full canonical request graph or re-marshalling the request body.

The design deliberately does **not** stream client bytes to a provider while validation is in progress. V1 still receives and validates the full request before provider body commitment. The optimization target is request heap/GC/copy pressure and redundant decode→canonical-object→encode work.

This document is revalidated against `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533.

## Brownfield Findings That Drive the Final Design

1. `frontendpipe -> reqbody.ReadAll -> shared preflight -> decodeqos -> protocol Decode` is the actual LLM ingress hot path; `internal/jsonbody` is not.
2. Typed `feature.Plane[T]` declarations/FrozenPlaneSet are now production architecture. Request-body access classification must extend that system only.
3. Protocol semantic proof must run under the current decode-admission authority; it cannot happen uncontrolled during upload.
4. A later core fallback after releasing decode admission is also unsafe: it would either decode canonically outside QoS or reacquire a second permit and create a new saturation race.
5. Therefore canonical fallback must remain frontend-owned and the final core eligibility decision must happen before the original decode permit is released.
6. Current secure-session preparation requires a real `*lipapi.Call`; simply saying “wire execution begins a normal turn” is insufficient. Wire mode needs exact bounded session/identity facts plus a shared fact-only BeginTurn/A-leg primitive.
7. Current frontends use Call/Decoded state after execution for response IDs, session response headers, cancellation carriers, timestamps and wrappers. EventStream-only is insufficient.
8. OpenResponses create defaults `store=true`, so it is not the simplest first wire lane.
9. A recoverable spool failure after consuming network bytes requires an explicit continuation reader; “fallback” alone is not an implementation.
10. All first-wave replay/planning consumers are internal. V1 does not need to widen public `pkg/lipsdk` surface.

## Goals

- materially reduce heap/GC pressure for certified multi-MiB requests;
- preserve request-size/shared-JSON safety, decode QoS, frontend validation, session/B2BUA, routing/recovery, billing/accounting, guardrails/traffic and response semantics;
- preserve lossless canonical fallback before logical-turn commitment;
- keep frontend protocol knowledge at frontend edges and provider HTTP knowledge at backend edges;
- keep core routing/recovery/session ownership;
- fail closed on unknown planes/callbacks/session modes/route state;
- keep ordinary canonical execution unchanged and easy to compare.

## Non-Goals

- native passthrough (#490);
- client→provider streaming before complete proof;
- cross-protocol raw forwarding;
- broad conversion of hooks/plugins to streaming request APIs;
- changing request-size defaults;
- adding Brotli/zstd/deflate;
- weakening traffic/guard/billing/session authorities;
- fabricating partial Calls;
- expected canonical fallback after `BeginTurn`;
- stabilizing a public low-level replay API for external plugins in V1.

---

## 1. Canonical Flow: Behavioral Oracle

Simplified current create flow:

```text
frontend handler
  ├─ method/path/auth/content-type
  └─ frontendpipe.ServeHTTP
       ├─ reqbody.ReadAll (gzip + decoded MaxBytesReader)
       ├─ route-selector inputs
       ├─ jsonguard.PreflightWithContext
       ├─ decodeqos.TryAdmit(decoded body bytes)
       │    └─ decodeqos.Guard(Spec.Decode)
       ├─ authoritative session-header application
       ├─ Call.Validate
       ├─ Spec.AfterDecode when configured
       ├─ Call.Validate
       ├─ frontend TrafficPorts.Emit(full []byte)
       ├─ stream/debug request facts
       ├─ Executor.Execute(*lipapi.Call)
       │    ├─ secure identity/session/A-leg
       │    ├─ request authorities/content stages
       │    ├─ billing/routing/B-legs/recovery
       │    └─ canonical EventStream
       ├─ Spec.WrapStream
       ├─ BuildEncodeOpts(decoded Call/Extra)
       └─ response writer + session carriers
```

Before production changes, characterization tests freeze this order, including malformed/trailing JSON, exact body/gzip limits, decode-admission decisions/release, session/A-leg lifecycle, route/failover/race behavior, frontend response IDs/cancellation/session headers, and `AfterDecode` state.

---

## 2. Final V1 State Machine

The decisive architectural change is a two-phase core seam: **plan before decode-admission release, execute only an accepted plan**.

```text
HTTP body
  ↓
cheap canonical gates
  ├─ disabled / unsupported executor / frontend full-body traffic
  ├─ known identity length below threshold
  └─ gzip wave 1
        → ordinary canonical path
  ↓
replay capture + shared streaming JSON preflight to EOF
  ├─ over-limit/invalid → existing frontend error
  ├─ mid-capture optimization decline → lossless canonical continuation
  └─ complete immutable Source
  ↓
decodeqos.TryAdmit(exact decoded bytes)
  ↓ permit held
frontend protocol semantic proof from Source.Open()
  ├─ profile decline → ordinary Spec.Decode under SAME permit
  │                    → release → ordinary post-decode/Execute
  ↓ proven metadata
core PlanLargeBody(metadata)  [bounded, side-effect-free]
  ├─ decline → ordinary Spec.Decode under SAME permit
  │            → release → ordinary post-decode/Execute
  └─ accepted plan
        ↓
     release decode permit exactly once
        ↓
     ExecuteLargeBody(plan, Source)
        ↓ ONE-WAY WIRE COMMIT BEFORE BeginTurn
     fact-only secure-session/A-leg preparation
        ↓
     existing route/attempt/recovery owner using wire facts
        ↓
     backend OpenWire + canonical EventStream
        ↓
     bounded ResponseFacts + frontend-owned wire state
        ↓
     existing frontend response protocol
```

There is no `Canonicalize` callback inside core and no expected `PathCanonicalFallback` returned from `ExecuteLargeBody`. A plan decline never enters wire execution at all.

### Why planning happens while the decode grant is held

After protocol proof, core still must determine typed-plane blockers, Call-shaped callback blockers, route candidate superset and backend wire compatibility. Releasing the permit first would make a later canonical fallback unsafe. Acquiring a second permit is also wrong because optimization consideration could introduce a new overload rejection.

Therefore final planning is completed under the original grant, but it has a strict contract:

- no provider/network I/O;
- no DB/store/session/A-leg reads;
- no waiting for client bytes;
- no spill filesystem work;
- no arbitrary unbounded plugin callback;
- bounded immutable configuration/selector/backend-declaration work only.

Planning hold duration is measured; if a fact cannot be determined under this bound, the corresponding configuration is made canonical-only earlier.

---

## 3. Configuration and Generation Pinning

Illustrative config:

```yaml
server:
  max_request_body_bytes: 8388608
  large_payload_fast_path:
    enabled: false
    threshold_bytes: 1048576
    memory_spool_bytes: 65536
    max_inflight_spool_bytes: 1073741824
    spool_dir: ""
```

Rules:

- default off;
- positive threshold/memory/budget and `memory_spool_bytes <= threshold_bytes`;
- explicit spool directory validated on candidate generation/reload;
- invalid reload keeps the last-good generation;
- request-size defaults are unchanged;
- spool budget is optimization storage budget, not global process-memory admission.

Fast-path ingress policy, plane access summary, planner/backend declarations and execution must come from the same immutable generation. Do not cache a stale reloadable fast-path policy inside a long-lived `frontendpipe` `sync.Once` spec unless the handler itself is rebuilt per generation. The implementation may expose a generation-pinned immutable ingress policy from the optional internal executor seam or pass a generation-owned policy when building the handler; either approach must have reload tests.

---

## 4. Internal Provider-Neutral Large-Body Contract

V1 should use an internal package, conceptually:

```text
internal/core/largebody/
  policy.go
  source.go
  metadata.go
  plan.go
  response.go
  rewrite.go
```

The exact package name may differ, but it is **not** `pkg/lipsdk/requestbody` in V1 unless a concrete external consumer is identified.

The existing public `lipsdk.ExecutorView` remains unchanged. `frontendpipe` may type-assert the dynamic executor to an internal optional interface; external/manual executors cannot and need not implement it and remain canonical-only.

Conceptual types:

```go
type Span struct {
    Start int64
    End   int64
}

type Source interface {
    Size() int64
    Open() (io.ReadCloser, error) // independent offset-zero reader
    Close() error                 // idempotent root close
}

type SessionIngressFacts struct {
    AuthoritativeSessionID string
    ResumeToken            string // sensitive bearer; never telemetry/backend
    ClientSessionID        string
}

type Metadata struct {
    GenerationID         uint64
    ProfileID            ProfileID
    FrontendID           string
    Operation            lipapi.Operation
    DeliveryMode         lipapi.DeliveryMode
    RouteSelector        string
    ClientModel          string
    ProtocolRequirements lipapi.ProtocolRequirements
    ModelSpan            Span
    BodyBytes            int64
    MaxOutputTokens      *int64
    Session              SessionIngressFacts
    ClientUserAgent      string // only if dependency inventory proves needed; bounded
}

type PlanDecision struct {
    Allowed bool
    Reason  FallbackReason
    Plan    Plan // internal immutable accepted plan; absent on decline
}

type ResponseSessionFacts struct {
    AuthoritativeSessionID string
    ALegID                 string
    ResumeToken            string // only when canonical response behavior needs it
}

type ResponseFacts struct {
    CallID       string
    TraceID      string
    Operation    lipapi.Operation
    DeliveryMode lipapi.DeliveryMode
    Session      ResponseSessionFacts
}

type ExecutionResult struct {
    Stream   lipapi.EventStream
    Response ResponseFacts
}
```

`Plan` is an internal immutable value owned by the core package family. It may contain generation-pinned selector/candidate/backend support facts but no request content or provider SDK objects.

Optional executor seam, conceptually:

```go
type Executor interface {
    LargeBodyPolicy() PolicySnapshot
    PlanLargeBody(context.Context, Metadata) (PlanDecision, error)
    ExecuteLargeBody(context.Context, Plan, Source) (ExecutionResult, error)
}
```

Exact method names are implementation detail. Critical properties are separate plan/execute phases, a trusted accepted-plan value, and no core-owned canonical fallback callback.

---

## 5. Replay Capture and Lossless Mid-Capture Canonical Continuation

### Completed source

```text
client identity body
  ├─ fixed reusable copy buffer
  ├─ <= memory_spool_bytes → bounded memory
  └─ spill → private CreateTemp file
```

Invariants:

- immutable after EOF;
- independent `Open()` cursors;
- concurrent readers supported for race routes;
- root Close is idempotent/nonblocking with respect to active readers;
- final file deletion happens after root close + last reader close;
- no per-chunk or cleanup goroutine.

### Global reservation

Known identity length may reserve logical bytes up front within the effective request cap. Unknown/chunked capture reserves incrementally. Reservation is released exactly once.

### The no-rewind failure trap

Reservation exhaustion or file create/write failure can occur after bytes were consumed from the socket. Capture must not simply return “canonical fallback” with a consumed body.

Write discipline:

1. retain the current input chunk until the destination write reports success;
2. on a short write, record only the written prefix as durable;
3. keep the unwritten suffix available;
4. switch to a canonical continuation reader:

```text
retained memory/file prefix
+ current unwritten suffix
+ unread client body
```

5. materialize through the existing identity-body request ceiling and continue the ordinary frontend path.

This branch may allocate the canonical full `[]byte`; optimization has already been abandoned. It must not reread/restart the network request.

Fault tests cover reservation failure between chunks, file-create failure, partial file write, short write, cancellation, exact limit and removal failures.

### Confidentiality

- unpredictable non-user-derived names;
- owner-only permissions where supported;
- no body/path/session bearer in telemetry;
- docs warn spool plaintext may contain prompts/tool data.

---

## 6. Shared Streaming JSON Preflight

One provider-neutral scanner in the shared JSON-shape layer validates:

- UTF-8 and escapes/surrogate pairs across buffers;
- number grammar;
- root/delimiter/trailing/incomplete behavior;
- shared byte/token/depth/member/array/key/string/number limits;
- cancellation checkpoints.

Ordinary prompt scalars are not retained. Observer primitives expose bounded selected values and exact raw offsets without embedding protocol field names in the scanner.

Existing slice preflight remains the differential oracle.

---

## 7. Frontend Wire Profile and Session Extraction

A profile proves protocol/frontend facts only. It does not select backends or perform network I/O.

The profile:

- validates the certified protocol subset from a replay reader;
- derives exact protocol requirements;
- records model/stream/output controls and exact model span;
- detects canonical normalization/repair/duplicate/unknown-field triggers;
- validates all frontend pre-executor behavior required for the subset;
- produces bounded frontend-owned wire response state;
- lifts trusted session HTTP carriers using the same `HTTPHeaders` names/precedence as `sessionwire.ApplyAuthoritativeHeadersNamed`.

Raw `http.Header` never crosses into core. Initial OpenAI profiles treat body-carried proxy/session metadata as canonical-only unless explicitly parity-certified.

Resume tokens are sensitive bearer authority: never log, metric-label, persist raw in spool metadata, or forward to providers.

---

## 8. Decode Admission and Final Wire Planning

After shared preflight and complete capture:

1. call current `decodeqos.TryAdmit` with exact decoded bytes;
2. while the grant is held, run profile semantic proof from `Source.Open()`;
3. if profile declines, materialize and run current `Spec.Decode` under the same grant;
4. if profile succeeds, call bounded side-effect-free `PlanLargeBody` while the same grant remains held;
5. if plan declines, materialize and run current `Spec.Decode` under the same grant;
6. release exactly once;
7. run current post-decode/`AfterDecode` on canonical fallback, or call `ExecuteLargeBody` with accepted plan on the wire branch.

No second `TryAdmit` is allowed merely because the fast path was considered.

The planning duration is a new bounded extension to grant occupancy and therefore receives explicit latency/capacity benchmarks and saturation tests.

---

## 9. Typed Extension-Plane Access Summary

Extend canonical `feature.Plane[T]` declarations/generator with explicit request-body access metadata:

```go
type RequestBodyAccess uint8
const (
    RequestBodyAccessUnclassified RequestBodyAccess = iota
    RequestBodyAccessCanonicalRequired
    RequestBodyAccessMetadataOnly
    RequestBodyAccessResponseOnly
)
```

Rules:

- every production plane explicitly classified;
- zero/unclassified fails manifest/codegen/arch validation;
- runtime unknown fails closed;
- generated frozen storage computes a bounded access summary without request-path reflection/map walks;
- occupied content-reading/mutating planes block unless a separate typed wire contract exists;
- response-only planes remain available because provider output is still canonical events.

No legacy classification fork is implemented.

---

## 10. Side-Effect-Free Core Planning

`PlanLargeBody` may inspect only generation-pinned or otherwise pure inputs:

1. fast-path generation/policy match;
2. typed-plane access summary;
3. core traffic/raw-capture/redaction blockers;
4. presence of Call-only billing/policy/token/context callbacks;
5. post-A-leg route-override authority presence (V1 blocker unless pre-turn contract exists);
6. session mode such as detached mode (canonical-only in V1 unless separately implemented);
7. selector compilation with current aliases/defaults;
8. execution-composition validation;
9. native-model binding from immutable resolver data;
10. conservative selector candidate superset;
11. backend wire support for every candidate;
12. parallel-reader/rewrite/body-mode support.

Planning must not:

- call BeginTurn;
- fetch A-leg/session/DB state;
- open provider connections;
- mutate affinity/routing/session state;
- reserve billing exposure;
- execute arbitrary request-content plugins.

A plan decline has a bounded reason. An accepted plan freezes/fingerprints generation and compatibility facts so execution does not re-discover expected blockers after the commit point.

---

## 11. Fact-Only Secure-Session/A-Leg Preparation

Current `prepareIdentity`/`prepareSubmitAndALegSecure` cannot be reused by wire mode because they accept a real Call and later interleave content-dependent stages.

After plan acceptance, extract the smallest common fact-only primitive used by canonical and wire paths. Conceptually:

```go
type TurnStartFacts struct {
    TraceID               string
    Principal             execview.PrincipalView
    Scope                 scope.PrincipalScopeView
    AuthoritativeSessionID string
    ResumeToken           string
    ClientSessionID       string
    WorkspaceInputs       ... // bounded existing view/config facts only
}

type TurnIdentity struct {
    TraceID      string
    Principal    execview.PrincipalView
    Scope        scope.PrincipalScopeView
    Workspace    workspace.WorkspaceView
    Session      session.SessionView
    SecureTurn   execctx.SecureSessionTurn
    ALeg         b2bua.ALegRecord
    RouteAuth    routeAuthoritySnapshot
}
```

The shared primitive owns the currently equivalent sequence for:

- principal/scope resolution;
- session-open metadata stage if classified wire-safe;
- workspace resolution if classified wire-safe;
- `SecureSession.BeginTurn`;
- A-leg fetch;
- required route-authority snapshot/barrier;
- authoritative identity outputs.

Canonical execution then continues its existing Call/content stages. Wire execution continues only through stages already proven wire-safe/disabled by the plan.

This extraction must be preceded by characterization tests and must not change ordinary canonical ordering/errors/lifecycle. It is **not** a mechanism for late fallback; no canonical re-entry exists after BeginTurn.

Detached mode is canonical-only in V1 unless this dependency is separately designed and parity-tested.

---

## 12. Close Every Call Dependency From Commit Onward

Inventory starts at the accepted-plan/commit boundary, not merely after identity.

Known dependencies include:

- secure/detached preparation inputs;
- `preparedRequest.call` and `identity.ingressCall`;
- route selector/request-size/failover requirements;
- billing account/policy/pricing/max-output/token estimation;
- `recvTurnFacts` baselines;
- continuation-support calculation;
- interleaved-thinking recorder construction;
- terminal usage/session fields;
- any Call-shaped plugin/callback.

Each becomes:

1. pre-plan blocker;
2. exact bounded fact with a helper shared by canonical and wire branches; or
3. response-only frontend behavior.

No wire/post-commit function may dereference a canonical Call after closure except explicitly canonical-only code. An AST/architecture ratchet enforces this.

### Stock billing

Evaluate the actual standard composition:

- principal/account identity;
- pricing/charge policy;
- max-output exposure input;
- request-token/context estimation;
- terminal usage settlement.

If exact bounded facts suffice, introduce one typed billing input and make canonical billing derive the same value from its real Call. Do not use body-byte token approximations. Arbitrary custom Call callbacks remain canonical-only.

---

## 13. Routing and Candidate Superset

Planning compiles the configured selector using existing aliases/defaults and establishes a conservative superset of every candidate that later weighted-first/affinity/interleaved/recovery state may select.

After BeginTurn, dynamic session routing may reorder/select **within** this already-proven set. It may not reveal a new candidate. If a safe finite superset cannot be established pre-turn, planning declines.

Common routing helpers should accept explicit facts where semantics are content-independent. Canonical path should reuse those helpers where practical to avoid drift.

---

## 14. Backend Wire Contract

Internal backends gain additive optional functions, conceptually:

```go
type WireRequestFacts struct {
    ProfileID            largebody.ProfileID
    Operation            lipapi.Operation
    DeliveryMode         lipapi.DeliveryMode
    ProtocolRequirements lipapi.ProtocolRequirements
    ClientModel          string
    CandidateModel       string
    BodyBytes            int64
}

type WireRequestSupport struct {
    Compatible           bool
    NativeModel          string
    NeedsModelRewrite    bool
    SupportsParallelOpen bool
    Reason               FallbackReason
}

type WireAttempt struct {
    Facts   WireRequestFacts
    Source  largebody.Source
    Rewrite *largebody.RewritePlan
}
```

`ResolveWireRequest` is pure/config-derived and safe during planning; no provider network I/O. `OpenWire` runs only after plan acceptance and reuses existing endpoint/credential/client/parser/error-classification behavior. Nil support means canonical-only. Core never switches on provider names.

External backend plugin ABI is unchanged in V1.

---

## 15. Token-Aware Model Rewrite

The scanner records the exact raw span of the selected top-level model token. For each candidate:

```text
source[0:model.start]
+ json.Marshal(nativeModel)
+ source[model.end:size]
```

The splice reader streams those segments without a second full body. Checked `int64` arithmetic determines exact rewritten length. Duplicate/ambiguous/repaired model forms are canonical-only.

Each provider or credential retry receives a fresh source reader and a fresh rewrite reader.

---

## 16. Existing Attempt/Recovery Ownership

Wire opening integrates into the existing B-leg/attempt/recovery owner and preserves:

- B-leg allocation/cleanup;
- attempt budget/order;
- affinity/weighted-first/interleaved state;
- credential/provider failure classification;
- pre-output-only retry/failover;
- response parsing to canonical events;
- terminal/accounting ownership.

Parallel attempts use independent replay cursors. No goroutine per body chunk is introduced.

Unexpected post-commit need for canonical content is an invariant error: abort/finalize the one turn, no second ordinary Execute.

---

## 17. Frontend Response-State and Session-Carrier Bridge

`ExecutionResult` returns canonical events plus bounded authoritative response facts. Frontend-specific wire state stays in `frontendpipe`/the frontend.

Each target frontend must inventory:

- `BuildEncodeOpts`;
- `WrapStream`;
- stream/non-stream writers;
- `sessionwire.WriteResponseCarriers`;
- response IDs/timestamps/models;
- cancellation endpoints;
- streamdebug/log helpers;
- `AfterDecode`/Extra state.

### OpenAI Responses

Preserve response/cancellation carrier semantics using authoritative A-leg/session facts. Response session headers must match canonical semantics. Any sensitive resume authority needed for response headers is transported only in sensitive response facts and never telemetry.

### OpenAI Chat

Resolve completion ID/timestamp/model/session response headers without a fake Call. Protocol-opaque ID differences may be normalized only with explicit conformance justification.

### OpenResponses

Initial no-store wire state covers only response/options/wrappers that do not require continuation reservation/recorder state. `store:true` remains later scope.

Existing canonical writers remain source-compatible; implementation may add wire-specific bounded encode context/callbacks rather than widening every writer immediately.

---

## 18. Protocol Certification Order

### Lane 1 — OpenAI Responses → OpenAI-compatible Responses

Initial canonical-only triggers:

- body-carried proxy/session metadata;
- legacy/repair aliases;
- malformed tool/function histories canonical decode repairs/skips;
- duplicate protocol-owned members;
- unknown fields canonical backend encoding drops;
- unresolved response/session-carrier dependency.

Header-derived session authority may be supported through exact normalized facts.

### Lane 2 — OpenAI Chat Completions → OpenAI-compatible Chat

Add only after Lane 1 conformance. Preserve role/message/tool/function/reasoning normalization and response envelope/session carriers.

### Lane 3 — OpenResponses HTTP create → OpenResponses-compatible

Initial subset:

- HTTP create only;
- **explicit `store:false`**;
- no `previous_response_id`;
- no compaction;
- no WebSocket;
- bounded no-store response state;
- no unresolved `AfterDecode` failure/side effect.

Missing `store` is canonical because current default is true. `store:true`/continuation is a later certification.

Anthropic/Gemini/etc. remain canonical until separately certified.

---

## 19. Compression Follow-Up

Wave 1: gzip selects canonical before fast-path capture.

A later wave may stream-decompress into the same decoded replay source while reproducing current decompressed-byte limits and errors. Compressed Content-Length is never used for decoded threshold/reservation. Outbound request sends identity JSON and omits stale encoding headers.

No Brotli/zstd/deflate expansion under #503.

---

## 20. Failure Matrix

| Failure/decline | Decode grant | BeginTurn? | Provider body? | Outcome |
|---|---|---:|---:|---|
| body too large/shared JSON invalid | none | no | no | existing frontend mapping |
| mid-capture spool budget/I/O decline | none | no | no | stitch retained+current+remainder → canonical path |
| decode admission reject | rejected | no | no | existing decodeqos mapping |
| protocol profile decline | held | no | no | canonical Spec.Decode under same grant |
| core plan decline | held | no | no | canonical Spec.Decode under same grant |
| accepted plan | release once | no yet | no | enter ExecuteLargeBody |
| BeginTurn/session denial | released | yes attempted | no | existing session denial lifecycle |
| unexpected post-commit canonical dependency | released | yes | no/maybe | abort/finalize once; invariant error |
| provider failure before visible output | released | yes | maybe | existing retry/failover |
| failure after visible output | released | yes | yes | no new failover |
| client cancel | phase-dependent | phase-dependent | phase-dependent | close readers/source + existing lifecycle |

---

## 21. Observability and Performance

Bounded fallback reasons include disabled, below-threshold, gzip, frontend-traffic, spool-budget, spill-I/O, decode-admission, profile-decline, plane-blocker, session-mode, route-override-authority, Call-dependency, route-superset, backend-incompatible, rewrite-unsafe.

Metrics/traces record only bounded non-content facts: size buckets, memory/file spool, active logical reservation, replay/rewrite count, capture/preflight/profile-proof/plan/provider-open latency and plan reason. Never log model/backend/user/session IDs in metric labels; never log body/spool path/resume token.

Benchmarks cover 32 KiB, 256 KiB, 1 MiB, 5 MiB and test-only 20 MiB plus giant strings, late model, malformed data, retries, races, mid-capture fallback, slow uploads and spool saturation.

Measure:

- allocs/op and B/op;
- CPU;
- GC/heap under concurrency;
- temp-file I/O;
- decode-admission occupancy/capacity;
- capture/preflight/profile-proof/plan/provider-open latency;
- realistic eligible vs fallback mixes.

The feature is incomplete if normal production-like composition always falls back.

---

## 22. Architecture Ratchets

Tests/codegen fail if:

- a production extension plane is unclassified;
- a second manual plane-access mirror appears;
- provider names/types leak into core large-body contracts;
- a fake/minimal Call is used on wire paths;
- canonical Decode occurs outside the original admission grant due to optimization fallback;
- `PlanLargeBody` performs BeginTurn/store/provider/network side effects;
- a post-commit wire function reaches a non-allowlisted Call dependency;
- session/resume authority is logged or forwarded to backend;
- route eligibility prunes/reorders an incompatible candidate;
- expected canonical fallback is introduced after the wire commit;
- V1 low-level replay/planner types are promoted to public SDK without explicit review.

---

## 23. Implementation Gate

No production profile advertises wire support until all are green:

1. canonical ingress/decode-QoS/session/lifecycle/response characterization;
2. replay source plus mid-capture continuation fault tests;
3. shared scanner differential/fuzz suite;
4. typed-plane access classification ratchet;
5. target frontend protocol proof under decode admission;
6. bounded side-effect-free planner + single-grant fallback tests;
7. fact-only secure-session/A-leg primitive characterization;
8. complete commit-onward Call-dependency closure including stock billing;
9. route-superset/backend support/model rewrite tests;
10. frontend response/cancellation/session-carrier bridge;
11. first-lane canonical-vs-wire conformance including retries/cancel/session resume;
12. allocation/load/decode-admission/eligibility evidence.

First release remains default-off and explicit opt-in.
