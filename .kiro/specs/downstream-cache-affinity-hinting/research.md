# Research & Brownfield Gap Analysis

## Scope

**Current-main rebaseline (2026-10-08):** audited against `main` at `79a608632a1583b685f938103685ec34a78d5fa9`, rather than the September 1 pre-closure design snapshot. The feature itself remains unimplemented; this document distinguishes missing work from completed prerequisites.

1. `pre-oss-core-slimming` and `core-feature-ownership-full-closure` are **completed and archived**; see `.kiro/specs/archive/core-feature-ownership-full-closure/` and its final ownership census. Closure implementation #598/#599 and remediation #600 established the surviving `featurehost` and core-admission baseline.
2. `bulk-inference-provider-expansion` is completed (#619 archive, remediation through #622). Its **marker-aware provider-profile lifecycle** is now real production code, and must be reused instead of replaced.
3. The completed large-payload streaming path classifies occupied `PlaneAttemptTransforms` as `RequestBodyCanonicalRequired`. Cache-affinity enablement must not cause silent always-on wire-fast-path disqualification.
4. The repository still lacks downstream cache-affinity synthesis, its immutable generic backend-feature carrier, typed profile projection, and direct OpenAI Responses PCK forwarding. This remains one post-split feature, not an OSS Base blocker.

Provider-specific documentation and wire-carrier claims remain the proposed contract, but their official API support must be revalidated at implementation time. The findings here are about repository topology and code, not a fresh live-provider compatibility certification.

## Architecture Verdict

The prior cache-affinity design is **not acceptable against the planned final architecture** because it scheduled all of the following optional-feature growth:

```text
internal/core/cacheaffinity/
runtime.SecurityRuntime.DownstreamCacheAffinityDeriver
execbackend.Backend.ResolveDownstreamCacheAffinity
internal/core/runtime/executor_cache_affinity.go
core runtime cache-affinity metrics callback
```

The full-closure Core Admission Test says a responsibility may stay under `internal/core` only when it is required for base proxy correctness with optional features absent or is a feature-neutral universal/generic mechanism. Cache-affinity synthesis is an optional provider-locality optimization, so its HMAC derivation, policy, source/outcome semantics and telemetry do not qualify.

### Corrected ownership

```text
internal/plugins/features/downstreamcacheaffinity
  owns: derivation, fill-only decision policy, observer, bundle

internal/standardplugins/featurehost
  owns: standard composition + domain-derived subkey + metrics adapter

core
  owns: existing PlaneAttemptTransforms runner and generic AttemptMeta only

backend/profile/connector
  owns: capability declaration and provider wire projection
```

The only new core-facing concept that may be needed is a **generic immutable backend-feature ID list**, because executable backends already negotiate bounded feature IDs and in-process backends need the same provider-neutral fact available to candidate transforms. This is value metadata, not a cache-specific resolver or service framework.

---

# Brownfield Evidence

## B1 — Existing `PlaneAttemptTransforms` is the correct generic seam

Current `pkg/lipsdk/request/attempt_transform.go` already defines candidate-aware transforms with:

```text
BackendID
BackendPrefixes
Model
ReplaySupport
Scope
Session (authoritative SessionView)
Workspace
```

`internal/core/runtime/executor_open_attempt.go` and `executor_attempt_transform.go` already:

1. clones the canonical request;
2. performs candidate-specific shaping;
3. obtains the selected `execbackend.Backend`;
4. projects `request.AttemptMeta`;
5. runs the frozen `PlaneAttemptTransforms` sequence;
6. performs candidate admission/capability negotiation;
7. later runs request hooks, authority, adaptation and backend open.

Therefore cache-affinity does **not** need a new executor stage or plane. A standard feature can implement `request.AttemptTransform` and set existing `Call.PromptCacheKey`.

### Timing caveat that must be characterized

Attempt transforms currently run before request-part hooks and newer candidate-level projection/validation stages. The old cache SDD deliberately inserted synthesis immediately before final session scrub, which guaranteed that every prior explicit writer had run. Moving to the existing generic seam is leaner, but only safe if no later production stage currently creates/replaces PCK.

The implementation must therefore characterize this premise before code changes and add a ratchet for post-transform PCK writers. If the live implementation branch contains such a writer, the SDD must be revised rather than reintroducing a feature-specific executor seam.

## B2 — Existing final raw-session scrub remains correct

Current candidate-open flow ultimately performs:

```text
AdaptCallForCandidate(...)
wireCall := adaptedCall
wireCall.Session.ClientSessionID = ""
wireCall.Session.ContinuityKey = ""
wireCall.Session.AuthoritativeSessionID = ""
wireCall.Session.ResumeToken = ""
be.Open(..., wireCall, ...)
```

A PCK placed on the canonical call by an attempt transform can survive this path while raw session authority is still removed before backend open. Tests must lock this behavior against the already-completed full-closure baseline.

## B3 — Secure-session fingerprint root is still the correct key lifecycle

`internal/infra/runtimebundle/secure_session.go:buildSecureSessionRuntime` currently resolves the configured/ephemeral fingerprint root inside the secure-session construction scope. `buildPersistenceRuntime` runs before `featurehost.NewProcess` in `internal/infra/runtimebundle/process_services.go`, so a narrow generic domain-key capability can be handed forward without exposing key bytes to the feature or creating a second secret.

The corrected design does **not** give the feature this root. Generic secure-session/runtime composition exposes only a narrow domain-key derivation function that validates a **NUL-free domain label** and appends one terminal NUL internally for the HMAC input. Standard featurehost asks it once for:

```text
aiproxer/downstream-cache-affinity/key/v1
```

The exact resulting key-derivation **HMAC message** remains `aiproxer/downstream-cache-affinity/key/v1\x00`. Do not pass the NUL itself to a validator rejecting controls; this resolves a contradiction in the previous SDD. The feature receives the resulting 32-byte subkey and performs only value derivation per request:

```text
digest = HMAC-SHA256(subkey,
    "aiproxer/downstream-cache-affinity/value/v1\x00" ||
    namespace || "\x00" || authoritative_session_id)

wire = "aipca1_" + base64url_no_padding(full_digest)
```

This preserves two-stage domain separation while keeping the raw security root inside its existing owner.

### Rebranding correction

The earlier SDD froze `lipca1_` and `go-lip/...` domains even though project rebranding is already planned. Because no production/cache-wire compatibility exists yet, retaining those identifiers would create immediate migration debt. The revised contract uses `aipca1_` and `aiproxer/...` from first implementation. `aipca1_` is also seven characters, so the generated value remains exactly 50 characters and all provider bounds remain unchanged.

## B4 — Backend capability should be generic metadata, not a cache resolver

Current `execbackend.Backend` already contains several concrete capability/resolver fields, but the full-closure direction is explicitly to stop generic runtime structures from growing one optional feature at a time.

At the same time, the executable backend ABI already negotiates bounded feature IDs. That provides independent evidence for one generic representation such as:

```text
Backend.Features []backendfeature.ID
AttemptMeta.BackendFeatures []backendfeature.ID
```

with strict bounded normalization and no values/callbacks. In-process OpenAI/profile backends and executable connector adapters can set the same `downstream_cache_affinity_v1` ID. The cache feature then checks one generic fact.

On the audited October 8 tree, neither `execbackend.Backend` nor `request.AttemptMeta` has an equivalent generic feature ID list; the minimal value-only metadata seam remains planned. Re-inventory the implementation branch and reuse anything equivalent if subsequently added.

## B5 — Existing PCK semantic is sufficient

`Call.PromptCacheKeyValue()` already resolves the legacy field and canonical semantic extension and rejects conflicting aliases. Backend-plugin conversion already carries the PCK semantic.

No new canonical `DownstreamAffinity` field and no new backend-plugin value DTO are required.

## B6 — Direct OpenAI Responses still has a PCK forwarding gap

Current direct OpenAI Responses construction has a stable backend prefix and the SDK supports typed `PromptCacheKey`, but `ParamsForCall` does not currently serialize `Call.PromptCacheKeyValue()`.

Required repair remains:

```text
resolve PCK
empty -> omit
<=64 -> ResponseNewParams.PromptCacheKey
>64 -> pre-output error
alias conflict -> error
```

After this works, the direct backend advertises generic `downstream_cache_affinity_v1` support. No cache-specific backend resolver is needed.

## B7 — Provider-profile lifecycle is already marker-aware and semantics-preserving

The old finding and proposed replacement lifecycle are obsolete. `internal/standardplugins/provider_profile_binding.go` now implements the following production flow (completed by the bulk-provider workstream):

```text
configured kind: provider-profile, config.profile: <id>
  -> PrepareProviderProfiles / ExpandProviderProfileRowsWithCatalog
  -> compile catalog entry; build an isolated compatible-family config node
  -> attach reserved lip_profile_<id> YAML anchor and lip:provider-profile:<id> comment
  -> change only the PREPARED row's kind to the existing compatible-family kind
  -> standardBackendContributions' family wrapCompatibleLifecycle
  -> resolveProviderProfile(marker) -> immutable CompileProfile
  -> buildProviderProfileBackendWithNode(compiled profile, prepared node)
  -> compiled capability ceilings/headers/quirks/dialects reach constructed backend
```

Both marker forms are intentional: anchor is durable across YAML round trips, comment is backward compatible. In a test-only catalog, a reserved handle accompanies the marker. Unmarked arbitrary custom-compatible instances use the unmodified family lifecycle. The original operator config remains immutable; the prepared clone is permitted to use family kinds. Do **not** replace this with a second `LifecycleProviderProfile` registration or mandate retaining `provider-profile` as the **prepared** row kind.

Existing regression coverage includes `internal/infra/runtimebundle/provider_profile_candidate_test.go` (real candidate construction/capability ceiling), plus `internal/standardplugins/provider_profile_binding_test.go`. Cache-affinity needs additional verification that the *compiled profile* carries the new typed projection through this wrapper to the serializer; the generic YAML node is not the authority for that projection.

Required safeguards: reject forged/reserved markers on unrelated custom-compatible rows, mismatched family markers, missing/invalid catalog handles, and unknown profiles; preserve source-config immutability; prove disabling of compiled capability flags persists. Extend the established lifecycle rather than introducing an additional provider-profile router.

## B8 — Profile schema can remain declarative

The frozen typed projection remains appropriate:

```go
type CacheAffinityProjection struct {
    Enabled             bool
    Transport           CacheAffinityTransport
    WireName            string
    MaxLength           int
    AllowProxySynthesis bool
}

type CacheAffinity struct {
    Chat      CacheAffinityProjection
    Responses CacheAffinityProjection
}
```

`internal/providerprofiles` must not import the feature package. It may own local `MinCacheAffinityValueLength = 50`; architecture tests pin equality with the feature's generated length.

The profile-aware compatible builder owns wire projection and adds the generic backend feature when synthesis is allowed.

## B9 — Executable connectors need only capability negotiation

Backend-plugin protocol already negotiates optional feature names and carries PCK through existing invocation semantics. Host currently offers protocol minor 9 (`ProtocolMinorAccountingEvidenceV2`), while `ProtocolMinorSemanticExtensions` remains 6. The revised design keeps:

```text
downstream_cache_affinity_v1
minimum minor: existing semantic-extension minor 6
```

but maps successful negotiation to generic in-process `Backend.Features`. Host negotiation is duplicated in `pkg/lipsdk/backendplugin/host/session.go` (`ProtocolOffer.Features` and the wire `NegotiateRequest.HostFeatures`); both lists, the feature-minor registry in `protocol.go`, and the negotiated adapter must be kept in sync. A connector advertised feature is not sufficient without a negotiated, valid existing PCK carrier. There is no cache-specific `execbackend.Backend` callback and no raw session sent to the connector.

## B10 — Feature-owned telemetry avoids core growth

The prior SDD added `OnDownstreamCacheAffinity` to a core runtime metrics interface. That is unnecessary. The feature can emit a tiny bounded event to an injected observer. `standardplugins/featurehost` adapts it to the infrastructure metrics sink. Core's generic extension-runner telemetry remains untouched.

## B11 — Enabled attempt transform conflicts with large-payload wire eligibility

`pkg/lipsdk/feature/plane_manifest.go` declares `PlaneAttemptTransforms` with `RequestBodyCanonicalRequired`. `internal/core/largebody/authority_gate.go` and `eligibility.go` reject direct wire execution when such a plane is occupied. This prevents the wire path from silently skipping canonical transforms, and is a correctness property.

**Consequences:** an unconditional always-present cache-affinity transform—even a no-op for unsupported backend candidates—would disable wire acceleration for unrelated traffic. The standard distribution must provide explicit generation-scoped opt-in (an enabled feature registration or equivalent typed standard enablement). Disabled/absent configuration contributes **no** `PlaneAttemptTransforms` occupant. When enabled, the ordinary canonical path must run synthesis and the wire assessor must legitimately decline; do not add a cache-specific bypass or pretend the transform ran on the wire path. Test both modes with `server.large_payload_fast_path.enabled` and bounded allocation/performance evidence. This is a conscious opt-in performance tradeoff, not an invitation to weaken wire safety guards.

## B12 — Current OpenRouter payload has an independent `session_id` override surface

`connectors/openrouter/internal/service/body.go` first projects explicit `openrouter.session_id`, then iterates generic `extraBodyPrefix` extensions. This late generic body step can also write JSON `session_id`. When adding PCK fallback, explicitly preserve priority for the supported `openrouter.session_id` carrier, and reject or deterministically resolve any collision from the generic extra-body channel. Do not allow a later map write to silently override validated cache-affinity scope; preserve other existing passthrough behavior.

---

# Frozen Provider Inputs

The provider research remains the proposed implementation input; implementation agents must re-check affected current official provider contracts before claiming production support, without repeating unrelated broad provider discovery.

| Provider/path | Projection | Bound | Synthesis |
|---|---|---:|---:|
| OpenAI Responses | JSON `prompt_cache_key` | 64 | yes |
| xAI Chat | HTTP `x-grok-conv-id` | 256 | yes |
| xAI Responses | JSON `prompt_cache_key` | 64 | yes |
| Mistral Chat | JSON `prompt_cache_key` | 256 | yes |
| OpenRouter | JSON `session_id` only | 256 | negotiated |
| Fireworks Responses | JSON `prompt_cache_key` | 256 | yes |
| RunInfra Chat | JSON `prompt_cache_key` | 64 | yes |
| Anthropic direct | none | n/a | no |
| Gemini direct | none | n/a | no |
| unknown custom compatible | none | n/a | no |

Primary references retained from the original research:

- OpenAI Responses API: https://platform.openai.com/docs/api-reference/responses
- xAI prompt caching: https://docs.x.ai/developers/advanced-api-usage/prompt-caching
- Mistral prompt caching: https://docs.mistral.ai/studio/conversations/advanced/prompt-caching
- OpenRouter sticky routing: https://openrouter.ai/blog/tutorials/prompt-caching-sticky-routing/
- Fireworks prompt caching: https://docs.fireworks.ai/guides/prompt-caching
- vLLM KV-cache-aware routing: https://docs.vllm.ai/projects/production-stack/en/latest/use_cases/kv-cache-aware-routing.html

## Initial profile rows

| ID | Family | Base URL | Env | Projection |
|---|---|---|---|---|
| `fireworks` | `openai-responses-compatible` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | Responses JSON `prompt_cache_key`, max256 |
| `xai` | `openai-chat-compatible` | `https://api.x.ai/v1` | `XAI_API_KEY` | Chat header `x-grok-conv-id`, max256 |
| `xai-responses` | `openai-responses-compatible` | `https://api.x.ai/v1` | `XAI_API_KEY` | Responses JSON `prompt_cache_key`, max64 |
| `mistral` | `openai-chat-compatible` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | Chat JSON `prompt_cache_key`, max256 |
| `runinfra` | `openai-chat-compatible` | `https://api.runinfra.ai/v1` | `RUNINFRA_API_KEY` | Chat JSON `prompt_cache_key`, max64 |

Current embedded catalog has 141 entries: `fireworks`, `mistral`, and `xai` already exist with capability ceilings. Extend those in place; `xai-responses` and `runinfra` are absent and require additions. Preserve stricter inventory, capability, and credential data; cache-affinity must not broaden unrelated capabilities.

---

# Execution Revalidation Triggers

STOP the affected wave and update this SDD if any of these conditions arise against the implementation-branch baseline:

1. `PlaneAttemptTransforms` no longer provides authoritative SessionView plus candidate/backend identity.
2. A production stage after attempt transforms writes/replaces `PromptCacheKey`.
3. Candidate adaptation drops PCK for a backend that is otherwise declared capable.
4. The full-closure featurehost exposes a better existing generic domain-key derivation contract than the one described here; reuse it instead of duplicating.
5. A generic backend-feature metadata carrier already exists; reuse it.
6. The existing marker-aware compatible-family profile lifecycle no longer preserves compiled semantics; repair its authoritative owner rather than layering another bridge.
7. An official provider contract contradicts a frozen projection/bound.
8. Implementing the feature would require cache-specific core state/callbacks or a new cache-specific plane.
9. Feature enablement would occupy `PlaneAttemptTransforms` even when disabled or would bypass an occupied canonical-required plane on the wire fast path.
10. OpenRouter's generic extra-body extension can override an explicit `session_id` without deterministic validation.

A package/path rename caused by the pending #641 AIProxer rebranding is not itself a redesign trigger: use the actual post-rebrand path and preserve the frozen unimplemented `aipca1_` contract.

---

# Final Research Verdict


**GO for implementation on the completed, archived lean-core baseline when the post-split milestone is scheduled.** Reuse the existing candidate-aware attempt-transform plane and the completed marker-aware provider-profile lifecycle; add only generic immutable feature ID metadata if still missing. Preserve explicit opt-in to avoid a default wire-fast-path performance regression, and validate both HMAC domain-label termination and OpenRouter `session_id` collisions before production merge.
