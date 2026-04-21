# Reference client emulator — specification cross-check matrix

Normative URLs are listed in `research.md` under **Official API specification references (normative docs)**. This matrix ties each emulator to exercised surfaces and tests.

**Parity roadmap:** row IDs and `implemented` / `planned` / `out_of_scope` status for the full program live in [`../llm-api-parity/design.md`](../llm-api-parity/design.md) (tasks: [`../llm-api-parity/tasks.md`](../llm-api-parity/tasks.md)).

## Shared multimodal fixtures

- `testdata/refclient/tiny.png` — minimal valid PNG (1×1).
- `testdata/refclient/minimal.pdf` — minimal valid PDF for file-style inputs.

## 9.0.1 — OpenAI Responses API (`internal/refclient/openairesponses`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Create response (non-streaming) | Responses create | POST body + JSON response | `client_test.go` |
| Create response (streaming) | Responses streaming | SSE stream + terminal event | `client_test.go` |
| Auth | API key bearer | `Authorization` header | `client_test.go` |
| Multimodal — image | Image inputs guide | `input_image` in request JSON | `client_test.go` |
| Multimodal — document | PDF / file inputs | `input_file` with base64 `file_data` | `client_test.go` |
| Multimodal response — assistant output | Not claimed for v1 refclient evidence | Non-stream tests cover text output only; proxy canonical stream has no first-class assistant image/file event family | `client_test.go`, `VALIDATION_REVIEW.md` |

## 9.0.2 — OpenAI Chat Completions (`internal/refclient/openaichat`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Chat completion (non-streaming) | Chat create | POST + JSON | `client_test.go` |
| Chat completion (streaming) | Chat streaming | SSE `delta` events | `client_test.go` |
| Auth | API key bearer | `Authorization` header | `client_test.go` |
| Multimodal — image | Vision / image_url | image_url content part | `client_test.go` |
| Multimodal — document | Files in messages (where supported) | `file` part or image_url data URL for PDF | `client_test.go` |
| Multimodal response — assistant output | Not claimed for v1 refclient evidence | Non-stream / stream tests cover text deltas only | `client_test.go`, `VALIDATION_REVIEW.md` |

## 9.0.3 — Anthropic Messages (`internal/refclient/anthropicmessages`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Messages (non-streaming) | Messages API | POST + JSON | `client_test.go` |
| Messages (streaming) | Message streaming | SSE events | `client_test.go` |
| Errors | HTTP error body | 400 JSON error | `client_test.go` |
| Auth | `x-api-key` | request header | `client_test.go` |
| Multimodal — image | Image content blocks | `image` source base64 | `client_test.go` |
| Multimodal — document | Document PDF block | `document` base64 | `client_test.go` |
| Multimodal response — assistant output | Not claimed for v1 refclient evidence | Tests cover text / SSE events; no image/document assistant-output contract asserted for proxy v1 | `client_test.go`, `VALIDATION_REVIEW.md` |

## 9.3 — Anthropic Messages frontend (`internal/plugins/frontends/anthropic`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Messages (non-streaming) | Messages API | refclient POST + JSON via `httptest` | `integration_test.go` |
| Messages (streaming) | Message streaming | SSE `content_block_delta` + text | `integration_test.go` |
| Routing | `X-LIP-Route` | default + header selector | `decode_test.go`, `integration_test.go` |
| Multimodal — image / document | Content blocks | canonical `PartImageRef` / `PartFileRef` | `decode_test.go`, `integration_test.go` |
| Malformed JSON | Request validation | 400 | `integration_test.go` |

## 9.0.4 — Gemini generateContent (`internal/refclient/gemini`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| generateContent | Text generation / REST | POST + JSON | `client_test.go` |
| streamGenerateContent | Streaming | chunked JSON stream | `client_test.go` |
| Auth | API key (Google AI) | `x-goog-api-key` or client config | `client_test.go` |
| Multimodal — image | Parts / inline data | inline image bytes | `client_test.go` |
| Multimodal — document | File / PDF parts | inline PDF bytes | `client_test.go` |
| Multimodal response — image | Inline data output | non-stream `inlineData` image bytes parsed by SDK | `client_test.go` |
| Multimodal response — document | Inline data output | non-stream `inlineData` PDF bytes parsed by SDK | `client_test.go` |
