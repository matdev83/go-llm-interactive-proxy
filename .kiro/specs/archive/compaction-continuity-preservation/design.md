# Design Document

## Overview

Implement issue #344 as a bounded compaction-continuity feature that preserves decision state without replacing coding-agent compaction or creating a parallel inference/billing stack.

The design has seven cooperating pieces:

1. the `compaction-event-detection` detector/rule authority, implemented first and refactored only enough to provide pure request/response previews;
2. a separate additive `pkg/lipsdk/compaction.Preserver` content-bearing extension surface;
3. a process-owned bounded background auxiliary collector under `internal/core/auxreq`;
4. a typed detached auxiliary execution mode that preserves principal/billing identity while using a private child A-leg and no primary session-turn effects;
5. a process-owned branch coordinator serializing revision/job/injection state across runtime generations;
6. an official `internal/plugins/features/compactioncontinuity` implementation for capsule schema, deterministic plan carriers, sanitizer, extractor prompt/schema, merge and injection;
7. existing routing, B2BUA, usage/metering and BillingCallID accounting reused as execution authorities.

The semantic extractor runs independently from the primary coding turn and may use a completely different model/provider. It is nevertheless a normal canonical Go-LIP child call, so actual extractor usage is billed to the originating authenticated user/account by default.

## Goals

- Preserve accepted/current plan state and explicit user product/architecture decisions across repeated lossy compactions.
- Make structured planning state deterministic and LLM-free where possible.
- Run semantic extraction off-session/background with bounded process ownership.
- Allow a separately configured extractor route independent of the primary session route.
- Attribute auxiliary usage/cost to the originating user through existing billing authorities.
- Prevent the first post-compaction turn from outrunning already-ready or deterministically recoverable continuity state without submitting fresh billable provider work before its primary Open.
- Keep provider-native encrypted/opaque compaction content byte-identical.
- Keep the feature bounded, fail-open and content-safe in observability.

## Non-Goals

- general long-term/RAG memory;
- storing every conversation fact;
- replacing native/agent compaction;
- a general durable job/workflow framework;
- a second transcript database;
- a second financial ledger/rating engine;
- direct provider clients for the extractor;
- mutating `compaction.Observer`;
- provider-specific continuity branches in core;
- claiming restart durability without an authorized durable source.

## Dependency and Composition Gate

`.kiro/specs/compaction-event-detection/` is a hard runtime prerequisite.

```text
compaction-event-detection runtime
        |
        +--> pure request/response previews from the same matcher/fingerprint authority
        |
        v
compaction-continuity-preservation
```

When the continuity feature is **enabled**, generation/process composition verifies that detector preview/commit services, process BranchCoordinator and BackgroundAux are available. Missing prerequisites fail generation/startup composition with a clear error. When the feature is disabled, these requirements are no-op/compatible and no extractor configuration is required.

Continuity must never recreate the #312 signature matrix as a fallback.

## Existing Architecture Reused

- `lipapi.Call`, canonical messages/items/tools and normalized walkers provide provider-neutral source data.
- prerequisite `internal/core/compactiondetect` owns compaction evidence, rule IDs, A-leg transactions and history heuristic.
- `FeatureBundle` / single merge surface / request snapshot provide additive extension composition.
- `internal/core/auxreq` delegates child calls through the normal Executor, clones principal/scope, marks internal origin and suppresses plugins.
- `pkg/lipsdk/genpin.KindAsync` already models request-spawned asynchronous generation ownership.
- `ProcessServices` owns process lifetime and process `ExtensionState` across generation reloads.
- secure-session recording already provides optional durable transcript data when explicitly enabled.
- billing account identity derives from authenticated principal scope; independent child calls receive independent BillingCallIDs.

## Target Architecture

```text
 PRIMARY / COMPACTION FLOW                         PROCESS-OWNED AUXILIARY PLANE

 canonical request
       |
       v
 detector PreviewRequest (pure)
       |
       +--> Preserver.BeforeRequest
              | pending capsule/job?
              | bounded Await only for previously submitted work
              +--> canonical reinjection (transactional helper)
       |
 normal prepare / route / billing / Open
       |
       +-- Open succeeds
              |
              +--> detector RequestOpened (commit)
              +--> Preserver.RequestOpened
                       | deterministic harvest / sanitizer
                       +--> BackgroundAux.SubmitCollect --------+
                                                                 |
 primary stream continues                                        v
                                                          bounded workers
                                                                 |
                                                        detached child call
                                                                 |
                                                        private child A-leg
                                                                 |
                                                        independent selector
                                                                 |
                                                        normal B-legs/billing
                                                                 |
                                                        strict JSON result
                                                                 |
                                                        bounded result registry
       |
 final selected event
       |
 detector PreviewResponse (pure)
       |
 Preserver.BeforeResponseRelease
       |---- bounded Await --------------------------------------+ 
       |---- validate/merge capsule
       |---- safe plaintext? mechanical augment
       |---- opaque/late? pending reinjection
       |
 detector ResponseReleased(final event) (commit)
       |
 metadata-only compaction observers
       |
 client release
       |
 successful release commits branch-level reinjection watermark
```

