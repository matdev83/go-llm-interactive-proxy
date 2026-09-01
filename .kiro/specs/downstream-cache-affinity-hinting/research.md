# Research & Brownfield Gap Analysis

## Scope and Method

This research closes the remaining proxy-side prompt-cache locality gap after the implemented prompt-cache residency and keep-warm work. It combines:

1. Go-LIP `main` at `2420202807a704d0b2230f92bb1874970bbbfea9` (2026-09-01);
2. archived `prompt-cache-residency-contract` and `prompt-cache-keepwarm-orchestration` implementation history;
3. current coding-agent behavior from OpenAI Codex, OpenCode, Hermes Agent, Cline, Continue, Aider, and DeepSeek Harness;
4. current provider documentation for cache-aware/sticky routing;
5. current inference-server documentation showing replica-local KV/prefix-cache locality as a real systems concern;
6. a second execution-readiness audit performed specifically to remove choices that a weaker implementation model should not have to make.

The final design distinguishes four layers:

1. **Go-LIP route affinity** — chooses/sticks to a backend candidate;
2. **downstream cache-affinity hint** — advisory provider-facing locality value before inference;
3. **prompt-cache residency/control** — backend-observed effective cache target and maintenance handle after provider preparation/execution;
4. **keep-warm orchestration** — core policy over observed renewable residency.

These identities are intentionally non-interchangeable.

---

## Problem Validation: Replica Locality Is Real

The motivating RunInfra statement is directionally correct but marketing-compressed. If a provider's KV/prefix cache is local to one worker/provider endpoint, ordinary load balancing can send a repeated prompt to a cold replica. vLLM production routing therefore includes prefix/KV-cache-aware routing rather than assuming generic balancing preserves locality.

This does **not** imply that no session hint means no prompt caching. Providers can use content hashing, implicit conversation detection, cache-aware routing, distributed cache tiers, or other mechanisms. The Go-LIP feature is therefore advisory optimization only.

Primary systems reference:
- https://docs.vllm.ai/projects/production-stack/en/latest/use_cases/kv-cache-aware-routing.html

---

## Current Provider Contracts — Frozen Implementation Inputs

Implementation agents must not redo broad provider research. The following carriers are the frozen inputs for this spec unless a compile/test failure proves the repository API changed.

| Provider/path | Go-LIP projection | Generated value bound used by this spec | Notes |
|---|---|---:|---|
| OpenAI Responses | JSON `prompt_cache_key` | provider limit 64 | Direct SDK path and compatible Responses profiles |
| xAI Chat Completions | HTTP `x-grok-conv-id` | Go-LIP profile bound 256; generated value is 50 | xAI documents server-local prompt caching and conversation routing |
| xAI Responses | JSON `prompt_cache_key` | Go-LIP profile bound 64 | Current xAI docs expose Responses and recommend prompt-cache key locality |
| Mistral Chat | JSON `prompt_cache_key` | Go-LIP profile bound 256 | Documented stable session/workflow cache-affinity key |
| OpenRouter | JSON `session_id` **only** | provider guidance <256; Go-LIP bound 256 | Existing connector already has `openrouter.session_id` body support; do not add `x-session-id` |
| Fireworks Responses | JSON `prompt_cache_key` | Go-LIP profile bound 256 | Current API exposes `prompt_cache_key` and gives it precedence over `user` |
| RunInfra Chat | JSON `prompt_cache_key` | Go-LIP profile bound 64 | User-supplied RunInfra contract accepts `prompt_cache_key`; Model API base is `https://api.runinfra.ai/v1` |
| Anthropic direct | no generic synthesis | n/a | Existing `cache_control`/residency behavior remains separate |
| Gemini direct | no generic synthesis | n/a | Existing implicit/explicit cache-resource behavior remains separate |
| unknown compatible | no synthesis | n/a | Compatibility is not capability proof |

Primary references:
- OpenAI Responses: https://platform.openai.com/docs/api-reference/responses
- xAI prompt caching: https://docs.x.ai/developers/advanced-api-usage/prompt-caching
- xAI cache-hit guidance: https://docs.x.ai/developers/advanced-api-usage/prompt-caching/maximizing-cache-hits
- Mistral prompt caching: https://docs.mistral.ai/studio/conversations/advanced/prompt-caching
- OpenRouter sticky routing: https://openrouter.ai/blog/tutorials/prompt-caching-sticky-routing/
- OpenRouter routing override caveat: https://openrouter.ai/docs/guides/routing/auto-exacto
- Fireworks prompt caching: https://docs.fireworks.ai/guides/prompt-caching
- RunInfra Model API base/current product description: https://runinfra.ai/pricing

