# Current-State Review, Protocol Research, Dependency Evaluation, and Design Validation

Generated: 2026-07-26T12:00:00+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Repository baseline: `1022e47f4574f5b1cdfa63533d04268f763a32e6`
- Feature: `openresponses-api-support`
- External protocol profile: OpenResponses `2026-04-24`
- External source baseline: `openresponses/openresponses@92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c`
- Change scope: Kiro specification artifacts only
- Workflow completed: repository reconnaissance, external API research, implementation/license review, mandatory brownfield gap analysis, requirements generation, design option evaluation, and design validation
- Implementation readiness: designable and taskable; requirements, design, and tasks remain unapproved in `spec.json`

## Reviewed Steering, Rules, and Adjacent Specifications

The research and SDD follow:

- `AGENTS.md` and `.kiro/AGENTS.md`
- `.kiro/steering/product.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/api-standards.md`
- `.kiro/steering/routing-and-orchestration.md`
- `.kiro/steering/testing.md`
- `.kiro/rules/ears-format.md`
- `.kiro/rules/gap-analysis.md`
- `.kiro/rules/design-discovery-full.md`
- `.kiro/rules/design-principles.md`
- `.kiro/rules/design-review.md`
- `.kiro/rules/tasks-generation.md`
- `.kiro/specs/backend-connector-plugin-architecture/*`
- `.kiro/specs/generic-compatible-backend-modes/*`
- `.kiro/specs/openai-responses-reasoning-preservation/*`
- archived Go-core, transport-hardening, capability-catalog, and reference-backend specifications

The artifact set matches the repository convention:

- `spec.json`
- `requirements.md`
- `gap-analysis.md`
- `research.md`
- `design.md`
- `tasks.md`

## Sources Reviewed

### Official OpenResponses sources

- Specification landing page: <https://www.openresponses.org/>
- Dated specification: <https://www.openresponses.org/specification>
- API reference: <https://www.openresponses.org/reference>
- Official repository: <https://github.com/openresponses/openresponses>
- Dated OpenAPI snapshot: <https://github.com/openresponses/openresponses/blob/92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c/public/openapi/2026-04-24/openapi.json>
- Dated normative source: <https://github.com/openresponses/openresponses/blob/92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c/src/specifications/2026-04-24.mdx>
- Compliance implementation: <https://github.com/openresponses/openresponses/blob/92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c/src/lib/compliance-tests.ts>
- Changelog: <https://github.com/openresponses/openresponses/blob/92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c/CHANGELOG.md>
- License: <https://github.com/openresponses/openresponses/blob/92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c/LICENSE>

### Go implementation candidates

- OpenAI Go SDK: <https://github.com/openai/openai-go>
- `joeychilson/openresponses` package record: <https://pkg.go.dev/github.com/joeychilson/openresponses>
- `webforspeed/openresponses-go`: <https://github.com/webforspeed/openresponses-go>

### Repository implementation reviewed

#### Frontend

- `internal/plugins/frontends/openairesponses/*`
- `internal/plugins/frontends/frontendconfig/*`
- `internal/plugins/frontends/openaiwire/*`
- `internal/plugins/frontends/identitywire/*`
- `internal/plugins/frontends/sessionwire/*`
- `internal/plugins/protocols/openairesponsesitem/*`
- `internal/stdhttp/*`

#### Backend

- `internal/plugins/backends/openairesponses/*`
- `internal/plugins/backends/openaicompat/*`
- `internal/plugins/backends/openaicred/*`
- `internal/plugins/backends/credpool/*`
- `internal/plugins/backends/modeldiscover/*`
- `internal/plugins/backends/streampeek/*`
- `internal/standardplugins/custom_backends.go`

#### Canonical/runtime

- `pkg/lipapi/*`
- `pkg/lipsdk/*`
- `internal/core/capabilities/*`
- `internal/core/execbackend/*`
- `internal/core/routing/*`
- `internal/core/runtime/*`
- secure session, state, accounting, audit, diagnostics, model registry, runtime bundle, and configuration reload packages

