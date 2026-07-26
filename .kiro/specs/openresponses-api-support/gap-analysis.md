# Brownfield Gap Analysis

## Scope and Method

This review compares the OpenResponses `2026-04-24` protocol profile with the repository at `main` commit `1022e47f4574f5b1cdfa63533d04268f763a32e6`. It covers the current OpenAI Responses frontend, OpenAI/OpenAI-compatible Responses backends, canonical request/event contracts, route ownership, transport negotiation, continuation/session infrastructure, generic compatible backend modes, and the active backend connector plugin architecture.

Classifications:

- **Missing**: no current implementation or contract exists.
- **Partial**: a reusable implementation exists but does not satisfy the OpenResponses contract.
- **Constraint**: an existing architecture or compatibility commitment limits the solution.
- **Unknown**: implementation work must validate a material external or brownfield assumption.

Effort estimates are relative implementation sizes:

- **S**: focused package change with established seams.
- **M**: multi-package change with integration tests.
- **L**: public/internal contract change with broad adapter updates.
- **XL**: architecture migration, stateful transport, or public plugin ABI impact.

## Current Assets to Reuse

### Existing OpenAI Responses frontend

- `internal/plugins/frontends/openairesponses/mount.go`
- `internal/plugins/frontends/openairesponses/handler.go`
- `internal/plugins/frontends/openairesponses/decode.go`
- `internal/plugins/frontends/openairesponses/encode.go`
- `internal/plugins/frontends/openairesponses/sse_write.go`
- `internal/plugins/protocols/openairesponsesitem`

Reusable strengths:

- standard frontend authentication, route selection, body limits, client identity, session carriers, and executor invocation;
- JSON and SSE response plumbing;
- semantic event-to-wire aggregation patterns;
- exact OpenAI reasoning replay support;
- OpenRouter body/header capture utilities, which demonstrate bounded protocol metadata carriage but are not themselves the generic OpenResponses extension policy.

### Existing Responses backends

- `internal/plugins/backends/openairesponses`
- `internal/plugins/backends/openaicompat`
- official `github.com/openai/openai-go/v3` dependency
- credential pooling, endpoint configuration, model discovery, stream peeking, error classification, and transport capabilities

Reusable strengths:

- canonical-to-provider request mapping;
- HTTP JSON and SSE invocation;
- correct first-event peeking before candidate commitment;
- credential invalidation/rate-limit cooldown;
- generic OpenAI-compatible endpoint configuration and inventory.

### Canonical and runtime seams

- `pkg/lipapi.Call`, messages, parts, tools, generation options, extensions, events, managed streams, capabilities, invocation operations, and transport modes;
- `internal/core/capabilities` negotiation;
- `internal/core/routing`, pre-output failover, and commitment rules;
- non-streaming collection and standard HTTP composition;
- runtime reload, diagnostics, accounting, audit redaction, and secure session infrastructure;
- the already approved `github.com/gorilla/websocket` dependency.

### Relevant architecture commitments

- canonical core rather than pairwise protocol translators;
- wire ownership in frontends/backends;
- no provider SDK types in core or frontend packages;
- no transparent retry after client-visible output;
- dependency-free generic protocol-compatible modes may remain in the standard distribution;
- provider-specific OpenRouter and similar connectors remain external.

## API Comparison Summary

