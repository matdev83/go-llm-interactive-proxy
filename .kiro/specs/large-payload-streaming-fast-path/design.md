# Design Document

## Overview

This feature adds an **optional, proof-gated large-request execution path** for already-supported JSON create operations. Eligible requests are captured into a bounded-memory replayable source, validated to EOF, admitted through the same decode-QoS authority as canonical protocol decode, and sent to an explicitly compatible backend without constructing the full canonical request object graph or re-encoding the request body. The existing canonical pipeline remains the oracle and fallback.

“Streaming fast path” deliberately does **not** mean forwarding client bytes to a provider while validation is still in progress. Required route metadata can legally appear late in JSON, malformed/over-limit requests currently fail before provider execution, and #503 requires relevant authorities to be resolved before any provider body byte is committed. V1 is therefore a **pre-commit replay/spool + low-allocation proof + replay** design.

This revision was cross-checked against `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533 landed.

## Brownfield Findings That Change the Original Draft

1. **Typed extension planes are now production architecture.** `pkg/lipsdk/feature/plane_manifest.go` and `FrozenPlaneSet` are live. Request-body access classification must extend that manifest/generator; no legacy named-field mirror is allowed.
2. **Decode admission is an authority.** `frontendpipe` performs shared JSON preflight, then `decodeqos.TryAdmit(..., len(body))`, and holds only protocol `Decode` under `decodeqos.Guard`. Protocol semantic proof cannot simply run during upload outside this guard.
3. **An EventStream-only alternate executor result is insufficient.** Frontends still use `*lipapi.Call` and/or `Decoded.Extra` after execution for IDs, cancellation carriers, timestamps, wrappers, and response state. Wire execution needs bounded response facts plus frontend-owned state; it must not fabricate a Call.
4. **The canonical Call is still consumed deep after identity.** Current runtime code uses `preparedRequest.call` for routing/capability derivation, billing, request-size/token estimation, `recvTurnFacts`, continuation support, interleaved-thinking recorders, and terminal/session fields. Route refactoring alone is insufficient.
5. **OpenResponses is not a simple first lane.** Its create decoder defaults `store=true`, and `AfterDecode` prepares response/continuation state. Absence of `previous_response_id` alone does not make it wire-safe.
6. **The original post-`BeginTurn` canonical-continuation split adds avoidable risk.** Re-entering canonical protocol decode/admission or frontend `AfterDecode` after turn side effects changes ordering and complicates exact-once lifecycle. V1 should instead make every *expected* eligibility decision before `BeginTurn` and use a one-way commit point.

## Goals

- Reduce large-request heap/GC pressure by avoiding full request-tree materialization and outbound re-marshal for certified requests.
- Preserve request limits, shared JSON safety, decode QoS, auth/session/B2BUA, routing/recovery, billing/accounting, guardrails, traffic behavior, and frontend response semantics.
- Preserve lossless canonical fallback after client bytes have been consumed **while still before turn/provider commitment**.
- Keep provider knowledge at frontend/backend edges and core ownership of orchestration.
- Fail closed on unknown extension/callback/route state.
- Avoid a second executor architecture and avoid a broad secure-session preparation split in V1.

## Non-Goals

- Native provider passthrough (#490).
- Sending bytes upstream before whole-request proof.
- Cross-protocol forwarding.
- Broad conversion of existing hooks/plugins to streaming request APIs.
- Changing request-size defaults or adding Brotli/zstd/deflate.
- Weakening billing/guardrail/traffic authorities.
- Fabricating partial Calls.
- Post-`BeginTurn` optimization fallback in V1. A later spec may add a formally proven continuation mechanism if eligibility would materially benefit.

---

## 1. Canonical Flow: Behavioral Oracle

The shared create path currently behaves, in simplified order, as follows:

```text
frontend handler
  ├─ method/path/auth/content-type checks
  └─ frontendpipe.ServeHTTP
       ├─ reqbody.ReadAll (gzip + MaxBytesReader)
       ├─ preliminary route selector extraction
       ├─ jsonguard.PreflightWithContext
       ├─ decodeqos.TryAdmit(weight = decoded body bytes)
       │    └─ decodeqos.Guard(Spec.Decode)
       ├─ authoritative session-header application
       ├─ Call.Validate
       ├─ Spec.AfterDecode (when present)
       ├─ Call.Validate again
       ├─ frontend TrafficPorts.Emit(full []byte)
       ├─ request/debug facts
       ├─ Executor.Execute(*lipapi.Call)
       │    ├─ identity/session/A-leg
       │    ├─ request authorities / conversation / transforms
       │    ├─ billing / route / attempts
       │    ├─ backend encode/open
       │    └─ canonical EventStream
       ├─ Spec.WrapStream
       ├─ BuildEncodeOpts(decoded Call/Extra)
       └─ response writer
