# Requirements Document

## Introduction

OpenResponses is a separately governed, dated interoperability specification derived from the OpenAI Responses API. The two protocols share substantial vocabulary and wire shapes, but they are not interchangeable contracts. OpenResponses 2026-04-24 defines its own normative transport, item lifecycle, continuation, compaction, assistant phase, extension, response-resource, and compliance behavior.

Go-LIP already exposes an OpenAI Responses frontend and includes OpenAI Responses backend adapters. This feature adds OpenResponses as a distinct protocol surface without changing the meaning, routes, or compatibility promises of those existing adapters. It provides both:

- a client-facing OpenResponses frontend supporting HTTP JSON, HTTP SSE, standalone compaction, and persistent WebSocket turns; and
- a dependency-free generic backend mode for remote OpenResponses-capable providers and routers over HTTP JSON/SSE and compaction.

The implementation is pinned initially to the official `2026-04-24` specification snapshot and official compliance suite. The portable protocol profile is implemented directly. Provider- or router-specific behavior remains capability-gated and, where it requires proprietary headers, routing semantics, inventory, billing, or typed extensions, belongs in the corresponding provider connector rather than the generic mode.

## Boundary Context

- **In scope**: protocol identity and versioning; ordered item trajectory; HTTP JSON; SSE; response-resource synthesis; client-facing WebSocket; continuation state; compaction; generic remote backend mode; provider-prefixed opaque extensions; configuration; diagnostics; limits; conformance; adjacent architecture revalidation.
- **Out of scope**: changing the public contract of the existing OpenAI Responses endpoints; pretending OpenResponses is an OpenAI-compatible alias; background job retrieval APIs; arbitrary provider-specific header forwarding; OpenRouter-specific attribution/routing/billing behavior; automatic upstream WebSocket pooling; runtime SDK generation or download.
- **Adjacent specifications requiring revalidation**: `backend-connector-plugin-architecture`, `generic-compatible-backend-modes`, canonical API steering, routing/commitment steering, and any active backend plugin ABI work.

## Requirements

### Requirement 1: Distinct Protocol Identity and Version Pinning

**Objective:** As a maintainer, I want OpenResponses represented as an explicit dated protocol profile, so that compatibility does not drift with either OpenAI's API or an unversioned external schema.

#### Acceptance Criteria

1. The system shall identify OpenResponses with a distinct frontend ID, operation identity, backend factory kind, diagnostics identity, and conformance profile rather than aliasing `openai.responses`.
2. The initial implementation shall pin protocol behavior to the official OpenResponses `2026-04-24` normative specification, OpenAPI snapshot, examples, and compliance tests at a reviewed immutable upstream commit.
3. The implementation shall not silently follow the mutable unversioned OpenResponses schema or website when upstream changes.
4. When configuration requests an unsupported OpenResponses version, startup shall fail before serving with the supported versions listed.
5. A later OpenResponses snapshot shall require an explicit profile addition, compatibility review, fixture update, and conformance run rather than mutating the existing profile in place.
6. Existing OpenAI Responses frontend and backend behavior, operation names, route defaults, and protocol-specific reasoning replay shall remain unchanged unless a separately reviewed compatibility change is required.
7. Diagnostics shall expose the configured OpenResponses profile and whether the endpoint is client-facing, generic remote, or provider-specific.
8. The architecture tests shall fail if OpenResponses is registered as the existing OpenAI Responses operation or if one adapter's wire types are exported as the other's public contract.

### Requirement 2: Client-Facing Route Ownership and HTTP Semantics

**Objective:** As an operator, I want an exact OpenResponses HTTP surface that can coexist safely with the existing OpenAI Responses frontend.

#### Acceptance Criteria

