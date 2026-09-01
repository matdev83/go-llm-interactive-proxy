# Requirements Document

## Introduction

Go-LIP already implements secure proxy-owned sessions, internal backend affinity, protocol-neutral `PromptCacheKey` carriage, prompt-cache residency/control, keep-warm orchestration, executable-backend negotiation, provider profiles, and cache usage evidence. The remaining gap is the **provider-facing locality hint** used before inference: a client may omit a provider-understood conversation/cache-routing key, causing repeated agent turns to reach a cold provider replica or broker endpoint even though Go-LIP itself stayed on the same backend.

This specification closes that gap without redefining any existing authority. The generated value is advisory provider-routing metadata only. It is never session authority, continuation authority, cache-residency authority, or proof of a cache hit.

The requirements below are intentionally frozen for implementation by instruction-following executors. Where an observable decision can be made now, it is made here rather than delegated to implementation-time research.

## Requirement 1 — Preserve Existing Authorities

**User Story:** As a proxy operator, I want downstream cache-affinity hinting to improve locality without changing routing, continuation, or cache-residency correctness.

### Acceptance Criteria

1. When a request is processed, Go-LIP shall keep these identities logically independent: proxy session authority, internal route-affinity binding, downstream cache-affinity hint, provider continuation/turn state, and backend-observed prompt-cache residency target/generation.
2. When a downstream hint is generated or projected, Go-LIP shall not use it to authorize/resume a session, choose a backend candidate, identify a residency target, renew a cache, or continue a provider response.
3. When a provider ignores/evicts/rejects an advisory hint, normal inference correctness and existing retry/failover rules shall remain unchanged except for a legitimate provider request-validation error on an explicitly enabled carrier.
4. While failover or parallel routing selects different capable B-legs, each B-leg may receive a provider-scoped derived hint, but shall not inherit another B-leg's provider cache handle, continuation state, or residency observation.
5. When existing prompt-cache residency/control or keep-warm logic runs, it shall continue to consume only backend-observed residency profiles/observations/handles and shall not infer residency from hint emission.
6. Where archived prompt-cache specs describe backend-observed cache identity, their implemented authority shall remain unchanged.

## Requirement 2 — Use Only Trusted Proxy Conversation Scope for Generated Fallback

**User Story:** As a security-conscious operator, I want generated provider identifiers to be rooted in trusted proxy state rather than arbitrary client metadata.

### Acceptance Criteria

1. When proxy synthesis is eligible, the only generic conversation input for synthesis shall be the admitted `AuthoritativeSessionID` from the existing secure-session/execution view.
2. If no `AuthoritativeSessionID` is available, Go-LIP shall not synthesize a generic fallback value.
3. Go-LIP shall not synthesize a generic fallback from `ClientSessionID`, `ContinuityKey`, `ALegID`, principal/user ID, request/trace ID, IP address, `SafeMetadata`, arbitrary HTTP headers, or a resume token.
4. An explicit client/provider cache-affinity value that is already represented by an existing supported frontend/canonical carrier may still be preserved even when no authoritative session exists; this criterion does not promote that value to proxy session authority.
5. When secure-session policy rejects or cannot establish trusted session authority, cache-affinity synthesis shall not weaken or bypass that decision.
6. The feature shall not add a new session table, affinity store, request-time secure-session lookup, or other persistence solely for cache hinting.

## Requirement 3 — Generate a Stable, Opaque, Provider-Scoped Value

**User Story:** As a user, I want follow-up turns to carry a stable locality key without exposing my internal Go-LIP session identifier.

### Acceptance Criteria

1. When synthesis is applied to the same authoritative session and the same downstream affinity namespace under the same configured secure-session key generation, Go-LIP shall emit the same generated value.
2. When either the authoritative session or downstream affinity namespace differs, the generated value shall differ with cryptographic probability.
3. The generated wire value shall be exactly `lipca1_` followed by the unpadded base64url encoding of a full 32-byte HMAC-SHA256 digest: 50 ASCII characters total.
4. The generated value shall contain neither the raw session ID nor the raw root key and shall use only header/JSON-safe ASCII.
5. The derivation shall be keyed and domain-separated from secure-session resume-token fingerprinting; reusing the same root secret shall not reuse the same HMAC message/domain.
6. When the secure-session fingerprint root key changes, generated affinity values may change and provider cache hit rate may temporarily decrease, but authorization/session correctness shall not change.
7. When secure-session uses its existing ephemeral process-local fingerprint key for memory-store mode, generated affinity values may reset on process restart consistently with that key lifetime.

## Requirement 4 — Preserve Explicit Client Intent Before Generated Fallback

**User Story:** As a coding-agent author, I want my deliberately chosen logical cache scope to win over a generic proxy fallback.

### Acceptance Criteria

