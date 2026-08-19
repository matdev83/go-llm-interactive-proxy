# Research & Design Decisions

## Summary

- **Feature**: `high-concurrency-performance-hardening`
- **Issue**: #394 — high-volume/high-concurrency proxy reality check
- **Classification**: Brownfield, cross-cutting performance/scalability hardening of existing streaming, persistence, admission, and coordination paths
- **Audit baseline**: `962e1546e29a6df0f6d19f2ae195a9999cdf1b64`
- **Current main during spec creation**: `d8f61a6eeaf360fe8c561e5355a3b4830153a704`
- **Baseline freshness**: The only commit between the audited SHA and current main is a spec-only Kiro change; no production code changed, so the static audit findings remain current for this specification.
- **Current evidence status**: Static/code-path findings only. Existing focused benchmarks exist for a few paths, but no end-to-end 1k/5k/10k capacity certification was found. This spec therefore treats current performance claims as unproven until the implementation records measurements in `benchmark-scratch.md`.

### Key Findings

1. The goroutine-per-request/stream model is not the primary identified problem. The highest-impact hazards are synchronous shared side effects on event cadence, unbounded/duplicated retained stream state, admission after body materialization, and absolute whole-request timeouts.
2. Secure-session stream recording is the strongest known P0: an ordinary visible stream delta can synchronously drive activity, transcript sequence/append, and audit sequence/append work; the memory store adds a global lock and linear sequence scans, while durable adapters redundantly perform sequence discovery.
3. `retryRecvStream`/customer evidence/token accounting retain and copy output histories in several forms. Current `streamusage.Reconstruct` explicitly accepts complete `OutputText` and `Events`, then filters/clones events at terminalization.
4. Frontend body QoS is placed after `ReadAll` and JSON preflight. The existing process-owned decode limiter is good infrastructure, but it does not currently bound memory already allocated before admission.
5. Several P1 shared stores/dispatchers serialize independent work: auth sink dispatch, B2BUA memory continuity, and the optional concurrency-authority lease store. Terminal billing spooling is deliberately durable/bounded but does redundant aggregate work per append.
6. Several P2 candidates should be benchmarked rather than automatically refactored: deep `lipapi.Call` cloning, traffic observation serialization/copies, executor/RNG/credential/state/affinity/lifecycle locks, and HTTP transport pool tuning.
7. Existing atomic generation pinning, atomic model-catalog snapshots, per-stream EventPump synchronization, shared outbound transport, and much of per-A-leg lifecycle ownership are positive controls and should not be rewritten without contrary evidence.
8. The repository already contains protocol-faithful reference clients and reference backend HTTP emulators. The traffic generator should orchestrate/reuse these assets rather than reimplement all protocol wire behavior.

## Research Log

### 1. Product and repository constraints

**Context**: Performance work touches several layers and could easily become a broad architecture rewrite.

**Sources consulted**:
- `.kiro/steering/product.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/testing.md`
- active `.kiro/specs/turn-recv-terminal-ownership-simplification/*`

**Findings**:
- The product is explicitly streaming-first; non-streaming is collected over the canonical stream.
- Core policy must remain protocol/provider neutral; frontends and backends translate through `pkg/lipapi`.
- Explicit composition, no hidden `init()` state, no generic DI/reflection framework, and clean goroutine ownership are repository invariants.
- Tests use TDD, deterministic local `httptest`/fakes in default suites, environment-gated integration tests for real databases, and `goleak` in goroutine-owning packages.
- The active recv/terminal ownership spec intends to relocate response event accumulation, usage evidence, gate/drain state, and terminal ownership. It does not by itself guarantee bounded memory, so this performance spec must define boundedness independently of the eventual private owner.

**Implications**:
- Performance fixes should be selective seam/ownership improvements, not a new performance subsystem.
- Load tests may reuse real frontend wire paths and local reference backends without external provider APIs.
- Where an active structural spec lands first, this spec should modify the new owner rather than resurrect old fields.

