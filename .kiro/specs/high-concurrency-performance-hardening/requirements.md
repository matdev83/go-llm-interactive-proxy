# Requirements Document

## Introduction

Go-LIP shall harden its streaming data plane and supporting persistence/coordination paths for high-concurrency operation by addressing every material concurrency/performance issue identified by the static audit in GitHub issue #394. The target is to make **thousands of simultaneous logical LLM streams a routinely testable operating regime** and to establish reproducible evidence for the project's ambitious **10,000+ simultaneous-stream** goal on appropriately provisioned hosts. Physical transport-connection counts remain first-class evidence because HTTP/1.1 and multiplexed HTTP/2 topologies impose materially different socket costs.

This is a brownfield performance-and-scalability specification. It does not assume that a proposed optimization is useful merely because a code path looks inefficient. The implementation program shall be evidence-driven from the first task: construct/reuse deterministic traffic generators before changing production performance behavior, record a controlled baseline, measure each optimization under the workload it is intended to improve, and preserve unsuccessful experiments as evidence. **Not measured = not proved.**

Known correctness/scalability hazards found by #394 (for example whole-request streaming timeouts, event-cadence persistence, unbounded retained output, and body materialization before admission) remain required remediation targets; measurement determines the implementation choice and proves the result rather than providing permission to ignore a known broken contract. The untouched implementation baseline is the post-#446 tree: #446 changed B-leg launch/cancellation linearization, sibling teardown, executable-plugin cancellation, and terminal evidence ownership, so pre-#446 stream/disconnect assumptions are not acceptable substitutes for current measurements. Lower-priority or speculative changes shall ship only when measurements demonstrate material benefit or a separately demonstrated correctness/architectural requirement justifies them.

The specification shall not claim that every machine must sustain 10,000 active streams. Capacity certification must identify the tested hardware/OS/runtime/database and transport topology, distinguish logical streams from TCP/HTTP connections, distinguish proxy limits from traffic-generator/OS/external-service limits, and report the highest proven tier rather than extrapolating from smaller tests.

## Boundary Context

- **In scope**: deterministic load/traffic generation; performance evidence capture; logical-stream and transport-connection measurement; streaming timeout semantics; request-body admission placement; secure-session stream recording and persistence; secure-session/B2BUA lifecycle query amplification; bounded stream/evidence memory; auth event dispatch contention; B2BUA continuity-store contention/expiry; concurrency-authority lease-store contention; durable billing-spool completion throughput; canonical-call cloning/allocation pressure; traffic-observation overhead; residual shared runtime/credential/state/affinity/lifecycle locks; HTTP connection/transport tuning; resource cleanup; profiling/metrics needed to prove changes; final 1k/5k/10k logical-stream and soak certification.
- **Out of scope**: new LLM protocol semantics; selector/routing policy redesign; provider-specific behavior in core; new monetary/billing semantics; weakening secure-session mandatory durability/fail-closed policy; replacing the goroutine-per-stream model without evidence; generic worker-pool/actor frameworks; Cartesian frontend×backend performance matrices; production-provider load testing; public SDK/config growth solely for benchmarks.
- **Adjacent expectations**: `turn-recv-terminal-ownership-simplification` may relocate response-pipeline state and `request-attempt-pipeline-state-simplification` may relocate request/attempt state. This specification owns the **boundedness/performance contracts** regardless of which private owner holds them when implementation lands. Implementers must reconcile against the then-current code/spec state and must not reintroduce duplicated state merely to avoid coordinating the changes. The cancellation/lifecycle contracts delivered by #446 are current-state invariants and measurement surfaces, not pending dependencies or permission for another cancellation redesign.
- **Boundary ownership**: performance harness and benchmark tooling live in internal test/support surfaces; runtime semantics remain in their existing core/frontend/infrastructure owners; provider SDKs remain confined to backend adapters; composition/tuning stays in composition roots.
- **Revalidation triggers**: streaming/event ownership changes; secure-session durability semantics; admission ordering; auth/control-plane sink contracts; B2BUA/authority store semantics; billing-spool durability; HTTP client/server timeout semantics; database transaction/query changes; new queues/goroutines; public configuration changes; transport multiplexing/topology changes.

## Requirements

### Requirement 1: Evidence-First Performance Program

**Objective:** As a maintainer, I want every scalability claim tied to reproducible measurements, so that optimizations are retained because they work rather than because they look plausible.

#### Acceptance Criteria

