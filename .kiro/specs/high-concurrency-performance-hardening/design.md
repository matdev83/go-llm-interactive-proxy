# Design Document

## Overview

This design turns #394 from a static audit into an evidence-driven scalability program. The implementation begins by creating a reusable deterministic load laboratory around the existing reference clients/backends, freezes canonical workloads, and records the current baseline. Production changes then proceed in descending impact order: streaming correctness/body admission, secure-session event-cadence persistence, per-stream retained memory, P1 shared stores/persistence, and finally P2 allocation/observer/lock/transport candidates. Every optimization crosses the same experiment gate and leaves its before/after evidence in `benchmark-scratch.md`.

The architecture deliberately does **not** replace Go's goroutine-per-stream model or introduce a new actor/worker framework. The audit found the dominant risks in synchronous shared side effects, lifetime-retained data, admission placement, and a handful of global critical sections. Fixing those at their owning boundaries is lower-risk and more aligned with the repository's streaming-first/canonical architecture.

Capacity is treated as a measured property of workload + feature mode + persistence topology + host. The final result may certify 1k/5k/10k tiers differently for idle-held versus actively emitting streams and may return an explicit 10k NO-GO with a measured limiter. The design never extrapolates an untested tier.

### Goals

- Build reusable deterministic traffic generation and profiling **before** optimizing production code.
- Remove every known P0/P1 scalability hazard from #394 and measure the result.
- Measure every P2 candidate and change it only when materiality is demonstrated.
- Bound live memory and shared critical sections by meaningful process/session/dimension limits rather than lifetime history or arrival count.
- Preserve secure-session durability, billing durability, auth/security, routing/failover, canonical protocol, and exactly-once terminal semantics.
- Leave a repeatable performance regression harness and an append-only evidence trail for future changes.

### Non-Goals

- Replacing `net/http`, Go's scheduler, or the goroutine-per-connection/request model without evidence.
- Designing a universal scheduler, worker pool, actor system, or asynchronous event bus.
- Changing LLM protocol semantics, routing selector semantics, backend ABI, or provider-specific behavior in core.
- Weakening mandatory secure-session persist-before-release semantics or billing spool durability.
- Creating a Cartesian frontend×backend performance matrix.
- Treating a single 10k number as portable across hardware, OS, persistence, observer, and workload modes.

## Boundary Commitments

### This Spec Owns

- Internal load/performance tooling and canonical scenario/result definitions.
- Benchmark evidence protocol in `benchmark-scratch.md`.
- Streaming timeout lifetime semantics.
- Frontend request-body materialization admission.
- Secure-session stream-recording persistence shape and in-memory contention/retention.
- Stream/output evidence boundedness and usage-accounting integration needed to remove lifetime event histories.
- Auth dispatcher contention, B2BUA memory-store contention/expiry, concurrency-authority lease-store contention, and terminal billing-spool redundant aggregate work.
- Evidence-gated optimization of cloning, traffic observation, residual shared locks, and transport defaults.
- Final capacity/soak certification.

### Out of Boundary

- Provider SDK implementations except where a provider-local counter/adapter must implement an existing or narrowly extended performance seam.
- Monetary business policy, pricing, rating, journal semantics, or moving billing into stream receive cadence.
- New public benchmark/config APIs absent an independent product need.
- Ongoing structural ownership refactors except for the private integration needed to keep performance state bounded.

### Allowed Dependencies

- `internal/refclient`, `internal/refbackend`, `internal/testkit` for deterministic traffic and protocol fixtures.
- Existing diagnostics/pprof, runtime metrics, Prometheus/OpenTelemetry, and database instrumentation.
- Existing frontend pipeline/decode QoS, secure-session app/store adapters, B2BUA/authority stores, billing spool, runtime/token accounting, and traffic SDK boundaries.
- Active Kiro ownership specs as chronological dependencies when their implementation lands first.

### Revalidation Triggers

- `retryRecvStream`/response-pipeline ownership changes.
- Secure-session store contract/schema changes.
- Authentication event-sink ordering/failure-policy changes.
- New database/pooler restrictions.
- Billing spool durability/cap changes.
- New frontend body-reader architecture.
- New HTTP/TLS termination topology in the standard host.