1. The OpenResponses frontend shall expose `POST <base_path>/responses` and `POST <base_path>/responses/compact`, with `GET <base_path>/responses` reserved for WebSocket upgrade handling.
2. The default OpenResponses `base_path` shall be `/openresponses/v1`, allowing it to coexist with the existing OpenAI Responses `/v1/responses` route.
3. An operator may configure the canonical OpenResponses base path `/v1` only when no other enabled frontend owns any resulting method/path pair.
4. Before serving, the composition layer shall validate method/path ownership and return a deterministic conflict naming both owners rather than relying on `http.ServeMux` panic behavior or registration order.
5. The system shall not select between OpenAI Responses and OpenResponses by inspecting request bodies, headers, user agents, or response shape on one shared route.
6. The create endpoint shall require JSON request bodies, enforce configured body and nesting limits, and return protocol-shaped errors for malformed or unsupported requests.
7. The create endpoint shall accept a string input or an ordered item array and shall allow `model` or `input` omission only when the remaining request and resolved continuation make the request semantically valid.
8. Non-streaming creation shall return an `application/json` OpenResponses `ResponseResource` containing every field required by the pinned schema, with explicit nulls or zero/default values where the schema requires presence.
9. The frontend shall not expose the OpenAI-specific `/responses/cancel` route as part of the OpenResponses surface.
10. Authentication, route selection, client identity projection, request limits, and secure-session policy shall reuse standard frontend infrastructure without treating client-supplied response IDs as session authority.

### Requirement 3: Ordered Canonical Item Trajectory

**Objective:** As a protocol adapter author, I want an ordered, protocol-neutral item representation, so that OpenResponses items are not collapsed into a lossy message-only shape.

#### Acceptance Criteria

1. The canonical API shall add an ordered item trajectory capable of representing message, item reference, function call, function call output, reasoning, compaction, and bounded extension items.
2. A canonical item shall preserve applicable item identity, lifecycle status, role, assistant phase, call identity, ordering, content ordering, and opaque replay material without importing OpenResponses wire structs.
3. Canonical message roles shall distinguish `system`, `developer`, `user`, and `assistant` when the source protocol distinguishes them.
4. Assistant message phase shall preserve `commentary` and `final_answer` on input history and model output.
5. Canonical content shall support the pinned portable input content set, including text, image, file, and video references, and the portable model-output content set, including output text, refusal, reasoning text, summaries, and annotations where available.
6. Function call output shall preserve both string output and structured content-part arrays.
7. Reasoning items shall preserve content, summary, and encrypted provider replay material using bounded dialect-tagged opaque data and shall never expose or reinterpret protected reasoning.
8. Compaction items shall be round-trippable as opaque context items and shall remain bound to a compatible backend profile when their semantics are not portable.
9. Existing message-based calls shall remain source-compatible during migration, but validation shall prevent simultaneous conflicting item and legacy-message authorities.
10. Shared canonical walkers, capability derivation, hooks, accounting, and redaction shall handle both legacy calls and ordered item calls without silently skipping item-form requests.
11. Projection from ordered items to a legacy backend shall occur only through an explicit tested projector and shall reject before upstream work when the requested item semantics cannot be represented losslessly.
12. Item and content payloads shall have deterministic count, depth, string, binary-reference, and opaque-payload bounds.

### Requirement 4: Tools, Extensions, and Lossless Capability Negotiation

**Objective:** As a router user, I want portable tools and declared extensions preserved only on compatible candidates, so that provider-specific data is neither dropped nor leaked across providers.

#### Acceptance Criteria

