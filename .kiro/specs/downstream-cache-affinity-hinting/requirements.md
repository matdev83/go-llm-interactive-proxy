# Requirements Document

## Introduction

AIProxer already has secure proxy-owned sessions, internal route affinity, protocol-neutral `PromptCacheKey` carriage, prompt-cache residency/control, keep-warm orchestration, provider profiles, executable-backend negotiation, and provider-reported cache-usage evidence. The remaining gap is a provider-facing locality hint for the case where a client omits a provider-understood conversation/cache-routing key.

This SDD adds that optimization **without adding another optional feature implementation to generic core**. It targets the post-`pre-oss-core-slimming` and post-`core-feature-ownership-full-closure` architecture: optional cache-affinity derivation/policy/telemetry belongs to a standard feature; generic core owns only reusable extension execution and bounded backend capability facts.

The generated value is advisory routing metadata only. It is never session authority, continuation authority, cache-residency authority, or evidence of a cache hit.

## Requirement 1 — Preserve Existing Authorities

**User Story:** As a proxy operator, I want downstream cache-affinity hinting to improve locality without changing routing, continuation, billing, or cache-residency correctness.

### Acceptance Criteria

1. Proxy session authority, internal route-affinity binding, downstream cache-affinity hint, provider continuation state, and backend-observed prompt-cache residency shall remain logically independent.
2. A generated/projected hint shall not authorize or resume a session, choose a route/backend, identify a residency target, renew a cache, or continue a provider response.
3. Provider rejection/ignoring/eviction of the hint shall not alter normal retry/failover semantics except for a legitimate request-validation error from an explicitly enabled projection.
4. Each B-leg shall derive independently from its selected backend namespace; no mutable hint state, provider cache handle, or continuation state may be shared across parallel/failover legs.
5. Residency/control/keep-warm shall continue to consume only backend-observed profiles, observations, targets, generations and handles. Hint emission is not residency evidence.

## Requirement 2 — Conform to the Lean-Core Ownership Model

**User Story:** As a maintainer, I want this optional optimization to extend the proxy without regrowing `internal/core` or generic runtime composition.

### Acceptance Criteria

1. Production cache-affinity derivation, source/outcome policy, decision logic, and feature-specific telemetry shall live under `internal/plugins/features/downstreamcacheaffinity` (or the mechanically equivalent post-rebrand path), not under `internal/core`.
2. Standard-distribution construction shall be owned by the post-full-closure `internal/standardplugins/featurehost` boundary. `runtimebundle` shall not construct or switch on the cache-affinity feature directly.
3. The feature shall reuse the existing `PlaneAttemptTransforms` candidate-aware extension seam. It shall not add a cache-affinity-specific extension plane or a cache-affinity-specific executor stage.
4. `runtime.SecurityRuntime`, `runtime.Executor`, `runtimebundle.ProcessServices`, and `pkg/lipruntime.Options` shall gain no cache-affinity-specific field/service.
5. `execbackend.Backend` shall gain no cache-affinity-specific resolver or callback.
6. If current post-full-closure code lacks a way for an attempt transform to learn bounded backend semantic-feature support, the implementation may add **one generic immutable backend-feature metadata seam** shared by in-process and executable backends. It must be a bounded value-only capability list, not a service registry, dynamic lookup system, feature switchboard, or request-time callback.
7. The generic metadata seam, if needed, shall be projected into `request.AttemptMeta` and shall have independent justification from the already-existing executable-backend feature-negotiation model. Core shall not know provider wire names or cache-affinity policy.
8. Feature-specific metrics shall be emitted by a feature-owned observer/adapter composed by `featurehost`; no `internal/core/runtime` metrics interface shall gain cache-affinity methods.
9. The implementation shall add architecture ratchets forbidding reintroduction of `internal/core/cacheaffinity`, `SecurityRuntime.DownstreamCacheAffinity*`, `execbackend.Backend.ResolveDownstreamCacheAffinity`, and an executor-local cache-affinity helper/stage.
10. Production implementation shall not begin until `core-feature-ownership-full-closure` is implemented/certified on `main`. Its final core-admission manifest/ratchets are authoritative over stale package assumptions in this SDD.

## Requirement 3 — Use Only Trusted Proxy Conversation Scope

**User Story:** As a security-conscious operator, I want generated provider identifiers rooted in trusted proxy state rather than arbitrary client metadata.

### Acceptance Criteria

1. The only generic conversation input for synthesis shall be the admitted `AuthoritativeSessionID` supplied to the attempt transform through the existing authoritative `SessionView`.
2. No authoritative session means no generated fallback.
3. Synthesis shall never use `ClientSessionID`, `ContinuityKey`, `ALegID`, principal/user ID, request/trace ID, IP address, `SafeMetadata`, arbitrary HTTP headers, or resume tokens.
4. Explicit supported client/provider affinity already represented by canonical/owned adapter metadata may be preserved without an authoritative session; it remains non-authoritative.
5. The feature shall add no session table, affinity store, request-time secure-session lookup, or other persistence.