## Architecture

### Existing Architecture Analysis

The current architecture already contains several good scalability primitives: immutable generation pinning, atomic model-catalog publication, process-owned decode QoS, per-stream event synchronization, shared outbound transports, and mostly per-A-leg lifecycle ownership. Those become controls, not rewrite targets.

The problematic paths share four patterns:

1. **Work proportional to historical output** — secure-session sequence scans, retained event/output histories, terminal full-slice copies, lease-map scans.
2. **Unrelated work serialized by global ownership** — secure-session memory mutations, auth sink I/O, B2BUA memory activity updates, concurrency-authority lease scans.
3. **Expensive work before resource admission** — request-body `ReadAll`/preflight before decode admission.
4. **Policy expressed as a total-duration timeout instead of phase/progress policy** — inbound server write and outbound client total timeout.

The design attacks these properties at their current boundaries instead of moving the same complexity into new packages.

### Architecture Pattern & Boundary Map

```mermaid
flowchart LR
    G[Perf Load Driver\ninternal/testkit/perf] -->|real HTTP/SSE/WS client path| P[Go-LIP process]
    P -->|normal backend HTTP| U[Scripted Reference Upstream]
    P --> D[(SQLite/PostgreSQL\nwhen scenario requires)]

    G --> R[Machine-readable Run Result]
    P --> M[pprof/runtime metrics/DB counters]
    U --> R
    M --> R
    R --> E[benchmark-scratch.md\nexperiment conclusion]

    subgraph Production hardening
      P1[Streaming-safe timeout policy]
      P2[Pre-materialization admission]
      P3[Secure-session event mutation]
      P4[Bounded response/accounting evidence]
      P5[Per-key/per-dimension shared state]
      P6[Measured P2 optimizations]
    end
```

**Selected pattern**: evidence-first targeted hardening with narrow ownership changes.

**Existing patterns preserved**:
- canonical request/event contracts in `pkg/lipapi`;
- frontend/backend adapter boundaries;
- explicit composition roots;
- context-owned cancellation;
- no retry/failover after client-visible output;
- durable stores behind existing app/store seams.

**New components rationale**:
- `internal/testkit/perf` is necessary because existing conformance/reference clients exercise correctness but do not orchestrate thousands of controlled streams with stable measurement metadata.
- A small set of private accumulator/batch/store seams is introduced only where lifetime history or event-cadence calls are the root cause.
- No generic scheduler or global performance manager is introduced.

### Optional Hexagonal Lens

- **Domain/policy**: existing secure-session durability, authority limits, billing durability, usage reconciliation, and routing rules remain authoritative.
- **App orchestration**: secure-session recorder decides one logical stream-event recording mutation; runtime response pipeline feeds compact evidence; frontend pipeline orders auth → materialization admission → body read/preflight/decode.
- **Driving adapter**: frontend HTTP paths and the internal performance load driver.
- **Driven adapters**: secure-session memory/Bun stores, billing SQLite spool, traffic sinks, tokenizers, reference upstreams.
- **Composition root**: `runtimebundle`/standard host construct process-owned admission/HTTP/store dependencies; perf tool constructs local test topology.
- **Ports**: consumer-owned narrow batch/accumulator capability only where current per-operation interfaces force amplification.

### Priority / Dependency Waves

```mermaid
flowchart TD
    W0[Wave 0: Harness + frozen baseline] --> W1[Wave 1: Streaming timeout + body admission]
    W1 --> W2[Wave 2: Secure-session event persistence]
    W2 --> W3[Wave 3: Bounded per-stream evidence]
    W3 --> W4[Wave 4: P1 locks/lifecycle/spool]
    W4 --> W5[Wave 5: P2 clones/observers/residual locks/transport]
    W5 --> W6[Wave 6: 1k/5k/10k + soak certification]
```

Production waves are intentionally mostly sequential at the macro level because changing a dominant P0 can materially change the profile that justifies P1/P2 work. Independent **measurement** within a wave may run in parallel once its baseline commit is frozen.