### 2. Reusable traffic-generator assets

**Context**: The user requested traffic generators first and suspected protocol/client emulators can be reused.

**Sources consulted**:
- `internal/refclient/`
- `internal/refbackend/`
- `internal/testkit/conformance/wire_clients.go`
- `internal/refclient/openairesponses/`
- `internal/refbackend/openairesponses/`

**Findings**:
- Reference clients exist for OpenAI Responses, OpenResponses, legacy OpenAI Chat, Anthropic Messages, and Gemini.
- Reference backends exist for those protocol families plus several additional adapters.
- The OpenAI Responses reference backend already contains an HTTP server/responder and scripted response machinery, which is a natural deterministic upstream for controlled TTFT/event cadence/length/failure injection.
- Existing conformance wire-client helpers can reduce duplicated request construction.

**Implications**:
- Build one load orchestration layer that can choose a protocol driver and scripted local upstream while keeping scenario parameters protocol neutral.
- Start with one representative streaming protocol for high-scale resource tests, then add a bounded sentinel across other frontend families to prove the harness is not accidentally tied to one codec. Do not create a Cartesian FE×BE performance matrix.

### 3. Secure-session stream-event persistence

**Context**: #394 audit identified event-cadence persistence as the strongest throughput/latency risk.

**Sources consulted**:
- `internal/core/runtime/secure_session_stream_record.go`
- `internal/core/securesession/app/recorder.go`
- `internal/core/securesession/adapters/memory/store.go`
- `internal/core/securesession/adapters/bunstore/store_transcript.go`
- `internal/core/securesession/adapters/bunstore/store_audit.go`
- `internal/infra/controlplane/observers/securesession_store.go`
- `internal/core/controlplane/recorder.go`

**Findings**:
- `beforeEmitClientFacing` synchronously records essentially every client-facing event other than the keepalive warning path.
- Building the record also JSON-marshals the event representation.
- `RecordPostHookStreamEvent` can perform `TouchActivity`; for transcript-enabled events it separately asks for `NextTranscriptSeq` then appends transcript; it separately asks for `NextAuditSeq` then appends audit; usage events add usage persistence. A normal text delta can therefore require roughly five store operations before optional control-plane projection.
- The memory adapter uses one store-wide `sync.RWMutex`. Sequence allocation scans the existing transcript/audit slice for the maximum sequence while holding a write lock, so repeated appends grow toward O(n²) aggregate sequence work over a long history and unrelated sessions serialize.
- Bun `NextTranscriptSeq`/`NextAuditSeq` perform existence/`MAX(seq)` queries, while the corresponding append transaction independently computes the next `MAX(seq)+1`. The application-level separate next-sequence call is therefore redundant for these adapters.
- The durable append path also touches/locks parent rows and performs multiple SQL operations per event.
- Secure-session store decorators may project activity/audit/usage synchronously into the control plane, multiplying work if enabled.

**Implications**:
- Make sequence assignment + append one logical store mutation.
- Memory adapter: keep O(1) next counters and narrow synchronization to a session/shard.
- Durable adapter: allocate sequence atomically inside the append transaction; remove pre-append `MAX` query. Then benchmark query/transaction counts.
- Coalesce activity updates to a bounded interval or terminal event where semantic/expiry tests prove equivalence.
- Best-effort projections may be batched only with explicit boundedness. Mandatory persist-before-release recording remains synchronous in the durability sense; if remote durable acknowledgement is still limiting after removing redundant work, benchmark a local durable acknowledgement/spool or small durability-preserving microbatch before adding complexity.
- Explicit expiry/reclamation for memory histories is part of capacity correctness, not merely throughput.

### 4. Secure-session and B2BUA turn lifecycle chatter

**Sources consulted**:
- `internal/core/securesession/app/manager.go`
- secure-session Bun store create/load paths
- B2BUA lineage/store calls in request preparation and terminal paths