### Provider-profile rows that make this workstream self-contained

The separate `bulk-inference-provider-expansion` spec may land some of these rows before this spec is implemented. Cache-affinity implementation must be order-independent:

- if the row exists, augment it;
- if it does not exist, add it in this workstream with the frozen identity below;
- never create a duplicate semantic provider row.

| Profile ID | Family | Base URL | Auth/env | Cache-affinity projection |
|---|---|---|---|---|
| `fireworks` | `openai-responses-compatible` | `https://api.fireworks.ai/inference/v1` | `bearer_env` / `FIREWORKS_API_KEY` | Responses JSON `prompt_cache_key`, max 256, synthesis on |
| `xai` | `openai-chat-compatible` | `https://api.x.ai/v1` | `bearer_env` / `XAI_API_KEY` | Chat header `x-grok-conv-id`, max 256, synthesis on |
| `xai-responses` | `openai-responses-compatible` | `https://api.x.ai/v1` | `bearer_env` / `XAI_API_KEY` | Responses JSON `prompt_cache_key`, max 64, synthesis on |
| `mistral` | `openai-chat-compatible` | `https://api.mistral.ai/v1` | `bearer_env` / `MISTRAL_API_KEY` | Chat JSON `prompt_cache_key`, max 256, synthesis on |
| `runinfra` | `openai-chat-compatible` | `https://api.runinfra.ai/v1` | `bearer_env` / `RUNINFRA_API_KEY` | Chat JSON `prompt_cache_key`, max 64, synthesis on |

Use family-default model discovery for a missing row unless an already-landed profile from the bulk-provider work supplies a stricter frozen inventory. This cache-affinity spec must not broaden model capability claims; preserve the current/bulk-spec capability ceiling for any existing row.

### Note on xAI Responses

The earlier bulk-provider spec was frozen against evidence that selected Chat for xAI. Current xAI documentation available for this 2026-09-01 cache-affinity work exposes Responses as well. This spec therefore adds/augments a separate `xai-responses` profile for **this feature's initial matrix**. Do not rewrite unrelated historical spec text; implement the current contract above.

---

## Coding-Agent State

### OpenCode

Current OpenCode sends stable session-routing headers (`x-session-affinity`, `X-Session-Id`) from its session identity and has provider-specific `prompt_cache_key` transformation support. This validates automatic affinity as a meaningful coding-agent optimization.

Relevant source paths:
- `packages/opencode/src/session/llm/request.ts`
- `packages/opencode/src/provider/transform.ts`
- `packages/opencode/src/plugin/openai/ws-pool.ts`

### OpenAI Codex

Codex independently distinguishes session/thread identity, `prompt_cache_key`, per-turn sticky routing state, provider routing hints, and Responses/WebSocket continuation. Per-turn `x-codex-turn-state` must never become the new cross-turn generated hint.

Relevant source:
- `openai/codex/codex-rs/core/src/client.rs`

### Hermes Agent

Hermes is the strongest warning against naïvely setting `prompt_cache_key = raw session ID`. It maintains a logical/cache scope and explicitly opts providers into fields that they document, because generic OpenAI-compatible endpoints may reject unknown fields.

Relevant source paths:
- `agent/prompt_cache_scope.py`
- `agent/transports/codex.py`
- `agent/transports/chat_completions.py`
- `providers/base.py`

### Cline / Continue / Aider / DeepSeek Harness

Cline has explicit sticky-session provider metadata including OpenRouter `session_id`. Continue can pass `prompt_cache_key` on Responses. Aider is cache-aware but did not expose a comparable generic sticky-routing layer in the reviewed path. DeepSeek Harness had schema awareness but no equivalent generic synthesis path. Agent support is therefore heterogeneous enough for a proxy fallback to add value.

---

# Execution-Readiness Brownfield Audit

This section is normative implementation guidance. It records the exact code seams found on current `main` so a weaker executor does not need to rediscover architecture.

## B1 — The final runtime intentionally scrubs all session IDs before backend `Open`

Current file:
- `internal/core/runtime/executor_open_attempt.go`

Current sequence after candidate adaptation contains:

```go
wireCall := adaptedCall
wireCall.Session.ClientSessionID = ""
wireCall.Session.ContinuityKey = ""
wireCall.Session.AuthoritativeSessionID = ""
wireCall.Session.ResumeToken = ""
...
be.Open(openCtx, wireCall, routing.BackendFacingCandidate(c))
```

**Implication:** the generated opaque fallback must be computed **after candidate/backend capability is known but before `wireCall := adaptedCall` and the session scrub**. The connector must not be asked to derive the generic value from raw `proxy_owned_session_id` on the normal path.

