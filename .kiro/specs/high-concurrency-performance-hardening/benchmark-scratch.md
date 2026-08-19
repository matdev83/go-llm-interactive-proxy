# Performance Benchmark Scratchpad

> **Implementation evidence file.** This file intentionally starts as a template. Implementing agents MUST append measured baseline and post-change results here as tasks execute. Do not replace measurements with estimates or expected improvements.
>
> **Not measured = not proved.** A performance/concurrency optimization may be called successful only when an equivalent before/after experiment demonstrates the claimed effect. An optimization that is not measurably beneficial, is statistically inconclusive, or shifts cost to another critical metric must be reverted or explicitly retained only for a separately proven correctness/architectural reason.

## 1. Evidence Rules

1. Establish the reusable traffic/load harness and freeze named scenarios before optimizing production code.
2. For every production optimization attempt, create an entry in this file **before** making the production change. Record the hypothesis, affected path, baseline commit, scenario, host, configuration, and baseline measurements.
3. After the change, rerun the same scenario on the same class of host with the same Go version, GOMAXPROCS, process configuration, database mode, feature flags, payload shape, concurrency, ramp, stream cadence, duration, and profiler settings unless the variable being changed is itself the experiment. Record every intentional difference.
4. Keep failed, neutral, and reverted experiments. Do not delete evidence because a proposed optimization did not work.
5. Microbenchmarks must use repeated samples and a statistical comparison (prefer `benchstat` or an equivalent checked-in/reportable procedure). Do not compare single `go test -bench` numbers.
6. End-to-end/load scenarios must be repeated enough to expose variance. Record all repetitions or machine-readable result paths plus aggregate p50/p95/p99 where applicable.
7. For concurrency changes, capture mutex/block profiles when contention is part of the hypothesis. For allocation/memory changes, capture heap/alloc evidence. For CPU changes, capture CPU profiles. For scheduler/goroutine hypotheses, capture runtime metrics and Go trace when profiles are insufficient.
8. A throughput win that causes unacceptable latency, memory, error rate, durability, ordering, correctness, cleanup, or tail behavior is not a successful optimization.
9. Correctness-required fixes such as streaming timeout semantics still require before/after evidence demonstrating the failure and its removal, even when shipping the fix is not conditional on a throughput improvement.
10. Never use production provider APIs to establish scalability. Use deterministic local reference backends/fakes for proxy-capacity measurements; measure external database variants separately when persistence is the subject.
11. Raw prompts, credentials, tokens, or unbounded identifiers must not be written into this file, profiles, benchmark labels, logs, or metrics.
12. If the environment cannot run a requested scale tier, record the host/resource/OS limit that stopped the test and do not attribute that limit to the proxy without evidence.

## 2. Environment Record

Create one subsection per benchmark host/runtime combination and reference its ID from experiment entries.

### ENV-<id>

- Date/time:
- Commit / branch:
- OS / kernel / build:
- CPU model / logical CPUs:
- RAM / swap:
- Storage / filesystem:
- Go version:
- `GOMAXPROCS` / `GOMEMLIMIT` / relevant `GODEBUG`:
- Proxy build flags:
- Proxy configuration checksum / notable values:
- Database mode and version, if any:
- Database location/latency class, if any:
- Traffic generator host/process placement:
- Open-file/socket/backlog limits:
- Reverse proxy / TLS / HTTP version topology:
- Other competing workload:

## 3. Canonical Scenario Registry

Task 1 establishes stable scenario IDs here before production optimization begins. At minimum include:

- `HOLD-*` — long-held idle/low-rate streaming connections.
- `DELTA-*` — actively emitting streams with controlled event cadence and payload size.
- `START-*` — high request/session-start churn.
- `BODY-*` — concurrent maximum/near-maximum request-body materialization pressure, including chunked bodies.
- `OUTPUT-*` — long cumulative output / many-event streams for retained-memory analysis.
- `COMPLETE-*` — synchronized/compressed completion bursts for terminal persistence and billing spool pressure.
- `RETRY-*` — large prompts/tool schemas with controlled pre-output retry/failover counts.
- `OBSERVE-*` — traffic/audit disabled, enabled-fast, and deliberately slow-sink variants.
- `AUTHZ-*` — concurrency-authority same-dimension and many-dimension contention.
- `B2BUA-*` — independent A-leg, hot-A-leg, and expiration/churn variants.
- `DISCONNECT-*` — mass client disconnect/cancel and cleanup/reclamation.
- `SOAK-*` — steady-state long-duration run at the highest sustainable tier.