**Findings**:
- New-session setup creates A-leg, creates secure-session state, and touches activity; the Bun create path inserts then loads the row again.
- Runtime preparation may then fetch A-leg state again although it has already been established in the request flow.
- Resume performs load/readiness/touch and then downstream A-leg fetch.
- Finish loads session state and performs separate sequence lookup before audit append.

**Implications**:
- Return/use authoritative values already created by the write/orchestrator when safe.
- Collapse next-sequence + terminal audit append.
- Query-count and TTFT/completion-burst measurements decide whether further caching/consolidation is justified.

### 5. Per-stream retained output and terminal reconstruction

**Sources consulted**:
- `internal/core/runtime/executor_retry_stream.go`
- `internal/core/runtime/customer_evidence.go`
- `internal/core/runtime/executor_settlement.go`
- `internal/core/tokenaccounting/streamusage/reconstructor.go`

**Findings**:
- `retryRecvStream` retains `seenEvents []lipapi.Event` and a `visibleText strings.Builder`, plus completion-gate and recovery-drain state.
- `customerEvidenceAccumulator` separately retains text, reasoning, tool-argument builders and `content []lipapi.Event`; releasing an event can both append an event and copy its delta into builders.
- Terminal accounting copies the seen-event slice and passes full events plus the full released output text to `StreamUsage.Reconstruct`.
- `streamusage.Reconstructor` extracts usage events from the complete slice, calls output counting with the full text and a separately built non-usage event slice, and returns cloned usage events. This confirms that current accounting shape structurally encourages lifetime retention and terminal copies.

**Implications**:
- Introduce incremental/compact usage evidence at the accounting boundary rather than merely moving `seenEvents` to another collaborator.
- Establish one authoritative customer-output/evidence accumulator per semantic need; avoid duplicate full event/text forms.
- Inventory every long-lived stream buffer (`gateBuf`, gate/recovery drains, tool args, pending wire events) and give it an explicit byte/event bound or a documented reason/full-size guard.
- Measure memory slope against event count and concurrency plus terminal allocation spikes. A lower line count is irrelevant unless retained bytes/GC improve.

### 6. Request-body admission occurs after materialization

**Sources consulted**:
- `internal/plugins/frontends/frontendpipe/pipe.go`
- `internal/plugins/frontends/reqbody/body.go`
- `internal/plugins/frontends/decodeqos/limiter.go`
- `internal/infra/runtimebundle/process_services.go`

**Findings**:
- Authentication runs before the body is read, which is desirable for rejecting unauthorized requests early.
- The authorized request body is then fully `ReadAll`'d under the max-size guard and JSON-preflighted before decode admission is acquired.
- The existing decode limiter is process-owned and shared across runtime generations and supports count/byte budgeting, which is a good boundary.
- With the observed defaults of 8 MiB max request body, 32 concurrent decodes, and 64 MiB in-flight decode bytes, the limiter bounds decode work but not an arbitrary number of already-materialized bodies waiting to enter decode.

**Implications**:
- Prefer a staged use/evolution of the existing process-owned limiter: reserve a count/materialization slot before `ReadAll`, retain the max-size guard, then account/refine the actual byte weight for decode. This produces a straightforward upper bound without trusting `Content-Length` and avoids inventing a separate public memory-control subsystem.
- Chunked/unknown-length bodies must remain bounded while being read.
- Body-flood tests must prove peak memory is a function of configured limits, not concurrent arrival count.

### 7. Whole-request HTTP timeouts conflict with streaming

**Sources consulted**:
- `internal/stdhttp/generation_host.go`
- `internal/infra/httpclient/standard.go`
- associated timeout config/tune parsing

**Findings**:
- The standard server applies finite `ReadTimeout`/`WriteTimeout` defaults; the observed write default is 120 seconds.
- The shared outbound client has a finite 120-second `http.Client.Timeout` in addition to phase-specific transport timeouts.
- In Go, the client timeout is a total request time including reading the response body; a server write timeout is not an idle-progress timer for an arbitrarily long stream.
- Configuration parsing observed during the audit accepted only positive duration overrides in the relevant tune path, meaning a zero intended as disabled can be replaced by the default unless semantics are changed deliberately.