1. Before any production performance optimization is implemented, the system shall provide a deterministic local load harness capable of driving the proxy through real frontend HTTP/streaming paths against deterministic local reference backends or fakes.
2. Where existing `internal/refclient`, `internal/refbackend`, `internal/testkit`, or protocol fixtures can provide the required wire behavior, the implementation shall reuse or extend those assets instead of creating a parallel protocol-emulation stack.
3. The load harness shall parameterize at minimum **concurrent logical streams**, transport topology and transport-connection plan, ramp rate, request-body size, TTFT, stream duration, event count, event payload size, event cadence, controlled pre-output failures/retries, cancellation/disconnect timing, and synchronized completion bursts.
4. Before changing a production path covered by this specification, the implementing agent shall record an equivalent baseline experiment in `.kiro/specs/high-concurrency-performance-hardening/benchmark-scratch.md` including commit, environment, scenario, configuration, measurements, and relevant profile/result artifact paths.
5. After each optimization attempt, the implementing agent shall rerun an equivalent experiment and append the candidate measurements, causal/statistical comparison, conclusion, and keep/revise/revert decision to the same scratch file.
6. If an experiment is statistically inconclusive or does not demonstrate the claimed benefit, then the implementation shall not describe that optimization as successful and shall revert it unless a separately measured correctness or architectural invariant requires the change.
7. Failed, neutral, regressive, invalid, environment-limited, and reverted optimization attempts shall remain recorded in the scratch file so later agents do not repeat them without new evidence.
8. Microbenchmark comparisons shall use repeated samples and a statistical comparison procedure rather than one baseline number versus one candidate number.
9. End-to-end/load comparisons shall use repeated identical scenarios sufficient to expose run-to-run variance and shall report distributions/tail metrics where latency is relevant.
10. Before/after comparisons shall keep host class, Go version, `GOMAXPROCS`, proxy configuration, database topology, feature flags, payload shape, logical-stream count, transport topology/connection plan, ramp, stream cadence, duration, profiler settings, and certification gate profile identical except for explicitly recorded experimental variables.
11. Performance evidence shall never expose raw prompts, credentials, bearer tokens, secrets, or unbounded-cardinality user/model/session identifiers through scratch text, result JSON, benchmark labels, logs, metrics, profile labels, trace metadata, or artifact names.
12. Where a requested scale tier cannot be generated because of host, OS, traffic-generator, socket, file-descriptor, or external-service limits, the result shall identify that limiter and shall not attribute it to the proxy without evidence.
13. A run whose canonical event/order/count/terminalization or other required semantic assertions fail shall be machine-classified as invalid for performance certification regardless of throughput.
14. The machine-readable result shall distinguish valid success, correctness-invalid/incomplete runs, safety-aborted runs, environment/generator-limited runs, and proxy-derived failures rather than reducing all non-success outcomes to one error count.

### Requirement 2: Canonical High-Concurrency Scenario Matrix

**Objective:** As a performance engineer, I want stable workload shapes that isolate different resource costs, so that capacity and regressions can be diagnosed rather than hidden inside one aggregate benchmark.

#### Acceptance Criteria

1. The harness shall define separate long-held idle/low-rate streaming scenarios that measure per-logical-stream memory, goroutine, transport-connection/socket, and cleanup cost without conflating those costs with high event throughput.
2. The harness shall define active-delta scenarios with controlled event cadence/payloads that stress the stream receive/forwarding path, secure-session recording, usage evidence, observers, and CPU/allocator behavior.
3. The harness shall define request/session-start burst scenarios that stress authentication, B2BUA/secure-session creation/resume, routing preparation, and pre-first-token persistence.
4. The harness shall define concurrent request-body burst scenarios at configurable sizes up to the configured limit, including both known-length and chunked bodies, to test admission and peak materialization memory.
5. The harness shall define long-output/many-event scenarios that distinguish cumulative delivered output from currently live buffering and expose retained-memory growth and terminal-copy spikes.
6. The harness shall define synchronized completion-burst scenarios that stress terminal work, secure-session finish persistence, billing spool append throughput, and cleanup.
7. The harness shall define large-prompt/tool-schema scenarios with controlled pre-output retries/failovers to measure `lipapi.Call` clone/allocation amplification.
8. The harness shall define traffic-observation variants for observation disabled, enabled with a fast sink, and enabled with a deliberately slow sink where policy permits.
9. The harness shall define concurrency-authority and B2BUA contention variants covering one hot logical key/dimension and many independent keys/dimensions.
10. The harness shall define mass disconnect/cancellation scenarios and prove reclamation of goroutines, sockets, per-stream buffers, session/transient state, and terminal work.
11. The canonical progression shall include 1,000, 5,000, and 10,000 **simultaneous logical-stream** tiers where the host/generator supports them. Every run shall separately record target and observed client/front-door, terminator/proxy (when present), proxy-ingress, and proxy/backend transport connection counts where observable; lower logical-stream tiers establish scaling curves but shall not be linearly extrapolated.
12. Each run shall capture at minimum request-start latency, TTFT, client-facing event-forwarding latency, terminal latency where relevant, throughput/completions, expected versus unexpected error/rejection/cancellation counts, process CPU, system CPU, RSS, live heap, allocation bytes/counts, GC activity, goroutine/stack metrics, and open socket/file-descriptor counts where supported.
13. Where the hypothesis concerns contention, the run shall capture mutex/block profiles; where it concerns CPU, allocations, retained memory, or scheduling, it shall capture the corresponding CPU/alloc/heap/runtime/trace evidence.
14. Where durable storage is in the path, the run shall capture logical store operation count, SQL query/transaction count where observable, database pool wait, and latency distributions sufficient to distinguish proxy CPU from persistence latency.
15. After reaching steady active load and again after disconnect/termination, the harness shall record resource levels so monotonic growth and failed cleanup are distinguishable from steady-state working-set size.
16. Scenario fingerprints shall encode logical-stream target separately from transport-connection/topology controls; two scenarios with the same stream count but different HTTP/1.1 versus HTTP/2 multiplexing shall not compare as equivalent fingerprints.
17. Machine-readable results shall include typed correctness-assertion outcomes, typed run-validity/limiter reasons, achieved steady/peak logical streams, transport connection counts, allocation bytes/counts, stack memory, process/system CPU, and explicit unavailable markers for unsupported platform metrics.
18. Where both execution forms are supported, the harness shall fingerprint and measure built-in/standard backend execution separately from negotiated executable-backend-plugin execution, including per-logical-stream goroutines, bounded channel/buffer capacity and occupancy where observable, scheduler cost, and cleanup.
19. The disconnect/cancellation matrix shall cover cancellation while a B-leg launch is in flight, one versus multiple sibling B-legs, graceful cancellation acknowledgement, deadline/forced-close fallback, and races with upstream success/failure while proving exact terminal, usage, billing, and cleanup behavior.
20. If an executable backend or managed stream fails to satisfy the cancellation contract that forced `Close` makes `Cancel` joinable, the run shall fail its correctness/cleanup gate and expose the stalled owner without hanging indefinitely or being accepted for capacity certification.

