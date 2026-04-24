# Performance checks

## Local benchmark smoke

```bash
make bench
```

This runs packages under `internal/testkit`, `internal/core/stream`, `internal/core/runtime`, `internal/core/routing`, `internal/core/diag`, and streaming encoders. For a single package, e.g. `go test -bench=. -benchmem -run=Benchmark ./internal/core/runtime/...`.
Secure-session recorder smoke: `go test -bench=BenchmarkRecorder -benchmem -run=^$ ./internal/core/securesession/app` (also included in `make bench`).

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
