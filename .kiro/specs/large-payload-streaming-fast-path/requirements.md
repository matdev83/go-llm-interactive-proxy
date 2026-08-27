# Requirements Document

## Introduction

Issue #503 asks Go-LIP to reduce heap growth, allocation pressure, and redundant JSON work for large request bodies by forwarding a verified same-wire request without constructing the full canonical request tree. This is a brownfield optimization of already-supported request flows, not a new proxy mode and not native provider passthrough (#490).

Correctness has priority over optimization. The existing materialize → shape-preflight → frontend-decode → canonical-core → backend-encode path is the behavioral oracle. A request may use the large-payload path only when Go-LIP can prove, before any provider request body byte is committed, that doing so preserves every enabled authority and every relevant frontend/core/backend behavior. When that proof is unavailable, incomplete, unsupported, or fails, the same request shall use the existing canonical path without weakening validation or silently disabling features.

## Boundary Context

- **In scope**: optional large-payload ingress capture; bounded-memory pre-commit spooling; streaming JSON validation and metadata extraction; explicit fast-path eligibility; same-wire backend capability proof; bounded token-aware model rewrite; retry/failover replay from the captured body; gzip-compatible follow-up support; cancellation/backpressure/resource cleanup; metrics; differential conformance tests; allocation/load benchmarks; conservative rollout.
- **Out of scope**: native provider passthrough (#490); cross-protocol forwarding; removing or weakening canonical validation; changing default request size limits; changing routing/failover semantics; changing response canonicalization; bypassing secret/DLP/guardrail policy; silently dropping configured hooks/observers/accounting; adding new request content encodings as a side effect; provider SDK types in core; broad capability-profile work owned by #495.
- **Boundary ownership**: shared frontend ingress owns capture/scanning and canonical fallback; core/runtime owns authoritative eligibility, identity/session lifecycle, routing, billing/admission, attempt sequencing, and no-post-output-failover; backend plugins own wire-compatibility declaration and outbound HTTP construction; SDK/public contracts contain only provider-neutral wire-body contracts needed across those boundaries; composition root freezes generation-scoped eligibility facts.
- **Optional hexagonal lens**: request-body proof and eligibility are domain policy; wire execution and fallback are app orchestration; HTTP frontend is the driving adapter; same-wire backend transport is a driven adapter; runtimebundle is the composition root.
- **Revalidation triggers**: changes to JSON-shape rules (#434), frontend decode normalization, secure-session preparation, request/billing authority, request/attempt transforms, routing/capability negotiation, traffic capture/redaction, failover/replay semantics (#476), backend wire contracts (#495), or response commit ownership.

## Requirements

### Requirement 1: Compatibility-First Activation and Lossless Fallback
**Objective:** As an operator, I want the optimization to be incapable of silently bypassing normal proxy behavior, so that enabling it does not trade correctness for memory savings.

#### Acceptance Criteria
1. When the large-payload optimization is not explicitly enabled, the system shall execute every request through the pre-existing canonical request path with no new body-spool, scanner, or wire-execution behavior.
2. When the optimization is enabled but any eligibility condition is unknown, unsupported, or false, the system shall execute the request through the existing canonical path and preserve the original request bytes needed by that path.
3. While a request is still being evaluated for fast-path use, the system shall not commit any request body byte to an upstream provider.
4. If an eligibility or spool decision changes after some client bytes have been consumed but before upstream commitment, the system shall make those consumed bytes available to the canonical decoder in original order and continue reading the remaining body exactly once.
5. The system shall not convert an otherwise valid canonical request into an error merely because the fast path is unavailable, its configurable resource budget is exhausted, a compatible backend is absent, or an optional optimization component declines the request, except when the original request itself violates an existing limit or the body can no longer be read because of an underlying I/O failure.
6. The system shall preserve existing frontend HTTP status/error mapping for malformed JSON, request-too-large, unsupported media/content encoding, authentication, policy denial, and canonical decode errors.
7. The system shall keep the existing `Execute(*lipapi.Call)` path as the behavioral baseline and shall not require unrelated frontends, tests, or external executor implementations to implement the new wire path.

### Requirement 2: Threshold and Existing Request-Admission Semantics
**Objective:** As an operator, I want large-payload consideration to be bounded by existing admission rules, so that the optimization does not broaden accepted traffic.

#### Acceptance Criteria
1. Where the feature is enabled, a configurable size threshold shall control whether a request is considered for the fast path; the threshold alone shall never establish eligibility.
2. When a known positive `Content-Length` is below the configured threshold, the system shall use the canonical path without first spooling the whole body.
3. When body length is unknown or chunked, the system shall preserve bounded memory usage while determining final size and shall use canonical processing if the final decoded body is below the threshold.
4. The system shall continue to enforce each frontend's effective `MaxRequestBodyBytes`/protocol limits and shall not increase the current default 8 MiB request limit as part of this feature.
5. When a configured request limit is larger than the historical default, the fast path shall enforce that configured limit rather than substituting its own smaller or larger acceptance limit.
6. If the request exceeds the effective existing maximum, the system shall return the same request-too-large class/status as the canonical path and shall not open an upstream request.

### Requirement 3: Streaming JSON Safety Parity
**Objective:** As a security-conscious operator, I want low-memory scanning to preserve existing JSON hardening, so that large bodies do not create a weaker parser path.

#### Acceptance Criteria
1. When a JSON request is considered for the fast path, the system shall validate the entire decoded JSON body before upstream commitment.
2. The streaming validator shall enforce the same effective request-envelope byte, depth, token, array-element, object-member, key-length, string-length, number-length, UTF-8, empty-body, incomplete-body, multiple-root-value, and trailing-data rules as the applicable canonical frontend path.
3. Where a protocol applies stricter rules than shared `jsonshape.RequestEnvelopeLimits`, including duplicate-member rejection or protocol-specific depth/count limits, the fast-path validator shall enforce the stricter protocol rule before declaring the request eligible.
4. The streaming validator shall not rely on `encoding/json.Decoder.Token` for large scalar strings if doing so would allocate the full scalar value; it shall be capable of validating and skipping bounded legal string values incrementally.
5. The validator shall check cancellation at bounded intervals while scanning large strings, arrays, and objects rather than only between top-level fields.
6. If streaming validation cannot reproduce a canonical parser rule for a particular request shape, the system shall classify that request as canonical-only before upstream commitment rather than approximating the rule.
7. Tests shall differentially compare streaming validation outcomes and error classes against current canonical/shared validators over malformed, adversarial, boundary, fuzz-generated, and representative production-shaped JSON corpora.

### Requirement 4: Bounded Metadata Extraction with Arbitrary JSON Ordering
**Objective:** As the routing core, I want only the metadata necessary for pre-commit decisions extracted without materializing conversation history, so that routing remains correct for large legal JSON documents.

#### Acceptance Criteria
1. The fast-path scanner shall extract only explicitly declared bounded metadata required by the selected frontend profile, including at minimum model/route input, client stream intent, operation identity, and protocol feature facts needed for safe candidate admission.
2. The scanner shall find required top-level fields regardless of their legal position in the JSON object and shall not assume `model`, `stream`, or other required fields occur in a fixed prefix.
3. The scanner shall distinguish top-level field names from identical strings or field names nested inside messages, tool arguments, schemas, or arbitrary content.
4. When shared canonical decoding uses last-wins semantics for duplicate member names, the scanner shall either reproduce that result for extracted metadata or classify the request as canonical-only when retaining duplicates on the upstream wire could change semantics.
5. When protocol decoding rejects duplicate names, the scanner shall reject the same duplicates before upstream commitment.
6. Metadata values retained in memory shall have explicit independent bounds; when an otherwise valid metadata value exceeds a fast-path extraction bound, the system shall fall back to canonical processing rather than truncate it.
7. The scanner shall preserve byte offsets for any field whose wire token may need a bounded rewrite, without retaining the surrounding large payload in memory.
8. Unknown or extension fields shall never be interpreted as routing/policy authority merely because they contain familiar names.

### Requirement 5: Explicit Eligibility Proof Across All Pre-Commit Authorities
**Objective:** As a maintainer, I want a single explainable eligibility decision, so that new features cannot accidentally be bypassed by a scattered collection of fast-path conditionals.

#### Acceptance Criteria
1. Before wire execution, the system shall evaluate a generation-pinned eligibility proof that accounts for every configured stage that can reject, reroute, inspect content, transform content, alter canonical conversation state, require token/content-derived admission, or require a materialized request representation.
2. If any enabled pre-commit stage requires a full canonical call and has no separately implemented wire-compatible contract, the request shall use canonical processing.
3. Content-bearing secret guards/DLP, request transforms, pre-request transforms, attempt transforms, conversation projection/steering, canonical submit hooks, content-derived route-hint providers, content-derived local-turn handlers, and equivalent future planes shall be canonical-only unless explicitly supported by a typed wire contract.
4. Metadata-only stages may remain compatible only when their existing behavior can be executed from trusted extracted metadata without fabricating canonical content.
5. Where traffic raw capture, redaction, or observation requires the complete request body as a `[]byte`, the request shall use canonical processing until that traffic consumer has an explicitly tested streaming contract.
6. Where request metering, billing, credit authorization, context estimation, or capability admission requires facts that cannot be derived exactly from bounded wire metadata, the request shall use canonical processing.
7. The system shall expose a bounded non-content eligibility/fallback reason suitable for metrics/debug diagnostics, such as disabled, below-threshold, frontend-unsupported, compressed-unsupported, canonical-stage-active, traffic-capture-active, billing-requires-canonical, route-plan-incompatible, backend-wire-incompatible, replay-incompatible, rewrite-unsafe, or validation-failed.
8. The eligibility proof shall be computed from the same immutable request generation/configuration that governs the request; a reload shall not change eligibility or backend wire contracts mid-request.

### Requirement 6: Canonical Path Preservation and Dedicated Wire Execution Use Case
**Objective:** As a maintainer, I want the optimization isolated from ordinary execution, so that canonical behavior remains stable and easy to compare.

#### Acceptance Criteria
1. The system shall not pass a fabricated or intentionally incomplete `lipapi.Call` through code that assumes a validated canonical request.
2. Where wire execution is used, core shall retain ownership of trace identity, principal/workspace resolution, secure-session/A-leg authority, route authority, request authority, routing, candidate ordering, B-leg creation, billing/usage lifecycle that can be satisfied without canonical content, and response stream assembly.
3. The existing canonical preparation sequence shall remain functionally unchanged except for refactoring that extracts shared metadata-only orchestration used by both paths.
4. Any shared refactor shall have characterization tests proving identical canonical outputs, stage order, error order, A-leg/B-leg lifecycle, and cleanup before the fast path is activated.
5. The wire use case shall return the same canonical `lipapi.EventStream` abstraction as ordinary execution so existing frontend response encoding, no-post-output-failover, response guardrails, and completion behavior remain in force.
6. If the wire use case cannot satisfy an existing mandatory core authority from the wire request facts, it shall decline before provider commitment and allow canonical fallback rather than skip the authority.

### Requirement 7: Backend-Authored Same-Wire Compatibility
**Objective:** As the routing core, I want backends to explicitly certify request-wire compatibility, so that provider identity or protocol names are never used as a proxy for equivalence.

#### Acceptance Criteria
1. A backend shall be eligible for wire execution only when it exposes an explicit provider-neutral request-wire capability matching the frontend wire profile, operation, and delivery mode.
2. `BackendTransportCaps` or “same provider family” alone shall not establish raw request-wire compatibility.
3. The core shall not contain provider-name switches to decide whether a request body may be forwarded.
4. A wire capability shall declare whether it accepts the incoming body unchanged, requires a bounded model-token rewrite, supports replay, supports compressed/decompressed source mode, and has any additional eligibility constraints needed before open.
5. When a route can select or fail over to multiple candidates before client-visible output, the fast path shall preserve the configured candidate semantics; it shall not silently prune an incompatible candidate merely to retain optimization.
6. If any candidate that can legally be used under the frozen route/recovery plan cannot consume the replayable wire body with equivalent semantics, the request shall use the canonical path unless the existing route semantics prove that candidate is unreachable for that request.
7. Initial support shall be additive and narrow: only backend implementations with explicit conformance tests may advertise the capability; all other internal and external backends remain canonical-only without behavior change.
8. Future #495 capability-profile integration may consume this contract, but #503 shall not depend on #495 being implemented first and shall not duplicate general model-feature capability tables.

### Requirement 8: Token-Aware Model Rewrite
**Objective:** As a routing core, I want route/model normalization to work on large opaque bodies, so that inline backend selectors and aliases do not leak into provider model IDs.

#### Acceptance Criteria
1. When the selected candidate's native model string differs from the effective client `model` token, the system shall perform only a token-aware rewrite of the actual top-level model JSON value identified by the validated scanner.
2. The rewrite shall work with legal arbitrary whitespace, member ordering, JSON string escaping, and model-token length changes without regex replacement or raw substring search.
3. The rewrite shall emit a valid JSON string for the candidate-native model and preserve every byte outside the identified rewrite span.
4. If duplicate top-level model fields, unexpected token types, ambiguous spans, or another condition makes exact rewrite equivalence uncertain, the system shall use canonical processing.
5. The system shall update outbound body length semantics correctly after rewrite and shall not send a stale client `Content-Length`.
6. Tests shall cover longer/shorter replacements, escaped client models, misleading `"model"` strings in content, nested model keys, whitespace variants, last-field placement, and adversarial duplicate-key cases.

### Requirement 9: Routing, Retry, Failover, Race, and Replay Preservation
**Objective:** As an operator, I want large requests to retain existing recovery behavior, so that lower allocations do not reduce availability.

#### Acceptance Criteria
1. Wire execution shall use the core-owned route selector/alias/native-model binding and B2BUA attempt lifecycle rather than moving candidate selection into frontends or backends.
2. Before first client-visible output, a retry/failover attempt shall receive a fresh reader beginning at byte zero of the validated/re-written request body.
3. The replayable body source shall support the number and concurrency of readers required by the exact selected route strategy before that strategy is declared fast-path eligible.
4. If an execution strategy requires parallel/racing request-body readers and the body source/backend contracts do not safely support them, the request shall use canonical processing; the system shall not silently convert a race to a sequential route.
5. After the first client-visible output, wire execution shall obey the same prohibition on retry/failover as canonical execution.
6. B-leg allocation, attempt sequence, candidate failure classification, TTFT budget, affinity/session routing state, and terminal accounting shall retain their canonical ownership and ordering.
7. A provider open/read failure before visible output shall remain eligible for the same recovery classes as the canonical backend path when the backend wire adapter classifies it equivalently.

### Requirement 10: Request Compression Compatibility
**Objective:** As an operator, I want compressed requests to remain safe and behaviorally compatible, so that decompression does not introduce bombs, leaks, or wire changes.

#### Acceptance Criteria
1. The first implementation wave shall preserve current gzip-only request-content-encoding support and shall route gzip requests through the canonical path until streaming decompression-to-spool parity is implemented and enabled.
2. The feature shall not add acceptance for Brotli, zstd, deflate, or any other request encoding that Go-LIP does not already accept.
3. When gzip fast-path support is added, the decompressed byte stream shall be subject to the same effective `MaxRequestBodyBytes` ceiling as canonical `reqbody.ReadAll` and shall be the stream validated/spooled for downstream use.
4. When gzip fast-path support is added, the outbound request shall match canonical effective semantics: the backend receives the validated uncompressed JSON body unless that backend's explicit wire contract proves preservation of compressed bytes is equivalent.
5. Cancellation or malformed gzip input shall close/release the decompressor and spool resources deterministically and map to the same error class as canonical processing.
6. Any pooling introduced for gzip readers/buffers shall reset all references before reuse and shall have tests preventing cross-request data retention.

### Requirement 11: HTTP Transport and Credential Semantics
**Objective:** As an operator, I want the low-copy body path to preserve normal outbound HTTP behavior, so that transport edge cases do not become correctness regressions.

#### Acceptance Criteria
1. A same-wire backend adapter shall construct its own target URL, provider credentials, required headers, client identity, and transport options using the backend's existing configuration; it shall not blindly forward client authorization or hop-by-hop headers.
2. The adapter shall preserve streaming versus non-streaming response selection and use the existing canonical response parser/event-stream path for provider responses.
3. The adapter shall set or omit `Content-Length` consistently with the actual replay/rewrite source and shall permit Go's HTTP transport to use chunked/H2 framing when length is unknown.
4. The system shall not forward hop-by-hop headers, stale content-encoding headers, stale transfer-encoding state, or client trailers unless an explicit contract says they are part of the supported wire profile.
5. `Expect: 100-continue`, HTTP/1.1, HTTP/2, connection reuse, cancellation, and redirect policy shall follow the same shared outbound client/security policy as existing backend calls.
6. A backend wire adapter shall disable hidden SDK-level request retries that would replay the body outside core ownership; retries remain core-owned.

### Requirement 12: Guardrails, Accounting, and Traffic Observability
**Objective:** As an operator, I want security and accounting features to remain authoritative, so that the optimization cannot create a monitoring or policy blind spot.

#### Acceptance Criteria
1. If an enabled secret/DLP/guardrail requires complete canonical content or a full-buffer body, the request shall use canonical processing.
2. A future incremental wire inspector may keep a request eligible only through a typed contract that states whether it is metadata-only, incremental/full-body streaming, transforming, or canonical-only and that preserves existing failure policy.
3. If billing/credit/routing admission requires exact request-token or semantic facts unavailable from wire metadata, the request shall use canonical processing rather than estimate differently.
4. Byte accounting that depends only on validated decoded body size may use the fast path and shall record the same logical ingress byte count as canonical processing.
5. Canonical traffic capture (`lip/canonical+json`) shall not be fabricated from an opaque body; if a configured observer requires that representation, the request shall use canonical processing.
6. Raw A-leg/body traffic capture and redaction shall force canonical processing until the traffic API supports a separately reviewed streaming representation with equivalent privacy/failure semantics.
7. Fast-path metrics/logs shall contain only bounded metadata and reason codes; they shall not log request content, model secrets, tool arguments, prompt prefixes, or spool file names containing user-derived data.

### Requirement 13: Cancellation, Backpressure, Resource Ownership, and Spool Safety
**Objective:** As an operator, I want large-body resource use to remain bounded and leak-free under concurrency and cancellation.

#### Acceptance Criteria
1. Ingress capture and outbound replay shall use bounded reusable copy buffers and shall not spawn one goroutine per body chunk.
2. Backpressure from client read, spool write, spool read, or provider write shall propagate through blocking I/O/context cancellation rather than unbounded queues.
3. The replayable body shall have one clear owner and deterministic close semantics covering in-memory buffers, temp files, gzip readers, open replay readers, and any reservation accounting.
4. Temp files shall be created with non-user-derived randomized names in a configured/private temp directory, shall not be exposed through diagnostics, and shall be removed on success, fallback completion, error, timeout, or cancellation.
5. The spool shall never persist beyond request lifetime for later reuse and shall not become a cache.
6. If a configured global spool budget cannot be reserved, the system shall prefer canonical fallback while the consumed prefix remains recoverable rather than silently exceeding the budget.
7. If a spill-to-disk creation/write step fails after bytes have been consumed, the system shall attempt canonical fallback by replaying the successfully retained prefix plus the current unread/remainder stream; only irrecoverable underlying I/O loss may terminate the request with a system error.
8. Race-enabled replay readers shall not share mutable seek state; each reader shall have independent read position.
9. Leak/goleak, cancellation, short-read/short-write, client-disconnect, provider-disconnect, and filesystem-error tests shall demonstrate cleanup.

### Requirement 14: Initial Protocol Coverage and Conservative Subset Proof
**Objective:** As a maintainer, I want protocol rollout to be explicit and independently certifiable, so that adding one compatible lane cannot affect unrelated frontends/backends.

#### Acceptance Criteria
1. An optional frontend wire profile shall be nil/absent by default; a frontend without a profile shall always use canonical processing.
2. The first certified lanes shall be limited to same-wire create operations whose frontend/backend pair has a differential conformance suite; recommended implementation order is OpenResponses→OpenResponses-compatible, OpenAI Responses→OpenAI-compatible Responses, then OpenAI Chat Completions→OpenAI-compatible Chat.
3. Anthropic, Gemini, cross-protocol matrices, compaction endpoints, WebSocket-specific request flows, continuation-materialization flows, and other unproven lanes shall remain canonical-only until separately certified under the same contract.
4. For OpenAI adapters that contain compatibility normalization such as malformed-history cleanup, reasoning aliases, legacy function-call handling, session metadata extraction, or extension capture, the wire profile shall either reproduce the relevant validation/normalization proof or mark those request shapes canonical-only.
5. A protocol profile shall maintain an explicit list of normalization-sensitive/disallowed fast-path constructs rather than depending on implementer intuition.
6. Differential tests shall run each certified request corpus once through the canonical encoder path and once through the wire path against a capture server, then compare effective provider request semantics and canonical response events.

### Requirement 15: Performance Evidence and No-Hot-Path Regression
**Objective:** As a performance maintainer, I want measured evidence that the complexity buys real memory/GC improvements without regressing ordinary traffic.

#### Acceptance Criteria
1. Before activation, benchmarks shall capture the canonical baseline and fast path for representative body sizes including 32 KiB, 256 KiB, 1 MiB, 5 MiB, and a larger configured-limit case such as 20 MiB where the test raises the normal request cap.
2. Benchmarks shall report at minimum allocations/request, allocated bytes/request, peak/steady RSS or heap under concurrency, GC count/pause, CPU/request, ingress-to-upstream-open latency, TTFT, throughput, copy volume where measurable, goroutine count, and cancellation cleanup.
3. Load scenarios shall include concurrency 1, 100, 1000, and the highest stable environment-supported concurrency, with a target scenario of 5000+ when the host can sustain it.
4. Variants shall cover unchanged body, model rewrite, canonical fallback, retry/replay, unknown/chunked length, gzip canonical fallback, and gzip fast path once implemented.
5. The fast path shall demonstrate materially lower heap allocation for eligible multi-megabyte bodies; if a certified lane does not reduce memory/GC pressure versus canonical processing, it shall not be enabled merely because the code path exists.
6. When the feature is disabled or the request is below threshold, benchmarks shall demonstrate no material allocation/latency regression attributable to the optimization gate.
7. Benchmarks shall not replace correctness gates; all parity, security, race, and leak tests must pass before performance results are considered.

### Requirement 16: Rollout, Diagnostics, and Architecture Ratchets
**Objective:** As an operator and maintainer, I want a reversible rollout and durable safeguards, so that future changes cannot silently widen an unsafe fast path.

#### Acceptance Criteria
1. The feature shall ship disabled by default in the first implementation release; enabling it shall require explicit configuration.
2. Configuration reload shall publish fast-path settings and body-access/compatibility facts as part of a new immutable generation; in-flight requests shall retain the generation they started with.
3. Metrics shall include considered, eligible, used, fallback, validation-failure, rewrite, replay, spool-memory/spill, spool bytes, and fallback-reason counters/histograms with bounded labels.
4. Debug diagnostics shall make it possible to determine why a request was canonical-only without including body content.
5. Architecture tests shall prevent provider-name branching in core, prevent frontends from taking ownership of routing/failover, and prevent an opaque body from being coerced into a fake canonical `lipapi.Call`.
6. Adding a new frontend/backend wire profile shall require its capability declaration, protocol proof tests, canonical differential corpus, cancellation/replay tests, and benchmark evidence.
7. `make quality-checks`, `make test`, `make parity-checks`, and `make qa` shall pass before the feature is declared implementation-ready for merge.
