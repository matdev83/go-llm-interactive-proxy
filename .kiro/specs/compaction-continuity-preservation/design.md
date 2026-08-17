# Design Document

## Overview

Implement issue #344 as a bounded compaction-continuity feature that preserves decision state without replacing coding-agent compaction or creating a parallel inference/billing stack.

The design has seven cooperating pieces:

1. the existing `compaction-event-detection` detector/rule authority, implemented first and refactored only enough to provide a pure request preview;
2. a separate additive `pkg/lipsdk/compaction.Preserver` content-bearing extension surface;
3. a process-owned bounded background auxiliary collector under `internal/core/auxreq`;
4. a typed detached auxiliary execution/session mode that preserves principal/billing identity but not primary session-turn effects;
5. a process-owned branch coordinator that serializes revision/job/injection state across runtime generations;
6. an official `internal/plugins/features/compactioncontinuity` implementation for capsule schema, deterministic plan carriers, sanitizer, extractor prompt/schema, merge and injection;
7. existing routing, B2BUA, usage/metering and BillingCallID accounting reused unchanged as execution authorities.

The semantic extractor runs independently from the primary coding turn and may use a completely different model/provider. It is nevertheless a normal canonical Go-LIP child call, so the originating user is billed for actual extractor usage by default.

## Goals

- Preserve accepted/current plan state and explicit user product/architecture decisions across repeated lossy compactions.
- Make structured planning state deterministic and LLM-free where possible.
- Run semantic extraction off-session and in background, with bounded worker/resource ownership.
- Allow a separately configured cheap/fast extractor route independent of the primary session route.
- Attribute auxiliary usage/cost to the originating authenticated user through existing billing authorities.
- Prevent the first post-compaction turn from outrunning required continuity state.
- Keep provider-native encrypted/opaque compaction content byte-identical.
- Keep the feature bounded, fail-open and observable without logging decision content.

## Non-Goals

- general long-term/RAG memory;
- storing every conversation fact;
- replacing native/agent compaction;
- a general durable job framework;
- a second transcript database;
- a second financial ledger/rating engine;
- direct provider clients for the extractor;
- mutating `compaction.Observer`;
- provider-specific continuity branches inside core;
- transparent restart durability when no durable authorized source exists.

## Dependency and Implementation Order

The runtime implementation of `.kiro/specs/compaction-event-detection/` is a prerequisite.

Chronological implementation order is therefore:

```text
compaction-event-detection runtime
        |
        +--> pure request preview extracted from same matcher/fingerprint authority
        |
        v
compaction-continuity-preservation
```

Continuity implementation must not recreate the #312 agent-signature table as a temporary fallback.

## Existing Architecture Reused

- `lipapi.Call`, `NormalizedItems` and canonical item/tool/message forms provide provider-neutral source data.
- `lipapi.OperationContextCompaction` / `ItemKindCompaction` provide canonical strict semantics where supported.
- `internal/core/compactiondetect` (from prerequisite) owns compaction evidence, rule IDs, A-leg transactions and history heuristic.
- `pkg/lipsdk/feature.FeatureBundle` / `internal/featurebundle` / `extensions.RequestRuntimeSnapshot` provide additive extension composition.
- `internal/core/auxreq` already delegates auxiliary calls through the normal Executor, clones principal/scope, marks internal origin and supports plugin suppression.
- `pkg/lipsdk/genpin` already defines `KindAsync` for request-spawned asynchronous generation ownership.
- `ProcessServices` owns process lifetime and `ExtensionState` across generation reloads.
- secure-session recording already provides an optional durable transcript when explicitly enabled.
- billing account identity already comes from authenticated principal scope; child calls naturally get independent BillingCallIDs.

## Target Architecture