## File Structure Plan

The exact production files follow current ownership and may shift if adjacent structural specs land first. The performance tooling structure is stable:

```text
internal/testkit/perf/
├── scenario.go             # stable scenario model and validation
├── result.go               # machine-readable run/environment/result model
├── runner.go               # concurrency/ramp/lifecycle orchestration
├── metrics.go              # client-visible latency/error/resource sampling hooks
├── clients.go              # adapters over existing refclient/wire-client helpers
├── upstream.go             # controller for scripted reference upstream behavior
├── assertions.go           # event/order/completion correctness assertions
├── scenarios/              # named reusable scenario constructors/fixtures
└── cmd/lipperf/
    └── main.go             # internal CLI: load/upstream/describe/report as needed

.kiro/specs/high-concurrency-performance-hardening/
├── spec.json
├── requirements.md
├── research.md
├── design.md
├── tasks.md
└── benchmark-scratch.md    # append-only implementation evidence
```

Likely production change surfaces, resolved against the implementation-time tree:

- `internal/stdhttp/` — inbound streaming-safe timeout semantics and server configuration.
- `internal/infra/httpclient/` — outbound streaming-safe client lifetime/transport tuning.
- `internal/plugins/frontends/frontendpipe/`, `reqbody/`, `decodeqos/` — staged materialization/decode admission.
- `internal/core/securesession/app/`, `adapters/memory/`, `adapters/bunstore/` — logical stream-event mutation, O(1) sequence ownership, per-session synchronization/retention, SQL round-trip reduction.
- `internal/infra/controlplane/observers/` — prevent best-effort projection amplification where applicable.
- `internal/core/runtime/` and/or the post-refactor response-pipeline owner — bounded customer/output evidence.
- `internal/core/tokenaccounting/streamusage/` and tokenizer/counter adapter seams — compact/incremental output evidence.
- `internal/core/auth/` — sink serialization scope.
- `internal/core/b2bua/` — per-A-leg/sharded memory ownership and bounded expiry work.
- `internal/infra/concurrencyauthority/leasestore/` — per-dimension live state/tombstone expiry.
- `internal/infra/billingspool/` — maintained pending totals/reconciliation and completion-burst instrumentation.
- `pkg/lipapi/call_clone.go` + runtime preparation — only if clone benchmarks justify changes.
- `pkg/lipsdk/traffic/` + traffic-emission call sites — no-consumer fast path / bounded delivery only if measured.
- executor config/RNG, `pkg/credpool`, `internal/core/state`, affinity/lifecycle — only candidates proven material by profiles.

## System Flows

### Flow A: Evidence Gate for Every Optimization

```mermaid
flowchart TD
    H[Hypothesis from audit/profile] --> B[Run frozen baseline scenario]
    B --> S[Append baseline to benchmark-scratch]
    S --> C[Implement smallest candidate change]
    C --> T[Run correctness/race/durability tests]
    T --> A[Repeat equivalent candidate benchmark]
    A --> X[Compare distributions/profiles/resources]
    X -->|measurable benefit or correctness requirement| K[Keep and record claim]
    X -->|inconclusive| I[Revise experiment or no claim]
    X -->|regression/no material optional benefit| V[Revert candidate]
    I --> S2[Record inconclusive result]
    V --> S3[Record reverted result]
```

No task may skip directly from hypothesis to “optimized.”

### Flow B: Frontend Request Admission

```mermaid
sequenceDiagram
    participant C as Client
    participant A as Auth
    participant Q as Process Admission
    participant B as Body Reader/Preflight
    participant D as Decoder
    participant X as Executor

    C->>A: request headers / auth material
    A-->>C: reject early if unauthorized
    A->>Q: acquire materialization/count slot
    alt no capacity
      Q-->>C: bounded QoS rejection/wait outcome
    else admitted
      Q->>B: ReadAllLimited / chunked limited read
      B->>Q: actual body byte weight
      Q->>D: reserve/refine decode byte budget
      D->>X: canonical Call
      X-->>C: response stream
      D->>Q: release decode bytes/count per lifetime rule
    end
```