| Area | OpenResponses 2026-04-24 | Current OpenAI Responses implementation | Gap |
|---|---|---|---|
| Protocol identity | Separately governed dated interoperability profile | `openai.responses` operation and OAI-specific adapter | Distinct operation/profile missing |
| Route | `/v1/responses`, `/v1/responses/compact`, optional WebSocket on same response resource | POST `/v1/responses` plus OAI `/cancel`; no compact; no WebSocket | Route collision and missing endpoints/transports |
| Request validation | Model/input conditionally optional; broad dated request schema | Model and input always required; narrow fields; some known fields silently ignored | Partial and behaviorally incompatible |
| Canonical shape | Ordered items are the unit of context | Message-oriented calls with function/reasoning folded into parts | Structural loss and ordering risk |
| Roles/phase | System, developer, user, assistant; assistant `commentary`/`final_answer` | Developer collapses into system; no phase | Missing |
| Content | Asymmetric user/model unions; image/file/video and output/reasoning variants | Text/image/file plus limited reasoning; no video/refusal/phase fidelity | Partial |
| Tools | Portable function tools plus hosted/extension tools and dated schema variants | Function tools only in canonical; OpenRouter extras partially captured | Partial and extension-bound |
| Item lifecycle | IDs, statuses, output/content indices, semantic item events | Coarser canonical stream; frontend synthesizes sparse OAI events | Partial |
| Response object | Large required `ResponseResource` with explicit null/default fields | Sparse response object with id/object/time/status/model/output/usage | Non-conformant |
| SSE | Event name equals body type; semantic lifecycle; `[DONE]` | Same basic framing, incomplete event/resource coverage | Reusable but partial |
| Continuation | `previous_response_id`, persisted and connection-local semantics | Field recognized as known but not mapped into canonical behavior | Missing; current value is effectively dropped |
| Compaction | Standalone compact request/resource and compaction item | No frontend route, canonical operation, backend capability, or collector | Missing |
| WebSocket | Sequential `response.create`, one in flight, store-false cache, eviction, 60-minute limit | No frontend WebSocket transport for Responses | Missing |
| Extensions | Implementor-prefixed types, opaque handling, portability constraints | Top-level OpenRouter extras exist; unknown backend events are ignored | Partial and unsafe for generic use |
| Backend generic mode | Generic standard endpoint expected | `custom-openai-responses-compatible` uses OAI SDK/types | Similar plumbing, wrong protocol contract |
| Compliance | Official HTTP and WebSocket suite | No OpenResponses fixtures or CI profile | Missing |

## Mandatory Gap Register

