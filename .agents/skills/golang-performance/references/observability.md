# Production Observability for Performance

Third-party monitoring tools complement local profiling (pprof, benchmarks) by providing continuous monitoring, historical trends, and regression detection in production.

## Prometheus Metrics for Go

**Setup:** `github.com/prometheus/client_golang` — expose `/metrics` endpoint with `promhttp.Handler()`. Default collectors automatically export Go runtime metrics (`go_goroutines`, `go_memstats_*`, `go_gc_duration_seconds`, `process_cpu_seconds_total`, etc.).

See the local `golang-benchmark` skill's investigation session for runtime metrics, capture design, and profiling cost controls. Environment variables affect a running process only when the application implements reload.

### PromQL Queries for Performance Diagnosis

#### GC pressure

| PromQL | What to look for |
| --- | --- |
| `rate(go_gc_duration_seconds_count[5m])` | GC cycles/s; compare with a workload-specific baseline and allocation rate |
| `rate(go_gc_duration_seconds_sum[5m]) / rate(go_gc_duration_seconds_count[5m])` | Average GC pause — increasing trend means heap is growing or has too many pointers |
| `go_gc_duration_seconds{quantile="1"}` | Worst-case GC pause — spikes here cause tail latency |

#### Memory growth investigation

| PromQL | What to look for |
| --- | --- |
| `go_memstats_alloc_bytes` | Cumulative allocation; growth is expected and is not a live-memory/leak proof |
| `rate(go_memstats_alloc_bytes_total[5m])` | Allocation rate (bytes/s) — drives GC frequency; compare before/after deploy for regressions |
| `process_resident_memory_bytes - go_memstats_sys_bytes` | Rough non-Go/resident-memory gap; growth is an investigation signal |

#### Goroutine growth investigation

| PromQL | What to look for |
| --- | --- |
| `go_goroutines` | Compare with load and lifecycle; sustained independent growth is a leak suspect |
| `delta(go_goroutines[1h])` | Net change over 1h; combine with stack snapshots and controlled load before concluding |

#### CPU saturation

| PromQL | What to look for |
| --- | --- |
| `rate(process_cpu_seconds_total[5m])` | CPU cores consumed; compare to GOMAXPROCS to detect saturation |
| `rate(process_cpu_seconds_total[5m]) / <GOMAXPROCS>` | CPU utilization ratio; >0.8 sustained = CPU-saturated |

#### Regression detection (after deploy)

| PromQL | What to look for |
| --- | --- |
| `rate(go_memstats_alloc_bytes_total[5m])` | Compare before/after deploy; significant increase = new allocation pattern introduced |
| `histogram_quantile(0.99, rate(http_request_duration_seconds_bucket[5m]))` | p99 latency increase after deploy = regression (requires app-level histogram) |

### Alerting rules (examples)

[Example alerting rules](../assets/prometheus-alerts.yml) — adjust thresholds to your application; a high-throughput data pipeline will have different baselines than a lightweight API server.

Run these PromQL expressions in the Prometheus UI or a repository-approved PromQL client.

### Grafana Dashboards

See the local `golang-observability` skill for dashboard and alert design; validate dashboard labels against the metrics actually exported by the service.

## Continuous Profiling

Continuous profiling collects low-overhead samples in production and stores them for historical comparison. Use it to detect regressions across deploys, compare flamegraphs over time, and feed PGO (see [Runtime Tuning](./runtime.md#profile-guided-optimization-pgo)).

| Tool | Model | Overhead | Best for |
| --- | --- | --- | --- |
| **Grafana Pyroscope** | push SDK or pull (via Alloy) | Measure in the target workload | Grafana ecosystem, historical flamegraph comparison |
| **Parca** (Polar Signals) | eBPF-based pull | Measure in the target workload | Infrastructure-wide profiling, no code changes |
| **Datadog Continuous Profiler** | push client | Measure in the target workload | Existing Datadog users |
| **Google Cloud Profiler** | push client | Measure in the target workload | GCP-hosted Go services |

### Pyroscope push mode

```go
import "github.com/grafana/pyroscope-go"

pyroscope.Start(pyroscope.Config{
    ApplicationName: "myapp",
    ServerAddress:   "http://pyroscope:4040",
    ProfileTypes: []pyroscope.ProfileType{
        pyroscope.ProfileCPU,
        pyroscope.ProfileAllocObjects,
        pyroscope.ProfileAllocSpace,
        pyroscope.ProfileInuseObjects,
        pyroscope.ProfileInuseSpace,
        pyroscope.ProfileGoroutines,
    },
})
```

### Pyroscope pull mode (via Grafana Alloy)

No code changes required — Alloy scrapes `/debug/pprof/*` endpoints periodically. Configure Alloy to target your service's pprof endpoint.

When using third-party profiling libraries, refer to the library's official documentation for current API signatures.

## Real-Time Visualization (Development)

| Tool | What it does |
| --- | --- |
| **statsviz** (`github.com/arl/statsviz`) | Real-time browser dashboard at `/debug/statsviz` — heap, GC pauses, goroutines, scheduler. Register with `statsviz.Register(mux)`. Great for local development |
| **expvar** (stdlib `expvar`) | JSON metrics at `/debug/vars` — lightweight, no dependencies. Integrates with Netdata, Telegraf, or custom dashboards |