## Executive Conclusion

OpenResponses is **not the same API contract** as the OpenAI Responses API currently implemented in Go-LIP.

It is a separately governed, dated, Apache-2.0 interoperability specification based on the OpenAI Responses vocabulary. It deliberately standardizes a portable multi-provider profile and adds or normatively specifies behavior that the current Go-LIP OpenAI Responses adapters do not provide:

- dated protocol profiles;
- an item-first context and output model;
- assistant message phases;
- a complete response-resource schema;
- standalone context compaction;
- persistent sequential WebSocket turns;
- connection-local `store:false` continuation;
- implementor-prefixed extensibility;
- official compliance scenarios for HTTP, SSE, compaction, and WebSocket.

The current adapters provide strong reusable transport and mapping infrastructure but are not an honest compatibility surface. The correct implementation is a **distinct OpenResponses frontend and generic backend wire codec over an additive protocol-neutral ordered item trajectory**, with proxy-owned continuation state and a core-routed compaction operation.

The existing OpenAI Responses frontend/backend must remain separate. Shared code should be limited to low-level, protocol-neutral helpers whose parity is protected by characterization tests.

## OpenResponses Protocol Findings

### 1. Governance and versioning

The specification is published as dated snapshots. The reviewed repository contains at least `2026-01-15` and `2026-04-24` OpenAPI/specification material. The latter adds WebSocket transport, compaction, assistant phase, and related compliance cases.

Implication:

- Go-LIP must pin an immutable upstream commit and dated profile.
- The mutable website and unversioned OpenAPI must not be production inputs.
- A future profile is an explicit compatibility addition, not a silent schema refresh.

### 2. HTTP and SSE

The pinned profile requires JSON request bodies and JSON non-streaming responses. Streaming uses SSE with:

- semantic event objects;
- `event:` equal to the event body's `type`;
- no reliance on SSE `id`;
- terminal literal `data: [DONE]` after the terminal response event.

This resembles the current OpenAI Responses frontend framing, but the required event and response-resource coverage is broader.

### 3. Item-first semantics

OpenResponses defines items as the fundamental unit of context. Items are bidirectional and ordered. The pinned schema includes:

- message items;
- item references;
- reasoning items;
- compaction summary items;
- function calls and function call outputs;
- custom tool calls and outputs;
- multiple hosted/tool-specific item forms inherited by the dated schema.

Every item participates in lifecycle semantics and can carry identity, status, content, call identity, and provider replay data. The current `lipapi.Call` primarily carries messages and folds some non-message items into message parts. That representation cannot preserve the full OpenResponses trajectory without an additive canonical item form.

### 4. Assistant phase

The compliance suite explicitly sends and validates assistant messages with:

- `phase: "commentary"`
- `phase: "final_answer"`

The current canonical message has no phase and currently collapses the developer role into system semantics in the OpenAI Responses decoder. Both distinctions must be preserved for OpenResponses.

### 5. Response resource

The pinned `ResponseResource` has many required fields, including explicit nullable or defaulted fields for status, completion, model, previous response, instructions, output, error, tools, tool choice, truncation, sampling, reasoning, usage, storage, service tier, metadata, and safety/cache controls.

The current OpenAI Responses encoder returns a much smaller object. Reusing that sparse resource would fail schema conformance even when the underlying model output is correct.

### 6. Continuation

`previous_response_id` means the server logically samples over:

`previous input` → `previous output` → `new input`

The server owns loading and preserving this semantic order. On WebSocket, the profile additionally requires connection-local state for `store:false` turns and defines missing-state and failed-continuation eviction behavior.

The current OpenAI Responses frontend lists `previous_response_id` as a known body key but does not map it into `lipapi.Call` or any continuation service. Therefore current behavior is not partial continuation support; it is field acceptance followed by semantic loss.

A proxy must not expose raw upstream response IDs as client authority because doing so would:

- pin routing implicitly without core policy;
- expose provider identity and storage semantics;
- weaken tenant/session isolation;
- break fallback and backend substitution;
- conflate provider state with secure proxy state.

The selected design issues proxy-owned response IDs and stores a bounded canonical trajectory plus backend lineage and replay constraints.

### 7. Compaction

The standalone `POST /responses/compact` operation:

- requires a model;
- accepts ordered input context and selected continuation/cache fields;
- returns a `response.compaction` resource;
- returns a compacted item window rather than a continuation response ID;
- is used to start a new chain on a later create request.

Go-LIP currently has no canonical compaction operation or backend capability. Implementing it as a frontend shortcut to one backend would violate routing and adapter boundaries. It must become a protocol-neutral core-routed operation.

### 8. WebSocket

The pinned WebSocket transport is an alternate transport for the same Responses resource:

- client sends `type: "response.create"`;
- HTTP/SSE-only fields are forbidden;
- one response may be in flight per connection;
- multiple turns are processed sequentially;
- output uses the same event objects without SSE framing;
- `store:false` continuation can use connection-local state;
- a continuation attempt that fails with a classified 4xx/5xx-equivalent failure evicts referenced local state; disconnects, cancellations, and unrelated transport failures do not evict it;
- maximum connection age is 60 minutes;
- protocol-shaped errors include `previous_response_not_found` and `websocket_connection_limit_reached`.

The frontend can satisfy these semantics while using ordinary canonical execution and upstream HTTP/SSE. Upstream persistent WebSocket pooling is not required for interoperability and would introduce session affinity, credential rotation, reconnect, state synchronization, and failure-ownership complexity.

### 9. Extensions and a specification tension

Normative prose says non-standard items and tools must be prefixed with an implementor slug, such as `openai:web_search_call` or `acme:custom_document_search`.

The dated generated OpenAPI schema also includes many unprefixed OpenAI-derived hosted tools and item types and contains `x-openai-*` source annotations. This creates a practical tension between:

- the portable, provider-neutral extension rule; and
- compatibility with recognized types copied into the dated schema.

Selected precedence/profile rule:

1. Normative BCP-14 prose defines portability and extension policy.
2. The dated OpenAPI defines required field presence and the recognized schema vocabulary for the pinned profile.
3. The official compliance suite defines executable minimum behavior.
4. Portable common types receive project-owned typed representations.
5. Recognized dated but provider-derived unprefixed types are accepted only behind an exact advertised capability/dialect.
6. Newly encountered unknown non-standard types must be implementor-prefixed and are carried opaquely.
7. No unknown extension may cross to a different implementor candidate.

This rule must be encoded in tests and documented as the Go-LIP OpenResponses profile rather than improvised per provider.

### 10. Background and conversation controls

The dated request/resource schema contains `background` and `conversation` fields, but the reviewed OpenResponses compliance suite does not establish a complete asynchronous job-resource API for the proxy to implement. The initial Go-LIP profile should reject unsupported background/conversation modes explicitly rather than accept and ignore them.

## Existing Go-LIP OpenAI Responses Frontend

### Current behavior

The existing frontend:

- owns `POST /v1/responses`;
- exposes an OpenAI-specific cancel route;
- supports JSON and SSE;
- requires `model` and `input`;
- parses message, function-call, function-output, and reasoning forms;
- stores the wire model in an OpenAI-specific extension;
- captures selected OpenRouter request fields and headers;
- maps to `OperationOpenAIResponses`;
- emits a sparse OpenAI response object and an SSE `[DONE]` marker.

### Material differences from OpenResponses

- No distinct dated profile.
- No configurable base path.
- No route ownership descriptor.
- No compact endpoint.
- No WebSocket endpoint.
- No continuation implementation.
- No assistant phase.
- Developer role is not distinct canonically.
- No ordered item authority.
- No video or compaction item.
- Narrow response object.
- Incomplete item/content lifecycle event synthesis.
- No generic unknown extension event preservation.