1. The portable OpenResponses profile shall support function tools, all pinned `tool_choice` forms required by the official schema, parallel-tool-call policy, function call items, and function call outputs.
2. The implementation shall distinguish the normative portable profile from provider-derived hosted tool and item shapes present in the dated OpenAPI schema.
3. A standard item, content part, annotation, tool, or event supported by the pinned profile shall use a typed project-owned representation rather than arbitrary maps.
4. A non-standard extension type shall be accepted as opaque only when its discriminator follows the pinned extension naming rules or is an explicitly recognized legacy schema type from the pinned snapshot.
5. Opaque extension records shall preserve exact bounded JSON, discriminator, implementor slug, direction, and dialect while remaining invisible to generic core policy except for validation, capability negotiation, redaction, and routing constraints.
6. A call containing provider-bound item, tool, reasoning, compaction, or top-level extension data shall require a backend that advertises the exact compatible dialect and, where applicable, implementor slug or configured extension key.
7. Core shall reject a candidate before upstream work when an extension would be dropped, renamed, inspected by an unrelated provider, or sent to an incompatible implementor.
8. Pre-output failover shall consider only candidates that satisfy the same extension and replay constraints; no failover shall occur after client-visible output.
9. The generic backend mode shall reject unknown proprietary top-level request fields by default.
10. An operator may explicitly allow named bounded top-level JSON extension keys for a generic endpoint; those keys shall be candidate-bound, excluded from logs, and never grant arbitrary header forwarding.
11. Provider-specific authentication, attribution headers, model routing, inventory, billing, or typed proprietary extensions shall remain in the provider's external connector even when that connector reuses the shared OpenResponses wire codec.
12. Unknown provider output extensions shall be surfaced as bounded opaque events or items to an OpenResponses client and shall not be silently discarded.

### Requirement 5: Streaming State Machine and Response Resource Fidelity

**Objective:** As an OpenResponses client, I want schema-valid semantic events and terminal ordering, so that streaming behavior is interoperable and deterministic.

#### Acceptance Criteria

1. When `stream:true`, the create endpoint shall return `text/event-stream` and emit each JSON event with an SSE `event` value equal to the event body's `type`.
2. The final SSE record shall be the literal `[DONE]` data value after exactly one terminal response event.
3. The frontend encoder shall synthesize or preserve the pinned response, item, content-part, text, reasoning, function-argument, refusal, and error lifecycle events in valid sequence-number order.
4. Every output item shall follow `response.output_item.added` through one terminal `response.output_item.done`, with content-part lifecycle events where applicable.
5. Item IDs, call IDs, output indices, content indices, statuses, assistant phases, and sequence numbers shall remain internally consistent across the stream and final response resource.
6. A streaming error shall produce the required structured error event and terminal failed response semantics before `[DONE]` when the transport remains writable.
7. The adapter shall emit no event after a terminal event and shall not emit duplicate terminal, item-done, or content-done events.
8. Backpressure, client cancellation, write failure, panic recovery, and stream closure shall retain bounded memory and exactly-once terminal ownership.
9. The non-streaming collector and streaming encoder shall be derived from the same ordered canonical event/item semantics and shall produce equivalent final output.
10. Provider-native event IDs or response IDs shall not become proxy continuation authority merely because they are exposed on the client-facing stream.

### Requirement 6: Proxy-Owned Response Continuation

**Objective:** As a client using `previous_response_id`, I want continuation to work without exposing backend state or weakening routing and session isolation.

#### Acceptance Criteria

1. Client-facing response IDs shall be proxy-issued opaque identifiers scoped to the authenticated client or authoritative session and shall not be treated as raw backend response IDs.
2. When `previous_response_id` is supplied, the continuation service shall resolve the referenced prior input and output, preserve their semantic order, and append the new input before backend execution.
3. A continuation record shall include protocol profile, canonical input/output trajectory, model and route lineage, persistence mode, expiry, and any provider-bound replay requirements needed for safe routing.
4. When a continuation contains only portable canonical items, core may route or fail over among candidates that satisfy the request capabilities.
5. When a continuation contains provider-bound opaque material, compaction, or native replay state, routing shall remain pinned to compatible lineage or fail before upstream work.
6. The generic backend connector shall use materialized canonical history by default and shall not forward a client-supplied `previous_response_id` directly to a remote provider.
7. An implementation may use a remote native response ID as an internal optimization only when the selected backend instance and protocol profile match the stored lineage and a full canonical fallback remains policy-valid.
8. HTTP continuation with persisted `store:true` state shall obey configured TTL, tenant/session isolation, storage limits, deletion, and startup/shutdown ownership.
9. A missing, expired, unauthorized, evicted, or incompatible response ID shall return `previous_response_not_found` without revealing whether another tenant owns the ID.
10. Client request metadata, response IDs, and model strings shall not override authoritative session, route, or backend identity.
11. Stored continuation data shall use the existing secure state policy or a new equally reviewed storage boundary and shall not be written to ordinary logs or diagnostics.
12. Continuation lookup and materialization shall be bounded against unbounded chain depth, cycles, amplification, and oversized reconstructed context.

