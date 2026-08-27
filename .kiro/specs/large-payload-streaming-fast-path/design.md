# Design Document

## Overview

#503 adds an **optional, proof-gated large-request execution lane** for already-supported JSON create operations. An eligible request is captured into a bounded-memory replay source, receives the same shared JSON protections, is semantically proven under the existing byte-weighted decode admission, is assessed against the complete frozen runtime composition, and only then is replayed directly to an explicitly same-wire backend without building the full canonical `lipapi.Call` graph or re-marshalling the provider request.

The existing canonical path remains the behavioral oracle and fallback. “Streaming” does **not** mean client bytes are forwarded upstream before EOF: Go-LIP still needs complete request validity and eligibility before provider commitment. The first-order benefit is lower retained heap/GC and avoided canonical decode/re-encode work for multi-MiB bodies, not earlier provider TTFT.

This document was revalidated against `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533.

## Brownfield Findings That Changed the Draft

The original spool/replay direction was sound, but implementation-readiness required material corrections:

1. `feature.Plane[T]`, `plane_manifest.go`, and `FrozenPlaneSet` are now production architecture; there is no reason to preserve a second legacy plane-classification path.
2. `hooks.Bus` remains a separate production request authority outside the typed plane set and must be classified explicitly.
3. Protocol `Decode` is protected by `decodeqos.TryAdmit(..., decodedBodyBytes)`. Performing wire semantic proof during upload would bypass that authority.
4. Releasing decode admission after semantic proof and later rediscovering a core blocker would force a second admission decision; that could turn a request that was already admitted into a new 429/503. The correct seam is therefore **two-phase assessment while the same permit remains held**.
5. Frontend response construction still depends on `*lipapi.Call`/`Decoded.Extra`, including cancellation identity and session response carriers. EventStream-only is insufficient.
6. New secure sessions return the raw resume token by mutating the canonical `call.Session` after `BeginTurn`; wire execution needs a sensitive frontend-bound session response carrier.
7. The secure-session recorder is standard/always composed, but currently records only normalized item shape (role/ordinal/part kinds), not prompt text. It should get a compact wire view rather than permanently blocking the optimization.
8. Metering frontend/backend ingress checkpoints currently clone and retain full Calls even when the public checkpoint needs only bounded quantities/correlation. The wire lane needs wire-native checkpoints or the memory optimization is defeated.
9. `diag.StableCallID`/`StableCallToken`/`StableUnix` hash the canonical Call, and Call.ID drives restart-stable metering identities. A different raw-body hash is not a harmless implementation detail; profiles need an equivalent canonical semantic digest.
10. Standard memory and Bun continuity stores implement route overrides and runtimebundle wires `RouteOverrideReader` when available. Treating reader presence as a blocker would make V1 effectively dead in normal deployments. Late-bound routing instead needs a pre-certified **route-domain envelope**.
11. OpenResponses create defaults `store=true`; absence of `previous_response_id` is not enough for a stateless first lane.
12. `preparedRequest.call` remains consumed by routing, metering/accounting, billing, `recvTurnFacts`, continuation/interleaved-thinking, and terminal/session paths. Every post-commit dependency must be closed or blocked before assessment succeeds.

---

## 1. Canonical Flow: Behavioral Oracle

The current shared HTTP create flow is approximately:

```text
frontend handler
  ├─ method/path/auth/content-type checks
  └─ frontendpipe.ServeHTTP
       ├─ reqbody.ReadAll (gzip decode + MaxBytesReader)
       ├─ preliminary route selector/model resolution
       ├─ jsonguard.PreflightWithContext
       ├─ decodeqos.TryAdmit(weight = decoded body bytes)
       │    └─ decodeqos.Guard(Spec.Decode)
       ├─ authoritative session headers
       ├─ Call.Validate
       ├─ Spec.AfterDecode (if any)
       ├─ Call.Validate again
       ├─ stable Call/trace identity
       ├─ frontend TrafficPorts.Emit(full []byte)
       ├─ Executor.Execute(*lipapi.Call)
       │    ├─ secure-session identity / BeginTurn / A-leg
       │    ├─ route override snapshot
       │    ├─ guards / traffic / request authority / submit hooks
       │    ├─ secure-session client-turn record
       │    ├─ conversation/request transforms
       │    ├─ metering/accounting/billing
       │    ├─ route/capability/attempts/backend encode
       │    └─ canonical EventStream + terminal settlement
       ├─ frontend WrapStream / BuildEncodeOpts
       ├─ session response carriers
       └─ protocol response writer