## Requirement 4 — Generate a Stable, Opaque, Provider-Scoped Value

**User Story:** As a user, I want follow-up turns to carry a stable locality key without exposing internal session identity.

### Acceptance Criteria

1. Same authoritative session + same downstream namespace + same key generation shall produce the same value; changing session or namespace shall change it with cryptographic probability.
2. The generated value shall be exactly `aipca1_` plus the unpadded base64url encoding of a full 32-byte HMAC-SHA256 digest: **50 ASCII characters total**. Do not truncate.
3. The value shall contain neither raw session ID nor root/subkey material and shall use only header/JSON-safe ASCII.
4. Key derivation shall be domain-separated from secure-session fingerprint/resume-token use. The frozen domains are `aiproxer/downstream-cache-affinity/key/v1\x00` and `aiproxer/downstream-cache-affinity/value/v1\x00`.
5. No new user-facing secret shall be introduced. Standard feature composition shall request a feature subkey from a narrow domain-key derivation capability backed by the already-resolved secure-session fingerprint root; the feature shall not receive or retain that root.
6. Changing the secure-session fingerprint root may change generated values and cache hit rate but shall not alter authorization/session correctness.
7. Memory-store process-local key lifetime may reset generated affinity on restart consistently with the existing secure-session key lifetime.

## Requirement 5 — Preserve Explicit Intent Before Generated Fallback

**User Story:** As a coding-agent author, I want my deliberate cache scope to win over a proxy-generated fallback.

### Acceptance Criteria

1. A supported explicit provider-specific carrier already owned by an adapter shall win according to that adapter's existing documented precedence.
2. Otherwise, a non-empty `Call.PromptCacheKeyValue()` shall be preserved unchanged.
3. Otherwise, the feature may synthesize only when the selected backend advertises the generic `downstream_cache_affinity_v1` capability, has a stable bounded namespace/prefix, and an authoritative session exists.
4. Otherwise no value shall be synthesized.
5. Existing `PromptCacheKeyValue()` alias-conflict validation remains authoritative; the feature shall not invent new alias precedence.
6. The attempt transform shall be ordered after existing standard attempt transforms and shall be **fill-only**: it never overwrites a non-empty effective PCK.
7. Before implementation, characterization shall prove no current post-attempt-transform/request-part-hook stage writes or replaces `PromptCacheKey`. If such a later writer exists after the full-closure migration, STOP and revise the SDD rather than accepting generated-vs-explicit precedence inversion.
8. Tests shall prove the generated PCK survives normal request hooks, candidate adaptation and the final raw-session scrub to the selected backend serializer.

## Requirement 6 — Make Provider Projection and Synthesis Explicit Capabilities

**User Story:** As an operator using many compatible services, I want wire injection only where the concrete backend contract supports it.

### Acceptance Criteria

1. Generic OpenAI compatibility alone shall never enable downstream cache-affinity projection or synthesis.
2. Built-in/profile/executable backends that allow synthesis shall advertise the generic bounded backend feature `downstream_cache_affinity_v1`; absence means no generated value.
3. The synthesis namespace shall be a stable backend prefix/profile identity already owned by backend composition. Empty/ambiguous namespace means synthesis disabled.
4. Provider-profile configuration shall use one typed `cache_affinity` projection with per-API-flavor `enabled`, `transport`, `wire_name`, `max_length`, and `allow_proxy_synthesis` fields.
5. An enabled projection shall fail validation for invalid transport/wire name, a bound below 50 characters, or API-family mismatch.
6. Unknown/undeclared profiles default disabled.
7. Provider wire projection remains backend/profile/connector-owned; generic core and the cache feature shall contain no provider wire-name switches.
8. Configured `kind: provider-profile` rows shall preserve complete compiled semantics through the actual production registry/lifecycle path. They shall not be reduced to lossy generic compatible YAML.
9. `internal/providerprofiles` shall remain declarative and shall not import the feature implementation. It may define local `MinCacheAffinityValueLength = 50`, with an architecture test pinning that value to the feature's generated length.
10. If the bulk-provider implementation has already repaired the provider-profile lifecycle on the implementation base, this SDD shall verify/reuse that repair instead of duplicating it. If this SDD lands first, the corresponding bulk-provider task becomes verification-only.

## Requirement 7 — Complete the Initial Provider Matrix

**User Story:** As a proxy user, I want the feature to be operational on the highest-value documented cache-affinity paths immediately.

### Acceptance Criteria