| ID | Severity | Classification | Effort | Finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Missing | M | OpenResponses has no distinct protocol identity. | Add explicit versioned frontend, operation/profile, backend kind, and diagnostics identity. |
| G-02 | P0 | Constraint | M | OpenResponses and OpenAI Responses both claim `/v1/responses`; the current frontend hard-mounts the OAI route. | Add configurable OpenResponses base path and pre-serve method/path ownership validation; never sniff one shared route. |
| G-03 | P0 | Missing | XL | The canonical call is message-oriented while OpenResponses is ordered item-oriented. | Add an additive ordered canonical item trajectory and migrate walkers/capability derivation/hooks. |
| G-04 | P0 | Partial | L | System/developer distinction, assistant phase, item IDs/status, structured function output, video, refusal, and compaction cannot be represented exactly. | Extend protocol-neutral item/content metadata and lossless projections. |
| G-05 | P0 | Partial | L | Current frontend requires model/input and recognizes but does not implement `previous_response_id`. | Implement OpenResponses-specific validation and proxy-owned continuation resolution. |
| G-06 | P0 | Missing | XL | No persistent or connection-local response history contract exists for OpenResponses semantics. | Add bounded tenant/session-scoped continuation storage and materialization. |
| G-07 | P0 | Missing | L | No context-compaction operation exists in core/backend ports. | Add protocol-neutral compaction service/capability through normal routing. |
| G-08 | P0 | Missing | XL | No client-facing Responses WebSocket transport exists. | Add authenticated sequential WebSocket termination, state, limits, and shared event encoding. |
| G-09 | P0 | Partial | L | Current response encoder omits many schema-required fields. | Build a profile-specific response resource builder with presence/default rules. |
| G-10 | P0 | Partial | L | Current stream events are too coarse for full item/content lifecycle and assistant phase. | Add minimal canonical metadata/events and a deterministic OpenResponses state-machine encoder. |
| G-11 | P0 | Partial | L | Unknown backend events are ignored, which violates extension observability. | Add bounded opaque extension item/tool/event carriers and capability binding. |
| G-12 | P0 | Constraint | M | Existing `custom-openai-responses-compatible` is tied to OpenAI SDK unions and OAI semantics. | Create a separate dependency-free `custom-openresponses-compatible` mode; do not extend the OAI alias. |
| G-13 | P0 | Constraint | L | The active backend plugin architecture names a fixed built-in family set and has no compaction port. | Revalidate built-in-compatible classification and plugin ABI before implementation. |
| G-14 | P0 | Missing | M | No official OpenResponses conformance profile is pinned. | Pin official Apache-2.0 sources/commit and add Go-native plus official-suite gates. |
| G-15 | P1 | Partial | M | SSE framing is reusable but event sequencing, required terminal failure behavior, and response schema are incomplete. | Refactor low-level framing separately from protocol state-machine encoding. |
| G-16 | P1 | Missing | M | No protocol profile/version configuration exists. | Strictly support `2026-04-24` first and reject unknown snapshots. |
| G-17 | P1 | Missing | M | Route ownership is implicit in handler registration. | Introduce immutable method/path ownership descriptors and deterministic collision errors. |
| G-18 | P1 | Missing | L | Candidate capability negotiation cannot express item dialect, assistant phase, compaction, or opaque extension binding. | Extend explicit hard capability requirements and backend declarations. |
| G-19 | P1 | Partial | M | `Call.Extensions` can carry raw JSON but does not distinguish semantic, provider-bound, stale, or safe-to-failover data. | Use typed residual protocol controls and bounded opaque records, not a raw full-request tunnel. |
| G-20 | P1 | Missing | M | Upstream provider response IDs could be confused with client-facing continuation IDs. | Issue proxy IDs and retain backend IDs only as internal lineage optimizations. |
| G-21 | P1 | Missing | M | No chain depth, amplification, TTL, or cross-tenant response-ID policy exists. | Add bounded continuation policy and indistinguishable not-found behavior. |
| G-22 | P1 | Missing | M | WebSocket origin, queue, pending-event, idle, and maximum-age policies are absent for this endpoint. | Add strict defaults and development-only origin relaxation. |
| G-23 | P1 | Unknown | S | Official OpenResponses normative prose and generated OpenAPI contain tension: the schema includes unprefixed OpenAI-derived hosted tools/items while extension prose requires implementor prefixes. | Define a pinned conformance profile: typed portable core, recognized dated legacy types capability-gated, new unknown types prefix-gated. |
| G-24 | P1 | Unknown | S | `github.com/joeychilson/openresponses` appears complete and MIT on pkg.go.dev, but is untagged and its source repository is not currently retrievable through GitHub. | Do not adopt as production dependency; retain as research evidence and re-evaluate only through an explicit dependency gate. |
| G-25 | P1 | Unknown | S | `github.com/webforspeed/openresponses-go` has no repository license file and only a narrow client README. | Reject as a production dependency unless licensing and coverage are made explicit and reviewed. |
| G-26 | P1 | Constraint | S | Official `openai-go` is Apache-2.0 and already used, but is OpenAI-specific and lacks first-class Responses WebSocket support. | Keep it in OAI backend packages; do not use it as the OpenResponses contract. |
| G-27 | P1 | Partial | M | Generic endpoint config/credentials/inventory are reusable but currently permit OAI-flavor construction only. | Reuse common infrastructure with a new OpenResponses wire client and strict capability profile. |
| G-28 | P1 | Missing | M | Standard errors and WebSocket error envelopes are not mapped for OpenResponses. | Add profile-specific HTTP/SSE/WS error mapping with stable internal classifications. |
| G-29 | P1 | Missing | M | No differential proof protects the existing OAI frontend/backend during shared-helper refactoring. | Add characterization and parity fixtures before moving shared logic. |
| G-30 | P1 | Missing | M | Audit/redaction/counting code may scan legacy messages only. | Add ordered-item walkers and security tests before enabling item-form requests. |
| G-31 | P1 | Missing | M | Runtime reload/shutdown semantics for WebSockets and continuation state are unspecified. | Add atomic ownership, drain/cancel, and persistence lifecycle rules. |
| G-32 | P2 | Constraint | S | Initial generic upstream WebSocket support would add session-affine pooling and failure recovery complexity without being required for base interoperability. | Terminate client WebSockets at Go-LIP and use upstream HTTP/SSE initially; defer upstream pooling to a separate review. |
| G-33 | P2 | Partial | S | The dated schema includes optional background/conversation controls without a complete proxy job-resource surface. | Reject unsupported background/conversation modes explicitly in the initial profile; do not silently ignore them. |
| G-34 | P2 | Missing | S | Documentation could confuse OpenResponses with the unrelated `open-responses/open-responses` project. | Add explicit naming and compatibility notes. |

