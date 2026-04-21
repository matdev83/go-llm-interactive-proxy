# Testing and determinism

This note classifies **intentional entropy** versus **stable translation** behavior in the Go proxy, for reliable goldens and CI.

## Intentional non-determinism (production)

| Mechanism | Location | Purpose |
|-----------|----------|---------|
| `crypto/rand` | `internal/core/diag/trace.go` (`NewTraceID`) | Opaque trace IDs |
| `crypto/rand` | `internal/core/b2bua/store.go` (`newID`) | A-leg / B-leg identifiers |
| `crypto/rand` | `internal/plugins/backends/acp/prompt_msg.go` (`newMessageID`) | Message IDs when call extensions omit a stable id |
| `time.Now()` | `internal/core/runtime/executor.go` | Wall clock when `Executor.Now` is nil |
| `time.Now()` | Frontend `encode.go` (Anthropic, OpenAI Responses, legacy) | Default response/message IDs and `created` timestamps when `EncodeOptions` fields are empty |
| Polling | `internal/refbackend/acp/server.go` | Emulator wait loops (not used for ordering guarantees) |

## Concurrency / `select` (deterministic behavior)

| Mechanism | Location | Notes |
|-----------|----------|--------|
| Keepalive vs inner result | `internal/core/stream/keepalive.go` | When the keepalive timer and a buffered inner `Recv` result are both ready, `Recv` drains the real event instead of emitting keepalive; the timer branch also prefers `ctx` cancellation when it races with the timer. |
| Test `delayedStream` | `internal/core/stream/keepalive_test.go` | After the timer fires, re-checks `ctx` so `context.Canceled` wins over `io.EOF`/events when `ctx` and a zero-delay timer are both ready. |

Tests that assert on IDs or timestamps must **inject** values: set `lipapi.Call.ID`, pass `EncodeOptions` (`ResponseID`, `MessageID`, `CreatedAt`, etc.), set `X-Trace-ID`, or configure `Executor.Now` / `b2bua.MemoryStoreOptions.Now` as needed.

## Deterministic by design

| Mechanism | Notes |
|-----------|--------|
| `math/rand` defaults | `Executor` uses `rand.NewSource(1)` when `Rand` is nil; `pickWeighted` uses `rand.NewSource(0)` when `opt.Rand` is nil — stable for a given input. |
| Test harness | `internal/testkit/executor_stub.go` and conformance harness use fixed seeds (`42`). |
| Hook chains | `internal/core/hooks/bus.go` sorts hooks by `Order`, then `ID`, then **registration index** so equal `(Order, ID)` pairs stay in config order (`slices.SortFunc` is not stable for ties). |
| JSON `encoding/json` | Marshaling maps sorts keys; not a source of key-order flakiness. |
| OpenAI legacy tool finish order | `internal/plugins/backends/openailegacy/map_events.go` records **first-seen tool index order** in `activeToolOrder` and emits `EventToolCallFinished` in that order (never `range` over `activeTools`). |

## Re-audit commands

From repo root:

```sh
rg "time\.Now\(" --glob "*.go" internal pkg cmd
rg "crypto/rand" --glob "*.go"
rg "math/rand" --glob "*.go" internal pkg
```

For ordered semantics, avoid emitting events by iterating Go maps; prefer slices, or sorted keys with a documented policy.
