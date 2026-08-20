---
name: golang-concurrency
description: Design, review, and debug concurrent Go code involving goroutines, channels, select, mutexes, atomics, errgroup, singleflight, context propagation, deadlines, and cancellation. Use to prevent races, deadlocks, leaks, lost errors, or unbounded background work.
---

# Go Concurrency & Context Guide

Concurrent Go programs coordinate goroutines through channels, synchronization primitives, and `context.Context`. Concurrency must be structured, deterministic, and bounded: every goroutine must have a clear lifecycle owner, termination trigger, and error path.

---

## 1. Goroutine Lifecycle & Ownership

### Principles of Ownership
- **The creator owns the lifecycle**: The function or struct that starts a goroutine must provide the mechanism to stop it and wait for its termination.
- **Never start a goroutine without a stop mechanism**: Every long-running or background goroutine must listen for context cancellation or a dedicated shutdown/quit channel.
- **Always handle panics at goroutine boundaries**: An unrecovered panic in any goroutine terminates the entire process.

### Structured Concurrency Pattern
~~~go
type WorkerPool struct {
    wg     sync.WaitGroup
    cancel context.CancelFunc
}

func NewWorkerPool(ctx context.Context, workers int, tasks <-chan Task) *WorkerPool {
    ctx, cancel := context.WithCancel(ctx)
    pool := &WorkerPool{cancel: cancel}

    for i := 0; i < workers; i++ {
        pool.wg.Add(1)
        go func() {
            defer pool.wg.Done()
            for {
                select {
                case <-ctx.Done():
                    return
                case task, ok := <-tasks:
                    if !ok {
                        return
                    }
                    task.Execute(ctx)
                }
            }
        }()
    }
    return pool
}

func (p *WorkerPool) Stop() {
    p.cancel()
    p.wg.Wait()
}
~~~

---

## 2. Context Propagation & Boundaries

`context.Context` carries cancellation signals, deadlines, and request-scoped metadata across API and process boundaries.

### Rules of Context
1. **Pass as First Parameter**: Pass `ctx context.Context` as the first parameter of any function performing I/O or cancellable work.
2. **Never Store in Structs**: Do not store context in struct fields; pass it explicitly down the call stack.
3. **Always Call CancelFunc**: Pair `context.WithCancel`, `WithTimeout`, or `WithDeadline` with `defer cancel()` to release runtime timer resources immediately upon function exit.
4. **Bounded Detached Work**: When detaching work from a request context (e.g., async logging or audit trails using `context.WithoutCancel`), **always** attach a fresh bounded timeout or pass it to a supervised worker pool. Never spawn unbounded detached background routines.

~~~go
// Safe detached background execution with supervision
func (s *Service) RecordAuditAsync(ctx context.Context, event AuditEvent) {
    detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
    s.bgWorkers.Submit(func() {
        defer cancel()
        if err := s.auditStore.Save(detachedCtx, event); err != nil {
            s.logger.Error("audit record failed", "err", err)
        }
    })
}
~~~

### Context Values
- Use `context.WithValue` strictly for **transitive request-scoped data** (trace IDs, request IDs, authenticated user tokens).
- Define private, unexported custom types for context keys to prevent cross-package collisions:
~~~go
type contextKey struct{}
var traceIDKey = contextKey{}

func WithTraceID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, traceIDKey, id)
}

func TraceIDFromContext(ctx context.Context) (string, bool) {
    id, ok := ctx.Value(traceIDKey).(string)
    return id, ok
}
~~~

---

## 3. Channels & Coordination

### Channel Ownership Rules
- **The sender closes the channel**: Never close a channel from the receiver side, and never close a channel with multiple concurrent senders.
- **Directional channel parameters**: Use `<-chan T` (receive-only) and `chan<- T` (send-only) in function signatures to enforce dataflow direction at compile time.
- **Unbuffered vs Buffered**:
  - Prefer **unbuffered channels** (`make(chan T)`) for synchronous handoffs and guarantee of delivery.
  - Use **buffered channels** (`make(chan T, n)`) only when throughput decoupling or batching is measured and necessary. Never use buffer size as a race fix.

