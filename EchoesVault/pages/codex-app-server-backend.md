---
type: architecture
title: Codex App Server Backend
description: OpenAI Codex CLI app-server export — protocol mapping, handshake, lifecycle, and event translation (connectors/codex).
stack: [go]
tags: [backends, codex, connectors, subprocess, local-agent]
status: active
---

# Codex App Server Backend

## Overview

The Codex App Server export is part of the external **`connectors/codex`** executable plugin artifact (kind `openai-codex-app-server`). It launches the OpenAI Codex CLI in `app-server` mode over stdio and speaks a JSON-RPC 2.0 protocol distinct from ACP. Unlike hosted provider backends, this is a **local-agent** connector that uses the user's personal Codex login (OAuth-style), so it is treated as credential-none / local-only.

The HTTP Codex export (`openai-codex`) ships in the **same** connector artifact. Model catalog ownership lives in `connectors/codex/internal/catalog` (connector-local; not a root-core package). See [backend-connector-plugins](backend-connector-plugins.md) and [ADR 0008](../../docs/adr/0008-hybrid-backend-connector-plugins.md).

## Protocol Differences from ACP

The Codex app-server protocol is **not** ACP (`session/prompt` → `session/update`). It uses its own lifecycle:

| Stage | Codex Method | ACP Equivalent |
|---|---|---|
| Handshake | `initialize` → `initialized` notification | `initialize` → `authenticate` |
| Session creation | `thread/start` (returns thread ID) | `session/new` (returns session ID) |
| Prompt turn | `turn/start` (returns NDJSON stream) | `session/prompt` (returns NDJSON stream) |
| Text deltas | `item/agentMessage/delta` notification | `session/update` with `agent_message_chunk` |
| Reasoning deltas | `item/reasoning/summaryTextDelta`, `item/reasoning/textDelta` | `session/update` with `reasoning` |
| Tool completion | `item/completed` notification | `session/update` with `tool_call` |
| Terminal | `turn/completed` (response with `id` matching `turn/start`) | `session/prompt` response with `stopReason` |
| Server requests | `execCommandApproval`, `applyPatchApproval`, `item/*/requestApproval` | `vendor/*` methods |

Implementation lives under `connectors/codex/internal/appserver` (and related connector-local packages). Shared ACP-shaped helpers, when needed, come from `connector-support/acp` — not from re-adding optional kinds to root essential tables.

## Handshake Flow

```
Proxy                          Codex App-Server
  |--- initialize (id=1) --------->|
  |<-- result: protocolVersion -----|
  |--- initialized (notification) ->|
  |--- thread/start (id=2) -------->|
  |<-- result: {id: "thread-xxx"} --|
  |--- turn/start (id=3) ---------->|
  |<-- item/agentMessage/delta -----|  (NDJSON stream)
  |<-- item/reasoning/summaryDelta -|
  |<-- item/completed --------------|
  |<-- result: {turnId: "turn-xx"} -|  (terminal, id matches turn/start)
```

### thread/start Result Shape Compatibility

Handshake accepts the `thread/start` result in two shapes for cross-version compatibility with the Codex CLI:

- **Flat (preferred):** `{"id": "thread-xxx"}`
- **Nested (fallback):** `{"thread": {"id": "thread-xxx"}}`

## Operator notes

- Install the packaged `connectors/codex` artifact; enable `openai-codex-app-server` in config.
- Catalog knobs (`catalog_enabled`, `catalog_fallback_path`, `catalog_codex_binary_path`) are connector instance config — not root core config types.
- See [`docs/openai-codex-backend.md`](../../docs/openai-codex-backend.md) for Go operator behavior.
