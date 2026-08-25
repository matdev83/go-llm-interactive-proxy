# Implementation Plan

## Execution Rules

- **Evidence first.** Do not modify production performance behavior until Phase 1 establishes the reusable load harness and Phase 2 records the applicable baseline. For every later production optimization attempt, append a baseline experiment to `benchmark-scratch.md` **before** the change and append the equivalent candidate measurement after it.
- **Not measured = not proved.** An implementing agent may claim an optimization only when repeated before/after evidence supports it. Keep/revise/revert decisions belong in the scratch entry. Neutral/inconclusive/invalid/environment-limited/reverted attempts stay recorded.
- **Logical streams are the capacity unit.** The 1k/5k/10k tiers mean simultaneous logical LLM streams. Physical transport connections/sockets are separate scenario/result fields and must be reported per topology/transport leg; never equate an HTTP/2 connection with one stream.
- **Invalid correctness runs do not count.** A run with failed canonical event/order/count/terminalization or mandatory durability assertions is `invalid-run` for performance claims regardless of throughput. Host/generator inability is `unsupported-by-host`, not a proxy failure.
- **Freeze certification gates before seeing certification results.** Every scenario/tier used for GO/NO-GO has a versioned gate profile covering steady-state duration, correctness, unexpected failures/rejections, applicable tail latency/resource objectives, memory growth, cleanup, and durability. Numeric thresholds require a documented pre-run source; do not invent or relax them after seeing results.
- **Known correctness/scalability defects still get fixed.** Streaming total-duration timeout behavior, pre-materialization admission, mandatory cleanup/boundedness, and confirmed algorithmic/event-cadence amplification are required. Their benchmark proves the chosen fix and detects regressions; it is not permission to leave a reproduced correctness defect intact.
- **P2 work is conditional on measured materiality.** Clone/COW work, asynchronous observer delivery, residual-lock redesign, and transport tuning may complete as `measured-no-change` with no production modification when profiles show no material bottleneck.
- **Use TDD.** Characterization/regression tests are RED before behavior/synchronization changes and GREEN after. Run targeted `-race` for concurrency changes. New goroutine owners require cancellation/shutdown tests and `goleak` where repository steering requires it.
- **Preserve causal attribution.** Keep macro phases sequential. Do not combine unrelated optimizations before measuring each one. Optional measurement tasks marked `(P)` may run in parallel only from the same frozen baseline commit/environment/scenario/gate definitions and must record that baseline.
- **Reuse existing emulators.** Prefer `internal/refclient`, `internal/refbackend`, and `internal/testkit` assets. Do not create a parallel protocol implementation merely for load testing.
- **No production providers.** Proxy-capacity tests use deterministic local upstreams. External PostgreSQL/database latency is measured only in dedicated persistence scenarios.
- **No benchmark-only public surface.** Performance tooling stays internal unless a separate product requirement independently justifies a public API/config field.
- **Preserve architecture.** Do not introduce a generic worker pool, actor/event framework, mutable execution bag, reflection registry, service locator, unbounded queue, or per-event goroutine architecture.
- **Classify hot-path I/O before moving it.** Reuse already-loaded/immutable versioned values before adding a cache. Do not cache stale authority or move mandatory security/session/admission/billing writes to volatile async queues; only documented best-effort work or a proved local durable acknowledgement may leave the initiating call stack.
- **Reuse and pooling are conditional.** Try ownership/copy elimination, preallocation, incremental processing, and redundant-owner removal first. A buffer/object pool must bound retained capacity and reset/zero session data; a worker pool may own only bounded short independent jobs and never hides blocking stream `Recv`, ordered emission, `Cancel`, or `Close` lifecycles.
- **Measure the deployment process tree.** Executable-plugin evidence reports the Go-LIP process, managed connector child processes, and aggregate resources separately. Moving goroutines, heap, allocations, or CPU across IPC is not a deployment-wide optimization.
- **Coordinate adjacent specs.** Before modifying recv/response/request state, inspect the then-current `turn-recv-terminal-ownership-simplification` and `request-attempt-pipeline-state-simplification` implementation state and change the authoritative owner rather than recreating old duplicate fields.
- **Protect every diagnostic channel.** Result JSON, CLI labels, logs, metrics, pprof labels/metadata, trace task/region/log metadata, scratch references, and artifact names must use bounded/sanitized identifiers. Binary pprof/trace artifacts remain access-controlled diagnostics rather than public evidence.
- **Record raw evidence outside the Markdown when large.** `benchmark-scratch.md` records environment, commands/scenario/gate fingerprints, summaries, profile/result paths, conclusion, and decision; do not commit large binary pprof/trace outputs into the spec folder by default.
- **Record NO-GO when a gate fails.** Do not wait for every gate to pass before writing the verdict. Continue remaining independent gates where safe; mark gates not run after a safety stop explicitly.
- **Anchor the baseline after #446.** The first eligible implementation baseline begins at `f70201d037268508931ceab599b12ee4d3b40aad`; record the exact implementation-time commit, and refresh affected scenarios if later production changes land. Never substitute pre-#446 HOLD/DELTA/DISCONNECT assumptions.
- **Treat #446 cancellation ownership as a regression contract.** Measure built-in/standard and negotiated executable-plugin paths separately, preserve launch-permit/parallel-sibling/terminal-evidence behavior, and do not create another cancellation authority absent a separately measured defect.

## Phase 1 — Build the Performance Laboratory Before Touching Production Hot Paths

- [ ] 1. Establish deterministic, reusable load generation and measurement.

- [ ] 1.1 Inventory and select reusable client/backend harness assets
  - Map `internal/refclient`, `internal/refbackend`, and conformance wire-client helpers to streaming HTTP/SSE/WS workloads; select one primary high-scale protocol and a bounded cross-protocol sentinel.
  - Identify existing scripted reference-backend controls that can represent TTFT, event count/size/cadence, pre-output failure, long streams, and terminal timing; extend existing emulators instead of duplicating them.
  - Record which measurements can be collected externally from the load process versus internally from the proxy and ensure the generator can run in a separate process/host.
  - Inventory direct HTTP/1.1 and supported front-door/HTTP2 termination topologies so the harness can distinguish logical streams from physical transport connections.
  - Inventory the post-#446 built-in/standard and negotiated executable-plugin execution paths, including launch permits, sibling cancellation, plugin control/upstream readers, bounded event buffering, optional closer, forced-close joinability, terminal usage/billing evidence, and reusable contract fakes.
  - Build a brownfield hot-path inventory by lifecycle phase: logical/physical database operations and correctness class; allocations/copies; process-shared mutable owners/lock scope; proxy-side and connector-side goroutines/channels/stacks/buffers plus cancellation/join owner.
  - Add a deterministic harness inventory/test fixture so later refactors cannot silently bypass real frontend/backend wire paths.
  - _Requirements: 1.1–1.3, 2.1–2.10, 2.18–2.20, 15.1–15.3, 17.1, 17.8, 19.1–19.4, 19.10–19.12_
  - _Boundary: tests / internal performance support_
  - _Depends: none_
  - _Validation: `go test ./internal/refclient/... ./internal/refbackend/... ./internal/testkit/...`_