### Reuse boundary

Safe candidates for extraction after characterization:

- bounded body reading;
- generic SSE line writing;
- response/item ID generation helpers if made protocol-neutral;
- selected content conversion helpers whose semantics are identical;
- executor invocation wrapper;
- common usage conversion where field semantics match.

Unsafe candidates for direct reuse:

- OpenAI wire structs;
- OpenAI model extension keys;
- OpenRouter residual capture policy;
- OpenAI reasoning dialect IDs;
- response resource defaults;
- operation identity;
- event switch behavior that ignores unknown events.

## Existing Go-LIP Responses Backends

### Native OpenAI Responses connector

The native connector uses `github.com/openai/openai-go/v3` and maps canonical calls into OpenAI SDK request unions. It includes:

- official SDK client construction;
- credential pools;
- model-aware capabilities;
- exact OpenAI reasoning replay;
- SSE event adaptation;
- error classification;
- first-event peeking before attempt commitment.

It is a high-quality OpenAI connector, not a generic OpenResponses connector.

### Generic OpenAI-compatible connector

The `openaicompat` package can choose chat or Responses flavor and underlies `custom-openai-responses-compatible`. It still uses OpenAI SDK types and advertises the OpenAI Responses operation.

This is reusable at the infrastructure level but not as the protocol contract. Extending its flavor enum to call OpenResponses would make unknown extensions, compaction, phase, response presence, and versioning hostage to the OpenAI SDK.

### Required backend direction

Add a separate `custom-openresponses-compatible` built-in-compatible mode with:

- project-owned wire request/response/event types;
- shared endpoint, HTTP, credential, retry, inventory, and stream-peeking infrastructure;
- explicit profile and capabilities;
- HTTP JSON, SSE, and compact support;
- bounded unknown implementor-prefixed output events;
- no upstream WebSocket requirement in the initial implementation.

Provider-specific connectors, especially OpenRouter, may reuse the wire codec but retain their own headers, routing, billing, catalog, and extension policy.

## Canonical Contract Research

### Current shape

`lipapi.Call` contains:

- instructions;
- messages;
- function tool definitions and tool choice;
- generation options;
- raw extension map;
- invocation operation/delivery/transport metadata.

Canonical events cover response/message starts, text/reasoning deltas, tool-call lifecycle, usage, warnings, errors, completion, and assistant image/file references.

### Why raw extensions alone are insufficient

Storing the entire OpenResponses request or unknown items under `Call.Extensions` would preserve bytes but not semantics. Core would be unable to:

- derive capabilities;
- enforce provider binding;
- redact or bound content consistently;
- meter or inspect the item trajectory;
- materialize continuation safely;
- project to another backend;
- determine whether failover is legal.

The design therefore adds typed canonical common items and a bounded typed opaque extension carrier. `Call.Extensions` remains appropriate for residual top-level protocol controls after they have explicit ownership, limits, and routing semantics.

### Additive migration rather than flag day

Existing adapters and public code use `Messages`. A full replacement would be disruptive and would make OpenResponses the canonical domain model. The selected migration is additive:

- `Call.Items` is authoritative when non-empty.
- Legacy `Instructions`/`Messages` remain supported.
- Validation rejects ambiguous simultaneous authorities except through an explicit normalized projection.
- Shared walkers expose a common ordered view.
- Existing adapters continue to receive legacy projection until migrated.
- A projector hard-rejects non-representable item semantics.

This preserves compatibility while creating a neutral item-oriented seam.

## Go SDK and Licensing Evaluation

