# API Standards (Steering)

## Core Architectural Invariants

1. **Canonical in the middle**: Frontends decode to canonical (`pkg/lipapi`); backends emit canonical events. Zero pairwise translators.
2. **Streaming primary**: Non-streaming is collection over canonical SSE stream events.
3. **Protocol legality**: Output framing, status codes, and terminal error shapes must remain protocol-legal for the active frontend.
4. **Deterministic capabilities**: Unsupported required semantics MUST fail explicitly before upstream execution starts.
5. **No lossy downgrades**: Any capability degradation must be explicit in capability catalogs.
6. **Core owns product semantics**: Only cross-protocol or core-orchestrated semantics belong in `pkg/lipapi`.
7. **Adapters own wire details**: Provider SDKs, vendor payloads, and transport quirks stay inside backend/frontend adapters.

---

## Supported Surface Matrix

- **Frontends**: OpenAI Responses API & OpenResponses 2026-04-24 (HTTP POST/SSE + WebSocket turns/continuation), legacy OpenAI Chat/Models, Anthropic Messages API, Gemini `generateContent`.
- **Essential hosted backends**: OpenAI Responses, legacy OpenAI Chat, Anthropic Messages, Gemini `generateContent`, Bedrock Converse, Alibaba Token Plan International (`alibabatokenplanintl`), plus built-in custom-compatible families.
- **Optional connectors**: ACP prompt-turn family, OpenRouter, NVIDIA, Hugging Face, OpenAI Codex, OpenCode Go/Zen, Ollama (`ollama`/`ollama-cloud`), llama.cpp, LM Studio, vLLM, `localstub`, and other executable gRPC plugins under `connectors/`.

Source of truth: [`internal/standardplugins/standard_table.go`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/standardplugins/standard_table.go) and [`pkg/lipsdk/standard_bundle.go`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/pkg/lipsdk/standard_bundle.go).

---

## Canonical Contracts & Dialects (`pkg/lipapi`)

- Keep contracts protocol-neutral, versionable, and free of provider SDK / HTTP server types.
- **Reasoning Carriers (`EventReasoningPart`)**:
  - Chat-style text: `EventReasoningDelta` / text fields.
  - Anthropic signed thinking: Opaque delta carriers.
  - OpenAI Responses: Dialect `openai.responses.reasoning_item.v1` with allowlisted Opaque JSON schema ([`internal/plugins/protocols/openairesponsesitem`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/protocols/openairesponsesitem)).
  - OpenAI Codex native compaction: Opaque reasoning continuations retained across compaction turns ([`codexclientcompat`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/features/codexclientcompat)).
  - Alibaba Token Plan: Dialect-aware streaming reasoning deltas forwarded without dropping markers.
  - Never silently convert dialects across providers.
- **Tool classification**: Canonical `ToolEvent` carries a coarse `ToolCategory` and conservative `MayMutateLocalFS` derived from the tool **name** (`ClassifyToolName`). Match is trim + case-fold + exact alias; no argument, schema, provider, or harness inspection. Unknown/empty names are `unknown` with `MayMutateLocalFS=true`. Name-less fragments inherit by `ToolCallID` for the request; rewrites recompute from the effective name. Classification is derived metadata for policy/reactors, never allow/deny authority.

---

## Frontend Pipeline & Adapter Rules

- Driving adapters decode incoming HTTP/SSE/WebSocket into canonical requests and encode canonical events to protocol responses.
- **Unified Pipeline**: All standard HTTP/SSE handlers and pumps are unified behind [`internal/plugins/frontends/frontendpipe/`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe) and `stream.PumpSSE`.
- **OpenResponses Extension**: [`internal/plugins/frontends/openresponses`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openresponses) adds WebSocket turn management, allowed-tool filters, and HTTP/WS continuation lifecycle stores.

---

## Backend Adapter Rules

- Driven adapters translate canonical requests -> upstream provider calls, and upstream responses -> canonical events.
- Keep provider SDK types strictly inside adapter packages (`internal/plugins/backends/` or `connectors/`).
- Reuse compatible-protocol helpers ([`internal/plugins/backends/openaicompat`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/backends/openaicompat), `openresponsescompat`, `compatmode`, `transporterr`) without making them canonical shortcuts.
- Backend factories declare credential and access-scope posture metadata for startup trust validation.

---

## Session Authority, Identity & Sentinel Error Rules

- **Session Authority**: Proxy-owned (`securesession`). `BeginTurn` validates authority before backend execution. Client session hints are untrusted.
- **Proxy Identity**: Product identity (`Server` header on A-leg, `User-Agent` / OpenRouter attribution on B-leg) is proxy-owned and configured separately from session authority (see [`docs/proxy-identity.md`](file:///C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/docs/proxy-identity.md)).
- **Sentinel Error Protection**: Never leak raw internal stack traces, local paths, or unredacted provider payloads to clients. Surface legal protocol errors to clients while recording diagnostic details in internal logs and audit sinks.
- Terminal stream errors must be inspectable with `errors.As`.
