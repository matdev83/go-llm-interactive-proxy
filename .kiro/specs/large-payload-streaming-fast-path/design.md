# Design Document

## Overview

This feature adds an **optional, proof-gated large-request execution path** for already-supported JSON create operations. Eligible requests are consumed into a bounded-memory replayable source, validated to EOF, admitted through the same decode-QoS authority as canonical protocol decode, and then sent to an explicitly compatible backend without constructing the full canonical request object graph or re-encoding the request body. The existing canonical pipeline remains the oracle and fallback.

“Streaming fast path” deliberately does **not** mean forwarding client bytes to a provider while validation is still in progress. Required route metadata can legally appear late in JSON, malformed/over-limit requests currently fail before provider execution, and #503 requires all relevant authorities to be resolved before any provider body byte is committed. V1 is therefore a **pre-commit replay/spool + low-allocation proof + replay** design.

This document was revalidated after PR #533 landed. The brownfield baseline used for the correction is `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72`.

## Brownfield Findings That Change the Original Draft

The original architecture was directionally correct but four assumptions were not strong enough for implementation:

1. **Typed extension planes are no longer optional/future architecture.** `pkg/lipsdk/feature/plane_manifest.go` and `FrozenPlaneSet` are live production architecture. Request-body access classification must extend that manifest/generator and must not recreate named mirrors.
2. **Decode admission is an authority.** `frontendpipe` runs shared JSON preflight first, then `decodeqos.TryAdmit(ctx, ..., len(body))`, and only protocol decode is held under `decodeqos.Guard`. A protocol semantic scanner run during client upload would bypass this authority and alter concurrency/DoS semantics.
3. **An EventStream-only alternate executor result is insufficient.** Current frontends use the canonical `*lipapi.Call` and/or `Decoded.Extra` after execution to construct response IDs, cancellation carriers, timestamps, stream wrappers, and protocol state. The wire use case needs bounded response facts plus frontend-owned response state; it must not fabricate a partial Call.
4. **The canonical Call is still consumed after identity and route preparation.** `preparedRequest.call` currently feeds routing/capability facts, billing admission/identity/token estimates, `recvTurnFacts`, continuation support, interleaved-thinking recorders, terminal usage/session fields, and other downstream machinery. Route refactoring alone is not sufficient. Every post-identity Call dependency must be inventoried and either converted to exact bounded facts or classified as a wire blocker.

A fifth protocol-specific finding affects rollout order: OpenResponses create defaults `store=true`, and its `AfterDecode` prepares continuation/response state. Therefore “no previous_response_id” is not enough to make OpenResponses a simple first lane.

## Goals

- Remove whole-request heap materialization, canonical request-tree construction, and outbound re-encoding for certified multi-megabyte requests.
- Preserve request limits, JSON hardening, decode QoS, auth/session/B2BUA, routing/recovery, billing/accounting, guardrails, traffic behavior, and frontend response semantics.
- Keep canonical fallback possible after client bytes have been consumed but before provider commitment.
- Keep provider knowledge at frontend/backend edges; core owns policy/orchestration only.
- Make wire eligibility explicit, generation-pinned, fail-closed, and observable.
- Avoid a second executor architecture: canonical and wire lanes converge on the same route/B-leg/recovery/response-event machinery as soon as exact facts exist.

## Non-Goals