| Candidate | License/provenance | Coverage | Stability | Decision |
|---|---|---|---|---|
| Official OpenResponses repository/schema | Apache-2.0, official repository, immutable commit available | Normative schema, examples, tests; TypeScript tooling rather than Go runtime | Dated snapshots | Accepted as source material |
| `github.com/joeychilson/openresponses` | pkg.go.dev reports MIT; source repository currently not retrievable through GitHub | Reported broad client/server/SSE/WS/compact coverage | Pseudo-version, no stable tag, no known importers | Do not depend; research-only pending auditable source |
| `github.com/webforspeed/openresponses-go` | No repository LICENSE file found | Narrow typed client example, OpenRouter-oriented | Small and incomplete evidence | Rejected |
| `github.com/openai/openai-go/v3` | Apache-2.0, official OpenAI SDK | Mature OpenAI HTTP/SSE; no first-class Responses WebSocket; OpenAI schema | Stable and already used | Retain only for OpenAI connector |
| Project-owned codec | Go-LIP license plus attributed Apache-2.0 schema provenance | Exactly selected OpenResponses profile | Controlled by repository tests | Selected |

### Custom codec generation policy

The wire package may be generated or manually maintained, but it must:

- be reproducible from a pinned source;
- avoid importing a JavaScript runtime into normal build/test;
- contain only the supported profile and required recognized types;
- use discriminated unions with explicit unknown-extension handling;
- preserve absent versus null where required;
- include upstream source commit and license metadata;
- pass Go-native golden and official compliance tests.

A future official or mature Go SDK may replace internal wire plumbing after an explicit dependency and conformance review. The architecture must not depend on that replacement.

## Architecture Options and Decisions

### Decision 1: Separate protocol adapters

**Selected:** OpenResponses receives separate frontend/backend adapter packages and operation identities.

**Rejected:** one OpenAI/OpenResponses adapter with runtime schema guessing.

Reason: route coexistence, versioning, error/default differences, extension policy, and conformance require honest identities.

### Decision 2: Add a neutral ordered item trajectory

**Selected:** additive `lipapi` item contracts with legacy projection.

**Rejected:** encode everything as raw JSON or replace canonical calls with OpenResponses wire structs.

Reason: routing, capability, continuation, audit, and provider independence need semantic neutral contracts.

### Decision 3: Proxy-owned continuation

**Selected:** proxy response IDs and canonical state, with optional native provider IDs as internal optimizations.

**Rejected:** forward client IDs directly upstream or make the frontend sticky to one backend implicitly.

Reason: tenant isolation, routing ownership, failover legality, storage policy, and backend substitution.

### Decision 4: Compaction through core

**Selected:** protocol-neutral compaction operation and capability.

**Rejected:** direct frontend call to the generic OpenResponses backend.

Reason: routing, external backend plugins, accounting, error classification, and provider selection must remain core-owned.

### Decision 5: Client WebSocket termination at Go-LIP

**Selected:** WebSocket frontend over normal canonical execution, initially upstream HTTP/SSE.

**Rejected:** require one persistent upstream WebSocket per client connection.

Reason: lower complexity, broad provider compatibility, no session-affine credential/failover design, same response semantics.

### Decision 6: Hard-bound opaque extensions

**Selected:** typed bounded opaque carriers with implementor/dialect capability requirements.

**Rejected:** silently ignore unknown output events or forward arbitrary body/header maps to any candidate.

Reason: preserves information without cross-provider leakage or unsafe failover.

### Decision 7: Non-colliding default route

**Selected:** `/openresponses/v1` default, with explicit `/v1` takeover only when method/path ownership is free.

**Rejected:** body/header sniffing on `/v1/responses`.

Reason: the same route cannot truthfully promise two independently versioned response schemas.

## Selected Architecture Pattern

The selected pattern is **ports and adapters with a versioned protocol anti-corruption layer and proxy-owned continuation application service**.

- Frontend adapter owns OpenResponses HTTP/SSE/WebSocket wire behavior.
- Core owns routing, capability legality, failover, commitment, state authority, and compaction orchestration.
- Canonical contracts own protocol-neutral ordered items and events.
- Backend adapter owns remote OpenResponses JSON/SSE/compact behavior.
- Provider connectors own proprietary authentication, routing, inventory, billing, and typed extensions.
- Composition root owns route registration, endpoint construction, state store lifecycle, and runtime reload/shutdown.