```

Characterization tests must freeze this ordering for target profiles before production refactoring.

---

## 2. Central ADR: Two-Phase Assess → Commit → Execute

V1 does not use a deep `Canonicalize` callback from core and does not split secure preparation merely to support late fallback.

Instead the optional executor seam is conceptually:

```go
// Illustrative internal/provider-neutral shape. Public lipsdk.ExecutorView is unchanged in V1.
type LargeBodyExecutorView interface {
    AssessLargeBody(context.Context, AssessmentRequest) (Assessment, error)
    ExecuteLargeBody(context.Context, ExecuteRequest) (ExecutionResult, error)
}
```

### Phase A: capture and proof

```text
handler cheap gates
  ↓
bounded replay capture to EOF
  + shared streaming JSON preflight
  ↓
final decoded size known
  ↓
decodeqos.TryAdmit(exact decoded bytes)
  ↓ permit held
frontend protocol semantic proof from Source.Open()
  ↓
exact canonical identity digest + bounded wire facts
  ↓
core AssessLargeBody(wire facts)
```

`AssessLargeBody` is side-effect-free: no `BeginTurn`, store read or mutation, DB I/O, billing reservation, provider network call, spill filesystem work, client-body wait, or arbitrary unbounded plugin work. It reads only immutable/frozen generation composition and calls pure backend wire-support functions. Its additional decode-permit hold is explicitly benchmarked and bounded by generation-sized data structures.

### Decline

If the profile or assessment declines, **the same decode permit remains held**:

```text
materialize replay source
  ↓
existing Spec.Decode under current Guard/admission ownership
  ↓
release permit at today's boundary
  ↓
existing Validate / AfterDecode / traffic / Executor.Execute
```

There is no release/reacquire race and no new admission outcome introduced by the optimization.

### Eligible

If assessment succeeds:

```text
release decode permit
  ↓
WIRE COMMIT (one way)
  ↓
ExecuteLargeBody(assessment stamp + proof + Source)
  ↓
BeginTurn / A-leg / route authorities / attempts / response
```

No expected condition after this point requires a canonical Call. If execution finds an unclassified content dependency after commit, that is an invariant bug: finalize/abort the one turn; never launch a second canonical execution.

### Assessment stamp

Assessment returns an opaque provider-neutral stamp, e.g. generation ID + digest/token over immutable assessment inputs. Execution validates that it is running on the same executor/generation and that the proof inputs have not changed. Backend-specific plan objects do not cross the internal frontend/core seam, and the public SDK remains unchanged; execution may recompute pure support checks and assert identical results.

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
    max_semantic_fact_bytes: 262144
    spool_dir: ""
```

Rules:

- default off;
- positive threshold/spool/budget/fact bounds; memory spool <= threshold;
- explicit spool directory validated during candidate config/reload; empty uses `os.TempDir()`;
- invalid reload retains last-good generation;
- `max_inflight_spool_bytes` bounds optimization-owned replay storage only. Canonical fallback may still allocate according to the old path; this is not global OOM admission.

---

## 4. Replayable Body Source

Internal provider-neutral V1 contract; this is not new public SDK surface:

```go
type Span struct {
    Start int64
    End   int64
}

type Source struct {
    Size  int64
    Open  func() (io.ReadCloser, error)
    Close func() error
}
```