### Requirement 3: Streaming-Safe Timeout Semantics

**Objective:** As a streaming client, I want healthy long LLM streams to remain alive based on progress/cancellation policy rather than an absolute whole-request deadline, so that valid responses are not terminated merely for lasting more than the historical 120-second default.

#### Acceptance Criteria

1. When an inbound streaming response remains healthy and continues according to configured progress/idle policy, the server shall not terminate it solely because total response duration exceeds the former blanket `WriteTimeout` horizon.
2. When an outbound backend stream remains healthy and continues according to request context, TTFT, and stream-idle policies, the HTTP client shall not terminate it solely because the total round trip exceeds the former blanket `http.Client.Timeout` horizon.
3. The system shall retain bounded dial, TLS handshake, response-header/TTFT, request-header/body, explicit stream-idle, caller-cancellation, and shutdown protections appropriate to their phases; removing an absolute whole-stream timeout shall not mean removing all timeouts.
4. Where configuration exposes a whole-request timeout, the effective configuration shall clearly distinguish streaming-safe disabled/zero semantics from positive finite deadlines and shall not silently substitute a positive default when the operator intentionally disables the blanket timeout.
5. Caller cancellation, A-leg cancellation, backend failure, stream-idle timeout, shutdown, and existing pre-output failover rules shall remain behaviorally equivalent except for removal of the unintended total-duration cutoff.
6. Before the timeout change, an automated scenario shall reproduce the long-stream failure at a duration beyond the legacy boundary; after the change, the equivalent scenario shall prove continued streaming and correct terminalization.
7. Non-streaming collection over the canonical streaming path shall continue to have intentional cancellation/deadline behavior and shall not gain an accidental unbounded lifetime through a second execution path.

### Requirement 4: Admission Before Expensive Request-Body Materialization

**Objective:** As an operator, I want a process-level bound on concurrent request-body memory before full `ReadAll`/preflight work, so that a burst of large requests cannot bypass the existing decode budget.

#### Acceptance Criteria

1. Before fully materializing an otherwise accepted request body, the frontend pipeline shall acquire a process-owned admission resource that bounds the number and therefore maximum aggregate size of concurrently materialized request bodies.
2. The admission resource shall remain shared across immutable runtime generations so configuration reload cannot multiply the process memory budget.
3. The admission design shall handle both trustworthy `Content-Length` requests and chunked/unknown-length requests without permitting unbounded body allocation while waiting for downstream decode admission.
4. The existing maximum request-body size, JSON shape/preflight protections, authentication ordering, status/error mapping, and protocol decoding behavior shall remain correct.
5. When admission capacity is unavailable, the implementation shall use bounded rejection or bounded waiting consistent with the frontend QoS contract and shall not spawn unbounded goroutines or allocate the full request body before capacity exists.
6. The implementation shall avoid creating a second independent memory budget when the existing decode-QoS limiter can safely provide staged count/byte reservation; any new limiter/configuration surface requires measured justification.
7. A before/after body-flood benchmark shall record peak RSS/live heap, allocation rate, admitted/rejected requests, latency, and cleanup for at least one near-maximum-body burst at increasing concurrency.
8. The resulting bound shall be derivable from configured admission/body limits rather than from the number of clients that can concurrently reach `ReadAll`.

### Requirement 5: Secure-Session Stream Recording Without Event-Cadence Amplification

**Objective:** As an operator using secure sessions, I want audit/transcript durability semantics without avoidable O(n²), global-lock, or multi-round-trip work on every small stream delta, so that recording does not dominate event forwarding at scale.

#### Acceptance Criteria

