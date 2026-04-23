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

The standard distribution is expected to bundle these client-facing surfaces:

- OpenAI Responses-compatible
- legacy OpenAI-compatible chat-style
- Anthropic Messages-compatible
- Gemini generateContent-compatible

And these backend flavors:

- OpenAI Responses-compatible
- legacy OpenAI-compatible
- Anthropic Messages-compatible
- Gemini generateContent-compatible
- Bedrock Converse-compatible
- ACP prompt-turn compatible

## Canonical contract guidance

Canonical contracts in `pkg/lipapi` should be:

- protocol-neutral,
- small and versionable,
- shaped around shared product semantics,
- free of provider SDK types and transport-server types.

Do not add a canonical concept just because one protocol happens to expose a feature.
Prefer plugin-local handling or extension fields until the feature is clearly part of the shared product contract.

## Frontend rules

Driving adapters are responsible for:

- decoding protocol input into canonical requests,
- transport-level validation,
- auth and transport concerns at the edge,
- encoding canonical events and canonical errors into legal frontend responses.

Driving adapters may call concrete core services where that is the cleanest boundary.
They do not need inbound interfaces purely for symmetry.

## Backend rules

Driven adapters are responsible for:

- translating canonical requests into upstream/provider calls,
- mapping upstream responses into canonical events,
- keeping provider SDKs and wire models at the edge,
- translating infrastructure failures into core-understandable errors.

No backend may require provider SDK types to cross into `pkg/lipapi`, `pkg/lipsdk`, or `internal/core`.

## HTTP and streaming conventions

- Use explicit content types.
- Preserve protocol-specific streaming framing rules at the frontend boundary.
- Do not buffer an entire backend response solely to make the streaming encoder easier.
- Respect request cancellation and client disconnects.
- Where a streaming frontend requires keepalive behavior during pre-output recovery windows, emit only protocol-legal keepalive frames/events.

## Error handling

Frontend responses should distinguish between:

- bad client input,
- unsupported capability combinations,
- surfaced upstream failures,
- internal proxy failures,
- cancellations and timeouts.

Rules:

- surface errors in the legal shape for the current frontend protocol,
- preserve enough structured detail for operators while avoiding raw backend leakage,
- keep canonical error categories stable even when frontend rendering differs.

## Versioning and expansion

- add new protocol surfaces as plugins, not as core branches,
- add new canonical semantics only when multiple protocols truly need them,
- prefer narrow adapter-local anti-corruption layers over widening canonical contracts too early,
- revalidate routing, streaming, and capability rules whenever a new surface changes shared semantics.

## API memory rules

When updating this file:

- capture enduring API and translation rules,
- keep core-vs-adapter ownership explicit,
- avoid endpoint-by-endpoint inventories,
- update when compatibility policy or streaming/error rules change materially.

---
_Updated 2026-04-23: canonical contract guidance, frontend/backend ownership, pragmatic adapter rules._