### Requirement 7: Standalone Context Compaction

**Objective:** As an agent client, I want `/responses/compact` to produce a reusable compacted context through normal routing, so that compaction is not a frontend-to-backend shortcut.

#### Acceptance Criteria

1. The system shall expose a protocol-neutral context-compaction operation through core-owned route selection and an optional backend compaction capability.
2. `POST <base_path>/responses/compact` shall require a model and shall accept the pinned compact request fields and ordered input trajectory.
3. A backend candidate shall be eligible for compaction only when it advertises compaction support for the required profile and item/replay capabilities.
4. The generic OpenResponses backend shall map the canonical compaction request to remote `POST /responses/compact` without routing around core.
5. The frontend shall return the pinned `response.compaction` resource with required ID, object, output, created timestamp, and usage fields.
6. Compaction output shall contain a reusable ordered item window and at least one compaction item when required by the upstream result.
7. A compacted output used on a later create request shall start a new response chain and shall not implicitly reuse the pre-compaction `previous_response_id`.
8. Compaction errors shall use classified pre-output retry/failover policy; once a compaction result is returned, it shall not be replayed transparently.
9. If no eligible backend supports compaction, the frontend shall return a protocol-shaped unsupported-capability error rather than emulating lossy summarization.
10. The backend plugin ABI and built-in backend port shall be revalidated and, if necessary, extended so external OpenResponses provider connectors can advertise and execute the same compaction contract.

### Requirement 8: Client-Facing WebSocket Transport

**Objective:** As an agent client, I want persistent sequential OpenResponses turns over WebSocket with the same semantics as HTTP/SSE.

#### Acceptance Criteria

1. When enabled, `GET <base_path>/responses` with a valid WebSocket upgrade shall authenticate and upgrade using the project's approved WebSocket library and origin policy.
2. Each client turn shall begin with a JSON `response.create` message; HTTP-only fields `stream`, `stream_options`, and `background` shall be rejected on WebSocket requests.
3. The server shall process at most one in-flight response per connection and shall process multiple accepted turns sequentially without multiplexing.
4. WebSocket output shall use the same event objects, ordering, item lifecycle, sequence semantics, and terminal response objects as SSE, without SSE framing or `[DONE]`.
5. `store:false` continuation state shall be connection-local, unavailable after reconnect, and excluded from persistent storage.
6. When a `store:false` continuation references missing connection-local state, the server shall return a WebSocket error envelope with code `previous_response_not_found`.
7. When a continuation turn fails with a 4xx or 5xx-equivalent error, the referenced connection-local response shall be evicted before a later turn is accepted.
8. Connections shall have a configurable maximum age not exceeding the pinned 60-minute protocol limit and shall emit `websocket_connection_limit_reached` before closure when possible.
9. Per-connection read size, write size, pending events, turn queue, idle time, ping/pong, and total retained state shall be bounded.
10. Disconnect and cancellation shall propagate to the active canonical execution attempt without transparent post-output retry.
11. The frontend WebSocket transport may execute upstream through ordinary HTTP/SSE backends; client-facing WebSocket support shall not require an upstream persistent WebSocket.
12. Origin relaxation for browser compliance testing shall require an explicit development-only configuration and shall not be enabled by default.

### Requirement 9: Generic Remote OpenResponses Backend Mode