## D1. Pure Preview Versus Committed Detector State

The prerequisite detector remains the one recognition authority.

Add pure internal/public-to-core preview shapes (exact names may differ):

```go
type RequestPreview struct {
    Evidence Evidence
    RuleID   string
    Kind     PreviewKind // none | start_candidate | completion_candidate
    TransactionID string // only when safely derivable from existing active state
    BoundaryFingerprint string // stable detector-owned preview identity when no transaction exists
}

type ResponsePreview struct {
    Evidence Evidence
    RuleID   string
    Kind     PreviewKind // none | completion_candidate
    TransactionID string
}

func (d *Detector) PreviewRequest(meta RequestMeta, call lipapi.Call) RequestPreview
func (d *Detector) PreviewResponse(meta ResponseMeta, ev lipapi.Event) ResponsePreview
```

Preview properties:

- same matcher/fingerprint logic as committed detection;
- no transaction/fingerprint mutation;
- no lifecycle event emission;
- no observer dispatch;
- request preview cannot establish a billable semantic-extraction start;
- completion-only preview can expose a stable boundary fingerprint for a non-billable intent before a committed transaction exists;
- response preview lets preservation identify the matching job before final response mutation.

Committed boundaries remain:

- `RequestOpened` only after successful upstream Open;
- `ResponseReleased` only after all permitted preservation finalization, on the exact event sent to the client.

## D2. Preservation Extension Contract

Extend the `pkg/lipsdk/compaction` package introduced by #312 with a distinct preservation surface.

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
    BeforeRequest(context.Context, *lipapi.Call, RequestPreview, PreservationMeta, Services) error
    RequestOpened(context.Context, lipapi.Call, []Event, PreservationMeta, Services) error
    BeforeResponseRelease(context.Context, *lipapi.Event, ResponsePreview, PreservationMeta, Services) error
}
```

Semantics:

- `BeforeRequest`: pre-open pending reinjection / completion-only barrier; may mutate only through the continuity injection helper.
- `RequestOpened`: successful-open source commit and background job scheduling; current primary request has already been sent upstream.
- `BeforeResponseRelease`: pure-preview-guided bounded join and verified result-side augmentation before committed `ResponseReleased`.

The interface remains `error`-returning, but **preserver errors are not primary-traffic errors**. Core dispatch owns the following composition rule:

1. invoke each preserver behind panic isolation;
2. on callback error/panic, emit only content-free preservation diagnostics and continue native traffic;
3. `BeforeRequest` and `BeforeResponseRelease` mutation runs against a transactional helper/snapshot so callback error, panic, or post-mutation canonical validation restores the exact pre-preservation `Call`/`Event` before continuing;
4. `RequestOpened` failure cannot undo the already-opened primary request and therefore only records preservation failure/cleans feature-local pending state;
5. preservation failure never becomes route/failover/no-retry authority and never masquerades as a provider failure.

A practical implementation can clone the bounded canonical mutation target before dispatch or make the continuity helper return an undo/commit handle; do not add a general transaction framework.

`FeatureBundle` gains `CompactionPreservers []compaction.Preserver`; the single merge surface concatenates in registration order and the runtime snapshot exposes a frozen defensive copy.

No request/response content is added to `compaction.Event` and `Observer` remains unchanged.

## D3. Process-Owned Background Auxiliary Collector

### Additive SDK capability

Keep synchronous `auxiliary.Client` source-compatible. Add a narrow interface:

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

`DisabledBackgroundClient` returns explicit not-configured errors.

### Scheduler implementation

Create focused `internal/core/auxreq` background collection support:

```text
BackgroundScheduler
  bounded worker count
  bounded queue
  coalescing map
  bounded result registry
  process root context
  closeOnce + WaitGroup
