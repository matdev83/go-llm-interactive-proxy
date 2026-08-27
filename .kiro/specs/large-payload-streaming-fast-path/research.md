# Research & Design Decisions

## Summary

- **Feature**: Large-Payload Streaming Fast Path (#503)
- **Initial discovery snapshot**: Go-LIP around `8684666809f687ab43dc1393dc1f726a0b0161f7`, plus Bifrost large-payload research.
- **Primary revalidation snapshot**: Go-LIP `main` at `40168ce1f3890a1c86c22e898be9d264d63ccd72` after PR #533.
- **Result**: the core pre-commit replay/spool idea is sound, but implementation readiness required multiple brownfield repairs. The final V1 is a bounded-memory replay path with a two-phase **side-effect-free plan → one-way execute** seam. Canonical fallback stays frontend-owned and occurs before `BeginTurn`.

## Final Decisions

1. Keep bounded-memory pre-commit capture/replay rather than immediate client→provider piping.
2. Preserve the complete existing request-size/shared-JSON safety envelope before provider commitment.
3. Preserve decode QoS with one admission grant: protocol proof and the final bounded wire-plan decision happen before the grant is released; a decline runs the ordinary canonical decoder under that same grant.
4. Keep canonical fallback at the frontend. Core does not call a `Canonicalize` callback after the decode permit has been released.
5. Use a side-effect-free core `PlanLargeBody` operation followed by a one-way `ExecuteLargeBody` only for accepted plans.
6. Use current typed extension-plane declarations/FrozenPlaneSet as the only request-body access-classification architecture.
7. Never fabricate a partial `lipapi.Call`.
8. Inventory **all** Call dependencies at/after the wire commit, including secure-session preparation, not only post-identity `preparedRequest.call` uses.
9. Introduce bounded normalized session/identity facts and a shared fact-only BeginTurn/A-leg preparation primitive; this is not a late-fallback split.
10. Keep the low-level replay/planning seam internal in V1; do not widen `pkg/lipsdk` merely for an internal optimization.
11. Preserve frontend response/cancellation/session-carrier behavior through bounded response facts plus frontend-owned state.
12. Prove wire compatibility for the conservative superset of every candidate the selector may later use; never prune/reorder to retain optimization.
13. Roll out OpenAI Responses first, OpenAI Chat second, OpenResponses explicit `store:false` third.
14. Keep gzip canonical in wave 1.
15. Make mid-capture budget/file failures losslessly continuable into the canonical reader; never discard the current partially written chunk.

---

## 1. Actual LLM Ingress Path

### Sources

- `internal/plugins/frontends/frontendpipe/pipe.go`
- `internal/plugins/frontends/reqbody/body.go`
- `internal/plugins/frontends/decodeqos/*`
- `internal/plugins/frontends/jsonguard/*`
- `internal/core/jsonshape/*`

### Findings

The issue's early pointer to `internal/jsonbody` is not the LLM hot path. Shared create frontends currently:

1. fully read/decompress with `reqbody.ReadAll`;
2. apply the effective body cap to decoded gzip bytes;
3. run shared JSON preflight;
4. acquire byte-weighted decode admission;
5. run protocol `Spec.Decode` under `decodeqos.Guard`;
6. apply authoritative session headers, validate, and run `AfterDecode`;
7. emit frontend ingress traffic with complete bytes;
8. invoke the canonical executor.

The optimization therefore belongs around `frontendpipe` request capture plus explicit core/backend contracts. It does not justify changing admin `internal/jsonbody` behavior.

---

## 2. Why Immediate Client→Provider Streaming Is Unsafe

Required metadata can occur late in legal JSON. Go-LIP also rejects malformed/over-limit/protocol-invalid requests and resolves important authorities before provider work.

So V1 still consumes and validates the complete request before provider body commitment. The performance target is:

- bounded request-body heap;
- no giant canonical request graph on the success lane;
- no full outbound re-marshal;
- lower GC pressure under many multi-MiB sessions;
- replayable retry/failover.

It is not an early-upload TTFT feature.

---

## 3. Bifrost: Useful Concepts, Not a Drop-In Architecture

Useful ideas:

- threshold before expensive typed parsing;
- replayable raw body;
- bounded metadata skeleton;
- surgical model replacement;
- decompression as an independent layer;
- explicit backend raw-body support.

Unsafe to copy directly:

- skipping parser/hooks merely because a body is large;
- substring/prefix model replacement;
- assuming same protocol/provider name proves wire equivalence;
- bypassing canonical session/accounting/guardrail behavior.

Go-LIP has richer canonical normalization, extension planes, secure sessions, B2BUA state, billing, conversation projection, and response semantics.

---

## 4. Shared Streaming JSON Scanner

Current slice preflight remains the behavioral oracle. `encoding/json.Decoder.Token` is not a sufficient large-scalar strategy because decoded string tokens may allocate proportional to the payload.

The new scanner should incrementally validate JSON lexical/shape limits while retaining only bounded selected values and exact byte spans. It must handle arbitrary member ordering and late model fields. Fuzz/differential testing against the current preflight is mandatory.

---

## 5. Decode QoS: The Final Fallback Ownership Correction

### Current contract

`frontendpipe` does:

```text
shared preflight
  → decodeqos.TryAdmit(weight = decoded bytes)
  → decodeqos.Guard(Spec.Decode)
  → release
  → AfterDecode / executor
```

The permit is intentionally not held during client upload.

### First review repair

Protocol semantic proof must not run during upload outside this authority. A wire candidate therefore uses a second replay pass under decode admission.

### Remaining collision found during final cross-check

If semantic proof succeeds, the previous revised design released the permit and then called core `ExecuteLargeBody`. A later core route/backend/plane decline could invoke a frontend `Canonicalize` callback. That creates only two choices, both wrong:

1. run the ordinary protocol decoder outside decode admission; or
2. call `TryAdmit` a second time and allow optimization consideration itself to create a new saturation rejection that the canonical path would not have seen.

### Final decision: plan before releasing the single permit

```text
capture + shared preflight to EOF
  ↓
TryAdmit(exact decoded bytes)
  ↓ permit held
protocol semantic proof
  ↓
side-effect-free core PlanLargeBody
  ├─ decline → ordinary Spec.Decode under SAME permit
  │            → release → ordinary AfterDecode/executor
  └─ accept  → freeze plan → release
                            → ExecuteLargeBody(plan, source)
```

`PlanLargeBody` is deliberately constrained while the permit is held: no provider/network I/O, DB/store/session/A-leg reads, client-body waiting, filesystem spill work, or arbitrary unbounded plugin callbacks. Static/generation/selector/backend declaration checks that cannot satisfy this bound are earlier canonical blockers.

This slightly extends permit hold time by a bounded planning phase, so capacity/latency must be measured. It is preferable to an unguarded canonical decode or a second admission race and is mechanically testable.

---

## 6. Typed Extension-Plane Architecture Drift

PR #533 landed the typed `feature.Plane[T]`/manifest/FrozenPlaneSet path. The earlier conditional “typed manifest vs legacy named fields” design is obsolete.

Request-body access semantics belong on the canonical plane declaration/generator. Every production plane receives an explicit access class. `Unclassified` fails declaration/codegen/architecture validation; runtime unknown additionally fails closed.

Do not create another manually synchronized classification mirror beside the manifest.

---

## 7. Keep the Low-Level Fast-Path Seam Internal in V1

`pkg/lipsdk/executor_view.go` explicitly describes `ExecutorView` as a narrow supported module-boundary seam and says to widen only when justified.

All first-wave consumers of replay `Source`, wire planning, rewrite plan, and backend raw open are built-in internal packages (`frontendpipe`, runtime, internal backends). No external plugin needs to construct these objects for #503.

Therefore V1 should keep the low-level contract under an internal provider-neutral package and type-assert the concrete executor for optional support. Existing external/manual executors remain canonical-only and source-compatible. If a future external frontend/backend needs the feature, promote a stabilized SDK contract in a separate API/ABI review.

This avoids turning a performance implementation detail into permanent public surface by accident.

---

## 8. Frontend Response State Is More Than an EventStream

Current frontend response construction still consumes canonical Call/decoded state after execution.

Examples:

- OpenAI Responses: response ID, cancellation carrier, timestamp, model, response session headers;
- OpenAI Chat: completion ID/timestamp/model/session response headers;
- OpenResponses: `createEncodeState`, response ID, continuation/store observer state, wrappers and options.

A wire execution result therefore needs bounded provider-neutral response facts. Frontend-specific state remains in the driving adapter. No partial Call is fabricated for old writer signatures.

Protocol-opaque IDs/timestamps may differ only when the protocol allows it and differential conformance explicitly documents the normalization. Cancellation/correlation/session semantics remain exact.

---

## 9. Session Carriers Were Missing From the Earlier Metadata Sketch

`frontendpipe` currently calls `sessionwire.ApplyAuthoritativeHeadersNamed` after decode. `sessionwire` also writes session response carriers from canonical Call state. `lipapi.SessionRef.ResumeToken` is bearer authority and is explicitly forbidden from backend forwarding/persistent raw adapter storage.

A wire request that ignores these carriers would change secure-session resume semantics even if the JSON body itself were perfectly forwarded.

### Decision

Profiles lift only the normalized bounded carriers needed by core, using the same configured header names and precedence as the canonical path. Raw `http.Header` never crosses the boundary.

Initial OpenAI profiles keep body-carried proxy/session metadata canonical-only; header-derived authoritative session ID/resume authority may be supported through exact facts.

Sensitive session bearer facts:

- are never logged/metric-labelled;
- are never forwarded to providers;
- are carried only as long as needed for secure-session authority;
- are included in response facts only if the canonical frontend actually needs them to emit equivalent response carriers.

---

## 10. Secure-Session Preparation Still Requires a Real Call Today

### Brownfield evidence

`prepareIdentity` dispatches into `prepareSubmitAndALegSecure`/detached preparation with `*lipapi.Call`.

The secure path reads Call fields to:

- derive trace/call identity;
- construct `session.SessionView` and `SecureSession.BeginTurn` input;
- apply authoritative session/resume/client-session facts;
- fetch the A-leg and snapshot route authority;
- then run content-dependent guard/metering/submit/traffic/conversation work.

The previous revised spec correctly blocked the content-dependent stages, but saying “after commit begin one normal logical turn” was still incomplete: current normal preparation cannot be invoked without the canonical Call that the optimization deliberately avoids.

### Final decision

After all expected blockers have been planned away, extract the smallest **fact-only BeginTurn/A-leg primitive** shared by canonical and wire execution. It owns principal/scope, session-open stage, workspace resolution, secure BeginTurn, A-leg fetch, and identity outputs. The canonical branch still executes its existing content stages in the same order after this shared primitive.

This is not the original risky late-fallback split:

- no canonical re-entry is allowed after BeginTurn;
- no wire request starts a turn until planning is accepted;
- characterization tests freeze canonical ordering/error/lifecycle before extraction.

Detached execution stays canonical-only in V1 unless its own Call dependencies are separately factored and tested.

---

## 11. Deep Call Dependencies Beyond Identity

Known examples remain:

- route selector/request-size/failover requirements;
- billing identity, policy/pricing/max-output and token estimation;
- `recvTurnFacts` baselines;
- continuation-support calculation;
- interleaved-thinking recorder setup;
- terminal usage/session fields.

The dependency inventory must start at the **wire commit point**, not “post-identity.” Every use becomes:

1. pre-plan blocker;
2. exact bounded fact shared with canonical helpers; or
3. response-only frontend behavior.

A ratchet prevents new wire/post-commit functions from silently reaching back to `preparedRequest.call`/`identity.ingressCall` or another Call-shaped callback.

Stock billing must be evaluated explicitly rather than dismissed as custom. If exact facts suffice, canonical and wire paths should share the same typed billing input. Otherwise billing-on remains a documented fallback configuration.

---

## 12. OpenResponses Default `store=true`

Current `internal/plugins/frontends/openresponses/decode.go` initializes `store := true`. Therefore absence of `previous_response_id` does not make a create request stateless.

Initial OpenResponses certification requires:

- HTTP create;
- **explicit `store:false`**;
- absent `previous_response_id`;
- no compaction;
- no WebSocket;
- bounded response-state bridge;
- proof that no `AfterDecode` error/side effect was moved after BeginTurn.

`store:true`/continuation is a separate later certification.

---

## 13. Backend Compatibility and Candidate Superset

Same protocol name is insufficient. Planning checks every candidate in the conservative selector superset before wire commit. Later weighted-first/affinity/interleaved state may reorder/select only inside that already-proven set.

Backend compatibility resolution is pure/config-derived during planning. It performs no provider network I/O and declares exact profile/operation/delivery/requirements/rewrite/parallel-reader support.

The optimization never drops an incompatible fallback/race candidate.

---

## 14. Model Rewrite

The scanner records the exact top-level model token span. Per candidate, emit:

```text
prefix + json.Marshal(nativeModel) + suffix
```

No regex, substring scan, prefix search, or full output buffer. Every retry/credential attempt opens a fresh replay reader.

---

## 15. Mid-Capture Recovery Is a Separate Required Primitive

Spool-budget exhaustion or a file-create/write failure can happen after network bytes have already been consumed. “Fallback to canonical” is not enough as a design statement.

The capture implementation must retain the current chunk until its destination write succeeds. On partial write, it knows exactly which prefix is durable and which suffix remains in memory.

A canonical-continuation reader then presents:

```text
successfully retained memory/file prefix
+ current unwritten suffix
+ unread client body
```

under the existing request limit, exactly once. It may allocate the normal canonical full body because the optimization has already been abandoned.

Tests inject short writes, reservation failure between chunks, file-create failure, file-write partial success, cancellation, and exact-limit boundaries. No restart/re-read of the client socket is permitted.

---

## 16. Spool Resource Semantics and Privacy

The global spool byte budget bounds optimization-owned replay storage, not total process memory. Budget exhaustion can intentionally fall back to the old heap-materializing path, so operators must not treat it as a global OOM guard.

`Source.Close` is idempotent/nonblocking with respect to active readers. Root close marks deletion pending; the last tracked reader performs final cleanup. This avoids Windows open-file deletion problems and avoids a cleanup goroutine.

Spool files may contain plaintext prompt/tool data. Documentation covers protected storage, short lifetime, telemetry redaction, cancellation cleanup, and threat-model implications.

---

## 17. Conservative Protocol Normalization Policy

Profiles must decline request shapes whose raw forwarding can differ from canonical decode→encode, including:

- unknown fields discarded by canonical encoders;
- legacy aliases;
- malformed histories repaired/skipped;
- duplicate protocol-owned keys;
- body-carried session/proxy metadata not explicitly parity-certified.

OpenResponses keeps strict duplicate behavior. Profile broadening requires differential evidence in the same change.

---

## 18. Performance Validation

Required sizes: 32 KiB, 256 KiB, 1 MiB, 5 MiB and test-only 20 MiB.

Measure:

- allocs/op and B/op;
- CPU;
- GC/heap under concurrency;
- capture/shared-preflight/protocol-proof/**wire-plan**/provider-open latency;
- decode-admission occupancy/capacity;
- temp-file I/O;
- retry replay;
- malformed/late-field/giant-string workloads;
- spool-budget saturation and mid-capture fallback.

Publish a realistic eligibility matrix including extensions, frontend traffic, route override, session resume, stock billing and route strategy. A feature that compiles but always falls back under normal composition is incomplete.

---

## 19. Recommended Implementation Order

1. rebase/revalidate and freeze canonical ingress/decode/session/response behavior;
2. replay source + explicit mid-capture canonical continuation;
3. shared streaming JSON scanner;
4. typed-plane access classification;
5. internal large-body contracts and frontend wire state;
6. protocol proof under decode admission;
7. bounded side-effect-free `PlanLargeBody` under the same admission grant;
8. fact-only secure-session/A-leg primitive + complete Call-dependency closure;
9. backend proof/model rewrite/replay attempt integration;
10. frontend response/session-carrier bridge;
11. OpenAI Responses lane;
12. OpenAI Chat lane;
13. OpenResponses explicit-no-store lane;
14. gzip follow-up;
15. default-off rollout with allocation/load/eligibility evidence.

This ordering isolates each regression-sensitive seam and avoids a simultaneous repo-wide refactor.