1. Direct OpenAI Responses: JSON `prompt_cache_key`, max 64, synthesis enabled, and existing explicit PCK forwarding repaired.
2. xAI Chat profile: HTTP `x-grok-conv-id`, max 256, synthesis enabled.
3. xAI Responses profile: JSON `prompt_cache_key`, max 64, synthesis enabled.
4. Mistral Chat profile: JSON `prompt_cache_key`, max 256, synthesis enabled.
5. Fireworks Responses profile: JSON `prompt_cache_key`, max 256, synthesis enabled.
6. RunInfra Chat profile: JSON `prompt_cache_key`, max 64, synthesis enabled; base `https://api.runinfra.ai/v1`, env `RUNINFRA_API_KEY`.
7. OpenRouter: JSON body `session_id` only, max 256; explicit `openrouter.session_id` wins, otherwise effective PCK; no extra `x-session-id`.
8. Direct Anthropic, direct Gemini and arbitrary unknown custom-compatible backends remain synthesis-disabled.
9. Missing profile rows (`fireworks`, `xai`, `xai-responses`, `mistral`, `runinfra`) shall be added here; existing rows shall be augmented without weakening stricter inventory/capability data.
10. Completion does not depend on later bulk-provider work and shall be certified through the real `provider-profile` production path.

## Requirement 8 — Preserve Executable-Backend Compatibility Without New Value DTOs

**User Story:** As a connector author, I want this feature to reuse the existing semantic carrier and negotiation protocol.

### Acceptance Criteria

1. No new protobuf invocation field and no protocol-minor bump shall be added for the generated value.
2. The existing prompt-cache semantic/legacy carrier shall carry the already-generated PCK to an executable backend.
3. Add optional negotiated feature `downstream_cache_affinity_v1` at the existing semantic-extension minimum minor (6); connectors advertise it only when they consume PCK as downstream affinity.
4. The backend-plugin host adapter shall translate successful negotiation into the same generic immutable backend-feature metadata used by in-process backends; it shall not add a cache-specific `execbackend.Backend` callback.
5. Old peers/lacking-feature peers remain synthesis-disabled.
6. Raw authoritative session identity remains scrubbed before backend `Open` and is never sent to the connector for derivation.

## Requirement 9 — Keep Hot-Path Work Bounded

**User Story:** As an operator at high concurrency, I want the locality optimization to have negligible shared-state cost.

### Acceptance Criteria

1. Feature subkey derivation occurs once during standard feature process/generation composition; per attempt performs at most one value HMAC and one base64url encoding when synthesis is needed.
2. No DB, filesystem, network, model, tokenizer, remote cache lookup, goroutine, timer, session map or unbounded cache is permitted in the attempt transform.
3. Backend-feature lookup uses immutable request-pinned metadata and is O(small bounded feature count).
4. The feature shall not retain prompts, tools, request bodies, raw session IDs beyond existing request lifetime, credentials or cache contents.
5. Metrics/logs shall never contain the raw generated hint, PCK, raw session ID, root/subkey, or residency handle.

## Requirement 10 — Observe Decisions Truthfully

**User Story:** As an operator, I want to know whether the proxy supplied locality hints without confusing that with actual cache reuse.

### Acceptance Criteria

1. Feature-owned telemetry shall expose bounded source (`explicit_prompt_cache`, `proxy_generated`, `none`) and outcome (`applied_or_available`, `unsupported`, `disabled`, `invalid`) plus the existing bounded backend-label convention.
2. No actual hint/session/PCK/residency/request value may become a metric label/log field.
3. Hint emission shall never be reported as cache-hit evidence.
4. Provider-reported cache read/write/cached-token evidence remains authoritative.
5. Optional live validation may compare enabled/disabled behavior, but CI shall not require deterministic external cache improvement.

## Requirement 11 — Finish the Generic Architecture With No Cache-Hint Follow-Up

**User Story:** As a maintainer, I want one implementation program to close the generic cache-affinity architecture and leave the lean-core ratchets intact.

### Acceptance Criteria

1. Completion shall include: post-full-closure revalidation, generic backend-feature metadata only if still needed, feature-owned derivation/attempt transform/telemetry, featurehost composition/key derivation, direct OpenAI forwarding, non-lossy provider-profile lifecycle, typed profile projection, initial provider rows, OpenRouter negotiation/projection, tests/TCK, docs, performance and architecture gates.
2. No cache-specific production package/file/field/callback shall be added under `internal/core`, `runtimebundle.ProcessServices`, `runtime.SecurityRuntime`, `runtime.Executor`, or `execbackend.Backend`.
3. Architecture tests shall prove provider wire literals stay outside generic core/feature policy, generated values never become session/residency authority, provider profiles do not depend upward on the feature, and lossy profile rewriting cannot return.
4. The final ownership census shall classify the cache-affinity feature outside core and preserve the full-closure zero-debt core-admission result.
5. Final review shall resolve all TODOs/placeholders required by this feature before archive.
6. After completion, a newly documented provider carrier shall be an ordinary profile/backend/connector addition against the completed contract, not generic architecture work.
