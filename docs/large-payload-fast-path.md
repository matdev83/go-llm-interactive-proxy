# Large-Payload Streaming Fast Path

Operator guide for configuring, observing, and rolling back the large-payload streaming fast path optimization in `cmd/lipstd` and `pkg/lipruntime`.

---

## 1. Overview and Architecture

The large-payload streaming fast path (`server.large_payload_fast_path`) is an optional memory-optimization subsystem designed for multi-megabyte requests (e.g., large documents, massive tool definitions, extensive system instructions).

In standard canonical execution, the proxy decodes the full request into an in-memory `lipapi.Call` struct, clones it multiple times for attempt derivations and checkpoints, and re-encodes it to upstream JSON. For requests larger than 1 MiB, this produces substantial Go heap allocation and garbage collection overhead.

The fast path captures the request into a bounded memory/disk spool, scans structural JSON boundaries, verifies protocol constraints, and streams the original bytes (with candidate model name rewriting if necessary) directly to upstream backends without allocating or retaining a full `lipapi.Call` object.

---

## 2. Full-Body Prevalidation

The fast path is **not** an unvalidated raw pipe:

1. **Full Receipt Before Provider Open:**
   Per Requirement 21.8, proxy validation receives and validates the **complete request body before opening the upstream provider connection**. The primary benefit is Go heap/GC reduction and elimination of repeated AST allocations—**not early Time To First Token (TTFT)**.
2. **Outer Handler Checks First:**
   Authentication, TLS, and media-type validation (`Content-Type: application/json`) execute in the outer HTTP handlers before any fast-path capture logic.
3. **Five Pre-Capture Gates (`EvaluatePreCaptureGates`):**
   Requests undergo five cheap pre-capture gates before any spooling:
    - Gate 1, feature/profile/two-phase executor: `enabled: true`, a profile, and a two-phase executor must all be present.
    - Gate 2, known-length threshold: parsed known identity/uncompressed request length below `threshold_bytes` stays canonical (compressed `Content-Length` is never trusted as decoded length).
    - Gate 3, gzip wave 1: gzip-encoded requests bypass capture to canonical decoding.
    - Gate 4, frozen static disposition: `DefinitelyCanonical` generations stay canonical with zero spool/scanner work.
    - Gate 5, legacy full-body resolver: a configured legacy `ResolveRouteSelector` without a bounded contract stays canonical.
   (`POST`-method and route matching are enforced one level out, in shared `ServeHTTP`, before gate evaluation.)
4. **Streaming Token Scanning & Protocol Proof:**
   During capture, an incremental JSON scanner extracts structural tokens (e.g., model name, stream flag, store parameter) into bounded semantic facts. A protocol proof verifies that the request belongs to an approved wire subset.
5. **One-Way Wire Commit:**
   Core assessment verifies route override domains, economic admission, and metering bounds before committing to wire execution. If any check fails, the request gracefully falls back to the canonical path under the same decode permit.

---

## 3. Spool Confidentiality and Security

Spool files contain **plaintext client prompt data**. Operators must configure host environments accordingly:

- **Volume Protection:**
  When `spool_dir` is configured, place it on a dedicated volume or filesystem with restricted access permissions and encryption at rest (e.g., LUKS, BitLocker, or platform volume encryption).
- **Owner-Only Permissions:**
  Spool files are created with mode `0600` (read/write by proxy owner process only) where supported by the operating system.
- **Unpredictable Filenames:**
  Spool files use cryptographically random, unpredictable filenames. Names never contain user IDs, session tokens, model identifiers, or request metadata.
- **Lifetime and Cleanup:**
  Spool files are ephemeral. The underlying file is unlinked immediately upon completion, error, or client cancellation. On Windows systems where open files cannot be immediately unlinked, deletion is marked pending and executed when the last file descriptor closes.
- **Telemetry Redaction:**
  Spool directory paths, full filenames, prompt text prefixes, and sensitive session resume tokens are strictly excluded from logs, Prometheus metrics, and traces.

---

## 4. Memory vs. Disk I/O Tradeoff

### 4.1 Configuration Knobs

Configured under `server.large_payload_fast_path`:

| Key | Type | Default | Description |
|---|---|---|---|
| `enabled` | `bool` | `false` | Master toggle for the optimization. Default is disabled. |
| `threshold_bytes` | `int64` | `1048576` (1 MiB) | Decoded-size consideration threshold. Requests smaller than this use canonical processing. |
| `memory_spool_bytes` | `int64` | `65536` (64 KiB) | In-memory buffer size per captured request. Exceeding bytes spill to disk. |
| `max_inflight_spool_bytes` | `int64` | `268435456` (256 MiB) | Global logical spool reservation ceiling across concurrent captures. |
| `max_semantic_fact_bytes` | `int64` | `262144` (256 KiB) | Maximum memory allowed for profile-derived metadata facts. |
| `spool_dir` | `string` | `""` | Directory for temporary spill files. Empty string uses OS temp directory. |