1. When the selected provider adapter sees a supported explicit provider-specific affinity carrier already decoded into existing request extension/canonical metadata, it shall preserve that value and shall not replace it with a generated fallback.
2. Otherwise, when `Call.PromptCacheKeyValue()` returns a non-empty explicit protocol-neutral prompt-cache key, that value shall be preserved and shall not be replaced by a generated fallback.
3. Otherwise, when the selected backend explicitly allows proxy synthesis and an authoritative session exists, Go-LIP shall generate the provider-scoped fallback described in Requirement 3.
4. Otherwise, Go-LIP shall emit no newly synthesized affinity value.
5. Conflicting aliases for the existing `PromptCacheKey` semantic shall continue to fail using the existing `PromptCacheKeyValue()` validation contract rather than being resolved by new precedence logic.
6. This feature shall not introduce a new generic raw-header capture subsystem merely to preserve arbitrary inbound provider headers; explicit-provider precedence applies to values already represented by supported frontend/adapter metadata.

## Requirement 5 — Make Synthesis an Explicit Backend/Profile Capability

**User Story:** As an operator using many OpenAI-compatible services, I want hints injected only where the concrete backend contract supports them.

### Acceptance Criteria

1. When the selected backend/profile does not explicitly advertise downstream cache-affinity synthesis support, Go-LIP shall not synthesize a new value.
2. Generic OpenAI compatibility alone shall never enable synthesis.
3. The executor-facing capability shall be provider-neutral and shall contain, at minimum, an enable/disable decision and a bounded derivation namespace; core shall contain no provider wire field/header names.
4. Provider-profile configuration shall use one bounded typed `cache_affinity` projection with per-API-flavor entries and explicit `enabled`, `transport`, `wire_name`, `max_length`, and `allow_proxy_synthesis` fields.
5. An enabled profile projection shall fail profile validation if its transport/wire name is invalid, its declared maximum cannot carry the 50-character generated value, or its API flavor conflicts with the profile family.
6. Unknown/undeclared profiles shall default to `cache_affinity` disabled.
7. Provider wire projection shall remain adapter/profile-owned; core shall never switch on provider names or literal values such as `x-grok-conv-id`, `session_id`, `x-session-id`, or `x-session-affinity`.
8. When a configured backend uses `kind: provider-profile`, the complete compiled profile semantics, including `cache_affinity`, shall reach the actual production registry/lifecycle backend construction without being reduced to lossy generic compatible YAML.
9. `internal/providerprofiles` shall remain declarative and shall not import `internal/core/cacheaffinity`; the 50-character minimum may be duplicated as a local boundary constant only if an architecture/cross-package test ratchets equality with the core generated length.

## Requirement 6 — Complete the Initial Provider Matrix in This Workstream

**User Story:** As a proxy user, I want the feature to be useful immediately on high-value coding-agent backends rather than leaving provider wiring to another cache-hint follow-up.

### Acceptance Criteria

1. OpenAI Responses shall project the effective protocol-neutral value to JSON `prompt_cache_key`; direct OpenAI synthesis shall be enabled and its 64-character provider limit shall be enforced.
2. xAI Chat Completions shall project the effective value to HTTP header `x-grok-conv-id` through the provider-profile/OpenAI-compatible family path.
3. xAI Responses shall project the effective value to JSON `prompt_cache_key` through a Responses profile path.
4. Mistral Chat shall project the effective value to JSON `prompt_cache_key` through the provider-profile/OpenAI-compatible family path.
5. OpenRouter shall use **JSON body `session_id` as the single canonical Go-LIP carrier**; it shall preserve existing explicit `openrouter.session_id` first and otherwise use the effective protocol-neutral prompt-cache value. It shall not additionally emit `x-session-id` for this feature.
6. Fireworks shall use its Responses-compatible profile and JSON `prompt_cache_key` as the Go-LIP cache-affinity projection.
7. A RunInfra OpenAI-chat-compatible profile shall use JSON `prompt_cache_key` as the Go-LIP projection; the profile endpoint is `https://api.runinfra.ai/v1` and the Go-LIP credential env-var convention is `RUNINFRA_API_KEY`.
8. Direct Anthropic and direct Gemini shall remain synthesis-disabled by default; their existing cache-control/resource semantics are unaffected.
9. A generic unknown OpenAI-compatible backend shall receive no newly synthesized field or header.
10. If any of `fireworks`, `xai`, `xai-responses`, `mistral`, or `runinfra` is absent from `internal/providerprofiles/catalog.json` on the implementation branch, this workstream shall add the missing row using the frozen endpoint/auth/family data in `research.md`; if a row already exists, this workstream shall augment it rather than duplicate it.
11. Completion of this specification shall not depend on a later provider-expansion implementation to make the matrix above operational.
12. The initial profile-matrix behavior shall be certified through the real `kind: provider-profile` production build path, not only by direct family-builder unit tests.

## Requirement 7 — Reuse Existing Key/Session Composition Without Hot-Path State

**User Story:** As an operator at high concurrency, I want locality optimization to remain cheap and bounded.

### Acceptance Criteria