1. When recording a transcript or audit event, the store contract shall allocate sequence/order and append the record as one logical mutation rather than requiring the application path to call `Next*Seq` and then a separate append that re-discovers the next sequence.
2. The in-memory secure-session store shall obtain the next transcript/audit sequence in O(1) amortized time rather than scanning all prior events for every append.
3. Mutating one secure session shall not require unrelated sessions to serialize behind one process-wide store mutation lock where per-session or sharded ownership can preserve the same contract.
4. Stream activity timestamps shall not require one durable write for every client-facing delta when an equivalent coalesced/terminal update can preserve the authoritative activity/expiry contract.
5. The durable SQLite/PostgreSQL adapters shall minimize synchronous SQL round trips and transactions per client-facing event while preserving atomic sequence uniqueness, record order, fail-closed semantics, and transaction-pooler compatibility.
6. Best-effort transcript/audit/control-plane projection may use bounded batching or asynchronous delivery only when its existing failure policy permits it; any queue shall have explicit capacity, backpressure/drop/coalesce behavior, cleanup, and bounded-cardinality metrics.
7. Mandatory persist-before-release recording shall remain mandatory: client-visible content shall not be released before the required durable acknowledgement merely to make a benchmark faster.
8. If the minimal remote durable transaction per mandatory event remains the dominant limiter, the implementing agent shall benchmark at least one durability-preserving alternative such as a local durable WAL/spool acknowledgement or bounded microbatching **before** adding that complexity, and shall record the latency/durability trade-off in the scratch file.
9. Secure-session control-plane decorators shall not multiply per-delta durable work beyond the authoritative event mutation unless that projection is explicitly mandatory; otherwise projection shall follow its documented best-effort/bounded policy.
10. The memory store shall have explicit session/transcript/audit retention and expiry/reclamation behavior sufficient to prevent completed/expired session history from growing process memory indefinitely.
11. Benchmarks shall cover increasing event history lengths and increasing concurrent sessions for memory and durable stores, measuring event-forwarding latency, store operations/SQL QPS, CPU, allocations, mutex/block contention, and retained memory.
12. Fixed-operation microbenchmarks shall include long-history cases so adaptive benchmark iteration does not hide increasing per-append complexity.

### Requirement 6: Remove Avoidable Turn Start/Finish Persistence Chatter

**Objective:** As a client starting or completing a turn, I want only persistence operations required by the authoritative contract, so that burst admission and completion are not dominated by redundant reads/writes.

#### Acceptance Criteria

1. When a durable secure-session/A-leg record is created and all data required by the caller is already known or returned by the write, the application shall not immediately re-read the same row merely to reconstruct equivalent state unless a measured consistency requirement demands it.
2. When stream/turn setup already holds an authoritative A-leg value, downstream preparation shall reuse that value rather than re-fetching the same record solely because of ownership plumbing.
3. Turn-finish audit persistence shall not perform a separate next-sequence query followed by an append that independently calculates the sequence.
4. Secure-session readiness, resume fingerprint semantics, A-leg lineage, route overrides, exactly-once terminal behavior, and durable transaction boundaries shall remain correct.
5. SQLite, PostgreSQL, and configured PgBouncer/transaction-pooler compatibility rules shall remain satisfied by any query/transaction consolidation.
6. Before/after start-burst and completion-burst measurements shall record store operations/SQL query and transaction counts, pool waits, p50/p95/p99 start/TTFT/finish latency, and CPU/allocation effects.
7. New caching shall be introduced only when workload measurements show repeated stable reads whose invalidation/consistency contract is clear; cache existence shall not be used as a substitute for eliminating redundant calls.

### Requirement 7: Bounded Per-Stream Retained Output and Incremental Usage Evidence

**Objective:** As an operator, I want live stream memory to depend on bounded in-flight state rather than all output already delivered to the client, so that long responses do not multiply heap usage across thousands of streams.

#### Acceptance Criteria

1. Usage reconstruction shall support an incremental or otherwise compact evidence model that does not require retaining the complete client-visible event history until terminalization solely to count/reconcile usage.
2. The runtime shall not simultaneously retain multiple unbounded representations of the same already-forwarded output such as full event slices plus concatenated visible text plus duplicate customer-evidence event/text builders unless each representation is required by an explicit contract.
3. Text, reasoning, tool-argument, event-history, completion-gate, recovery-drain, and pending-wire-event buffers shall each have one documented owner and either an explicit byte/event bound or a documented semantic reason why full retention is unavoidable.
4. Where full materialization is semantically unavoidable for a feature, the feature shall enforce an explicit configured or protocol-derived maximum and shall fail/bound safely rather than allowing memory to scale without limit.
5. Terminal usage/accounting shall avoid copying the entire accumulated event history multiple times merely to filter usage/non-usage events or construct a terminal input.
6. Provider/operator usage evidence, client-visible usage reconstruction, tool finalization, completion gates, secure-session recording, traffic/final observers, compaction observation, and interleaved-thinking semantics shall remain correct after evidence compaction.
7. Where `turn-recv-terminal-ownership-simplification` has landed, bounded accumulators shall live in its response-pipeline/current-attempt owners; where it has not landed, this work shall use a narrow private seam that can migrate to those owners without recreating duplicated buffers.
8. A long-output benchmark shall sweep event count/output bytes and concurrent stream count while measuring per-stream/live heap slope, allocations, GC CPU/pauses, event latency, and terminalization peak allocation.
9. After an event has been irreversibly delivered and no contract requires replay/materialization, increasing cumulative delivered output shall not cause unbounded proportional retained-memory growth in that stream.
10. After stream completion/cancellation and bounded terminal work, retained output/evidence memory shall become reclaimable and the disconnect/soak tests shall show no monotonic heap growth attributable to completed streams.

