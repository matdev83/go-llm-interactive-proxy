# Backend adapter boundaries (mapping vs SDK plumbing)

Official backend plugins live under [`internal/plugins/backends/`](../internal/plugins/backends/). Each adapter has two distinct concerns:

1. **Canonical mapping** — Translating [`pkg/lipapi`](../pkg/lipapi/) calls and event streams to provider wire formats and back. This is what **conformance**, **refbackend** emulators, and **golden** fixtures primarily constrain.
2. **SDK / transport plumbing** — Vendor client configuration, connection pooling, retries compatible with core policy (no failover after first output is enforced in the **executor**, not hidden inside retries), credential injection, and error shaping into [`lipapi`](../pkg/lipapi/) errors.

Regression tests **must** cover mapping behavior (streaming order, tool events, multimodal). SDK plumbing is covered by smaller unit tests in each plugin plus review; it is **not** reconstructible from mapping tests alone.

## Per adapter

| Backend plugin | Mapping evidence (primary) | Plumbing notes |
|----------------|---------------------------|----------------|
| `openairesponses` | Conformance parity + refbackend OpenAI Responses shapes | `openai-go` client; static API key / optional key pool |
| `openailegacy` | Parity + legacy chat completions wire | `openai-go` chat completions path |
| `anthropic` | Parity + Messages API wire | `anthropic-sdk-go`; SSE streaming |
| `gemini` | Parity + Gemini generateContent stream | `google.golang.org/genai` |
| `bedrock` | Parity + Bedrock converse/stream conventions | AWS SDK v2; workload credential mode |
| `acp` | Parity + ACP subset (tools deferred per matrix) | HTTP client + ACP-specific session/update flows |
| `nvidia` | Parity + NVIDIA NIM chat/responses wire | `openai-go` client; `NVIDIA_API_KEY` env pool; `max_tokens` remap, `stream_options` strip, `extra_body` pass-through |
| `openrouter` | Parity + OpenRouter chat/responses wire | `openai-go` client; shared invoke/event mapping via [`openaicompat`](../internal/plugins/backends/openaicompat/); OpenRouter-specific headers and extensions |
| `huggingface` | Parity + Hugging Face Inference Providers chat wire | `openai-go` client; shared invoke/event mapping via [`openaicompat`](../internal/plugins/backends/openaicompat/); chat completions only (`TransportChatOnly`); `HUGGINGFACE_API_KEY` env pool |
| `local-stub` | Dogfood YAML + executor stub tests | No upstream credentials ([`CredentialNone`](../pkg/lipsdk/backend_security.go)); deterministic text |

## Shared OpenAI-compatible adapter layer

[`internal/plugins/backends/openaicompat`](../internal/plugins/backends/openaicompat/) holds shared `openai-go` invoke and event-mapping helpers for backends that speak OpenAI-compatible APIs. It lives at the backend adapter edge alongside concrete providers (`openrouter`, `nvidia`), not in core or public contracts.

Dependency direction is one-way: concrete providers may import `openaicompat`; `openaicompat` must not import concrete provider packages. Core (`internal/core/...`), `pkg/lipapi`, and `pkg/lipsdk` must not import `openai-go` or other vendor SDKs.

`openaicompat` may depend on first-party canonical OpenAI adapter packages such as `openailegacy` and `openairesponses` for shared request parameter construction. Those packages are protocol codecs, not concrete hosted providers, and remain under the same backend adapter boundary.

Import boundaries are enforced in [`internal/archtest/openaicompat_boundaries_test.go`](../internal/archtest/openaicompat_boundaries_test.go).

When changing an adapter, decide whether the diff touches **mapping** (requires conformance/golden updates) or **plumbing** (client options, headers, timeouts — extend plugin-local tests).
