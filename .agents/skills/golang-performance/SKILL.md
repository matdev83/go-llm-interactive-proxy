---
name: golang-performance
description: "Go performance optimization after measurement: allocations, CPU, memory, I/O, GC, caching, and workload-aware trade-offs."
---

# Go performance optimization

Use this skill after a benchmark, profile, trace, or production metric identifies a meaningful bottleneck. First define the workload and success metric, then change one important factor at a time and compare against a representative baseline. Compiler behavior, hardware, Go release, and workload shape all matter; numeric thresholds in examples are hypotheses, not Go rules.

## Decision loop

1. Reproduce the problem with a benchmark or controlled production sample.
2. Identify whether time is CPU, allocation/GC, lock contention, I/O, scheduler wait, or an external dependency.
3. Estimate the user-visible and operational trade-off (memory, complexity, freshness, correctness, tail latency).
4. Make the smallest change that tests the hypothesis.
5. Run correctness, race, and performance checks; keep or revert based on the measured workload.

Do not optimize a runtime symbol just because it is high in a flat profile. Use cumulative callers and source annotations to find the application work that caused it. See `golang-benchmark` for measurement and `golang-observability` for safe production signals.

## Allocations and memory

Reduce allocations only when allocation rate or retained data matters. Reuse buffers when ownership is clear, pre-size slices/maps when a useful size estimate exists, and avoid retaining a large backing array for a small live result. `append` is typically amortized linear because capacity grows geometrically, but growth is implementation detail; measure the actual pattern. Do not use `unsafe` or a pool to hide an ownership bug.

`sync.Pool` is a GC-managed temporary reuse mechanism, not a cache and not a guarantee that an item remains available. Pool only resettable, interchangeable objects and measure contention and memory effects. A cache needs explicit bounds, eviction, expiry, and concurrency semantics.

Struct layout and pointer density can affect memory and GC scanning. Inspect with `go tool compile -m`, `go vet`/layout tools, pprof, and benchmarks; there is no universal “large enough to use a pointer” or “pool above N bytes” threshold.

## CPU and compiler

Use CPU profiles and `-gcflags=-m=2` to find hot code, escapes, and inlining decisions. Value versus pointer receivers are trade-offs among copying, method sets, mutation, escape behavior, and interface use; neither guarantees nor prevents inlining. Keep functions clear unless a measured hot path justifies a change. Avoid reflection and repeated parsing in hot loops when a typed/precomputed representation is safe, but verify that complexity and maintenance cost are worth it.

PGO profiles must represent the deployed workload and be validated against a separate run. Do not promise a fixed percentage gain or generate a generic profile and apply it everywhere. Keep the Go toolchain and flags consistent when comparing.

## I/O and concurrency

Profile queueing and wait time before increasing goroutines. Bound worker counts, request bodies, queues, retries, and connection pools; match them to downstream limits. Reuse HTTP transports and database handles according to their documented lifecycle. Batch work only when it preserves latency and failure semantics. Use deadlines and cancellation, but do not abandon required cleanup.

For lock contention, inspect mutex/block profiles and trace timelines. Shard state or reduce critical sections only after identifying the shared invariant. `sync.Map`, atomics, channels, and lock-free designs each have workload and correctness costs.

## Caching and algorithms

Choose data structures and algorithms from measured access patterns. A map lookup, sorted slice, index, compiled regexp, or memoization may help—or may cost more than the saved work. Bound caches and account for stale data, invalidation, stampedes, tenant isolation, and memory pressure. Use singleflight-style suppression for duplicate concurrent loads only when cancellation and error-sharing semantics are acceptable.

## Runtime and production

Use `GOMEMLIMIT`, `GOGC`, `GOMAXPROCS`, and runtime metrics as workload controls, not magic tuning knobs. Change one setting, observe CPU/heap/latency/error budgets, and retain a rollback path. Keep pprof, trace, and continuous profiling endpoints protected and limit capture overhead/data retention.

References: [memory](references/memory.md), [CPU](references/cpu.md), [I/O and networking](references/io-networking.md), [runtime](references/runtime.md), [caching](references/caching.md), and [observability](references/observability.md).

Related local skills: `golang-benchmark`, `golang-observability`, `golang-concurrency`, `golang-database`, `golang-safety`, and `golang-testing`.
