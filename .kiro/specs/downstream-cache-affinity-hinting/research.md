# Research & Brownfield Gap Analysis

## Scope and Method

This research closes the remaining proxy-side prompt-cache locality gap after the implemented prompt-cache residency and keep-warm work. It combines:

1. Go-LIP `main` at `2420202807a704d0b2230f92bb1874970bbbfea9` (2026-09-01);
2. archived prompt-cache residency/keep-warm specs and implementations;
3. current coding-agent behavior from Codex, OpenCode, Hermes, Cline, Continue, Aider, and DeepSeek Harness;
4. current provider documentation for cache-aware/sticky routing;
5. current inference-server evidence that replica-local KV/prefix cache locality is a real systems concern;
6. an execution-readiness audit against the exact current Go-LIP code paths, including the active bulk-provider spec's known profile-binding gap.

The final architecture keeps four concepts separate:

1. **route affinity** — Go-LIP chooses/sticks to a backend candidate;
2. **downstream cache-affinity hint** — advisory provider-facing locality metadata before inference;
3. **prompt-cache residency/control** — backend-observed cache target/generation/handle after provider preparation/execution;
4. **keep-warm orchestration** — core policy over observed renewable residency.

No implementation task may merge those identities.

---

## Problem Validation

The motivating RunInfra statement is directionally correct: if KV/prefix cache state is local to one worker/provider endpoint, ordinary load balancing can send a repeated agent prompt to a cold replica. vLLM production routing includes prefix/KV-cache-aware routing for exactly this reason.

This does **not** mean no session hint means no caching. Providers can internally use hashing, cache-aware routing, distributed cache tiers, implicit conversation detection, or other techniques. Go-LIP's generated value is therefore an optimization signal, never a correctness/cache-hit guarantee.

Primary systems reference:
- https://docs.vllm.ai/projects/production-stack/en/latest/use_cases/kv-cache-aware-routing.html

---

# Frozen Provider Inputs

Implementation agents must not redo broad provider research. Use this matrix.

| Provider/path | Go-LIP projection | Bound | Notes |
|---|---|---:|---|
| OpenAI Responses | JSON `prompt_cache_key` | 64 | direct SDK + compatible Responses profiles |
| xAI Chat | HTTP `x-grok-conv-id` | 256 | generated value is only 50 chars |
| xAI Responses | JSON `prompt_cache_key` | 64 | current xAI docs expose Responses |
| Mistral Chat | JSON `prompt_cache_key` | 256 | documented conversation/workflow cache-affinity key |
| OpenRouter | JSON `session_id` **only** | 256 | existing connector already owns body carrier; do not add `x-session-id` |
| Fireworks Responses | JSON `prompt_cache_key` | 256 | preferred cache-affinity body key |
| RunInfra Chat | JSON `prompt_cache_key` | 64 | Model API base `https://api.runinfra.ai/v1` |
| Anthropic direct | no generic synthesis | n/a | existing cache controls/residency remain separate |
| Gemini direct | no generic synthesis | n/a | existing implicit/explicit cache resources remain separate |
| unknown compatible | no synthesis | n/a | compatibility is not capability proof |

Primary references:
- OpenAI: https://platform.openai.com/docs/api-reference/responses
- xAI: https://docs.x.ai/developers/advanced-api-usage/prompt-caching
- xAI cache-hit guidance: https://docs.x.ai/developers/advanced-api-usage/prompt-caching/maximizing-cache-hits
- Mistral: https://docs.mistral.ai/studio/conversations/advanced/prompt-caching
- OpenRouter: https://openrouter.ai/blog/tutorials/prompt-caching-sticky-routing/
- OpenRouter override caveat: https://openrouter.ai/docs/guides/routing/auto-exacto
- Fireworks: https://docs.fireworks.ai/guides/prompt-caching
- RunInfra: https://runinfra.ai/pricing

## Self-contained initial profile rows

This cache work must not wait for the separate bulk-provider implementation. If a row is absent, add it here; if it already landed, augment it and preserve stricter inventory/capability settings.