For each scenario record: protocol/client driver, reference backend script, concurrency, ramp, request bytes, TTFT, stream duration, event count, event size, event cadence, failure/cancel schedule, completion synchronization, secure-session mode, store mode, traffic observer mode, authority mode, and expected correctness assertions.

## 4. Baseline Matrix

Do not edit production performance behavior until the harness exists and at least the P0-relevant baseline matrix has been captured.

| Scenario | 1k | 5k | 10k | Store / Feature Mode | Evidence entry |
|---|---:|---:|---:|---|---|
| HOLD | pending | pending | pending | minimal | pending |
| DELTA | pending | pending | pending | secure-session memory | pending |
| DELTA | pending | pending | pending | secure-session durable | pending |
| START | pending | pending | pending | normal | pending |
| BODY | pending | pending | pending | normal | pending |
| OUTPUT | pending | pending | pending | normal | pending |
| COMPLETE | pending | pending | pending | durable billing spool | pending |
| DISCONNECT | pending | pending | pending | normal | pending |

Use `unsupported-by-host` rather than `fail` when the generator/OS/host cannot supply the requested tier. Record the limiting evidence.

## 5. Experiment Entry Template

Copy this section for **every** optimization attempt, including reverted attempts.

### EXP-<nnn> — <short title>

**Status:** planned | baseline-recorded | changed | proved-improvement | correctness-only | inconclusive | no-material-effect | regressed | reverted

**Task / requirements:**

**Hypothesis:**

**Why this is being attempted:**

**Production path under test:**

**Baseline commit:**

**Candidate commit:**

**Environment ID:**

**Scenario ID(s):**

**Controlled variables / intentional differences:**

#### Baseline measurements

- Repetition count:
- Throughput / completions:
- Request-start latency p50/p95/p99:
- TTFT p50/p95/p99:
- Client-facing event forwarding latency p50/p95/p99:
- Error/rejection/cancellation counts:
- CPU process/system:
- RSS / live heap / heap objects:
- Alloc bytes/op and allocs/op where applicable:
- GC CPU / pause / cycles:
- Goroutine count / stack memory:
- File descriptors / sockets:
- Mutex/block profile summary:
- DB query count/QPS/p50/p95/p99/pool wait, if applicable:
- Durable spool append latency/backlog, if applicable:
- Outbound connections/TLS handshakes/HTTP2 streams, if applicable:
- Queue depth/drop/coalesce/backpressure metrics, if applicable:
- Profile/result artifact paths:

#### Candidate measurements

Record the same fields as the baseline.

#### Statistical / causal comparison

- Tool/procedure:
- Primary metric delta and confidence:
- Tail-latency delta:
- Memory/allocation delta:
- Contention delta:
- Secondary regressions:
- Correctness/durability checks:

#### Conclusion

- **Measured conclusion:**
- **Decision:** keep | revise-and-rerun | revert | no production change required
- **Claim allowed by evidence:**
- **Remaining limiter / next experiment:**

## 6. Final Certification Record

Complete only after all implementation tasks and the final unchanged scenario rerun.

- Certification commit:
- Environment IDs:
- Highest successfully held streaming tier:
- Highest successfully active-delta tier:
- 1k result:
- 5k result:
- 10k result:
- Multi-hour soak duration and result:
- Secure-session memory-store result:
- Secure-session durable-store result:
- Traffic observation result:
- Concurrency-authority result:
- Billing completion-burst result:
- Long-stream (> legacy 120 s) result:
- Body-flood admission result:
- Disconnect cleanup result:
- Monotonic heap/session/history growth observed? yes/no + evidence:
- Dominant remaining CPU profile nodes:
- Dominant remaining mutex/block profile nodes:
- Dominant remaining DB operations:
- Proxy-derived capacity limiter:
- Host/OS/external-service limiter:
- Findings intentionally left unchanged because benchmarks showed no material impact:
- Overall certification verdict and exact scope of that verdict:
