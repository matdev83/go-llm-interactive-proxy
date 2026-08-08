# Product Overview (Steering)

## Core Identity & Value Proposition

**LLM Interactive Proxy (Go)** is a universal LLM control plane that decouples client integrations from backend models and providers.

- **Universal Translation**: Frontends decode to canonical (`pkg/lipapi`); backends emit canonical events. Zero pairwise translators.
- **Policy-Owning Core**: Dynamic routing, weighted load-balancing, ordered failover, parallel races, TTFT budgets, and pre-output recovery are strictly core-owned.
- **Fail-Fast Capabilities**: Mismatches or lossy feature degradations fail explicitly before upstream execution.
- **Observable Continuity**: A-leg continuity and B-leg attempt lineage are fully observable and audit-logged.

---

## Supported Compatibility Surfaces

- **Client Frontends**: OpenAI Responses API & OpenResponses 2026-04-24 (HTTP POST/SSE + WebSocket turns/continuation), legacy OpenAI Chat/Models, Anthropic Messages API, Gemini `generateContent`.
- **Hosted Backends**: OpenAI Responses, legacy OpenAI Chat, Anthropic Messages, Gemini `generateContent`, Bedrock Converse, ACP prompt-turn family (`acp`, `agycliacp`, `cursorcliacp`, `cursorsdk`, `geminicliacp`), OpenRouter, NVIDIA, Hugging Face, OpenAI Codex, OpenCode Go/Zen, Alibaba Token Plan International (`alibabatokenplanintl`).
- **Local Runtimes**: Ollama (`ollama`/`ollama-cloud`), llama.cpp, LM Studio, vLLM, `localstub`.

Source of truth: [`internal/standardplugins/standard_table.go`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/standardplugins/standard_table.go) and [`pkg/lipsdk/standard_bundle.go`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/pkg/lipsdk/standard_bundle.go).

---

## Core Product Pillars

1. **Streaming-First Execution**: Primary path is streaming. Non-streaming collects events over the canonical stream.
2. **Authority Coordination**: Execution stage limits and settle failure recording via [`internal/core/authoritycoord`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/authoritycoord).
3. **Control Plane Ledger**: Fact projections, metering usage bridges, and readiness reporting via [`internal/core/controlplane`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/controlplane).
4. **Interleaved Reasoning**: Structured reasoning block retention across turns/attempts via [`internal/core/interleavedthinking`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/interleavedthinking).
5. **Decoupled Extensibility**: Hooks and extension stages (`pkg/lipsdk`) provide typed facades for auth, sessions, workspace resolution, tool reactors, completion gates, and accounting without core coupling.
6. **Fail-Closed Security**: Mandatory secure-session authority (`securesession`), loopback-only `no_auth`, and non-root execution.

---

## Architectural Non-Goals

- NO Python-era legacy claims without Go implementation.
- NO provider-specific or transport-specific logic in `internal/core/`.
- NO textbook `app/domain/adapters` directory taxonomy churn.
- NO Go native dynamic binary loading (Go `plugin` package forbidden; use out-of-process gRPC connectors under `connectors/`).
- NO feature addition that compromises small core policy ownership or contract testability.