```text
 PRIMARY USER TURN / COMPACTION                       PROCESS-OWNED AUXILIARY PLANE

 canonical request
       |
       v
 shared detector PreviewRequest (pure) -----> preservation BeforeRequest
       |                                         |  pending capsule/job?
       |                                         |  bounded Await if required
       |                                         +--> canonical reinjection
       v
 normal prepare / route / billing / Open
       |
       +-- Open succeeds ---------------------> detector RequestOpened / transaction
       |                                         |
       |                                         +--> Preserver.RequestOpened
       |                                               |
       |                                               +--> deterministic harvest
       |                                               +--> sanitize delta
       |                                               +--> BackgroundAux.SubmitCollect
       |                                                        |
       |                                                        v
       |                                                bounded worker pool
       |                                                        |
       |                                                detached child Executor
       |                                                        |
       |                                                independent route/model
       |                                                        |
       |                                                normal usage/billing
       |                                                        |
       |                                                strict JSON result
       |                                                        |
       |                                                validate + branch merge
       |
 primary compaction stream proceeds concurrently
       |
 final selected event
       |
 detector derives completion metadata (no mutation)
       |
 preservation BeforeResponseRelease
       |---- bounded Await if useful ----------------------------+
       |---- verified plaintext carrier? -> mechanical augment
       |---- opaque/late? -> mark pending reinjection
       v
 compaction metadata observers
       v
 client release
```

## D1. Shared Detector Preview Versus Committed Detection

The prerequisite detector currently has committed request semantics conceptually equivalent to:

```go
func (d *Detector) RequestOpened(meta RequestMeta, call lipapi.Call) []compaction.Event
```

Add a pure preview API or internal equivalent:

```go
type RequestPreview struct {
    Evidence compaction.Evidence
    RuleID   string
    Kind     PreviewKind // none | start_candidate | completion_candidate
    // Opaque deterministic fingerprint/token for revalidation; no raw content.
}

func (d *Detector) PreviewRequest(meta RequestMeta, call lipapi.Call) RequestPreview
```

Properties:

- same rule/fingerprint implementation as committed detection;
- read-only with respect to transaction/fingerprint state;
- emits no observer event;
- cannot be used as proof that an upstream compaction started;
- useful only to let `BeforeRequest` identify a completion-only/installed-compaction boundary before opening the first post-compaction B-leg.

`RequestOpened` remains the only request-side state-commit point and remains after successful `Open`.

## D2. Public Preservation Extension Contract

Extend the `pkg/lipsdk/compaction` package introduced by the prerequisite with a distinct preservation surface. Exact names may change, but responsibilities shall remain separate.

Conceptual contract:

```go
type PreservationMeta struct {
    TraceID       string
    SessionID     string
    ALegID        string
    BLegID        string
    AttemptSeq    int
    TransactionID string
    RuleID        string
    Evidence      Evidence
}

type Services struct {
    State         ContinuityState
    BackgroundAux auxiliary.BackgroundClient
}

type Preserver interface {
    ID() string
    BeforeRequest(
        context.Context,
        *lipapi.Call,
        RequestPreview,
        PreservationMeta,
        Services,
    ) error

    RequestOpened(
        context.Context,
        lipapi.Call,
        []Event,
        PreservationMeta,
        Services,
    ) error

    BeforeResponseRelease(
        context.Context,
        *lipapi.Event,
        []Event,
        PreservationMeta,
        Services,
    ) error
}
```

`BeforeRequest` may mutate only the canonical request through the documented continuity injection path. `RequestOpened` is observation/source/job scheduling after successful Open. `BeforeResponseRelease` may mutate only a verified plaintext continuation carrier.

`FeatureBundle` gains a separate optional `CompactionPreservers []compaction.Preserver` slice. The single merge surface concatenates it in registration order and the request snapshot exposes a frozen defensive copy.

Do **not** add request/response content to `compaction.Event` or change `Observer` into a decision/mutation interface.

## D3. Process-Owned Background Auxiliary Collector

### SDK surface

Keep synchronous `auxiliary.Client` source-compatible. Add an additive interface:

```go
type JobID string

type SubmitOptions struct {
    CoalesceKey string
    Timeout     time.Duration
}

type BackgroundClient interface {
    SubmitCollect(ctx context.Context, req Request, opts SubmitOptions) (JobID, error)
    Await(ctx context.Context, id JobID) (lipapi.Collected, error)
    Forget(id JobID)
}
```

`DisabledBackgroundClient` provides explicit not-configured behavior.

### Core implementation

Create a focused scheduler under `internal/core/auxreq` (names indicative):

```text
BackgroundScheduler
  fixed/bounded worker count
  bounded input queue
  keyed coalescing map
  bounded result map + TTL
  process root context
  closeOnce + WaitGroup
```

It executes canonical auxiliary calls only; it does not accept arbitrary Go callbacks/functions.

### Submit-time ownership transfer

`SubmitCollect` executes synchronously until ownership is safely transferred:

1. validate cloneable child call and options;
2. resolve current generation's `ExecutorRunner` through the generation snapshot cell;
3. obtain request `genpin.Retainer` and `Retain(genpin.KindAsync)`;
4. clone required principal/scope/correlation context into an immutable job envelope;
5. reserve/coalesce the job in the process scheduler;
6. enqueue or fail atomically;
7. on enqueue failure, release the newly retained pin immediately.

A worker never attempts to acquire a fresh generation spawn right later. The captured pin is released exactly once after terminal child execution/collection or cancellation.

### Job context

Worker context derives from the scheduler/process root plus copied safe execution attribution, not from the parent request's cancellation tree. Per-job timeout then bounds model work.

Preserve:

- principal/scope;
- internal origin;
- parent trace/session/A-leg correlation;
- submission-time generation identity.

Do not preserve:

- parent cancellation lifetime as root;
- primary secure-session authority token/turn authority;
- primary A-leg route override authority.

### Result lifetime

Raw `lipapi.Collected` output is retained only until awaited/parsed, with explicit count/byte/TTL limits. `Forget` or expiry deletes it. Logs/metrics expose only job ID hash/status/size/token metadata.

## D4. Detached Auxiliary Session Mode

Current auxiliary execution delegates through the normal Executor, whose standard path performs secure-session BeginTurn and primary request authority work. Add a typed execution policy to `auxiliary.Request` or an internal sibling type:

```go
type SessionMode uint8
const (
    SessionModeNormal SessionMode = iota
    SessionModeDetached
)
```

The exact enum location may differ, but `Detached` is core-interpreted execution metadata and never provider-visible call content.

Detached semantics:

- authenticated principal/scope is preserved;
- parent IDs remain correlation metadata;
- no primary secure-session BeginTurn/new TurnID;
- no primary transcript/activity/turn-count mutation;
- no primary resume-token/session-authority propagation;
- no primary A-leg route-override lookup/application;
- no client-visible session headers/history;
- child still receives normal independent trace, route plan, B-legs, usage, metering and billing.

The implementation should factor the Executor prepare path rather than copy the whole executor. For detached calls, execute only the identity/scope + ordinary execution authorities that remain relevant.

Security invariant: `SessionModeDetached` may only originate from trusted internal auxiliary code; frontends cannot request it from wire fields.

## D5. Independent Extractor Routing

Feature config requires either:

- explicit `extractor.route`, or
- explicit `extractor.route_policy: inherit`.

Default should not accidentally inherit the main route.

The extractor builds a canonical child call with its own `Route.Selector`. Normal routing then owns aliases, selector grammar, safe execution-composition policy, health, retry/failover and B-leg opening.

Parent A-leg override is not consulted in detached mode.

The child is stamped:

```text
Role       = "compaction_continuity_extractor"
Visibility = "private"
Origin     = internal auxiliary
```

Tools are empty/disabled and tool choice is none/valid equivalent.

## D6. Originating-User Billing and Workload Classification

The child enters the normal Executor with cloned authenticated `scope`. Existing billing identity therefore resolves the same customer account.

Every extraction gets:

- separate BillingCallID;
- separate B-leg attempt sequence;
- separate usage records;
- separate operational exposure/customer settlement;
- separate provider COGS.

No special charging function is added.

Add a bounded classification to execution/accounting evidence, conceptually:

```go
type WorkloadClass string
const (
    WorkloadPrimary WorkloadClass = "primary"
    WorkloadAuxiliary WorkloadClass = "auxiliary"
)

AuxRole = "compaction_continuity_extractor"
```

Prefer reusing existing auxiliary lineage metadata as the source and project it into metering/billing/report DTOs where needed. Do not put raw capsule/source text into money records.

Primary frontend usage projection remains tied to the primary BillingCall/response only. Aggregate account/operator reports include both calls and can group auxiliary cost.

If the child is rejected before upstream submission, the feature records a fail-open extraction outcome and does not bypass admission. If provider work was submitted, later invalid/stale extraction does not erase actual usage/cost.

## D7. Process-Owned Continuity Branch Coordinator

A per-generation feature mutex is insufficient because immutable generation reload can overlap with in-flight workers/turns. `ExtensionState` is process-owned but its current `Get` + `Put` API has no atomic compare-and-swap.

