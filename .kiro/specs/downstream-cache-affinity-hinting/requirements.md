# Requirements Document

## Introduction

Go-LIP already implements provider-neutral prompt-cache residency/control, prompt-cache-key carriage, secure proxy-owned session identity, session/backend routing affinity, executable-backend negotiation, and cache usage/maintenance observation. The remaining gap is the **last downstream hop**: when a coding agent does not supply a provider-understood conversation/cache routing hint, a multi-replica inference service or inference broker may route subsequent turns away from the replica/provider endpoint that owns the warm prefix/KV cache.

This specification completes generic proxy-side cache hinting. The proxy may synthesize a stable opaque conversation-affinity value from trusted conversation identity and a backend adapter may project it only into a provider/model wire field whose semantics are explicitly supported. The feature is an optimization only; it must never become a correctness, authorization, continuation, cache-residency, or failover authority.

The following concepts are deliberately distinct:

- **conversation affinity identity**: the proxy-known stable scope used to derive a fallback downstream hint;
- **client cache/affinity hint**: explicit intent supplied by the client/harness and preserved when supported;
- **effective downstream hint**: the final provider-facing cache-routing value selected after precedence and capability resolution;
- **cache residency target**: backend-observed provider state defined by the existing prompt-cache residency contract.

Implementation of this spec is intended to fully close proxy-supported downstream cache hinting. No generic cache-hinting follow-up is part of the planned scope.

## Requirement 1: Preserve Existing Cache and Session Authorities

**User Story:** As an operator, I want downstream cache affinity to improve cache locality without changing existing session, routing, continuation, or residency semantics, so that optimization cannot corrupt agent execution.

### Acceptance Criteria

1.1. When downstream affinity hinting is enabled, the system shall preserve `AuthoritativeSessionID`, client session hints, A-leg identity, `PromptCacheKey`, continuation identifiers, routing affinity bindings, and backend-observed prompt-cache residency as separate semantic authorities.

1.2. The system shall not treat an authoritative session ID, client session ID, A-leg ID, `PromptCacheKey`, response ID, transport connection ID, or residency target ID as universally interchangeable.

1.3. When the existing prompt-cache residency subsystem reports an effective target, the system shall continue to treat that backend observation as authoritative for cache residency/control and shall not replace it with a synthesized downstream hint.

1.4. When route affinity selects or reuses a backend candidate, downstream hint synthesis shall not alter candidate selection, retry ordering, race semantics, TTFT budgets, health filtering, or no-retry-after-output rules.

1.5. When backend failover legitimately moves a request to another backend/provider, the system may reuse the same conversation-scoped hint value if the new backend explicitly supports the same logical hint semantics, but shall not claim that the old provider cache remained resident.

1.6. If downstream affinity hinting is disabled or unsupported, all existing request behavior shall remain unchanged except for bounded diagnostic/metric absence.

## Requirement 2: Resolve a Stable Proxy Conversation-Affinity Scope

**User Story:** As a client that does not emit cache-routing hints, I want Go-LIP to provide a stable per-conversation fallback identity, so repeated agent turns can be routed toward warm provider caches.

### Acceptance Criteria

2.1. When a trusted proxy-owned authoritative session ID exists, the system shall use that session scope as the preferred source for proxy-generated downstream conversation affinity.

2.2. If no authoritative session ID exists, the system shall use only an explicitly approved non-authoritative fallback source defined by existing session/affinity policy; it shall not silently elevate arbitrary client metadata to proxy authority.

2.3. Where existing routing/session policy rejects missing affinity identity, downstream hint generation shall respect that policy rather than inventing a process-global, user-global, IP-derived, or request-derived substitute.

2.4. The generated affinity scope shall remain stable across ordinary turns of the same logical proxy session and shall change across distinct authoritative sessions.

2.5. The generated affinity scope shall not intentionally merge unrelated concurrent sessions belonging to the same principal, tenant, API key, workspace, or user.

