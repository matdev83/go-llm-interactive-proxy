# Research & Brownfield Gap Analysis

## Scope

This revision revalidates downstream cache-affinity hinting against two newer architecture decisions that are now normative for implementation:

1. `.kiro/specs/pre-oss-core-slimming/` removes release-critical optional feature implementation/policy from core; and
2. `.kiro/specs/core-feature-ownership-full-closure/` finishes the migration with an explicit Core Admission Test and one standard-distribution `featurehost` composition boundary.

The provider research from the prior cache-affinity SDD remains valid input. The material change is ownership and execution topology, not the provider matrix.

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

`internal/core/runtime/evaluateCandidate` already:

1. clones the canonical request;
2. performs candidate-specific shaping;
3. obtains the selected `execbackend.Backend`;
4. projects `request.AttemptMeta`;
5. runs the frozen `PlaneAttemptTransforms` sequence;
6. performs candidate admission/capability negotiation;
7. later runs request hooks, authority, adaptation and backend open.

Therefore cache-affinity does **not** need a new executor stage or plane. A standard feature can implement `request.AttemptTransform` and set existing `Call.PromptCacheKey`.

### Timing caveat that must be characterized

Attempt transforms currently run before request-part hooks. The old cache SDD deliberately inserted synthesis immediately before final session scrub, which guaranteed that every prior explicit writer had run. Moving to the existing generic seam is leaner, but only safe if no later production stage currently creates/replaces PCK.

The implementation must therefore characterize this premise before code changes and add a ratchet for post-transform PCK writers. If the post-full-closure tree contains such a writer, the SDD must be revised rather than reintroducing a feature-specific executor seam.

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

A PCK placed on the canonical call by an attempt transform can survive this path while raw session authority is still removed before backend open. Tests must lock this behavior after the full-closure migration.

## B3 — Secure-session fingerprint root is still the correct key lifecycle

Existing secure-session composition already owns the fingerprint root and its memory-vs-durable lifetime rules. Adding a second cache-affinity secret is unnecessary.

The corrected design does **not** give the feature this root. Generic secure-session/runtime composition exposes only a narrow domain-key derivation function. Standard featurehost asks it once for:

```text
aiproxer/downstream-cache-affinity/key/v1\x00
```

The feature receives the resulting 32-byte subkey and performs only value derivation per request:

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

If the post-full-closure tree already has an equivalent generic fact carrier, reuse it. Do not add a second one.

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

## B7 — Production provider-profile binding is currently lossy

Current `internal/standardplugins/provider_profile_binding.go` performs:

```text
kind: provider-profile
 -> compile profile
 -> ProfileConfigNode
 -> rewrite row kind/config to generic compatible factory
 -> generic lifecycle build
```

This loses compiled-only semantics. `BuildProviderProfileBackend(compiled, ...)` already exists and is the correct semantic-aware builder, but the production configured-row path bypasses it.

Both this SDD and `bulk-inference-provider-expansion` independently identified the same issue. There must be only one repair:

```text
kind: provider-profile
 -> real ProviderProfileKind lifecycle factory
 -> profileReference
 -> immutable catalog lookup
 -> CompileProviderProfile
 -> BuildProviderProfileBackend
```

`PrepareProviderProfiles` keeps the row as `provider-profile`; it validates/clones but does not lower compiled profile semantics into generic YAML.

Whichever workstream lands the repair first owns implementation; the other only verifies it.

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

Backend-plugin protocol already negotiates optional feature names and carries PCK through existing invocation semantics. The revised design keeps:

```text
downstream_cache_affinity_v1
minimum minor: existing semantic-extension minor 6
```

but maps successful negotiation to generic in-process `Backend.Features`. There is no cache-specific `execbackend.Backend` callback and no raw session sent to the connector.

## B10 — Feature-owned telemetry avoids core growth

The prior SDD added `OnDownstreamCacheAffinity` to a core runtime metrics interface. That is unnecessary. The feature can emit a tiny bounded event to an injected observer. `standardplugins/featurehost` adapts it to the infrastructure metrics sink. Core's generic extension-runner telemetry remains untouched.

---

# Frozen Provider Inputs

The provider research remains the implementation input; implementation agents must not redo broad provider research unless a current official contract directly contradicts a frozen row.

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

If another implementation has already added a row, augment it and retain stricter inventory/capability data. Cache-affinity may not broaden unrelated capabilities.

---

# Execution Revalidation Triggers

STOP the affected wave and update this SDD if any of these are true after full-core closure:

1. `PlaneAttemptTransforms` no longer provides authoritative SessionView plus candidate/backend identity.
2. A production stage after attempt transforms writes/replaces `PromptCacheKey`.
3. Candidate adaptation drops PCK for a backend that is otherwise declared capable.
4. The full-closure featurehost exposes a better existing generic domain-key derivation contract than the one described here; reuse it instead of duplicating.
5. A generic backend-feature metadata carrier already exists; reuse it.
6. Provider-profile production binding has changed materially from the real lifecycle plan; verify semantics rather than layering another bridge.
7. An official provider contract contradicts a frozen projection/bound.
8. Implementing the feature would require cache-specific core state/callbacks or a new cache-specific plane.

A package/path rename caused only by #429 is not a redesign trigger: use the mechanically renamed post-rebrand path and preserve the architecture.

---

# Final Research Verdict

**GO, but only on the post-full-closure architecture.** The existing candidate attempt-transform plane is sufficient to eliminate the previously planned cache-specific core runtime path. The provider/profile/connector work remains valid. The remaining generic addition—bounded immutable backend feature IDs—is justified by existing executable-backend feature negotiation and prevents another optional-feature resolver from entering core.
