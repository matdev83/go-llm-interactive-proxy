# API Standards (Steering)

## Purpose

This project sits between multiple client-facing APIs and multiple backend API flavors. API standards exist to keep protocol behavior predictable while preventing protocol-specific logic from leaking into the core runtime.

## Core Rules

1. **Canonical in the middle.** Frontends decode to canonical contracts. Backends emit canonical events. No pairwise protocol translators.
2. **Streaming first.** If a protocol supports streaming, that is the default execution path.
3. **Protocol legality over convenience.** Output framing, status codes, event types, and terminal error shapes must remain valid for the surfaced frontend protocol.
4. **Deterministic capability handling.** Unsupported required semantics must fail explicitly before upstream work starts.
5. **No hidden lossy downgrade.** If behavior is degraded, it must be intentional and represented through capability rules.

## Supported Compatibility Surfaces

The standard distribution is expected to bundle these client-facing surfaces:

- OpenAI Responses-compatible
- Legacy OpenAI-compatible chat-style
- Anthropic Messages-compatible
- Gemini generateContent-compatible

And these backend flavors:

- OpenAI Responses-compatible
- Legacy OpenAI-compatible
- Anthropic Messages-compatible
- Gemini generateContent-compatible
- Bedrock Converse-compatible
- ACP prompt-turn compatible

## HTTP and Streaming Conventions

- Use explicit content types.
- Preserve protocol-specific streaming framing rules at the frontend boundary.
- Do not buffer an entire backend response solely to make the streaming encoder easier.
- Respect request cancellation and client disconnects.
- Where a streaming frontend requires keepalive behavior during pre-output recovery windows, emit only protocol-legal keepalive frames/events.

## Error Handling

Frontend responses should distinguish between:

- bad client input,
- unsupported capability combinations,
- surfaced upstream failures,
- internal proxy failures,
- cancellations/timeouts.

Rules:

- surface errors in the legal shape for the current frontend protocol,
- preserve enough structured detail for operators while avoiding raw backend leakage,
- keep canonical error categories stable even when frontend rendering differs.

## Versioning and Expansion

- Add new protocol surfaces as plugins, not as core branches.
- Add new canonical semantics only when multiple protocols truly need them.
- When one protocol exposes a unique feature, prefer extension fields or plugin-local handling unless the feature becomes part of the shared product contract.

## API Memory Rules

When updating this file:

- capture enduring API and translation rules,
- avoid endpoint-by-endpoint inventories,
- update when compatibility policy or streaming/error rules change materially.