```

Before production refactoring, tests freeze malformed/trailing JSON, exact body limits, gzip decoded limits, decode-admission decisions/release, session/A-leg lifecycle, request authority/traffic/billing ordering, route/failover/race behavior, and frontend response IDs/cancellation/`AfterDecode` state.

---

## 2. V1 Commit Model: All Expected Eligibility Before `BeginTurn`

V1 has one explicit optimization commit point:

```text
client bytes captured + validated
  ↓
protocol proof under decode admission
  ↓
frontend pre-core state proven safe
  ↓
core static/generation/callback eligibility
  ↓
compile selector and prove every reachable configured candidate wire-compatible
  ↓
all expected blockers resolved?
  ├─ no → Canonicalize + ordinary Executor.Execute (no BeginTurn yet)
  └─ yes → COMMIT TO WIRE EXECUTION
             ↓
          BeginTurn / A-leg / billing / route execution / provider open
             ↓
          no optimization fallback after this point
```

### Why this is safer

The original draft split secure preparation so a late blocker could canonicalize after identity. That creates three difficult parity problems:

- canonical `Spec.Decode` is normally governed by decode admission before executor entry;
- frontend `AfterDecode` may fail or perform stateful work before executor entry;
- a second ordinary `Execute` would duplicate `BeginTurn`/A-leg/accounting unless a large preparation refactor is perfect.

V1 avoids all three. Any authority that can only reveal whether the request is wire-safe after a turn starts causes a **conservative pre-commit blocker** for that configuration/profile.

Examples:

- a configured `RouteOverrideReader` is a V1 blocker because its authoritative state is keyed by A-leg and read after A-leg fetch;
- content-bearing conversation projection/local-turn/request-transform stages are blockers through the typed access summary;
- a Call-shaped custom billing/policy/token estimator without an exact wire contract is a blocker;
- route strategies whose possible candidates cannot be conservatively proven from the pre-turn selector are blockers.

Weighted-first/affinity state may select a subset after BeginTurn; V1 proves compatibility for the conservative **superset of every candidate reachable from the configured selector**, so later selection does not require fallback.

After the commit point, ordinary policy denials/provider errors are execution outcomes, not optimization fallback. An unexpected invariant showing that canonical content is suddenly required aborts/finalizes the one turn and is treated as an internal parity bug.

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
- positive threshold/memory/budget; memory spool <= threshold;
- explicit spool directory validated during candidate config/reload; empty resolves to `os.TempDir()`;
- invalid reload retains the last-good generation;
- the spool budget is an **optimization resource budget**, not total process memory admission. Exhaustion selects the old canonical path, which may allocate according to existing behavior.

---

## 4. Provider-Neutral Replay Source

Public SDK contract (illustrative names):

```go
package requestbody

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

Invariants:

- immutable after capture;
- each `Open` returns an independent offset-zero reader;
- multiple readers may coexist for race/parallel attempts;
- root `Close` is idempotent and non-blocking with respect to active readers; deletion happens once root is closed and readers reach zero;
- no per-chunk or cleanup goroutines;
- source is owned by frontend until handed to `ExecuteLargeBody`; after entry, core owns it through terminal replay need.

### Capture

```text
client body
  ├─ fixed reusable copy buffer
  ├─ <= memory_spool_bytes → bounded memory prefix
  └─ spill → private CreateTemp file + append
```

