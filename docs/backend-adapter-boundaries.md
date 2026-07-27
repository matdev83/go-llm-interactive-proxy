# Backend adapter boundaries (mapping vs SDK plumbing)

Backend adapters split across **essential builtins** (root module) and **optional executable connectors** ([ADR 0008](adr/0008-hybrid-backend-connector-plugins.md)).

1. **Canonical mapping** — Translating [`pkg/lipapi`](../pkg/lipapi/) calls and event streams to provider wire formats and back. This is what **conformance**, **refbackend** emulators, and **golden** fixtures primarily constrain.
2. **SDK / transport plumbing** — Vendor client configuration, connection pooling, retries compatible with core policy (no failover after first output is enforced in the **executor**, not hidden inside retries), credential injection, and error shaping into [`lipapi`](../pkg/lipapi/) errors.

Regression tests **must** cover mapping behavior (streaming order, tool events, multimodal). SDK plumbing is covered by smaller unit tests in each adapter plus review; it is **not** reconstructible from mapping tests alone.

## Essential builtins (`internal/plugins/backends/`)

| Backend plugin | Mapping evidence (primary) | Plumbing notes |
|----------------|---------------------------|----------------|
| `openairesponses` | Conformance parity + refbackend OpenAI Responses shapes | `openai-go` client; static API key / optional key pool via root `UpstreamAPIKeys` |
| `openailegacy` | Parity + legacy chat completions wire | `openai-go` chat completions path |
| `anthropic` | Parity + Messages API wire | `anthropic-sdk-go`; SSE streaming |
| `gemini` | Parity + Gemini generateContent stream | `google.golang.org/genai` |
| `bedrock` | Parity + Bedrock converse/stream conventions | AWS SDK v2; workload credential mode |
| custom OpenAI/Anthropic compatible kinds | Config + parity helpers | Dependency-free compatible modes in essential allowlist |

Root `ResolveUpstreamAPIKeysFromEnv` resolves only OpenAI / Anthropic / Gemini env pools for these builtins.

## Optional executable connectors (`connectors/`)

Optional kinds are separate modules installed as digests + closed manifests. Credentials are connector-local (plugin YAML / secrets), not root env pools. Do not re-home them under `internal/plugins/backends/` or add them to essential/`standard_table` fixed tables.

| Connector (examples) | Notes |
|----------------------|-------|
| `openrouter`, `nvidia`, `huggingface` | Hosted OpenAI-compatible HTTP; shared helpers may live in `connector-support/openaicompat` |
| `ollama`, `llamacpp`, `lmstudio`, `vllm`, `localstub` | Local / OpenAI-compatible runtimes |
| `opencode`, `codex` | Multi-export artifacts (Go/Zen; Codex HTTP + app-server) |
| ACP family (`acp`, `cursorcliacp`, …) | Local-agent stdio; support helpers in `connector-support/acp` |

## Shared OpenAI-compatible helpers

Root-module [`internal/plugins/backends/openaicompat`](../internal/plugins/backends/openaicompat/) may still hold helpers used by essential custom-compatible paths. Connector-owned OpenAI-compat support prefers `connector-support/` so optional modules do not pull essential tables into their graph.

Dependency direction is one-way: concrete adapters may import shared compat helpers; helpers must not import concrete optional connectors. Core (`internal/core/...`), `pkg/lipapi`, and `pkg/lipsdk` (except the public backendplugin ABI) must not import vendor SDKs.

Import boundaries are enforced in [`internal/archtest`](../internal/archtest/).

When changing an adapter, decide whether the diff touches **mapping** (requires conformance/golden updates) or **plumbing** (client options, headers, timeouts — extend adapter-local tests).