**Key decision**: Authentication stays before body allocation. A process-owned count/materialization slot is acquired before full `ReadAll`; the existing max-body reader remains authoritative. The existing decode QoS should be evolved into staged reservation where practical so reload generations share one budget and operators do not have to reason about two independent limits.

### Flow C: Secure-Session Stream Event

```mermaid
sequenceDiagram
    participant RP as Response Pipeline
    participant R as SecureSession Recorder
    participant S as StreamEvent Store
    participant CP as Best-effort Projection
    participant C as Client

    RP->>R: record client-facing canonical event
    R->>S: AppendStreamEvent(mutation)
    Note over S: assign sequences O(1)/atomically;\noptional transcript + mandatory audit + usage/activity\nunder minimum required transaction scope
    S-->>R: assigned refs / durable acknowledgement
    opt best-effort projection enabled
      R->>CP: bounded projection after authoritative success
    end
    R-->>RP: success/failure
    RP-->>C: release event only when mandatory policy permits
```

#### Stream-event mutation contract

The current `Next*Seq` + append orchestration is replaced for the hot path by a consumer-owned operation conceptually equivalent to:

```go
// Package-private/internal shape; exact names follow existing contracts.
type StreamEventMutation struct {
    SessionID   string
    TurnID      string
    BLegID      string
    ActivityAt  time.Time
    Transcript  *TranscriptAppend
    Audit       AuditAppend
    Usage       *UsageAppend
}

type StreamEventMutationResult struct {
    TranscriptSeq int64
    AuditSeq      int64
}

type StreamEventStore interface {
    AppendStreamEvent(context.Context, StreamEventMutation) (StreamEventMutationResult, error)
}
```

This is not a new generic event store. It is a use-case-shaped operation that lets adapters remove avoidable round trips while preserving the recorder's existing atomic/durability policy.

#### Memory adapter target

- Top-level map synchronization covers lookup/insert/remove only.
- A session entry owns its own mutable transcript/audit/usage/activity state and lock.
- `nextTranscriptSeq` and `nextAuditSeq` are maintained as O(1) counters under the session entry lock.
- Session expiry/removal does not require scanning the entire store during every create/touch. The initial implementation should compare the smallest bounded-amortized cleanup approach against sharding/expiry-index alternatives and keep the simplest one that meets contention/retention evidence.
- Removing an entry must not race active users; use explicit reference/entry lifetime or deletion under shard ownership rather than returning mutable pointers after unprotected map removal.

#### Durable adapter target

Stage 1 is mandatory and simple: sequence allocation occurs inside the append transaction; the application does not issue a separate `Next*Seq` call. The existing parent-session lock/order mechanism may initially remain if it is the simplest correct dual-dialect implementation.

Stage 2 is benchmark-gated: if `MAX(seq)+1`/parent update remains dominant, compare a per-session persisted counter (`UPDATE ... RETURNING`) or another dual-dialect atomic sequence mechanism against the Stage-1 transaction. A schema/counter change ships only when repeated durable-store benchmarks justify its added migration complexity.

Activity update should be folded into the same authoritative mutation where possible. If per-event parent updates remain expensive and activity can be coalesced safely, use a bounded activity gate with terminal flush and explicit expiry tests rather than an unbounded cache.

#### Best-effort versus mandatory delivery

- Mandatory recorder semantics remain on the release path and require durable acknowledgement.
- Best-effort control-plane/observer projection may be batched asynchronously only through a bounded owner with explicit shutdown, capacity, backpressure/drop/coalesce semantics, and metrics.
- If remote durable acknowledgement remains the limiter at active-delta scale, the next experiment is a durability-preserving local acknowledgement mechanism, not silently best-effort delivery.

### Flow D: Bounded Response and Usage Evidence

Current terminal reconstruction structurally consumes full `OutputText` and full `Events`. The target separates **canonical usage evidence** from **full replay material**:

```mermaid
flowchart LR
    E[Released canonical event] --> U[UsageEvidenceAccumulator]
    E --> C[CustomerEvidenceAccumulator]
    E --> G[Bounded gate/recovery/tool state]
    U --> T[Terminal usage reconciliation]
    C --> O[Only semantically required terminal/observer view]
    G --> R[Recv/recovery mechanics]
```

#### `UsageEvidenceAccumulator`

The private accumulator owns:
- provider usage deltas/scopes needed for reconciliation;
- compact output-counting state;
- provider/tokenizer-specific streaming counter state only behind a counter capability, never by branching provider names in runtime;
- bounded fallback state if exact incremental counting is unavailable.

Conceptual capability:

```go
type StreamOutputCounter interface {
    Observe(context.Context, lipapi.Event) error
    Finish(context.Context) (app.CountResult, error)
}
```

The existing terminal `CountOutput(Text, Events)` remains a compatibility/fallback capability only until implementations can prove a compact exact path. Candidate strategies must be benchmarked and correctness-compared:

1. eliminate full event history first while retaining one output-text builder;
2. exact streaming/incremental tokenizer state where the tokenizer implementation can prove equality with terminal counting across arbitrary chunk boundaries;
3. bounded spill/chunk representation when exact terminal counting requires replay but RAM retention would violate the concurrency target.

A strategy must not approximate token counts more loosely than current policy unless the accounting contract already allows that authority/estimator class. Property/golden tests compare streaming versus existing terminal results on adversarial chunk boundaries, UTF-8, reasoning/tool events, and provider usage mixtures.

#### Customer/observer evidence

Only one component owns concatenated customer text/reasoning/tool data when a terminal consumer genuinely requires it. Event history is not duplicated merely for observers; observers receive events as they pass or a bounded snapshot according to existing API. Full content retention that remains necessary receives an explicit byte/event cap and failure policy.

#### Buffer inventory

Implementation must inventory and benchmark:
- completion gate buffer/drain;
- recovery drain;
- tool argument assembly/finalization;
- pending frontend/wire events;
- interleaved reasoning/memo state;
- final observer/traffic snapshots.

Each gets one owner and an explicit bound or a documented semantic maximum. This inventory is re-run after any adjacent ownership spec lands.

### Flow E: Per-Key Shared State

Three shared-memory hot paths use the same design principle but remain separate domains:

1. **B2BUA continuity** — top-level/shard map protects index membership; one A-leg entry protects mutable attempt/activity/interleaved/override state; expiry work is bounded/amortized.
2. **Concurrency authority** — one rule/dimension bucket owns current live count/leases and expiry; admission does not scan unrelated dimensions/history; tombstones are removed/reconciled.
3. **Secure sessions** — one session entry owns transcript/audit counters/history/activity; unrelated sessions do not share the mutation lock.

Do not create one generic sharded-store abstraction. The lifecycles/invariants are different and the repository prefers local concrete owners.

### Flow F: Billing Completion Spool

Keep one durable writer initially. Replace per-append `COUNT/SUM` with maintained pending metadata:

```mermaid
sequenceDiagram
    participant T as Terminal Work
    participant S as Billing Spool
    participant DB as SQLite

    T->>S: Append(record)
    S->>DB: BEGIN + replay lookup
    S->>DB: read/update maintained pending counters
    S->>DB: capacity/disk guards + INSERT
    S->>DB: COMMIT (FULL durability preserved)
    DB-->>S: durable ack
    S-->>T: success
```

On open/recovery, reconcile metadata against pending rows so counters are not trusted blindly after crash/version upgrade. Only if completion-burst evidence still shows the single writer is the limiter should batching/connection alternatives be benchmarked; acknowledgement/durability must remain equivalent.

## Components and Interfaces

