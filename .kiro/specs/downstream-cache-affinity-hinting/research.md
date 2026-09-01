# Research & Brownfield Gap Analysis

## Scope and Method

This research closes the remaining proxy-side prompt-cache locality gap after the already implemented prompt-cache residency and keep-warm work. It combines:

1. current Go-LIP architecture on `main` at `2420202807a704d0b2230f92bb1874970bbbfea9`;
2. the archived `prompt-cache-residency-contract` and `prompt-cache-keepwarm-orchestration` specs/implementations;
3. current coding-agent behavior from OpenAI Codex, OpenCode, Hermes Agent, Cline, Continue, Aider, DeepSeek Harness, and exact-source searches for other OSS agents;
4. current provider documentation for cache-aware/sticky routing;
5. current inference-server documentation demonstrating replica-local KV/prefix cache locality as a real systems concern.

The design intentionally distinguishes **cache routing affinity** from **cache residency authority**. A provider-facing routing hint can improve probability of cache reuse without proving that any specific cache is resident, renewable, or still valid.

## Problem Validation: Replica Locality Is Real

The motivating RunInfra statement is directionally correct but marketing-compressed. The underlying failure mode is established in production inference systems: when prefix/KV cache state is local to a worker or provider endpoint, generic load balancing can send a repeated prompt to a cold replica. vLLM's production stack therefore provides prefix-aware/KV-cache-aware routing rather than assuming ordinary load balancing preserves cache locality.

This does **not** imply that every provider without a session hint has no prompt caching. Providers may internally use content hashing, cache-aware routing, distributed cache tiers, implicit conversation detection, or other mechanisms. Therefore Go-LIP must treat generated affinity as an advisory optimization and never as a cache-hit guarantee.

## Current Provider Contracts

### OpenAI Responses

Current OpenAI Responses exposes `prompt_cache_key` as a request-level cache/routing hint. This is a request semantic, not a provider cache-resource identity or continuation token. Go-LIP already carries `PromptCacheKey` protocol-neutrally, so the missing behavior is fallback generation when a client omits it and the selected provider/model explicitly supports synthesis.

Primary reference:
- https://platform.openai.com/docs/api-reference/responses

### xAI

xAI documents automatic prompt caching but explicitly recommends `x-grok-conv-id` for Chat Completions because cache entries are per-server and requests may otherwise reach different servers. For Responses-style usage, xAI also supports `prompt_cache_key`-style affinity. This is strong direct evidence that a stable conversation routing key can materially improve cache hit probability.

Primary references:
- https://docs.x.ai/developers/advanced-api-usage/prompt-caching
- https://docs.x.ai/developers/advanced-api-usage/prompt-caching/how-it-works
- https://docs.x.ai/developers/advanced-api-usage/prompt-caching/maximizing-cache-hits

### Mistral

Mistral documents prompt-cache affinity using `prompt_cache_key` and recommends stable conversation/session/workflow identifiers. The key is a routing/cache optimization, not a deterministic cache-residency resource.

Primary reference:
- https://docs.mistral.ai/studio/conversations/advanced/prompt-caching

### OpenRouter

OpenRouter's July 2026 sticky-routing guidance directly describes the same agent-loop problem: a warm cache only helps if follow-up turns return to the provider endpoint holding it. OpenRouter supports explicit `session_id` in the request body or `x-session-id` header and recommends a stable conversation/workflow-run value. It also notes that routing features such as Auto Exacto can intentionally override stickiness, proving that affinity remains advisory and subordinate to provider/broker routing policy.

Primary references:
- https://openrouter.ai/blog/tutorials/prompt-caching-sticky-routing/
- https://openrouter.ai/docs/guides/routing/auto-exacto

### Fireworks / RunInfra-style OpenAI-compatible Services

Some services expose `x-session-affinity`, `prompt_cache_key`, `user`, or related carriers for sticky routing. These reinforce the generic capability shape, but Go-LIP must not assume every OpenAI-compatible service accepts the same fields. The provider profile must explicitly opt in.

### Anthropic and Gemini

Both support prompt caching, but direct provider caching semantics are not equivalent to a generic session-affinity header. Anthropic uses explicit cache controls/breakpoints; Gemini supports implicit caching and explicit cached-content resources. They are evidence that **prompt caching support does not imply cache-routing-header support**. Generic synthesis remains disabled unless a documented provider-specific affinity mechanism exists.

## Coding-Agent State

### OpenCode

Current OpenCode sends stable session routing headers from its own session identity (`x-session-affinity` and `X-Session-Id`) and has provider-specific `prompt_cache_key` transformation support. This validates automatic agent-side affinity as a useful optimization.

Relevant source paths:
- `packages/opencode/src/session/llm/request.ts`
- `packages/opencode/src/provider/transform.ts`
- `packages/opencode/src/plugin/openai/ws-pool.ts`

### OpenAI Codex

Current Codex distinguishes several identities:

- session/thread identity;
- `prompt_cache_key`;
- per-turn sticky `x-codex-turn-state`;
- Responses/WebSocket continuation identifiers;
- provider routing hints.

This distinction is critical. A per-turn sticky token must not become a cross-turn cache hint, and a response continuation token must not become a conversation-affinity identity.

Relevant source:
- `openai/codex/codex-rs/core/src/client.rs`

### Hermes Agent

Hermes is the strongest warning against naïvely setting `prompt_cache_key = raw session ID`. It maintains a logical cache scope and derives a content-addressed/stable cache key while explicitly documenting that many OpenAI-compatible endpoints reject unknown request fields. Hermes therefore both validates the value of automatic hinting and demonstrates why support must be provider-opt-in and precedence must preserve richer client-generated keys.

Relevant source paths:
- `agent/prompt_cache_scope.py`
- `agent/transports/codex.py`
- `agent/transports/chat_completions.py`
- `providers/base.py`

### Cline

Cline has explicit provider metadata for sticky-session projection, including OpenRouter `session_id`, and separate cache-control handling for Anthropic/Bedrock. This again supports a provider-specific projection contract rather than one universal wire field.

### Continue / Aider / DeepSeek Harness / Other Agents

Continue can pass `prompt_cache_key` on Responses requests but does not provide evidence of universal automatic derivation. Aider has explicit prompt-cache controls but no generic first-class downstream affinity generation was found. DeepSeek Harness includes schema awareness of `prompt_cache_key` but no active generic synthesis path was found. Exact current-tree searches for several other OSS coding agents did not reveal equivalent first-class affinity behavior.

Conclusion: proxy-side fallback generation has real UX value because agent support is heterogeneous.

## Current Go-LIP Brownfield Findings

### Existing foundation 1: secure session identity

`pkg/lipapi.SessionRef` already separates:

- `ClientSessionID`;
- `ContinuityKey`;
- `ALegID`;
- proxy-owned `AuthoritativeSessionID`;
- `ResumeToken`.

The comments explicitly warn that client hints are not proxy authority. This is the correct input boundary for generated downstream affinity.

### Existing foundation 2: core route affinity

`internal/core/affinity` already resolves session/client affinity keys. For session affinity it prefers proxy-owned authoritative session identity and only falls back according to existing policy. Runtime uses that key to keep Go-LIP routing sticky to a backend candidate.

This solves:

```text
client -> Go-LIP -> same backend candidate
```

It does not solve the provider's internal last hop:

```text
Go-LIP backend -> provider load balancer -> GPU/provider endpoint replica
```

The new feature should therefore reuse the existing conversation identity semantics without reimplementing route affinity.

### Existing foundation 3: prompt-cache request semantic

`pkg/lipapi.Call.PromptCacheKey` and the newer semantic-extension carrier already preserve a protocol-neutral prompt-cache hint. Executable backend ABI bridges can carry the key and proxy-owned session ID separately.

This means the new feature does **not** need a new canonical provider-specific field. It needs an effective-hint resolution step and provider adapter projection.

### Existing foundation 4: prompt-cache residency/control

The implemented residency contract deliberately defines backend-observed cache target/generation identity after provider preparation. Its research explicitly rejected using the physical A-leg/session ID as cache residency identity.

That decision remains correct and must not be reversed. The new synthesized hint is only a routing hint supplied before/with the provider request. The actual residency target remains whatever the backend later observes.

### Existing foundation 5: provider/candidate capability architecture

Go-LIP already uses model/candidate-aware capability/profile resolution in `internal/core/execbackend`, backend adapters, connector negotiation, and compiled routing/backend configuration. The new capability belongs there rather than in a provider-name switch in core.

### Existing foundation 6: provider-neutral connector ABI

`api/backendplugin/v1` already carries `prompt_cache_key` and `proxy_owned_session_id` separately and has negotiated semantic extensions. The brownfield default should be to reuse these fields and derive provider wire projection inside the configured backend/connector. A new ABI field is justified only if implementation proves the existing information is insufficient.

## Gap Analysis

### G1 — No generic effective downstream hint resolver

Existing code can carry a client `PromptCacheKey` and knows an authoritative session, but there is no single explicit contract that resolves:

1. explicit provider-specific client hint;
2. explicit protocol-neutral client cache key;
3. proxy-generated fallback;
4. unsupported/no hint.

**Required repair:** add a protocol-neutral effective-hint resolver with deterministic precedence and explicit source classification.

