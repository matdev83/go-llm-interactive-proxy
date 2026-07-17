# Current-State Review and Requirements Gap Analysis: Reasoning Output Preservation

Generated: 2026-07-17T08:52:15+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `cda1c3f02ef6bbb7eec7c314c9469275f309fcc5`
- Source issue: [#157 — Reasoning output preservation](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)
- Initial requirements source: the first revision of `.kiro/specs/reasoning-output-preservation/requirements.md`
- Final requirements source: the gap-remediated `.kiro/specs/reasoning-output-preservation/requirements.md`
- Review mode: static source, contract, steering, archived-spec, and issue-plan review through the connected GitHub repository.
- Scope: brownfield gap analysis before design. No production code is changed by this specification PR.

## Executive Assessment

The repository already carries response-side reasoning through the canonical event stream and has strong routing, failover, secure-session, feature-plugin, and adapter boundaries. It does **not** yet have the request-side representation or lifecycle seams required to restore historical reasoning safely.

The feature cannot be implemented correctly as a narrow response hook plus an early request transform:

- the early request transform does not know the selected backend/model;
- the candidate-aware request-part hook executes after initial capability, context, and accounting decisions;
- the response-part hook can see events but has no exactly-once stream outcome lifecycle;
- completion gates buffer the whole response and are incompatible with a streaming-first observer;
- the canonical request has no reasoning part;
- the current reasoning capability is soft-downgradable;
- adapter request decoders do not round-trip the reasoning shapes that the response encoders already partially expose.

The correct brownfield direction is to add two **generic** extension seams—candidate-aware attempt transforms and final-canonical-stream observers—then implement preservation as an official feature plugin over those seams. Shared historical reasoning semantics belong in `pkg/lipapi`; provider wire details remain in adapters.

## Reviewed Steering and Specification Assets

### Repository rules and Kiro workflow

- `AGENTS.md`
- `.kiro/AGENTS.md`
- `.kiro/steering/api-standards.md`
- `.kiro/steering/routing-and-orchestration.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/testing.md`
- `.kiro/settings/templates/specs/requirements.md`
- `.kiro/settings/templates/specs/design.md`
- `.kiro/settings/templates/specs/tasks.md`

### Canonical request and event contracts

- `pkg/lipapi/call.go`
- `pkg/lipapi/parts.go`
- `pkg/lipapi/events.go`
- `pkg/lipapi/capabilities.go`
- `pkg/lipapi/output_commit.go`
- canonical clone, validation, limits, sizing, token-accounting, and fuzz tests

### Extension platform

- `pkg/lipsdk/feature/bundle.go`
- `pkg/lipsdk/request/transform.go`
- `pkg/lipsdk/hooks/parts.go`
- `pkg/lipsdk/completion/*`
- `pkg/lipsdk/state/store.go`
- `internal/core/extensions/*`
- `internal/core/hooks/*`
- `internal/featurebundle/merge_surface.go`
- `internal/pluginreg/feature_scope.go`
- `internal/infra/runtimebundle/build_extension.go`
- `internal/core/diag/inventory_extensions.go`
- `internal/standardplugins/features_install.go`

### Routing and stream lifecycle

- `docs/adr/0002-immutable-baseline-and-attempt-derivation.md`
- `internal/core/runtime/executor_open_attempt.go`
- `internal/core/runtime/executor_retry_stream.go`
- `internal/core/runtime/executor_recv_handlers.go`
- `internal/core/runtime/parallel_race.go`
- interleaved-thinking runtime and store packages
- B2BUA continuity and output-commit tests

### Protocol adapters

- `internal/plugins/frontends/openailegacy/decode.go`
- `internal/plugins/frontends/openailegacy/encode.go`
- `internal/plugins/frontends/openairesponses/decode.go`
- `internal/plugins/frontends/anthropic/decode.go`
- `internal/plugins/frontends/gemini/*`
- OpenAI-compatible, OpenAI Responses, OpenRouter, Anthropic, and related backend payload/mapping packages

### Adjacent reasoning specifications

- `.kiro/specs/archive/anthropic-thinking-signature/requirements.md`
- `.kiro/specs/archive/anthropic-thinking-signature/design.md`
- `.kiro/specs/archive/anthropic-thinking-signature/tasks.md`
- interleaved-thinking configuration, memo, store, and runtime tests

## Existing Strengths to Preserve

### 1. Canonical response-side reasoning exists

`EventReasoningDelta` is a first-class canonical event. `EventReasoningSignatureDelta` and `Event.Signature` preserve Anthropic integrity metadata without leaking Anthropic SDK types into the core. This establishes a valid precedent for a provider-neutral historical-reasoning carrier.

### 2. Immutable per-attempt derivation is already a documented invariant

ADR 0002 requires:

1. one immutable post-submit baseline;
2. a fresh `CloneCall(baseline)` for every attempt;
3. no mutation leakage across retry, failover, or parallel candidates.

Reasoning restoration should extend this invariant, not bypass it.

### 3. The extension platform already owns request/response mutation

The repository explicitly requires request/response mutation to live behind feature hooks or extension seams. `FeatureBundle` provides stable ordering, explicit composition, panic isolation, diagnostics inventory, and plugin-instance scoping. Preservation belongs in an official feature plugin rather than a reasoning-specific branch spread through the executor.

### 4. The runtime already has the required candidate metadata

The attempt-open path knows:

- candidate key;
- backend instance;
- model;
- backend family prefixes;
- B-leg identity and sequence;
- authoritative session/scope views;
- capability facts;
- context eligibility;
- backend-ingress metering checkpoint.

The missing concern is a generic extension point at the correct position.

### 5. Secure-session and state partitioning foundations exist

The SDK state store supports request, session, principal, and global partitions and TTL. The runtime can project authoritative session views. This provides the identity substrate, even though the existing `Get`/`Put` API is not sufficient for atomic bounded turn-ring updates.

### 6. Adapter parity and conformance are established release practices

The repository already treats frontend/backend parity, protocol goldens, deterministic in-process integration tests, race tests, and fuzzing as release evidence. Reasoning replay should extend those matrices.

## Initial Requirement-to-Asset Map

| Initial requirement area | Existing assets | Gap classification | Brownfield conclusion |
| --- | --- | --- | --- |
| Opt-in feature and catalog | `plugins.features`, standard feature registry, inventory | Partial | Reuse feature row; add strict plugin-local config and versioned catalog. |
| Canonical historical reasoning | response events only; no `PartReasoning` | Missing / blocker | Add a request-side canonical part and hard replay capability. |
| Stream capture | response hooks; completion gates; traffic observers | Partial / wrong lifecycle | Add final-canonical-stream observer with explicit terminal outcome. |
| Exact detection | canonical messages and JSON payloads | Missing | Add deterministic anchor and placement algorithms in feature package. |
| Candidate restoration | early transform and late request-part hook | Missing / wrong timing | Add candidate-aware attempt transform before final negotiation/eligibility/preflight. |
| Bounded state | session TTL store | Partial / atomicity gap | Use feature-owned bounded concurrent store in v1. |
| Adapter replay | partial response emission | Missing on request path | Add adapter-local decode/encode and candidate replay profiles. |
| Observability | stage metrics, diagnostics inventory, structured logging | Partial | Add generic stage occupancy/outcomes and content-safe feature counters. |
| TDD/release | strong steering and test topology | Present | Make contract-first red phase explicit in tasks. |

## Critical Findings

## G-01 — Canonical requests cannot represent historical reasoning

**Severity:** P0 contract blocker

`lipapi.Message` contains ordered `Part` values, but `PartKind` has no reasoning kind. Response events cannot be placed back into later request history without overloading visible text or provider-specific JSON.

**Required requirement revision:**

- make historical reasoning an ordered assistant-only canonical part;
- carry a stable replay dialect and bounded payloads;
- validate and deep-clone opaque metadata;
- include reasoning in sizing, counting, checkpoints, and fuzzing.

## G-02 — Existing reasoning capability is not a replay guarantee

**Severity:** P0 compatibility blocker

`CapabilityReasoning` currently means that a request asks a model to reason. It is soft-downgradable, and the downgrade clears only `ReasoningEffort`. Historical reasoning already present in a request is materially different: silently dropping it defeats issue #157.

**Required requirement revision:**

- add a distinct hard `reasoning_replay` capability;
- derive it from canonical reasoning parts;
- require explicit backend support;
- forbid lossy downgrade.

## G-03 — Early request transforms do not know the target candidate

**Severity:** P0 lifecycle blocker

`request.Transform` runs before route planning. Backend/model matching cannot be implemented correctly there, and mutation would affect the immutable baseline used by all later candidates.

**Required requirement revision:**

- restore only on a candidate-specific clone;
- pass backend/model/family/replay support into a generic attempt transform;
- run after route selection and interleaved shaping.

## G-04 — Request-part hooks run too late

**Severity:** P0 accounting and eligibility blocker

`RequestPartHook` is candidate-aware, but it currently runs after required-capability derivation, initial model-context eligibility, token preflight, and some admission work. Injecting reasoning there can make all preceding decisions stale.

**Required requirement revision:**

- run restoration before hard capability derivation, final context eligibility, token preflight, backend-ingress freeze, and backend translation;
- recompute candidate size/token exposure after restoration;
- preserve only coarse baseline sizing during route expansion.

## G-05 — Existing mutation interfaces cannot exclude one candidate safely

**Severity:** P0 failover blocker

A normal hook error aborts or fails open according to failure mode. Issue #157 needs an unrepresentable candidate to be excluded so failover can try another compatible candidate.

**Required requirement revision:**

- define an attempt-transform decision distinct from errors;
- support `continue` and `exclude_candidate`;
- keep `log_skip` an explicit feature policy;
- return a stable compatibility error only after all candidates are exhausted.

## G-06 — Response-part hooks lack stream lifecycle and state services

**Severity:** P0 capture blocker

A response-part hook sees individual events but has no exactly-once finish callback for success, EOF, replacement, cancellation, close, or gate replacement. Maintaining an ad hoc global map keyed by B-leg would violate the explicit lifecycle and bounded-state rules.

**Required requirement revision:**

- add an observer factory that creates one attempt-scoped observation object;
- give the runtime exactly-once finish ownership;
- make observation read-only and non-blocking for client output.

## G-07 — Completion gates are incompatible with streaming-first capture

**Severity:** P0 architecture blocker

Completion gates buffer the whole completion before the first output. Enabling one solely to capture reasoning would delay TTFT and change behavior for every request.

**Required requirement revision:**

- observe incrementally;
- do not use completion gates for preservation;
- integrate with gate outcomes rather than turning preservation into a gate.

## G-08 — Raw upstream observation can anchor the wrong output

**Severity:** P0 correctness blocker discovered during gap analysis

Response hooks and completion gates can mutate or replace the canonical stream that reaches the frontend. If preservation anchors raw backend events, later client history may never match the stored artifact.

**Required requirement revision:**

- observe the final canonical stream after response hooks and gate resolution;
- discard original buffered output when a gate replaces it;
- anchor what the runtime released to the frontend adapter.

## G-09 — Success cannot mean transport acknowledgement

**Severity:** P1 boundary clarification

The core can know that it returned `response_finished` to the frontend encoder, but the current execution contract does not acknowledge that every HTTP/SSE byte was written successfully.

**Required requirement revision:**

- define v1 success as runtime release of the terminal canonical event;
- discard early close/cancellation before terminal release;
- do not claim proof of client transport acknowledgement.

## G-10 — Reasoning payload alone is insufficient to reconstruct order

**Severity:** P0 data-model blocker

An artifact containing only `[]ReasoningPart` cannot determine whether reasoning appeared before the first visible part, between visible content and a tool call, or in multiple interleaved blocks.

**Required requirement revision:**

- persist placement relative to non-reasoning part indexes;
- preserve per-block dialect, signature, and opaque metadata;
- restore at exact recorded positions.

## G-11 — Stable provider response IDs are not a current canonical contract

**Severity:** P1 scope correction

The initial plan considered preferring stable response/item IDs. Current canonical request/event types do not carry a provider-neutral response-item identity suitable for this purpose. Adding one would widen the feature unnecessarily and still would not be available across all protocols.

**Required requirement revision:**

- make v1 exact non-reasoning anchors the sole association mechanism;
- classify duplicate anchors as ambiguous;
- defer stable item identities to a separate canonical-contract change.

## G-12 — SDK state `Get` plus `Put` is not an atomic turn-ring contract

**Severity:** P0 concurrency and bounds blocker

The session-scoped SDK state store is useful for simple values but cannot atomically append, evict, enforce total bytes, and return defensive copies under concurrent turns.

**Required requirement revision:**

- use a feature-owned narrow store;
- make session append/evict atomic;
- bound TTL, turn count, per-turn bytes, and session bytes;
- explicitly classify v1 as process-local.

## G-13 — The feature must not trust client session hints

**Severity:** P0 isolation blocker

`Call.Session` contains client hints as well as proxy-owned authority. A feature that keys state directly from a client-provided ID could cross-contaminate unrelated traffic.

**Required requirement revision:**

- runtime projects an opaque authoritative session/A-leg partition;
- plugin never chooses authority from raw client values;
- no partition means no cross-request restoration unless a safe A-leg scope exists.

## G-14 — Adapter request-side parity is incomplete

**Severity:** P0 functional blocker

Current gaps include:

- OpenAI-compatible Chat request decoding does not ingest historical reasoning fields;
- legacy non-stream output omits collected reasoning;
- OpenAI Responses request decoding does not ingest reasoning input items;
- Anthropic request decoding does not ingest `thinking` or `redacted_thinking`;
- backend payload builders do not accept canonical historical reasoning;
- Gemini has no established legal replay contract in this codebase.

**Required requirement revision:**

- define adapter-owned replay dialects;
- add explicit request and backend mappings for supported families;
- advertise dialect support per candidate/model;
- isolate unsupported paths instead of cross-provider best-effort forwarding.

## G-15 — Backend instance rules and built-in catalog rules need different identities

**Severity:** P1 configuration blocker

Operators configure arbitrary backend instance IDs. A built-in catalog cannot rely on those names. The runtime already exposes connector-family `BackendPrefixes`.

**Required requirement revision:**

- explicit rules match exact instance IDs;
- built-in entries match stable backend-family prefixes plus model keywords;
- OpenRouter/compatible backends resolve the effective upstream flavor/model;
- explicit rules retain deterministic override priority.

## G-16 — Observability must be useful without becoming a hidden-reasoning export

**Severity:** P0 privacy blocker

The feature needs operational evidence but reasoning, signatures, opaque replay data, prompt excerpts, and even anchor digests are sensitive. Arbitrary model/rule/session labels would also create high-cardinality metrics.

**Required requirement revision:**

- fixed outcome taxonomy;
- safe structured correlation only;
- bounded-cardinality metrics;
- static configuration and aggregate counters only in diagnostics;
- no new raw capture path.

## G-17 — Multi-replica durability is not available through current feature state

**Severity:** P1 delivery-boundary correction

A process-local store cannot restore after restart or when a session moves to another replica.

**Required requirement revision:**

- v1 explicitly process-local;
- state miss is non-mutating;
- document sticky-session expectations;
- defer durable/distributed store work rather than implying it exists.

## Requirements Remediation Record

The committed `requirements.md` is the revised version produced after this analysis.

| Gap | Initial requirement weakness | Revision applied |
| --- | --- | --- |
| G-01, G-02 | Generic “canonical reasoning” and “support” wording | Added assistant-only ordered part, bounded payloads, deep clone, sizing/counting, and hard `reasoning_replay`. |
| G-03, G-04 | “Before backend open” was too imprecise | Fixed exact attempt-transform position and mandatory post-restoration eligibility/accounting recalculation. |
| G-05 | Hook errors were treated as candidate incompatibility | Added explicit candidate exclusion and final stable compatibility error. |
| G-06, G-07 | “Observe stream” did not define lifecycle | Added attempt-scoped observer factory, incremental operation, exactly-once terminal outcomes, and no completion-gate dependency. |
| G-08 | Initial plan observed raw backend events | Changed capture to final post-hook/post-gate canonical events. |
| G-09 | “Actually surfaced” overstated current transport evidence | Defined success as runtime terminal-event release and documented lack of transport ACK. |
| G-10 | Artifact lacked placement | Added ordered placement metadata and exact reinsertion. |
| G-11 | Stable provider IDs were assumed available | Removed stable IDs from v1 matching; duplicate exact anchors are ambiguous. |
| G-12, G-17 | Session state was under-specified | Added feature-owned atomic bounded process-local store and sticky-session limitation. |
| G-13 | Session scope could be read as client ID | Required opaque authoritative session/A-leg partition supplied by runtime. |
| G-14 | Adapter coverage was broad but not testable | Added explicit dialect requirements for Chat, Responses, Anthropic, OpenRouter/compatible, and unsupported Gemini behavior. |
| G-15 | Built-in and operator backend matching were conflated | Split exact instance rules from family-prefix built-ins and fixed precedence. |
| G-16 | “Log activity” lacked privacy/cardinality rules | Added content-safe taxonomy, bounded metrics, diagnostics limits, and no new raw capture. |

## Architecture Options Considered

### Option A — Early request transform plus response-part hook

**Rejected.**

It is superficially small but fails candidate matching, immutable baseline isolation, capability/eligibility ordering, lifecycle finalization, and bounded state ownership.

### Option B — Completion gate plus request transform

**Rejected.**

A gate can inspect a complete response and has state services, but it buffers before first output, changes TTFT, and still cannot restore per candidate at the correct point.

### Option C — Core-owned reasoning preservation branch

**Rejected.**

It could access all lifecycle details but would violate the extension-platform boundary and embed model/provider policy in core orchestration.

### Option D — Generic attempt-transform and final-stream-observer seams plus official feature plugin

**Selected.**

- canonical request reasoning remains shared product semantics;
- generic lifecycle seams are reusable and provider-neutral;
- preservation policy/state/matching stays in the feature plugin;
- adapters own replay dialects;
- runtime owns candidate ordering and exactly-once observation finalization;
- disabled deployments have no preservation behavior.

## Recommended Delivery Sequence

1. Freeze canonical and SDK interfaces plus failing tests.
2. Implement canonical reasoning and hard capability.
3. Implement generic attempt-transform and stream-observer runners.
4. Implement bounded feature state, catalog, anchors, capture, and restoration.
5. Add adapter replay dialects and capability profiles.
6. Prove failover, parallel, gate, cancellation, race, fuzz, privacy, and disabled behavior.
7. Document process-local posture and operator configuration.

## Requirement-to-Finding Map

| Requirement | Primary findings |
| --- | --- |
| 1 | G-15, G-16 |
| 2 | G-01, G-02, G-10 |
| 3 | G-06, G-07, G-08, G-09, G-10 |
| 4 | G-10, G-11 |
| 5 | G-03, G-04, G-05 |
| 6 | G-12, G-13, G-17 |
| 7 | G-02, G-14, G-15 |
| 8 | G-16 |
| 9 | all cross-cutting regression and delivery findings |

## Gap-Analysis Verdict

Proceed to design using Option D. The initial requirements were not sufficient because they did not pin the correct stream observation point, candidate exclusion semantics, exact placement, authoritative session partition, process-local limitation, or runtime-release success boundary. Those gaps are corrected in the committed requirements.
