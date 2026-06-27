# API Standards (Steering)

## Purpose

This project sits between multiple client-facing APIs and multiple backend API flavors.
API standards exist to keep protocol behavior predictable while preventing protocol-specific logic
from leaking into the core runtime.

## Core rules

1. **Canonical in the middle.** Frontends decode to canonical contracts. Backends emit canonical events. No pairwise protocol translators.
2. **Streaming first.** If a protocol supports streaming, that is the primary execution path.
3. **Protocol legality over convenience.** Output framing, status codes, event types, and terminal error shapes must remain valid for the surfaced frontend protocol.
4. **Deterministic capability handling.** Unsupported required semantics must fail explicitly before upstream work starts.
5. **No hidden lossy downgrade.** If behavior is degraded, it must be intentional and represented through capability rules.
6. **Core owns shared semantics.** Only semantics needed across protocols or for core orchestration belong in canonical contracts.
7. **Adapters own wire details.** Provider payloads, transport shapes, and protocol-specific quirks stay inside adapters.

## Supported compatibility surfaces

The standard distribution bundles these client-facing surfaces:

- OpenAI Responses-compatible
- legacy OpenAI-compatible chat-style
- Anthropic Messages-compatible
- Gemini generateContent-compatible

Backend compatibility is grouped by adapter family:

- hosted/provider APIs: OpenAI Responses, legacy OpenAI-compatible, Anthropic Messages, Gemini generateContent, Bedrock Converse, ACP prompt-turn, OpenRouter, NVIDIA, Hugging Face, OpenAI Codex, OpenCode Go/Zen,
- local/OpenAI-compatible runtimes: Ollama (`ollama` / `ollama-cloud`), llama.cpp, LM Studio, vLLM, `localstub`,
- operator-defined custom OpenAI/Anthropic-compatible backend rows.

Exact support is code-owned by `internal/pluginreg/standard_table.go` and `pkg/lipsdk/standard_bundle.go`; docs should link to those files rather than duplicating row-level truth everywhere.

## Canonical contract guidance

Canonical contracts in `pkg/lipapi` should be:

- protocol-neutral,
- small and versionable,
- shaped around shared product semantics,
- free of provider SDK types and transport-server types.

Do not add a canonical concept just because one protocol happens to expose a feature.
Prefer plugin-local handling, capability catalogs, model inventory, or extension fields until the feature is clearly part of the shared product contract.

## Frontend rules

Driving adapters are responsible for:

- decoding protocol input into canonical requests,
- transport-level validation,
- frontend-specific limits and JSON/body guards,
- auth/session wire mapping at the edge,
- encoding canonical events and canonical errors into legal frontend responses.

Driving adapters may call concrete core services where that is the cleanest boundary.
They do not need inbound interfaces purely for symmetry.

## Backend rules

Driven adapters are responsible for:

- translating canonical requests into upstream/provider calls,
- mapping upstream responses into canonical events,
- keeping provider SDKs and wire models at the edge,
- translating infrastructure failures into core-understandable errors,
- declaring credential posture metadata for startup validation where the standard bundle can enforce trust boundaries,
- exposing model inventory and capability metadata through SDK/core seams when available.

No backend may require provider SDK types to cross into `pkg/lipapi`, `pkg/lipsdk`, or `internal/core`.

OpenAI-compatible and custom-compatible backends should reuse shared compatible-protocol helpers where practical, but they remain backend adapters rather than canonical shortcuts.

## Session, identity, and secure resume contracts

Session authority is proxy-owned once traffic enters the runtime:

- transport authentication attaches principals at the edge through stable SDK context/view contracts,
- frontends may pass client session hints, but must not grant authority by trusting client-provided A-leg or resume fields,
- secure-session BeginTurn runs before backend execution and before client-visible output,
- resume tokens and authoritative session IDs are treated as security-sensitive wire values,
- frontends map session denials into protocol-legal, client-safe errors while preserving operator diagnostics.

Any spec that changes session wire fields, principal propagation, or resume behavior must revalidate secure-session policy,
frontend parity, diagnostics redaction, and B2BUA lineage.

## HTTP and streaming conventions

- Use explicit content types.
- Preserve protocol-specific streaming framing rules at the frontend boundary.
- Do not buffer an entire backend response solely to make the streaming encoder easier.
- Respect request cancellation and client disconnects.
- Where a streaming frontend requires keepalive behavior during pre-output recovery windows, emit only protocol-legal keepalive frames/events.
- Keep non-streaming collection on top of the canonical event stream.

## Error handling

Frontend responses should distinguish between:

- bad client input,
- unsupported capability combinations,
- surfaced upstream failures,
- internal proxy failures,
- auth/session denials,
- cancellations and timeouts.

Rules:

- surface errors in the legal shape for the current frontend protocol,
- preserve enough structured detail for operators while avoiding raw backend leakage,
- keep canonical error categories stable even when frontend rendering differs,
- keep terminal stream errors inspectable with `errors.As` rather than string matching.

## Versioning and expansion

- add new protocol surfaces as plugins, not as core branches,
- add new canonical semantics only when multiple protocols truly need them,
- prefer narrow adapter-local anti-corruption layers over widening canonical contracts too early,
- route model/provider-specific differences through capability, inventory, or feature seams when possible,
- revalidate routing, streaming, and capability rules whenever a new surface changes shared semantics.

## API memory rules

When updating this file:

- capture enduring API and translation rules,
- keep core-vs-adapter ownership explicit,
- avoid endpoint-by-endpoint inventories,
- update when compatibility policy or streaming/error rules change materially.