### G2 — No privacy-safe generated fallback contract

Raw session IDs must not be emitted to arbitrary providers. A deterministic opaque derivation with bounded output and keying policy is needed.

**Required repair:** add a secure derivation component that consumes trusted conversation scope and produces a transport-safe bounded identifier.

### G3 — Provider support is not one universal wire contract

Different providers use request JSON fields, headers, or broker-specific session fields. Some OpenAI-compatible endpoints reject unknown fields.

**Required repair:** add a provider/model/API-flavor capability describing semantic, transport, wire name, length bound, and synthesis support. Unknown providers remain off by default.

### G4 — Existing route affinity is internal-only

`internal/core/affinity` keeps Go-LIP on a backend but does not project affinity downstream.

**Required repair:** reuse the resolved session scope/authority but add a separate outbound provider projection stage. Do not merge the two subsystems.

### G5 — Existing prompt-cache residency docs can be misread as owning all cache identity

The old design correctly says residency identity is backend-observed, but the system now needs a documented additional pre-request routing-hint identity.

**Required repair:** update active documentation/comments where necessary to explicitly distinguish routing affinity hint vs residency target. Do not rewrite archived implementation history as if it had implemented this feature.

### G6 — Provider rollout is incomplete

The generic capability only produces user value once high-value providers are wired.

**Required repair:** include the initial documented provider set in this same implementation batch: OpenAI Responses, xAI Chat/Responses, Mistral, OpenRouter, and explicitly configured Fireworks/RunInfra-compatible profiles; explicitly keep direct Anthropic/Gemini generic synthesis off.

### G7 — No effect-oriented observability

A generated hint is not evidence of a cache hit.

**Required repair:** add low-cardinality source/projection metrics and correlate with existing cache-read/write evidence without logging the actual hint.

## Requirements Reconciliation Performed

The first requirements draft risked describing a single generated session key as the universal fallback. Brownfield/provider review changed the requirements in three material ways:

1. **Explicit client intent now has stronger precedence.** A client-generated logical cache key can be more accurate than the proxy's physical session identity (Hermes demonstrates this).
2. **Provider/model capability opt-in is mandatory.** Generic OpenAI compatibility is not sufficient evidence to inject `prompt_cache_key` or a custom header.
3. **Cache residency authority remains backend-observed.** Generated affinity is advisory routing metadata only and does not modify the completed residency/control contract.

These corrections are propagated into the design and tasks.

## Selected Design Direction

- Reuse existing trusted session/affinity identity semantics.
- Add a small protocol-neutral `DownstreamAffinityHint`/effective-hint contract with source classification.
- Derive fallback values deterministically and opaquely from trusted scope using configured secret/key material.
- Let provider/model capability resolve whether synthesis is legal and how to project the value to wire.
- Preserve explicit client provider hints and existing `PromptCacheKey` before using generated fallback.
- Keep core free of provider header/body names.
- Keep residency/control and keep-warm behavior unchanged.
- Add initial provider support and observability in the same implementation so the feature is complete.

## Rejected Alternatives

### Set `PromptCacheKey = AuthoritativeSessionID` globally

Rejected because client logical cache scope can differ from physical/proxy session scope, raw session IDs should not be leaked, and some providers reject unknown fields.

### Add `x-session-affinity` to every OpenAI-compatible request

Rejected because it is not a universal standard and can be ignored or rejected. Provider profiles must explicitly opt in.

### Make generated hint the cache residency target ID

Rejected because provider routing metadata is not proof of effective cache state. The existing residency contract correctly observes target identity after backend preparation/execution.

### Use principal/user identity as fallback

Rejected because it merges unrelated concurrent sessions, can hotspot a replica, leaks a broader identity scope, and is semantically coarser than agent conversations.

### Persist a new affinity table

Rejected because the fallback can be deterministically regenerated from existing trusted session scope. Persistence would add hot-path state, lifecycle complexity, and privacy surface without value.

### Add a central provider-name switch in core

Rejected because provider/API flavor support evolves independently and the repository already has provider/candidate-aware capability boundaries.

## Design Validation Verdict

**GO.** The feature can be implemented without new canonical trajectory authority, without changing scheduler/keep-warm semantics, and likely without a backend-plugin ABI expansion. The existing secure-session, route-affinity, prompt-cache semantic-extension, residency/control, provider-profile, and connector-negotiation foundations cover the hard architectural parts. The remaining work is a bounded outbound optimization layer plus provider adapters, tests, docs, and observability.

The implementation is only complete if the initial provider rollout and precedence/compatibility TCKs land in the same batch; leaving those as follow-up would recreate the original UX gap.