Known identity-body size may reserve logical spool bytes up front within the existing request cap. Unknown/chunked capture reserves incrementally. Every consumed byte remains recoverable for canonical fallback.

### Confidentiality

- unpredictable `lip-large-body-*` names only;
- owner-only temp-file permissions where supported;
- no user/model/session data in filenames;
- no spool path/body content in ordinary logs/traces/metrics;
- docs explicitly warn that spool files can contain plaintext prompts and recommend protected local storage.

---

## 5. Shared Streaming JSON Preflight

Extend/reuse one provider-neutral scanner in the shared JSON-shape layer. It incrementally validates:

- UTF-8 and escapes/surrogate pairs across buffer boundaries;
- JSON number grammar;
- nesting/delimiters/root/trailing data;
- shared token/depth/object/array/key/string/number limits;
- cancellation checkpoints.

It retains no ordinary prompt scalar contents. Selected-token observation can expose bounded decoded values and exact raw offsets, but provider field semantics live in frontend profiles.

The existing slice preflight remains the differential oracle. Fuzz/differential suites compare pass/fail category and aggregate counts.

---

## 6. Preserve Decode Admission: Two-Pass Proof

Protocol semantic proof must not run uncontrolled during upload.

```text
capture to EOF + shared streaming shape preflight
  ↓
final decoded size known
  ↓
decodeqos.TryAdmit(weight = final decoded bytes)
  ├─ reject → same current admission response
  ↓ permit held
reopen Source
  ↓
frontend protocol semantic verifier
  ├─ decline → materialize and run current Spec.Decode under the same admission contract
  └─ proven → finish verifier
  ↓
release exactly once
```

The permit is **not** held while waiting for client upload. This preserves current DoS/concurrency characteristics. The second source read trades I/O for low heap and semantic correctness.

For a declined candidate that becomes canonical at this stage, canonical post-decode validation/`AfterDecode` then proceeds in its normal pre-executor position.

---

## 7. Frontend Wire Profile

A profile proves only protocol/frontend facts. It does not select backends or perform network I/O.