```

It accepts only canonical auxiliary model collection jobs, never arbitrary Go callbacks/tasks/timers.

The scheduler only sees **committed billable job identities**. A completion-only preview that has no committed transaction is represented in BranchCoordinator as a non-billable `PreviewIntentKey`; it is not inserted into the scheduler coalescing map and does not retain a generation pin. After successful primary Open, the preview intent binds to the committed transaction and produces the normal scheduler key:

```text
coalesce_key = H(parent_branch_binding | committed_transaction_id | target_source_revision)
```

An empty transaction ID is invalid for a new billable continuity submission.

### Submit-time ownership transfer

`SubmitCollect` synchronously:

1. validates/clones the child request;
2. resolves the current generation `ExecutorRunner` from the request snapshot cell;
3. obtains `genpin.Retainer` and `Retain(KindAsync)` while spawn authority is live;
4. clones required principal/scope/correlation attribution plus the **captured parent continuity branch binding** as opaque lineage;
5. reserves/coalesces the committed job;
6. enqueues or fails atomically;
7. releases a newly retained pin immediately on failed handoff.

A worker never attempts a later generation retain. The captured pin is released exactly once after terminal child collection/cancel.

The captured parent branch binding is not execution authority for the child. It exists so feature logic can associate pending results with the primary continuity branch; the private child A-leg created later must never replace it.

### Worker context

Worker context derives from the scheduler/process root and copied attribution, not from parent request cancellation. A per-job timeout bounds inference.

Preserve principal/scope, internal origin, parent correlation, captured parent continuity branch binding and captured generation. Do not preserve primary resume/session authority or primary A-leg route authority.

### Result retention

Raw `lipapi.Collected` is count/byte bounded and never logged. A result referenced by a BranchState `PendingJobID` remains available for the configured bounded pending-continuity retention window, not merely a tiny cache TTL. First successful consume validates/merges then calls `Forget` immediately. Branch/job expiry clears both sides coherently. No result outlives the configured bounded continuity/source retention horizon.

## D4. Detached Auxiliary Execution and Private Child A-Leg

Add trusted auxiliary execution policy (exact type location may differ):

```go
type SessionMode uint8
const (
    SessionModeNormal SessionMode = iota
    SessionModeDetached
)
```

This is carried on the trusted auxiliary request object after frontend decode; no wire field/header maps to it.

Detached child flow:

```text
parent continuity BranchKey = Session/A-leg A123 (captured before submit)
       |
       +---- opaque lineage only --------------------------------+
                                                                  |
originating principal/scope                                      |
       |                                                          |
       v                                                          |
private auxiliary logical call                                   |
       |                                                          |
       +--> create/touch private child A-leg AUX456               |
       |       parent_a_leg=A123 only in lineage                  |
       |                                                          |
       +--> normal route selector / request authority / BillingCallID
       |
       +--> one or more child B-legs
       |
       +--> normal terminal usage/billing

continuity Await/merge/revision/reinjection always use captured parent BranchKey,
never AUX456.
```

Detached semantics:

- preserve authenticated principal/scope for security and billing;
- parent SessionID/A-leg/trace are correlation only for child execution but remain the separately captured authority for continuity-state lookup;
- do not call primary secure-session BeginTurn or create primary TurnID;
- do not mutate primary transcript/activity/turn counts;
- do not propagate primary resume authority;
- do not fetch/apply primary A-leg route override;
- create/use a **private child A-leg** through existing B2BUA lifecycle/store semantics;
- keep child A-leg/B-legs private/internal but fully usable by existing request authority/billing machinery;
- no client-visible session/history/header effects.

Implementation should factor shared Executor prepare logic rather than clone an entire second executor.

## D5. Independent Extractor Routing

Feature config requires either explicit `extractor.route` or explicit `route_policy: inherit`.

The plugin builds a canonical child `Call.Route.Selector`; normal routing owns parsing, aliases, safe composition policy, health, failover and B-leg opening. The private child A-leg has no parent route override state, so the primary A-leg override cannot hijack the extractor.

Child attribution:

```text
Role       = compaction_continuity_extractor
Visibility = private
Origin     = internal auxiliary
```

Tools are absent/disabled; tool choice is none/legal equivalent.

Submission captures route/timeouts/token bounds/failure policy immutably. Reload changes only future jobs.

## D6. Originating-User Billing and Workload Classification

Detached execution retains original authenticated scope, so existing billing identity resolves the same account.

Every extraction has:

- independent BillingCallID;
- private child A-leg and independent B-leg attempts;
- positive authoritative `AttemptSeq` on every independently persisted child/failover B-leg;
- normal usage/concurrency authority;
- normal credit screen/exposure admission when authoritative billing applies;
- normal terminal usage/customer settlement/provider COGS;
- final billing evidence with the existing usage quantities/presence and cost evidence/presence plus valid `Source`, `Authority`, and `DedupeKey` for each auxiliary B-leg/failover record.

The existing independent-leg billing boundary rejects `AttemptSeq <= 0`; continuity must not treat a distinct BillingCallID as sufficient if the per-leg record is invalid or would be rejected. Tests therefore pin field-level evidence, not only call identity.

No special charging function or money ledger is added.

Project a bounded content-free workload classification into existing accounting/metering/diagnostics, preferably from auxiliary lineage:

```text
workload_class = auxiliary
aux_role       = compaction_continuity_extractor
```

This classification does not implicitly change rates.

Primary frontend protocol usage stays primary-call-only. Account/operator totals include child records and can group auxiliary cost.

Pre-submit rejection means no provider usage and preservation fail-open. Once provider work is submitted, actual usage remains accountable even if the result is invalid/late/stale.

## D7. Process-Owned Branch Coordinator

A per-generation feature mutex cannot protect overlapping generations/workers. Current process `ExtensionState` is reload-stable but has no atomic compare-and-swap.

Create a small process-owned `internal/core/compactioncontinuity` coordinator. It is a synchronization/state facade, not semantic memory logic.

Responsibilities:

- serialize updates per authoritative **parent** branch key;
- use process `ExtensionState` as serialized backing where practical;
- revision-checked load/update;
- non-billable preview intent -> committed transaction binding;
- pending job/injection watermarks;
- bounded max entries/TTL and lazy cleanup;
- opaque capsule/source blobs only.

Conceptual branch key:

```go
type BranchKey struct {
    AuthoritativeSessionID string
    ALegID                 string // parent primary A-leg; never detached child A-leg
    PrincipalPartition     string // used when no secure SessionID
}
```

Client session hints never authorize lookup. The feature captures this key before any auxiliary child A-leg is created and derives a stable content-free `BranchBinding = SHA256(domain || canonical(BranchKey))` for serialized capsule/job identity. Raw principal/session identifiers need not be sent to the extractor model.

Conceptual state:

```go
type PreviewIntent struct {
    Key                  string // H(branch binding | detector preview boundary/fingerprint | target revision)
    TargetSourceRevision uint64
}