| Component | Layer | Intent | Requirements | Critical dependencies |
|---|---|---|---|---|
| Perf Scenario Model | internal test support | Stable workload definition | 1, 2, 18 | refclients/refbackends |
| Perf Runner | internal test support | Concurrency/ramp/load orchestration | 1, 2 | HTTP clients, resource samplers |
| Perf Result Collector | internal test support | Machine-readable metrics/environment | 1, 2, 16, 18 | runtime/OS/db metrics |
| Streaming Timeout Policy | stdhttp/httpclient config | Remove absolute stream lifetime cutoff | 3 | context, idle/TTFT policies |
| Staged Decode Admission | frontend/process service | Bound body materialization + decode | 4 | decodeqos, reqbody |
| Secure Stream-Event Recorder | app/store seam | One logical authoritative mutation/event | 5, 6 | memory/Bun stores |
| Session Entry / durable append | driven adapters | O(1)/narrow-lock sequence + retention | 5 | store contract/db |
| Usage Evidence Accumulator | runtime/token accounting | Compact/bounded stream evidence | 7 | streamusage/counters |
| Auth Dispatcher | core auth | Remove sink I/O from global critical section | 8 | sink contracts |
| B2BUA Memory Store | core continuity | Per-A-leg independent contention | 9 | TTL/attempt invariants |
| Authority Lease Store | infra authority | Per-dimension admission state | 10 | rule/dimension semantics |
| Billing Spool Metadata | infra billing | Avoid aggregate scans per completion | 11 | SQLite durability/caps |
| Clone Optimization Gate | runtime/lipapi | Remove copies only if material | 12 | immutable call semantics |
| Traffic Fast/Bounded Path | SDK/runtime traffic | Pay-for-use observation | 13 | redaction/sinks |
| Residual Lock Review | owning packages | Evidence-based small lock fixes | 14 | race/correctness tests |
| Transport Tuning Gate | stdhttp/httpclient | Topology-specific connection tuning | 15 | net/http/OS |

### Performance Scenario Model

Conceptual internal type:

```go
type Scenario struct {
    ID            string
    Protocol      ProtocolDriver
    Connections   int
    Ramp          time.Duration
    RequestBytes  int
    ChunkedBody   bool
    TTFT          time.Duration
    StreamFor     time.Duration
    EventCount    int
    EventBytes    int
    EventEvery    time.Duration
    RetryFailures int
    DisconnectAt  time.Duration
    CompletionSkew time.Duration
    FeatureMode   FeatureMode
}
```

Validation rejects impossible/unbounded values. Scenarios are serializable/describable so an experiment can record an exact scenario fingerprint rather than prose only.

### Result Model

Machine-readable result includes:
- environment/scenario fingerprint;
- run start/end and commit;
- success/error/rejection/cancel counts;
- latency histograms/quantiles for start, TTFT, event forward, terminal completion;
- throughput/completions/events;
- sampled RSS/heap/goroutines/GC/FD/sockets;
- optional DB/store counters and connection/handshake counters;
- paths to pprof/trace/raw result artifacts.

The CLI may emit JSON for scripts plus a concise console summary. `benchmark-scratch.md` stores the human decision and references raw result paths; large binary profiles are not committed into the spec folder by default.

### Staged Admission State

Preferred lifecycle:

```text
unadmitted
  -> materialization-slot-held
  -> body-read-and-sized
  -> decode-byte-reservation-held
  -> decoded/released
```

The implementation may merge count and byte reservations inside `decodeqos` as long as:
- the count/materialization slot exists before full body read;
- cancellation releases every partial reservation;
- no lock is held while performing network/body I/O;
- generation reload shares the same process-owned state;
- oversized requests fail with current contract semantics.

### Shared-Store Concurrency Rules

For secure-session/B2BUA/authority memory stores:

- Never hold a store/shard index mutex while performing external I/O, JSON encode/decode, callbacks, or other potentially blocking work.
- Per-entry locks may protect one logical session/A-leg/dimension's invariant set.
- Define lock ordering explicitly if a top-level index plus entry lock are both needed; prefer lookup under index lock → retain/stabilize entry → release index → mutate entry.
- Expiry deletion must not invalidate an entry still referenced by an in-flight operation.
- Avoid background goroutines solely for cleanup unless benchmark evidence shows bounded opportunistic cleanup is inadequate; if introduced, own shutdown and `goleak` tests.

## Requirements Traceability

