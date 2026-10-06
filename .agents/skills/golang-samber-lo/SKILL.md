---
name: golang-samber-lo
description: "Use and review samber/lo transforms, aliasing, callback semantics, mutable/parallel variants, channels, iterators, and retry utilities."
---

# samber/lo

This guidance targets github.com/samber/lo v1.53. The module is not dependency-free; its go.mod includes golang.org/x/text. Check the target version and package path before using an API.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Check exact callback signatures, eager argument evaluation, error-returning variants, nil/empty behavior, and order guarantees in the pinned package.
- Trace element aliases and callback mutation through eager, mutable, lazy, and parallel variants. A new outer slice does not deep-copy its elements.
- Bound parallel fan-out and channel consumption; inspect library-owned channel closure and blocked producers when consumers stop early.
- Check retries for fixed versus adaptive delay, cancellation and idempotency; compare a plain loop only when it removes a demonstrated correctness or readability problem.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Choosing the package

The core package is eager and operates on finite slices/maps. Use Map, Filter, Reduce, GroupBy, Chunk, Uniq, and related helpers when their callback and allocation behavior improve clarity. Mutable and parallel subpackages have different trade-offs. Parallel helpers are not a magic worker pool: lo/parallel can launch one goroutine per element, so it may be unsuitable for unbounded or expensive inputs. Use an explicit bounded worker pool when concurrency must be limited.

The it subpackage adapts Go iterators; verify the Go version and iterator API before using it. Avoid experimental package paths in a stable library contract unless the project accepts their compatibility risk.

## Semantics

Most transforms allocate a new result and preserve input values, but callback side effects, referenced maps/slices, and iteration order still matter. Map iteration order is not deterministic. Pre-size or use a loop when allocation, early cancellation, or detailed error handling matters more than brevity.

Use Attempt for immediate retries. AttemptWithDelay adds a fixed delay; it is not exponential backoff. Use an explicit context-aware loop for adaptive backoff; Attempt alone does not sleep. lo retry helpers do not carry a context, so cancellation and operation-specific idempotency may require your own loop.

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

Use the exact DispatchingStrategy type and constructor from the pinned release; do not use old names such as a generic broadcast strategy unless the package exposes them. SliceToChannel and ChannelDispatcher create and close their output channels. Consumers must not close library-owned outputs; callers own input closure and must drain outputs or choose a cancellation-aware alternative to avoid blocked producers. A dispatcher can distribute work but does not provide cancellation, retries, or bounded downstream processing.

## Review checklist

Check nil versus empty results where wire semantics matter, aliases of values that contain references, map-order assumptions, goroutine count, retry delay, error propagation, and whether a straightforward loop is clearer. Benchmark before replacing a measured hot path with parallel or mutable variants. Keep lo out of domain policy when a standard loop communicates the invariant better.