All first-wave consumers are built-in frontend/core/backend packages. Existing public `lipsdk.ExecutorView` and external/manual executors remain unchanged and canonical-only. Promotion of these low-level replay/proof/assessment types to `pkg/lipsdk` would require a separate API/ABI/versioning review once a concrete external consumer exists.

Capture state:

```text
client body
  ├─ fixed/reused copy buffer
  ├─ bounded in-memory prefix
  └─ spill → secure CreateTemp file + append
```

Invariants:

- immutable after EOF;
- every `Open` begins at offset zero and has an independent cursor;
- multiple readers may coexist for races;
- root `Close` is idempotent and nonblocking with respect to outstanding readers;
- root close marks deletion pending; last tracked reader performs final file removal;
- no goroutine per body chunk and no cleanup-wait goroutine;
- every consumed byte remains recoverable until frontend chooses canonical fallback or ownership transfers to wire execution.

Mid-capture optimization failure has an explicit no-rewind contract. Capture retains the current input chunk until its destination write has fully succeeded. On a short/partial file or memory write, only the written prefix is treated as durable and the unwritten suffix stays owned by capture. A canonical-continuation reader then presents exactly:

```text
successfully retained memory/file prefix
+ current unwritten suffix
+ still-unread client body
```

under the existing request-size ceiling. It may materialize the ordinary canonical `[]byte` because the optimization has been abandoned, but it must never restart or reread the client socket. Reservation decline, file-create failure, partial write, exact-limit boundaries, cancellation, and randomized chunk splits are fault-tested byte-for-byte against direct canonical reading.

Capture computes an incremental **source digest** over decoded identity bytes. This is useful for immutability/replay/widening evidence but is distinct from the canonical semantic identity digest described later.

Spill confidentiality:

- unpredictable names only;
- no user/model/session data in names;
- owner-only permissions where supported;
- no spool path/content in normal telemetry;
- docs state files may contain plaintext prompts.

---

## 5. Shared Streaming JSON Shape Scanner

One provider-neutral shared scanner validates:

- UTF-8 across buffers;
- escapes and surrogate pairs;
- JSON number grammar;
- delimiters/nesting/root/trailing data;
- existing byte/depth/token/object/array/key/string/number limits;
- context cancellation.

It retains no ordinary giant scalar contents. It can emit bounded structural events and raw spans to a frontend profile observer.

The current slice preflight remains the differential oracle. Fuzz/property suites compare acceptance/error class and aggregate counts across buffer splits and exact limit boundaries.

---

## 6. Frontend Semantic Proof

A frontend profile is a protocol-specific **proof adapter**, not a backend selector. It consumes a replay reader under decode admission and emits bounded facts only for request subsets proven equivalent to canonical decode/validation.

Illustrative proof:

```go
type SessionInput struct {
    AuthoritativeSessionID string
    ResumeToken            string // sensitive; never backend/log/metric
    ClientSessionID        string
}

type ClientTurnShape struct {
    Lines []ClientInputShape // bounded role/ordinal/part-kind facts
}

type CanonicalIdentity struct {
    ExplicitCallID string
    StableSum      [32]byte // same semantic digest as current canonical stableCallSum
}

type Proof struct {
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
    Session              SessionInput
    ClientTurn           ClientTurnShape
    Identity             CanonicalIdentity
    SourceDigest         [32]byte
}
```

Exact types/names may differ, but the constraints are fixed:

- no raw arbitrary headers;
- no provider SDK types;
- no unbounded maps/user metadata;
- no prompt/tool text retained merely for proof;
- sensitive session token fields are explicitly marked and never serialized to telemetry/backend requests;
- semantic fact memory has an explicit bound; overflow selects canonical.

### Session precedence

`frontendpipe`/profile owns the same authoritative header/body precedence as current `sessionwire.ApplyAuthoritativeHeaders`. Initial OpenAI profiles may simply reject body-carried LIP session metadata while supporting the normal LIP session/resume headers.