Use the already-admitted execution/session view as the trusted input. Do not add a second secure-session read.

## B2 — Secure sessions already provide exactly the key lifecycle required

Current files:
- `internal/core/config/effective_secure_session.go`
- `internal/infra/runtimebundle/secure_session.go`
- `internal/core/securesession/app/ids.go`

Facts:

- secure session is effectively enabled by default when `enabled` is omitted;
- `secure_session.token_fingerprint_key` is already validated/loaded as the root keyed-identity material;
- memory-store mode already creates a 32-byte process-local root when the operator omits it;
- durable SQLite/PostgreSQL requires a stable key of at least 32 characters;
- existing session code already uses HMAC-SHA256 plus a versioned domain separator.

**Frozen decision:** do **not** add a new cache-affinity secret. Reuse the resolved secure-session fingerprint root only as key-derivation input, with a distinct domain.

Exact construction:

```text
subkey = HMAC-SHA256(root_key,
                     "go-lip/downstream-cache-affinity/key/v1\x00")

digest = HMAC-SHA256(subkey,
                     "go-lip/downstream-cache-affinity/value/v1\x00" ||
                     namespace || "\x00" || authoritative_session_id)

wire = "lipca1_" + base64url_no_padding(digest)
```

`digest` is the full 32 bytes. Base64url without padding is 43 characters; `lipca1_` is 7; the exact output is **50 characters**. Do not truncate it and do not use different lengths per provider.

The namespace comes from the selected backend capability so generated pseudonyms are not unnecessarily linkable across unrelated provider/profile namespaces.

## B3 — Exact new core package and runtime fields

Create:
- `internal/core/cacheaffinity/`

The package owns only:
- `Deriver` and its HMAC construction;
- `Support` (`SynthesisAllowed`, bounded `Namespace`);
- bounded source/outcome enums used by tests/metrics;
- normalization/validation helpers.

Do **not** put provider header/body names in this package.

Extend:
- `internal/core/execbackend/backend.go`

with one optional candidate-aware resolver:

```go
ResolveDownstreamCacheAffinity func(
    context.Context,
    lipapi.Call,
    routing.AttemptCandidate,
) cacheaffinity.Support
```

and add an `EffectiveDownstreamCacheAffinity(...)` helper mirroring existing `EffectivePromptCacheProfile`/capability helpers.

Extend:
- `internal/core/runtime/executor_config.go`

`SecurityRuntime` with the composed `*cacheaffinity.Deriver`. Tests/minimal executors may inject one directly.

Extend:
- `internal/infra/runtimebundle/secure_session.go`

so `assembleSecureSession`, which already receives the resolved fingerprint key (`fp`), constructs the cache-affinity deriver once and `securityRuntimeFromSecureSession` passes it to `runtime.SecurityRuntime`.

No new config secret/key is added.

## B4 — Exact runtime application point

Create:
- `internal/core/runtime/executor_cache_affinity.go`

The helper receives the current context, selected `execbackend.Backend`, selected candidate, adapted call, trusted `SessionView`, and composed deriver. It performs exactly:

1. call `adaptedCall.PromptCacheKeyValue()`; return its existing conflict error unchanged/wrapped;
2. if non-empty: leave the value unchanged and classify `explicit_prompt_cache`;
3. otherwise resolve `execbackend.EffectiveDownstreamCacheAffinity(...)`;
4. if synthesis unsupported, deriver nil, or trusted `AuthoritativeSessionID` empty: leave call unchanged;
5. otherwise derive the exact 50-char value from support namespace + authoritative session;
6. set only `adaptedCall.PromptCacheKey` to that generated value;
7. do not copy raw session identity into `Extensions`, `SafeMetadata`, or provider headers;
8. return bounded decision metadata for metrics/tests.

Call this helper in `executor_open_attempt.go` after `execbackend.AdaptCallForCandidate(...)` succeeds and before the `wireCall := adaptedCall` session-scrub block.

The trusted session input is the existing execution/request/session view, not a reread from storage.

## B5 — Existing `PromptCacheKey` semantic carrier is sufficient

Current files:
- `pkg/lipapi/semantic_extension.go`
- `pkg/lipsdk/backendplugin/invocation_meta.go`
- `pkg/lipsdk/backendplugin/items_wire.go`
- `api/backendplugin/v1/backend.proto`

`Call.PromptCacheKeyValue()` already resolves the legacy field and the canonical `lip/prompt_cache_key` semantic carrier, rejecting conflicting aliases. Backend-plugin conversion already maps a non-empty legacy value into the existing semantic-extension representation when negotiated.

