---
name: golang-performance-diagnostics
description: Profile, optimize, observe, and troubleshoot Go applications: pprof analysis, execution tracing, memory/CPU optimization, structured logging (slog), metrics, and diagnosing leaks or deadlocks.
---

# Go Performance & Diagnostics Guide

Performance optimization must be driven by measurement, profiling, and observability rather than premature guesswork. This guide covers diagnosing production anomalies, profiling bottlenecks, and applying targeted, verified optimizations.

---

## 1. Profiling Workflows (`pprof` & `trace`)

### Capturing Profiles
Enable `net/http/pprof` endpoints on an authenticated, internal debug listener:

~~~go
import _ "net/http/pprof"

go func() {
    // Isolated internal listener for diagnostics
    _ = http.ListenAndServe("localhost:6060", nil)
}()
~~~

### Diagnostic Profile Types
| Profile | Endpoint / Command | Purpose |
| :--- | :--- | :--- |
| **CPU** | `/debug/pprof/profile?seconds=30` | Identifies hot CPU functions consuming computation cycles. |
| **Heap (Alloc)** | `/debug/pprof/heap` (`-alloc_space`) | Finds sources of garbage collection churn and short-lived allocations. |
| **Heap (Inuse)** | `/debug/pprof/heap` (`-inuse_space`) | Identifies resident memory consumers and potential memory leaks. |
| **Goroutine** | `/debug/pprof/goroutine?debug=2` | Full dump of all active goroutine stack traces (triage leaks/deadlocks). |
| **Block / Mutex** | `/debug/pprof/block` & `/mutex` | Measures lock contention and channel blocking latencies. |
| **Trace** | `/debug/pprof/trace?seconds=5` | Microsecond-level execution trace of scheduler, GC pauses, and syscalls. |

### Analyzing Profiles
```bash
# Interactive web UI for flamegraphs and top functions
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/heap

# Execution tracer UI
curl -o trace.out http://localhost:6060/debug/pprof/trace?seconds=5
go tool trace trace.out
```

---

## 2. Memory & CPU Optimization Patterns

### Preallocating Slices & Maps
Avoid repeated geometric reallocations in tight loops by providing capacity hints:
~~~go
// Preallocate known capacity
results := make([]Result, 0, len(inputs))
lookup := make(map[string]Item, len(inputs))
~~~

### Zero-Allocation String/Byte Conversions
In hot paths, use `strings.Builder` or `bytes.Buffer` rather than repeated `+` string concatenations:
~~~go
var builder strings.Builder
builder.Grow(estimatedSize)
for _, chunk := range chunks {
    builder.WriteString(chunk)
}
finalString := builder.String()
~~~

### Reducing Heap Escapes & GC Churn
- **Avoid Interface Boxing in Tight Loops**: Passing concrete structs to `any` / `interface{}` parameters (e.g., `fmt.Sprintf` or unparameterized loggers) forces heap allocations.
- **Buffer Reuse with `sync.Pool`**:
~~~go
var bufPool = sync.Pool{
    New: func() any {
        return new(bytes.Buffer)
    },
}

func ProcessPayload(data []byte) {
    buf := bufPool.Get().(*bytes.Buffer)
    buf.Reset()
    defer bufPool.Put(buf)

    buf.Write(data)
    // process buffer...
}
~~~

---

## 3. Production Observability

### Structured Logging with `log/slog`
Use `log/slog` for structured, high-performance logging:
~~~go
import "log/slog"

func HandleRequest(ctx context.Context, logger *slog.Logger, reqID string, duration time.Duration) {
    logger.InfoContext(ctx, "request completed",
        slog.String("request_id", reqID),
        slog.Duration("duration_ms", duration),
        slog.Int("status", http.StatusOK),
    )
}
~~~

### Production Metrics & OpenTelemetry
- Record operational metrics (request rates, error ratios, latency histograms).
- Instrument distributed spans across boundaries with OpenTelemetry tracing, attaching trace IDs to logs for correlated debugging.

---

## 4. Production Troubleshooting & Anomaly Triage

### Triage 1: Goroutine Leaks
- **Symptom**: Continuously rising goroutine counts and memory usage over time.
- **Diagnosis**: Capture `/debug/pprof/goroutine?debug=1`. Look for hundreds or thousands of goroutines blocked on the same function call or channel operation:
  - Blocked on unbuffered channel write with no receiver.
  - Blocked on HTTP request without a configured timeout on `http.Client`.
  - Blocked on database query without `context.WithTimeout`.

### Triage 2: Deadlocks & Lock Contention
- **Symptom**: CPU drops to 0%, but requests hang and time out.
- **Diagnosis**: Inspect goroutine dump for two goroutines waiting on mutexes held by each other, or a goroutine trying to acquire a non-reentrant lock it already holds.

### Triage 3: Memory Spikes & OOM Kills
- **Symptom**: Process killed by OS OOM killer.
- **Diagnosis**: Compare heap profile `-alloc_objects` vs `-inuse_space`.
  - If `alloc_objects` is massive $\rightarrow$ GC churn (preallocate buffers, use `sync.Pool`).
  - If `inuse_space` grows unbounded $\rightarrow$ retained references in global slices/maps or unclosed response bodies (`resp.Body.Close()`).