2.6. Branches, subagents, compaction rotations, resumptions, and other conversation lifecycle events shall follow the existing authoritative session/continuity contract rather than provider-specific guesses; the implementation shall add no new hidden conversation lineage authority.

## Requirement 3: Generate an Opaque, Bounded, Privacy-Safe Fallback Hint

**User Story:** As a security-conscious operator, I want generated cache-routing identifiers to be stable but non-sensitive, so provider hints do not leak internal session identifiers or create unsafe identifiers.

### Acceptance Criteria

3.1. When the proxy generates a downstream hint, the wire value shall be deterministic for the selected conversation-affinity scope and stable across turns that share that scope.

3.2. The wire value shall not expose the raw authoritative session ID, resume token, principal ID, API key, tenant secret, database identifier, or other sensitive internal identifier.

3.3. The generated value shall be opaque and cryptographically derived or equivalently non-reversible under the configured proxy secret/keying policy.

3.4. The default generated representation shall fit the strictest supported provider limit in the initial capability set without per-request truncation ambiguity; where a provider has a smaller explicit limit, the adapter shall enforce that limit deterministically.

3.5. Generated hints shall be safe for both HTTP-header and JSON-string transports and shall contain no control characters or unbounded user-controlled text.

3.6. Key rotation or process configuration changes that alter generated hint values shall be treated as optimization cache invalidation only and shall not alter session correctness or authorization.

3.7. The system shall not persist generated downstream hint material as a new independent durable identity when it can be deterministically regenerated from existing trusted scope and configured keying material.

## Requirement 4: Preserve Explicit Client Intent With Deterministic Precedence

**User Story:** As a sophisticated coding agent, I want my explicit cache or affinity hint to remain authoritative when supported, so the proxy does not degrade harness-specific cache scope design.

### Acceptance Criteria

4.1. When a request carries an explicit supported provider-specific affinity field/header, the proxy shall preserve it unless existing security/policy rules explicitly forbid forwarding it.

4.2. When a request carries an explicit protocol-neutral `PromptCacheKey` or equivalent semantic extension that the selected backend supports, the proxy shall prefer that client value over a proxy-generated fallback for the same provider wire semantic.

4.3. The proxy shall not overwrite a non-empty explicit client hint with a generated value merely because the generated value is available.

4.4. When multiple explicit client carriers represent the same provider semantic and conflict, the system shall apply a documented deterministic precedence or reject the conflict; it shall not silently choose based on map/header iteration order.

4.5. When an explicit client cache key has narrower or richer logical scope than the proxy session identity, the proxy shall preserve the client scope rather than coarsening it to the proxy session.

4.6. When a client hint is unsupported by the selected backend, the system shall preserve existing unsupported-field behavior and may independently generate a provider-supported fallback only if that does not reinterpret the unsupported client field as trusted authority.

## Requirement 5: Use Explicit Provider/Model Capabilities, Never Blind Compatibility Injection

**User Story:** As an operator using many OpenAI-compatible and native backends, I want cache-affinity fields emitted only where documented and supported, so generic compatibility does not cause request rejection or semantic drift.

### Acceptance Criteria

5.1. The system shall model downstream cache-affinity projection as an explicit provider/model/candidate capability rather than infer support from "OpenAI-compatible" protocol shape alone.

5.2. A capability shall identify, at minimum, whether proxy synthesis is supported, the logical semantic, transport kind, wire field/header name, maximum length if constrained, and whether client-provided values are accepted/preserved.

5.3. Unknown or undeclared providers shall receive no newly synthesized cache-affinity field or header by default.

5.4. Backend/provider adapters shall own provider wire projection; generic core shall not contain a provider-name switch for `prompt_cache_key`, `session_id`, `x-session-id`, `x-session-affinity`, `x-grok-conv-id`, or future vendor fields.