### Normalized secure-session record shape

The current secure recorder consumes `lipapi.NormalizedItems` only to persist role/ordinal/content-part kinds. Profiles produce the equivalent shape for certified requests. They do not store prompt text. Large pathological item-count shapes that exceed the semantic-fact budget fall back.

---

## 7. Exact Canonical Identity Digest

A raw source SHA is **not** an acceptable substitute for canonical request identity.

Current `diag.stableCallSum` clones the canonical Call, clears `ID`, marshals it, and hashes the canonical JSON. That digest feeds stable Call IDs/tokens/timestamps. Call.ID then drives metering checkpoint IDs, fact/source IDs, trace correlation, billing/request identities, and frontend deterministic response fields.

A wire profile therefore implements a streaming **canonical semantic identity writer** for its certified subset. It must emit/hash the same canonical Call representation that the current decoder + session-header application + pre-core normalization would produce, without retaining large scalar contents.

Conceptually:

```text
raw JSON string/content
  → incremental decode/unescape + certified normalization
  → canonical Call-field serialization into hash.Writer
  → stable sum identical to diag.stableCallSum(canonicalCall)
```

The implementation may refactor `diag` to expose helpers such as stable ID/token/Unix **from an already-proven sum** without changing existing canonical outputs.

Differential identity tests are mandatory for:

- huge strings;
- Unicode/escape/HTML-sensitive string cases;
- message/tool/function shapes;
- optional fields/zero values;
- model/route selector;
- session header precedence;
- every certified protocol normalization.

If the profile cannot produce exact identity for a shape, it declines.

This also lets wire frontend response IDs/timestamps and economic identities remain path-stable rather than defining a second identity namespace.

---

## 8. Frozen Wire-Eligibility Summary

### Typed planes

Extend canonical `feature.Plane[T]` declarations with an access class:

```go
type RequestBodyAccess uint8
const (
    RequestBodyAccessUnclassified RequestBodyAccess = iota
    RequestBodyAccessCanonicalRequired
    RequestBodyAccessMetadataOnly
    RequestBodyAccessResponseOnly
    RequestBodyAccessWireContract
)
```

`Unclassified` is zero and rejected for production declarations. The generator/frozen set derives a compact access summary; runtime unknown still fails closed.

### Legacy hook bus is separate

`RequestRuntimeSnapshot` still contains `*hooks.Bus`; its submit/request-part/response-part/tool chains are not magically covered by `plane_manifest.go`. `HookChainLengths()` already provides frozen occupancy without reflection.

V1 summary rules are conservative:

- occupied submit hooks: canonical-required unless a new typed wire contract exists;
- occupied request-part hooks: canonical-required;
- response-part chains may be response-only when characterization proves that;
- tool reactors/future ambiguous chains fail closed unless explicitly classified.

### Standard non-plane authorities

The generation summary also records standard runtime capabilities/modes that matter for wire execution:

- frontend/core traffic/raw capture/redaction;
- secure-session recorder (wire-shape support is expected, not a permanent blocker);
- metering checkpoint mode;
- token accounting/preflight/billing/counting capabilities;
- route-override capability;
- Call-shaped custom billing/policy callbacks;
- any additional non-plane Call dependency discovered by the ratchet inventory.

The summary is built at composition time and pinned with the request generation. There is one plane declaration system, but wire eligibility intentionally spans more than planes because the brownfield runtime does.

---

## 9. Side-Effect-Free Core Assessment

`AssessLargeBody` takes the profile proof and frozen generation summary and returns `eligible` or one bounded decline reason.

It performs only pure/frozen work:

1. validate proof/profile support and assessment identity;
2. inspect typed-plane/hook/non-plane eligibility summary;
3. verify standard wire views exist for secure-session recorder/metering and any enabled accounting/billing path;
4. compile/validate the exact initial route selector using existing aliases/execution-composition policy;
5. bind direct candidate models where semantics are pure/generation-fixed;
6. build the exact initial candidate superset needed by weighted/fallback/race selection;
7. build any **late-bound route authority domain**;
8. ask backends for exact and/or domain-wide wire compatibility;
9. validate replay/parallel/model-rewrite requirements;
10. return a generation-bound assessment stamp.

No secure-session store lookup, route-override store read, token reservation, DB I/O, provider request, spill I/O, client-body wait, or other turn side effect occurs here.

If accounting/token preflight needs an exact input count and no wire counter exists, assessment declines now while the same decode permit is still held.

---

## 10. Late-Bound Route-Authority Envelope

### Why reader presence cannot be a blocker

Standard memory and Bun continuity stores implement the optional route-override store, and runtimebundle installs `RouteOverrideReader` whenever that capability exists. A rule `RouteOverrideReader != nil => canonical` would make V1 unreachable in ordinary secure-session deployments.

### Domain proof

Current route override state is keyed by authoritative A-leg and is intentionally snapshotted only after `BeginTurn`/A-leg fetch. V1 keeps that canonical ordering but pre-proves a **domain** large enough that the later selector can never reveal a wire-incompatible backend.

For route override, derive the domain from the same generation validator that accepts stored/admin selectors:

- current known backend IDs;
- alias expansion semantics;
- current execution-composition policy (sequential/fallback/race/etc.);
- model-selection domain accepted by that validator.

Because override validation can accept model text that is not a finite catalog, a backend used in this domain may need to certify that same-wire semantics hold for **any model value accepted by that backend/profile**. If backend wire support is model-dependent and the accepted override model domain cannot be closed, the domain is not provable and assessment declines.

After wire commit, the existing `snapshotRouteOverride`/barrier runs normally. Whatever selector it returns is within the already-certified domain by construction. Weighted-first/affinity/interleaved state may choose/order a subset without new proof.

### Other late selectors

Any route-hint/future authority that can produce a selector after commit needs its own bounded domain declaration. Existing route-hint providers currently receive a full Call, so they are canonical-required by default.

### Trade-off

A heterogeneous generation containing a canonical-only backend may make the route-override domain too broad and force canonical handling even when the request's initial selector is same-wire. This is a deliberate V1 correctness trade-off. Eligibility benchmarks must quantify it. A future read-only/transactional route-authority preview can narrow the domain without reintroducing unsafe late fallback.

---

## 11. Backend Wire Contract

Backends need both exact-candidate and, where necessary, domain-wide proof. Illustrative internal shape:

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

type WireDomainFacts struct {
    ProfileID            largebody.ProfileID
    Operation            lipapi.Operation
    DeliveryMode         lipapi.DeliveryMode
    ProtocolRequirements lipapi.ProtocolRequirements
    ModelDomain          ModelDomain // exact set or AnyAcceptedModel
    ExecutionModes       ExecutionModeSet
}

