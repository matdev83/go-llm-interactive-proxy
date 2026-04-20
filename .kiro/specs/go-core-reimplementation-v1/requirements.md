# Requirements Document

## Project Description (Input)

Create the first **Go** re-implementation of the LLM Interactive Proxy as a greenfield core/runtime that is fundamentally simpler than the current Python system while preserving the product’s most distinctive strengths:

- multi-frontend API compatibility,
- multi-backend API compatibility,
- cross-API translation,
- streaming-first execution,
- **core-owned** routing/failover/load-balancing,
- **core-owned** B2BUA-like orchestration that can swallow recoverable pre-output failures and continue on another backend within the same client request,
- and **plugin-owned** advanced behaviors connected through strict hook surfaces rather than direct dependencies.

The new system must be deliberately **small-core, boundary-first, idiomatic Go**, with a standard distribution that bundles official frontend and backend plugins while keeping the runtime itself free of protocol-specific and provider-specific coupling.

## Scope and boundaries

**Primary target**

Design and implement the first production-capable Go core that can serve the shared prompt/tool/streaming subset across the required protocol matrix, while establishing durable boundaries for future feature growth.

**In scope for this spec**

- Greenfield Go repository and package structure
- Canonical request / event model
- Core runtime, plugin registry, hook bus, configuration, diagnostics
- Client-facing protocol plugins:
  - OpenAI Responses API
  - Legacy OpenAI-compatible API
  - Anthropic Messages API
  - Gemini generateContent-compatible API
- Backend protocol plugins:
  - OpenAI Responses API
  - Legacy OpenAI-compatible API
  - Anthropic Messages API
  - Gemini generateContent API
  - Amazon Bedrock Converse / ConverseStream
  - ACP prompt-turn subset
- Cross-protocol translation via canonical model
- Streaming-first execution, including collector-based non-streaming support
- Core routing, weighted load-balancing, ordered failover, session-aware first-request routing
- Core B2BUA-like request orchestration and attempt lineage
- Reserved hook surfaces for:
  - user request submit hooks,
  - request part altering,
  - response part altering,
  - tool call reactors with rewrite/swallow/replace decisions
- TDD-first implementation plan and conformance suite

**Explicitly out of scope for v1**

- Full feature parity with the Python repository
- OAuth/personal-auth connector families
- Out-of-process plugin sandboxing
- Dynamic tool compression / context compaction
- Dangerous-command protection
- Advanced loop detection
- Persistent multi-node B2BUA state replication
- Full ACP terminal/filesystem/slash-command surface
- Full multimodal parity beyond the declared shared subset
- Production feature plugins beyond no-op/reference hook implementations

## Requirements

### Requirement 1: Small core ownership and boundary isolation

**Objective:** As a maintainer, I want the new Go implementation to have a tiny, explicit core with strong package boundaries, so that future features do not recreate the current coupling and dependency sprawl.

#### Acceptance Criteria

1. When the repository is initialized, the system shall separate canonical contracts, stable plugin SDK, provider-agnostic core runtime, and bundled protocol plugins into distinct Go packages with explicit ownership boundaries.
2. The core runtime shall not import provider SDK packages, bundled protocol plugin packages, or feature-plugin implementation packages.
3. Where behavior can be supplied externally, the system shall expose only narrow typed interfaces and registration contracts rather than direct references to concrete implementations.
4. If a behavior defines request-execution semantics for every call, such as route planning, failover, or B2BUA lineage, then the system shall keep orchestration in the core and isolate policy details behind narrow interfaces.
5. The v1 system shall not depend on Go’s native `plugin` package for runtime extensibility.

### Requirement 2: Canonical request, event, and capability model

**Objective:** As an architect, I want one small canonical model for requests and one small canonical event stream for outputs, so that translation stays linear rather than pairwise.

#### Acceptance Criteria

1. When any supported frontend request is accepted, the system shall decode it into one canonical call model before route planning or backend execution begins.
2. When any supported backend produces output, the system shall normalize that output into one canonical event stream before frontend encoding begins.
3. Where protocol-specific data must be preserved, the system shall isolate that data in explicit vendor-extension fields rather than expanding the core semantic surface without review.
4. If a required request capability cannot be represented or preserved for the selected backend, then the system shall reject the request with a deterministic capability error before the upstream call is started.
5. The system shall implement cross-API translation only through protocol-to-canonical and canonical-to-protocol adapters, not through pairwise protocol-to-protocol translators.

### Requirement 3: Client-facing frontend compatibility

**Objective:** As an operator, I want the Go distribution to expose the required client-facing API surfaces, so that existing tools can point at the new proxy without being rewritten.

#### Acceptance Criteria

1. When the standard distribution starts successfully, the system shall expose an OpenAI Responses-compatible frontend surface.
2. When the standard distribution starts successfully, the system shall expose a legacy OpenAI-compatible frontend surface for chat-style clients.
3. When the standard distribution starts successfully, the system shall expose an Anthropic Messages-compatible frontend surface.
4. When the standard distribution starts successfully, the system shall expose a Gemini generateContent-compatible frontend surface.
5. Where a frontend supports streaming in its native protocol, the system shall expose streaming using that protocol’s legal framing and event semantics.
6. If a request fails before protocol output begins, the frontend shall return an error shape that remains valid for that frontend protocol.

### Requirement 4: Backend protocol compatibility

**Objective:** As an operator, I want the Go distribution to speak the required backend API flavors, so that one frontend can target any supported backend family on the shared subset.

#### Acceptance Criteria

1. When configured, the system shall support an OpenAI Responses backend adapter.
2. When configured, the system shall support a legacy OpenAI-compatible backend adapter.
3. When configured, the system shall support an Anthropic Messages backend adapter.
4. When configured, the system shall support a Gemini generateContent backend adapter.
5. When configured, the system shall support an Amazon Bedrock backend adapter based on Converse / ConverseStream semantics.
6. When configured, the system shall support an ACP backend adapter for the prompt-turn subset defined by this specification.
7. Where an official SDK or reference implementation exists and is suitable, the backend adapter shall use it behind the plugin boundary rather than reimplementing wire behavior ad hoc.

### Requirement 5: Streaming-first execution

**Objective:** As a developer, I want streaming to be the primary execution mechanism, so that protocol behavior, failover rules, and non-streaming support all follow one semantic path.

#### Acceptance Criteria

1. When the core invokes any backend adapter, the backend adapter shall return or emit a canonical event stream as the primary execution result.
2. Where a client requests non-streaming behavior, the system shall produce the non-streaming response by collecting the canonical event stream rather than by using a separate execution pipeline.
3. When a client disconnects or a request context is cancelled, the system shall propagate cancellation through the core and the active backend adapter.
4. If client-visible content has already begun, the system shall not silently retry or fail over to a different backend for the same client response.
5. While the system is waiting during a recoverable pre-output failure path for a streaming request, the system shall emit protocol-legal keepalive output where needed to avoid idle timeouts.

### Requirement 6: Core routing, load balancing, and failover

**Objective:** As an operator, I want dynamic routing and failover to remain a first-class core feature, so that the proxy can continue to improve UX and reliability without moving orchestration into plugins.

#### Acceptance Criteria

1. When parsing route selectors, the system shall support explicit backend-plus-model routing, backend-instance routing, model-only routing, ordered failover chains, weighted routing, and URI-style selector parameters.
2. When an ordered failover selector is used, the system shall attempt candidates left-to-right until the request succeeds or the attempt policy is exhausted.
3. When a weighted selector is used, the system shall choose from eligible weighted candidates using deterministic, testable selection logic.
4. If a weighted candidate fails before client-visible output begins, the system shall be able to exclude the failed candidate and re-select among the remaining eligible weighted candidates within the same client request.
5. Where backend health or temporary exclusion state exists, the route planner shall consider that state before producing the final attempt order.
6. The system shall record the selected route plan and the final surfaced branch for diagnostics.

