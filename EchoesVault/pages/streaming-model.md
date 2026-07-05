---
type: architecture
title: Streaming Model
description: Streaming-first canonical event model, non-streaming collection, and stream constraints.
stack: [go]
tags: [streaming, lipapi, events]
status: active
---

# Streaming Model

## Streaming-First

Streaming is the default execution path for frontend and backend integrations that support it. Non-streaming behavior is collection over the same canonical event stream - never a second execution path.

## Canonical Event Stream

All protocols translate through one canonical event stream (`lipapi.EventStream`). Events are typed and ordered: content deltas, tool calls, errors, completion signals.

## Streaming Conventions

- Every external call receives `context.Context` (first parameter)
- No storing contexts in structs
- Deterministic event ordering
- Keepalive and flush behavior centralized in stream components
- No retry after first client-visible content event
- No per-request handler goroutines without explicit allowlisting
- Background work must have explicit owner, cancellation, and shutdown paths

## Stream Components

| Package | Responsibility |
|---|---|
| `internal/core/stream/` | Stream pumps, collectors, event plumbing |
| `internal/core/streamrecovery/` | Stream recovery after interruptions |
| `pkg/lipapi/events.go` | Canonical event types and stream types |
| `pkg/lipapi/events.go` | Non-streaming collection helpers over events |

## Non-Streaming Collection

`lipapi.Collect` iterates a canonical event stream and produces a complete result. Used by frontends and backends that don't support native streaming, collecting the same stream path.

## Key Constraints

- Preserve protocol-specific streaming framing rules at frontend boundary
- Do not buffer entire backend response solely for encoder convenience
- Respect request cancellation and client disconnects
- Emit only protocol-legal keepalive frames during pre-output recovery windows
- TTFT budgets satisfied only by client-visible canonical output, not keepalive/warning/usage events
