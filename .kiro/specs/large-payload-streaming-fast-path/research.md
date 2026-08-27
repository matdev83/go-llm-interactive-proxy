# Research & Design Decisions

## Summary

- **Feature**: Large-Payload Streaming Fast Path (#503)
- **Discovery Scope**: Brownfield complex integration / performance optimization with security, routing, accounting, and retry constraints.
- **Repositories inspected**:
  - Go-LIP `matdev83/go-llm-interactive-proxy` at `main` (discovery snapshot around `8684666809f687ab43dc1393dc1f726a0b0161f7`).
  - Bifrost `maximhq/bifrost` on `dev`, including its large-payload middleware, router integration, provider body forwarding, tests, and metadata schema.
- **Key findings**:
  1. The issue's pointer to `internal/jsonbody/jsonbody.go` is not the LLM ingress hot path. Standard LLM frontends flow through `frontendpipe` → `reqbody.ReadAll` → `jsonguard/jsonshape` → protocol-specific decoder. `internal/jsonbody` is used by admin/management-style handlers and should not be modified for #503.
  2. Go-LIP cannot safely copy Bifrost's “large mode = skip request parser/hooks and carry selected metadata” behavior. Go-LIP's core and frontend decoders perform significantly more semantic normalization, policy, traffic, session, billing, conversation, routing, and attempt work over a real `lipapi.Call`.
  3. A true immediate client→provider zero-copy pipe is incompatible with #503's own pre-commit invariant when the current proxy must validate the whole JSON body before provider execution and required metadata may appear at the end. The safe optimization is therefore **bounded-memory pre-commit capture/spooling plus low-copy replay**, not “send while scanning.” This still removes the dominant full-body heap copies and canonical object graph for eligible requests.
  4. Existing JSON hardening uses `encoding/json.Decoder.Token`, which materializes decoded string tokens. A new streaming scanner must validate strings/escapes incrementally instead of simply wrapping `Decoder.Token` around an `io.Reader`.
  5. Existing frontend decoders contain compatibility behavior (for example malformed-history cleanup and extension capture in OpenAI adapters). Same protocol names are therefore insufficient for proof. Each frontend/backend lane needs an explicit wire-equivalence profile and differential corpus.
  6. Core routing/failover remains authoritative. The fast path needs a new narrow wire-execution use case; passing a fake/minimal `lipapi.Call` through `Executor.Execute` is unsafe because `Call.Validate`, secure preparation, traffic capture, projection, transforms, capability derivation, and billing assume a real canonical request.
  7. Bifrost's most reusable ideas are: threshold-before-full-parse, replayable body reader carried in context, small metadata skeleton, streaming decompression, and provider-side model normalization. Its unsafe-to-copy details for Go-LIP are: parser/hook skipping by mode and bounded-prefix/raw-substring model rewriting.

## Research Log

### 1. Actual Go-LIP ingress path

**Sources consulted**
- `internal/plugins/frontends/frontendpipe/pipe.go`
- `internal/plugins/frontends/reqbody/body.go`
- `internal/plugins/frontends/jsonguard/guard.go`
- `internal/core/jsonshape/preflight.go`
- `internal/core/jsonshape/profiles.go`
- `internal/plugins/frontends/routeselect/routeselect.go`
- `internal/jsonbody/jsonbody.go`

**Findings**
- `frontendpipe` is the common LLM request path. It currently reads the complete decoded request into `[]byte`, resolves body-derived routing, preflights JSON, decodes the protocol into a canonical call, validates it, captures ingress traffic, and then invokes the executor.
- `reqbody.ReadAll` applies `http.MaxBytesReader`; for gzip it creates a gzip reader and applies the same cap to the **decompressed** stream before `io.ReadAll`. This is an important security contract to retain.
- `routeselect.FromModelOrDefault` separately `json.Unmarshal`s the full body into a model probe, producing another whole-document decode pass.
- `jsonguard` is a compatibility shim over `internal/core/jsonshape`; new reusable shape logic belongs in `jsonshape` rather than adding a second policy implementation in the frontend helper.
- `internal/jsonbody` also uses full-buffer reading but its callers are admin/management endpoints, not the shared LLM hot path. Changing it does not solve #503 and risks unrelated behavior.

**Implications**
- The feature entry point should be optional behavior in `frontendpipe`/`reqbody`, not a rewrite of each handler and not a change to `internal/jsonbody`.
- Frontends that do not opt into a wire profile remain bit-for-bit on the old path.
- Existing request byte limits must stay authoritative.

### 2. JSON-shape semantics and why `Decoder.Token` is insufficient

**Sources consulted**
- `internal/core/jsonshape/preflight.go`
- `internal/core/jsonshape/profiles.go`
- #434 security-hardening context referenced by #503.

**Current request-envelope rules**
- Default request max: 8 MiB unless the frontend/server supplies a different configured limit.
- Depth 128, token budget 1,000,000, array elements 100,000, object keys 100,000, key bytes 16 KiB, number bytes 1 MiB, string bytes bounded by request/part limits.
- Request-envelope duplicate member names are currently allowed (`RejectDuplicateNames=false`) to preserve `encoding/json` last-wins compatibility; stricter protocols may impose their own duplicate policy later in decode.
- `PreflightWithContext` counts every `Decoder.Token()` result, including `{`, `}`, `[`, `]`, keys, and scalars; it validates root cardinality, complete closure, and trailing data.

**Critical implementation observation**
`encoding/json.Decoder.Token()` returns a Go `string` for JSON strings. A single multi-megabyte message/base64 content string therefore still materializes the entire decoded scalar even if the source is an `io.Reader`. Replacing `bytes.Reader` with a streaming reader would not satisfy the memory objective.

**Implications**
- Implement a small purpose-built streaming lexical/state scanner in `internal/core/jsonshape`, with differential tests against the existing preflight implementation.
- It must validate UTF-8, escapes including `\uXXXX`/surrogate correctness, number grammar, literal grammar, separators, object-key/value state, depth/count/token limits, and decoded string byte length without storing the whole string.
- It may materialize only bounded field names and explicitly requested metadata values.
- The existing slice-based preflight remains unchanged and remains the fallback oracle.

### 3. Go-LIP canonical core is not skippable infrastructure

**Sources consulted**
- `pkg/lipsdk/executor_view.go`
- `pkg/lipapi/call.go`
- `internal/core/runtime/executor.go`
- `internal/core/runtime/executor_prepare_request.go`
- `internal/core/runtime/executor_prepare.go`
- `internal/core/runtime/executor_prepare_secure.go`
- `internal/core/runtime/executor_route_plan.go`
- `internal/core/runtime/billing_admission.go`
- `internal/core/extensions/snapshot.go`

**Findings**
- `ExecutorView.Execute(ctx, *lipapi.Call)` is the current frontend→core seam.
- `lipapi.Call.Validate()` requires actual canonical content (`Messages` or item authority) and validates tools/options/reasoning invariants. A metadata-only dummy call is invalid by design.
- Secure preparation currently performs principal/workspace/session resolution and A-leg/route-authority binding, then also runs content-dependent work: secret guard, frontend ingress metering capture, request-authority admission, submit hooks, canonical CTP traffic capture, conversation snapshot/projection/filtering, and later request/pre-request/attempt stages.
- `buildRoutePlan` derives request-size/capability/failover facts from the canonical call and owns affinity, TTFT, attempt budget, and recovery controller.
- Billing interfaces may invoke account/pricing/token estimation callbacks with the full call.
- `RequestRuntimeSnapshot` already provides the correct generation-pinned place to freeze extension-plane occupancy and compatibility facts.

**Implications**
- Add a second optional `WireExecutorView`; do not change `ExecutorView.Execute` semantics or force every mock/integration to implement a new method.
- Refactor only metadata/identity work that can be proven common; keep canonical-specific preparation in the canonical path.
- The wire path must decline when a configured stage still requires `*lipapi.Call`.
- The response remains canonical events so frontend response behavior does not fork.

### 4. Canonical decoders perform semantic normalization

**Sources consulted**
- `internal/plugins/frontends/openailegacy/decode.go`
- `internal/plugins/frontends/openairesponses/decode.go`
- `internal/plugins/protocols/openresponses/decode_request.go`

**OpenAI Chat observations**
- The decoder trims/requires `model`, requires messages, validates metadata and session carriers, parses tool structures, and captures extension fields.
- It deliberately skips certain malformed replay history fragments such as unnamed tool calls/orphan artifacts instead of failing the request.
- It supports aliases/compatibility shapes such as reasoning fields and legacy function-call forms that are not simply byte-preserving.

**OpenAI Responses observations**
- Similar compatibility cleanup exists for malformed function history.
- String/array input forms and reasoning/tool items are normalized into canonical messages/parts.
- Text/reasoning/extension fields are split between canonical options and extension preservation.

**OpenResponses observations**
- Its protocol decoder is stricter and closer to a round-trippable canonical wire, but still performs authority conflict checks, background rejection, unknown-field policy, item/tool validation, continuation handling, instruction projection, reasoning/text-control validation, and canonical validation.

**Implications**
- A same-named frontend/backend is not automatically raw-wire equivalent.
- Initial wire profiles need a conservative **normalization-sensitive construct matrix**. If a request uses a construct the lightweight validator cannot prove semantically equivalent to canonical decode→encode, it falls back.
- Differential wire tests are mandatory and should intentionally include canonical normalization cases to prove they fall back rather than slip through.

### 5. Traffic capture and observability can force materialization

**Sources consulted**
- `pkg/lipsdk/traffic/emit.go`
- `frontendpipe` ingress capture call sites
- secure executor CTP canonical capture.

**Findings**
- `traffic.PortBundle.Emit` accepts a complete `[]byte`; raw capture/redaction/observer execution is therefore currently full-buffer by contract.
- Secure core may marshal the canonical call as `lip/canonical+json` for CTP capture.
- Faking canonical traffic from opaque wire bytes would be incorrect and could leak/provider-couple diagnostics.

**Implications**
- In V1, any non-noop request-body traffic bundle that needs full payload or canonical CTP capture is an eligibility blocker.
- Add a future typed streaming traffic contract only as separate work inside later tasks if needed; do not overload `Emit` with reader lifetime semantics in the first lane.
- Eligibility/fallback metrics are metadata-only and safe.

### 6. Backend boundaries and capability evidence

**Sources consulted**
- `internal/core/execbackend/backend.go`
- `pkg/lipapi/transport.go`
- `pkg/lipsdk/backendplugin/interfaces.go`
- `internal/plugins/backends/openaicompat/backend.go`
- `internal/plugins/backends/openresponsescompat/backend.go`
- issue #495.

**Findings**
- Internal `execbackend.Backend.Open` currently accepts a canonical call; there is no raw-body open seam.
- `BackendTransportCaps` declares operation + streaming/non-streaming support, not request wire-shape equivalence.
- OpenAI-compatible backends use the OpenAI SDK and core-generated canonical calls; a wire adapter must construct provider HTTP requests itself (or through a body-stream-capable helper) while retaining credential pools, base URL, shared HTTP client, response parsing, and pre-output failure classification.
- OpenResponses-compatible backend similarly builds requests from canonical data.
- External backend plugin `ConfiguredInstance.Execute` is DTO-stream-based; broad external raw-wire ABI work should not be forced into #503's first implementation.
- #495 is still a feature request for general versioned model/deployment capabilities. #503 can define a narrowly scoped request-wire compatibility contract now and later project it into #495 rather than block on #495.

**Implications**
- Add an optional backend field/interface for wire request capability/open. Absence means canonical-only.
- Core asks the backend; no `if backend == openai` switches.
- First support only bundled backends with dedicated tests. External plugins stay canonical-only.

### 7. Bifrost large-payload design

**Sources consulted**
- `maximhq/bifrost/dev/core/providers/openai/large_payload.go`
- `maximhq/bifrost/dev/core/providers/utils/utils.go`
- `maximhq/bifrost/dev/transports/bifrost-http/handlers/middlewares.go`
- `maximhq/bifrost/dev/transports/bifrost-http/integrations/router.go`
- `maximhq/bifrost/dev/transports/bifrost-http/integrations/router_large_payload_test.go`
- `maximhq/bifrost/dev/core/schemas/bifrost.go`
- `maximhq/bifrost/dev/core/providers/utils/utils_test.go`

**Useful patterns**
- Large-payload mode is selected before normal request parser work.
- The transport can retain a body reader and selected metadata instead of a full parsed request.
- `LargePayloadMetadata` carries a small set of facts (`Model`, stream intent, response modalities, speech config).
- Provider code can attach a body stream directly.
- Request decompression can be streamed and pooled.
- Tests explicitly assert that normal parser work is skipped in large-payload mode.

**Patterns not safe to copy literally**
- Bifrost's large mode intentionally skips ordinary request parser/plugin-body handling. Go-LIP cannot do that implicitly because those stages may be mandatory authorities.
- Bifrost's model rewrite searches a bounded prefix (256 KiB) for the `model` member and performs a bespoke byte rewrite. #503 explicitly requires arbitrary legal member ordering, so a model after that prefix must still work. A raw-prefix search can also confuse nested/content strings without a full lexical state proof.
- Bifrost supports gzip/deflate/br/zstd request decompression. Go-LIP currently supports gzip only; adopting all Bifrost encodings would be an unrelated API behavior expansion.

**Implications**
- Borrow the architecture idea, not the exact shortcuts.
- Go-LIP needs a full streaming lexical scan to EOF before provider commit and a token-offset rewrite.
- Compression support must preserve existing accepted encoding set.

### 8. Why a pre-commit spool is necessary

**Reasoning**
The following four constraints hold simultaneously:
1. Current Go-LIP rejects malformed/over-limit JSON before provider execution.
2. Required metadata such as `model` can legally appear at the end of the object.
3. #503 forbids provider body commitment before all rejection/routing/transform/replay authorities are resolved.
4. We want to avoid retaining the complete request in heap memory.

If the scanner must read to EOF before opening the provider, every byte consumed before EOF must remain recoverable for the provider or canonical fallback. Therefore some replayable storage is required. A bounded-memory spool that spills to a temporary file is the least invasive cross-platform solution.

**Implications**
- V1 optimizes **heap/RSS/GC and parse/canonicalization copies**, not necessarily time-to-upstream-request-start.
- A direct tee from client to provider is explicitly forbidden in V1 because it would violate pre-commit validation.
- The spool also provides clean replay for pre-output retries/failovers.

### 9. Replay/failover implications

**Sources consulted**
- `internal/core/runtime/executor_route_plan.go`
- current recovery controller/attempt ownership referenced from executor.
- issue #476 for ongoing failure-class improvements.

**Findings**
- Current core may retry/fail over before visible output and owns attempt budget/TTFT/affinity/B-leg state.
- Route strategies may be sequential or racing/parallel.
- A single `io.Reader` is insufficient; replay requires a reader factory over immutable captured bytes.

**Implications**
- `ReplayableBody.Open()` returns independent readers.
- For a file-backed spool, use independent file descriptors or `SectionReader` over a stable descriptor with no shared seek state.
- A fast-path plan is eligible only if every candidate/strategy the current plan may use can consume the wire profile; do not prune or serialize configured recovery merely for optimization.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| Direct client→upstream tee | Validate/extract while simultaneously sending | Lowest copy and latency | Violates pre-commit rejection semantics; cannot find late metadata safely; fallback/replay impossible | Rejected |
| Full `[]byte` + skip canonical decode | Read body as today but avoid canonical tree | Easy, lower CPU | Does not solve dominant large-body heap/RSS allocation | Rejected as insufficient |
| `encoding/json.Decoder.Token` over reader | Stream preflight using stdlib token API | Simple, familiar | Large strings are materialized; memory objective not met | Rejected for scanner core |
| Bounded-memory spool + custom scanner + replay | Scan to EOF, spill large bytes to file, then replay | Preserves pre-commit semantics, bounded heap, supports retry | Adds temp-file I/O and lifecycle complexity | **Selected** |
| Frontend-only shortcut | Frontend chooses backend and sends raw body | Fewer core changes | Violates routing/failover/core ownership, skips authorities | Rejected |
| Dedicated core wire use case | Frontend supplies validated metadata + replayable body; core proves eligibility and routes | Preserves ownership, explainable fallback | Requires careful shared-preparation refactor | **Selected** |
| Provider-name compatibility switch | Core knows OpenAI/OpenResponses names | Quick | Provider policy leaks into core; unscalable, unsafe | Rejected |
| Backend-authored wire profile | Optional provider-neutral capability on backend | Correct ownership, additive | New contract/testing | **Selected** |

## Design Decisions

### Decision 1: Optimization unit is a replayable pre-commit body, not a live stream
- **Selected approach**: capture/scanner consumes the client request once into a bounded-memory spool; provider attempts open independent readers only after full validation and eligibility.
- **Rationale**: only design that satisfies full validation, arbitrary metadata ordering, no early upstream commitment, low heap, and retries simultaneously.
- **Trade-off**: eligible requests may incur disk I/O before provider open. This is acceptable because #503 targets memory/GC stability under high concurrency; benchmarks will quantify latency.

### Decision 2: Ship disabled by default
- **Selected approach**: `server.large_payload_fast_path.enabled: false` by default.
- **Rationale**: optimization touches security/routing/replay boundaries. Explicit opt-in provides a reversible first release while parity/load evidence accumulates.
- **Trade-off**: operators must enable it to receive benefit. A future issue may revisit the default after production evidence; #503 does not silently flip it.

### Decision 3: Keep canonical `Executor.Execute` intact and add an optional wire executor
- **Selected approach**: a provider-neutral optional SDK seam such as `WireExecutorView.ExecuteWire(ctx, requestbody.Request)`.
- **Rationale**: avoids breaking existing frontend tests/executor implementations and prevents fake canonical calls.
- **Trade-off**: some orchestration must be factored into shared helpers and wire-specific preparation state.

### Decision 4: Canonical-content extension stages are blockers until explicitly adapted
- **Selected approach**: generation build derives a `RequestBodyAccessSummary` from enabled extension planes and core features. Existing stages receiving `*lipapi.Call` default to `CanonicalRequired`.
- **Rationale**: absence of an explicit wire contract is not proof of safety.
- **Trade-off**: initial eligibility may be narrow. This is deliberate; coverage can expand by adding typed incremental contracts later.

### Decision 5: Differential parity is part of capability certification
- **Selected approach**: no backend/frontend pair advertises a wire profile until a corpus compares canonical provider request semantics with the wire request and demonstrates canonical fallback for normalization-sensitive shapes.
- **Rationale**: static “same protocol” labels cannot capture compatibility cleanup or extension projection.

### Decision 6: Model rewrite uses scanner offsets
- **Selected approach**: the scanner records the exact top-level model value byte span. The replay body can expose an optional splice reader that copies `[0:start]`, emits `json.Marshal(nativeModel)`, then copies `[end:]`.
- **Rationale**: arbitrary order/whitespace/escaping works and no full body mutation is needed.
- **Duplicate model policy**: because request-envelope duplicates are generally last-wins but raw duplicate forwarding could diverge from canonical re-encode, duplicate top-level `model` makes the request canonical-only in V1.

### Decision 7: Gzip is staged after uncompressed parity
- **Selected approach**: gzip immediately remains accepted through the canonical path. Later task adds streaming gzip→decompressed spool using the same decompressed ceiling and scanner.
- **Rationale**: avoids mixing two high-risk changes in the first certification lane and preserves current encoding surface.

### Decision 8: No broad external backend-plugin ABI change in the first wave
- **Selected approach**: optional wire-open capability starts on internal `execbackend.Backend`/bundled backends. SDK request-body types are provider-neutral, but external plugin execution remains DTO/canonical unless a later ABI extension is justified.
- **Rationale**: #503 can deliver value without destabilizing the external plugin protocol.

## Proposed Initial Certification Matrix

| Frontend operation | Backend family | Initial status | Important canonical-only triggers |
|---|---|---|---|
| OpenResponses create | OpenResponses-compatible create | First target | continuation materialization, unsupported standard controls, active canonical stages, incompatible route candidates |
| OpenAI Responses | OpenAI-compatible Responses | Second target | normalization-sensitive malformed history, body session metadata, unsupported aliases/controls not proven by validator, active canonical stages |
| OpenAI Chat Completions | OpenAI-compatible Chat | Third target | malformed-history cleanup cases, legacy `function_call`, reasoning aliases unless explicitly proven, body session metadata, active canonical stages |
| Anthropic | any | Canonical-only in this spec | not certified |
| Gemini | any | Canonical-only in this spec | not certified |
| any cross-protocol pair | any | Canonical-only | requires translation/canonicalization |
| compaction | any | Canonical-only | different operation semantics |
| WebSocket request ingress | any | Canonical-only | separate transport lifecycle |

The table defines implementation order, not permission to “best effort” unsupported shapes. Each row changes from canonical-only only after its tests pass.

## Risks & Mitigations

- **Semantic drift from canonical decoder** — conservative profile; differential corpus; explicit normalization-sensitive fallback; default disabled.
- **Security drift from #434 JSON rules** — scanner differential tests and one shared `jsonshape` implementation; protocol-specific stricter adapters.
- **Temp filesystem exhaustion** — configurable spool budget; bounded memory prefix; canonical fallback while prefix remains recoverable; no user-derived filenames; deterministic cleanup.
- **Fast path skips new future feature** — generation body-access summary defaults unknown/new canonical stages to `CanonicalRequired`; architecture tests require declaration update.
- **Retry changes** — immutable replay source and route-plan compatibility proof; no candidate pruning; no retry after output.
- **Credential/header leakage** — backend constructs outbound headers from its own configuration; no blind client-header forwarding.
- **Hidden SDK retries** — wire adapters use shared HTTP client with retries owned by core; provider SDK automatic retry is not allowed on the wire path.
- **Allocation moved from heap to parser strings** — custom scalar scanner, bounded metadata buffers, allocation benchmarks around giant single strings.
- **Cancellation leaks temp files/readers** — single owner, `defer Close`, goleak and injected failure tests.
- **Feature complexity** — staged implementation with canonical characterization gates before enabling any lane; no simultaneous all-protocol rollout.

## Brownfield Gap Analysis and Requirement Repair

### Gaps discovered against the actual codebase
1. **Issue wording referenced the wrong hot-path helper**: `internal/jsonbody` is not used by shared LLM ingress. Requirements were adjusted to target `frontendpipe`/`reqbody` and explicitly leave admin JSON behavior outside scope.
2. **Original “streaming” wording could imply immediate upstream streaming**: whole-body JSON validation + arbitrary field ordering + no early provider commit makes that unsafe. Requirements now explicitly require pre-commit replayable capture and define the performance goal as low heap/copies rather than premature provider start.
3. **A metadata-only canonical call is impossible**: `Call.Validate` and executor stages require real content. Requirements now require a dedicated wire use case and forbid fake canonical calls.
4. **Traffic capture is a materialization authority**: current traffic ports consume `[]byte`. Requirements now make such configured consumers eligibility blockers until separately adapted.
5. **Billing/capability routing may consume full canonical facts**: requirements now require exact derivability or canonical fallback.
6. **OpenAI decoders normalize malformed history**: requirements now include per-protocol equivalence validators and differential certification instead of a blanket same-wire rule.
7. **#495 is not a landed dependency**: requirements now use a narrow backend wire profile without depending on the broader future capability-profile feature.
8. **Compression surface differs from Bifrost**: requirements now preserve Go-LIP's gzip-only accepted encoding set and stage gzip fast-path support after plain JSON parity.

### Requirements gate result
**PASS after repair.** The requirements now match current architecture, state explicit non-regression behavior, avoid depending on unfinished #495/#476 work, and describe fallback for every discovered current-state blocker.

## References

### Go-LIP
- https://github.com/matdev83/go-llm-interactive-proxy/issues/503 — feature request and invariant source.
- https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/plugins/frontends/frontendpipe/pipe.go — shared LLM ingress pipeline.
- https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/plugins/frontends/reqbody/body.go — current full-body/gzip admission.
- https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/core/jsonshape/preflight.go — current JSON-shape oracle.
- https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/core/runtime/executor_prepare_secure.go — secure/current canonical preparation authorities.
- https://github.com/matdev83/go-llm-interactive-proxy/blob/main/internal/core/runtime/executor_route_plan.go — core route/recovery ownership.
- https://github.com/matdev83/go-llm-interactive-proxy/blob/main/pkg/lipsdk/traffic/emit.go — full-buffer traffic consumer contract.
- https://github.com/matdev83/go-llm-interactive-proxy/issues/495 — future general capability profiles; not a blocker.
- https://github.com/matdev83/go-llm-interactive-proxy/issues/476 — failover classification follow-up; not a blocker.
- https://github.com/matdev83/go-llm-interactive-proxy/issues/490 — distinct native passthrough feature.

### Bifrost
- https://github.com/maximhq/bifrost/blob/dev/core/providers/openai/large_payload.go
- https://github.com/maximhq/bifrost/blob/dev/core/providers/utils/utils.go
- https://github.com/maximhq/bifrost/blob/dev/transports/bifrost-http/handlers/middlewares.go
- https://github.com/maximhq/bifrost/blob/dev/transports/bifrost-http/integrations/router.go
- https://github.com/maximhq/bifrost/blob/dev/transports/bifrost-http/integrations/router_large_payload_test.go
- https://github.com/maximhq/bifrost/blob/dev/core/schemas/bifrost.go
- https://github.com/maximhq/bifrost/blob/dev/core/providers/utils/utils_test.go