Create a **small process-owned continuity coordinator** in `internal/core/compactioncontinuity` (name indicative) and build it in `ProcessServices` when the capability is enabled/available. It is not a general plugin framework.

Responsibilities:

- serialize updates per authoritative branch key;
- use process `ExtensionState` as backing storage for serializable state;
- provide revision-checked load/update operations to preservation services;
- track pending job IDs and injection watermarks;
- enforce max entries/TTL with lazy/opportunistic cleanup;
- contain no extractor prompts, provider code or billing logic.

Conceptual branch key:

```go
type BranchKey struct {
    AuthoritativeSessionID string // when available
    ALegID                 string
    PrincipalPartition     string // used when no secure session id
}
```

Client session hints do not participate as authority.

Conceptual state:

```go
type BranchState struct {
    Revision                  uint64
    CapsuleJSON               json.RawMessage
    CapsuleDigest             [32]byte
    SourceHighWatermark       string
    SanitizedSourceJSON       json.RawMessage
    PendingJobID              auxiliary.JobID
    PendingJobTargetRevision  uint64
    PendingInjectionRevision  uint64
    LastInjectedRevision      uint64
    LastCompactionTransaction string
    UpdatedAt                 time.Time
}
```

The coordinator can expose opaque capsule/source JSON to the plugin so core does not own semantic fact interpretation.

## D8. Continuity Capsule Schema

Feature-owned v1 model (exact field names may evolve under tests):

```json
{
  "schema_version": 1,
  "revision": 12,
  "source_high_watermark": "...",
  "plan": {
    "status": "accepted",
    "source": "structured|user_acceptance|semantic",
    "steps": [
      {"id":"...","text":"...","status":"pending","source_ref":"..."}
    ]
  },
  "decisions": [
    {
      "id":"...",
      "statement":"...",
      "status":"active",
      "authority":"user_explicit|user_acceptance|semantic",
      "rationale":"...",
      "source_ref":"..."
    }
  ],
  "constraints": [],
  "rejected_alternatives": [],
  "open_questions": []
}
```

### Merge precedence

Highest authority / latest source wins:

1. later explicit user correction/decision;
2. later explicit user acceptance/selection;
3. authoritative deterministic structured-plan update;
4. validated semantic inference;
5. older capsule state.

Semantic inference may propose new facts/status transitions but cannot resurrect a superseded/rejected fact against newer explicit user evidence.

Fact IDs are stable across revisions. Existing IDs supplied in the previous capsule should be reused by the extractor when updating a fact. New deterministic carrier facts get deterministic IDs from type + canonical normalized content/source; new semantic facts get normalized content/category IDs only after validation.

### Bounded retention

When over budget, deterministic pruning order is:

1. preserve active constraints/decisions;
2. preserve pending/in-progress plan steps;
3. preserve associated rationale needed for those facts;
4. preserve meaningful current rejections;
5. condense completed plan history;
6. drop oldest superseded/irrelevant history.

Never truncate JSON in the middle; prune complete facts and revalidate.

## D9. Deterministic Plan Carrier Catalog

Place semantic carrier parsing in `internal/plugins/features/compactioncontinuity`, not provider/core adapters.

Static versioned table with explicit canonical matchers:

```text
codex.update_plan.v1
opencode.todo.v1
cline.task_progress.v1
```

Implementation research must pin actual canonical tool/item shapes from supported harness snapshots before enabling each rule. A rule requires table-driven positive and near-miss tests.

No generic “markdown checklist = accepted plan” rule.

The detector's compaction RuleID may be recorded for diagnostics, but carrier matching should work from canonical tool shapes rather than infer a provider brand.

## D10. Sanitized Extraction Source

Source builder walks the effective canonical call and produces a bounded structured envelope, not a raw wire dump.

Priority:

1. user messages/text relevant to decisions;
2. assistant plan/proposal/clarification text needed to interpret user replies;
3. recognized structured plan/TODO carriers;
4. previous capsule.

Default drops:

- ordinary tool results;
- command output;
- compiler/test logs;
- large source/code/file dumps;
- images/video/binary/file blobs;
- reasoning payloads not needed for explicit user decisions;
- unrelated external text.

Tool/external content that must be included is tagged `untrusted_external` and delimited.

Size policy should use deterministic bytes/estimated tokens and prefer tail + decision-bearing user turns rather than arbitrary truncation.