**Frozen decision:** the generated fallback reuses this carrier. Do not add `Call.DownstreamAffinity`, do not add another prompt-cache semantic extension, and do not add a new protobuf invocation string for the value.

## B6 — Direct OpenAI Responses has a pre-existing forwarding gap

Current file:
- `internal/plugins/backends/openairesponses/invoke.go`

`ParamsForCall` currently builds `responses.ResponseNewParams` but does not serialize `Call.PromptCacheKeyValue()`.

**Frozen repair:** before relying on generated fallback, make this path forward a non-empty effective prompt-cache value to `ResponseNewParams.PromptCacheKey`, with the OpenAI 64-character bound. Add a regression proving an explicit client key survives even when synthesis is unavailable/disabled.

Direct OpenAI Responses backend construction must expose synthesis support with namespace `openai-responses`.

## B7 — Provider profiles already have the correct family compiler seam

Current files:
- `internal/providerprofiles/schema.go`
- `internal/providerprofiles/compiler.go`
- `internal/standardplugins/provider_profile_binding.go`
- `internal/plugins/backends/openaicompat/compatible_factory.go`
- `internal/plugins/backends/openaicompat/backend.go`

Do not create provider packages for xAI/Mistral/Fireworks/RunInfra.

Add the following bounded schema to `providerprofiles.Profile`:

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

and `Profile.CacheAffinity CacheAffinity`.

Validation rules are fixed:

- disabled zero value is valid;
- enabled projection transport is exactly `json_field` or `http_header`;
- header names use the existing safe-header-name validation;
- JSON field names use a bounded safe-name validation and cannot contain separators/control bytes;
- `MaxLength` must be `>= 50` and `<= providerprofiles.MaxStringBytes`;
- a Chat family may enable only `chat`; a Responses family may enable only `responses`; other families reject enabled cache affinity;
- unknown transport/wire name/family mismatch fails closed.

`CompiledProfile` remains immutable generation input. Because the new projection structs contain only value fields, existing clone logic does not require pointer/deep-copy work for them.

## B8 — Exact OpenAI-compatible profile projection seam

Extend `internal/plugins/backends/openaicompat/compatible_factory.go` instead of creating provider-specific adapters.

Add a profile-aware builder/helper that receives the selected `CacheAffinityProjection` in addition to bounded static headers. Keep `BuildCompatible` and `BuildCompatibleWithHeaders` source-compatible for existing custom-compatible rows.

The profile-aware builder must:

1. install `Backend.ResolveDownstreamCacheAffinity` with `SynthesisAllowed = projection.Enabled && projection.AllowProxySynthesis` and `Namespace = profile.ID`;
2. project `Call.PromptCacheKeyValue()` only when projection is enabled;
3. use `option.WithJSONSet(projection.WireName, value)` for `json_field`;
4. use `option.WithHeader(projection.WireName, value)` for `http_header`;
5. enforce `MaxLength` before provider open;
6. never project on arbitrary custom-compatible backends without a compiled profile declaration.

`internal/standardplugins/provider_profile_binding.go` selects `profile.CacheAffinity.Chat` or `.Responses` from the family and passes it into that builder. No provider-name switch is added.

## B9 — OpenRouter has an existing body carrier; use it

Current files:
- `connectors/openrouter/internal/service/service.go`
- `connectors/openrouter/internal/service/body.go`

The connector already supports an explicit `openrouter.session_id` extension and maps it to JSON `session_id`.

Frozen precedence in `applyOpenRouterBody`:

1. non-empty explicit existing `openrouter.session_id` extension;
2. otherwise `call.PromptCacheKeyValue()`;
3. otherwise omit `session_id`.

Do not emit `x-session-id` in addition to the body field.

## B10 — Executable connector negotiation needs a feature flag, not a new DTO

Current files:
- `pkg/lipsdk/backendplugin/bounds.go`
- `pkg/lipsdk/backendplugin/protocol.go`
- `pkg/lipsdk/backendplugin/host/session.go`
- `internal/infra/backendplugins/adapter/backend.go`
- `connectors/openrouter/internal/service/service.go`

Add:

```go
FeatureDownstreamCacheAffinity = "downstream_cache_affinity_v1"
```

with minimum protocol minor `ProtocolMinorSemanticExtensions` (6). This feature means:

> The connector can consume the existing negotiated prompt-cache semantic value as a provider downstream-affinity hint.

No protobuf field and no protocol minor 9 are introduced.

