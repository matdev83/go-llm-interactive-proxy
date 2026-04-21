# Migration goldens (Python LIP → Go)

These fixtures are copied or derived from the sibling Python repository
`github.com/matdev83/llm-interactive-proxy` to anchor cross-implementation wire parity checks.

| File | Provenance |
|------|------------|
| `python_lip_openai_responses_http_streaming.json` | Copied from `tests/integration/fixtures/responses_api_frontend/http_streaming_sse.json` at commit `66b8cdd836d3b966fce8900d1ad017003e03564e`. Endpoint: OpenAI Responses streaming (SSE event payloads as JSON objects). Streaming mode: HTTP SSE contract fixture. |
| `python_lip_openai_responses_http_nonstream.json` | Derived minimal completed Responses object matching the same contract style as `http_streaming_sse.json` final `response.completed` payload (same commit tree). Endpoint: `POST /v1/responses` non-streaming JSON body. Streaming mode: **non-stream** (`stream:false`). |
| `python_lip_anthropic_messages_nonstream.json` | Minimal Anthropic Messages API-style assistant JSON synthesized to mirror shapes exercised by Python integration tests in the same commit tree (no single-file fixture existed under `tests/integration/fixtures/` for Anthropic). Endpoint: `POST /v1/messages` non-streaming JSON body. Streaming mode: **non-stream**. |

Import date into Go repo: 2026-04-21.