type WireSupport struct {
    Compatible           bool
    NeedsModelRewrite    bool
    SupportsParallelOpen bool
    Reason               WireSupportReason
}
```

`ResolveWireRequest` / `ResolveWireDomain` are pure and perform no provider I/O. Nil/unknown means canonical-only.

`OpenWire` remains attempt-time and receives the immutable source + rewrite plan. It reuses normal backend URL/credential/client/parser/error machinery.

Core never switches on provider names.

---

## 12. Model Rewrite

The profile records the exact raw token span of the selected top-level model. Per candidate:

```text
source[0:model.start]
+ json.Marshal(nativeModel)
+ source[model.end:size]
```

A splice reader streams these segments. Checked `int64` arithmetic computes rewritten length. Duplicate/ambiguous/repaired model forms decline.

Every retry opens a fresh source reader and creates a fresh splice reader.

---

## 13. Wire Secure-Session Preparation

After wire commit, execution follows the existing secure-session lifecycle but consumes bounded views rather than a fake Call.

Refactor only reusable **fact-based** helpers needed by both paths:

- principal/scope resolution;
- workspace/session-opener stages whose inputs are metadata-only;
- `BeginTurn` request construction from `SessionInput`;
- authoritative session/A-leg result binding;
- route-override snapshot after A-leg fetch;
- request authority inputs from exact identity/scope/session facts.

Content-reading/mutating stages are absent because assessment already proved them inactive or wire-certified.

### Secure recorder

Instead of `buildClientTurnRecordInput(..., *lipapi.Call)`, add a sibling/helper accepting the profile's `ClientTurnShape`. The resulting `ClientTurnRecordInput` must equal canonical output for the certified corpus.

### Session response carrier

Current `BeginTurn` returns a resume token for a new session and canonical preparation copies it into the caller-visible Call. Wire execution returns this through a sensitive response carrier:

```go
type SessionResponseCarrier struct {
    AuthoritativeSessionID string
    ALegID                 string
    ResumeToken            string // sensitive
}
```

The frontend writes existing `X-LIP-Session-ID` / `X-LIP-Resume-Token` semantics and never logs the token.

---

## 14. Wire-Native Metering Checkpoints

Current `checkpoint.Snapshot` clones a full Call for recount/rerate; using it unchanged would reintroduce the exact large heap object #503 is meant to avoid.

Add a wire-native checkpoint representation or a tagged source that shares the same public `metering.Checkpoint` semantics without retaining a Call.

### Frontend ingress wire evidence

Exact facts are available from proof + secure identity:

- stable request ID / trace ID from canonical digest;
- scope/frontend ID;
- A-leg/session correlation after BeginTurn;
- request count = 1;
- exact max-output quantity when present;
- capture timestamp/perspective.

### Backend ingress wire evidence

Per attempt:

- same stable request/trace identity;
- B-leg/attempt/backend/model;
- A-leg/session correlation;
- request count/max-output bounds;
- source digest + exact rewrite plan/attempt digest for immutability/widening evidence.

No prompt body is copied into checkpoint storage.

### Counting

If accounting/preflight is disabled, these public wire checkpoints need no token count.

If accounting requires input-token count, introduce a typed `WireCounter`/equivalent capable of exact counting from proof/source/backend semantics. It may use a provider-native raw counting capability or a bounded streaming profile counter. **Bytes are never tokens.** If the configured economic authority has only `CountCall`, assessment declines before commit.

Stock billing/exposure logic receives exact bounded views where possible; arbitrary custom Call callbacks remain blockers.

---

## 15. Closing Remaining Full-Call Consumers

Before `OpenWire` can exist in production, maintain a checked inventory of every post-commit Call dependency.

Known categories:

- route selector/request-size/protocol requirements;
- capability negotiation;
- `recvTurnFacts` baselines;
- continuation support;
- interleaved-thinking recorders;
- frontend/backend metering checkpoints and widening checks;
- token accounting/preflight;
- billing identity/exposure/price/policy/max-output callbacks;
- terminal usage/session closures;
- secure recorder;
- traffic snapshots;
- response/debug helpers;
- stable identity helpers.

Every consumer is one of:

1. **bounded exact fact/view**;
2. **source/digest/rewrite contract**;
3. **response-only**;
4. **assessment blocker**.

No post-commit code receives a fake `lipapi.Call`. Architecture/AST tests enforce the inventory and disallow accidental `preparedRequest.call` use on the wire branch.

---

## 16. Frontend Response-State Bridge

Wire execution result:

```go
type ResponseFacts struct {
    CallID                 string
    TraceID                string
    StableSum              [32]byte
    ALegID                 string
    AuthoritativeSessionID string
    SessionID              string
    Operation              lipapi.Operation
    DeliveryMode           lipapi.DeliveryMode
    SessionCarrier         SessionResponseCarrier // sensitive field handling required
}