The host advertises the feature. OpenRouter advertises both `FeatureSemanticExtensions` and `FeatureDownstreamCacheAffinity` (plus its existing cancellation feature). The backend-plugin adapter exposes `ResolveDownstreamCacheAffinity` only when the new feature is actually negotiated. Use a bounded stable connector namespace derived from its route/backend prefix, not `InstanceID` and not raw session state.

Old peers remain synthesis-disabled.

## B11 — Observability should extend the existing executor metric seam

Current files:
- `internal/core/runtime/metrics_sink.go`
- `internal/infra/metrics/bundle.go` and its Prometheus sink implementation

Add one bounded cache-affinity observation to `runtime.MetricsSink` rather than a parallel metrics registry. Use only enums/source/outcome plus an already-bounded backend class if the existing sink pattern requires it. Never label by actual hint/session/key/target.

The runtime metric records its own host decision (`explicit_prompt_cache`, `proxy_generated`, `none`). Provider-specific explicit-extension preservation may be tested in adapter fixtures and need not force a new generic raw-header source carrier.

---

## Gap Analysis After Execution Audit

### G1 — Direct OpenAI explicit cache-key forwarding is incomplete

The feature cannot safely build synthesis before repairing explicit `PromptCacheKey` serialization in the direct Responses SDK path.

**Required repair:** direct OpenAI `ParamsForCall` forwards and bounds the existing semantic first.

### G2 — Original task plan left package/key/length decisions to implementation

**Required repair:** exact package, root key reuse, domain separators, 50-character format, and runtime insertion point are now frozen above.

### G3 — Original connector plan incorrectly allowed connector-side raw-session derivation

Current runtime strips raw session identifiers before backend `Open`.

**Required repair:** host derives before scrub; connector receives only existing opaque prompt-cache semantic. Feature negotiation advertises support; no new DTO.

### G4 — Original provider tasks assumed dedicated provider packages

Current architecture is profile/family-based and the bulk-provider expansion is not yet implemented.

**Required repair:** xAI/Mistral/Fireworks/RunInfra use `providerprofiles` + `openaicompat`; missing initial rows are added here so this cache feature is self-contained.

### G5 — OpenRouter carrier choice was unnecessarily delegated

**Required repair:** existing body `session_id` wins; no `x-session-id` addition.

### G6 — Generated fallback must not use untrusted client session hints

Internal route affinity has context-specific fallback behavior, but provider-facing pseudonymous identifiers have a stricter privacy boundary.

**Required repair:** generic synthesis uses admitted `AuthoritativeSessionID` only.

---

## Selected Design Direction

- Create `internal/core/cacheaffinity`, not a new public/canonical protocol package.
- Derive a provider-scoped 50-character opaque value from the existing secure-session root with exact HMAC domain separation.
- Apply fallback after candidate adaptation and before the final session scrub in `executor_open_attempt.go`.
- Reuse `Call.PromptCacheKey` / existing semantic-extension carriage as the effective protocol-neutral value.
- Add one provider-neutral resolver field to `execbackend.Backend`.
- Repair direct OpenAI Responses explicit prompt-cache forwarding.
- Extend `providerprofiles.Profile` with one bounded typed `cache_affinity` projection and use existing OpenAI-compatible request options for JSON/header projection.
- Make initial provider rows order-independent relative to the bulk-provider spec; add missing ones here.
- Use OpenRouter JSON `session_id` and existing extension precedence.
- Add backend-plugin feature negotiation at existing minor 6; no new proto field/minor.
- Keep residency/control and keep-warm unchanged.
- Extend existing metric/TCK/architecture seams rather than creating parallel infrastructure.

## Rejected Alternatives

- Raw `AuthoritativeSessionID` on provider wire — privacy/correlation leak.
- Client `ClientSessionID` as generated fallback — non-authoritative input.
- Principal/user identity — merges unrelated sessions/hotspots workers.
- Full-prompt hashing — expensive and changes with dynamic agent turns.
- New affinity database — deterministic derivation already solves continuity.
- Universal OpenAI-compatible injection — unknown endpoints may reject fields.
- New canonical `DownstreamAffinity` field — existing prompt-cache semantic is sufficient.
- New backend-plugin protobuf field/minor — existing semantic extension is sufficient.
- Connector-side HMAC/root secret — host already owns trusted key material and session authority.
- Dedicated xAI/Mistral/Fireworks/RunInfra Go backends — provider-profile family architecture is the intended scaling seam.

## Brownfield Design Validation Verdict

**GO after repairs encoded above.** The original spec was directionally correct but not sufficiently execution-grade for a weak implementation model because it still delegated several architectural choices. Those choices are now frozen. Implementation should require local code reading for mechanical editing/tests only, not architectural invention or provider research.