### Requirement 8: Remove Global Auth-Event Sink Serialization

**Objective:** As a client starting requests concurrently, I want independent auth/session-start events to avoid one global sink critical section, so that logging/projection latency cannot serialize the request-start path.

#### Acceptance Criteria

1. The auth event dispatcher shall not hold one process-wide mutex while invoking external/logging/control-plane sink code unless strict cross-request ordering is an explicit contract and is proven necessary.
2. If a particular sink requires serialization, that sink or a dedicated adapter shall own the serialization scope rather than imposing it on all auth-event delivery.
3. If ordering is required only within one session/principal, the implementation shall prefer the narrowest ordering scope that preserves the contract.
4. Default structured logging behavior, fail-open/fail-closed delivery policy, redaction, and event ordering requirements shall remain correct.
5. Before/after request-start burst benchmarks shall include the default log sink and, where available, the control-plane-decorated sink, recording throughput, p50/p95/p99 start latency, CPU, and mutex/block profiles.
6. The implementation shall not replace one short mutex with an unbounded event queue or one goroutine per event merely to remove lock contention.
7. If the baseline demonstrates no material contention under representative start rates, no speculative concurrency framework shall be added; the result shall be recorded as measured-no-change.

### Requirement 9: Scale B2BUA In-Memory Continuity by A-Leg Rather Than Store-Wide Mutation

**Objective:** As an operator with many independent conversations, I want continuity activity and attempt operations to contend primarily within the affected A-leg, so that unrelated sessions scale independently.

#### Acceptance Criteria

1. Ordinary `ResolveALeg`/`FetchALeg` activity refresh shall not require a store-wide exclusive lock for the full operation when per-record/sharded synchronization can preserve correctness.
2. Creating or touching one A-leg shall not perform an unbounded full-store expiration sweep while holding a store-wide request-path critical section.
3. Expiry/eviction shall use bounded-amortized cleanup, an expiry index/wheel/heap, sharding, or another measured design that preserves TTL and maximum-leg semantics without long global pauses.
4. Mutable attempt sequence, route override, weighted-first, interleaved state, last-seen activity, and lineage invariants shall remain authoritative and race-safe for one A-leg.
5. Benchmarks shall separately cover many independent A-legs, one hot A-leg, mixed reads/writes/attempt creation, and high-cardinality expiry/churn.
6. Before/after evidence shall include operation latency/throughput, CPU/allocations, mutex/block profiles, live entry count, expiry work, and race-test results.
7. The implementation shall prefer the smallest synchronization change that measurements show removes material contention; sharding/per-leg locks shall not be introduced where a simpler lock-scope reduction proves sufficient.

### Requirement 10: Scale the In-Memory Concurrency-Authority Lease Store by Dimension

**Objective:** As an operator enforcing concurrency limits, I want admission cost to depend on the relevant rule/dimension rather than the full historical lease map, so that the safety mechanism does not become the global bottleneck it is intended to prevent.

#### Acceptance Criteria

1. Acquiring a lease shall not require scanning all historical leases under one process-wide mutex to reclaim expiry and count live leases when equivalent per-dimension state can maintain the same authority.
2. Released/expired lease tombstones shall not remain in the active scan set indefinitely and cause admission cost to grow with lifetime history.
3. Capacity checks, idempotent/replay behavior, lease identity, expiry, release, query/reporting, and fail-closed authority semantics shall remain exact under concurrent acquisition/release.
4. The existing 100-contender benchmark shall be retained or evolved, and additional fixed scenarios shall cover at least 1,000 contenders plus the highest locally practical tier for one saturated dimension and many independent dimensions.
5. Before/after evidence shall include acquire/release latency distributions, throughput, CPU, allocations, mutex/block profiles, live/tombstone counts, and cleanup cost.
6. More complex expiry/index data structures shall be introduced only when measurements show material benefit over a simpler per-dimension/sharded counter design.
7. If the feature is disabled in a production configuration, the harness shall still measure the enabled path separately and shall not use the disabled default to dismiss its scalability contract.

### Requirement 11: Preserve Durable Billing While Scaling Completion Bursts

**Objective:** As an operator using durable terminal billing spooling, I want completion throughput to avoid redundant full-table aggregate work while preserving crash-safe durability and bounded storage.

#### Acceptance Criteria