Every successfully opened primary request may refresh the process-local **sanitized** source window/high-watermark. Failed pre-open requests never become committed source state.

## D11. Semantic Extractor Child Call

Build one canonical call with:

- configured independent route;
- fixed system/developer instructions describing the extraction task;
- previous capsule JSON;
- deterministic plan facts;
- sanitized context delta;
- no tools;
- bounded max output;
- preservation plugin disabled;
- detached session mode;
- role `compaction_continuity_extractor`.

Expected output is one strict JSON object, e.g.:

```json
{
  "schema_version": 1,
  "base_revision": 11,
  "facts": [...],
  "plan_updates": [...],
  "remove_or_supersede": [...]
}
```

Validation happens before BranchCoordinator merge:

- JSON only / one top-level object;
- schema version exact;
- base revision expected or stale-discard path;
- byte/depth/item/count/string bounds;
- enum values exact;
- no unknown authority escalation;
- no raw tool/blob fields;
- valid source references only within supplied bounded input when required.

Malformed output is discarded fail-open.

## D12. Semantic Eligibility Gate

The local gate exists to save cost, not to decide truth.

Candidate signals may include:

- recognized structured plan carrier changed;
- assistant substantial plan followed by user affirmative/corrective turn;
- explicit user choice/correction/constraint language;
- previous capsule absent while compaction source contains planning markers;
- new relevant user turns after capsule high-watermark.

Near misses must avoid triggering solely on ordinary words such as `plan`, `summary`, or `continue`.

If deterministic state fully satisfies the preservation categories for the new delta, skip the LLM job.

## D13. Strict Remote-Compaction Flow

### Request preview

Before normal B-leg Open:

- detector preview may identify a strict start candidate;
- preservation may prepare/sanitize state but **must not submit a strict-start semantic job yet**;
- existing pending reinjection from a prior completed compaction is handled here.

### Successful Open

After primary compaction B-leg successfully opens:

1. detector commits `started` transaction;
2. preservation commits the sanitized request source;
3. deterministic carrier extraction updates capsule if possible;
4. eligibility gate decides whether semantic extraction adds value;
5. if yes, `BackgroundAux.SubmitCollect` submits exactly one coalesced job;
6. primary compaction stream continues without waiting.

### Completion release

At final selected canonical event:

1. detector derives completion metadata without mutating the event;
2. preservation checks the matching transaction/job;
3. if a bounded Await completes and output validates, merge new capsule revision;
4. if event has a verified mutable plaintext continuation carrier, append/merge a versioned continuity block mechanically;
5. otherwise set `PendingInjectionRevision`;
6. dispatch metadata-only compaction observers;
7. release event to client.

Await timeout/error is fail-open and leaves the job/result state available only according to bounded policy. A late valid job may still merge the capsule for post-compaction reinjection, subject to revision checks.

## D14. Completion-Only / Local-Compaction Flow

For local compaction where no strict pre-compaction start was visible, the first post-compaction request may be the first evidence.

Before its B-leg opens:

1. detector `PreviewRequest` identifies a completion candidate using the previous successful request fingerprint/current rewrite;
2. preservation loads the previous sanitized source/capsule;
3. deterministic carrier delta is applied;
4. if semantic extraction is required and no coalesced job exists, submit it to BackgroundAux;
5. bounded Await barrier waits only up to configured `barrier_timeout`;
6. ready capsule is injected into the current canonical request;
7. on timeout/failure, continue fail-open without invalid output;
8. after successful Open, detector commits completion-only state normally.

Although the main request waits at the narrow barrier, the extractor still executes independently on the background worker and its own model route/session/billing lifecycle.

## D15. Authority-Aware Reinjection

Create one canonical helper owned by the feature/core extension layer, not frontend protocols.

For message-authoritative calls:

- add a bounded proxy-owned developer/system instruction using the existing legal instruction/message representation.

For item-authoritative calls:

- add a canonical message item with equivalent role/content;
- do not simultaneously populate legacy `Messages`/`Instructions` because `Call.Validate` forbids mixed authorities.

Continuity block format conceptually:

```text
[LIP CONTINUITY CAPSULE v1 | revision=12]
The following facts were preserved from earlier accepted session state after context compaction.
Treat them as prior context, not as a new user request.
...
[/LIP CONTINUITY CAPSULE]
```

The serialized textual projection is derived deterministically from the capsule and rechecked against the injection budget.

