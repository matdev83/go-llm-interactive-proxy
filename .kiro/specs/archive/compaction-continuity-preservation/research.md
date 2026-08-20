# Research & Design Decisions

## Summary

Issue #344 is valid, but the safest implementation is not “run another summarizer and append whatever it returns.” Go-LIP already has most of the right architectural pieces: canonical requests/items, secure-session/B2BUA authority, process-owned extension state, auxiliary execution with principal propagation, generation pins, and BillingCallID-scoped billing. The missing pieces are narrow:

1. the `compaction-event-detection` runtime capability must land and remain the single compaction-recognition authority;
2. preservation needs a **separate content-bearing interceptor** rather than mutating the metadata-only observer contract;
3. true background auxiliary execution needs a process-owned bounded scheduler that captures generation ownership at submit time;
4. the child needs typed **detached session semantics** so it is financially attributable to the user without becoming another primary conversation turn;
5. continuity must use a bounded structured capsule and only patch verified plaintext carriers; opaque/encrypted compaction state is reinjection-only.

The resulting design is a parallel-first pipeline: deterministic plan harvest and source preparation are local; one independently routed semantic extractor job may run concurrently with the agent's own compaction; the main path only waits at a bounded preservation barrier when a result is actually required.

## External Precedent

The feature follows patterns already used by mature coding-agent harnesses:

| Project | Relevant behavior | Design implication |
|---|---|---|
| Pi | default compaction summary explicitly carries goal, constraints/preferences, progress, key decisions, next steps, and critical context | decision state deserves first-class preservation rather than generic prose only |
| Pi | `session_before_compact` can replace/customize compaction and examples use a cheaper secondary model | pre-/around-compaction interception and independent model choice are established patterns |
| OpenAI Codex | compact prompt asks for progress/key decisions, user preferences/constraints, next steps, critical continuation data | preservation categories align with real agent continuation needs |
| OpenAI Codex | `update_plan` exposes structured plan state | deterministic plan harvest should precede semantic LLM inference |
| OpenCode | pre-summary compaction hook and rolling structured session checkpoint | structured cumulative state is preferable to repeatedly summarizing prior summaries |
| OpenCode | todo state is structured and appears in compaction/session representations | machine-readable plan progress can survive without LLM rediscovery |
| Codex issue #14347 | repeated compaction can lose historical decisions | repeated compactions require cumulative merge semantics, not recursive summary trust |

References:

- https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/compaction/compaction.ts
- https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md
- https://github.com/earendil-works/pi/blob/main/packages/coding-agent/examples/extensions/custom-compaction.ts
- https://github.com/openai/codex/blob/main/codex-rs/prompts/templates/compact/prompt.md
- https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/plan.rs
- https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/plugins.mdx
- https://github.com/anomalyco/opencode/blob/dev/specs/v2/session.md
- https://github.com/anomalyco/opencode/blob/dev/packages/sdk/js/src/gen/types.gen.ts
- https://github.com/openai/codex/issues/14347

## Go-LIP Brownfield Findings

### Compaction detection is designed but not implemented yet

`.kiro/specs/compaction-event-detection/` defines:

- canonical protocol/signature/history recognition;
- a process-owned detector keyed by authoritative A-leg;
- strict start only after successful upstream `Open`;
- completion at the final canonical release seam;
- a metadata-only, non-mutating `pkg/lipsdk/compaction.Observer`.

There is no runtime `pkg/lipsdk/compaction` package on current `main`. Continuity preservation therefore has a hard implementation-order dependency on that spec. The correct response is to make #312 a prerequisite, not to recreate its signature table in #344.

### Existing auxiliary execution already preserves user identity

`pkg/lipsdk/auxiliary.Request` carries role, visibility, parent lineage, disabled plugin IDs, and a canonical child call. `internal/core/auxreq.Client`:

- increments auxiliary depth;
- suppresses requested plugins;
- clones the parent principal/scope and marks `scope.OriginInternal`;
- creates an auxiliary trace ID and lineage extension;
- delegates to the ordinary runtime Executor.

This is exactly the path we want for routing, attempts, usage, billing and provider calls. A direct HTTP/provider client would duplicate too many authorities.

### But current auxiliary execution is synchronous

`Aux.Collect` calls `Stream` and collects immediately. The client retains `genpin.KindAsync` when the child call starts. `genpin.Retainer` explicitly says a post-lease spawn attempt fails closed.

Therefore this is unsafe:

```text
request hook
  -> go func() { Aux.Collect(parentCtx, ...) } // wrong
request returns / generation lease ends
  -> goroutine starts later and tries to retain/execute
```

The fix is a submit-time handoff: resolve the current executor and retain `KindAsync` synchronously, then enqueue immutable child work to a process-owned scheduler. The worker owns its own context/deadline and releases the pin exactly once after terminal collection.

### Worker ownership belongs to ProcessServices

`ProcessServices` already owns process-lifetime mutable services, worker-like subsystems, stores and closers. `ExtensionState` is also process-owned and survives generation replacement. A feature `Lifecycle` alone is generation-composed and is not the right sole owner for work that may overlap a hot reload.

A narrow `auxreq` background collector/scheduler under ProcessServices matches current ownership architecture and keeps the feature plugin away from Executor/provider internals.

### Current auxiliary execution needs a detached-session policy

The child delegates through the ordinary Executor. The primary prepare path performs secure-session BeginTurn, transcript/activity recording, A-leg route-override snapshotting and other user-turn semantics.

`auxiliary.Request.Visibility` is currently only lineage metadata; it does not suppress those primary session effects. A continuity extractor must therefore use a typed internal execution/session mode that says:

- preserve authenticated principal/scope;
- preserve parent IDs only as correlation;
- do not begin/append a primary secure-session turn;
- do not update primary last activity/turn count/transcript;
- do not apply the primary A-leg routing override as child route authority.

This is execution metadata, not provider-visible prompt content.

### Independent routing fits the canonical child call

`lipapi.Call.Route.Selector` already expresses route intent. The extractor can set an operator-configured selector on its child call and then use the normal core planner, aliases, failover, capability checks and admission.

This allows, for example:

```text
primary:   anthropic:claude-frontier-coding
extractor: openai-responses:small-fast-model
```

without any provider-specific code. Parent route overrides remain primary-session authority only. An explicit `inherit` option can be supported, but accidental inheritance is a bad default because it defeats the cost-control purpose of a dedicated extractor route.

### Existing billing naturally supports originating-user attribution

The authoritative billing identity adapter derives the account from `scope.PrincipalID`. Auxiliary execution already clones scope from the parent. If detached mode preserves principal/scope, the child can pass through the normal Executor and receive:

- normal usage/concurrency admission;
- its own BillingCallID;
- its own B-leg attempts/failover;
- normal operational exposure and post-usage customer settlement;
- normal provider COGS processing.

No new money path is necessary. The missing detail is classification: accounting/diagnostics should project a bounded `compaction_continuity_extractor` workload/origin so users/operators can distinguish auxiliary continuity cost from primary inference. That is metadata, not a new ledger/rating authority.

Primary response protocol usage must remain the primary call's usage only. Account totals include both independent calls.

### Process ExtensionState is useful but not restart-durable

`ProcessServices.ExtensionState` is an in-memory process-owned store. `ScopeSession` partitions by `SessionView.PartitionKey()`, which chooses authoritative SessionID when available. This is suitable for:

- current capsule revision;
- high-watermarks;
- bounded sanitized source window;
- pending background job ID;
- pending/injected revision watermarks.

The feature key must additionally include authoritative A-leg/branch identity because session partition alone is not a branch key.

This state survives generation reload, but not process restart. The spec should say so. When secure-session transcript capture is already enabled, a narrow authorized transcript reader may reconstruct missing capsule state. Otherwise restart/resume is fail-open; this feature does not justify a second durable transcript or general durable plugin-state platform.

### Native compaction is often opaque

`lipapi.CompactionItem` carries:

- `EncapsulatedID`;
- `Dialect` / `Implementor`;
- `EncryptedContent`;
- `Opaque` provider data.

There is no universal plaintext “summary” property. Editing encrypted/opaque state could break replay or provider protocol semantics.

Therefore:

- result-side augmentation is allowed only when the path exposes a verified mutable plaintext summary/continuation carrier;
- otherwise the validated capsule remains proxy-owned state and is injected into the first eligible post-compaction request;
- opaque/encrypted content is an exact-preservation invariant.

## Decision D1 — Treat #312 as a hard prerequisite and share one recognizer

Implementation should first land `compaction-event-detection` (or implement it in chronological dependency order before this feature). The detector's rule table remains the only compaction signature authority.

Continuity may need a pure request **preview** so the first post-compaction turn can be protected before B-leg Open. That preview should be factored from the same matcher/fingerprint logic but must not mutate detector state or emit lifecycle events. Committed start/completion still follows #312 semantics.

## Decision D2 — Add a separate compaction preservation interceptor

Do not mutate `compaction.Observer`.

Add a distinct additive FeatureBundle contribution (name indicative):

```go
type Preserver interface {
    ID() string
    BeforeRequest(ctx context.Context, call *lipapi.Call, meta RequestMeta, preview RequestPreview, svc Services) error
    RequestOpened(ctx context.Context, call lipapi.Call, meta OpenMeta, derived []Event, svc Services) error
    BeforeResponseRelease(ctx context.Context, ev *lipapi.Event, meta ResponseMeta, derived []Event, svc Services) error
}
```

Exact method names may differ, but the semantic stages are deliberate:

- **BeforeRequest:** pre-open only; check pending reinjection, protect completion-only first turn, never emit detector truth.
- **RequestOpened:** successful-open only; commit sanitized source and start one background semantic job for a real strict compaction transaction.
- **BeforeResponseRelease:** final selected event; bounded join and verified plaintext augmentation/fallback marker before metadata observers/client release.

The slice is merged/frozen separately from `CompactionObservers`.

## Decision D3 — Add a narrow process-owned background auxiliary collector

Add an additive auxiliary capability rather than expanding `Client` into a task framework. Conceptually:

```go
type JobID string

type BackgroundClient interface {
    SubmitCollect(ctx context.Context, req Request, opts SubmitOptions) (JobID, error)
    Await(ctx context.Context, id JobID) (lipapi.Collected, error)
    Forget(id JobID)
}
```

`SubmitCollect` must synchronously:

1. validate/copy the canonical child request;
2. resolve the current Executor runner;
3. retain `genpin.KindAsync` while the request still has spawn authority;
4. clone only required principal/scope/correlation context;
5. enqueue into a bounded process scheduler.

Workers execute with a scheduler-rooted context and configured timeout. A job registry supports coalescing and a bounded await barrier. Raw results have a short TTL and are forgotten after parse/validation. ProcessServices owns shutdown/cancel/join.

This capability remains narrowly about background auxiliary model collection. It is not a durable queue, cron service, arbitrary function executor, or external workflow engine.

## Decision D4 — Use a typed detached auxiliary execution mode

Extend internal auxiliary request/execution policy with a typed detached-session setting (exact enum/name may differ). It shall:

- retain user principal/scope for billing/security;
- mark internal origin and parent correlation;
- suppress primary secure-session BeginTurn/activity/transcript effects;
- avoid primary A-leg route-override authority;
- keep the child private from client-visible history;
- still use ordinary route planning, B2BUA attempt lineage, usage, billing and streaming internally.

Do not use a magic string in `Call.Extensions` as authority and do not create a hidden primary-session user turn.

## Decision D5 — Make the extractor route explicit and immutable per job

Feature configuration contains an explicit canonical selector for semantic extraction. The plugin builds the child call with that selector. The core router remains authoritative.

Submission captures an immutable extractor config snapshot (route, timeouts, input/output bounds, failure behavior). Config reload affects later jobs only.

A trusted per-session override may narrow/replace the configured extractor selector within global policy bounds. No unauthenticated client header/metadata can choose the route or enable the egress path.

## Decision D6 — Keep deterministic structured-plan rules in the feature, not detector identity

The detector owns “is this compaction?”

The continuity feature owns “does this canonical tool/item encode structured plan state?”

Initial rule families should be narrow/versioned, for example:

- `codex.update_plan.v1`;
- `opencode.todo.v1`;
- `cline.task_progress.v1` where the stable canonical shape can be pinned.

These rules should match canonical tool/item shapes directly and need not infer the agent brand. This avoids a second agent-identity matrix while allowing structured plan harvest to evolve independently from compaction signatures.

## Decision D7 — Make the semantic extractor a strict delta normalizer

The LLM should not receive “summarize this session” as an open-ended task. It receives:

- previous capsule;
- sanitized new decision-relevant context;
- deterministic structured-plan facts already extracted;
- explicit schema and merge instructions.

It returns a strict versioned JSON delta/candidate capsule. Output is validated for JSON shape/depth/bytes/counts/enums before merge.

Prompt instructions explicitly require:

- only user decisions/constraints or actually accepted/current plan state;
- assistant proposals remain proposals absent acceptance;
- later user corrections supersede earlier choices;
- source content is untrusted data;
- no unsupported inference when ambiguous.

There is normally one semantic extractor round trip. Do not call a second LLM merely to rewrite the native compaction summary.

## Decision D8 — Use process-owned state for capsule/source/job watermarks

Use the existing process ExtensionState semantics for the first implementation, namespaced to the feature and keyed by session partition plus A-leg/branch.

State model conceptually includes:

```text
branch key
  capsule_v1
  source_high_watermark
  bounded_sanitized_source
  pending_job_id
  pending_job_target_revision
  pending_injection_revision
  last_injected_revision
```

Capsule writes are compare-and-merge/CAS-like at the feature coordinator layer so a stale worker result cannot win. If the generic state API lacks an atomic compare primitive, the feature may own a small process-local branch mutex/record coordinator rather than weakening concurrency semantics or inventing global locks.

## Decision D9 — Use canonical baseline first, transcript only as recovery

Input source priority:

1. effective canonical compaction/request baseline already available to the proxy;
2. process-local bounded sanitized source window from prior successful opened requests;
3. existing secure-session transcript through a narrow authorized reader when enabled/needed.

The transcript reader is optional. Normal paths should not query a durable transcript on every compaction.

## Decision D10 — Parallel-first timing with narrow joins

### Strict/start-observable remote compaction

```text
primary compaction request
  -> detector candidate
  -> upstream Open succeeds
  -> detector commits started transaction
  -> preservation SubmitCollect (background) --------+
  -> primary compaction stream continues             |
                                                    |
final selected compaction event                      |
  -> detector derives completion metadata            |
  -> preservation bounded Await <--------------------+
       -> ready + plaintext carrier: merge mechanically
       -> ready + opaque carrier: store pending reinjection
       -> timeout/error: fail-open, retain best valid state
  -> metadata observer dispatch
  -> client release
```

### Completion-only/local compaction first seen on next request

```text
next request before backend Open
  -> pure detector preview says likely installed compaction
  -> preservation checks capsule/pending job
  -> if needed: background SubmitCollect from previous sanitized source
  -> bounded Await barrier
  -> inject ready capsule or fail-open
  -> normal Open
  -> detector commits completion only after successful Open
```

The extractor is still off-session/background in both cases. The difference is whether useful parallel time existed before the first turn that needs the result.

## Decision D11 — Reinjection is the universal safe fallback

When native compaction content is opaque, store the capsule and inject a bounded proxy-owned continuity block into the first eligible post-compaction request.

Injection must respect canonical authority:

- message-authoritative call -> proxy-owned instruction/developer/system message representation allowed by current canonical contract;
- item-authoritative call -> canonical message item representation, not simultaneous legacy `Instructions`/`Messages` that would violate `Call.Validate`.

Use one helper that preserves call validity rather than protocol-specific encoders.

A revision watermark prevents duplicate injection.

## Decision D12 — User billing is normal billing, not special charging code

The detached child retains the original `scope.PrincipalID`, so ordinary billing resolves the same account. The child gets a separate BillingCallID and per-B-leg records.

Add only a content-free workload/origin projection such as:

```text
workload = auxiliary
aux_role = compaction_continuity_extractor
parent_session_id_hash / parent_trace correlation as allowed
```

This lets reports distinguish continuity overhead while the money ledger/journal remains unchanged.

If the child is denied before provider submission, preservation fails open by default. If provider work was submitted, usage remains billable even if the result is discarded later.

## Decision D13 — Global config plus trusted per-session narrowing/override

Conceptual feature config:

```yaml
# exact nesting follows standard feature-plugin opaque config conventions
compaction_continuity:
  enabled: false
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

Values above are examples, not normative defaults. Implementation tests should pin chosen defaults/validation.

Per-session overrides come only from trusted proxy-owned policy/session state and cannot exceed global egress/resource maxima unless explicitly authorized.

## Decision D14 — Do not claim restart durability in v1

The first implementation guarantees continuity across compaction and generation reload **within the process**. It does not create a new durable capsule database.

If an authorized secure-session transcript exists after restart, the capsule can be reconstructed on demand. Otherwise the feature resumes without prior process-only capsule state and fails open. This is truthful and materially simpler than introducing a new durable state subsystem for a UX enhancement.

## Rejected Alternatives

### Run extractor inline on the main request goroutine

Rejected. It violates the off-session/background requirement and adds full extractor latency directly before native compaction.

### Fire-and-forget goroutine calling current `Aux.Collect`

Rejected. It can lose generation spawn rights, inherit canceled request lifetime, leak on shutdown, and has no bounded admission/result ownership.

### Direct provider/HTTP client for the extractor

Rejected. It would bypass routing, principal scope, usage authority, billing, B2BUA attempts, retries and current provider abstractions.

### Put extractor call into the primary session as another assistant/user turn

Rejected. It pollutes conversation state, can influence agent behavior, changes transcript/turn semantics and makes auxiliary billing indistinguishable from primary inference.

### Append text to every `CompactionItem`

Rejected. Native compaction may be encrypted/opaque provider state and does not expose a universal safe text carrier.

### Second LLM call to rewrite/improve native summary

Rejected by default. The validated continuity capsule is already the semantic result; deterministic merge/reinjection is cheaper and more reliable.

### Re-send the entire raw session on every compaction

Rejected. It creates O(session age) cost, unnecessary privacy egress and prompt-injection surface. Use delta + prior capsule + bounded recovery sources.

### Build a second durable transcript/capsule/job database now

Rejected. Existing secure transcript can be used when authorized; process state is sufficient for the primary compaction UX objective. A general durable job/state system is disproportionate.

### Use the #312 history heuristic as semantic truth about accepted plans

Rejected. Compaction detection and user-decision semantics are different problems. Heuristics may decide when to call the semantic extractor, not manufacture user decisions.

## Main Risks and Mitigations

| Risk | Mitigation |
|---|---|
| semantic extractor hallucinates acceptance | strict schema/provenance, deterministic merge, explicit-user precedence, ambiguity omission |
| extra user cost | deterministic-first/eligibility gate, independently cheap route, visible auxiliary cost classification |
| worker leaks across reload/shutdown | ProcessServices ownership, submit-time KindAsync pin, bounded scheduler, race/goleak tests |
| extractor changes primary session state | typed detached-session execution mode and session/transcript regression tests |
| stale job overwrites correction | branch revision/high-watermark compare-and-merge |
| opaque compaction corrupted | exact byte-preservation invariant; reinjection fallback |
| first post-compaction turn races job | pre-open pure preview + bounded Await barrier |
| privacy egress of tool/file dumps | canonical sanitizer, redaction, excluded external/tool data by default |
| duplicate billable jobs on retry | transaction/revision coalescing key |
| config reload changes running job unexpectedly | immutable submission snapshot + retained generation |
| restart loses process capsule | truthful v1 contract; authorized transcript reconstruction only when available |

## Research Conclusion

The feature has high UX value and fits Go-LIP's current architecture if implemented as **structured process continuity + a narrow background auxiliary capability**, not as a generic memory subsystem or compactor rewrite. The highest-risk brownfield work is not the LLM prompt; it is lifecycle/session/accounting correctness around a detached asynchronous child call. The design and tasks should therefore put RED tests around those boundaries before implementing semantic extraction details.
