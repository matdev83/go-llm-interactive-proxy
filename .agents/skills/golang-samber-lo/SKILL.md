---
name: golang-samber-lo
description: Use samber/lo for generic slice and map transforms, tuple helpers, channels, iterators, parallel work, and bounded retry utilities.
---

# samber/lo

This guidance targets github.com/samber/lo v1.53. The module is not dependency-free; its go.mod includes golang.org/x/text. Check the target version and package path before using an API.

## Choosing the package

The core package is eager and operates on finite slices/maps. Use Map, Filter, Reduce, GroupBy, Chunk, Uniq, and related helpers when their callback and allocation behavior improve clarity. Mutable and parallel subpackages have different trade-offs. Parallel helpers are not a magic worker pool: lo/parallel can launch one goroutine per element, so it may be unsuitable for unbounded or expensive inputs. Use an explicit bounded worker pool when concurrency must be limited.

The it subpackage adapts Go iterators; verify the Go version and iterator API before using it. Avoid experimental package paths in a stable library contract unless the project accepts their compatibility risk.

## Semantics

Most transforms allocate a new result and preserve input values, but callback side effects, referenced maps/slices, and iteration order still matter. Map iteration order is not deterministic. Pre-size or use a loop when allocation, early cancellation, or detailed error handling matters more than brevity.

Use Attempt for immediate retries. Backoff belongs to AttemptWithDelay or an explicit context-aware loop; Attempt alone does not sleep. lo retry helpers do not carry a context, so cancellation and operation-specific idempotency may require your own loop.

## Channels

Current channel helpers include:

~~~go
out := lo.SliceToChannel(16, values)
values = lo.ChannelToSlice(out)

parts := lo.ChannelDispatcher(
    input,
    3,                    // number of child channels
    32,                   // per-channel buffer
    lo.DispatchingStrategyRoundRobin[int],
)
~~~

Use the exact DispatchingStrategy type and constructor from the pinned release; do not use old names such as a generic broadcast strategy unless the package exposes them. Channel ownership and closure remain the caller's responsibility. A dispatcher can distribute work but does not provide cancellation, retries, or bounded downstream processing.

## Review checklist

Check nil versus empty results where wire semantics matter, aliases of values that contain references, map-order assumptions, goroutine count, retry delay, error propagation, and whether a straightforward loop is clearer. Benchmark before replacing a measured hot path with parallel or mutable variants. Keep lo out of domain policy when a standard loop communicates the invariant better.