| Profile ID | Family | Base URL | Auth/env | Projection |
|---|---|---|---|---|
| `fireworks` | `openai-responses-compatible` | `https://api.fireworks.ai/inference/v1` | bearer / `FIREWORKS_API_KEY` | Responses JSON `prompt_cache_key`, max256, synth on |
| `xai` | `openai-chat-compatible` | `https://api.x.ai/v1` | bearer / `XAI_API_KEY` | Chat header `x-grok-conv-id`, max256, synth on |
| `xai-responses` | `openai-responses-compatible` | `https://api.x.ai/v1` | bearer / `XAI_API_KEY` | Responses JSON `prompt_cache_key`, max64, synth on |
| `mistral` | `openai-chat-compatible` | `https://api.mistral.ai/v1` | bearer / `MISTRAL_API_KEY` | Chat JSON `prompt_cache_key`, max256, synth on |
| `runinfra` | `openai-chat-compatible` | `https://api.runinfra.ai/v1` | bearer / `RUNINFRA_API_KEY` | Chat JSON `prompt_cache_key`, max64, synth on |

For a newly added row use family-default model discovery unless an already-landed bulk-provider row defines a stricter frozen inventory. Cache-affinity support must not broaden model capabilities.

### xAI Responses vs earlier bulk-provider snapshot

The earlier bulk-provider spec was frozen against evidence that selected Chat for xAI. Current 2026-09-01 xAI documentation exposes Responses as well. This spec adds/augments a distinct `xai-responses` profile for this current cache-affinity matrix. Do not rewrite historical spec artifacts.

---

# Coding-Agent Evidence

- **OpenCode** emits stable session-affinity headers and provider-specific prompt-cache keys.
- **Codex** independently separates session/thread, `prompt_cache_key`, per-turn sticky state, routing hints and continuation.
- **Hermes** explicitly avoids naïvely equating raw physical session IDs with cache keys and opts providers into supported fields only.
- **Cline** has provider metadata for sticky sessions including OpenRouter `session_id`.
- **Continue** passes `prompt_cache_key` on Responses.
- **Aider/DeepSeek Harness/other reviewed agents** do not provide equivalent universal automatic locality behavior.

Conclusion: client support is heterogeneous enough for a proxy fallback to provide material UX value, but explicit client keys can be richer than proxy session scope and therefore win precedence.

---

# Exact Current Go-LIP Brownfield Map

## B1 — Final backend wire call scrubs raw session identifiers

File:
- `internal/core/runtime/executor_open_attempt.go`

Current sequence includes:

```go
wireCall := adaptedCall
wireCall.Session.ClientSessionID = ""
wireCall.Session.ContinuityKey = ""
wireCall.Session.AuthoritativeSessionID = ""
wireCall.Session.ResumeToken = ""
...
be.Open(openCtx, wireCall, routing.BackendFacingCandidate(c))
```

**Decision:** host/core derives the opaque fallback after candidate adaptation/capability resolution but before `wireCall := adaptedCall`. Connector-side generic derivation from raw session IDs is forbidden.

Use the already-admitted session/execution view; do not add a second secure-session lookup.

## B2 — Existing secure-session root is the key lifecycle

Files:
- `internal/core/config/effective_secure_session.go`
- `internal/infra/runtimebundle/secure_session.go`
- `internal/core/securesession/app/ids.go`

Facts:

- secure session is effectively enabled by default;
- `secure_session.token_fingerprint_key` is already resolved as keyed identity material;
- memory mode generates a 32-byte process-local root if omitted;
- durable modes require a stable sufficiently long key;
- existing session code already uses HMAC-SHA256 with domain separation.

**Decision:** no second cache-affinity secret.

Exact formula:

```text
subkey = HMAC-SHA256(root_key,
                     "go-lip/downstream-cache-affinity/key/v1\x00")

digest = HMAC-SHA256(subkey,
                     "go-lip/downstream-cache-affinity/value/v1\x00" ||
                     namespace || "\x00" || authoritative_session_id)

wire = "lipca1_" + base64url_no_padding(full_32_byte_digest)
```