**Implications**:
- Streaming data-plane clients should rely on phase-specific dial/TLS/header/TTFT/stream-idle/request-context/shutdown policies instead of a blanket total-body deadline.
- Inbound stream writes likewise need streaming-safe lifetime semantics.
- This is a correctness fix and must be implemented even if a throughput benchmark does not improve; before/after evidence must reproduce and remove the >120 s failure.

### 8. Auth event dispatcher global critical section

**Sources consulted**:
- `internal/core/auth/events.go`
- `internal/infra/runtimebundle/auth_events.go`

**Findings**:
- One dispatcher mutex is held while calling sink implementations for auth decisions/session starts.
- The default sink is structured logging and may be wrapped/decorated by control-plane behavior.
- External sink work therefore sits inside a process-wide request-start critical section even for unrelated requests.

**Implications**:
- Remove process-wide serialization from the dispatcher unless a real global ordering contract exists.
- A sink that needs serialization owns that adapter; session-local ordering, if needed, should not become global ordering.
- Benchmark default/slow/control-plane variants before deciding whether anything beyond lock-scope removal is justified.

### 9. B2BUA continuity memory store

**Sources consulted**:
- `internal/core/b2bua/store.go`
- continuity configuration/effective defaults

**Findings**:
- The memory store has one `RWMutex`, but many operations use the write side because apparently read-like access also refreshes `LastSeenAt` and may perform eviction.
- Create can sweep expired legs while holding the store-wide lock.
- The in-memory store is the default continuity implementation in common composition, so this is not merely a test adapter.

**Implications**:
- Separate record lookup from activity metadata/expiry maintenance where possible.
- Use per-A-leg or sharded synchronization and bounded/amortized expiry work if profiles confirm contention.
- Benchmark independent A-legs, a single hot A-leg, and expiry churn; do not optimize only the easiest many-key case.

### 10. Concurrency-authority memory lease store

**Sources consulted**:
- `internal/infra/concurrencyauthority/leasestore/memory.go`
- `internal/infra/concurrencyauthority/leasestore/contention_bench_test.go`

**Findings**:
- A single mutex covers acquire/release/query state.
- Acquire reclaims/scans lease state and counts live leases under the same lock; historical released/expired records can expand scan work.
- A useful 100-contender benchmark already exists, but it does not establish 1k–10k admission behavior.

**Implications**:
- Reuse the benchmark and extend fixed contender/dimension cases.
- Likely target is per-dimension live counters plus bounded expiry/tombstone cleanup, but the implementation choice must be measured against simpler sharding/per-key approaches.

### 11. Durable billing spool completion path

**Sources consulted**:
- `internal/infra/billingspool/spool.go`

**Findings**:
- The local SQLite spool is intentionally bounded and durable, with WAL/`synchronous=FULL` behavior and one owned connection.
- Each append checks replay and calculates pending `COUNT`/`SUM`, along with capacity/disk checks, then inserts/commits.
- This is terminal-only rather than per-delta, so it is lower priority than stream recorder/memory, but synchronized completion bursts can expose it.

**Implications**:
- First remove redundant aggregate scans by maintaining safe pending counters/bytes with startup reconciliation or equivalent transactional metadata.
- Do not weaken synchronous durability or add concurrent writers unless completion-burst evidence proves the simpler single writer is inadequate.

### 12. Deep `lipapi.Call` cloning

**Sources consulted**:
- `pkg/lipapi/call_clone.go`
- request preparation/retry paths

**Findings**:
- `CloneCall` recursively copies message parts, tools/parameter JSON, generation options, extensions/raw JSON, semantic extensions, and session metadata.
- Preparation/hook/transform/retry paths can clone the logical baseline multiple times.
- Cost scales with prompt/tool-schema size and retry count, but unlike the P0 items its production materiality has not been established.