## Conformance Strategy

### Pinned official cases

The reviewed official suite includes:

- basic response;
- assistant phase input;
- assistant output phase schema;
- SSE streaming;
- WebSocket basic response;
- sequential WebSocket responses;
- WebSocket continuation;
- store-false reconnect recovery;
- missing previous response;
- failed-continuation eviction;
- compact-then-new-WebSocket-chain;
- system prompt;
- function tool calling;
- image input;
- multi-turn history;
- compaction;
- compact request missing model.

### Test layers

1. **Schema golden tests**: request/resource/event/null-presence fixtures.
2. **Canonical tests**: ordered items, projection, capability binding, continuation materialization.
3. **Codec tests**: standard and extension union decoding/encoding.
4. **State-machine tests**: sequence, lifecycle, terminal ownership.
5. **Frontend integration**: HTTP JSON, SSE, errors, route ownership.
6. **Backend integration**: JSON, SSE, compact, unknown extensions, first-event failure.
7. **WebSocket integration**: queueing, local state, eviction, age, disconnect.
8. **Official suite**: pinned JavaScript compliance runner against a reference deployment.
9. **Differential OAI tests**: existing OpenAI Responses behavior unchanged.
10. **Race/leak/fuzz/architecture gates**.

The official suite is necessary but not sufficient because it does not cover all Go-LIP routing, security, extension, state, and cancellation invariants.

## Security Findings

Primary threats:

- cross-tenant response-ID probing;
- response-ID fixation and lineage confusion;
- raw upstream ID exposure;
- extension smuggling across providers;
- oversized opaque reasoning/compaction payloads;
- continuation chain amplification or cycles;
- WebSocket origin abuse;
- unbounded queued turns or connection-local history;
- event injection and inconsistent discriminators;
- sensitive prompts/reasoning in logs or diagnostics;
- reload/shutdown orphaning WebSocket or state ownership.

Required controls:

- opaque high-entropy proxy IDs;
- authenticated tenant/session scoping before lookup;
- same outward missing-ID behavior for absent and unauthorized IDs;
- TTL, depth, byte, item, and chain bounds;
- extension discriminator and exact dialect binding;
- strict route ownership and configuration;
- origin policy and WebSocket resource limits;
- standard redaction and cardinality-safe observability;
- exactly-once state/connection/stream lifecycle.

## Performance and Scale Considerations

- Streaming must remain incremental; no full-response collection on SSE or WebSocket paths.
- Non-streaming and compaction aggregation must use explicit limits.
- Continuation materialization must avoid recursive database round trips and quadratic copying.
- Store canonical normalized snapshots or bounded append records with deterministic flattening limits.
- WebSocket connections are sequential by protocol, so one connection does not require multiplexing locks but needs bounded turn queues and output backpressure.
- Opaque extension and compaction payloads must be byte-bounded before allocation/copy.
- State persistence and cleanup should be keyed and indexed by tenant/session plus expiry.
- Model inventory refresh remains separate from request and WebSocket execution capacity.

## Adjacent Architecture Revalidation

### Backend connector plugin architecture

Implementation must determine how external provider connectors advertise and execute:

- `openresponses.create` operation support;
- context compaction;
- item and extension dialect capabilities;
- assistant phase;
- continuation materialization and replay constraints;
- opaque extension output events.

If the active ABI cannot express these, its versioned DTOs and conformance harness must advance before an external OpenResponses connector can claim support. Core must not special-case external plugin processes.

### Generic compatible backend modes

The new generic mode follows the same architectural classification as dependency-free compatible aliases:

- it is a built-in protocol-family mode;
- it adds no provider SDK;
- each configured endpoint is an independent backend instance;
- shared endpoint, credential, inventory, admission, and diagnostics infrastructure is reused;
- provider-specific OpenRouter behavior remains external.