| Requirement | Design realization |
|---|---|
| 1 | Evidence gate, scenario/result model, scratchpad workflow |
| 2 | Canonical scenario matrix and perf runner |
| 3 | Streaming Timeout Policy + >legacy-boundary scenario |
| 4 | Staged Admission State / frontend flow |
| 5 | Secure Stream-Event Recorder, per-session entry, durable staged optimization |
| 6 | Stream-event batch plus lifecycle query-count experiments |
| 7 | Usage Evidence Accumulator + buffer inventory/bounds |
| 8 | Auth dispatcher lock-scope redesign + start-burst profile |
| 9 | Per-A-leg B2BUA state + bounded expiry |
| 10 | Per-dimension authority state + tombstone cleanup |
| 11 | Maintained billing spool pending metadata + recovery reconciliation |
| 12 | Clone benchmark gate and smallest-safe-copy reduction |
| 13 | No-consumer traffic fast path + bounded optional delivery |
| 14 | Residual-lock profile/review with measured-no-change path |
| 15 | Topology-aware transport/OS measurement + tuning gate |
| 16 | Existing profiling/metrics reuse + bounded added metrics |
| 17 | Local concrete owners, no generic framework, active-spec reconciliation |
| 18 | Final unchanged matrix, 1k/5k/10k, soak, honest capacity verdict |

## Data / State Changes

### Secure-session durable sequence state

The first implementation should avoid schema change if eliminating redundant `Next*Seq` calls and consolidating append work already meets the target. If a persisted per-session next-sequence counter is benchmarked and selected, the migration must:
- initialize counters from existing transcript/audit maxima transactionally;
- preserve uniqueness/order across concurrent writers;
- work on SQLite and PostgreSQL;
- obey PgBouncer transaction-pool constraints;
- include downgrade/old-row behavior or explicitly irreversible migration policy consistent with repository migrations.

A schema migration is therefore a **conditional design branch**, not mandatory speculative work.

### Billing spool pending metadata

Prefer metadata stored transactionally in the spool database or reconstructed safely at open. It must not become an in-memory-only authority that can undercount pending rows after crash. If cached in memory for speed, database reconciliation remains the source of recovery truth.

### Performance result data

Performance JSON/results/profiles are test artifacts, not product state. They contain no secrets and can be excluded from normal runtime persistence. The small human evidence ledger remains `benchmark-scratch.md`.

## Error Handling

### Performance Harness

- A run that cannot establish requested concurrency must distinguish generator exhaustion, proxy rejection, backend failure, DB failure, and host/OS resource failure.
- Partial runs are recorded as invalid/inconclusive unless the scenario explicitly tests rejection behavior.
- Correctness assertions (event order/count/terminalization) failing makes the performance result invalid even if throughput is high.

### Body Admission

- Preserve existing request-too-large/invalid JSON/auth error classes.
- Admission exhaustion uses the existing frontend QoS error vocabulary or a narrowly extended internal reason; do not expose implementation details.
- Cancellation while waiting/reading releases partial reservations exactly once.

### Secure-Session Recording

- Mandatory store failure follows current fail-closed/pre/post-commit rules.
- Best-effort projection failure is observable but cannot fail a mandatory event unless current policy says so.
- Sequence conflicts/transaction failures retry only where the existing store contract permits safe retry; no duplicate audit/transcript row may be created.

### Shared Stores

- Eviction/expiry must not surface spurious “not found” for an entry actively retained by an operation.
- Shard/per-entry design retains current typed errors and idempotency.

### Billing Spool

- Metadata mismatch on startup triggers reconciliation/fail-closed behavior according to current spool safety posture, never silent cap undercounting.

## Performance & Scalability Measurement Strategy

### Microbenchmark protocol

Use fixed scenario sizes (`-benchtime=Nx` where history growth would make adaptive iteration non-stationary) and repeated sample counts appropriate for `benchstat`. Record `ns/op`, `B/op`, `allocs/op` plus relevant custom counters. For contention benchmarks, also run with representative `-cpu`/`GOMAXPROCS` and collect mutex/block profiles in dedicated repeats.