1. Appending one terminal billing-spool record shall not execute a full pending `COUNT`/`SUM` aggregation on every append when equivalent pending-record/payload totals can be maintained safely and reconciled after restart/recovery.
2. Replay/idempotency lookup, pending-record and pending-byte caps, live database-size cap, minimum free-disk guard, WAL behavior, and required synchronous durability shall remain intact.
3. The spool shall remain bounded and shall not trade database serialization for an unbounded in-memory queue.
4. Any change to connection count, batching, transaction grouping, synchronous mode, or acknowledgement semantics shall be accepted only after both throughput measurements and crash/durability tests prove the existing contract is preserved.
5. Completion-burst benchmarks shall measure append p50/p95/p99 latency, sustained/burst throughput, DB lock/busy time, SQL operation counts, CPU, allocations, queue/backlog, database growth, and replay/recovery behavior.
6. If the current single-writer connection remains adequate after removing redundant aggregate work, the implementation shall keep the simpler durable writer rather than add concurrency for theoretical throughput.
7. Billing remains terminal-only: this work shall not move monetary journal/rating writes onto the stream receive/event path.

### Requirement 12: Reduce `lipapi.Call` Clone/Allocation Pressure Only Where Proven

**Objective:** As an operator handling large prompts and failover, I want request preparation/retry allocations to avoid unnecessary deep copies while preserving immutable attempt baselines and mutation isolation.

#### Acceptance Criteria

1. Before changing `CloneCall` or request preparation ownership, benchmarks shall measure small calls, large message histories, large tool schemas/extensions, and controlled retry/failover counts with `ns/op`, allocations/op, bytes/op, CPU, and request-start/TTFT impact where meaningful.
2. The immutable logical-call baseline shall remain protected from mutation by hooks/transforms/attempt-specific preparation after any copy reduction.
3. If clone pressure is material, the implementation shall prefer the smallest safe reduction such as eliminating redundant clones or moving ownership before introducing broad copy-on-write structures.
4. Any copy-on-write or immutable-sharing design shall have characterization tests proving one attempt/hook cannot mutate another attempt, the baseline, or caller-owned request data.
5. Post-change benchmarks shall repeat the exact baseline matrix and shall include GC/heap effects under concurrent large-request scenarios.
6. If measurements show clone cost is not material after higher-impact fixes, the implementation shall record that finding and leave the simpler deep-copy semantics unchanged.

### Requirement 13: Make Traffic/Observation Cost Pay-for-Use and Bounded

**Objective:** As an operator, I want disabled observation to have negligible payload-copy/serialization cost and enabled observation to obey explicit backpressure bounds, so that diagnostics cannot silently dominate streaming throughput.

#### Acceptance Criteria

1. When there are no traffic sinks/observers requiring a payload, hot paths shall avoid serializing, cloning, or redacting large request/event bodies solely to discover that emission is a no-op.
2. When observation is enabled, existing redaction/privacy, event ordering, raw-versus-observer semantics, and mandatory/best-effort failure policy shall remain correct.
3. Best-effort asynchronous observation, if measurements justify it, shall use a bounded queue with explicit full behavior and metrics; mandatory observation shall apply bounded backpressure rather than silently dropping required events.
4. The implementation shall not create an unbounded goroutine or channel per observed event.
5. Existing stream-traffic microbenchmarks shall be retained/evolved and compared across observation-off, fast-sink, redaction, and deliberately slow-sink scenarios.
6. Before/after evidence shall capture event-forwarding latency, throughput, CPU, allocations, serialization/redaction profile share, queue metrics if applicable, and correctness under sink failure.
7. Asynchronous redesign shall not ship solely because it appears scalable; the simpler synchronous path shall remain when no material bottleneck is demonstrated.

### Requirement 14: Measure and Remove Residual Shared Locks Only When Material

**Objective:** As a maintainer, I want remaining shared synchronization assessed systematically, so that true contention is removed without replacing short safe locks with unnecessary architecture.

#### Acceptance Criteria

1. The implementation program shall benchmark/profile at minimum executor lifecycle-coordinator lookup locking, shared RNG routing selection, credential-pool acquisition, generic in-memory scoped-state access/decoding, affinity memory-store access, and top-level leg-lifecycle start/end churn.
2. For each residual lock candidate, the scratch file shall record whether contention is material at representative concurrency and which metric/profile establishes that conclusion.
3. When a lock is material, the implementation shall reduce lock scope, pre-wire immutable references, shard/per-key state, move expensive decode/work outside the lock, or use an appropriate atomic/concurrency primitive while preserving existing semantics.
4. Deterministic routing tests, credential cooldown/selection semantics, state TTL/encoding behavior, affinity correctness, and lifecycle cancel/end exactly-once behavior shall remain correct.
5. When a lock is not material, no production change shall be required and the measured-no-change conclusion shall satisfy this requirement.
6. Generation retain/release CAS, model-catalog atomic snapshots, per-stream event-pump synchronization, and existing per-A-leg lifecycle synchronization shall not be rewritten without new profile evidence showing they are material bottlenecks.

### Requirement 15: Connection and Transport Scalability Must Be Measured, Not Assumed

**Objective:** As an operator, I want logical stream concurrency, direct HTTP/1.1 connections, externally terminated/multiplexed HTTP/2 connections, and outbound transport behavior characterized separately, so that socket/handshake tuning reflects the deployment topology rather than conflating streams with sockets.