### 4.2 Optimization Budget vs. OOM Admission

`max_inflight_spool_bytes` is an **optimization budget, not a global request admission gate**:

- If total in-flight spool allocations exceed `max_inflight_spool_bytes`, the proxy **declines fast-path spooling** and routes the request to canonical processing.
- Exhaustion produces **no client-visible error and no HTTP 413**.
- Because canonical fallback can still allocate heap according to pre-existing limits, this budget must not be treated as a global OOM prevention mechanism.

### 4.3 Measured Tradeoffs (Production Evidence)

From empirical benchmarks (Tasks 19.2–19.7):

- **Retained Go Heap:** On accepted wire requests, post-commit retained Go heap during provider streaming is approximately **35.6 KiB with a flat 0.000% slope** across 1 MiB, 5 MiB, and 20 MiB payloads (compared to multi-megabyte retained heaps and 7–9 Call clones on the canonical path).
- **Latency Impact:** Fast-path end-to-end latency is approximately **1.4x to 1.6x higher** than canonical transport (~38.9 ms vs 26.9 ms at 1 MiB; ~184.4 ms vs 116.0 ms at 5 MiB). This overhead stems from disk spill I/O, streaming SHA-256 calculation, and proof compilation before upstream connection establishment.
- **Disk I/O:** Requests exceeding `memory_spool_bytes` (64 KiB) incur sequential disk write and read operations. High-throughput deployments should ensure `spool_dir` resides on fast NVMe or ramdisk storage.

---

## 5. Observability and Diagnostics

Diagnostics use closed-enum metrics with bounded label cardinality (Requirement 22.2). No model names, user IDs, session tokens, or payload paths appear in metric labels.

### 5.1 Prometheus Metrics

Namespace: `lip_large_payload_*`

| Metric Name | Type | Labels | Description |
|---|---|---|---|
| `lip_large_payload_pipeline_stages_total` | Counter | `stage` | Request count reaching each pipeline stage (`considered`, `static_canonical`, `captured`, `profile_proven`, `assessment_eligible`, `wire`, `canonical`). |
| `lip_large_payload_declines_total` | Counter | `reason` | Total requests declined from fast-path to canonical execution, partitioned by decline reason. |
| `lip_large_payload_captured_total` | Counter | `size_bucket`, `storage` | Captures by size bucket (`lt_32k`, `32k_256k`, `256k_1m`, `1m_5m`, `5m_20m`, `gte_20m`) and storage tier (`memory`, `file`). |
| `lip_large_payload_replays_total` | Counter | None | Total times a captured replay source was executed against an upstream provider. |
| `lip_large_payload_rewrites_total` | Counter | None | Total candidate model token rewrites spliced into outbound streams. |
| `lip_large_payload_stage_duration_seconds` | Histogram | `stage` | Latency histogram for pipeline stages (`pre_capture_gates`, `capture`, `proof`, `assessment`, `execution`). |
| `lip_large_payload_active_spool_bytes` | Gauge | None | Current active logical spool reservation in bytes across all in-flight captures. |

### 5.2 Decline Reasons

When a request falls back to canonical processing, `lip_large_payload_declines_total` records one of the following reasons:

- **Static Configuration Blockers:**
  - `feature_disabled`: Feature is toggled off (`enabled: false`).
  - `below_threshold`: Payload `Content-Length` is smaller than `threshold_bytes`.
  - `gzip_compressed`: Inbound payload is gzip-compressed (bypasses capture in V1).
  - `static_blocker`: Active extension planes require full `lipapi.Call` materialization.
  - `local_turn`: Local turn handler plane is occupied.
  - `secret_guard`: Secrets guard inspection is active.
  - `terminal_decision`: Terminal decision provider is configured.
  - `frontend_route_resolver`: Legacy route resolver hook is active.
  - `traffic`: Traffic capture or payload redaction plane is active.
  - `accounting_counting`: Token accounting requires full Call inspection.
  - `custom_call_callback`: Custom Call callback is registered.
  - `backend_domain`: Backend does not belong to a certified wire profile.
- **Dynamic Runtime Declines:**
  - `spool_budget_exhausted`: Active spool bytes exceed `max_inflight_spool_bytes`.
  - `proof_uncertain`: JSON scanner or profile proof encountered unsupported fields or malformed syntax.
  - `authority_blocker`: An extension plane declined the request during pre-commit assessment.
  - `route_incompatible`: Outbound candidates have incompatible wire rewrite domains.
  - `backend_incompatible`: Selected backend does not support raw wire execution.
  - `rewrite_unsupported`: Model name rewrite cannot be applied cleanly to the payload.
  - `session_unsupported`: Session metadata exceeds semantic fact limits.
  - `metering_unsupported`: Metering configuration requires full Call cloning.
  - `counting_unsupported`: Exact wire token counter unavailable for this provider.
  - `generation_mismatch`: Active generation changed during assessment.
  - `canceled`: Client aborted connection during capture or assessment.
  - `limit_exceeded`: Request body exceeded maximum allowable request size.
  - `read_error`: Network I/O failure while reading from client.