5.5. Capability resolution shall be able to vary by model/API flavor when one provider exposes different cache-affinity contracts on Chat Completions, Responses, gRPC, broker, or other endpoints.

5.6. A provider capability shall be independently disableable by configuration/operator policy without disabling unrelated prompt caching, cache residency observation, or route affinity.

5.7. Provider capability declaration shall not imply deterministic cache residency, cache lifetime, cache-hit guarantee, or safe active renewal.

## Requirement 6: Support the Complete Initial Provider Projection Set

**User Story:** As a Go-LIP user, I want useful provider-specific cache routing to work out of the box for documented high-value backends, so I do not need to patch my coding agent.

### Acceptance Criteria

6.1. Where current OpenAI Responses support declares `prompt_cache_key`, the backend shall be able to project the effective downstream hint into that request field without altering unrelated request fields.

6.2. Where current xAI Chat Completions support declares `x-grok-conv-id`, the backend shall be able to project the effective downstream hint into that header.

6.3. Where current xAI Responses support declares `prompt_cache_key`, the backend shall be able to project the effective downstream hint into that request field.

6.4. Where current Mistral support declares `prompt_cache_key`, the backend shall be able to project the effective downstream hint according to the supported API flavor.

6.5. Where current OpenRouter support declares sticky session routing, the backend shall be able to project the effective downstream hint through the documented `session_id` request field or `x-session-id` header, with one canonical projection choice per adapter path.

6.6. Where Fireworks/RunInfra-compatible backend profiles explicitly declare supported affinity carriers, their adapters/profiles shall be able to project the same protocol-neutral effective hint without adding vendor semantics to core.

6.7. Direct Anthropic and direct Gemini shall not receive a generic synthesized session-affinity field merely because they support prompt caching; only documented provider-specific affinity mechanisms may be enabled.

6.8. If a provider contract changes, capability/profile tests shall fail until the provider adapter is intentionally updated; silent broadening through generic compatibility code is forbidden.

## Requirement 7: Keep Hint Generation and Projection on the Hot Path Cheap and Stateless

**User Story:** As an operator serving high concurrency, I want cache hinting to add negligible overhead, so an optimization does not become a proxy bottleneck.

### Acceptance Criteria

7.1. Effective hint resolution shall require no database read/write, network call, provider discovery request, background job, or LLM call on the per-request hot path.

7.2. The implementation shall not spawn a goroutine per request or per session solely for downstream affinity hinting.

7.3. Deterministic hint generation shall use bounded allocations and may reuse immutable/configured derivation state.

7.4. Capability/profile lookup shall reuse existing compiled backend/provider configuration or generation-local capability structures rather than query mutable global registries repeatedly.

7.5. The feature shall not create unbounded process-global maps keyed by session or generated hint.

7.6. Cache-hint observability shall use bounded-cardinality dimensions and shall not label metrics with raw session IDs, raw generated hints, prompt cache keys, or provider cache targets.

## Requirement 8: Maintain Correct Retry, Failover, Race, and Continuation Behavior

**User Story:** As a coding-agent user, I want affinity hints to improve locality without making requests sticky beyond safe routing semantics, so failures still recover correctly.

### Acceptance Criteria

8.1. A downstream affinity hint shall be advisory; provider rejection, cache miss, eviction, or loss of locality shall not by itself make a request semantically invalid.

8.2. Pre-output failover shall remain allowed according to existing B2BUA policy even when the failed attempt carried an affinity hint.

8.3. Parallel-race attempts may carry the same logical conversation hint to independently capable providers, but no shared hint shall imply shared cache state or cross-provider continuation authority.

8.4. The first client-visible canonical output shall continue to commit the winning attempt exactly as before; cache hinting shall not introduce post-output failover.

8.5. WebSocket/Responses continuation state, `previous_response_id`, provider turn-state tokens, and transport connection reuse shall remain governed by their existing scoped contracts and shall not be synthesized from the downstream cache-affinity hint.