**Implications**:
- Benchmark first. If material, remove redundant clones before introducing copy-on-write/immutable-sharing complexity.
- Attempt/hook mutation isolation is a correctness gate for any sharing optimization.

### 13. Traffic observation serialization/copying

**Sources consulted**:
- `pkg/lipsdk/traffic/ports.go`
- `pkg/lipsdk/traffic/redact_chain.go`
- `internal/core/runtime/attempt_stream_traffic_bench_test.go`

**Findings**:
- Emission applies redaction/copies and invokes raw/observer sinks synchronously.
- Observer delivery can copy bodies/headers; chained redactors can copy repeatedly.
- Existing benchmark commentary already identifies event JSON serialization as a significant traffic-emission cost in at least a microbenchmark.
- Some request paths may serialize before determining whether a consumer exists.

**Implications**:
- Add a cheap no-consumer capability/guard where it avoids real work.
- Benchmark observer off/on/slow. Use bounded asynchronous delivery only for best-effort contracts and only when measured.

### 14. Residual locks and connection tuning candidates

**Sources consulted**:
- `internal/core/runtime/executor_config.go`
- executor RNG helpers
- `pkg/credpool/pool.go`
- `internal/core/state/mem.go`
- `internal/core/affinity/memorystore/store.go`
- `internal/core/leglifecycle/coordinator.go`
- standard HTTP transport/server configuration

**Findings**:
- Lifecycle-coordinator lazy lookup and a shared RNG have small global mutexes on request/routing paths.
- Credential pools serialize acquire/scan but pools are typically small.
- Generic scoped memory state holds a mutex while encoding/decoding in some paths.
- Affinity has a short map lock; leg lifecycle has a top-level map lock but expensive cancel/close work is already moved outside it and B-leg mutable state is mostly per A-leg.
- The outbound transport is shared, attempts HTTP/2, and its idle-pool limits do not cap active connections. Direct standard inbound serving is plaintext HTTP/1.1 unless an external TLS/H2 terminator is used.

**Implications**:
- These are profile-first P2 candidates. `sync.Once`/pre-wiring, lock-scope reduction, sharding, or transport tuning should ship only where evidence is material.
- Final capacity tests must record direct-vs-terminated topology and OS socket/file-descriptor prerequisites.

## Existing Strengths to Preserve

- Runtime generation request pinning/refcount is atomic/CAS-based.
- Model-catalog active snapshot reads are atomic.
- EventPump synchronization is per stream rather than process global.
- Existing decode admission is process-owned across generations.
- Shared outbound HTTP transports avoid per-request client construction and attempt HTTP/2.
- Leg lifecycle avoids holding its top-level lock around expensive B-leg cancel/close calls.
- Billing spool already has explicit storage/backlog caps and durable crash-oriented behavior.

The performance program shall not mechanically replace these patterns merely because synchronization exists.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| Broad worker-pool/actor rewrite | Route requests/events through a new scheduler | Can centralize resource control | Changes semantics, adds queues/goroutines, conflicts with streaming-first ownership, not justified by audit | Reject |
| Micro-optimize first | Tune clones/maps/pools before structural P0 work | Easy local patches | Optimizes secondary costs while DB/event retention dominates | Reject |
| Evidence-first targeted hardening | Build harness, fix known P0s, then benchmark P1/P2 candidates | Causal, incremental, preserves boundaries | Requires disciplined repeated measurement | **Select** |
| Pure benchmark-only program | Measure but do not require known hazard remediation | Minimal code churn | Leaves correctness/scalability defects known from static path analysis | Reject |
| Async everything | Put recorder/observers behind queues | Can reduce caller latency | Can violate mandatory durability/order, creates hidden memory/backpressure | Reject as default; only bounded best-effort where policy permits |

## Design Decisions

### Decision 1: Load infrastructure lands before optimization