## Current Behavior That Must Be Characterized Before Change

1. OpenAI Responses route registration, error shape, request field handling, SSE framing, and non-streaming aggregation.
2. OpenAI reasoning-item exact replay and OpenRouter residual body/header capture.
3. Backend stream first-event peeking, credential rotation, error classification, cancellation, and one-terminal behavior.
4. Canonical capability derivation, downgrade policy, hook traversal, redaction, accounting, and audit serialization.
5. Generic compatible endpoint URL joining, no-auth behavior, model inventory, route-prefix validation, and runtime reload.
6. Standard HTTP route composition and duplicate registration failure modes.

## Architecture Options

### Option A: Treat OpenResponses as an OpenAI Responses alias

**Approach**

- Reuse the existing `/v1/responses` frontend and `custom-openai-responses-compatible` backend.
- Add a few missing fields and call it OpenResponses-compatible.

**Advantages**

- Smallest initial diff.
- Reuses the official OpenAI Go SDK.

**Disadvantages**

- Cannot expose both protocols on one route with honest contracts.
- Silently inherits OpenAI-specific unions, defaults, errors, IDs, and SDK drift.
- Does not solve compaction, connection-local WebSocket continuation, phase, full response-resource presence, or unknown extension events.
- Preserves the current lossy message-oriented canonical path.

**Verdict:** Rejected. It produces nominal compatibility rather than the separately versioned protocol requested.

### Option B: Raw reverse proxy / protocol tunnel

**Approach**

- Forward the OpenResponses JSON or SSE bytes directly from frontend to one configured remote endpoint.
- Bypass canonical mapping for unknown items and events.

**Advantages**

- High wire fidelity for one provider.
- Limited schema implementation effort.

**Disadvantages**

- Violates canonical-core and frontend/backend independence rules.
- Prevents normal routing, capability negotiation, hooks, accounting, redaction, state, failover, and provider selection.
- Creates a pairwise frontend-to-backend dependency.
- Makes continuation and compaction authority opaque to core.

**Verdict:** Rejected.

### Option C: Full replacement of the canonical message model with OpenResponses wire items

**Approach**

- Make OpenResponses item structs the new canonical API and migrate every adapter at once.

**Advantages**

- Exact OpenResponses representation.
- Simplifies the new adapters.

**Disadvantages**

- Leaks one external protocol into core.
- Forces a repository-wide flag day and breaks existing plugins/SDKs.
- Makes future non-Responses protocols project through OpenResponses rather than neutral concepts.

**Verdict:** Rejected.

### Option D: Distinct versioned adapters over an additive protocol-neutral item trajectory

**Approach**

- Add a neutral ordered item trajectory and compaction port.
- Keep OpenAI Responses and OpenResponses wire codecs separate.
- Add an OpenResponses frontend with configurable route ownership and proxy continuation state.
- Add a dependency-free generic OpenResponses backend mode using project-owned wire types.
- Preserve provider extensions through bounded dialect-tagged carriers and hard capability binding.
- Terminate client WebSockets at the proxy while using existing upstream HTTP/SSE execution.

**Advantages**

- Honest protocol identity and versioning.
- Preserves canonical routing, failover, hooks, accounting, and no-retry-after-output rules.
- Supports full client-facing semantics without upstream WebSocket complexity.
- Creates reusable neutral concepts for future OAI compaction and item-oriented adapters.
- Keeps provider SDKs and proprietary extensions outside core.