1. The implementation shall derive its affinity HMAC subkey once during secure-session/runtime composition from the already-resolved secure-session fingerprint root key; it shall not add a second required user-facing secret.
2. Per-request derivation shall perform bounded in-memory work only: no database, filesystem, network, model, tokenizer, or remote cache lookup.
3. Per-request derivation shall not create a goroutine, timer, background worker, session map, or unbounded cache.
4. Capability/profile lookup shall use immutable/generation-owned compiled state.
5. The feature shall not retain full prompts, tool schemas, request bodies, credentials, raw session IDs beyond their existing lifetime, or provider cache contents.
6. Ordinary metrics/logs shall never contain the raw generated hint, raw `PromptCacheKey`, raw session ID, HMAC root/subkey, or residency handle.

## Requirement 8 — Preserve Retry, Failover, and Continuation Semantics

**User Story:** As a user, I want cache locality improvements without changing B2BUA behavior.

### Acceptance Criteria

1. When a pre-output retry targets the same capable downstream namespace, deterministic derivation shall produce the same generated value for that session/namespace.
2. When failover targets a different downstream namespace, Go-LIP shall derive that namespace's value independently.
3. Parallel arms shall not share mutable cache-affinity state.
4. The generated affinity value shall not be used as `previous_response_id`, Codex turn state, WebSocket continuation identity, ACP session ID, or transport connection key unless an existing provider adapter independently and explicitly defines such behavior outside this feature.
5. Existing no-retry-after-visible-output rules shall remain unchanged.
6. Existing provider/account/region/downstream affinity enforcement for prompt-cache maintenance shall remain unchanged.

## Requirement 9 — Observe Decisions Truthfully

**User Story:** As an operator, I want to know whether Go-LIP supplied locality hints without confusing hint emission with actual cache reuse.

### Acceptance Criteria

1. Go-LIP shall expose low-cardinality cache-affinity decision metrics using bounded enums for source (`explicit_prompt_cache`, `proxy_generated`, `none`) and outcome (`applied_or_available`, `unsupported`, `disabled`, `invalid`). Provider-specific adapters may additionally classify a preserved explicit provider carrier where they already observe one.
2. Metric labels shall not include actual hint values, session IDs, prompt-cache keys, residency IDs/handles, arbitrary request IDs, or unbounded model IDs.
3. Hint emission shall never be reported as `cache_hit=true` or equivalent evidence.
4. Existing provider-reported cache-read/cache-write/cached-token evidence shall remain the authoritative observation of cache effect.
5. Optional live validation may compare affinity-enabled vs disabled requests, but CI shall not require a deterministic cache-hit improvement from an external provider.

## Requirement 10 — Preserve Executable-Backend Compatibility Without New Proto Fields

**User Story:** As a connector author, I want cache-affinity synthesis to compose with the existing negotiated ABI rather than force a redundant session/cache identity field.

### Acceptance Criteria

1. This feature shall add **no new protobuf invocation field and no new backend-plugin protocol minor solely for the generated value**.
2. Executable backends shall receive the already-derived opaque value through the existing negotiated `prompt_cache_key` semantic-extension/legacy carrier; raw `AuthoritativeSessionID` shall still be scrubbed before ordinary backend `Open` execution.
3. A new optional backend-plugin feature flag `downstream_cache_affinity_v1` shall be added at the existing semantic-extension minimum minor (minor 6) to advertise that a connector can consume the existing prompt-cache semantic as downstream affinity; the feature shall not add DTOs. Individual connectors advertise it only when they implement that contract.
4. A host shall synthesize for an executable connector only when that feature is successfully negotiated.
5. An old peer, a peer lacking the feature, or a connector that does not advertise it shall continue ordinary inference with no proxy-generated fallback.
6. `SafeMetadata` shall not be treated as proxy session authority and shall not carry raw session authority for this feature.

## Requirement 11 — Finish the Generic Architecture With No Cache-Hint Follow-Up

**User Story:** As a maintainer, I want one implementation batch to close the generic proxy-side cache-hinting architecture.

### Acceptance Criteria

1. Completion shall include: secure derivation, executor capability, selected-attempt insertion, direct OpenAI forwarding, non-lossy production provider-profile lifecycle binding, provider-profile schema/compiler/projection, required initial profile rows, OpenRouter connector parity, tests/TCK, observability, documentation, and architecture/performance gates.
2. The implementation shall add regression coverage proving direct OpenAI explicit `PromptCacheKey` forwarding works before/following synthesis.
3. The implementation shall add reusable tests proving unknown-compatible, Anthropic, and Gemini negative behavior.
4. The implementation shall add architecture guards preventing provider-name/wire-name switches in generic core, preventing generated values from becoming residency/session authority, preventing `providerprofiles`→core dependency inversion, and preventing reintroduction of the lossy provider-profile-to-generic-YAML production bridge.
5. The final completion review shall search for unresolved TODOs/placeholders required by this feature and resolve them before archiving the spec.
6. After this specification is complete, adding a newly documented provider carrier shall be ordinary provider-profile/adapter data/code against this completed contract, not a prerequisite generic architecture follow-up.