- **Selected approach**: Reuse reference clients/backends and add a scenario orchestration/load driver with stable scenario IDs and machine-readable output.
- **Rationale**: Every later decision needs a comparable baseline. Building it after changes destroys causal evidence.
- **Trade-off**: Delays first production patch, but prevents large speculative refactors.

### Decision 2: One append-only evidence scratchpad is part of implementation workflow

- **Selected approach**: Keep `benchmark-scratch.md` in the spec folder and append experiment entries including unsuccessful ones.
- **Rationale**: It provides durable reasoning/evidence across multiple implementation agents/PRs and enforces “not measured = not proved.”
- **Trade-off**: The file will grow; that is intentional provenance rather than production documentation.

### Decision 3: Separate connection-holding capacity from active-event throughput

- **Selected approach**: Maintain `HOLD-*` and `DELTA-*` scenarios independently at 1k/5k/10k tiers.
- **Rationale**: 10k mostly-idle streams and 10k rapidly emitting streams stress very different resources. A single “concurrent connections” number is misleading.

### Decision 4: Correctness hazards do not require a positive throughput delta to ship

- **Selected approach**: Whole-request timeout semantics, bounded body admission, mandatory resource cleanup, and security/durability correctness remain required fixes. Their evidence proves the defect is removed and no unacceptable regression is introduced.
- **Rationale**: A correctness failure cannot be retained because throughput is unchanged.

### Decision 5: Secure-session optimization is layered, not an immediate asynchronous rewrite

- **Selected approach**: First remove redundant sequence calls/linear scans/global lock scope and activity amplification; benchmark. Then consider bounded batching/local durable acknowledgement only if mandatory durable remote acknowledgement remains the measured limiter.
- **Rationale**: This preserves the fail-closed contract and may capture most benefit with far less complexity.

### Decision 6: Body admission should evolve the existing process-owned QoS budget when possible

- **Selected approach**: Prefer staged reservation (count/materialization before `ReadAll`, actual bytes before/through decode) under the existing process-owned limiter.
- **Rationale**: It naturally survives generation reload and avoids a second resource-control system. Counting slots before reading also safely covers chunked bodies without trusting headers.

### Decision 7: Stream accounting must consume compact state, not require full history

- **Selected approach**: Add incremental/compact provider-usage and local-output evidence sufficient for final reconciliation/counting, with bounded suffix/structured state only where tokenizers/protocol semantics require it.
- **Rationale**: Moving full slices into a new response-pipeline owner does not solve memory scaling.
- **Guard**: Provider-specific counters may own provider/tokenizer-specific compact state behind the existing accounting boundary; do not put provider branches in core runtime.

### Decision 8: Optional optimizations require measured materiality

- **Selected approach**: Clone reduction, traffic async delivery, lock sharding for low-contention stores, and transport-pool changes start with a baseline experiment. A measured-no-change result is a valid task outcome.
- **Rationale**: This minimizes architectural churn and preserves simple known-correct code.

### Decision 9: Final certification reports capacity honestly

- **Selected approach**: Report 1k/5k/10k separately, held versus active, feature/store modes, tested environment, and limiting resource. No extrapolation from 1k to 10k.
- **Rationale**: Capacity is a property of workload plus host plus dependencies, not a source-code constant.

### Decision 10: Performance tooling remains reusable but internal

- **Selected approach**: Keep load harness/scenarios in internal tooling/test support and retain after this project; production observability additions are minimal/bounded.
- **Rationale**: Future regressions need the harness, but benchmark needs alone do not justify public API surface.

## Brownfield Requirements Gap Analysis

### Initial gap inventory

The first requirements pass was cross-checked against the current implementation and audit. The following gaps were found and repaired upstream in `requirements.md`:

1. **Control-plane amplification**: Initial secure-session wording focused on the authoritative store but did not explicitly cover synchronous decorator/projection amplification. Added Requirement 5.9.
2. **Memory-store retention**: Initial lock/sequence work did not guarantee completed session/transcript/audit reclamation. Added Requirement 5.10 and final monotonic-growth checks.
3. **Timeout disable semantics**: Removing the runtime timeout without fixing positive-default configuration semantics could make a zero value ineffective. Added Requirement 3.4.
4. **Chunked request bodies**: Admission based only on `Content-Length` would leave unknown-length bodies exposed. Added Requirements 4.3 and 4.6.
5. **Terminal copy spikes**: Merely bounding steady-state buffers would miss full-slice copies during settlement. Added Requirements 7.5 and 7.8.
6. **Active ownership refactors**: The response-state structural spec could either land before or after this work. Added Requirements 7.7 and 17.3 so performance ownership follows the authoritative shape instead of creating duplicate state.
7. **Host versus proxy attribution**: 10k tests can fail on generator/FD/backlog limits. Added Requirements 1.12, 15.1–15.6, and 18.7.
8. **Residual locks**: The audit listed lifecycle/RNG/credential/state/affinity/leg locks as lower-priority candidates. Added Requirement 14 so every audited candidate is measured and either optimized or explicitly closed as non-material.
9. **Instrumentation distortion**: Profiling/metrics can themselves add hot-path cost/cardinality. Added Requirement 16.
10. **Durability preservation**: Async/batching suggestions could accidentally weaken mandatory recorder or billing durability. Strengthened Requirements 5.6–5.8 and 11.2–11.4.
11. **Failed-experiment provenance**: A before/after-only rule could cause agents to delete neutral/reverted attempts. Added Requirements 1.6–1.7 and the append-only scratch template.
12. **Connection type ambiguity**: “10k streams” without separating idle-held from active emitting was too vague. Added Requirement 2.1–2.2 and 18.5.

### Requirements gate after repair

**PASS.** Every #394 audit finding has an observable requirement, measurement obligation, or explicit measured-no-change path. Correctness requirements remain separate from optional optimization materiality. No requirement dictates a provider-specific implementation or a generic architecture framework.

## Brownfield Design Validation

The design was validated against steering boundaries, current source ownership, active specs, durability/security contracts, and the requirements above.

| Validation question | Verdict | Design guard / repair |
|---|---|---|
| Can the harness reuse real repo emulators rather than introduce a new wire stack? | PASS | `internal/refclient` + `internal/refbackend` are explicit dependencies of the internal load driver. |
| Does measurement infrastructure have to touch public API? | PASS | Keep scenario/config/result types internal; use existing HTTP/product config surface. |
| Does secure-session optimization weaken mandatory persist-before-release? | PASS after repair | Design explicitly separates mandatory acknowledgement from best-effort projection and makes async optional/benchmark-gated. |
| Can sequence calls be collapsed without changing record order? | PASS | Make sequence allocation internal to append transaction/record owner; preserve uniqueness/order tests. |
| Does early body admission conflict with auth-first behavior? | PASS | Authentication remains before body materialization admission/read; admission is inserted after auth and before `ReadAll`. |
| Can chunked bodies be bounded without a new header-trust contract? | PASS after repair | Reserve a materialization/count slot before reading; enforce existing max reader; refine byte budget using actual bytes. |
| Does incremental evidence conflict with `turn-recv-terminal-ownership-simplification`? | PASS after repair | Boundedness belongs to the response/accounting state contract; physical owner follows whichever spec has landed. |
| Does the design require provider logic in core for incremental counting? | PASS | Compact/incremental provider-specific state stays behind accounting/counter adapters; core owns only canonical evidence. |
| Could B2BUA/authority sharding change single-key correctness? | PASS | Per-key/dimension correctness is characterization/race-gated; implementation starts with smallest measured lock-scope change. |
| Could billing optimization weaken FULL durability/caps? | PASS | Aggregate-counter optimization precedes any writer concurrency; crash/replay tests are mandatory. |
| Could traffic async queues become unbounded? | PASS | Only bounded queues with explicit full policy; mandatory observers retain backpressure. |
| Are known-good atomic/per-stream primitives accidentally in scope for rewrite? | PASS after repair | Requirements 14.6 explicitly preserve them absent contrary profile evidence. |
| Does final 10k goal become an invented absolute SLO? | PASS | Certification states tested environment/highest proven tier and may produce NO-GO for 10k with evidence. |