The adjacent spec's composed ownership and secret policy should be reused rather than duplicated.

## Design Validation Findings and Corrections

| ID | Severity | Initial risk | Correction in final design |
|---|---:|---|---|
| DV-01 | P0 | Treating OpenResponses as an OAI alias would create false compatibility. | Separate profile, operations, packages, routes, and conformance. |
| DV-02 | P0 | Raw item JSON would bypass core capability and security policy. | Typed neutral common items plus bounded opaque extensions. |
| DV-03 | P0 | `previous_response_id` could leak provider state and bypass routing. | Proxy IDs and canonical materialization; native IDs internal only. |
| DV-04 | P0 | Compaction could become a frontend/backend shortcut. | Core-routed compaction port and capability. |
| DV-05 | P0 | `/v1/responses` collision could be resolved by registration order. | Pre-serve method/path ownership catalog and non-colliding default. |
| DV-06 | P0 | External backend plugin ABI might omit compaction/new capabilities. | Explicit adjacent ABI revalidation and task gate before implementation. |
| DV-07 | P1 | Upstream WebSocket requirement would overcomplicate the first release. | Client WebSocket termination over upstream HTTP/SSE; defer pooling. |
| DV-08 | P1 | Official OpenAPI provider-derived types conflict with extension prose. | Profile precedence and exact capability gating for recognized legacy types. |
| DV-09 | P1 | Dual legacy/item canonical forms could diverge. | One-authority validation, shared ordered walkers, explicit projection. |
| DV-10 | P1 | Shared helper extraction could regress OpenAI Responses. | Characterization and differential parity before refactoring. |
| DV-11 | P1 | `store:false` state could accidentally persist. | Connection-local store with separate type/lifecycle and reconnect loss. |
| DV-12 | P1 | Unauthorized IDs could become an enumeration oracle. | Tenant/session scope before lookup and indistinguishable not-found. |
| DV-13 | P1 | Unknown events could be dropped by current backend switch. | Opaque extension events and output items with bounds and dialect. |
| DV-14 | P1 | Sparse response synthesis would fail official schema. | Profile-specific required-field/default builder. |
| DV-15 | P1 | Third-party Go SDK could be adopted without auditable source/license. | Default custom codec and explicit dependency acceptance gate. |

## Final Validation Verdict

**GO after the documented architecture corrections.**

The implementation is substantial but fits Go-LIP's canonical routing architecture. The final design preserves existing OpenAI Responses behavior, introduces only protocol-neutral canonical concepts, keeps provider behavior out of core, routes compaction normally, owns continuation securely, and uses an auditable permissively licensed source baseline.

## Open Implementation Decisions

The following details may be finalized during Task 1 without changing requirements:

1. Exact package and type names for canonical items and profile wire packages.
2. Whether ordered items live directly on `lipapi.Call` or in a small canonical trajectory envelope referenced by the call.
3. Exact persistence adapter selected for `store:true` response records in the standard distribution.
4. Default TTL, chain depth, and byte limits, provided they are bounded and configurable.
5. Whether protocol profile identifiers use a dated string or a structured family/version value.
6. Exact plugin ABI version increment required for compaction and item capabilities.
7. Exact list of recognized dated provider-derived item/tool types included in the initial capability catalog.
8. Whether schema code is generated by a repository tool or manually maintained from minimized fixtures.
9. Whether native provider response-ID optimization is implemented initially or deferred.
10. Whether the generic remote model inventory uses `/models` when present or static inventory only by default.

The following are not open implementation decisions and require requirements/design revalidation:

- merging OpenResponses and OpenAI Responses into one protocol identity;
- forwarding client response IDs directly upstream;
- bypassing core for compaction;
- using raw full-request tunneling as the canonical representation;
- enabling arbitrary header forwarding;
- allowing extensions to fail over across incompatible implementors;
- requiring upstream persistent WebSocket for the first release;
- adding a dependency with unavailable source or unclear license.
