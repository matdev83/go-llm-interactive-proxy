# Performance checks

## Local benchmark smoke

```bash
make bench
```

This runs packages under `internal/testkit`, `internal/core/stream`, `internal/core/runtime`, `internal/core/routing`, `internal/core/diag`, `internal/core/toolcallrepair`, and streaming encoders. For a single package, e.g. `go test -bench=. -benchmem -run=Benchmark ./internal/core/runtime/...`.
Secure-session recorder smoke: `go test -bench=BenchmarkRecorder -benchmem -run=^$ ./internal/core/securesession/app` (also included in `make bench`).
Tool-call repair smoke: `go test -bench='BenchmarkEngineRepair|BenchmarkSafeTailRepair|BenchmarkOrderedParse|BenchmarkOrderedPreflightPlusParse' -benchmem -run=^$ ./internal/core/toolcallrepair` (also included in `make bench`). `BenchmarkSafeTailRepair` separates valid pass-through, V1 append-only completion, terminal-comma deletion, deterministic pending `const`/`default`, and near-limit refusal. Latency is for `benchstat` comparison only — the default unit suite does not assert wall-clock thresholds (allocation budgets and semantic bounds stay in unit tests).

### JSON shape profiles (production)

| Profile | Max bytes | Max depth | Duplicate names | Notes |
|---|---:|---:|---|---|
| Request envelope (`jsonguard` / `RequestEnvelopeLimits`) | **8 MiB** default (configurable; keep generous vs tool limits) | **128** | accepted (last-wins compatible) | Overall HTTP body cap; independent of tool schema/args |
| Tool schema (`ToolSchemaLimits`) | **256 KiB** | **32** | rejected | Offline compile path |
| Tool args (`ToolArgumentsLimits`) | **64 KiB** default | **64** | rejected | Engine / repair materialize path |

Preflight runs before materialize on frontend ServeHTTP (`reqbody` → `jsonguard.Preflight` → Decode*) and on schema/args/repair candidates in `toolcallrepair` (archtest order gates). Safe-tail candidates remain private until this preflight succeeds; terminal-comma repair deletes exactly one final comma after a complete value, while pending-value repair appends only an exact root-property `const`, one-element `enum`, or `default` selected from the compiled schema.

### Ordered args parser retain decision (Step 5)

After preflight guarantees args depth ≤ **64** (schema ≤ **32**), the recursive `parseOrderedJSON` / `decodeOrderedValue` builder is **retained**. An iterative token-stack builder is deferred until profiling shows a material need. No custom grammar scanner.

Windows local evidence only (`go test -bench='BenchmarkOrderedParse|BenchmarkOrderedPreflightPlusParse|BenchmarkEngineRepair' -benchmem -benchtime=10x -run=^$ ./internal/core/toolcallrepair`, AMD Ryzen 7 5800X). Numbers are representative snapshots — **not** Linux CI baselines and **not** claims of linear scaling:

| Case | Parse alone | Preflight+parse | Notes |
|---|---:|---:|---|
| depth 64 nested array | ~7.8 µs, 144 allocs | ~10.6 µs, 163 allocs | Exact policy max; no stack issue |
| wide object 1024 keys | ~762 µs | ~1.5 ms | Near `MaxObjectKeys` |
| wide array 4096 elems | ~1.2 ms | ~4.1 ms | Near `MaxArrayElems`; preflight dominates |
| mixed ~60 KiB args | ~622 µs, 7.2k allocs | ~1.2 ms | Full `Engine.Repair` on same payload ~2.0 ms |
| tiny valid Engine hot path | — | — | `BenchmarkEngineRepair/valid_cached` ~6.8 µs |

Recursive parse cost at bounded depth is modest versus preflight and full repair; stack safety is enforced by rejecting depth 65 in preflight before materialize (`TestOrderedParse_depth64SucceedsAfterPreflight` / `TestOrderedParse_depth65RejectedBeforeParser`).

## Comparing before/after (benchstat)

```bash
go test -bench=BenchmarkExecutorExecuteAndDrain32Deltas -benchmem -count=6 -run=^$ ./internal/core/runtime/... | tee /tmp/bench-old.txt
# change code
go test -bench=BenchmarkExecutorExecuteAndDrain32Deltas -benchmem -count=6 -run=^$ ./internal/core/runtime/... | tee /tmp/bench-new.txt
benchstat /tmp/bench-old.txt /tmp/bench-new.txt
```

## Profiling the streaming path (alloc space)

Example: allocation profile from the runtime benchmark (replace the mem profile path as needed):

```bash
go test -memprofile=mem.prof -bench=BenchmarkExecutorExecuteAndDrain32Deltas -benchtime=500ms -run=^$ ./internal/core/runtime/...
go tool pprof -top -nodecount=30 mem.prof
```

`retryRecvStream.Recv` uses `diag.EnsureCallDiag`, which allocates a child context only when the trace / A-leg on `ctx` differs from the active attempt. Bundled HTTP frontends call `diag.EnsureCallDiag(ctx, call.ID, call.Session.ALegID)` immediately after a successful `Executor.Execute` (`prepareSubmitAndALeg` sets `call.Session.ALegID`). Custom callers should do the same before draining `EventStream` from the HTTP request context if they want this fast path.

### Baseline profiling note (executor benchmark)

Heap profiles of `BenchmarkExecutorExecuteAndDrain32Deltas` showed `diag.WithCallDiag` and `context.WithValue` among top allocators when `Recv` wrapped an empty request context each time. Combining the frontend `EnsureCallDiag` attach with resolver-populated `call.Session.ALegID`, the same benchmark drops to **~69 allocs/op** for 32 deltas (was **~145** without the attach).

## CI

The GitHub Actions workflow at `.github/workflows/benchmarks.yml` runs `make bench` on a schedule and on `workflow_dispatch`, and stores the text log as a build artifact (for optional `benchstat` against a saved baseline on a developer machine).