type InjectionTarget struct {
    BoundaryKey     string
    CapsuleRevision uint64
}

type InjectionWatermark struct {
    BranchBinding   string
    BoundaryKey     string
    CapsuleRevision uint64
}

type BranchState struct {
    Revision                  uint64
    CapsuleJSON               json.RawMessage
    CapsuleDigest             [32]byte
    SourceHighWatermark       string
    SanitizedSourceJSON       json.RawMessage
    PendingPreviewIntent      *PreviewIntent
    PendingJobID              auxiliary.JobID
    PendingJobTargetRevision  uint64
    PendingJobBranchBinding   string
    PendingInjection          *InjectionTarget
    LastReleasedInjection     *InjectionWatermark
    LastCompactionTransaction string
    UpdatedAt                 time.Time
}
```

`PendingJobBranchBinding` must match the branch state that owns the JobID before Await/merge. Child A-leg IDs are never accepted as a replacement key.

Core coordinator never interprets plan/decision facts. Worker/provider work runs outside coordinator locks.

## D8. Continuity Capsule V1

Feature-owned serialized envelope:

```json
{
  "schema_version": 1,
  "revision": 12,
  "source_high_watermark": "...",
  "branch_binding": "sha256:...",
  "content_digest": "sha256:...",
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
      "conflict_key":"architecture.billing.mode",
      "supersedes":[],
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

### Envelope binding and digest

- `branch_binding` is the content-free digest derived from the authoritative parent `BranchKey`; the private child A-leg never contributes to it.
- `content_digest` is `SHA256(domain || canonical_json(envelope_without_content_digest))`. The digest scope includes schema version, revision, high-watermark, branch binding and the complete semantic payload; only the digest field itself is omitted to avoid recursion.
- Canonical JSON rules (object-key order, number/string representation, no insignificant whitespace) are fixed by feature tests. The implementation may use a typed canonical encoder rather than generic map serialization.
- Registry consume, merge, reinjection/projection, authorized recovery and generation-reload reuse first validate branch binding + digest. A mismatch is fail-open discard, never cross-branch adoption.
- Model-facing extractor input/projection may omit raw branch identifiers and may omit the branch binding itself after local validation; self-binding is a storage/transfer integrity contract, not a reason to expose account/session identity remotely.

### Decision identity and merge precedence

Merge precedence:

1. later explicit user correction/decision;
2. later explicit user acceptance/selection;
3. authoritative deterministic structured-plan update;
4. validated semantic inference;
5. older capsule state.

Each decision has two identities with different purposes:

- `id`: stable fact identity/provenance across revisions;
- `conflict_key`: stable normalized semantic slot describing what choice the decision governs.

Extractor-generated IDs alone never decide conflicts. Validation/merge enforces:

- at most one `active` decision per `conflict_key`;
- a new active decision for an existing conflict key deterministically supersedes the older active decision according to precedence/revision;
- a correction whose semantic slot cannot safely reuse the prior conflict key must carry validated `supersedes` references to known active decision IDs; unknown/cross-branch references are rejected;
- non-conflicting decisions retain independent conflict keys and stable IDs;
- semantic output cannot resurrect a superseded fact against newer explicit intent.

Deterministic carrier facts use deterministic normalized IDs. Semantic IDs are accepted only after schema/source/conflict validation.

Bounded pruning preserves active decisions/constraints and pending/in-progress plan steps first, then useful rationale/current rejections, then condenses completed/superseded history. Prune whole facts and revalidate/re-digest; never truncate JSON syntactically.

## D9. Deterministic Plan Carrier Catalog

Place structured carrier parsing in `internal/plugins/features/compactioncontinuity`, not provider adapters/core detector identity.

Initial versioned families, after pinning actual canonical fixtures:

```text
codex.update_plan.v1
opencode.todo.v1
cline.task_progress.v1
```

Rules match canonical tool/item shapes, not agent brand. Each needs positive/near-miss tests. There is no generic “markdown checklist means accepted plan” rule.

## D10. Sanitized Extraction Source

Build a bounded structured envelope from effective canonical calls rather than raw wire bodies.

Priority:

1. user decision/constraint text;
2. assistant plan/proposal/clarification needed to interpret later user replies;
3. recognized structured plan carriers;
4. prior locally validated capsule semantic payload.

Default drop/truncate:

- ordinary tool results;
- command/compiler logs;
- large source/file/code dumps;
- images/video/binary/file blobs;
- unnecessary reasoning payloads;
- unrelated external content.

Any exceptional included tool/external material is tagged untrusted and delimited.

Every **successfully opened primary request** may refresh the process sanitized source/high-watermark. Failed pre-open calls never become committed source state.

Source priority is: current/effective canonical baseline -> process sanitized window -> optional existing authorized secure-session transcript via a narrow reader. No durable transcript read is required on ordinary hot path.

## D11. Semantic Extractor Call

One canonical child call contains:

- configured independent route;
- fixed extraction instructions;
- prior validated capsule semantic payload/base revision;
- deterministic plan facts;
- sanitized delta;
- no tools;
- bounded max output;
- continuity plugin suppressed;
- detached mode/private child A-leg;
- role `compaction_continuity_extractor`.

Expected output is one strict JSON object such as:

```json
{
  "schema_version": 1,
  "base_revision": 11,
  "facts": [],
  "plan_updates": [],
  "decision_updates": [
    {
      "id": "...",
      "conflict_key": "architecture.billing.mode",
      "supersedes": ["decision-old"]
    }
  ],
  "remove_or_supersede": []
}
```

Validate before merge:

- exactly one JSON object;
- exact schema version/base revision handling;
- byte/depth/item/string/count limits;
- exact enums;
- no unknown authority escalation;
- no raw tool/blob fields;
- valid/allowed source refs where used;
- decision conflict keys normalized/bounded and `supersedes` references limited to known decisions in the same validated parent capsule.

Malformed output is discarded fail-open. There is normally no second LLM pass.

## D12. Semantic Eligibility Gate

The local gate saves cost; it does not decide semantic truth.

Candidate signals may include structured plan change, substantial assistant plan followed by affirmative/corrective user text, explicit user choice/constraint/correction language, absent/stale capsule around planning markers, or new decision-relevant user turns after high-watermark.

Generic words alone never trigger. If deterministic state fully satisfies the configured preservation categories, skip the semantic job.

## D13. Strict Remote-Compaction Flow

### Pre-open

- `PreviewRequest` may identify a strict start candidate.
- preservation may prepare source/check old pending reinjection.
- **do not submit a strict-start semantic job yet**; a failed Open must not bill extraction.

### Successful primary Open

1. detector `RequestOpened` commits start/transaction;
2. preservation commits sanitized source;
3. deterministic carrier extraction updates/re-digests capsule;
4. eligibility decides whether semantic work adds value;
5. derive committed coalescing identity `H(parent_branch_binding | transaction_id | target_source_revision)`;
6. `BackgroundAux.SubmitCollect` submits one coalesced job if needed and BranchState stores the JobID against the captured parent branch;
7. primary compaction stream proceeds concurrently.

### Final selected event

1. detector `PreviewResponse` identifies potential completion/transaction without committing;
2. preservation resolves the matching parent-branch JobID and optionally `Await`s it for the bounded barrier;
3. ready strict output is validated and revision-merged/re-digested into the same parent branch;
4. verified plaintext carrier may receive deterministic continuity projection;
5. unsafe opaque/late paths set `PendingInjection{BoundaryKey, CapsuleRevision}`;
6. detector `ResponseReleased` receives the **post-preservation final event** and commits completion;
7. metadata-only observers dispatch;
8. final event releases to client.

A barrier timeout is fail-open. A late result remains available while referenced by bounded pending state and can be consumed by the next eligible turn.

## D14. Completion-Only / Local-Compaction Flow

If the first evidence is the next rewritten request, v1 deliberately avoids fresh billable semantic work before the primary request is known to have opened.

### Before primary Open

1. `PreviewRequest` identifies a completion candidate from the shared history matcher/fingerprint state without committing it.
2. capture the authoritative parent BranchKey/branch binding and derive `PreviewIntentKey = H(parent_branch_binding | preview_boundary_fingerprint | target_source_revision)`.
3. store/coalesce only that **non-billable preview intent** in BranchCoordinator; do not call `BackgroundAux.SubmitCollect`, do not retain `KindAsync`, and do not create a child A-leg yet.
4. load/validate the prior capsule and merge deterministic carrier delta.
5. if a matching semantic job was already submitted by an earlier successfully opened request (for example a late strict job), optionally `Await` that existing JobID up to the bounded barrier; otherwise there is no pre-open semantic wait.
6. inject the best already-ready/deterministic capsule transactionally if required, then continue normal primary Open fail-open.

### After successful primary Open

7. detector `RequestOpened` commits the completion-only transaction.
8. bind the PreviewIntentKey to the committed transaction and commit source/high-watermark/deterministic state for this successfully opened request.
9. if semantic eligibility still requires additional information, derive the normal committed coalescing key and submit one background job now.
10. that newly submitted job may improve subsequent turns/continuity state, but it is **not allowed to retroactively justify pre-open billing** for this request.

If primary Open fails, discard/expire the preview intent under bounded cleanup and produce **zero new billable continuity child work**. The extractor remains off-session/background after successful submission; v1 accepts that completion-only semantic information discovered only at this boundary may not be available for the same request, because preserving billing/lifecycle correctness is stronger than paying before Open.

## D15. Canonical Reinjection

One provider-neutral helper applies a deterministic text projection of a locally branch/digest-validated capsule.

- message-authoritative call -> legal proxy-owned developer/system instruction/message representation;
- item-authoritative call -> legal canonical message item;
- never populate legacy Messages/Instructions alongside item authority in violation of `Call.Validate`.

Projection format is versioned/delimited and says it is prior continuation state, not a new user request. Internal branch binding/digest metadata need not be emitted into the model-facing text after local validation.

### Boundary-scoped deduplication

Revision alone is not an injection identity. Use:

```go
type InjectionWatermark struct {
    BranchBinding   string
    BoundaryKey     string // committed transaction when available; detector preview boundary key while awaiting bind
    CapsuleRevision uint64
}
```

Rules:

1. `PendingInjection` records the boundary + revision requiring carry-forward.
2. a successive distinct compaction boundary may inject the same capsule revision again; this is required when no new facts were learned between two opaque compactions.
3. within one primary call, a call-local ephemeral marker prevents the helper from inserting the same continuity block twice during internal retry/failover preparation; this marker is not the durable branch watermark.
4. canonical insertion must pass `Call.Validate`. Callback error/panic/validation failure restores the exact pre-injection call and leaves `PendingInjection` unchanged.
5. primary Open failure leaves `PendingInjection` unchanged.
6. `LastReleasedInjection` advances and matching `PendingInjection` clears **only after the primary turn reaches successful final client release**. If the turn aborts before release, a later eligible request may inject again.
7. after completion-only Open binds a preview boundary to a committed transaction, the branch state rewrites the pending boundary key consistently before final release bookkeeping.

Projection is rechecked against injection budget after serialization.

## D16. Plaintext Result Augmentation Boundary

`ItemKindCompaction` is not assumed text. Maintain an allowlisted capability/matcher for verified mutable plaintext continuation carriers only.

Never modify:

- `CompactionItem.EncryptedContent`;
- `CompactionItem.Opaque`;
- provider signatures/opaque reasoning;
- unknown extension blobs.

Tests compare those bytes exactly with feature on/off.

Reinjection is mandatory universal fallback; response augmentation is only a safe-path optimization/stronger immediate carry-through.

## D17. Per-Session Policy

Global feature config is standard opaque feature YAML. Trusted session resolution:

```text
operator hard maxima / safety policy
        > trusted per-session value
        > global feature default
```

Allowed trusted session controls may include enabled, preserved categories, approved extractor selector, and tighter limits. Client headers/session metadata do not directly set these values and no new unauthenticated control header is introduced.

## D18. Configuration

Indicative only; implementation tests pin defaults. The example intentionally remains disabled so copying it cannot silently enable remote data egress or extra billed inference:

```yaml
plugins:
  features:
    - id: compaction-continuity
      enabled: false
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
        barrier_timeout: 2s
        pending_result_ttl: 2h
        max_capsule_tokens: 2500
        source_ttl: 2h
        failure_mode: fail_open
```

Values are examples, not normative defaults.

Validation:

- enabled feature requires prerequisite detector preview/commit + BranchCoordinator + BackgroundAux;
- semantic mode requires explicit route or explicit inherit;
- finite positive queue/concurrency/source/result/capsule bounds;
- pending result TTL must be consistent with branch/source usefulness and remains hard bounded;
- trusted session overrides cannot exceed global maxima without explicit authorization;
- invalid enabled config fails generation/startup composition instead of selecting an expensive model silently.

## D19. Failure Semantics

Default request-time behavior is fail-open.

| Failure | Behavior |
|---|---|
| prerequisite missing while feature enabled | generation/startup compile error |
| no decision candidate | no semantic job |
| queue full | deterministic capsule/native flow continue |
| generation retain fails | no job; fail-open |
| child billing/admission denied | no bypass; deterministic/native flow continue |
| extractor backend/timeout fails | child normal recovery then fail-open |
| invalid schema | discard result; actual submitted usage still accountable |
| stale result | discard/forget; no state regression |
| barrier timeout | continue native flow; pending result may be consumed later while bounded/useful |
| capsule branch-binding/digest mismatch | discard capsule/result for this branch; native flow continues |
| capsule over budget | whole-fact deterministic pruning then re-digest |
| preserver callback error/panic | record content-free stage failure; do not propagate to primary traffic |
| failed/invalid BeforeRequest mutation | restore pre-preservation `Call`; keep pending reinjection; native flow continues |
| failed/invalid BeforeResponseRelease mutation | restore pre-preservation `Event`; committed detector sees restored final event |
| completion-only primary Open fails | discard/expire non-billable preview intent; no new child submission/billing |
| state/coordinator unavailable | skip preservation mutation; native traffic continues |
| process shutdown | stop admission; cancel/join worker; normal submitted usage/accounting remains terminally owned |

## D20. Privacy and Security

- disabled by default;
- remote extractor is explicit data egress;
- existing redaction/secret policy precedes egress;
- source is bounded/sanitized, not full raw session by default;
- source content is untrusted quoted data;
- no tools and preservation plugin suppressed;
- prompt/output/capsule absent from normal logs/metrics;
- branch binding is a content-free one-way binding and raw parent BranchKey identifiers need not be sent to the model;
- pending raw result exists only in bounded process memory;
- transcript reads preserve principal/session/workspace authorization;
- no implicit durable transcript capture;
- detached mode is trusted auxiliary metadata unavailable to frontends.

## D21. Observability

Content-free metrics/diagnostics:

- compaction preview/start/completion by evidence/rule;
- structured carrier hits by rule ID;
- semantic eligibility skip reasons;
- preview intents created/bound/expired without provider submission;
- background jobs submitted/coalesced/saturated/canceled/completed/failed/stale;
- queue depth/in-flight;
- extractor route/backend/model identifiers where policy allows;
- extractor tokens/cache/cost through existing usage/billing;
- auxiliary accounting rejection by invalid attempt/evidence contract;
- capsule revision/serialized size/fact counts/digest-validation outcome;
- barrier wait duration/timeouts;
- augmentation/reinjection counts and pending/committed watermark outcomes;
- preserver callback fail-open outcomes by stage;
- stale/revision/conflict-key conflicts;
- opaque-carrier no-mutation count.

Logs use hashes/IDs/counts only.

## D22. Lifetime and Concurrency Invariants

1. ProcessServices owns BackgroundScheduler and BranchCoordinator.
2. Generation snapshots hold non-owning clients only.
3. Each successfully submitted background job owns exactly one captured `KindAsync` pin; non-billable preview intents own none.
4. Pin releases exactly once on every terminal/cancel/handoff-failure path.
5. A worker never obtains a different generation after submit.
6. Parent BranchKey/branch binding is captured before child A-leg creation and remains the only continuity-state key for that job.
7. BranchCoordinator serializes revision/job/injection state across overlapping generations.
8. No external model/provider call executes while a branch-coordinator lock is held.
9. No observer/plugin callback runs under scheduler/coordinator internal locks.
10. Job/result/branch/preview-intent maps have hard bounds/TTL and no unbounded cleanup goroutine pattern.
11. Reload changes future jobs only; already-submitted work remains accountable and terminally owned.
12. Branch-level reinjection watermark commits only after successful final client release; validation/Open/aborted-release failures retain pending state.

## D23. Restart and Durable Resume

V1 guarantees continuity across compaction and immutable generation reload within one process. It does not create durable capsule/job storage.

An optional future/implementation recovery adapter may reconstruct from an already-enabled authorized secure-session transcript. It must be read-only, bounded and authorization-preserving. Reconstructed capsules receive the current authoritative branch binding and a freshly validated canonical digest before use. Without that source, missing process capsule after restart is explicit fail-open state.

No durable job queue or full transcript store is added here.

## D24. Expected Change Surface

Primary packages:

- `pkg/lipsdk/compaction` — preview-related preservation contract beside prerequisite observer;
- `pkg/lipsdk/feature` — additive preserver slice;
- `pkg/lipsdk/auxiliary` — additive BackgroundClient/job types and trusted detached policy carrier;
- `internal/featurebundle` / `internal/core/extensions` — merge/snapshot/preservation stages and fail-open transactional mutation dispatch;
- `internal/core/compactiondetect` — pure request/response previews sharing matcher authority;
- `internal/core/auxreq` — bounded scheduler, submit-time pin handoff, detached child adapter;
- `internal/core/compactioncontinuity` — narrow BranchCoordinator/preview-intent/injection-watermark state;
- `internal/core/runtime` — request preview/open, final-response ordering, detached prepare/private child A-leg wiring, successful-release watermark commit;
- `internal/infra/runtimebundle` — process ownership and generation snapshot clients;
- `internal/plugins/features/compactioncontinuity` — config/capsule/carriers/sanitizer/eligibility/extractor/merge/injection;
- existing metering/billing/report projections only for content-free auxiliary workload classification;
- focused testkit/architecture fixtures.

Individual provider/backend and frontend protocol packages should not need compaction-continuity-specific branches.

## Testing Strategy

TDD order:

1. RED capsule branch-binding/digest, decision conflict-key/supersession, merge/pruning + carrier fixtures.
2. RED sanitizer/eligibility/extractor-schema tests.
3. RED BackgroundAux submit-time pin/committed coalescing/saturation/result-lifetime/shutdown tests plus non-billable preview-intent binding tests.
4. RED detached-session/private-child-A-leg tests proving parent BranchKey remains continuity authority.
5. RED separate-route + user-billing/BillingCallID/per-B-leg AttemptSeq/evidence/protocol-usage tests.
6. RED detector request/response preview and final-release ordering tests.
7. RED preserver callback error/panic rollback and canonical validation fail-open tests.
8. RED strict/remote and completion-only compaction barriers including failed-Open zero-child-billing.
9. RED boundary-scoped reinjection tests: same revision across two compactions, validation/Open/release failure then retry, plaintext/opaque paths.
10. RED three-plus repeated-compaction/concurrency/generation-reload tests.
11. GREEN minimal implementation in that order.
12. repository quality/race/architecture/simplification gates.

No external model credentials are required; deterministic fake/local backends return fixture JSON.

## Design Invariants

1. One compaction recognition authority; no duplicate signature matrix.
2. Metadata observer remains content-free/non-mutating.
3. Committed detector sees the actual final released event after permitted preservation mutation.
4. Preserver callback failure is feature-local/fail-open; failed mutation restores the prior canonical object.
5. Extractor is off primary session but billed to originating user by default.
6. No new billable semantic child is submitted before successful primary Open, including completion-only/local discovery.
7. Extractor route is explicit/independent unless explicit inherit.
8. Background ownership is captured before request spawn right ends.
9. ProcessServices owns worker/coordinator lifetime.
10. Detached child uses a private auxiliary A-leg and never creates a primary secure-session turn; continuity state remains keyed to the captured parent branch.
11. Normal routing/B2BUA/usage/billing own child execution; every persisted auxiliary B-leg has valid AttemptSeq/final evidence and there is no direct provider path.
12. Primary protocol usage excludes child usage; account totals include it.
13. Continuity capsule is bounded, revisioned, parent-branch-bound, digest-validated and reload-safe.
14. Contradictory active decisions cannot coexist solely because semantic extraction emitted new IDs; conflict keys/supersedes define deterministic replacement.
15. Opaque/encrypted compaction bytes are immutable.
16. First post-compaction request may await only already-submitted semantic work before Open; fresh semantic work waits for successful Open.
17. Reinjection dedupe is branch/boundary/revision-scoped and commits only after final client release.
18. Pending late results remain useful only within a bounded branch retention window.
19. Configuration examples/default behavior remain disabled until explicit operator opt-in.
20. No second transcript DB, money ledger, generic workflow engine or redundant summary LLM pass.