- [ ] 1.2 Implement the internal scenario, result, and certification-gate contracts
  - Add `internal/testkit/perf` scenario types with validation and stable IDs for `HOLD`, `DELTA`, `START`, `BODY`, `OUTPUT`, `COMPLETE`, `RETRY`, `OBSERVE`, `AUTHZ`, `B2BUA`, `DISCONNECT`, and `SOAK` families.
  - Model `LogicalStreams` independently from transport topology/connection controls; fingerprint target client/front-door and terminator/proxy connections plus multiplexing limits separately from stream count.
  - Fingerprint backend execution path and cancellation schedule independently: built-in/standard versus negotiated executable plugin, launch-in-flight, sibling count, graceful acknowledgement, deadline/forced close, and upstream-terminal race.
  - Add machine-readable environment/result models covering target/achieved logical streams, observed transport connections, latency/throughput, expected/unexpected outcomes, process/system CPU, RSS/heap/stack, allocation bytes/counts, GC/scheduler, goroutines/channels/buffers, sockets/FDs, database work by lifecycle phase, shared-lock evidence, correctness assertions, unavailable metrics, and typed limiter/run-validity status.
  - Model proxy, managed connector child, and aggregate process-tree resource scopes separately; derive fixed-per-stream versus request-start/per-event/terminal/cleanup cost rather than one aggregate average.
  - Add versioned `CertificationGateProfile` types with frozen warm-up/steady duration, valid repetition count, correctness/durability/error gates, applicable sourced latency/resource/fixed-stream/event-rate/DB/scheduler gates, memory-growth/cleanup methods, and gate-profile fingerprinting.
  - Unit-test serialization, stable fingerprints, zero-vs-unavailable metrics, HTTP/1.1/HTTP2 stream/connection validation, backend-path/cancellation-schedule inequivalence, invalid-correctness classification, limiter classification, and gate PASS/NO-GO behavior.
  - _Requirements: 1.3, 1.10–1.14, 2.11–2.20, 15.1–15.9, 18.5–18.11, 19.1–19.3, 19.14_
  - _Boundary: tests / internal performance support_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/testkit/perf/...`_

- [ ] 1.3 Implement the standalone load runner and scripted-upstream orchestration
  - Add an internal CLI (for example `go run ./internal/testkit/perf/cmd/lipperf`) that can run a named scenario at configured logical-stream concurrency/ramp and emit machine-readable JSON plus a concise console summary.
  - Reuse reference clients for actual frontend wire behavior and control the reference upstream's TTFT/event cadence/event bytes/failure/finish schedule deterministically.
  - Make HTTP transport drivers honor the scenario connection plan: direct HTTP/1.1 active streams use distinct active connections as required; multiplexed HTTP/2 drivers report stream-per-connection behavior and actual connections rather than hiding it.
  - Support graceful mass cancellation/disconnect and exact request/event/completion correctness assertions; failed assertions set `invalid-correctness` so a fast but semantically wrong run cannot certify capacity.
  - Drive equivalent HOLD/DELTA streams through the built-in/standard and negotiated executable-plugin paths where supported, and drive launch-in-flight, one/many sibling, graceful, forced-close, and upstream-terminal-race DISCONNECT schedules using existing #446 contracts rather than duplicating them.
  - Bound every cancellation test by an outer harness watchdog; if forced `Close` does not make `Cancel` joinable, emit a typed correctness/cleanup failure with owner diagnostics instead of hanging or detaching an unowned worker.
  - Add low-scale deterministic smoke tests using local `httptest`/reference servers; do not put 1k/10k workloads into the default unit-test suite.
  - _Requirements: 1.1–1.3, 1.13–1.14, 2.1–2.20, 15, 18.2, 18.17_
  - _Boundary: tests / internal performance support_
  - _Depends: 1.1–1.2_
  - _Validation: `go test ./internal/testkit/perf/...` and low-scale `lipperf` smoke runs for direct/multiplexed drivers_

- [ ] 1.4 Integrate profiling/resource/database evidence collection and privacy validation
  - Reuse existing diagnostics/pprof and runtime metrics to collect CPU, heap, allocation, mutex, and block evidence; add Go-trace support for explicitly scheduler-sensitive experiments.
  - Add platform-aware sampling for process/system CPU, RSS, live heap, allocation counts/bytes, stack memory, goroutines, and socket/file-descriptor counts where available, with explicit `unavailable` rather than fabricated zero values.
  - Add benchmark/test instrumentation for logical secure-session/store operations, SQL query/transaction/pool-wait observations, billing spool latency/backlog, and outbound connection/handshake/HTTP2 reuse where scenarios require them.
  - Attribute DB reads/writes/queries/transactions to request-start, stream-event, terminal, and cleanup phases and record their authoritative/reusable/best-effort/idempotent classification; collect proxy/connector process-tree goroutine/stack/channel/buffer and scheduler/runnable/blocking evidence.
  - Implement a diagnostic privacy validator/allowlist covering result JSON/console output, labels, logs, metrics, pprof labels/metadata where inspectable, trace task/region/log metadata, and artifact names; keep binary profiles/traces access-controlled.
  - Measure the overhead of any newly always-on production instrumentation before retaining it on a hot path; keep heavyweight instrumentation test-only when appropriate.
  - _Requirements: 1.4–1.14, 2.12–2.17, 16.1–16.8, 19.1–19.4, 19.13–19.14_
  - _Boundary: tests / observability / adapter instrumentation_
  - _Depends: 1.2–1.3_
  - _Validation: `go test ./internal/testkit/perf/...` plus focused metrics/diagnostics/privacy tests_

- [ ] 1.5 Freeze scenario definitions, gate profiles, and benchmark-scratch procedure
  - Populate the Canonical Scenario Registry in `benchmark-scratch.md` with exact primary fingerprints, including logical streams, transport topology/connection plan, held-versus-active shape, and feature/store variants.
  - Add distinct built-in/standard and negotiated executable-plugin HOLD/DELTA fingerprints plus the complete post-#446 DISCONNECT schedule matrix; record which variants are mandatory at each capacity tier and which are bounded sentinels.
  - Populate the Certification Gate Registry before certification use; document the source of each numeric threshold and reject any gate profile whose required release objective is chosen after results are visible.
  - Freeze the applicable fixed-stream, event-rate allocation/GC, per-phase DB-operation, shared-contention, scheduler, and process-tree cleanup envelopes before their certification runs; record `GOMEMLIMIT`/`GOGC` as controlled environment values rather than hidden tuning.
  - Document reproducible commands for baseline/candidate runs, repeated microbenchmarks/`benchstat`, pprof/trace capture, race tests, high-scale driver placement, and gate evaluation.
  - Add comparison validation that rejects uncontrolled scenario/environment/gate differences before an A/B comparison is accepted.
  - Validate privacy across **all** emitted channels: result/scratch/CLI labels, logs, metrics, pprof labels/metadata, trace metadata, and artifact names; no raw prompt/secret/session/user/model identifiers may enter evidence.
  - _Requirements: 1.4–1.14, 2, 15, 16, 18.2, 18.6–18.11, 18.17, 19.1–19.3, 19.13–19.14_
  - _Boundary: tests / docs-in-spec_
  - _Depends: 1.1–1.4_
  - _Validation: perf-tool `describe`/fingerprint/gate/privacy tests_

## Phase 2 — Capture the Untouched Baseline and Rank Real Bottlenecks

- [ ] 2. Record the baseline before production optimization.

- [ ] 2.1 Capture P0 logical-stream, active-delta, body, output, and timeout baselines
  - On a recorded environment and exact post-#446 commit, run `HOLD`, `DELTA`, `BODY`, and `OUTPUT` at 1k logical streams and higher feasible tiers, plus a healthy stream beyond the legacy total-timeout boundary; record actual physical connection counts for each topology.
  - Run equivalent HOLD/DELTA cases through built-in/standard and negotiated executable-plugin execution where supported; record goroutines/stack per logical stream, bounded event-buffer capacity/occupancy where observable, scheduler/trace evidence, event latency, and post-terminal cleanup separately.
  - For executable plugins, record Go-LIP adapter and managed connector process CPU/RSS/heap/stack/goroutine/channel/buffer/alloc/GC data independently and as an aggregate process tree; include current adapter pending-event and usage-evidence bounds rather than measuring only the plugin-side 16-event channel.
  - Run DISCONNECT at launch-in-flight, one and multiple sibling B-legs, graceful acknowledgement, deadline/forced close, and upstream success/failure race points; prove exact terminal/usage/billing evidence and return of goroutines/sockets/buffers/owners to the frozen cleanup band.
  - Separate secure-session disabled/minimal, memory-store recording, and durable-store recording where available so event-cadence cost is attributable.
  - Capture CPU/heap/alloc/mutex/block profiles for active-delta and long-output cases and peak RSS/heap for body bursts; preserve typed correctness/limiter status for every run.
  - Append baseline entries to `benchmark-scratch.md`; mark generator/host-limited tiers `unsupported-by-host`, mark semantic failures invalid, and do not extrapolate.
  - _Requirements: 1, 2, 3.6, 4.7, 5.11–5.12, 7.8–7.10, 15, 17.6–17.7, 18.2–18.3, 19.1–19.3, 19.11, 19.14_
  - _Boundary: tests / evidence_
  - _Depends: 1.1–1.5_
  - _Validation: frozen `lipperf` baseline commands; no production diff required_

- [ ] 2.2 Capture request-start, shared-lock, authority, billing, clone, observer, and transport baselines (P)
  - Run `START`, `B2BUA`, `AUTHZ`, `COMPLETE`, `RETRY`, and `OBSERVE` variants with mutex/block/CPU/alloc/database evidence appropriate to each path.
  - Run existing secure-session recorder, authority contention, and stream-traffic microbenchmarks with repeated samples; add fixed-history/fixed-operation sizes where adaptive benchmark iteration would hide growth.
  - Produce a lifecycle-phase DB ledger for every material START/DELTA/COMPLETE path, a top allocation/escape/copy ledger for request/event/terminal work, and a process-shared state/lock ledger; classify each database operation before proposing cache/async alternatives.
  - Record outbound connection reuse/TLS handshake/HTTP2 observations and direct-listener/front-door topology without changing transport defaults; always pair connection counts with logical-stream counts.
  - Append one baseline experiment entry per candidate family, including typed run validity and explicit `material` / `uncertain` / `not-material-at-baseline` observations without yet modifying production code.
  - _Requirements: 5, 6, 8–15, 19.1, 19.4–19.10, 19.13–19.14_
  - _Boundary: tests / evidence_
  - _Depends: 1.1–1.5_
  - _Validation: named microbenchmarks + frozen `lipperf` scenarios_

- [ ] 2.3 Produce the implementation-time bottleneck ledger
  - Correlate valid baseline profiles with the #394 static findings and rank measured dominant CPU, memory, lock, SQL, and terminal costs while retaining the spec's mandatory P0 correctness work.
  - Record which P2 candidates are sufficiently material to justify production experiments and which currently qualify only for later confirmation/no-change.
  - Record the baseline commit/environment/scenario/gate definitions that every Phase 3–7 experiment derives from; if unrelated production changes land before a task starts, rerun the affected baseline.
  - Reclassify serial sibling cancellation as resolved-by-#446 with regression evidence rather than an implementation target; record executable-plugin per-stream ownership and forced-close joinability as measured baseline facts/limiters.
  - Record fixed cost per stream and request-start/per-event/terminal/cleanup cost slopes, identify which database reads are reusable versus authoritative, and rank measured allocation/GC/shared-state/goroutine multipliers across proxy and connector processes.
  - For each proposed TTL/version cache, async writer, buffer/object pool, or short-job pool, record the smallest non-pooled alternative, the correctness/lifecycle contract, and the evidence required to retain it; absence of materiality closes the candidate as measured-no-change.
  - Do not alter task order to chase microbenchmarks ahead of known P0 fixes; use the ledger to choose alternatives **within** scheduled high-impact work and to prune unproven optional complexity.
  - _Requirements: 1.4–1.14, 18.14, 19.1–19.14_
  - _Boundary: evidence / planning_
  - _Depends: 2.1–2.2_
  - _Validation: completed baseline sections in `benchmark-scratch.md`_

## Phase 3 — P0 Correctness and Ingress Resource Bounds

- [ ] 3. Fix long-stream timeout semantics and request-body admission before persistence/memory micro-optimization.

- [ ] 3.1 Characterize and fix inbound/outbound whole-stream timeout semantics
  - Add RED tests reproducing a healthy stream that outlives the legacy server write/client total timeout while maintaining valid progress, plus caller cancel, idle timeout, header/TTFT timeout, and shutdown controls.
  - Remove/disable the blanket total-duration deadline for the streaming data plane while retaining phase-specific dial/TLS/header/body/TTFT/idle/context/shutdown limits; make zero/disabled configuration semantics effective rather than silently restoring a positive default.
  - Preserve non-streaming-over-stream behavior, error vocabulary, cancellation, and no-retry-after-visible-output invariants.
  - Rerun the exact long-stream baseline and relevant HOLD/DELTA scenarios, append before/after evidence, and keep the fix only in the streaming-safe shape that removes elapsed-duration failure without material regressions.
  - _Requirements: 3.1–3.7, 17.5–17.6, 18.3, 18.7_
  - _Boundary: stdhttp / httpclient config / runtime streaming_
  - _Depends: 2.1–2.3_
  - _Validation: focused timeout tests; targeted `-race`; long-stream `lipperf` A/B_

- [ ] 3.2 Add RED tests for pre-materialization body admission
  - Characterize current auth → `ReadAllLimited` → JSON preflight → decode-admission ordering and reproduce peak materialization under concurrent near-limit bodies.
  - Add tests for known `Content-Length`, chunked/unknown-length, oversized body, invalid JSON, auth rejection, admission exhaustion, cancellation while waiting/reading, and runtime-generation reload sharing.
  - Express the target invariant that full body materialization cannot grow with arbitrary admitted arrivals beyond the process admission/body bounds.
  - Record the baseline BODY experiment and memory profile before production changes if Task 2.1 did not already cover the exact candidate scenario.
  - _Requirements: 4.1–4.8, 17.5–17.7_
  - _Boundary: frontend tests_
  - _Depends: 2.1–2.3_
  - _Validation: focused `frontendpipe`/`reqbody`/`decodeqos` tests_

- [ ] 3.3 Implement staged process-owned materialization/decode admission
  - Evolve the existing process-owned decode-QoS ownership when practical so a count/materialization slot is held before `ReadAll`, actual bytes refine/reserve decode weight, and all partial reservations release exactly once.
  - Keep auth before body admission/read; keep the existing max-body reader and JSON preflight; do not trust `Content-Length` as the only byte bound for chunked requests.
  - Do not hold QoS mutexes across network/body I/O and do not add a second independent public budget unless a measured experiment demonstrates the existing limiter cannot safely express the staged lifecycle.
  - Make all Task 3.2 behavior/cancellation/reload tests GREEN and add architecture/lifecycle assertions if needed to prevent future admission from moving back after materialization.
  - _Requirements: 4.1–4.6, 4.8, 17.2, 17.5–17.9_
  - _Boundary: frontend plugin / process composition_
  - _Depends: 3.2_
  - _Validation: focused tests + targeted `-race`_

- [ ] 3.4 Prove body-memory bound and record the ingress decision
  - Rerun identical BODY bursts across increasing logical-stream/request concurrency/size and record peak RSS/live heap, allocations, admission rejection/wait behavior, latency, and cleanup.
  - Confirm memory is bounded by configured process/body limits rather than client arrival count and that chunked bodies obey the same safety invariant.
  - Compare throughput/tail latency against baseline and revise staged reservation if it introduces avoidable serialization; do not weaken the memory bound to win throughput.
  - Append the final experiment conclusion/claim to `benchmark-scratch.md` before proceeding to event-cadence persistence work.
  - _Requirements: 1.4–1.10, 4.7–4.8, 18.2, 18.15_
  - _Boundary: tests / evidence_
  - _Depends: 3.3_
  - _Validation: BODY A/B matrix + cleanup measurements_

## Phase 4 — P0 Secure-Session Event-Cadence Persistence and Contention

- [ ] 4. Collapse secure-session hot-path amplification before lower-impact lock tuning.

- [ ] 4.1 Instrument and characterize one logical client-facing record operation
  - Add focused store/recorder spies/counters reporting logical store calls and SQL operations without changing production semantics, covering normal delta, usage delta, transcript enabled/disabled, mandatory/best-effort policy, and control-plane decoration.
  - Add concurrency tests pinning sequence monotonicity/order, same-session writers, different-session writers, mandatory pre-release failure, and post-commit failure behavior.
  - Extend recorder microbenchmarks with fixed 100/1k/10k history lengths and concurrent-session cases to expose O(n) per-append growth and global lock contention.
  - Record baseline operation/query counts, event latency, CPU/allocations, mutex/block profiles, and retained session/history memory in the scratchpad.
  - _Requirements: 5.1–5.12, 6.3, 16.6, 17.5–17.6_
  - _Boundary: secure-session tests / store instrumentation_
  - _Depends: 3.1–3.4_
  - _Validation: secure-session app/store tests + fixed recorder benchmarks_

- [ ] 4.2 Replace `Next*Seq` + append orchestration with one logical stream-event mutation
  - Add a narrow consumer-owned secure-session store operation accepting one stream-event mutation (activity, optional transcript, mandatory audit, optional usage) and returning assigned sequence references after authoritative persistence.
  - Move sequence allocation inside the adapter mutation/transaction and remove hot-path application calls that separately request next transcript/audit sequence before append.
  - Keep current record schemas/query contracts where needed outside the hot use case, but do not maintain dual hot-path writes or two authorities for sequence assignment.
  - Make mandatory durability, ordering, usage, transcript policy, and control-plane characterization tests GREEN before measuring performance.
  - _Requirements: 5.1, 5.5, 5.7, 6.3–6.5, 17.3–17.5_
  - _Boundary: secure-session app orchestration / store port_
  - _Depends: 4.1_
  - _Validation: secure-session app/store contract tests + targeted `-race`_

- [ ] 4.3 Make in-memory secure-session sequence/mutation work O(1) and session-local
  - Replace transcript/audit maximum scans with per-session next counters maintained under session-entry synchronization and initialized correctly for loaded/test fixtures.
  - Narrow top-level map locking to index/lifetime operations and move transcript/audit/usage/activity mutation to per-session or measured shard ownership so unrelated sessions do not serialize behind one global write lock.
  - Define safe entry lifetime/eviction semantics so a session cannot be removed while an in-flight operation uses it; document lock order and keep external/JSON work outside index locks.
  - Rerun fixed-history/concurrent-session benchmarks; append A/B latency/CPU/alloc/mutex evidence and revise/revert over-complex sharding if a simpler lock-scope change performs equivalently.
  - _Requirements: 5.2–5.3, 5.10–5.12, 17.6_
  - _Boundary: secure-session memory driven adapter_
  - _Depends: 4.2_
  - _Validation: memory-store tests + `-race` + fixed-history/contended benchmarks_

- [ ] 4.4 Consolidate durable SQLite/PostgreSQL stream-event writes
  - Implement the logical mutation in Bun adapters with sequence allocation inside the same minimum required transaction; eliminate redundant existence/`MAX(seq)` round trips already repeated by append.
  - Fold activity/parent-row update into the authoritative mutation when that preserves current expiry/locking semantics and avoids a distinct `TouchActivity` operation.
  - Preserve dual-dialect uniqueness/order, transaction-pooler rules, failure atomicity, and mandatory acknowledgement; add concurrent-writer SQLite/PostgreSQL integration tests.
  - Rerun durable recorder benchmarks and record per-event SQL query/transaction count, pool waits, event latency, CPU/allocations, and DB QPS before/after.
  - _Requirements: 5.1, 5.4–5.5, 5.7, 5.11, 6.3–6.6, 17.5–17.6_
  - _Boundary: secure-session Bun driven adapters_
  - _Depends: 4.2_
  - _Validation: SQLite tests + PostgreSQL integration tests + durable recorder A/B_

- [ ] 4.5 Remove avoidable per-delta activity/control-plane amplification
  - Measure whether the consolidated authoritative mutation already makes separate activity writes unnecessary; if not, implement the smallest safe bounded coalescing/terminal-flush policy with explicit expiry semantics and restart/crash characterization.
  - Audit control-plane secure-session decorators so best-effort activity/audit/usage projection does not multiply mandatory durable work on every delta; preserve any explicitly mandatory projection contract.
  - If best-effort projection remains a material synchronous bottleneck, compare synchronous versus bounded batch/queue delivery with explicit capacity/full/shutdown metrics and retain async delivery only when measured benefit justifies it.
  - Append each attempted alternative—including no-change/reverted alternatives—to the scratchpad with event-latency, QPS, queue/backpressure, correctness, and cleanup evidence.
  - _Requirements: 5.4, 5.6, 5.9, 16.5, 17.2, 17.7_
  - _Boundary: secure-session app / control-plane observer_
  - _Depends: 4.3–4.4_
  - _Validation: policy/expiry/projection tests + active-delta A/B_

- [ ] 4.6 Add bounded secure-session memory retention and expiry cleanup
  - Characterize current session/transcript/audit retention after turn/session expiry and reproduce any monotonic memory growth using controlled completed-session churn.
  - Implement bounded/amortized expiry/reclamation for memory-store entries/history without full-store request-path sweeps or unsafe deletion of in-use entries; choose the simplest measured cleanup design.
  - Add fake-clock tests for TTL, active-session protection, terminal cleanup, max-entry behavior, and high-cardinality expiry plus targeted race/goleak coverage if a maintenance goroutine is introduced.
  - Rerun churn/disconnect/soak-style memory tests and append live-entry/RSS/heap/cleanup/lock evidence; revert any background cleanup mechanism if bounded opportunistic cleanup proves simpler/equivalent.
  - _Requirements: 5.10–5.12, 17.6–17.7, 18.4, 18.15_
  - _Boundary: secure-session memory driven adapter_
  - _Depends: 4.3_
  - _Validation: fake-clock/race tests + retention benchmark_

- [ ] 4.7 Decide whether mandatory durable event acknowledgement needs a second-stage architecture
  - From the post-4.4/4.5 DELTA durable profile, determine whether the minimum mandatory remote transaction is still the dominant limiter at target active-stream rates; record measured-no-change if it is not.
  - If dominant, implement benchmark prototypes for at least one durability-preserving alternative (for example local durable WAL/spool acknowledgement and/or tightly bounded durable microbatch) without changing client-release durability semantics.
  - Compare event latency, throughput, fsync/database load, crash/replay correctness, ordering, bounded backlog, recovery time, and operational complexity against the simple consolidated transaction.
  - Keep only an alternative with clear measured benefit and equivalent fail-closed durability; otherwise delete prototype production code and retain negative evidence.
  - _Requirements: 5.6–5.8, 5.11, 16.5, 18.2, 18.14_
  - _Boundary: secure-session persistence / evidence-gated optional durable adapter_
  - _Depends: 4.4–4.5_
  - _Validation: crash/replay tests + durable DELTA A/B + backlog bounds_

- [ ] 4.8 Re-run P0 secure-session active-delta gates
  - Run memory-store and durable-store DELTA scenarios at 1k logical streams then higher feasible tiers using the same workload/topology as baseline and capture event-forward latency, CPU, allocations/GC, mutex/block, SQL/store ops, retained memory, and physical connection counts.
  - Verify per-event sequence work is history-independent, unrelated sessions no longer serialize on a store-wide mutation lock, and completed/expired memory-store histories reclaim.
  - Record remaining dominant secure-session cost and explicitly state whether it is CPU, JSON serialization, durable acknowledgement, database pool/latency, or another measured component.
  - Close the phase only with valid scratch evidence; invalid/unsupported runs remain recorded and do not count as proof.
  - _Requirements: 5, 18.2, 18.12–18.15_
  - _Boundary: tests / evidence_
  - _Depends: 4.3–4.7_
  - _Validation: DELTA memory/durable A/B matrix_

## Phase 5 — P0 Bounded Per-Stream State and Terminal Allocation

- [ ] 5. Make retained memory depend on bounded in-flight state rather than cumulative delivered output.

- [ ] 5.1 Reconcile active recv/response ownership and inventory every retained buffer
  - Inspect the implementation state of `turn-recv-terminal-ownership-simplification`; map current owners of `seenEvents`, visible/customer text, reasoning, tool args, provider usage, completion-gate/drain, recovery-drain, pending-wire, interleaved, and observer state.
  - Add a deterministic inventory/test fixture recording each retained buffer's semantic consumer, lifetime, and byte/event bound (or current lack of one).
  - Run OUTPUT scenarios sweeping event count/output bytes and logical-stream concurrency, capturing live-heap slope and terminalization allocation spikes before changing representation.
  - Append the exact baseline and identify which copies are semantically redundant versus required by tokenization/observer/tool contracts.
  - _Requirements: 7.1–7.10, 17.3, 18.15_
  - _Boundary: runtime/response pipeline tests / evidence_
  - _Depends: 4.8_
  - _Validation: focused runtime/token-accounting tests + OUTPUT baseline_

- [ ] 5.2 Eliminate duplicate full event histories and terminal full-slice copies
  - Introduce one canonical provider-usage evidence collector so usage events do not require retaining every non-usage client event to terminalization.
  - Remove redundant `seenEventsCopy`/full-slice filtering and duplicate customer-content event storage where no consumer contract requires replay, preserving event delivery/observer ordering.
  - Keep at most one full output-text representation temporarily where current local token counting still requires it; do not claim bounded cumulative memory yet if this fallback remains.
  - Rerun OUTPUT micro/end-to-end scenarios and append memory/alloc/GC/terminal-spike evidence before continuing to tokenizer/evidence strategy work.
  - _Requirements: 7.1–7.6, 7.8, 17.3–17.5_
  - _Boundary: runtime response owner / token accounting_
  - _Depends: 5.1_
  - _Validation: runtime/tokenaccounting tests + OUTPUT A/B_

- [ ] 5.3 Benchmark exact compact output-counting alternatives before choosing one
  - Add correctness probes comparing existing terminal `CountOutput(Text, Events)` results against candidate exact streaming/incremental counter behavior across adversarial chunk boundaries, UTF-8, reasoning/tool content, and model/tokenizer variants.
  - Where an exact incremental tokenizer capability is not practical, benchmark a bounded replay/spill strategy against retaining one full in-memory text builder, including high-concurrency disk/memory/latency effects.
  - Measure state bytes per stream, event-path CPU/allocations, terminal latency, and tokenizer equality for each candidate; reject any option that silently changes usage authority/accuracy semantics.
  - Record the selected strategy or explicit bounded-fallback design in the scratchpad; do not choose based on expected tokenizer behavior.
  - _Requirements: 7.1–7.5, 7.8–7.9, 12.2, 17.5_
  - _Boundary: token accounting / tokenizer adapters / evidence_
  - _Depends: 5.2_
  - _Validation: tokenizer property/golden tests + candidate microbenchmarks_

- [ ] 5.4 Implement the selected compact `UsageEvidenceAccumulator`
  - Add a private streaming output-counter/evidence capability at the consumer boundary and adapt local tokenizers/providers only as needed, keeping provider-specific logic outside core runtime.
  - Feed provider usage deltas and output-count state incrementally as client-visible events pass; terminal reconciliation consumes compact state instead of lifetime event slice/full duplicate text forms.
  - Preserve provider/operator versus client-visible usage planes, fallback/warning behavior, billing/metering evidence, cancellation, and synthesized-usage ordering.
  - Make equivalence tests GREEN and rerun the exact OUTPUT candidate benchmark; append before/after evidence and revert any unproven complex strategy.
  - _Requirements: 7.1–7.6, 7.8–7.10, 17.1, 17.4–17.6_
  - _Boundary: runtime / tokenaccounting / tokenizer driven adapters_
  - _Depends: 5.3_
  - _Validation: tokenaccounting/runtime tests + targeted `-race` + OUTPUT A/B_

- [ ] 5.5 Bound completion-gate, recovery, tool, pending-wire, and remaining response buffers
  - For every buffer from Task 5.1 not eliminated by compact usage evidence, identify its existing protocol/config limit or add the narrowest explicit byte/event bound and safe overflow/error behavior.
  - Preserve completion-gate buffering/draining, recovery legality, tool-call argument/finalizer limits, interleaved-thinking state, frontend wire ordering, and final-observer semantics.
  - Add RED→GREEN boundary tests for each newly explicit cap and ensure no cap is an arbitrary low product regression; prefer existing protocol/body/tool limits where they already define a maximum.
  - Rerun stress cases targeting each bound and record peak live bytes/event latency; no unbounded queue may remain merely because normal outputs are small.
  - _Requirements: 7.3–7.7, 7.9–7.10, 17.5–17.9_
  - _Boundary: runtime response owner / frontend stream helpers_
  - _Depends: 5.1, 5.4_
  - _Validation: focused buffer-limit tests + targeted stress scenarios_

- [ ] 5.6 Prove bounded stream memory and cleanup at concurrency
  - Repeat OUTPUT sweeps at 1k logical streams and higher feasible tiers and report live-heap slope versus cumulative delivered output after steady in-flight buffers reach their bounds.
  - Capture GC CPU/pause, allocations, event forwarding tail latency, terminal peak allocation, and post-disconnect reclamation; compare to Phase 2/5.1 baselines.
  - Confirm no terminal path recreates a full event-history copy and no completed stream retains customer/usage evidence beyond bounded terminal work.
  - Append final Phase-5 evidence/claim and identify any remaining semantically required per-stream full materialization with its explicit bound/cost.
  - _Requirements: 7.8–7.10, 18.4, 18.12, 18.15_
  - _Boundary: tests / evidence_
  - _Depends: 5.2–5.5_
  - _Validation: OUTPUT + DISCONNECT A/B, heap/alloc profiles_

## Phase 6 — P1 Start/Shared-State/Persistence Bottlenecks

- [ ] 6. Remove measured process-wide contention and redundant terminal/start persistence in impact order.

- [ ] 6.1 Remove avoidable secure-session/B2BUA start-and-finish query chatter
  - Record/confirm START and COMPLETE baselines with logical store/SQL query counts, pool waits, start/TTFT/finish latency, and current create→reload/fetch patterns.
  - Return/reuse authoritative rows/A-leg values already produced by create/load orchestration and remove immediate redundant reads; keep readiness/resume/route/lineage semantics intact.
  - Consolidate terminal audit sequence/append through the new stream-event/store mutation where applicable and avoid introducing caches until repeated stable reads justify them.
  - Rerun identical bursts and append query-count/latency/CPU evidence; revert any ownership shortcut that changes durable/readiness semantics without material gain.
  - _Requirements: 6.1–6.7, 17.3–17.6_
  - _Boundary: secure-session app / B2BUA orchestration / stores_
  - _Depends: 4.2–4.8, 5.6_
  - _Validation: lifecycle tests + SQLite/PostgreSQL integration + START/COMPLETE A/B_

- [ ] 6.2 Remove process-global auth sink serialization
  - Use the Phase-2 START/mutex profile or refresh it on the current commit; add scheduling tests that pin only ordering actually required by auth/session-start contracts.
  - Move serialization, if required, to the narrow sink/session scope and ensure dispatcher locks are not held while calling logging/control-plane/external sink code.
  - Do not substitute an unbounded queue; if a bounded asynchronous sink is proposed, benchmark it separately and preserve failure/order policy.
  - Rerun default-log and control-plane/slow-sink START scenarios, append throughput/tail/mutex evidence, and close measured-no-change if global-lock removal is not material beyond correctness/architecture simplification.
  - _Requirements: 8.1–8.7, 16.5, 17.2, 17.6–17.7_
  - _Boundary: core auth / sink adapter_
  - _Depends: 5.6_
  - _Validation: auth event tests + `-race` + START A/B_

- [ ] 6.3 Scale B2BUA memory continuity by A-leg
  - Refresh baselines for many independent A-legs, one hot A-leg, mixed resolve/fetch/attempt/update operations, and high-cardinality expiry; include mutex/block/CPU/alloc/live-entry evidence.
  - Narrow index lock scope and move mutable per-A-leg state/activity behind per-entry or measured shard ownership; separate activity refresh from unrelated global mutation where safe.
  - Replace unbounded create-time full-store expiry sweeps with bounded/amortized cleanup selected by benchmark while preserving TTL/max-leg/override/interleaved/attempt invariants.
  - Run race/fake-clock tests and identical benchmarks; append evidence and retain the simplest design that materially removes contention/expiry stalls.
  - _Requirements: 9.1–9.7, 17.3, 17.6_
  - _Boundary: core B2BUA memory store_
  - _Depends: 5.6_
  - _Validation: B2BUA tests + `-race` + B2BUA A/B matrix_

- [ ] 6.4 Scale concurrency-authority memory leases by rule/dimension
  - Extend the existing 100-contender benchmark to fixed 1k and highest practical same-dimension plus many-dimension scenarios; measure live/tombstone growth and acquire/release mutex cost.
  - Replace full historical scans with per-dimension live state/counters and bounded expiry/tombstone cleanup, starting with the smallest concrete design and benchmarking more complex expiry indexes only if needed.
  - Preserve exact capacity, replay/idempotency, release, expiry, query/reporting, and fail-closed semantics under concurrent races.
  - Rerun fixed benchmarks/load scenarios and append latency/throughput/CPU/alloc/mutex/live-state evidence; keep a simple/measured-no-change design if higher complexity is not justified.
  - _Requirements: 10.1–10.7, 17.6_
  - _Boundary: concurrency-authority memory driven adapter_
  - _Depends: 5.6_
  - _Validation: authority tests + `-race` + contention benchmarks_

- [ ] 6.5 Remove per-append aggregate scans from the durable billing spool
  - Refresh COMPLETE baseline and characterize replay lookup, pending `COUNT/SUM`, capacity/disk checks, insert/commit, DB busy/lock time, and crash/reopen behavior.
  - Maintain pending record/byte totals transactionally or with safe persisted/reconciled metadata so one append does not aggregate all pending rows; preserve caps, replay, WAL, `synchronous=FULL`, free-disk, and single-writer simplicity initially.
  - Add crash/reopen/mismatch reconciliation tests proving counters cannot undercount pending durable rows; do not change writer count/batching/ack semantics in this first optimization.
  - Rerun completion bursts and append SQL/latency/throughput/CPU/backlog evidence; retain one writer if adequate after aggregate removal.
  - _Requirements: 11.1–11.7, 17.4–17.6_
  - _Boundary: billing spool driven adapter_
  - _Depends: 5.6_
  - _Validation: spool crash/replay/cap tests + COMPLETE A/B_

- [ ] 6.6 Benchmark any second-stage billing writer optimization only if still dominant
  - If post-6.5 COMPLETE profiles show the durable writer remains a material limiter, define a new scratch hypothesis and benchmark bounded batching/transaction grouping/connection alternatives without changing acknowledgement semantics.
  - Add crash/durability/order/idempotency tests for each candidate before throughput comparison.
  - Keep only a candidate materially improving the measured completion bottleneck without weakening bounds/durability; otherwise revert it and retain negative evidence.
  - If the writer is no longer material, close measured-no-change with no production modification.
  - _Requirements: 11.3–11.7, 18.14_
  - _Boundary: billing spool / evidence-gated optional optimization_
  - _Depends: 6.5_
  - _Validation: COMPLETE A/B + crash/recovery suite_

- [ ] 6.7 Classify and remove remaining material request-path database amplification
  - Refresh the Phase-2 lifecycle DB ledger after Tasks 4.4, 6.1, and 6.5; rank remaining request-start, stream-event, terminal, and cleanup reads/writes by query/transaction count, latency, pool wait, and correctness/freshness class.
  - For repeated stable reads, first reuse the authoritative value already loaded by orchestration or project an immutable/versioned value into the existing generation/process owner. Consider a bounded version/TTL cache only when staleness is explicitly valid and tests cover identity, freshness, invalidation, entry/byte bounds, expiry, duplicate-miss collapse, origin failure, and close.
  - Do not cache transactional authority for authentication, secure-session mandatory state, concurrency/exposure admission, billing, or settlement. Consolidate redundant authoritative calls into the minimum owning transaction instead of serving stale state.
  - Move writes off the caller only for existing best-effort projections or after a separate durability-preserving local-ack experiment; define bounded overload/retry/crash/restart/drain/shutdown behavior and never acknowledge mandatory work from volatile memory.
  - Rerun identical START/DELTA/COMPLETE scenarios and retain only a material end-to-end/query-count improvement without freshness, durability, memory, contention, or tail regression; otherwise record measured-no-change/revert per operation.
  - _Requirements: 5.5–5.9, 6.1–6.7, 11.2–11.7, 16.5–16.8, 19.4–19.7, 19.10, 19.14_
  - _Boundary: owning app/store/composition seams; no generic cache layer_
  - _Depends: 4.4, 6.1, 6.5–6.6_
  - _Validation: focused freshness/durability/cache tests where selected + SQLite/PostgreSQL + START/DELTA/COMPLETE A/B_

## Phase 7 — P2 Allocation, Observation, Residual Locks, and Transport (Profile-Gated)

- [ ] 7. Address every lower-priority audit candidate, but only ship measured improvements.

- [ ] 7.1 Measure and, if material, reduce `lipapi.Call` deep-clone amplification (P)
  - Run repeated micro/end-to-end RETRY cases for small calls, large histories, large tools/extensions, and 0/1/N pre-output retries; record `ns/op`, `B/op`, `allocs/op`, heap/GC, request-start/TTFT, and CPU profile share.
  - If material, first eliminate redundant clones/ownership handoffs while preserving the immutable logical baseline; introduce COW/structural sharing only after a separate benchmark proves simpler elimination insufficient.
  - Add mutation-isolation tests across caller input, baseline, hooks/transforms, and retry attempts before retaining shared representation.
  - Rerun identical benchmarks and append keep/revert/no-change evidence; no clone refactor is required if post-P0 profiles show immaterial cost.
  - _Requirements: 12.1–12.6, 17.1, 17.6, 18.14_
  - _Boundary: `pkg/lipapi` / runtime request preparation_
  - _Depends: 6.1–6.6_
  - _Validation: clone/runtime tests + RETRY A/B + repeated microbench/benchstat_

- [ ] 7.2 Make disabled traffic observation a true fast path when measured (P)
  - Re-run existing stream-traffic benchmarks and OBSERVE scenarios for no consumer, fast sink, redaction chain, and slow sink; record serialization/redaction CPU and allocation share.
  - If no-consumer overhead is material, add a cheap immutable/capability guard before JSON serialization/body/header copies while preserving observer registration/reload semantics.
  - Keep redaction before payload exposure and preserve raw/observer/failure/order contracts; add tests proving disabled mode performs no avoidable payload construction.
  - Rerun identical cases and append A/B evidence; close measured-no-change if the fast path is already negligible after prior refactors.
  - _Requirements: 13.1–13.2, 13.5–13.7, 16.3–16.4, 17.5–17.6_
  - _Boundary: traffic SDK/runtime emission_
  - _Depends: 6.1–6.6_
  - _Validation: traffic tests + existing traffic benchmark + OBSERVE A/B_

- [ ] 7.3 Benchmark bounded asynchronous traffic delivery only if synchronous sinks remain material
  - If post-7.2 OBSERVE/DELTA profiles show synchronous best-effort sink latency remains material, add a scratch experiment comparing current synchronous delivery with one bounded queue/batcher candidate.
  - Define queue capacity, full/backpressure/drop/coalesce policy, shutdown/drain ownership, and metrics before implementation; mandatory observers must not silently drop.
  - Add slow-sink/full-queue/cancel/shutdown/goleak tests and measure event latency, CPU, allocations, depth/full behavior, dropped/coalesced counts where allowed, and memory bound.
  - Retain async delivery only with clear measured benefit and policy equivalence; otherwise revert and record. If synchronous delivery is non-material, complete measured-no-change.
  - _Requirements: 13.2–13.7, 16.5, 17.2, 17.7, 18.14_
  - _Boundary: traffic sink adapter / optional queue owner_
  - _Depends: 7.2_
  - _Validation: slow-sink/queue tests + OBSERVE/DELTA A/B_

- [ ] 7.4 Measure executor lifecycle-coordinator and RNG shared locks (P)
  - Use START/routing contention microbenchmarks and mutex profiles to quantify lifecycle-coordinator lookup lock and shared `lockedRng` impact at high request-start rates.
  - If material, pre-wire/use `sync.Once`/immutable coordinator access and the narrowest concurrency-safe RNG ownership preserving deterministic injected-RNG tests and routing distributions.
  - Do not change routing selection semantics or add per-request random initialization cost without measuring it.
  - Append A/B or measured-no-change evidence for **each** lock separately.
  - _Requirements: 14.1–14.6, 17.6, 18.14_
  - _Boundary: core runtime/routing support_
  - _Depends: 6.1–6.6_
  - _Validation: routing/runtime tests + `-race` + START microbench_

- [ ] 7.5 Measure credential-pool lock/scan cost and optimize only if material (P)
  - Benchmark representative and intentionally large credential pools under concurrent acquire/acquire-by-ID/cooldown updates; record mutex profile, scan CPU, allocations, and selection latency.
  - If material, reduce lock scope or separate immutable credential metadata from per-entry mutable availability/cooldown state using the smallest safe design.
  - Preserve credential ordering/selection, cooldown/rate-limit marking, copying/secret isolation, and deterministic tests.
  - Append A/B or measured-no-change evidence; do not introduce sharding/atomics for normal tiny pools without demonstrated benefit.
  - _Requirements: 14.1–14.5, 18.14_
  - _Boundary: credential pool_
  - _Depends: 6.1–6.6_
  - _Validation: credpool tests + `-race` + contention benchmark_

- [ ] 7.6 Measure generic state, affinity, and leg-lifecycle top-level locks (P)
  - Benchmark generic memory-state Get/Set with encode/decode payloads, affinity Get/Set/Delete, and high-churn post-#446 leg-lifecycle Start/launch-permit/Cancel/End separately; collect mutex/block/CPU/alloc/scheduler profiles.
  - Preserve parallel sibling cancellation as a positive-control regression gate and compare one/many B-leg cancellation latency without reverting to serial teardown or moving cancel/close work under a shared lock.
  - Where generic state decode work under lock is material, copy/stabilize the entry under lock then decode outside it; where affinity/lifecycle locks are short/non-material, leave them unchanged.
  - If a store truly contends, apply per-key/shard ownership without moving external work under locks and preserve TTL/affinity/lifecycle exactly-once semantics.
  - Record separate A/B or measured-no-change conclusions for state, affinity, and lifecycle rather than one aggregate “locks optimized” claim.
  - _Requirements: 14.1–14.6, 18.14_
  - _Boundary: core state / affinity / leg lifecycle_
  - _Depends: 6.1–6.6_
  - _Validation: package tests + targeted `-race` + separate lock benchmarks_

- [ ] 7.7 Measure logical-stream/connection topology and tune HTTP transport only if evidence proves a need (P)
  - Run HOLD/START/DELTA with the same logical-stream targets against direct HTTP/1.1 ingress and supported externally terminated/multiplexed topology where available; record target/achieved logical streams separately from client/front-door, terminator/proxy, proxy-ingress, and outbound connection/socket counts.
  - Record streams-per-connection, OS open-file/socket/backlog prerequisites, generator capacity/headroom, outbound active/idle connections, TLS handshakes, HTTP2 reuse, CPU, and latency; add diagnostics/tool output rather than silently changing host sysctls/limits.
  - Change idle-pool/keepalive/transport defaults only when repeated evidence shows connection/TLS churn or starvation is material, and verify no stale/idle-resource regression.
  - Append topology-specific A/B or measured-no-change evidence and retain the existing shared transport/no active cap unless a measured requirement says otherwise.
  - _Requirements: 2.11, 2.16–2.17, 15.1–15.9, 18.5, 18.12–18.13_
  - _Boundary: stdhttp / httpclient / perf tooling_
  - _Depends: 6.1–6.6_
  - _Validation: HOLD/START/DELTA topology A/B + stream/connection accounting tests_

- [ ] 7.8 Measure event-path allocation/GC cost and retain only bounded safe reuse (P)
  - Use DELTA/OUTPUT plus focused codec/adapter benchmarks to rank `alloc_space`, allocation objects/bytes per event, conversion copies, temporary JSON/byte buffers, GC CPU/pause, and terminal allocation peaks across built-in and executable-plugin paths.
  - For each material site, compare the smallest applicable alternatives in order: remove redundant ownership/copy, preallocate known capacity, process incrementally/stream, then a narrow buffer/object reuse candidate. Do not add a general pooling package.
  - If reuse is tested, define capacity classes/max retained capacity, discard oversized outliers, fully reset state, zero sensitive backing storage where required, prevent cross-session observability, and do not treat `sync.Pool` as a memory bound.
  - Rerun identical micro/end-to-end loads and retain reuse only when allocation/GC and relevant tail/resource metrics improve materially without privacy, memory-retention, contention, or correctness regression; otherwise revert/record measured-no-change.
  - _Requirements: 2.2, 2.12–2.13, 7.8–7.10, 12.1–12.6, 16.8, 19.1, 19.8–19.9, 19.13–19.14_
  - _Boundary: measured owning codec/adapter/runtime package only_
  - _Depends: 6.1–6.7_
  - _Validation: focused `-benchmem`/benchstat + alloc/heap profiles + DELTA/OUTPUT A/B + cross-session privacy/retention tests when reuse is selected_

- [ ] 7.9 Measure and minimize fixed per-stream goroutine/channel topology without hiding lifecycle work (P)
  - At fixed HOLD/DELTA tiers, derive proxy-side, connector-side, and aggregate goroutines, stack bytes, channels/buffer capacity/occupancy, runnable/blocked scheduler time, event latency, and cleanup per logical stream; verify the count is independent of event count and identify each owner.
  - Compare current topology first with removing/fusing only a proved redundant owner or buffer. Do not pool blocking `Recv`, ordered send, `Cancel`, `Close`, or sibling teardown merely to lower goroutine count; preserve #446 launch/cancel/terminal ordering and parallel sibling cleanup.
  - If a distinct short independent CPU/I/O job remains a material spawn/scheduler cost, test one bounded worker candidate with explicit concurrency, queue/full/fairness/cancellation/panic/error/shutdown contracts and no work admitted before ownership; reject pool-wide starvation under blocked/adversarial jobs.
  - Retain a topology/pool change only with equivalent cancellation/disconnect/race behavior, lower aggregate process-tree cost, no tail/backpressure regression, and full `goleak`/post-cleanup recovery; otherwise record measured-no-change.
  - _Requirements: 2.18–2.20, 14.6, 17.2, 17.6–17.7, 19.1–19.3, 19.10–19.14_
  - _Boundary: existing stream/adapter/lifecycle owner; no generic scheduler_
  - _Depends: 6.1–6.7_
  - _Validation: HOLD/DELTA/DISCONNECT A/B + Go trace/runtime metrics + targeted `-race`/goleak_

- [ ] 7.10 Re-profile after all retained P2 work and prevent accidental rewrites of known-good primitives
  - Capture fresh CPU/heap/alloc/mutex/block profiles on representative START/DELTA/OUTPUT loads and compare top contributors with the Phase-2 bottleneck ledger.
  - Explicitly verify generation refcount CAS, model-catalog atomic reads, per-stream EventPump synchronization, and existing per-A-leg lifecycle structure remain unchanged unless one was separately proven material.
  - Add/retain architecture tests preventing reintroduction of global external-sink I/O locks, unbounded response histories, and separate hot-path next-sequence calls where practical.
  - Record remaining dominant costs and any audited candidate intentionally left unchanged due to measured non-materiality.
  - _Requirements: 14.6, 17.1–17.10, 18.12, 18.14, 19.1–19.14_
  - _Boundary: architecture tests / evidence_
  - _Depends: 7.1–7.9_
  - _Validation: profiles + `go test ./internal/archtest/...`_

## Phase 8 — Cross-Cutting Correctness, Database, Privacy, and Resource-Reclamation Gates

- [ ] 8. Prove that performance work preserved product semantics, diagnostic privacy, and resource ownership.

- [ ] 8.1 Run cross-domain race and goroutine ownership gates
  - Run targeted `-race` on secure-session stores, frontend admission, runtime response/accounting state, auth events, B2BUA, concurrency authority, billing spool, and any new queue/batcher owners.
  - Add/verify `goleak` TestMain or focused leak checks in every package newly owning goroutines; prove cancellation/shutdown drains or abandons work according to policy without hanging.
  - For every retained cache/reuse/worker mechanism, run deterministic expiry/invalidation/final-release/Close schedules plus queue-full/fairness and cross-session reset/privacy cases as applicable; prove one logical owner and no I/O/cleanup while holding a central shared lock.
  - Run the full post-#446 DISCONNECT matrix across built-in/standard and negotiated executable-plugin paths: launch-in-flight, one/many siblings, graceful acknowledgement, deadline/forced close, upstream-terminal races, a compliant fake whose `Cancel` returns only after `Close`, and a subprocess-isolated negative fake that remains unjoinable after `Close`.
  - Measure post-run goroutines, sockets/FDs, heap, bounded stream buffers, launch/attempt owners, admission reservations, per-session state, lease state, and spool/observer queues; a non-joinable cancel or detached worker fails the gate.
  - Record/fix cleanup regression before capacity certification; a benchmark with leaked resources is invalid regardless of throughput.
  - _Requirements: 2.10, 2.18–2.20, 17.6–17.7, 18.2, 18.15, 19.2, 19.5–19.12_
  - _Boundary: cross-cutting tests_
  - _Depends: 3.1–7.10_
  - _Validation: targeted `go test -race ...`, goleak suites, DISCONNECT scenario_

- [ ] 8.2 Run dual-dialect persistence and pooler gates
  - Execute SQLite secure-session/billing suites and configured PostgreSQL integration suites for concurrent stream-event append, sequence uniqueness/order, lifecycle query consolidation, and billing metadata reconciliation.
  - When transaction-pooler testing is configured, run the PgBouncer gate and verify no session GUC/temp/prepared/advisory-lock behavior was introduced.
  - Run durable DELTA/COMPLETE scenarios with DB query/pool metrics and confirm optimization does not merely shift latency into pool waits/transactions.
  - Verify the final lifecycle DB ledger contains no unclassified material operation and that any retained cache/local durable acknowledgement/async projection preserves its declared freshness, durability, restart, and overload contract in both dialects where applicable.
  - Append any environment-specific durability/performance differences affecting final certification scope.
  - _Requirements: 5.5–5.8, 6.5, 11.2–11.6, 17.5–17.6, 18.2, 18.7, 19.4–19.7_
  - _Boundary: persistence integration tests / evidence_
  - _Depends: 4.4–4.7, 6.1, 6.5–6.7_
  - _Validation: SQLite + PostgreSQL + pooler integration commands_

- [ ] 8.3 Reconcile adjacent specs, diagnostics privacy, and architecture gates
  - Re-read active/implemented recv-terminal and request-attempt simplification specs; ensure bounded evidence/admission/persistence changes live at current authoritative owners with no duplicated compatibility state.
  - Run deterministic architecture/change-surface checks and add targeted guards for new seams/bounds without encoding fragile file names where ownership is intentionally moving.
  - Add architecture ratchets against request/event-path DB loops that bypass the classified owner, unbounded process-global caches/queues, per-event goroutine spawns, lifecycle-bearing generic pools, and pooled sensitive/oversized buffers where a stable structural check is practical.
  - Run the complete diagnostic privacy validation over representative result JSON/CLI/log/metrics/profile-label/trace-metadata/artifact-name output and reject evidence artifacts containing forbidden identifiers.
  - Run `make quality-checks`, focused unit suites, and parity/contract tests affected by frontend/runtime/usage/store changes; fix regression before freezing the certification commit.
  - _Requirements: 16.1–16.8, 17.1–17.10, 18.18, 19.4–19.13_
  - _Boundary: architecture / privacy / cross-spec integration_
  - _Depends: 8.1–8.2_
  - _Validation: `make quality-checks`, `go test ./internal/archtest/...`, perf privacy tests, relevant parity suites_

## Phase 9 — Final 1k → 5k → 10k Logical-Stream Certification and Soak

- [ ] 9. Re-run frozen workloads and pre-frozen gates unchanged and state exactly what is proven.

- [ ] 9.1 Certify the 1,000-logical-stream tier
  - Freeze/verify applicable gate-profile IDs **before** the evaluated runs, then run frozen HOLD, DELTA secure-memory, DELTA secure-durable, START, BODY, OUTPUT, COMPLETE, AUTHZ, OBSERVE, and DISCONNECT scenarios at 1k logical streams where applicable.
  - Include built-in/standard and negotiated executable-plugin HOLD/DELTA variants plus the full post-#446 DISCONNECT contract matrix where executable plugins are supported; report their per-stream owner/buffer/scheduler and cleanup results separately.
  - Record target/achieved logical streams, actual physical connection counts by topology/leg, p50/p95/p99 latency, throughput/expected-vs-unexpected outcomes, CPU/RSS/heap/alloc/GC/goroutines/sockets plus DB/queue metrics and dominant profiles; split proxy, connector-child, and aggregate process-tree resources.
  - Evaluate the frozen fixed-stream, request-start, per-event, terminal, cleanup, DB-operation, scheduler/GC, shared-contention, and resource-headroom gates and record every retained/rejected cache/async/reuse/topology mechanism.
  - Require valid correctness/durability results and evaluate every required scenario gate; do not count invalid-correctness or environment-limited runs as PASS.
  - Populate the 1k certification record with `GO`, `NO-GO (proxy-limited)`, `NO-GO (environment-limited)`, or `NO-GO (gate-definition-incomplete)` plus exact feature/store/topology scope and failed/unsupported gates.
  - _Requirements: 2.18–2.20, 18.1–18.15, 19.1–19.14_
  - _Boundary: performance certification_
  - _Depends: 8.1–8.3_
  - _Validation: full frozen 1k logical-stream scenario/gate matrix_

- [ ] 9.2 Certify the 5,000-logical-stream tier
  - Escalate the same frozen relevant scenarios/gate profiles to 5k logical streams without weakening cadence/output/features; separate idle-held from actively emitting results.
  - Capture physical connections/multiplexing plus host/generator/OS/database utilization so a failed tier can be attributed to the actual limiter rather than assumed proxy capacity.
  - Compare the phase-aware fixed/rate cost slopes against 1k, including proxy/connector process-tree allocation/GC/goroutine/stack/channel and per-phase DB-operation scaling; do not accept hidden queue/cache growth as throughput.
  - Repeat enough valid runs to satisfy the frozen gate profile and capture diagnostic profiles at/near the first degraded point.
  - Record PASS/NO-GO per gate/profile immediately; continue independent remaining gates where safe instead of omitting a failed verdict.
  - _Requirements: 18.1–18.16, 19.1–19.3, 19.13–19.14_
  - _Boundary: performance certification_
  - _Depends: 9.1_
  - _Validation: full frozen 5k logical-stream scenario/gate matrix where host supports it_

- [ ] 9.3 Certify the 10,000-logical-stream stretch tier
  - Attempt 10k logical-stream HOLD first, then active DELTA and other representative high-impact scenarios only when generator/host safety permits; do not infer active capacity from held streams or infer streams from connection count.
  - Record exact stream/connection topology, proxy/host/generator/database/socket limits, error/tail behavior, memory slope, and dominant CPU/mutex/DB profile at the highest stable load.
  - Record proxy and connector-child resource slopes, fixed owners/buffers per stream, per-event allocation/GC and DB work, scheduler/blocking evidence, and any cache/queue/pool saturation that limits active rather than merely held streams.
  - If 10k cannot be generated on one driver, distribute generation without changing the proxy host and record aggregate driver resources/configuration and actual physical connections.
  - Mark 10k `GO` only for valid scenarios completing the frozen steady-state window and passing all required gates; otherwise record explicit NO-GO classification, failed/unsupported gate, measured limiter, and next experiment.
  - _Requirements: 1.12–1.14, 2.11, 15.1–15.9, 18.5–18.16, 19.1–19.3, 19.13–19.14_
  - _Boundary: performance certification_
  - _Depends: 9.2_
  - _Validation: frozen 10k logical-stream scenario/gate set where environment permits_

- [ ] 9.4 Run steady-state soak and post-load reclamation certification
  - At the highest sustainable representative active logical-stream tier, run a multi-hour SOAK (or longer equivalent duration justified/recorded by environment constraints) with secure sessions and representative production features enabled.
  - Sample per-process and aggregate RSS/live heap/objects/GC/goroutines/stacks/sockets/session-history/cache/lease/queue/spool state over time and evaluate the **pre-frozen** memory-growth method rather than visually accepting a graph.
  - Terminate/disconnect the workload and evaluate the frozen cleanup window/band for heap/goroutines/sockets/session/lease/queue state; investigate any retained-growth slope instead of accepting a one-time peak.
  - Include a representative negotiated executable-plugin run where supported and verify its control/upstream/closer/cancellation owners and bounded buffers return to baseline after termination.
  - Record soak duration, logical streams, physical connections, resource slopes, cleanup gate result, and any leak/retention fix experiment.
  - _Requirements: 2.15, 2.18–2.20, 5.10, 7.10, 18.2, 18.4, 18.6–18.11, 18.15, 19.2–19.3, 19.9–19.14_
  - _Boundary: performance certification / leak evidence_
  - _Depends: 9.1–9.3_
  - _Validation: SOAK + DISCONNECT final gate profiles_

- [ ] 9.5 Finalize the evidence ledger and capacity verdict
  - Complete every #394 finding row as `proved improvement`, `correctness fixed`, or `measured non-bottleneck/no production change`; no item may disappear because an attempt failed, was invalid, or was unsupported by the host.
  - Summarize final versus baseline deltas for event latency, memory per held/active logical stream, CPU/GC, major lock contention, SQL/store work, completion throughput, cleanup, and physical transport connections, with scenario/environment/gate IDs.
  - Publish the final phase-aware cost envelope and operation classification, including proxy/connector process-tree resources and why each cache, async writer, buffer/object pool, shared-state change, or goroutine/worker topology was retained, rejected, or left unchanged.
  - State the highest proven HOLD and active DELTA logical-stream tiers separately for memory/durable secure-session modes plus observer/authority/billing/topology qualifications and remaining measured limiter.
  - State explicit NO-GO for every uncertified tier and enumerate failed, missing-definition, safety-stopped, and environment-unsupported gates; never convert a 1k/5k curve or connection count into a 10k stream claim by extrapolation.
  - _Requirements: 1.5–1.14, 15, 18.5–18.17, 19.1–19.14_
  - _Boundary: evidence / spec scratch finalization_
  - _Depends: 9.1–9.4_
  - _Validation: completed `benchmark-scratch.md` Final Certification Record_

- [ ] 9.6 Run final repository verification, record verdicts even on failure, and preserve the regression harness
  - Run `make quality-checks`, `make test`, relevant parity/architecture suites, targeted race tests, configured durable integration tests, perf privacy validation, and all safely executable required certification gates on the exact certification commit.
  - **Always update `benchmark-scratch.md` with the certification commit and final GO/NO-GO scope.** A failing required gate prevents GO but does not prevent recording NO-GO; continue remaining independent gates where safe to improve diagnosis.
  - If a safety/resource failure requires stopping a tier, record the failed gate/limiter immediately and mark dependent unexecuted gates `not-run-after-safety-stop`; do not pretend the gate suite passed and do not discard the partial evidence.
  - Ensure low-scale deterministic perf-harness contract tests remain CI-appropriate while expensive scale/soak commands are documented for explicit/nightly/manual execution; verify scenario/result/gate formats and tool help are sufficient for future repetition.
  - _Requirements: 16, 17.10, 18.11–18.18, 19.3, 19.13–19.14_
  - _Boundary: repository verification / tests / documentation_
  - _Depends: 9.5_
  - _Validation: `make quality-checks && make test`, targeted race/integration/parity/privacy/perf commands plus completed verdict record_

## Task Graph / Parallelization Summary

- **Strictly before production changes**: 1.1 → 1.2/1.3 → 1.4/1.5 → 2.1/2.2 → 2.3.
- **P0 order**: Phase 3 (timeouts/body) → Phase 4 (secure-session persistence) → Phase 5 (stream memory). These should not be reordered behind P2 work.
- **Post-#446 baseline**: built-in/standard versus executable-plugin HOLD/DELTA and the expanded DISCONNECT matrix are Phase 1/2 prerequisites and Phase 8/9 regression gates. #446's parallel sibling cancellation is not a new implementation task.
- **P1**: 6.2–6.5 can be implemented as separate reviewable changes after Phase 5; baseline refreshes may run in parallel, but each production change must preserve its own A/B attribution. 6.6 is conditional on 6.5 evidence; 6.7 reconciles the remaining classified database-operation ledger after the targeted store changes.
- **P2**: 7.1, 7.2, 7.4, 7.5, 7.6, 7.7, 7.8, and 7.9 may run in parallel from the same post-P1 baseline when agent capacity allows. 7.3 depends on 7.2 evidence. 7.10 reconverges the branch/profile and phase-aware cost envelope.
- **Certification**: Phase 8 must reconverge correctness/privacy first; 9.1 → 9.2 → 9.3 scales logical streams progressively; 9.4 soak follows the highest sustainable tier; 9.5–9.6 close out. A failed gate locks that tier out of GO immediately, but remaining independent gates continue where safe.

## Completion Rule

This implementation plan is complete only when **all audited findings and the Requirement-19 cost envelope have evidence**, not when all possible code has been changed. A lower-priority cache/reuse/pool candidate that benchmarks as non-material is correctly completed with no production modification and a durable scratch entry. A change with no before/after evidence is incomplete. A claimed 10k capacity result without an actually executed **10,000-logical-stream** scenario and its frozen required gates is invalid. Physical connection counts are evidence about topology/resource use, never a substitute for the logical-stream capacity claim, and parent-process improvement cannot hide connector-child regression. Final evidence must record both GO and NO-GO outcomes rather than suppressing failed certification attempts.