#### Acceptance Criteria

1. Capacity scenarios shall define logical-stream concurrency independently from physical transport-connection controls and shall identify whether clients connect directly to the standard plaintext listener or through a TLS/HTTP2-capable reverse proxy/terminator.
2. HTTP/1.1 and HTTP/2 workload generation shall encode their multiplexing semantics explicitly: one active HTTP/1.1 streaming response occupies its connection, while HTTP/2 may carry multiple logical streams per client/front-door connection subject to the configured/negotiated stream limit.
3. Capacity results shall report target/achieved logical streams separately from actual client/front-door, terminator/proxy, proxy-ingress, and proxy/backend connection/socket counts where observable.
4. Direct long-stream testing shall record host open-file/socket/backlog constraints so OS limits are distinguishable from Go/proxy limits.
5. The shared outbound HTTP transport shall remain shared across requests/generations and shall not gain an arbitrary active-connection cap solely as a tuning experiment.
6. Before changing `MaxIdleConns`, `MaxIdleConnsPerHost`, keepalive, or related pool values, benchmarks shall record outbound connection reuse, active/idle counts where observable, TLS handshakes, HTTP/2 stream reuse, CPU, latency, and backend topology.
7. Transport default changes shall ship only when repeated measurements demonstrate a benefit without connection starvation, stale-connection regressions, or excessive idle resource retention.
8. Where host tuning is required to reach a scale tier, reusable benchmark tooling/documentation shall report the prerequisite instead of silently modifying the operating system.
9. A capacity claim phrased in “connections” shall state which transport leg it refers to; the project-level 1k/5k/10k certification in this spec is primarily a **logical-stream** claim with connection counts reported as topology evidence.

### Requirement 16: Low-Overhead and Privacy-Safe Performance Observability

**Objective:** As a performance engineer, I want enough observability to explain bottlenecks without materially creating them or leaking sensitive data, so that benchmark conclusions are attributable and diagnostic evidence is safe.

#### Acceptance Criteria

1. The program shall reuse existing diagnostics/pprof, runtime metrics, Prometheus, tracing, database instrumentation, and benchmark hooks where adequate before adding parallel observability infrastructure.
2. CPU, heap, allocation, block, mutex, and trace artifacts shall remain protected by existing diagnostics/access controls and shall not become unauthenticated production endpoints or public artifacts.
3. New production metrics, benchmark labels, pprof labels, trace task/region/log metadata, logs, result files, and artifact names shall use bounded/sanitized identifiers and shall not include raw prompts, secrets, unconstrained session IDs, or arbitrary model/user identifiers.
4. Before high-scale evidence is accepted, automated validation and/or an explicit allowlist shall verify every diagnostic channel the harness emits: result JSON/console labels, scratch references, logs, metrics, pprof labels/metadata where inspectable, trace metadata/labels, and generated artifact names. Binary profile/trace files that cannot be safely content-redacted shall be treated as access-controlled diagnostics rather than published evidence.
5. Where a new bounded queue/batcher/spool is introduced, metrics shall expose the minimum state needed to prove capacity/backpressure behavior, such as depth, full/backpressure, drop/coalesce count where allowed, drain lag, and errors.
6. Where persistence amplification is optimized, instrumentation shall make logical store operations and/or SQL query/transaction counts measurable in tests/benchmarks without coupling domain code to a specific SQL implementation.
7. Benchmark-only instrumentation that is too expensive or too specific for production shall remain in internal test/support surfaces and shall not widen public SDK/configuration contracts.
8. The performance overhead of newly added always-on production instrumentation shall itself be measured when it appears on a hot event/request path.

### Requirement 17: Preserve Brownfield Architecture and Coordinate Active Refactors

**Objective:** As a maintainer, I want scalability fixes to simplify hot-path ownership instead of creating a second performance framework, so that the code remains extensible and compatible with ongoing architecture cleanup.

#### Acceptance Criteria

1. The implementation shall preserve `Frontend -> pkg/lipapi canonical -> core -> Backend` boundaries and shall not add pairwise frontend×backend performance logic or provider-specific branches to core.
2. The implementation shall not introduce a generic worker pool, actor/event framework, universal mutable execution bag, service locator, reflection registry, or unbounded per-event goroutine architecture merely to address contention.
3. Where an active ownership-simplification spec has already landed, performance state shall be changed at its new authoritative owner; where it has not landed, the change shall use a narrow private seam that can be migrated without dual authorities.
4. A performance change shall not restore financial rating/journal mutation to the stream receive path or weaken existing billing-call/leg exactly-once semantics.
5. A performance change shall not weaken secure-session mandatory persistence, authentication, request-size guards, secret redaction, capability negotiation, or no-retry-after-client-visible-output rules.
6. Every production change shall include focused correctness/regression tests before or with the optimization, and synchronization changes shall pass targeted `-race` coverage.
7. Packages that newly own goroutines/queues shall include explicit shutdown/cancellation ownership and `goleak` coverage consistent with repository steering.
8. Benchmark harness APIs shall remain internal/test support unless a real product consumer independently justifies a public contract.
9. Architecture tests shall be added or extended where a newly established bound/ownership rule is susceptible to regression, particularly unbounded stream histories, per-event durable call amplification, and global external-sink locking.
10. The final implementation shall pass repository quality, unit, parity/architecture, and relevant integration gates in addition to performance certification.