The result is exactly 50 chars (7-char prefix + 43-char rawurl SHA-256). No truncation/per-provider length variants.

## B3 — Exact new core package and backend resolver

Create:
- `internal/core/cacheaffinity/`

It owns only HMAC derivation, bounded `Support`, and bounded source/outcome enums.

Extend:
- `internal/core/execbackend/backend.go`

with:

```go
ResolveDownstreamCacheAffinity func(
    context.Context,
    lipapi.Call,
    routing.AttemptCandidate,
) cacheaffinity.Support
```

`Support` contains only `SynthesisAllowed` and bounded `Namespace`; no provider wire names.

Extend `runtime.SecurityRuntime` with `*cacheaffinity.Deriver` and compose it once from the already-resolved secure-session root in `secure_session.go`.

## B4 — Exact runtime algorithm/insertion

Create:
- `internal/core/runtime/executor_cache_affinity.go`

Algorithm:

1. `call.PromptCacheKeyValue()`; existing conflict is error;
2. existing non-empty PCK => preserve (`explicit_prompt_cache`);
3. resolve selected backend `Support`;
4. unsupported / nil deriver / empty admitted `AuthoritativeSessionID` => no generated value;
5. derive exact provider-scoped 50-char value;
6. set only `call.PromptCacheKey` on the cloned/adapted call;
7. do not copy raw session to `Extensions`, `SafeMetadata` or provider headers;
8. return bounded decision metadata.

Call immediately before the existing final session scrub in `executor_open_attempt.go`.

## B5 — Existing `PromptCacheKey` carrier is sufficient

Files:
- `pkg/lipapi/semantic_extension.go`
- `pkg/lipsdk/backendplugin/invocation_meta.go`
- `pkg/lipsdk/backendplugin/items_wire.go`
- `api/backendplugin/v1/backend.proto`

`Call.PromptCacheKeyValue()` already resolves legacy field + `lip/prompt_cache_key` semantic and rejects conflicts. Backend-plugin conversion already has an existing semantic-extension/legacy carrier.

**Decision:** no new canonical `DownstreamAffinity` field and no new backend-plugin value DTO.

## B6 — Direct OpenAI Responses has an explicit-key forwarding gap

File:
- `internal/plugins/backends/openairesponses/invoke.go`

`ParamsForCall` currently does not serialize `Call.PromptCacheKeyValue()`.

**Decision:** repair this first. Forward non-empty PCK to typed `ResponseNewParams.PromptCacheKey`, reject >64, preserve alias conflict semantics. Direct OpenAI backend advertises synthesis namespace `openai-responses`.

## B7 — Provider-profile schema/family builder is correct, but production profile binding is currently lossy

Files:
- `internal/providerprofiles/schema.go`
- `internal/providerprofiles/compiler.go`
- `internal/standardplugins/provider_profile_binding.go`
- `internal/standardplugins/provider_profiles.go`
- `internal/standardplugins/standard_contributions.go`
- `internal/plugins/backends/openaicompat/compatible_factory.go`

Important current fact also documented by the merged `bulk-inference-provider-expansion` spec:

- `BuildProviderProfileBackend(compiled, ...)` correctly receives complete compiled profile semantics;
- **production** `PrepareProviderProfiles -> ExpandProviderProfileRows` currently compiles a profile and then rewrites the row to generic compatible YAML through `ProfileConfigNode`;
- that rewrite loses compiled-only semantics before the normal registry lifecycle, so simply adding `Profile.CacheAffinity` would not make it to production backend construction.

This is a material prerequisite for cache-affinity profiles.

### Frozen profile-binding repair

Do not serialize `cache_affinity` into generic compatible YAML and do not create a second semantic authority.

Implement one real lifecycle registration for `ProviderProfileKind`:

```text
kind: provider-profile
config:
  profile: <profile-id>
        |
        v
LifecycleProviderProfile(instanceID, node, upstream, deps)
        |
        +-> profileReference(node)
        +-> ProviderProfileCatalog()
        +-> CompileProviderProfile(profile)
        +-> BuildProviderProfileBackend(compiled, instanceID, upstream, deps)
```

Concrete steps:

1. Add `LifecycleProviderProfile(...) pluginreg.LifecycleBackendFactory` in `provider_profile_binding.go`.
2. It resolves the profile ref from the existing config node, loads from `ProviderProfileCatalog`, compiles exactly once for that build, and calls existing `BuildProviderProfileBackend`.
3. Register `ProviderProfileKind` as a lifecycle backend in `standard_contributions.go` using normal inference/static-credential composition metadata.
4. Change `PrepareProviderProfiles`/`ExpandProviderProfileRows` so configured `provider-profile` rows are validated/clone-preserved but **not rewritten to generic factory kinds/config**. Keep source config immutable.
5. Keep arbitrary `custom-*-compatible` behavior unchanged.
6. Keep `ProfileConfigNode` for family-builder/internal tests where useful; it is no longer the production semantic bridge for a `provider-profile` row.
7. Add a production registry/candidate-build test starting from `kind: provider-profile` proving a compiled cache-affinity projection reaches the constructed backend resolver/request projection.

This repair also satisfies the already-known bulk-provider architecture direction; do not invent another approach.

## B8 — Exact typed `cache_affinity` profile field

Add to `providerprofiles.Profile`:

```go
type CacheAffinityTransport string
const (
    CacheAffinityTransportJSONField  CacheAffinityTransport = "json_field"
    CacheAffinityTransportHTTPHeader CacheAffinityTransport = "http_header"
)

type CacheAffinityProjection struct {
    Enabled             bool                   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
    Transport           CacheAffinityTransport `json:"transport,omitempty" yaml:"transport,omitempty"`
    WireName            string                 `json:"wire_name,omitempty" yaml:"wire_name,omitempty"`
    MaxLength           int                    `json:"max_length,omitempty" yaml:"max_length,omitempty"`
    AllowProxySynthesis bool                   `json:"allow_proxy_synthesis,omitempty" yaml:"allow_proxy_synthesis,omitempty"`
}

type CacheAffinity struct {
    Chat      CacheAffinityProjection `json:"chat,omitempty" yaml:"chat,omitempty"`
    Responses CacheAffinityProjection `json:"responses,omitempty" yaml:"responses,omitempty"`
}
```

Validation is strict:

- disabled projection must be all-zero;
- enabled transport exactly JSON field or HTTP header;
- wire name required/safe;
- `MaxLength >= 50 && <= providerprofiles.MaxStringBytes`;
- Chat family can enable only Chat; Responses family only Responses;
- other v1 families reject enabled projection;
- synthesis true requires enabled true.

No schema version bump and no arbitrary transform DSL.

## B9 — Exact OpenAI-compatible projection seam

Use `internal/plugins/backends/openaicompat` family construction; do not create provider packages.

Profile-aware builder receives profile ID + selected validated projection and:

- exposes synthesis support namespace = profile ID when enabled+synthesis allowed;
- resolves `call.PromptCacheKeyValue()`;
- validates max length;
- `json_field` => `option.WithJSONSet`;
- `http_header` => `option.WithHeader`;
- arbitrary custom-compatible rows do not get this behavior.

## B10 — OpenRouter existing body carrier wins

Files:
- `connectors/openrouter/internal/service/body.go`
- `connectors/openrouter/internal/service/service.go`

Precedence:

1. explicit existing `openrouter.session_id` extension;
2. otherwise `call.PromptCacheKeyValue()`;
3. otherwise omit.

Body JSON `session_id` only; max256; no `x-session-id` duplicate.

## B11 — Executable connector feature flag, no DTO

Files:
- `pkg/lipsdk/backendplugin/bounds.go`
- `pkg/lipsdk/backendplugin/protocol.go`
- `pkg/lipsdk/backendplugin/host/session.go`
- `internal/infra/backendplugins/adapter/backend.go`
- OpenRouter `Describe`.