### Requirement 7: Session-aware first-request routing

**Objective:** As an operator, I want the first request of a session to be steerable independently from later requests, so that onboarding, warmup, or expensive-model gating can be controlled without breaking later weighted behavior.

#### Acceptance Criteria

1. When a weighted selector contains exactly one first-request annotation, the system shall force that branch for the first request of the logical session.
2. When the first request of the logical session has already been consumed, the system shall ignore the first-request annotation and use normal weighted routing.
3. If more than one branch in the same weighted selector is marked as first-request, then the system shall reject the configuration or request deterministically.
4. When a retry or failover path is entered after the initial branch has been selected, the system shall ignore first-request annotations and only consider the remaining eligible candidates.
5. The system shall persist or retain the first-request-consumed state inside the logical session state owned by the core.

### Requirement 8: B2BUA-like multi-attempt orchestration

**Objective:** As a power user, I want one client request to be able to create multiple related backend attempts under one logical exchange, so that recoverable backend failures can be masked and UX can remain smooth.

#### Acceptance Criteria

1. When the core accepts a logical client request, the system shall create or resolve one core-owned A-leg identity for that logical exchange.
2. When the core starts a backend attempt, the system shall allocate a distinct B-leg identity for that attempt and link it to the A-leg.
3. If a backend fails before client-visible output begins and the failure is recoverable under policy, then the system shall be allowed to swallow the failure and continue with another backend attempt within the same client request.
4. Where recovery is attempted, the system shall record attempt order, backend identity, effective model, timing, and recovery reason in the A-leg lineage.
5. When a follow-up client request is recognized as belonging to the same logical session, the system shall be able to reuse the same A-leg identity according to the configured continuity policy.
6. If continuity cannot be resolved safely, then the system shall create a new A-leg rather than guessing across isolation boundaries.

### Requirement 9: User request submit hook surface

**Objective:** As a future feature author, I want a typed submit-hook API before route planning, so that request admission, annotation, and lightweight rewrites can be added later without modifying the core.

#### Acceptance Criteria

1. Before route planning begins, the system shall invoke zero or more registered submit hooks against the canonical call and request metadata.
2. Submit hooks shall receive only canonical and core-owned types, not provider SDK types or frontend-specific request models.
3. A submit hook shall be able to annotate request metadata, rewrite selected canonical fields, reject the request, or pass the request through unchanged.
4. The core shall support deterministic hook ordering and per-hook failure mode selection.
5. The standard distribution shall remain functional when no submit hooks are registered.

### Requirement 10: Request and response part altering hook surface

**Objective:** As a future feature author, I want request-part and response-part hook APIs, so that future integrations can alter canonical content without re-entering the protocol adapters.

#### Acceptance Criteria

1. Before backend translation begins, the system shall allow registered request-part hooks to inspect, insert, replace, or remove canonical request parts.
2. Before frontend encoding begins, the system shall allow registered response-part hooks to inspect, insert, replace, or remove canonical response parts or events.
3. If a hook returns an invalid mutation that violates canonical invariants, then the system shall reject that mutation deterministically and record a typed hook error.
4. Hook interfaces shall operate on canonical parts and events rather than frontend-specific or backend-specific payload types.
5. The v1 standard distribution may ship with no-op part hooks, but the hook contracts and execution points shall exist and be covered by tests.

### Requirement 11: Tool call reactor hook surface

**Objective:** As a future feature author, I want a tool-call reactor API with rewrite/swallow/replace decisions, so that later steering features can be added without reworking the stream engine.

#### Acceptance Criteria