**Objective:** As an operator, I want to connect arbitrary standards-compliant inference providers and routers without writing provider code.

#### Acceptance Criteria

1. The standard distribution shall provide a dependency-free built-in-compatible backend factory kind named `custom-openresponses-compatible` or an equivalently reviewed stable kind.
2. Each configured instance shall have an independent runtime ID, backend route prefix, base URL, credentials, model inventory, capability profile, limits, and diagnostics provenance.
3. The backend shall support remote non-streaming creation, SSE creation, and standalone compaction over HTTP using the pinned OpenResponses wire profile.
4. The backend shall use one validated endpoint descriptor for create, compact, and model inventory path joining and shall preserve intentional base-path prefixes.
5. The backend shall support authenticated and explicitly unauthenticated endpoints without emitting an empty authorization header.
6. Credential references and pooling shall use shared secret-aware infrastructure and shall not require literal secrets in YAML.
7. The backend shall parse all pinned standard events it claims to support and shall surface unknown provider-prefixed events as bounded opaque canonical extension events.
8. Provider or protocol errors shall be classified into pre-output recoverability, rate-limit/auth credential state, terminal model failure, and post-output committed failure without hiding retries from core.
9. The backend shall advertise operation, transport, item, reasoning replay, phase, extension, continuation-materialization, and compaction capabilities honestly and may vary them by model.
10. A request requiring an unsupported field, item, tool, extension, phase, or replay dialect shall fail before opening the upstream request.
11. The initial generic connector shall not require or automatically pool upstream WebSocket connections; a later upstream WebSocket optimization shall require separate lifecycle and session-affinity revalidation.
12. OpenRouter-specific attribution headers, provider ordering, fallback semantics, catalog, billing, and proprietary top-level controls shall remain in the OpenRouter connector, which may reuse the shared codec.

### Requirement 10: Configuration, Diagnostics, and Security

**Objective:** As an operator, I want strict configuration and bounded observability, so that protocol support does not introduce route ambiguity, secret leakage, or unbounded state.

#### Acceptance Criteria

1. Frontend configuration shall strictly validate base path, supported profile, WebSocket enablement, origin policy, connection limits, continuation persistence, TTL, and storage bounds.
2. Backend configuration shall strictly validate route prefix, endpoint, credential references, inventory, declared extension slugs/keys, capability overrides, and request/stream limits.
3. Unknown configuration fields shall fail with the frontend or backend instance identified.
4. Base URLs shall be absolute `http` or `https`, require a host, reject userinfo and fragments, and use deterministic path joining.
5. Diagnostics shall expose sanitized endpoint identity, protocol profile, route ownership, enabled transports, continuation mode, capability declarations, inventory state, and conformance status without request content, response content, raw opaque payloads, credentials, or full client identifiers.
6. Logs and metrics shall use bounded reason codes and cardinality-safe protocol/backend labels rather than response IDs, item IDs, model prompts, extension payloads, or arbitrary provider error strings.
7. Request, response, SSE event, WebSocket message, item count, content part, JSON depth, string, metadata, annotation, extension, and reconstructed continuation sizes shall be bounded independently.
8. Authentication and authorization shall occur before body processing, WebSocket state allocation, continuation lookup, or backend execution.
9. Client-controlled metadata, `user`, `safety_identifier`, prompt-cache fields, and proprietary extensions shall follow explicit forwarding/redaction policy and shall never become routing or session authority.
10. Shutdown and runtime reload shall close WebSockets, cancel active attempts, flush or discard continuation state according to persistence policy, and release resources exactly once.
11. Reload shall reject route or storage changes that cannot be applied atomically without orphaning active connections or continuation ownership.
12. Security review shall cover cross-tenant response-ID probing, continuation fixation, extension smuggling, WebSocket origin abuse, decompression/amplification, event injection, and sensitive opaque replay material.

### Requirement 11: Licensed Implementation Strategy and Dependency Boundaries

