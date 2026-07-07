---
type: architecture
title: Codex App Server Backend
description: OpenAI Codex CLI app-server backend — protocol mapping, handshake, lifecycle, and event translation.
stack: [go]
tags: [backends, codex, acp, subprocess, local-agent]
status: active
---

# Codex App Server Backend

## Overview

The Codex App Server backend (`internal/plugins/backends/codexappserver/`) launches the OpenAI Codex CLI in `app-server` mode over stdio and speaks a JSON-RPC 2.0 protocol distinct from ACP. Unlike hosted provider backends, this is a **local-agent backend** that uses the user's personal Codex login (OAuth-style), so it is treated as credential-none.

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

Because of this protocol difference, the Codex App Server backend does **not** use `acp.SubprocessConnectorSpec`. It has its own backend runner (`codexBackend`) that reuses ACP infrastructure types (`RuntimePool`, `WorkspacePolicy`, `Transport`, `ProcessStarter`) but implements its own handshake, stream mapper, and server-request handler.

## Architecture

```
codexappserver/
├── connector.go              — Config, resolveExecutable, buildCodexCommand, codexServerRequestHandler, New(), NewWithStarter()
├── spec.go                   — codexSpec, codexBackend runner: Open(), ensureProcess(), runCodexHandshake(), codexManagedStream, codexClient
├── stream.go                 — codexStream: NDJSON line mapper, notification → canonical event translation, item/completed summary builders (buildItemCompletionSummary/buildCommandSummary/buildFileChangeSummary/commandBasename) routed through acp.FormatToolCompletionSummary
├── doc.go                    — Package documentation
├── connector_test.go         — Unit tests (config, command build, server request handler, commandBasename, New())
├── stream_internal_test.go   — Unit tests (mapNotification dispatch, item/completed summary builders)
├── integration_test.go       — Integration tests (full lifecycle via fakeProcess + fakeStarter)
└── lifecycle_contract_test.go — leglifecycle.BLegAttempt contract assertions for codexStream/codexManagedStream
```

### Reused ACP Infrastructure

The backend reuses these exported types from `internal/plugins/backends/acp/`:

- `acp.RuntimePool` / `acp.RuntimeKey` / `acp.RuntimePoolConfig` — subprocess lifecycle pool
- `acp.WorkspacePolicy` — workspace resolution with hint keys
- `acp.Transport` / `acp.NewStdioTransport` — stdio JSON-RPC transport
- `acp.Process` / `acp.ProcessStarter` / `acp.OSProcessStarter` — subprocess abstraction
- `acp.NDJSONStreamBase` / `acp.NDJSONStreamStrategy` / `acp.IsInboundServerRequest` — embedded NDJSON scanner/framing base the `codexStream` builds on
- `acp.FormatToolCompletionSummary` — shared fenced `Tool:` summary formatter used by the `item/completed` summary builders in `stream.go`
- `acp.TranscriptHistoryCoordinator` — conversation prefix tracking

### Handshake Flow

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

`runCodexHandshake` accepts the `thread/start` result in two shapes for cross-version compatibility with the Codex CLI:

- **Flat (preferred):** `{"id": "thread-xxx"}`
- **Nested (fallback):** `{"thread": {"id": "thread-xxx"}}`

The flat shape is checked first; the nested shape is a fallback for CLI versions that wrap the thread ID under `result.thread.id`. Both are covered by integration tests (`TestIntegration_codexAppServerStreamingText` for flat, `TestIntegration_codexAppServerAcceptsNestedThreadStartResult` for nested). An empty thread ID after both fallbacks is a hard error.

### Event Mapping

| Codex Notification | Canonical Event |
|---|---|
| `item/agentMessage/delta` (delta) | `EventTextDelta` |
| `item/reasoning/summaryTextDelta` (delta) | `EventReasoningDelta` |
| `item/reasoning/textDelta` (delta) | `EventReasoningDelta` |
| `item/completed` (command/fileChange) | `EventTextDelta` (fenced summary) |
| `turn/completed` | `EventResponseFinished` |
| `item/started`, `turn/started`, `thread/started` | Suppressed (no event) |
| `item/commandExecution/outputDelta`, `item/fileChange/outputDelta` | Suppressed |
| `item/plan/delta`, `turn/diff/updated`, `thread/tokenUsage/updated` | Suppressed |

### Server Request Handling

The Codex app-server sends inbound JSON-RPC requests for approval decisions. The `codexServerRequestHandler` auto-accepts known approval methods and declines unknown ones:

| Method | Response |
|---|---|
| `execCommandApproval` | `{decision: "accept"}` |
| `applyPatchApproval` | `{decision: "accept"}` |
| `item/commandExecution/requestApproval` | `{decision: "accept"}` |
| `item/fileChange/requestApproval` | `{decision: "accept"}` |
| `item/permissions/requestApproval` | Echo back requested permissions as granted |
| Unknown methods | `{decision: "decline"}` (fail closed) |

## Configuration

| Config Field | Description | Default |
|---|---|---|
| `Executable` | Path to Codex CLI binary | Resolved from `CODEX_BIN` env, PATH, or npm-global |
| `Model` | Default model (vendor prefix stripped) | `auto` |
| `ConfigOverrides` | `-c key=value` overrides | None |
| `ExtraArgs` | Additional CLI arguments after `--stdio` | None |
| `DefaultWorkspace` | Fallback workspace directory | None (explicit required in production) |
| `IdleTimeout` | Subprocess idle reaping timeout | Disabled |
| `StaleKillDelay` | Stale kill timer after prompt turn | Disabled |

### Command Construction

```
codex --dangerously-bypass-approvals-and-sandbox --search app-server [-c overrides...] --stdio [extra...]
```

The model is **not** passed via CLI flags — it is sent in the `turn/start` JSON-RPC params.

## Plugin Registration

Registered in `internal/standardplugins/standard_table.go` and `internal/standardplugins/backends_acp_cli.go` as backend ID `openai-codex-app-server` with model prefix `openai/`.

Default inventory models: `auto`, `gpt-5.4`, `gpt-5.3-codex`, `gpt-5.2`.

## Testing

- **Unit tests** (`connector_test.go`): `stripOpenAIModelPrefix`, `isAutoModel`, `buildCodexCommand`, `codexServerRequestHandler` (accept/permissions/decline), `acp.CheckExecutable` (exercised via `resolveExecutable`), `defaultInventoryModels`, `commandBasename`, `New()` factory
- **Stream unit tests** (`stream_internal_test.go`): `mapNotification` dispatch (text/reasoning deltas, `item/completed` summary, terminal, suppressed methods, nil params), `buildItemCompletionSummary`/`buildCommandSummary`/`buildFileChangeSummary` covering both the flat-params fallback and the real nested `params.item` wire shape, all routed through `acp.FormatToolCompletionSummary`
- **Lifecycle contract** (`lifecycle_contract_test.go`): static assertions that `codexStream` and `codexManagedStream` satisfy `leglifecycle.BLegAttempt`
- **Integration tests** (`integration_test.go`): Full lifecycle using `fakeProcess` + `fakeStarter` — handshake → thread/start → turn/start → stream notifications → turn/completed → close. Tests cover streaming text, reasoning deltas, transcript-based prompt (`TranscriptHistoryCoordinator`), `item/completed` fenced summaries (commandExecution + fileChange, nested-item shape, no raw stdout leak), approval request handling, pool release on close, model override, workspace resolution from extensions, and nested `thread/start` result shape.