### Significant design repairs made during validation

- Made body admission staged around the existing process-owned QoS limiter rather than designing a separate semaphore family.
- Made secure-session asynchronous persistence a second-stage measured option, not the default architecture.
- Added explicit retention/reclamation to secure-session memory state and completed-stream evidence.
- Added a cross-spec migration rule so response ownership simplification and performance boundedness cannot create parallel authorities.
- Added measured-no-change as a valid completion path for P2 candidates, preventing unnecessary abstraction.
- Added explicit host/topology attribution and separate held-versus-active certification.

**Design validation verdict: PASS / GO.** The planned architecture is implementable against current boundaries and contains explicit safety rails for the high-risk durability, security, billing, and streaming semantics.

## Risks & Mitigations

- **Benchmark generator becomes the bottleneck** — support generator/process placement, record generator CPU/resources, distribute load when needed, and mark host/generator-limited tiers honestly.
- **Profiling perturbs tail latency** — collect profiles in dedicated repeats when necessary and compare non-profiled runs for headline latency/throughput.
- **Remote database variance hides code effects** — use local deterministic stores for code-path comparisons and a separate controlled PostgreSQL topology for durable integration/capacity evidence.
- **Optimization changes semantics** — TDD characterization, race tests, durability/replay tests, and active-spec reconciliation precede/guard each production change.
- **Async queues hide overload** — bounded capacity plus explicit full/backpressure/drop/coalesce semantics and metrics; never unbounded event goroutines.
- **Per-session/per-dimension state leaks after sharding** — explicit expiry/reclamation tests plus steady-state/disconnect/soak evidence.
- **10k is limited by OS rather than proxy** — record FD/socket/backlog/resource limits and separate direct HTTP/1.1 from externally terminated HTTP/2 topology.
- **Microbench win regresses end-to-end** — every retained micro-optimization also runs the relevant end-to-end scenario before final acceptance.
- **Parallel implementation agents invalidate baselines** — P0 production phases are sequential by dependency; optional P2 measurement tasks may parallelize only after the frozen harness/baseline and must name their baseline commit.

## Final Cross-Artifact Review

- Requirements cover all P0, P1, P2, positive-control, measurement, cleanup, and certification findings from #394.
- Research current-state facts remain valid against the production code because current main differs from the audit baseline only by a spec-only commit at spec creation time.
- Design choices preserve steering boundaries and explicitly coordinate with active runtime-ownership specs.
- Tasks are ordered by impact: harness/baseline first; streaming correctness/admission; secure-session event-cadence work; bounded stream memory; then P1 shared stores/persistence; then evidence-gated P2 allocation/observer/lock/transport work; final certification last.
- Every production optimization task has an evidence obligation in `benchmark-scratch.md`; no “expected to improve” task can close without measured before/after evidence.
- Spec-only delivery contains no production implementation.

**Final review verdict: GO for implementation.**

## References

- GitHub issue #394 and its four audit comments — authoritative problem statement and audit findings.
- Project steering: `.kiro/steering/product.md`, `tech.md`, `structure.md`, `testing.md`.
- Active ownership specs: `.kiro/specs/turn-recv-terminal-ownership-simplification/` and `.kiro/specs/request-attempt-pipeline-state-simplification/`.
- Go standard-library profiling/runtime facilities (`runtime/pprof`, `runtime/metrics`, execution trace) — profiling primitives used by the evidence program.
- Go `net/http` timeout/transport semantics — basis for separating total-request timeout from phase/idle/cancellation policy.
- `golang.org/x/perf/cmd/benchstat` — repeated microbenchmark comparison/statistical reporting.