Illustrative bounded core metadata:

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
    MaxOutputTokens      *int64 // exact only
}
```

Forbidden in core metadata: raw HTTP headers, credentials, temp paths, provider SDK objects, arbitrary maps, prompt/tool contents, and unbounded user metadata.

Profiles support arbitrary field order. Metadata overflow, unknown normalization, ambiguous route/model, or duplicate protocol-owned members that canonical decode/re-encode could collapse cause canonical fallback.

### Frontend pre-core state

Some response behavior is frontend-specific and must stay at the driving adapter. `frontendpipe` may carry a bounded request-local `WireFrontendState` internally. It never crosses SDK/core.

A profile is eligible only if all canonical frontend validation/error-producing work required before `Executor.Execute` has either:

- been exactly proven/reproduced before `ExecuteLargeBody`, or
- been shown irrelevant for that certified subset.

A profile with unresolved stateful/erroring `AfterDecode` behavior remains canonical-only.

---

## 8. Canonical Fallback Callback

Before the V1 commit point, core may decline based on generation/callback/route/backend facts. `ExecutionRequest` therefore carries a trusted callback:

```go
type CanonicalizeFunc func(context.Context) (*lipapi.Call, error)
```

The frontend owns a request-local canonical state holder. `Canonicalize` reopens/materializes the replay source, invokes the existing decoder/validators/`AfterDecode` in the normal pre-executor context, records canonical `Decoded`/Extra state locally, and returns only the fully valid Call to core.

Because V1 calls this callback **before `BeginTurn`**, core can then call ordinary `Execute` without duplicate lifecycle or decode-admission reordering.

If protocol proof had already succeeded and a later core blocker sends the request canonical, `Canonicalize` still uses normal canonical decoding as the behavioral oracle; expected fallback is correctness, not an error.

---

## 9. Large-Body Execution Contract and Response Facts

An EventStream-only result cannot preserve current frontend response behavior. The additive optional executor seam returns bounded response facts:

```go
type ExecutionPath uint8
const (
    PathUnknown ExecutionPath = iota
    PathWire
    PathCanonicalFallback
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

type ExecutionRequest struct {
    Metadata     Metadata
    Source       Source
    Canonicalize CanonicalizeFunc
}

type LargeBodyExecutorView interface {
    ExecuteLargeBody(context.Context, ExecutionRequest) (ExecutionResult, error)
}
```

`lipsdk.ExecutorView.Execute` is unchanged. Frontends type-assert the optional seam; absence means canonical-only.

Exact `ResponseFacts` fields are determined by characterization. No request content is added merely for convenience.

---

## 10. Frontend Response-State Bridge

Current response code consumes data that disappears when the canonical Call is skipped. The wire lane therefore needs frontend-owned response binding callbacks or a refactor to a common response encode context.

Examples that must be covered:

- OpenAI Responses: response ID/cancellation carrier, authoritative A-leg/session identity, timestamp, encode options;
- OpenAI Chat: completion ID and timestamp;
- OpenResponses: `createEncodeState`, response ID, continuation/store observer state, allowed-tool/options behavior;
- stream/debug helpers that currently take Call fields.

Invariants:

- do not fabricate a partial Call;
- do not put frontend/provider state into core;
- canonical path remains source-compatible;
- wire response facts are bounded/content-free;
- optimized requests retain cancellation/correlation/continuation semantics;
- protocol-opaque IDs/timestamps may differ only when the profile explicitly documents that the protocol permits it and conformance normalizes only those fields.

Conceptually `frontendpipe.Spec` gains optional wire callbacks such as `PrepareWireState`, `WrapWireStream`, and `BuildWireEncodeOpts`, or existing writers are refactored to a common bounded encode context. Exact API shape is implementation detail; the dependency inventory is mandatory.

---

## 11. Typed Extension-Plane Access Summary

Extend the canonical `feature.Plane[T]` declaration/generator with explicit request-body access metadata:

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

- zero/unclassified is rejected for production manifest declarations by validation/codegen/arch tests;
- runtime unknown still fails closed to canonical-required;
- generated frozen storage computes a bounded `AccessSummary` without request-path maps/reflection/type assertions;
- occupied content-reading/mutating planes block;
- response-only planes can remain active because provider responses still become canonical events;
- session/workspace stages are metadata-only only when their typed contract proves no request-content need.

The implementation targets only the current manifest/FrozenPlaneSet architecture. There is no conditional legacy branch.

---

## 12. Close Every Post-Identity Full-Call Dependency

Before `ExecuteLargeBody` can commit to wire execution, inventory every production read of:

- `preparedRequest.call`;
- `identity.ingressCall` / canonical baseline snapshots;
- Call-typed billing/policy/token/context callbacks;
- Call-derived terminal/response helpers used after the intended wire commit.

Known examples already include:

- route selector/request-size/failover requirements;
- billing account/pricing/policy/max-output/token estimation;
- `recvTurnFacts` baseline/ingress state;
- continuation-support calculation;
- interleaved-thinking recorder setup;
- terminal usage/session identifiers.

Each dependency becomes one of:

1. **pre-commit blocker** — exact semantics require canonical content;
2. **exact bounded fact** — canonical and wire branches share a typed helper/value;
3. **response-only** — covered by the response bridge.

No wire branch may dereference `prep.call`; a ratchet test enforces that after the closure task.

### Stock billing specifically

Do not assume billing is “custom” and simply block it without measuring usefulness. The stock composition is explicitly evaluated:

- standard account identity is already principal-context based;
- pricing/policy/max-output functions are inspected for exact fact requirements;
- raw body bytes are never substituted for request tokens;
- if stock billing can use exact bounded facts, add a typed wire admission path and have canonical billing derive the same input from Call;
- arbitrary custom Call callbacks stay canonical-only.

The eligibility matrix reports whether billing-enabled standard composition can reach wire execution.

---

## 13. Pre-Commit Core Eligibility

`ExecuteLargeBody` performs only non-side-effecting proof before its wire commit point:

1. generation-pinned AccessSummary;
2. core traffic/raw-capture/redaction blockers;
3. presence of route override authority or other post-A-leg content/selector authority — V1 blocker unless it has an explicit pre-turn wire-safe contract;
4. Call-dependent billing/policy/token/context callbacks lacking typed wire support;
5. compile selector from exact metadata using existing aliases/defaults;
6. validate execution composition;
7. bind candidate-native models using generation-pinned resolver where possible;
8. build the conservative reachable candidate **superset** independent of weighted-first/affinity state;
9. ask every candidate backend for exact wire support, rewrite support, and parallel-reader requirements.

Any decline invokes `Canonicalize`, then ordinary `Execute`, still before `BeginTurn`.

Only after all checks pass does core begin the normal secure identity/A-leg lifecycle. From that point the request is committed to the wire execution use case; no expected canonical fallback remains.

---

## 14. Routing Facts Without a Fake Call

Refactor the parts of route-plan construction that are truly metadata-only into explicit-fact helpers:

- selector compilation/aliases/default backend;
- execution-composition validation;
- native-model binding;
- failover requirements from exact `ProtocolRequirements`;
- request-size estimate only if an exact estimator contract exists.

After BeginTurn, existing weighted-first/affinity/interleaved/session state can select/order candidates from the already-proven compatible superset. This does not require re-proving compatibility.

The canonical path should use the same helpers where practical, with facts derived from the real Call, to prevent drift.

---

## 15. Backend Wire Contract

Internal backends gain optional driven-adapter fields, conceptually:

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
    Compatible           bool
    NativeModel          string
    NeedsModelRewrite    bool
    SupportsParallelOpen bool
    Reason               string // bounded static reason
}

type WireAttempt struct {
    Facts   WireRequestFacts
    Source  requestbody.Source
    Rewrite *requestbody.RewritePlan
}
```

`ResolveWireRequest` performs no network I/O. `OpenWire` reuses existing backend endpoint/credentials/client/response parser/failure classification. Nil support means canonical-only. Core never switches on provider name.

Every candidate in the conservative pre-turn reachable superset is checked before wire commit. The system never prunes/reorders candidates to retain eligibility.

---

## 16. Token-Aware Model Rewrite

The scanner records the exact raw span of the selected top-level model JSON token. Per candidate:

```text
source[0:model.start]
+ json.Marshal(nativeModel)
+ source[model.end:size]
```

The splice reader streams these parts without constructing another full body. Rewritten size uses checked `int64` arithmetic. Duplicate/ambiguous/repaired model forms are canonical-only.

Each provider attempt/credential retry opens a fresh source reader and creates its own rewrite reader.

---

## 17. Retry/Failover/Race Integration

Wire opening is integrated into the existing B-leg/attempt/recovery owner, preserving:

- attempt budget and candidate order;
- affinity/weighted-first/interleaved state;
- credential/provider failure classification;
- pre-output-only retry/failover;
- B-leg allocation/cleanup;
- response parsing to canonical events;
- terminal/accounting ownership.

Parallel/race attempts use independent offset-zero readers. Since compatibility for the full candidate superset was proven pre-turn, later route-state selection cannot reveal an incompatible candidate.

---

## 18. Protocol Certification Order

The audit changes the original order.

### Lane 1 — OpenAI Responses → OpenAI-compatible Responses

High relevance to agentic workloads and no current create `AfterDecode` side-effect stage in shared pipe. Certification must resolve response/cancellation facts.

Canonical-only triggers include body-carried proxy/session metadata, repair-sensitive aliases, malformed tool/function history normalized by canonical decode, duplicate protocol-owned names, unknown fields discarded by canonical encode, and any unresolved response-state dependency.

### Lane 2 — OpenAI Chat Completions → OpenAI-compatible Chat

Add only after Responses conformance is green. Resolve completion ID/timestamp without a fake Call. Normalize protocol-opaque ID differences only if explicitly justified.

### Lane 3 — OpenResponses HTTP create → OpenResponses-compatible backend

Moved later because current `AfterDecode` owns extra state. Initial certification requires at minimum:

- HTTP create only;
- **explicit `store:false`** — absence is not eligible because canonical default is true;
- no `previous_response_id`;
- no compaction or WebSocket;
- bounded response-state bridge for response ID/options/wrappers;
- no unresolved `AfterDecode` side effect/error.

`store:true`/continuation is a later certification only after reservation/recorder/cleanup/lineage parity exists.

Anthropic/Gemini/etc. remain canonical until individually certified.

---

## 19. Compression Follow-Up

Wave 1: gzip always canonical.

Later wave may capture the **decoded** identity JSON stream using existing decompression/decoded-limit semantics. Compressed `Content-Length` is never used as decoded threshold/reservation proof. Outbound wire request removes stale content encoding.

---

## 20. Observability and Performance

Bounded fallback reasons include disabled, below-threshold, gzip, frontend-traffic, decode-admission, profile-decline, plane-blocker, route-override-authority, Call-dependency, route/backend incompatibility, and spool-budget.

Record body-size buckets, memory/file spool, replay/rewrite counts, capture/preflight/protocol-proof/provider-open latency, and active logical spool reservation. Never label metrics with model/backend/user/session IDs and never log body/spool paths.

Benchmarks compare disabled canonical vs wire for 32 KiB, 256 KiB, 1 MiB, 5 MiB and a test-only 20 MiB case. Measure allocs/B/op, CPU, GC/heap under concurrency, file I/O, retry replay, malformed/late-field/giant-string workloads, and budget saturation.

Interpretation must be explicit: full client request receipt/validation is still required before provider open, so the primary expected win is heap/GC and redundant canonical decode/re-encode work, not a magical early TTFT reduction.

A realistic eligibility matrix is mandatory. The feature is incomplete if stock composition makes every practical request fall back.

---

## 21. Failure Matrix

| Failure/decline | BeginTurn happened? | Provider body committed? | Outcome |
|---|---:|---:|---|
| body too large | no | no | existing request-too-large mapping |
| shared JSON invalid | no | no | existing JSON error mapping |
| decode admission saturated | no | no | existing 429/503 + Retry-After semantics |
| profile declines | no | no | canonical decode/path |
| spool budget exhausted | no | no | canonical path; metric only |
| frontend/core/plane/Call dependency blocker | no | no | Canonicalize + ordinary Execute |
| route/backend superset incompatible | no | no | Canonicalize + ordinary Execute |
| unexpected wire invariant after commit | yes | no/possibly provider not yet opened | abort/finalize once; internal parity error, **no second Execute** |
| provider open failure pre-output | yes | maybe | existing retry/failover |
| failure after client-visible output | yes | yes | no new failover; existing terminal behavior |
| client cancel | phase-dependent | phase-dependent | close readers/source; existing lifecycle cleanup |

---

## 22. Architecture Ratchets

Tests/codegen fail if:

- a production extension plane is unclassified;
- a wire branch uses a fake/partial Call;
- a post-commit wire path dereferences `preparedRequest.call` after dependency closure;
- provider names/types leak into core/SDK contracts;
- frontend semantic proof bypasses decode admission;
- a profile has unresolved `AfterDecode`/response-state dependencies;
- route eligibility drops/reorders an incompatible candidate;
- an expected eligibility decline is added after the V1 BeginTurn commit point.

---

## 23. Implementation Gate

No production profile advertises wire compatibility until these proofs are green:

1. canonical ingress/decode-QoS/lifecycle/response characterization;
2. replay source and scanner differential/fuzz tests;
3. typed-plane access classification ratchet on current manifest architecture;
4. complete post-identity Call dependency inventory/classification;
5. frontend response-state bridge for the first lane;
6. pre-`BeginTurn` eligibility/commit-point tests proving fallback has no turn side effects;
7. route-superset/backend proof tests;
8. first-lane canonical-vs-wire end-to-end conformance including retry/cancel;
9. performance and realistic eligibility evidence.

The first release remains explicit opt-in/default-off.