**Objective:** As a maintainer, I want an auditable permissively licensed implementation, so that protocol support does not depend on unavailable or legally unsuitable code.

#### Acceptance Criteria

1. The official OpenResponses specification, schema, examples, and compliance fixtures may be used under their verified Apache-2.0 license with required notices and pinned provenance.
2. The implementation shall not depend on a Go package without a publicly auditable source repository and an explicit MIT, BSD, Apache-2.0, ISC, or equivalently approved license.
3. The implementation shall not adopt an untagged third-party Go module as a production dependency unless a reviewed exception verifies immutable source, provenance, maintenance, license, security, API fit, and conformance.
4. The existing official OpenAI Go SDK shall remain confined to OpenAI backend packages and shall not define the OpenResponses frontend, generic backend, canonical item, compaction, or WebSocket contracts.
5. The default design shall use project-owned Go wire types and codecs generated from or manually aligned to the pinned official schema, standard HTTP/JSON/SSE primitives, and the already approved WebSocket dependency.
6. Generated or copied schema-derived material shall be reproducible, reviewable, minimal to the supported profile, and accompanied by license and source-commit metadata.
7. Provider SDK types, OpenResponses wire types, and third-party library types shall not appear in `pkg/lipapi`, core ports, plugin ABI DTOs, or unrelated frontend/backend packages.
8. A future official or mature permissively licensed Go SDK may replace internal wire plumbing only after passing the same golden, fuzz, conformance, cancellation, extension, and dependency-boundary tests without changing public architecture.
9. Architecture tests shall prohibit dependencies from core to concrete OpenResponses adapters and prohibit the generic adapter from importing provider-specific connector packages.
10. Root builds shall remain valid with `GOWORK=off`, no external connector modules, and no JavaScript runtime required for normal build or unit tests.

### Requirement 12: TDD, Conformance, Migration, and Delivery Gates

**Objective:** As a maintainer, I want an executable brownfield migration with official conformance evidence, so that a large protocol addition cannot regress existing adapters or routing guarantees.

#### Acceptance Criteria

1. Interfaces, canonical item contracts, capability rules, route ownership tests, golden wire fixtures, and failing conformance tests shall be committed before production implementations.
2. The test suite shall include official-profile HTTP basic response, assistant phase, response phase schema, SSE, system prompt, function tool, image input, multi-turn, compaction, missing compact model, and all pinned WebSocket scenarios.
3. Official conformance cases shall be pinned to an immutable upstream commit and mirrored or invoked in CI without downloading mutable code at test time.
4. Go-native golden tests shall validate every required response field, event discriminator, sequence, item lifecycle, error envelope, null-presence rule, and compact resource field.
5. Differential tests shall prove existing OpenAI Responses behavior is unchanged and shall document every intentionally shared low-level helper and every protocol-specific divergence.
6. Fuzz tests shall cover request unions, item/content/tool discriminators, unknown extensions, SSE framing, event ordering, response aggregation, compaction payloads, and WebSocket message handling.
7. Race and leak tests shall cover HTTP cancellation, slow consumers, failed writes, WebSocket disconnects, queued turns, continuation eviction, runtime reload, shutdown, and backend stream termination.
8. Integration tests shall prove no retry after client-visible output, capability-safe pre-output failover, route-collision rejection, tenant isolation, and backend-extension binding.
9. The active backend connector plugin architecture and generic compatible mode specifications shall be reviewed before implementation; any changed public backend or compaction contract shall update their conformance/ABI fixtures in the same implementation series.
10. Documentation and examples shall distinguish OpenResponses, OpenAI Responses, and the unrelated `open-responses` project and shall show coexistence and canonical-path configurations.
11. The implementation shall not be considered complete until the pinned official compliance suite passes against a reference Go-LIP deployment for every enabled conformance transport.
12. The release gate shall run focused package tests, `go test -race` for changed concurrency packages, architecture tests, `go vet`, root build with `GOWORK=off`, and the official-profile conformance job.
