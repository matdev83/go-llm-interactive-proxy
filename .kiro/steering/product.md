# Product Overview (Steering)

## Core Identity & Value Proposition

**LLM Interactive Proxy (Go)** is a universal LLM control plane that decouples client integrations from backend models and providers.

- **Universal Translation**: Frontends decode to canonical (`pkg/lipapi`); backends emit canonical events. Zero pairwise translators.
- **Policy-Owning Core**: Dynamic routing, weighted load-balancing, ordered failover, parallel races, TTFT budgets, A-leg routing overrides, and pre-output recovery are strictly core-owned.
- **Fail-Fast Capabilities**: Mismatches or lossy feature degradations fail explicitly before upstream execution.
- **Observable Continuity**: A-leg continuity and B-leg attempt lineage are fully observable and audit-logged.

---

## Supported Compatibility Surfaces

- **Client Frontends**: OpenAI Responses API & OpenResponses 2026-04-24 (HTTP POST/SSE + WebSocket turns/continuation), legacy OpenAI Chat/Models, Anthropic Messages API, Gemini `generateContent`.
- **Essential hosted backends**: OpenAI Responses, legacy OpenAI Chat, Anthropic Messages, Gemini `generateContent`, Bedrock Converse, Alibaba Token Plan International (`alibabatokenplanintl`), plus built-in custom-compatible families.
- **Optional connectors**: ACP prompt-turn family (`acp`, `agycliacp`, `cursorcliacp`, `cursorsdk`, `geminicliacp`), OpenRouter, NVIDIA, Hugging Face, OpenAI Codex, OpenCode Go/Zen, Ollama (`ollama`/`ollama-cloud`), llama.cpp, LM Studio, vLLM, `localstub`.

Source of truth: [`internal/standardplugins/standard_table.go`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/standardplugins/standard_table.go) and [`pkg/lipsdk/standard_bundle.go`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/pkg/lipsdk/standard_bundle.go).

---

## Core Product Pillars

1. **Streaming-First Execution**: Primary path is streaming. Non-streaming collects events over the canonical stream.
2. **Authority Coordination**: Execution stage limits and settle failure recording via [`internal/core/authoritycoord`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/authoritycoord). Non-money quota and rate-limit rules stay here; production YAML must not encode monetary `budget` / `spend_cap` / `money_nano` authority.
3. **Control Plane Projections**: Operator facts, metering usage bridges, and readiness reporting via [`internal/core/controlplane`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/controlplane). Control-plane rows are not monetary truth.
4. **Usage-Record Billing**: Run a cheap credit screen, admit one atomic operational exposure after route/quote, execute billing-blind, append BillingCallID-scoped terminal usage, then post-usage customer settlement closes exposure while provider COGS posts independently. Stock `lipstd` and public `pkg/lipruntime.Options` do not invent accounts or open the journal; internal hosts inject via `runtimebundle.ComposeBilling`. Recipe: [`docs/billing-host-composition.md`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/docs/billing-host-composition.md).
5. **Interleaved Reasoning**: Structured reasoning block retention across turns/attempts via [`internal/core/interleavedthinking`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/interleavedthinking), with opt-in semantic compression of plain-text reasoning that keeps original artifacts authoritative and fails closed.
6. **Proxy-Owned Conversation View**: Replay-stable message identities let the proxy tag client-visible content as never-forwarded-to-backend (`never_backend`), persist client-hidden model-visible steering, and run generic proxy-local turns — all bounded, proxy-owned state ([`internal/core/conversationview`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/conversationview)).
7. **Pluggable Terminal Decisions**: Provisional-terminal continuation decisions flow through one core chokepoint with a single exclusive provider slot (e.g., Agent Loop Guard); core stays provider-neutral and removal restores generic behavior.
8. **Decoupled Extensibility**: Hooks and extension stages (`pkg/lipsdk`) provide typed facades for auth, sessions, workspace resolution, tool reactors, completion gates, and accounting without core coupling. Compatible-provider growth is data-driven (`internal/providerprofiles`) plus contract TCKs, not a Cartesian frontend×backend product.
9. **Fail-Closed Security**: Mandatory secure-session authority (`securesession`), loopback-only `no_auth`, and non-root execution.

---

## Architectural Non-Goals

- Avoid Python-era legacy claims without Go implementation.
- Do not leak provider-specific or transport-specific logic into `internal/core/`.
- Reject textbook `app/domain/adapters` directory taxonomy churn.
- Forbid Go native dynamic binary loading (Go `plugin` package is forbidden; use out-of-process gRPC connectors under `connectors/`).
- Prevent feature additions that compromise small core policy ownership or contract testability.
- Do not restore stream-time price enrichment, token-ledger monetary writes, or YAML auto-open of the billing journal.