type ExecutionResult struct {
    Stream   lipapi.EventStream
    Response ResponseFacts
}
```

The exact field list is characterization-driven. Frontend-specific state remains local to the frontend/profile; core does not carry `createEncodeState` or provider types.

Each frontend has a wire response binding path or a refactored common encode context. No partial Call is synthesized.

### OpenAI Responses

- response ID/token/timestamp can use the exact stable semantic sum;
- cancellation carrier must include authoritative A-leg/session semantics exactly as today;
- session response headers use `SessionCarrier`.

### OpenAI Chat

- deterministic completion ID/timestamp use the exact stable sum;
- remaining encode options come from bounded frontend wire state.

### OpenResponses

- moved later because `AfterDecode` creates response/continuation state;
- first lane is explicit no-store only.

---

## 17. Retry/Failover/Race Integration

Wire provider open is a new body source for the **existing** B-leg/attempt/recovery owner, not a second retry engine.

Preserved:

- attempt budget/order;
- weighted-first/affinity/interleaved state;
- credential retry/cooldown;
- provider failure classification;
- first-visible-event commitment;
- no post-output failover;
- B-leg lifecycle/cleanup;
- canonical response event assembly;
- terminal/economic settlement.

Parallel attempts receive independent replay readers. Late route override can select only within the pre-certified domain.

---

## 18. Protocol Certification Order

### Lane 1: OpenAI Responses → OpenAI-compatible Responses

Reasons:

- high-value agentic workload;
- shared `frontendpipe` create path;
- no current create `AfterDecode` stateful hook comparable to OpenResponses;
- forces response/cancellation/session-carrier/identity parity early.

Canonical-only triggers include body-carried LIP session metadata (initially), duplicate protocol-owned keys, unknown fields dropped by canonical encode, repair-sensitive histories/aliases, and any unhandled normalization.

### Lane 2: OpenAI Chat Completions → OpenAI-compatible Chat

Add after Lane 1. Preserve deterministic completion identity from the canonical semantic digest.

### Lane 3: OpenResponses HTTP create → OpenResponses-compatible backend

Initial subset:

- HTTP create only;
- **explicit `store:false`**;
- no `previous_response_id`;
- no compaction;
- no WebSocket;
- bounded response-state bridge;
- no unresolved `AfterDecode` error/side effect.

Missing `store` is canonical because current decoder defaults it to true. `store:true`/continuation is a later certification only after reservation/recorder/cleanup/lineage parity exists.

Other frontends remain canonical until individually certified.

---

## 19. Gzip Follow-Up

Wave 1: gzip always canonical.

Later:

```text
compressed body
  → existing bounded decompression semantics
  → decoded identity replay source
  → shared scanner + decode admission + semantic proof + assessment
  → identity JSON provider body with stale Content-Encoding removed