1. When a backend emits canonical tool-call lifecycle events, the system shall expose those events to registered tool reactors through a typed interface.
2. A tool reactor decision shall be able to pass through, rewrite, swallow, or replace tool-call-related output using canonical structures.
3. Tool reactor interfaces shall include enough stream and session context to support future reactor behaviors without depending on global mutable state.
4. The v1 system shall reserve the orchestration path for tool reactors even if the standard distribution ships without policy-heavy reactor implementations.
5. Unless explicitly configured otherwise, tool reactor failures shall fail open and preserve the underlying request flow.

### Requirement 12: Plugin registration, configuration, and capabilities

**Objective:** As a maintainer, I want plugins to register themselves through stable contracts with opaque private configuration, so that the core stays independent from plugin implementation details.

#### Acceptance Criteria

1. When the standard distribution is composed, the system shall register bundled frontends, backends, and hook plugins explicitly in the composition root.
2. The core shall validate plugin identities, mandatory plugin presence, and declared capability sets without importing plugin-private packages or reading plugin-private state.
3. Where a plugin owns private configuration, the core shall pass the plugin its opaque configuration payload without introducing core-owned schemas for plugin-private behavior.
4. The core configuration surface shall define only core-owned settings such as routing, B2BUA, diagnostics, and server wiring.
5. If duplicate plugin IDs or incompatible mandatory contracts are detected at startup, the system shall fail fast with a deterministic startup error.
6. The v1 implementation shall use constructor-based registration and shall not require a reflection-heavy dependency injection container.

### Requirement 13: Observability and diagnostics

**Objective:** As an operator, I want minimal but meaningful diagnostics for routing, B2BUA, and translation decisions, so that the new core is debuggable from day one.

#### Acceptance Criteria

1. When a request enters the system, the core shall assign a trace identifier that is carried through route planning, hook execution, backend attempts, and frontend encoding.
2. The system shall record A-leg and B-leg attempt lineage in structured diagnostics suitable for debugging recoveries and route decisions.
3. Where routing, capability negotiation, or hook execution materially changes request behavior, the system shall emit structured logs or counters describing that decision.
4. The standard distribution shall expose a basic health surface and a minimal attempt-diagnostics surface.
5. Diagnostics shall be obtainable through core-owned records and logs without requiring inspection of plugin-private internals.

### Requirement 14: Idiomatic Go engineering standards

**Objective:** As a Go maintainer, I want the new implementation to follow idiomatic Go patterns, so that the rewrite gains simplicity rather than only changing languages.

#### Acceptance Criteria

1. When implementing request-scoped behavior, the system shall use `context.Context` for cancellation, timeout, and trace propagation.
2. The system shall prefer the Go standard library for HTTP serving, JSON handling, streaming, and structured logging unless a dependency materially reduces protocol risk.
3. The implementation shall avoid package-level mutable globals for runtime request state.
4. Public interfaces shall remain narrow, explicit, and typed; generic catch-all maps shall be isolated to configuration extension points or vendor extensions.
5. The codebase shall be structured as small packages and files with explicit constructors rather than a reflection-heavy dependency injection framework.
6. The production test suite shall run successfully under the Go race detector.

### Requirement 15: TDD, conformance, and migration safety

**Objective:** As a lead developer, I want the rewrite to be test-first and behavior-driven, so that the Go version can replace the Python version without reintroducing semantic regressions.

#### Acceptance Criteria

1. Before implementing a new core package or protocol adapter, the system shall define failing contract tests or golden tests for the intended behavior.
2. The repository shall include a test kit with provider stubs, stream fixtures, and helper assertions for canonical events and protocol output.
3. The v1 implementation shall provide a shared-subset conformance matrix covering each bundled frontend against each bundled backend.
4. Decoder and selector parsers shall be covered by fuzz tests for malformed or adversarial inputs.
5. The migration suite shall include fixtures or goldens derived from the current Python repository’s captures, stream shapes, or documented behaviors where practical.
6. The implementation shall not be marked ready for production migration until the conformance matrix, race tests, and critical fuzz targets are green.