`LastInjectedRevision` makes the operation idempotent for a boundary.

## D16. Plaintext Result Augmentation Boundary

Do not assume `ItemKindCompaction` is text.

Define a small allowlisted capability/matcher for a **verified plaintext continuation carrier**. If no safe carrier is known, do not mutate the response.

Never write:

- `CompactionItem.EncryptedContent`;
- `CompactionItem.Opaque`;
- provider signatures/opaque reasoning;
- unknown extension blobs.

Tests compare opaque/encrypted payload bytes exactly with feature on/off.

The reinjection fallback is functionally sufficient; result augmentation is an optimization/stronger immediate carry-through only for proven-safe carriers.

## D17. Per-Session Policy

Global config comes from the standard feature-plugin opaque YAML config.

Trusted session-level override resolution order:

```text
operator hard maxima / safety policy
        > trusted per-session policy value
        > global feature default
```

Allowed session overrides may include:

- enabled/disabled;
- preserve-category toggles;
- extractor route chosen from operator-allowed selectors;
- tighter token/timeout/barrier limits.

Client-provided headers/session metadata do not directly produce these trusted values. No new unauthenticated control header is introduced.

## D18. Configuration

Indicative config only; concrete defaults are pinned during implementation tests:

```yaml
plugins:
  features:
    - id: compaction-continuity
      enabled: true
      config:
        preserve:
          plan: true
          user_decisions: true
          constraints: true
          rationale: true
          rejected_alternatives: true
        extractor:
          route: "openai-responses:small-model"
          timeout: 8s
          max_input_tokens: 12000
          max_output_tokens: 2000
          max_concurrency: 2
          queue_capacity: 16
          result_ttl: 30s
        barrier_timeout: 2s
        max_capsule_tokens: 2500
        source_ttl: 2h
        failure_mode: fail_open
```

Validation rules:

- semantic extraction requires explicit route or explicit inherit;
- positive finite queue/concurrency/result/source/capsule bounds;
- barrier <= job timeout or explicitly documented independent behavior;
- per-session values cannot exceed hard global maxima;
- invalid config fails startup/generation compile rather than silently enabling expensive defaults.

## D19. Failure Semantics

Default is fail-open for model traffic.

| Failure | Behavior |
|---|---|
| no candidate state | skip semantic call |
| background queue full | record saturation; deterministic capsule/native compaction continue |
| generation retain fails | no job; fail-open |
| child billing/admission denied | no provider bypass; deterministic state/native compaction continue |
| extractor route/provider fails | child normal retry rules; then fail-open |
| extractor timeout | job fails; no malformed injection |
| schema invalid | discard raw output; account actual submitted usage |
| job stale | discard/forget result; do not overwrite capsule |
| barrier timeout | continue native request/compaction; late valid result may merge if still current |
| capsule over budget | deterministic whole-fact pruning |
| state/coordinator unavailable | skip preservation mutation; native traffic continues |
| process shutdown | stop admission, cancel/join worker, settle normal submitted usage paths |

A future explicit fail-closed policy may be considered separately; v1 should not make a UX preservation feature a default model-traffic availability dependency.

## D20. Privacy and Security

- Feature default disabled.
- Remote extractor route is explicit data egress.
- Use existing redaction/secret handling before child egress.
- Do not send full raw session automatically.
- Treat all transcript/source as quoted untrusted data.
- Child has no tools and preservation plugin is suppressed.
- Never log extractor prompt/output/capsule.
- Raw completed background result lives only in bounded TTL process memory until parse/forget.
- Preserve principal/tenant/workspace authorization for any optional transcript read.
- Do not turn transcript capture on implicitly.
- Child detached-session mode is trusted internal-only, not frontend-controlled.

## D21. Observability

Add content-free metrics/diagnostics:

- compaction preview/start/completion counts by evidence/rule;
- deterministic carrier hits by versioned carrier ID;
- semantic eligibility skip reasons;
- background jobs submitted/coalesced/saturated/canceled/completed/failed/stale;
- worker queue depth/in-flight count;
- extractor route/backend/model identifiers where existing policy allows;
- extractor input/output/cache token usage and child cost via existing usage/billing;
- capsule revision, serialized size, active fact/plan-step counts;
- barrier wait count/duration/timeouts;
- result augmentation versus reinjection counts;
- stale/CAS conflict rejection;
- opaque-carrier no-mutation path count.