---

## 6. Configuration and Rollback

### 6.1 Sample Production Configuration

```yaml
server:
  address: "0.0.0.0:8080"
  large_payload_fast_path:
    enabled: false                   # Keep false until production assessor is wired
    threshold_bytes: 1048576         # 1 MiB
    memory_spool_bytes: 65536        # 64 KiB
    max_inflight_spool_bytes: 268435456 # 256 MiB
    max_semantic_fact_bytes: 262144  # 256 KiB
    spool_dir: "/var/lib/lip/spool"  # Dedicated protected volume
```

### 6.2 Transactional Reload and Last-Good Protection

The configuration parser strictly validates `large_payload_fast_path` on process start and during transactional configuration reloads (`POST /admin/config/reload` or `SIGHUP`):

- Setting negative or zero values for budgets when `enabled: true` fails validation.
- Setting `memory_spool_bytes > max_inflight_spool_bytes` fails validation.
- Blank or whitespace-only `spool_dir` strings, or paths containing NUL characters, fail validation.
- **Last-Good Preservation:** Any invalid reload candidate is rejected (`HTTP 422 invalid`), and the proxy continues operating on the previous valid generation without disruption.

### 6.3 Emergency Rollback

To immediately disable the fast path across all traffic:

1. Update the configuration file:
   ```yaml
   server:
     large_payload_fast_path:
       enabled: false
   ```
2. Trigger an atomic reload via the opt-in management listener (configured with
   `LIP_RELOAD_MANAGEMENT_ADDRESS`, plus `LIP_RELOAD_MANAGEMENT_TOKEN` for
   non-loopback/multi-user exposure):
    ```bash
    curl -X POST http://127.0.0.1:9090/admin/config/reload
    ```
    Or send `SIGHUP` to the process (Unix builds only; non-Unix builds expose reload via the API above).
3. In-flight wire streams finish normally; all new requests immediately revert to canonical processing with zero downtime.

---

## 7. Current Rollout Status and Canonical-Only Default

Per Tasks 19.7 and 20.3, the fast path is **default-off and canonical-only in production**:

- `server.large_payload_fast_path.enabled` defaults to `false`.
- No protocol lane advertises wire support in stock distributions (`lipstd`).
- All certified protocol lanes (Lane 1: OpenAI Responses, Lane 2: OpenAI Chat, Lane 3: OpenResponses No-Store) currently decline to canonical execution in production builds.

### 7.1 Activation Prerequisites

Before any lane may advertise wire support and execute in production, the following follow-up workstreams must be implemented and certified:

1. **Streaming `CompileProof` Rework:** Replace transient `io.ReadAll` inside `CompileProof` with streaming token scanning and `CallIdentityWriter` to eliminate proof-time $O(\text{payload})$ heap allocation.
2. **Production Assessor Composition:** Unify `AuthorityAssessor`, `BackendWireProofAssessor`, `RouteOverrideAssessor`, and `runtime.Executor` into a production `LargeBodyAssessor` composed in `runtimebundle.BuildHost`.
3. **Enablement Wiring:** Formally link `server.large_payload_fast_path` configuration into `runtimebundle` and frontend specifications.
4. **Detached-Session Execution (Req 19.8):** Detached execution stays canonical-only until its Call/lifecycle dependencies are separately represented and parity-tested.
5. **Legacy `ResolveRouteSelector` Contract (Req 13.2):** A configured full-body route resolver stays canonical-only until a future bounded route-resolution contract preserves the same ordering, precedence, and selector semantics.

---

## 8. Specification and Evidence References

For full verification artifacts and design details, consult:

- **Specification Requirements:** `.kiro/specs/large-payload-streaming-fast-path/requirements.md` (Requirements 20, 21, 22)
- **Technical Design:** `.kiro/specs/large-payload-streaming-fast-path/design.md` (Sections 3, 4, 7, 8, 15)
- **Call and Authority Census:** `.kiro/specs/large-payload-streaming-fast-path/evidence/1.8-call-census.md`
- **Plane Census:** `.kiro/specs/large-payload-streaming-fast-path/evidence/1.9-plane-census.md`
- **Benchmarks and Heap Analysis:** `.kiro/specs/large-payload-streaming-fast-path/evidence/19.2-19.5-benchmarks.md`
- **Eligibility Matrix and Lane ROI:** `.kiro/specs/large-payload-streaming-fast-path/evidence/19.6-19.7-eligibility-roi.md`
- **Rollout and Artifact Audit:** `.kiro/specs/large-payload-streaming-fast-path/evidence/20.3-20.4-rollout.md`