### Requirement 18: Deterministic Final Scalability Certification and Honest Capacity Statement

**Objective:** As a project owner, I want a final evidence package with predeclared pass/fail gates that states exactly what the optimized proxy can sustain and what still limits it, so that “thousands” or “10k” is a measured engineering claim rather than marketing extrapolation or a subjective reading of benchmark charts.

#### Acceptance Criteria

1. After all retained optimizations, the implementation shall rerun the frozen canonical baseline scenario matrix without weakening workload parameters or disabling the feature whose path is being certified.
2. Certification shall include at minimum minimal-feature long-held streams, active-delta streams with secure-session memory storage, active-delta streams with durable secure-session storage, traffic observation off/on, concurrency authority enabled, durable billing completion bursts, body admission bursts, and disconnect cleanup. Where executable backend plugins are supported, certification shall include at least one representative HOLD/DELTA execution-path comparison and the full post-#446 cancellation/disconnect contract matrix rather than certifying only the built-in/standard backend path.
3. Certification shall include a healthy stream that runs beyond the legacy whole-request timeout boundary and proves it is not terminated solely by elapsed total duration.
4. Certification shall include a multi-hour soak, or a longer equivalent duration justified by the environment, at the highest sustainable representative tier to detect monotonic heap/session/history/goroutine/socket growth.
5. The final scratch record shall state results separately for 1,000, 5,000, and 10,000 **simultaneous logical streams** where the environment can generate them, distinguish held/low-rate from actively emitting streams, and record actual transport-connection counts/topology for every tier.
6. Before the first run used to issue a capacity verdict, each required scenario/tier shall have a versioned **certification gate profile** frozen in the scratch registry and included in the scenario/result fingerprint. It shall define at minimum warm-up, minimum steady-state duration, valid repetition count, correctness expectations, unexpected error/rejection budget, applicable tail-latency gates, memory-growth criterion, cleanup-recovery criterion/window, mandatory durability criteria, and any resource-headroom gate required for the claim.
7. Correctness and mandatory durability are non-negotiable gates: unexpected canonical event/order/count/terminalization failures shall be zero unless the scenario explicitly declares the behavior as expected; missing or duplicate mandatory durable records shall be zero; existing exactly-once/order rules shall hold.
8. Expected QoS/policy rejections in a scenario designed to exercise rejection shall be classified separately from unexpected failures. A normal capacity scenario shall not silently treat proxy errors/rejections as successful throughput.
9. Numeric latency, error-budget, memory, cleanup, or resource thresholds used for `GO` shall have a source recorded **before** the evaluated run: an existing product SLO, configured/protocol bound, explicit operator capacity objective, predeclared baseline-regression budget, or frozen lower-tier scaling contract. A threshold shall not be selected or relaxed after candidate/certification measurements are visible.
10. The absence of an existing product latency SLO shall not authorize an invented arbitrary number. Where a required release gate has no defensible source, the tier may be measured but shall remain **NO-GO for certification until the gate objective is defined**; measurements still inform the choice of that future objective.
11. A certification run shall reach the target logical-stream count for the gate profile's required steady-state duration and pass every required gate to receive `GO`. Any required gate failure yields an explicit `NO-GO`; inability of the generator/OS/host to supply the tier yields `NO-GO (environment-limited)`/`unsupported-by-host` rather than a proxy-failure label.
12. The final report shall identify the dominant remaining CPU profile nodes, mutex/block contention, memory consumers, persistence operations, and any proxy-derived capacity limiter at the highest tested tier.
13. The final report shall separately identify OS/host/generator/database/network limits that prevented or constrained a tier and shall not label them proxy failures without supporting evidence.
14. No audited #394 item shall be marked “fixed/optimized” without a corresponding scratch experiment; an item may instead close as “measured non-bottleneck; no production change” when the evidence supports that result.
15. The implementation shall demonstrate cleanup after disconnect/completion, no unintended monotonic growth of completed secure-session/transcript/lease/output state, and no healthy-stream failure caused only by total elapsed duration. Cleanup and growth conclusions shall use the frozen gate method/window rather than visual inspection alone.
16. If 10,000 active logical streams cannot be certified after the scoped fixes, the final result shall be an explicit NO-GO for that tier with the measured limiting component, failed/unsupported gate, and next evidence-driven action rather than an unverified capacity claim.
17. The retained load harness, stable scenarios, result/gate schemas, and benchmark documentation shall remain reusable for future regression testing instead of being deleted after this optimization program.
18. Final implementation readiness shall require `make quality-checks`, `make test`, targeted race tests, relevant SQLite/PostgreSQL integration tests, architecture tests, and the documented performance scenario suite to be executed on the certification commit. `GO` requires all required gates to pass; `NO-GO` shall still be recorded when any gate fails, with remaining independent gates executed where safe and unexecuted safety-dependent gates explicitly marked.