Normal logs expose IDs/hashes/counts, never preserved text.

## D22. Lifetime and Concurrency Invariants

1. ProcessServices owns scheduler and branch coordinator.
2. Generation snapshot owns only non-owning handles to process services.
3. Each background job owns exactly one retained `KindAsync` pin after successful submission.
4. Job pin is released exactly once on every terminal/cancel/enqueue-failure path.
5. A job never obtains a new generation after its submit boundary.
6. Branch coordinator serializes revision/injection/job state across overlapping old/new generations.
7. Worker callbacks never run while coordinator internal locks are held.
8. Provider/model work never runs under a global branch-state mutex.
9. Job/coordinator maps have explicit bounds/TTL and no unbounded timer goroutines.
10. Config reload changes only future job configuration.

## D23. Process Restart and Durable Resume

V1 guarantees:

- continuity across compaction within a process;
- continuity across immutable generation reload;
- no cross-branch state leakage.

V1 does **not** guarantee capsule persistence across process restart.

If secure-session transcript capture already exists and future/implementation scope wires an authorized transcript-source adapter, the feature may reconstruct the capsule on demand after restart. The adapter must be read-only, bounded and principal/session authorized. Otherwise the missing capsule is explicit fail-open state.

Do not add a durable job queue or full transcript store under this spec.

## D24. Expected Change Surface

Primary implementation packages:

- `pkg/lipsdk/compaction` — preservation contract alongside prerequisite observer contract;
- `pkg/lipsdk/feature` — additive preserver slice;
- `pkg/lipsdk/auxiliary` — additive BackgroundClient/job types and detached session policy carrier;
- `internal/featurebundle` / `internal/core/extensions` — merge/frozen snapshot/dispatch stages;
- `internal/core/compactiondetect` — pure preview extracted from same rule authority;
- `internal/core/auxreq` — bounded background collector and detached execution handoff;
- `internal/core/compactioncontinuity` — narrow process branch coordinator;
- `internal/core/runtime` — request-open/final-release/detached prepare wiring;
- `internal/infra/runtimebundle` — ProcessServices ownership and snapshot clients;
- `internal/plugins/features/compactioncontinuity` — capsule, carrier rules, sanitizer, eligibility, extractor, merge, injection, config;
- existing metering/billing/report DTO/projection locations only as needed for content-free auxiliary workload classification;
- focused architecture/testkit fixtures.

Packages that should **not** need provider-specific edits:

- individual backend providers/connectors;
- individual frontend protocol codecs;
- provider SDK adapters for compaction continuity semantics.

## Testing Strategy

TDD order:

1. RED capsule merge/supersession/pruning and structured carrier fixtures.
2. RED sanitizer/eligibility/extractor-schema tests.
3. RED BackgroundAux submit-time pin/coalescing/saturation/shutdown/goleak tests.
4. RED detached-session tests proving no primary secure-session/route-override effects.
5. RED separate-route + user-billing/BillingCallID/protocol-usage tests.
6. RED compaction integration tests for successful-Open trigger, completion-only preview, parallel job, bounded barrier, plaintext/opaque behavior.
7. RED three-plus repeated-compaction/concurrency/generation-reload tests.
8. GREEN minimal implementation in the same order.
9. Repository quality, race, architecture and simplification gates.

No real model credentials are required. Extractor behavior uses deterministic fake/local backends returning fixture JSON.

## Design Invariants

1. One compaction recognition authority; no duplicate signature matrix.
2. Metadata observer remains non-mutating and content-free.
3. Extractor is off the primary session but billed to the originating user by default.
4. Extractor route is explicit and independent unless explicit inherit is configured.
5. Background job ownership is captured before the request spawn right ends.
6. ProcessServices, not a generation plugin lifecycle, owns worker lifetime.
7. Auxiliary child does not create/modify a primary secure-session turn.
8. Normal routing/B2BUA/usage/billing own child execution; no direct provider path.
9. Primary protocol-visible usage excludes extractor usage; account totals include it.
10. Continuity state is bounded, revisioned, branch-scoped and generation-reload-safe.
11. Opaque/encrypted compaction bytes are immutable.
12. First post-compaction turn has a bounded synchronization barrier when required.
13. No second transcript DB, money ledger, generic workflow engine, or redundant LLM summary pass.