```

All threshold/reservation limits are decoded bytes.

---

## 20. Failure / Ownership Matrix

| Stage | Decode permit held? | Turn begun? | Provider body committed? | Outcome |
|---|---:|---:|---:|---|
| body limit/shared JSON error | no | no | no | existing frontend error |
| mid-capture replay/spill decline | no | no | no | lossless stitched canonical continuation; existing path |
| decode admission reject | no permit acquired | no | no | existing 429/503 semantics |
| semantic profile decline | yes | no | no | canonical Decode under same permit |
| core assessment decline | yes | no | no | canonical Decode under same permit |
| canonical Decode error after decline | yes | no | no | existing decode error; permit releases normally |
| assessment eligible | yes → release | no | no | one-way wire commit |
| BeginTurn/policy/billing denial | no | yes/phase-specific | no | existing lifecycle/error semantics |
| actual route override | no | yes | no | must be inside pre-certified late-route domain |
| unexpected content dependency after commit | no | yes | no/possibly pre-open | invariant failure; finalize once, no canonical restart |
| provider failure pre-output | no | yes | maybe headers/body | existing retry/failover |
| failure after visible output | no | yes | yes | no new failover |
| client cancel | phase-dependent | phase-dependent | phase-dependent | close readers/source; existing cleanup |

---

## 21. Security and Privacy

- no upstream provider body before full request validation + assessment;
- decode QoS still governs expensive protocol proof;
- sensitive resume tokens are isolated from provider facts/telemetry;
- spool files are short-lived secrets and may contain plaintext prompts;
- no client Authorization/hop-by-hop header passthrough;
- extension/hook/non-plane authority summary fails closed;
- canonical semantic digest is treated as correlation data and does not expose request content directly;
- backend proof is explicit and provider knowledge remains outside core.

---

## 22. Observability and Performance Evidence

Bounded metrics:

- considered / assessed-eligible / wire / canonical counts;
- decline reason enum (`below_threshold`, `gzip`, `frontend_traffic`, `profile_decline`, `plane_blocker`, `hook_blocker`, `accounting_counter`, `route_domain`, `backend_wire`, `spool_budget`, etc.);
- body-size buckets;
- memory/file spill;
- replay/rewrite counts;
- capture/preflight/proof/assessment/provider-open latency;
- active logical spool bytes.

No backend/model/user/session IDs in metric labels and no body/path/resume token in telemetry.

Benchmarks compare disabled canonical vs wire for 32 KiB, 256 KiB, 1 MiB, 5 MiB, and test-only 20 MiB bodies. Measure allocs/B/op, CPU, GC/heap under concurrency, file I/O, replay, malformed/late metadata, giant strings, slow uploads, and spool saturation.

Measure the decode permit's additional hold across `AssessLargeBody`; it must remain bounded/pure and must never include client upload, DB/store access, spill I/O, or provider I/O.

Publish realistic eligibility for:

- standard secure-session recorder;
- wire-native metering with accounting disabled;
- accounting/billing enabled/disabled;
- empty vs occupied typed planes and hook chains;
- frontend/core traffic;
- route override under homogeneous same-wire vs heterogeneous backend generations;
- sequential/fallback/race selectors.

At least one normal secure-session + metering composition must hit wire execution before the feature is considered complete.

---

## 23. Architecture Ratchets

Tests/codegen fail if:

- a production typed plane is unclassified;
- a production hook/non-plane request authority is absent from wire eligibility inventory;
- provider names/types enter core/internal large-body contracts;
- V1 low-level replay/proof/assessment types are promoted to public SDK without explicit API/ABI review;
- protocol semantic proof runs outside decode admission;
- assessment performs turn/store/provider side effects;
- expected canonical fallback is added after wire commit;
- wire code fabricates a partial Call;
- a wire checkpoint clones/retains a full Call;
- a wire request uses a raw-body identity in place of exact canonical stable identity;
- a late route authority can select outside the pre-certified domain;
- route eligibility prunes/reorders candidates to retain wire mode;
- post-commit wire code acquires a new unclassified full-Call dependency.

---

## 24. Implementation Gate

No production profile advertises wire support until these are green:

1. canonical ingress/decode-admission/lifecycle/response/economic identity characterization;
2. replay source + mid-capture continuation + shared scanner differential/fuzz suite;
3. exact canonical identity-digest differential suite;
4. typed plane + hook + non-plane frozen eligibility summary/ratchets;
5. standard secure-session wire input/recorder/session-response-carrier parity;
6. wire-native metering checkpoint path;
7. accounting/billing disposition or exact wire contracts;
8. two-phase assessment tests proving declines occur under the same decode permit and before `BeginTurn`;
9. initial-route + late-route-domain backend proof tests;
10. complete post-commit Call-dependency closure;
11. first-lane canonical-vs-wire provider/frontend/retry/cancel conformance;
12. performance and realistic eligibility evidence.

The first release remains explicit opt-in/default-off.