### End-to-end protocol

- Drive real frontend HTTP/SSE or WS through a separately measurable proxy process where capacity matters.
- Use deterministic local upstream responses; do not include provider rate limits/network variance in proxy-capacity results.
- Prefer placing the traffic generator on a separate host/process for high tiers; when co-located, record generator resource use and leave headroom.
- Warm up compilation/caches/connections before timed steady-state periods when the scenario is not explicitly measuring cold start.
- Run at least 1k → 5k → 10k rather than jumping directly to 10k; stop/escalate based on correctness/resource safety.
- Capture headline results without heavy profiling and separate diagnostic runs with profiling if profiler overhead is material.

### Acceptance philosophy

The spec intentionally does not invent one universal percentage threshold for every optimization. A 3% CPU win may matter in an event hot loop while a 3% latency shift may be noise in a DB path. Each experiment states its primary metric and variance before the candidate change. The decision must be supported by repeated measurements and absence of unacceptable secondary regressions.

Known correctness fixes are accepted on demonstrated correctness plus no material regression. Optional complexity requires demonstrated material benefit relative to its maintenance cost.

## Testing Strategy

### Default / unit tests

- Scenario validation, result aggregation, histogram/quantile math, and cancellation cleanup.
- Staged admission state transitions and exact release under all error paths.
- Secure-session batch mutation equivalence, sequence monotonicity, concurrent session independence, expiry/reclamation.
- Incremental/compact usage evidence equivalence to existing reconstruction across adversarial chunk boundaries and event mixtures.
- B2BUA/authority per-key race/invariant tests and billing metadata reconciliation.

### Race / scheduling tests

- Body admission cancellation while waiting/reading/decoding.
- Secure-session same-session versus different-session concurrent append and expiry race.
- B2BUA fetch/touch/attempt/evict races.
- Authority acquire/release/expiry races.
- Observer queue shutdown/full behavior if any queue is added.

### Integration tests

- SQLite/PostgreSQL secure-session mutation parity and concurrent sequence allocation.
- PgBouncer transaction-pool compatibility where configured.
- Billing spool crash/reopen/reconciliation/cap behavior.
- Long HTTP stream exceeding the legacy timeout boundary.

### Performance tests

- Existing recorder, traffic, and authority benchmarks are extended rather than discarded.
- New body, B2BUA, billing, clone, residual-lock, and end-to-end harness scenarios follow the evidence protocol.
- Performance tests are not forced into every default unit-test invocation if expensive; smoke/validation tests remain deterministic/default while scale runs use explicit commands/CI/nightly/manual gates documented by the tool.

## Security Considerations

- Profiling endpoints retain existing authentication/access controls.
- Performance result labels and metrics use bounded scenario/environment IDs only; no prompts/secrets/session IDs.
- Early body admission occurs **after authentication** so unauthenticated request rejection does not consume body-memory slots unnecessarily, while still preceding full authorized-body materialization.
- Mandatory secure-session persistence is never converted to best-effort for performance.
- Traffic observation redaction remains before any observer that may expose payload data.

## Migration / Rollout Strategy

1. Land perf harness and baseline with no production behavior changes.
2. Land streaming timeout/body-admission fixes with characterization + measurements.
3. Land secure-session store/app contract simplification, migrate dual-dialect store implementations together, and run recorder/active-delta evidence.
4. Land bounded stream evidence in coordination with current response-owner shape.
5. Land P1 store/spool changes one domain at a time with their own before/after entries.
6. Run P2 measurement tasks; only branches with proven value become production changes.
7. Run final full matrix/soak and record capacity verdict.

Do not batch all optimizations into one opaque implementation PR: smaller causal changes preserve benchmark attribution and make regressions/reverts tractable.

## Final Design Validation Verdict

The design satisfies all 18 requirements and remains within the repository's existing architecture. The highest-risk semantic areas—mandatory security durability, billing durability, body/auth ordering, stream terminal behavior, and active runtime ownership refactors—have explicit preservation and revalidation gates. Optional complexity has benchmark gates and a measured-no-change path. **GO for implementation.**