8.6. Per-turn sticky-routing tokens that are valid only inside a provider turn shall never be stored or replayed as cross-turn downstream affinity hints.

## Requirement 9: Provide Truthful Observability and Empirical Validation

**User Story:** As an operator, I want to know whether synthesized hints are actually helping, so the feature can be evaluated rather than assumed beneficial.

### Acceptance Criteria

9.1. The system shall expose bounded diagnostics/metrics distinguishing at least: no hint, explicit client hint preserved, proxy fallback generated, provider projection unsupported, and provider projection applied.

9.2. The system shall not claim a cache hit solely because an affinity hint was generated or projected.

9.3. Existing provider cache-read/cache-write/cached-token evidence shall remain the source of truth for observed cache effects when available.

9.4. The implementation shall support controlled tests comparing identical multi-turn request sequences with synthesized hinting enabled versus disabled without changing model prompt semantics.

9.5. Provider integration tests shall verify request-wire projection and, where practical behind opt-in credentials, observed cache-effect evidence; unit/CI correctness shall not depend on live provider credentials.

9.6. Metrics shall permit operators to correlate cache-hit/read improvements with hinting mode at backend/provider/model-class granularity without high-cardinality session identifiers.

## Requirement 10: Preserve ABI, Plugin, Configuration, and Compatibility Boundaries

**User Story:** As a plugin/backend maintainer, I want this feature added through existing extension points, so old connectors remain compatible and new providers can opt in without Cartesian complexity.

### Acceptance Criteria

10.1. If the existing backend-plugin ABI already transports sufficient proxy-owned session and prompt-cache semantic information, implementation shall reuse it rather than add a redundant ABI identity field.

10.2. If an additive ABI capability is still required for downstream-affinity projection metadata, it shall use the existing negotiated feature/minor mechanism and old peers shall remain inference-compatible without the feature.

10.3. Executable connectors shall not derive trusted proxy session authority from `SafeMetadata` or arbitrary client headers when the host can supply negotiated proxy-owned session authority.

10.4. Provider-specific projection data shall remain at backend/connector/profile boundaries and shall not leak vendor enums or headers into `pkg/lipapi` canonical trajectory types unless an existing protocol-neutral semantic carrier is insufficient and a separate architecture review explicitly approves a change.

10.5. The implementation shall use reusable contract/TCK coverage for hint precedence, capability resolution, projection, and legacy behavior rather than require every frontend × backend combination to have bespoke tests.

10.6. Configuration shall document defaults, operator overrides, supported provider behaviors, and the distinction between route affinity, downstream affinity hints, prompt-cache residency, and keep-warm control.

## Requirement 11: Complete the Feature Without Hidden Follow-Up Scope

**User Story:** As a maintainer planning the OSS release, I want this implementation batch to finish generic proxy cache hinting, so no known architectural tail is deferred into another feature request.

### Acceptance Criteria

11.1. The implementation shall include the protocol-neutral effective-hint resolver, secure opaque derivation, provider/model capability contract, provider wire projection, precedence rules, configuration, observability, unit/TCK coverage, and initial documented provider rollout defined by this specification.

11.2. The implementation shall reconcile any current production code or documentation that conflates session affinity, prompt-cache key, and cache-residency identity.

11.3. The implementation shall preserve and integrate with the completed prompt-cache residency and keep-warm subsystems rather than reopen their scheduler/control scope.

11.4. Final review shall verify there is no remaining generic provider-agnostic cache-hinting prerequisite, TODO, placeholder provider switch, or required follow-up spec left by this work.

11.5. Future support for a newly documented provider-specific affinity field shall be ordinary provider/profile extension work against the completed capability contract, not evidence that this generic feature remains incomplete.

11.6. The final implementation shall be considered complete only after repository quality, architecture, compatibility, and focused performance/race tests pass and the documentation accurately describes supported and unsupported behavior.