Add:

```go
FeatureDownstreamCacheAffinity = "downstream_cache_affinity_v1"
```

Minimum minor = `ProtocolMinorSemanticExtensions` (6). Meaning: connector consumes existing prompt-cache semantic as downstream-affinity input.

No new proto field and no protocol minor 9. Host synthesizes only when feature is negotiated. Namespace is a stable route/backend prefix, not instance/session ID.

## B12 — Existing metrics seam is sufficient

Files:
- `internal/core/runtime/metrics_sink.go`
- `internal/infra/metrics/...`

Extend existing `MetricsSink`; do not build a parallel metrics subsystem. Bounded source/outcome enums only. Never log/label raw hints/session/PCK/residency identifiers. Hint emission is not cache-hit evidence.

---

# Gap Analysis / Repairs

### G1 — No generic effective downstream resolver
**Repair:** new `cacheaffinity` package + one candidate-aware `execbackend` resolver + runtime insertion before session scrub.

### G2 — No privacy-safe generated fallback
**Repair:** exact domain-separated HMAC using existing secure-session root; authoritative session only; provider/profile namespace; exact 50-char output.

### G3 — Direct OpenAI explicit PCK is not forwarded
**Repair:** typed Responses serialization + 64-char validation before synthesis support.

### G4 — Generic provider compatibility is not enough
**Repair:** typed `cache_affinity` provider-profile opt-in and explicit executable-connector feature.

### G5 — Production provider-profile binding loses compiled semantics
**Repair:** register `ProviderProfileKind` as a real lifecycle backend and stop rewriting configured profile rows into generic YAML. This spec owns that repair as a prerequisite; it does not wait for bulk-provider implementation.

### G6 — Original provider tasks assumed dedicated packages
**Repair:** xAI/Mistral/Fireworks/RunInfra use provider profiles + `openaicompat`; missing initial rows are created here.

### G7 — OpenRouter carrier/ABI choice was delegated
**Repair:** body `session_id` only; existing semantic carrier; minor6 feature flag; no new DTO/minor.

### G8 — Observability could be mistaken for cache effect
**Repair:** decision metrics only; existing provider cache evidence remains truth.

---

# Selected Architecture

- `internal/core/cacheaffinity` for derivation/support only.
- Existing secure-session fingerprint root, domain-separated subkey; no new secret.
- Exact 50-char provider-scoped generated value.
- Existing `Call.PromptCacheKey` semantic as effective protocol-neutral carrier.
- Runtime application after candidate adaptation and before final session scrub.
- One optional candidate-aware resolver on `execbackend.Backend`.
- Direct OpenAI explicit forwarding repair + synthesis support.
- Real `ProviderProfileKind` lifecycle registration so compiled profile semantics reach production family builders.
- One bounded typed `Profile.CacheAffinity` field.
- Existing `openaicompat` request-option seam for profile JSON/header projection.
- Self-contained initial provider rows.
- OpenRouter body `session_id` + minor6 negotiated support feature.
- Existing metrics/TCK/archtest seams.
- Residency/control/keep-warm unchanged.

## Rejected Alternatives

- raw session ID on wire;
- client session hint/principal/IP fallback;
- full prompt hashing;
- new affinity DB/store;
- universal compatible injection;
- new canonical downstream-affinity field;
- serialize new profile semantics into generic compatible YAML;
- second provider-profile catalog/policy map;
- new backend-plugin value field/minor;
- connector-side root secret/HMAC;
- dedicated xAI/Mistral/Fireworks/RunInfra Go backends.

## Brownfield Design Validation Verdict

**GO after the execution-audit repairs above.** The initial spec was directionally correct but still delegated package/key/length/carrier/ABI decisions and missed the lossy production provider-profile binding. Those issues are now frozen and scheduled. A weaker executor should need only mechanical current-file navigation, not architecture invention or provider research.
