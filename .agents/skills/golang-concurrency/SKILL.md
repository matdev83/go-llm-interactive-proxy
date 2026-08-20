---
name: golang-concurrency
description: "Design and review concurrent Go code involving goroutines, channels, select, mutexes, atomics, worker pools, errgroup, singleflight, cancellation, and fan-out/fan-in. Use when preventing races, deadlocks, leaks, lost errors, or unbounded work."
---

# Go concurrency

Make concurrency structured: every goroutine has an owner, an exit condition, a bounded resource budget, and a defined error path. Concurrency is not automatically faster; establish the sequential baseline and measure contention or throughput before optimizing.

## First questions

1. What state is shared, and who owns mutation?
2. Who starts and stops each goroutine? What happens when the caller is canceled or a sibling fails?
3. Can sends, receives, semaphore acquisition, and result delivery block forever?
4. Which error wins, and how are sibling errors and cleanup errors reported?
5. Are buffers, queues, retries, and fan-out bounded?

Prefer a mutex for shared state with short critical sections. Prefer a channel when the operation naturally transfers work or ownership. Use typed `sync/atomic` values for small independent counters/flags. Concurrent read-only map access is safe; any concurrent mutation requires synchronization. `sync.Map` is a specialized choice, not a default replacement for a typed map plus mutex.

## Ownership rules

- The sender normally closes a channel, and only when it owns the decision that no more values can be sent. A receiver should not close a channel it does not own.
- `chan<-` and `<-chan` in function signatures make direction explicit.
- Sending a pointer is valid when the pointed-to object is immutable or ownership is transferred. Copies are not universally safer; document the ownership rule.
- A `select` needs a cancellation or shutdown case only when the operation can otherwise outlive its owner. Do not add a `ctx.Done()` case that silently abandons required work.
- Every acquired resource (semaphore slot, lock, buffer, connection) has a release path on success, error, and cancellation.

## Choosing primitives

| Need | Default | Watch for |
| --- | --- | --- |
| Wait for work with no error result | `sync.WaitGroup` | `Go`/`Add` must not race with `Wait`; workers must always call `Done` |
| Collect errors and cancel siblings | `errgroup.WithContext` | Return the group error; apply `SetLimit` for bounded concurrency |
| Deduplicate an expensive concurrent call | `singleflight.Group` | Decide whether failures are cached or retried |
| Protect fields/invariants | `sync.Mutex` or `sync.RWMutex` | Never hold a lock across slow or re-entrant calls without a reason |
| One-time result, including an error | `sync.OnceValues` or explicit state | Cache failure intentionally; do not hide initialization errors |
| Temporary reusable buffers | `sync.Pool` | Reset before `Put`; it is a performance hint, not storage |

`sync/atomic` promises atomic operations and ordering semantics; do not infer a particular lock-free implementation from the API. Typed atomics are easier to review than untyped operations.

## Cancellation and timers

Pass context from the owning request or service. A worker that may block on input, output, a semaphore, or a timer should select on cancellation where abandoning the operation is valid. Use `time.NewTimer` and stop/drain/reset it in long-running loops to reduce allocation churn; `time.After` is still correct for one-shot waits and is not a universal memory leak.

For background work that must outlive a request, submit to a bounded queue owned by a service with a shutdown context and wait for completion. `context.WithoutCancel` preserves values but removes cancellation; never use it as a complete lifecycle plan.

## Verification

Write tests for cancellation, full/closed channels, first-error behavior, queue limits, and shutdown. Run focused tests and `go test -race` on a supported platform. For leak-sensitive code, use explicit shutdown plus a bounded wait; a goroutine-count check is evidence, not a proof. See [channel patterns](references/channels-and-select.md), [pipelines](references/pipelines.md), and [synchronization](references/sync-primitives.md).