- Native provider passthrough (#490).
- Sending bytes upstream before whole-request proof.
- Cross-protocol forwarding.
- Broadly converting existing plugins/hooks to streaming request APIs.
- Changing default request limits or adding Brotli/zstd/deflate.
- Weakening current billing/guardrail/traffic authorities.
- Maintaining a second extension-plane classification system.
- Fabricating a partial `lipapi.Call` to satisfy legacy consumers.

---

## 1. Current Canonical Flow: Behavioral Oracle

The shared HTTP create path currently behaves, in simplified order, as follows:

```text
frontend handler
  ├─ method/path/auth/content-type checks owned by handler
  └─ frontendpipe.ServeHTTP
       ├─ reqbody.ReadAll (gzip decode + MaxBytesReader)
       ├─ route-selector/header/body-model preliminary resolution
       ├─ jsonguard.PreflightWithContext
       ├─ decodeqos.TryAdmit(weight = decoded body bytes)
       │    └─ decodeqos.Guard(Spec.Decode)
       ├─ authoritative session-header application
       ├─ Call.Validate
       ├─ Spec.AfterDecode (when present)
       ├─ Call.Validate again
       ├─ frontend TrafficPorts.Emit(full []byte)
       ├─ stream/debug request facts
       ├─ Executor.Execute(*lipapi.Call)
       │    ├─ secure identity/session/A-leg preparation
       │    ├─ request authorities / hooks / guards / conversation state
       │    ├─ billing / route / capability / attempts
       │    ├─ backend encode/open
       │    └─ canonical EventStream
       ├─ Spec.WrapStream
       ├─ BuildEncodeOpts(decoded Call/Extra)
       └─ protocol response writer
```

Every optimized branch is compared against this ordering. A wire lane is not allowed to silently move a client-visible frontend error behind core side effects.

### Characterization gate

Before production refactoring, tests freeze at least:

- malformed/trailing JSON and exact body-limit boundaries;
- gzip decoded-limit behavior;
- decode-admission saturation/overweight/cancel/release behavior;
- session/A-leg lifecycle and finalization;
- request-authority/secret-guard/traffic/billing ordering;
- route/failover/race behavior;
- frontend response IDs, cancellation carriers, timestamps, `AfterDecode` state, wrappers, and error mapping.

---

## 2. Architecture Decisions

### ADR-1: Capture to EOF before provider commitment

The request body is captured once with bounded RAM and optional file spill. Whole-body shared JSON validation completes before provider open. This preserves the pre-commit safety requirement and provides immutable replay for failover.

### ADR-2: Two logical JSON passes preserve decode QoS

The design separates **shared shape validation** from **protocol semantic proof**:

1. During capture, run the provider-neutral streaming lexer/shape validator because canonical shared preflight also occurs before decode admission.
2. Once EOF/final decoded size is known, acquire the existing byte-weighted `DecodeAdmission` permit.
3. While the permit is held, reopen the immutable source and run the frontend profile's low-allocation semantic verifier. If the profile declines, run the existing canonical `Spec.Decode` under the same admission semantics.
4. Release exactly once before existing post-decode/`AfterDecode` behavior, matching current ownership.

This deliberately performs a second disk/memory read for a wire candidate. The trade is acceptable: it avoids a multi-megabyte object graph while preserving admission semantics and avoids holding scarce decode permits while a slow client uploads.

### ADR-3: No fake Call; use exact wire-execution facts

The wire lane carries only facts proven by frontend parsing, identity/session preparation, composition, and backend resolution. A fake or partial `lipapi.Call` is forbidden.

Any downstream consumer that still requires full request content is a canonical blocker until it receives an explicit typed wire contract.

### ADR-4: Alternate execution returns response facts, not only a stream

An eligible request needs frontend response metadata after execution. The large-body executor therefore returns a result envelope:

```go
// package pkg/lipsdk/requestbody (illustrative API; exact names may change)
type ExecutionPath uint8
const (
    PathUnknown ExecutionPath = iota
    PathWire
    PathCanonicalContinuation
)

type ResponseFacts struct {
    CallID                 string
    TraceID                string
    ALegID                 string
    AuthoritativeSessionID string
    SessionID              string
    Operation              lipapi.Operation
    DeliveryMode           lipapi.DeliveryMode
}

type ExecutionResult struct {
    Stream   lipapi.EventStream
    Response ResponseFacts
    Path     ExecutionPath
}

type LargeBodyExecutorView interface {
    ExecuteLargeBody(context.Context, ExecutionRequest) (ExecutionResult, error)
}
```

The exact field set is driven by characterization tests, not convenience. It remains provider-neutral and content-free.

Frontend-specific encode/continuation state remains in the frontend. `frontendpipe` gains wire-specific response binding callbacks rather than stuffing provider state into `ResponseFacts` or core.

Protocol-opaque IDs/timestamps do not have to byte-match the canonical implementation if the protocol does not promise determinism, but each profile must explicitly document that normalization and preserve format, uniqueness, correlation, cancellation/continuation semantics, and all non-opaque fields.

### ADR-5: Typed plane manifest is the only access-classification source

Add request-body access metadata to `feature.Plane[T]` (or an equivalent generated descriptor attached to the canonical plane declaration):

```go
type RequestBodyAccess uint8
const (
    RequestBodyAccessUnclassified RequestBodyAccess = iota
    RequestBodyAccessCanonicalRequired
    RequestBodyAccessMetadataOnly
    RequestBodyAccessResponseOnly
)
```

`Unclassified` is intentionally zero. Production manifest validation/codegen/architecture tests reject it. Runtime summary generation still treats absence/unknown as canonical-required for defense in depth.

The generator emits a bounded `AccessSummary` for the frozen generation. No runtime request-path map/reflection/type-assertion walk is introduced.

### ADR-6: Close all post-identity Call dependencies before backend wire work

Before a wire backend can be opened, build an inventory of every post-identity full-Call consumer. Examples already observed include:

- `buildRoutePlan`: selector, request-size estimate, failover requirements;
- billing credit/exposure/identity/policy/max-output/token-estimator callbacks;
- `recvTurnFacts` baseline/ingress snapshots;
- continuation-support calculation;
- hidden/visible interleaved-thinking recorder setup;
- terminal/session usage closure fields;
- any hooks/resolvers discovered by the inventory.

Each is classified as:

1. **canonical-required** — needs request content or arbitrary Call callbacks;
2. **exact fact** — refactor canonical and wire paths to use a common bounded value object;
3. **response-only** — safe because responses remain canonical events.

A ratchet test prevents new post-identity Call dependencies from being silently ignored.

---

## 3. Configuration

Illustrative configuration:

```yaml
server:
  max_request_body_bytes: 8388608 # existing authority; unchanged
  large_payload_fast_path:
    enabled: false
    threshold_bytes: 1048576
    memory_spool_bytes: 65536
    max_inflight_spool_bytes: 1073741824
    spool_dir: ""
```

Rules:

- default off;
- threshold > 0;
- memory spool > 0 and <= threshold;
- max in-flight logical reservation > 0;
- empty spool dir resolves to `os.TempDir()`; explicit dir is validated during candidate generation/reload;
- invalid reload retains last-good generation;
- the spool budget is an **optimization resource budget**, not global memory admission. Exhaustion selects canonical behavior, which can still allocate according to the pre-existing path; metrics/docs must say this explicitly.

---

## 4. Replayable Request Body

### Public provider-neutral source

```go
type Source struct {
    Size  int64
    Open  func() (io.ReadCloser, error)
    Close func() error
}

type Span struct {
    Start int64
    End   int64
}
```

Contract:

- immutable after capture;
- every `Open` starts at byte zero with an independent cursor;
- independent readers can coexist when route semantics require parallel attempts;
- `Close` is idempotent and marks root ownership closed; it does not block indefinitely waiting for readers;
- final temp-file deletion occurs when root is closed and active readers reach zero;
- no cleanup goroutine is required.

### Capture state machine

```text
client body
  ├─ fixed copy buffer
  ├─ <= memory_spool_bytes: immutable memory prefix
  └─ spill: private CreateTemp file
        └─ append remaining bytes
```

The capture tracks logical bytes against the global optimization budget. For known identity-body lengths, reservation may be made up front within the existing request cap. Unknown/chunked requests reserve incrementally. Gzip is canonical in wave 1.

If reservation/spill creation fails after bytes have been consumed, the capture retains a continuation/canonical reader whenever underlying I/O is still usable. It must not discard an already-consumed prefix.

### Confidentiality

- unpredictable names only (`lip-large-body-*`), never user/model/session text;
- owner-only file permissions as provided by secure temp-file APIs/platform;
- no spool paths/content in logs/traces/metrics;
- docs explicitly warn that plaintext prompts may reside briefly on disk and recommend a protected local volume/directory for sensitive deployments.

---

## 5. Streaming JSON Shape Scanner

One provider-neutral scanner under `internal/core/jsonshape` (or reuse/extend the current shared package) provides:

- incremental JSON lexical validation;
- exact shared limits/counts;
- cancellation checkpoints;
- bounded selected-token events with raw offsets;
- no retained ordinary scalar contents.

It must validate:

- UTF-8 split across buffers;
- escapes and surrogate pairs;
- number grammar;
- nesting/delimiters;
- multiple roots/trailing data;
- key/string/number byte limits;
- token/object/array/depth limits.

The existing slice preflight remains the compatibility oracle. Differential/fuzz tests are required before any profile is enabled.

The scanner's default duplicate behavior remains aligned with shared preflight. Protocol profiles can impose stricter duplicate handling. OpenAI wire profiles conservatively decline duplicate names in protocol-owned objects where canonical decode/re-encode can collapse duplicates unless exact parity is separately proven.

---

## 6. Frontend Wire Profile and Decode-Admission Flow

### Profile responsibility

A profile proves only frontend/protocol facts. It does not choose a backend or open network connections.

Illustrative bounded output:

```go
type Metadata struct {
    ProfileID            ProfileID
    FrontendID           string
    Operation            lipapi.Operation
    DeliveryMode         lipapi.DeliveryMode
    RouteSelector        string
    ClientModel          string
    ProtocolRequirements lipapi.ProtocolRequirements
    ModelSpan            Span
    BodyBytes            int64
    MaxOutputTokens      *int64 // only when exact and bounded
}
```

No raw headers, credentials, temp paths, provider SDK objects, arbitrary maps, prompt/tool contents, or user metadata cross into core.

### Ingress algorithm

```text
handler auth/content-type/path
  ↓
feature/profile/executor available?
  ├─ no → canonical
  ↓
frontend full-body traffic contract active?
  ├─ yes → canonical before spool
  ↓
known identity Content-Length < threshold?
  ├─ yes → canonical
  ↓
gzip in wave 1?
  ├─ yes → canonical
  ↓
capture to EOF under existing max body limit
  + shared streaming JSON preflight
  ↓
final decoded size < threshold?
  ├─ yes → materialize → canonical
  ↓
DecodeAdmission.TryAcquire(final bytes)
  ├─ reject → existing admission wire error
  ↓  (permit held)
profile semantic proof from Source.Open()
  ├─ decline → canonical Spec.Decode under preserved admission semantics
  └─ proven → release permit
  ↓
frontend wire-preparation checks/state
  ↓
ExecuteLargeBody
```

### Why protocol proof is not performed during upload

Doing so would perform the expensive frontend semantic decode outside the existing `DecodeAdmission` authority and would make configured decode capacity ineffective for wire candidates. Conversely, acquiring admission before reading the client body would allow slow uploads to occupy permits far longer than canonical requests. The second replay pass is the safe compromise.

### Canonicalization callback

`ExecutionRequest` contains a trusted callback capable of materializing/reopening the body and invoking the existing canonical frontend decoder/validator when core discovers a later blocker:

```go
type CanonicalizeFunc func(context.Context) (*lipapi.Call, error)
```

The frontend owns a request-local canonical-state holder. When `Canonicalize` executes, it records the normal `Decoded`/`AfterDecode` state locally and returns only the fully validated Call to core. Core never owns frontend state.

**Ordering constraint:** a profile is not eligible if canonicalization can still produce an ordinary frontend error or pre-core side effect that has not been equivalently resolved before `ExecuteLargeBody`. This prevents moving frontend errors behind `BeginTurn`. Profiles with `AfterDecode` side effects need an explicit pre-core wire-state contract or remain canonical-only.

---

## 7. Frontend Response-State Bridge

The original draft attempted to route the returned EventStream through existing encoders while no longer having a canonical Call. That does not work for current frontends.

`frontendpipe` therefore gets an additive wire lane with frontend-owned callbacks, conceptually:

```go
type WireFrontendState any // internal frontendpipe only; never crosses SDK/core

type Spec[Opts any] struct {
    // existing canonical fields unchanged
    ...

    WireProfile            WireProfile
    PrepareWireState       func(context.Context, WireProof) (WireFrontendState, error)
    WrapWireStream         func(context.Context, WireFrontendState, requestbody.ResponseFacts, lipapi.EventStream) (lipapi.EventStream, error)
    BuildWireEncodeOpts    func(WireFrontendState, requestbody.ResponseFacts) (Opts, error)
    WriteWireStream        func(...)
    WriteWireNonStream     func(...)
}
```

Exact API shape may be smaller if existing writers can be refactored to a common encode context. The invariants matter more than the names:

- no fake Call;
- no frontend/provider state in core;
- canonical path remains source compatible;
- wire response facts are content-free and bounded;
- cancellation/correlation semantics remain correct;
- canonical continuation uses the canonical state holder populated by `Canonicalize`.

### Response dependency inventory examples

- OpenAI Responses: `responseIDForCall`, A-leg/session cancellation carrier, `StableUnix`/clock, stream/nonstream encode options.
- OpenAI Chat: completion ID and created timestamp currently derive from the canonical Call.
- OpenResponses: `AfterDecode` creates `createEncodeState`; stream wrapping and continuation/store behavior use it; non-stream collection reads call options.
- stream/debug helpers currently accept Call fields and need either bounded wire facts or a wire-specific helper.

Each certified profile must resolve its inventory before advertising compatibility.

---

## 8. Core State Machine

### Static eligibility before identity side effects

Core first checks generation-pinned facts that do not require a durable turn:

- feature/profile supported;
- typed plane `AccessSummary` has no canonical-required occupant;
- core traffic/raw-capture/redaction authority is wire-compatible;
- configured Call-only billing/policy/token/context callbacks have a typed wire contract;
- other generation-global blockers.

Static decline calls `Canonicalize` and then ordinary `Execute` before any large-body-specific identity side effect.

### Safe preparation split

Only after characterization tests pass, split current preparation into the minimum reusable phases:

```text
prepareIdentityAuthority
  - request/trace identity facts available without content mutation
  - principal/scope/session-open metadata stage
  - workspace resolution when metadata-only certified
  - SecureSession.BeginTurn
  - A-leg identity/fetch
  - route-authority snapshot/barrier
  - frozen request views required by downstream execution

prepareCanonicalAfterIdentity
  - every remaining current canonical stage in exactly current order
  - receives a fully validated canonical Call
```

Ordinary canonical `prepareRequest` is recomposed through these functions first, so the refactor is battle-tested before wire execution uses it.

### Dynamic eligibility

After identity, resolve facts that truly require bound session/A-leg state:

- authoritative route override;
- active conversation exclusion/steering;
- frozen selector/reachable candidate set;
- route strategy/replay concurrency needs;
- per-candidate native model;
- any session-specific resolver discovered by Call-dependency inventory.

If blocked, call `Canonicalize` and continue via `prepareCanonicalAfterIdentity` **without a second `BeginTurn`**.

A profile cannot enter this state machine if `Canonicalize` could still legitimately fail due to a frontend pre-core side effect that should have occurred earlier.

---

## 9. Closing the Post-Identity Call Dependency

Create a `wireExecutionFacts`-style internal value object only after inventorying actual consumers. It may contain exact facts such as:

```go
type wireExecutionFacts struct {
    Selector             string
    Operation            lipapi.Operation
    DeliveryMode         lipapi.DeliveryMode
    ClientModel          string
    ProtocolRequirements lipapi.ProtocolRequirements
    MaxOutputTokens      *int64
    BodyBytes            int64
    TraceID              string
    ALegID                string
    AuthoritativeSessionID string
    // only additional bounded facts justified by a current consumer
}
```

Do not add a field simply because a Call has one. Every field has an owner/consumer and characterization test.

### Routing

Extract canonical route-plan construction into helpers accepting exact facts:

- selector compilation and aliases;
- execution-composition validation;
- native model binding;
- affinity/interleaved/session routing state;
- attempt/TTFT budgets;
- failover requirements built from exact protocol requirements;
- request-size estimate only when an exact existing estimator contract exists.

Canonical `buildRoutePlan` should use the same helper with facts derived from the real Call.

### Billing

Current stock billing surfaces embed a full Call. The design must not simply treat that as “some custom callback” and then discover that normal production composition always blocks.

Audit the stock path specifically:

- principal account identity is already context-derived in the standard identity implementation;
- charge policy/customer pricing/max-output callbacks may still be Call-shaped;
- request-token estimation cannot be replaced by raw body bytes;
- terminal usage needs exact session/A-leg/billing identity facts, not prompt content.

Where stock semantics can be represented exactly, introduce a typed wire admission input/helper and have canonical billing derive it from Call. Custom arbitrary Call callbacks remain canonical-only unless they opt into a typed wire contract.

The eligibility benchmark matrix must show whether billing-enabled stock composition can reach the wire lane.

### Terminal/response machinery

`recvTurnFacts`, continuation support, interleaved-thinking recorders, and terminal usage also participate in the dependency audit. Content-requiring pieces block; metadata-only pieces are refactored to exact views. No dereference of `prep.call` is allowed on the wire branch.

---

## 10. Backend Wire Contract

Internal backends gain optional functions conceptually:

```go
type WireRequestFacts struct {
    ProfileID            requestbody.ProfileID
    Operation            lipapi.Operation
    DeliveryMode         lipapi.DeliveryMode
    ProtocolRequirements lipapi.ProtocolRequirements
    ClientModel          string
    CandidateModel       string
    BodyBytes            int64
}

type WireRequestSupport struct {
    Compatible          bool
    NativeModel         string
    NeedsModelRewrite   bool
    SupportsParallelOpen bool
    Reason              string // bounded/static classification
}

type WireAttempt struct {
    Facts  WireRequestFacts
    Source requestbody.Source
    Rewrite *requestbody.RewritePlan
}

type Backend struct {
    // existing fields
    ResolveWireRequest func(context.Context, WireRequestFacts) (WireRequestSupport, error)
    OpenWire           func(context.Context, WireAttempt) (lipapi.EventStream, error)
}
```

Nil functions mean canonical-only. Core never switches on provider/backend names.

For the frozen route, support is resolved for **all reachable candidates** before the first body reader is handed to provider transport. An incompatible candidate causes whole-request canonical continuation.

---

## 11. Token-Aware Model Rewrite

The scanner records the exact raw span of the selected top-level model JSON token. For a candidate native model:

```text
original source
  [0 : model.start]
  + json.Marshal(nativeModel)
  + [model.end : size]
```

The reader is a streaming splice; it does not build a second body. Rewritten size uses checked arithmetic. Duplicate/ambiguous model fields or any canonical normalization uncertainty decline to canonical.

Every attempt opens a fresh source reader and constructs its own rewrite reader.

---

## 12. Retry/Failover/Race Integration

Wire provider open is integrated into the existing B-leg/attempt/recovery owner rather than creating a parallel retry loop.

Preserved contracts:

- same attempt budget and candidate order;
- same affinity and weighted-first/interleaved state;
- same credential/provider failure classification;
- retry/failover only before visible client output;
- same B-leg allocation/cleanup and terminal accounting;
- canonical EventStream assembly after provider response parsing.

For parallel/race selectors, each attempt receives an independent body reader. If a source/backend combination cannot support the exact concurrency semantics, the whole request is canonical.

---

## 13. Protocol Certification Order

The brownfield audit changes the recommended order.

### Lane 1: OpenAI Responses → OpenAI-compatible Responses

Reasons:

- uses shared `frontendpipe` with no current `AfterDecode` side-effect stage on create;
- high relevance to agentic coding workloads;
- provides a forcing function for the new response-facts/cancellation bridge.

Conservative profile rules include:

- exact create endpoint only;
- no body-carried proxy/session metadata or repair-sensitive aliases;
- duplicate protocol-owned names canonical-only;
- malformed function/tool histories that canonical decode repairs/skips are canonical-only;
- exact model/stream/output controls and protocol requirements;
- response cancellation ID must remain bound to authoritative A-leg/session through `ResponseFacts` or equivalent semantics.

### Lane 2: OpenAI Chat Completions → OpenAI-compatible Chat

Add only after Responses parity is green. Resolve completion ID/timestamp dependency without a fake Call. Treat protocol-opaque ID differences explicitly in conformance normalization if needed.

### Lane 3: OpenResponses HTTP create → OpenResponses-compatible backend

OpenResponses is intentionally moved later because current create decode has extra frontend state.

Initial eligibility requires at minimum:

- HTTP create only;
- explicit `store:false` (absence is **not** eligible because canonical default is true);
- no `previous_response_id`;
- no compaction;
- no WebSocket ingress;
- a bounded wire response-state bridge covering response ID, allowed-tool/options behavior, and stream/nonstream encoding needs;
- no unresolved `AfterDecode` side effect/error that could be shifted behind `BeginTurn`.

Only a later certification may add `store:true` or continuation, after reproducing reservation/recorder/cleanup/lineage semantics exactly.

### Other frontends

Anthropic/Gemini/etc. remain canonical until individually certified. Same JSON transport is not evidence of compatibility.

---

## 14. Compression Follow-Up

Wave 1: gzip always canonical.

Later wave:

```text
compressed client body
  → existing bounded decompressor semantics
  → decoded identity JSON replay source
  → same scanner/admission/profile proof
  → provider sends identity JSON with stale Content-Encoding removed
```

Decoded bytes, not compressed `Content-Length`, control threshold/limits/reservation.

---

## 15. Observability

Bounded metrics:

- considered / wire / canonical fallback counts;
- fallback reason enum (`disabled`, `below_threshold`, `gzip`, `frontend_traffic`, `decode_admission`, `profile_decline`, `plane_blocker`, `call_dependency`, `route_incompatible`, `backend_incompatible`, `spool_budget`, etc.);
- body-size buckets;
- memory-vs-file spool;
- rewrite/replay count;
- capture/shared-preflight/protocol-proof/provider-open latency;
- spool bytes and active logical reservations.

No model/backend/user/session IDs in metrics labels. No body content/path in logs.

---

## 16. Performance Validation

Benchmarks compare disabled canonical vs enabled wire path for representative 32 KiB, 256 KiB, 1 MiB, 5 MiB, and test-only 20 MiB bodies.

Measure:

- allocs/op and B/op;
- GC cycles/pause/heap under concurrency;
- CPU time;
- file I/O;
- capture → protocol proof → provider-open latency;
- retry replay cost;
- malformed/late-metadata/giant-string behavior;
- budget saturation.

Important interpretation: this architecture cannot improve time-to-provider-open below the need to receive/validate the full client request; its primary objective is heap/GC and redundant object/marshal reduction. Performance claims must reflect that.

An eligibility matrix must include standard production-like configurations. The feature is not complete if all realistic configurations silently fall back because stock billing/extension/response dependencies were left Call-only.

---

## 17. Failure and Ownership Matrix

| Failure point | Provider body committed? | Required outcome |
|---|---:|---|
| request exceeds current max | no | existing 413/error mapping |
| shared JSON invalid | no | existing invalid JSON mapping |
| decode admission saturated | no | existing 429/503 + Retry-After semantics |
| profile semantic decline | no | canonical decode/fallback |
| spool budget exhausted | no | canonical fallback; metric only |
| temp create/write fails but bytes recoverable | no | canonical fallback |
| temp I/O unrecoverable | no | existing read/internal I/O class |
| static core blocker | no | canonical before BeginTurn |
| dynamic blocker after identity | no | canonical continuation, same turn |
| backend wire proof incompatible | no | canonical continuation |
| provider open fails pre-output | maybe headers, no client output | existing retry/failover rules |
| failure after client-visible output | yes | no new failover; existing terminal behavior |
| client cancel | depends on phase | close readers/source; existing A/B-leg cleanup |

---

## 18. Security Considerations

- Full-body validation and all authorities remain pre-provider-commit.
- Spool files contain potentially sensitive plaintext and are treated as secrets-in-transit-at-rest for their short lifetime.
- Credentials never come from the client body/Authorization passthrough; backend credential policy remains authoritative.
- Raw forwarding is allowed only for certified same-wire semantics and known normalization behavior.
- Extension access summary fails closed.
- Decode admission prevents the wire semantic verifier from becoming an ungoverned CPU/heap lane.
- No provider-specific switch enters core.

---

## 19. Architecture Ratchets

Tests/code generation shall fail if:

- a new production extension plane has unclassified request-body access;
- a wire branch dereferences `preparedRequest.call` or supplies a fake Call after the dependency-closure task;
- provider/backend names appear in core wire eligibility;
- provider SDK types appear in `pkg/lipsdk/requestbody`;
- frontend wire semantic proof runs outside decode admission;
- a certified profile has unhandled `AfterDecode`/response-state dependencies;
- a route silently drops an incompatible candidate to stay on the wire lane.

---

## 20. Implementation Gate

The feature is implementation-ready only when the following design proofs exist as tests/artifacts before enabling a production profile:

1. canonical ingress/decode-QoS/lifecycle/response characterization;
2. typed-plane access classification ratchet on current manifest architecture;
3. complete post-identity Call dependency inventory and classification;
4. response-state bridge characterization for the first frontend;
5. replay source + scanner differential/fuzz suite;
6. static/dynamic canonical continuation exact-once tests;
7. route-wide backend proof tests;
8. canonical-vs-wire end-to-end differential conformance;
9. performance + realistic eligibility evidence.

Until then the configuration remains default-off and no profile advertises production wire compatibility.