### Fan-Out / Fan-In with Error Handling
~~~go
import "golang.org/x/sync/errgroup"

func ProcessItemsConcurrently(ctx context.Context, items []Item) error {
    g, ctx := errgroup.WithContext(ctx)
    g.SetLimit(10) // Bound concurrency

    for _, item := range items {
        item := item // pin variable for closure
        g.Go(func() error {
            if err := ctx.Err(); err != nil {
                return err
            }
            return processItem(ctx, item)
        })
    }

    return g.Wait() // Returns first non-nil error and cancels sibling group context
}
~~~

### Request Deduplication with Singleflight
Use `singleflight.Group` to collapse duplicate concurrent in-flight requests into a single execution (ideal for cache stampede prevention):
~~~go
import "golang.org/x/sync/singleflight"

type CachedLoader struct {
    group singleflight.Group
    cache Cache
}

func (l *CachedLoader) Get(ctx context.Context, key string) (Data, error) {
    if v, ok := l.cache.Get(key); ok {
        return v, nil
    }

    v, err, _ := l.group.Do(key, func() (any, error) {
        data, err := l.fetchFromOrigin(ctx, key)
        if err == nil {
            l.cache.Set(key, data)
        }
        return data, err
    })
    if err != nil {
        return Data{}, err
    }
    return v.(Data), nil
}
~~~

---

## 4. Synchronization Primitives

| Primitive | Use Case | Critical Rule |
| :--- | :--- | :--- |
| `sync.Mutex` | Exclusive read/write access to in-memory state. | Keep critical sections small; do not perform I/O under locks. |
| `sync.RWMutex` | Read-heavy state where reads vastly outnumber writes. | Avoid upgrading read locks to write locks (causes deadlocks). |
| `sync.WaitGroup` | Waiting for a known set of goroutines to complete. | `Add` before spawning; `Done` inside `defer` in the goroutine. |
| `sync.Once` | Idempotent one-time initialization. | Handle initialization errors with `sync.OnceValue` or `sync.OnceValues`. |
| `sync/atomic` | Simple counters, flags, or atomic pointer swaps. | Use typed atomic values (`atomic.Int64`, `atomic.Pointer[T]`). |
| `sync.Pool` | Reusing high-frequency temporary buffers to reduce GC churn. | Reset object state before putting back; do not store stateful clients. |

### Safe One-Time Initialization
~~~go
var (
    configOnce  sync.Once
    cachedCfg   *Config
    configErr   error
)

func GetConfig() (*Config, error) {
    configOnce.Do(func() {
        cachedCfg, configErr = loadConfigFromDisk()
    })
    return cachedCfg, configErr
}
~~~

---

## 5. Concurrency Anti-Patterns & Traps

1. **Goroutine Leaks via Blocked Channel Writes**:
   - *Trap*: Writing to an unbuffered channel when receiver has already returned due to error/context cancellation.
   - *Fix*: Select on `<-ctx.Done()` during send, or use a bounded buffered channel.
2. **Loop Variable Capture in Goroutines**:
   - In Go < 1.22, loop variables were per-loop, causing closures to share references. In Go >= 1.22, loop variables are per-iteration. Always pass variables explicitly if targeting older toolchains or readability.
3. **Holding Locks Across I/O**:
   - *Trap*: Calling network, disk, or long channel operations while holding a `sync.Mutex`.
   - *Fix*: Copy data under lock, release lock, then execute I/O.
4. **Copying Structs Containing Mutexes**:
   - *Trap*: Value receivers on structs with `sync.Mutex` or passing them by value copies the lock state (violating sync invariants).
   - *Fix*: Always use pointer receivers (`*T`) for types containing `sync.Mutex` or `sync.WaitGroup`.

---

## 6. Verification & Race Detection

Always test concurrent code under the race detector:
```bash
go test -race -count=10 ./path/to/package/...
```
- Avoid `time.Sleep` in tests to wait for goroutine completion. Use channels, `sync.WaitGroup`, or deterministic polling with timeouts.