**Disadvantages**

- Largest up-front design and migration effort.
- Requires careful dual-form canonical migration and plugin-ABI revalidation.
- Adds stateful continuation and WebSocket lifecycle ownership.

**Verdict:** Selected.

## Selected Brownfield Strategy

1. Pin and vendor metadata for the official `2026-04-24` profile.
2. Add characterization tests for existing OAI Responses behavior before sharing helpers.
3. Introduce neutral item/phase/content/opaque-extension contracts additively.
4. Extend capability negotiation so unsupported semantics hard-fail before upstream work.
5. Add a generic context-compaction port through route selection and backend capability contracts.
6. Build project-owned OpenResponses wire codecs and state-machine tests from pinned official material.
7. Add the client-facing HTTP/SSE frontend at a non-colliding default base path.
8. Add proxy-owned continuation state and proxy response IDs.
9. Add the dependency-free generic remote backend over HTTP/SSE and compact.
10. Add client-facing WebSocket termination using the same executor and event encoder.
11. Run Go-native and official compliance suites plus differential OAI tests.
12. Revalidate and update adjacent backend plugin/generic-mode contracts before implementation merges.

## Dependency Decision

### Official OpenResponses sources

- License: Apache-2.0.
- Use: normative source, pinned schema/examples, conformance scenarios, attribution.
- Decision: accepted.

### `github.com/joeychilson/openresponses`

- Reported license: MIT.
- Reported fit: client, server, compact, SSE, WebSocket, extensions, and compliance coverage.
- Risks: pseudo-version only, no stable tag, no known importers, source repository currently unavailable through GitHub despite pkg.go.dev cache.
- Decision: not a production dependency. May be consulted as non-authoritative implementation research only if source can be audited.

### `github.com/webforspeed/openresponses-go`

- Reported fit: small typed client oriented to OpenRouter.
- Risks: no LICENSE file found in the repository; no demonstrated server, compact, WebSocket, extension, or compliance coverage.
- Decision: rejected.

### Official `github.com/openai/openai-go/v3`

- License: Apache-2.0.
- Fit: mature OpenAI HTTP/SSE client already used by Go-LIP.
- Risks for this feature: OpenAI-specific schema and defaults; no first-class Responses WebSocket support; unknown OpenResponses extensions may be lost; using it would couple the new protocol to OpenAI release drift.
- Decision: retain only in OpenAI backend packages.

### Custom project-owned codec

- Inputs: pinned Apache-2.0 OpenResponses schema/spec/examples.
- Runtime dependencies: standard library plus existing approved WebSocket package.
- Decision: selected baseline.

## Design Review Gates Before Implementation

1. **Canonical gate**: ordered items are neutral, bounded, additive, and do not make OpenResponses wire types public core contracts.
2. **Routing gate**: continuation lineage and extension binding cannot bypass candidate negotiation or commitment.
3. **State gate**: proxy IDs, tenant isolation, TTL, chain bounds, and `store:false` behavior are explicit.
4. **Plugin gate**: compaction and new capability metadata are represented in the active backend plugin ABI or intentionally unavailable to external plugins until its version advances.
5. **Route gate**: coexistence and collision errors are deterministic before serving.
6. **Conformance gate**: official pinned suite and Go-native fixtures agree on the supported profile.
7. **Regression gate**: existing OpenAI Responses fixtures pass unchanged.
8. **Dependency gate**: no unavailable, unlicensed, untagged, or provider-specific package enters the root module without separate approval.

## Final Gap Verdict

**GO with architecture changes and explicit adjacent-spec revalidation.**

The existing HTTP/SSE, credential, routing, stream, and generic-endpoint infrastructure materially reduces implementation effort, but the feature is not a thin compatibility alias. Correct support requires a distinct protocol profile, ordered canonical items, compaction, continuation state, route ownership, response-resource fidelity, extension-safe capability negotiation, and a stateful client WebSocket frontend.
