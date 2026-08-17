# Requirements Document

## Introduction

Go-LIP shall preserve the decision state that matters for continuing a long coding-agent session across lossy context-compaction events. The feature is not general memory and is not a replacement compactor. Its purpose is narrower: retain the latest accepted plan, explicit user product/architecture decisions, constraints, useful rationale, meaningful rejected alternatives, current plan progress, and unresolved next actions when ordinary compaction would otherwise discard them.

The preserved state shall be represented as a bounded, versioned **Continuity Capsule** rather than an ever-growing natural-language transcript. Structured planning state already exposed by an agent shall be harvested deterministically. A separately configured auxiliary LLM may be used only for genuinely semantic extraction such as identifying conversational acceptance, user decisions, rationale, or supersession that cannot be recovered safely from structured carriers.

Auxiliary semantic extraction is explicitly **off the primary agent session** and runs as independent background work. It may use a completely different model/provider/route from the main coding session. It is nevertheless real additional model usage: by default its usage and cost belong to the same authenticated user/account that caused the extraction and must flow through normal Go-LIP admission, usage, metering, customer billing, and provider-cost accounting.

This specification depends on the compaction-recognition authority defined by the existing `compaction-event-detection` specification. That detector remains metadata-only and non-mutating. Continuity preservation adds a separate content-bearing preservation/interception capability and shall not turn the detector observer into a request/response mutation surface.

## Boundary Context

- **In scope:** continuity-capsule schema/merge semantics; deterministic structured-plan harvesting; bounded semantic-extraction eligibility; sanitized extraction input; process-owned background auxiliary execution; independent extractor routing; originating-user billing; detached auxiliary session semantics; compaction-boundary synchronization; verified plaintext augmentation and post-compaction reinjection; repeated compactions; authoritative session/A-leg scoping; trusted session overrides; privacy, observability, and TDD gates.
- **Dependency:** implementation requires the detector/rule authority specified by `compaction-event-detection`. If that runtime capability has not landed, it is a prerequisite rather than a reason to duplicate compaction signatures in this feature.
- **Existing authorities preserved:** core routing/selector parsing; B2BUA lineage; secure-session authority; generation pinning; `ProcessServices`; auxiliary execution; usage/concurrency authority; BillingCallID-based post-usage billing; canonical stream/retry commitment; extension merge/runtime snapshots.
- **Out of scope:** general RAG/long-term memory; retaining every conversation fact; replacing an agent compactor; a new provider client; rewriting encrypted/opaque native compaction blobs; hidden system-account billing by default; general-purpose durable job orchestration; general agent identity inference; provider-specific branching in core; a second full-transcript database.

## Requirement 1: Shared Compaction Recognition and Separate Preservation Contract

1.1. Continuity preservation shall reuse the protocol/signature/history recognition authority defined by `compaction-event-detection`; it shall not maintain a second independent compaction-signature matrix.
1.2. The metadata-only `compaction.Observer` contract shall remain non-mutating, content-free, and fail-open.
1.3. Go-LIP may add an additive content-bearing preservation/interception contract, but it shall be distinct from `compaction.Observer` and from ordinary response hooks.
1.4. `FeatureBundle`, the single feature merge surface, and the frozen request-runtime snapshot shall expose preservation interceptors separately and defensively copy/freeze them using existing extension conventions.
1.5. Preservation shall use the same authoritative A-leg and compaction transaction identity as the detector when available.
1.6. A strict start candidate shall not cause a billable semantic-extraction job if the compaction-looking request never successfully opens upstream.
1.7. When a recognized compaction request successfully opens upstream, preservation may start one extraction job for that logical transaction while primary compaction work continues independently.
1.8. Completion-only/history evidence shall never invent a historical start, but it may trigger extraction/reinjection required for a compaction only observable after installation.
1.9. Any pre-open request preview needed to protect the first post-compaction turn shall be a pure/non-committing view over the same matcher/fingerprint authority; preview shall not emit lifecycle events, advance detector state, or establish a strict-start billing trigger.
1.10. Final-release ordering shall keep detector derivation non-mutating: selected canonical event -> derived compaction lifecycle metadata -> separate preservation finalization -> metadata-observer dispatch -> client release. No ordinary response hook may run after preservation finalization.
1.11. Provider/frontend wire DTOs and provider SDK types shall not enter core continuity logic.
1.12. Generic words such as `plan`, `summarize`, `continue`, or `compact` shall not by themselves establish a compaction boundary or accepted plan.

## Requirement 2: Versioned Continuity Capsule and Decision Semantics

2.1. Continuity state shall use a versioned schema containing at least schema version, monotonic revision, source/high-watermark metadata, branch identity, and content digest.
2.2. The capsule shall represent the latest accepted/current plan separately from user decisions and constraints.
2.3. Plan steps shall support at least pending, in-progress, completed, and removed/superseded semantics.
2.4. User decisions shall support at least active, superseded, and rejected semantics.
2.5. Retained facts shall carry bounded provenance sufficient to distinguish explicit user text, user acceptance/correction, deterministic structured-plan state, and semantic extractor inference without retaining the full transcript.
2.6. Later explicit user intent shall supersede conflicting older active intent; contradictory decisions shall not remain simultaneously active merely because both occurred historically.
2.7. Assistant brainstorming/proposals shall not become authoritative user decisions unless later user evidence accepts/selects/instructs execution of them.
2.8. A structured current plan exposed authoritatively by the harness may be retained as current plan state without requiring a conversational acceptance sentence.
2.9. Explicitly rejected alternatives may be retained when they constrain future work; they shall not reappear as active absent later user reversal.
2.10. Useful rationale/trade-offs may be retained with the associated decision and shall follow that decision's active/superseded state.
2.11. Ambiguous semantic evidence shall be omitted or represented as provisional/non-authoritative, never promoted to explicit user intent.
2.12. Merge behavior shall be deterministic for `previous capsule + validated delta`: duplicate facts coalesce, statuses do not regress, and stale revisions cannot overwrite newer state.
2.13. Capsule size shall be bounded by configured byte/token-equivalent limits; overflow shall prioritize active decisions/constraints, pending/in-progress plan steps, useful rationale, then historical/completed material.
2.14. Completed steps and superseded history may be condensed/dropped under retention policy while active decisions and unresolved work remain until superseded/removed or the branch ends.
2.15. The capsule is continuity state, not an audit transcript, and shall not contain arbitrary logs, file dumps, binaries, credentials, or unrelated tool output.

## Requirement 3: Deterministic-First Extraction and Bounded Source Preparation

3.1. Before invoking an extractor LLM, the feature shall harvest supported machine-readable planning state mechanically from canonical calls/items/tool data.
3.2. Initial deterministic carrier coverage shall include versioned equivalents of Codex `update_plan`, OpenCode todo state, Cline-style task-progress/checklist state where observable, and other stable structured plan carriers established during implementation research.
3.3. Carrier matching shall use canonical item/tool shapes rather than provider DTOs; each rule shall have a stable versioned ID and positive/near-miss fixtures.
3.4. Carrier rules identify structured plan semantics, not agent identity; compaction-family identity remains owned by the shared detector.
3.5. Structured state that can be normalized deterministically shall not be sent to an LLM merely to rediscover the same facts.
3.6. Cheap local eligibility heuristics shall decide whether semantic extraction is likely to add information; they may suppress a model call but shall not declare arbitrary prose to be accepted user intent.
3.7. A compaction with no relevant plan/decision/constraint candidates and no stale/missing capsule need shall perform zero semantic-extractor calls.
3.8. Extractor input shall include the previous capsule plus only bounded new decision-relevant context needed to advance its source high-watermark.
3.9. User messages receive highest source priority; assistant plan/proposal/clarification content may be retained only as needed to interpret user acceptance/correction.
3.10. Ordinary tool outputs, shell/compiler logs, large file/code dumps, images/video/binary payloads, and unrelated external content shall be dropped or heavily truncated by default.
3.11. Structured plan/TODO tool calls and small required results may survive sanitization.
3.12. Untrusted tool-result/external text shall be excluded from semantic decision extraction by default; any included content remains untrusted data rather than extractor instructions.
3.13. Source preparation shall be incremental where possible so repeated-compaction cost follows new relevant context plus the bounded prior capsule, not total session age.
3.14. The feature may keep a bounded process-local sanitized source window for completion-only/local compactions, but it shall not become an unbounded or durable shadow transcript.
3.15. When existing secure-session transcript capture is enabled and authorized historical recovery is needed, it shall be accessed through a narrow read/source adapter rather than by importing secure-session store/Bun internals into the feature.
3.16. When transcript capture is disabled, the feature shall not enable durable full-transcript capture solely for continuity extraction.

## Requirement 4: Off-Session Background Auxiliary Execution

4.1. Semantic extraction shall execute as a proxy-created auxiliary/internal LLM invocation, not as a visible user/assistant turn in the primary agent conversation.
4.2. Extractor output shall be consumed by continuity logic and never surfaced as an assistant message.
4.3. The child shall have its own execution/B-leg lifecycle; parent session/A-leg/trace values are non-authoritative correlation/accounting lineage only.
4.4. Background extraction shall run behind a bounded independent asynchronous worker boundary. A bounded in-process goroutine/worker pool is acceptable; an external worker may be supported later without changing semantics.
4.5. The implementation shall enforce explicit maximum concurrent jobs and queue capacity and shall not spawn an unbounded goroutine per candidate/event.
4.6. Background scheduling shall be process-owned through `ProcessServices` or an equivalent existing process ownership seam, not solely by a generation-scoped feature lifecycle.
4.7. Submission shall synchronously resolve/capture the executable generation and retain `genpin.KindAsync` before returning; a delayed worker shall not attempt a new spawn after the originating request lease is gone.
4.8. Worker execution shall use an independent worker-owned context/deadline carrying only cloned required principal/scope/correlation values, not the canceled parent request context as its lifetime root.
4.9. The process-owned scheduler shall expose a narrow background-auxiliary collection contract with bounded job IDs/results and await/forget behavior; it shall not become a generic arbitrary task engine.
4.10. Equivalent jobs for the same authoritative branch, compaction transaction, and target source revision shall coalesce/idempotently reuse one submission so retries/failover cannot duplicate billable extraction calls.
4.11. Queue saturation, unavailable worker, shutdown, or failed generation retention shall follow preservation failure policy and shall not fall back to an unbounded goroutine/direct provider call.
4.12. Process shutdown shall stop admission, cancel/join worker execution, release generation pins exactly once, and satisfy repository goleak/race requirements.
4.13. Job results shall be revision-checked before merge; stale jobs cannot overwrite newer intent/capsule revisions.
4.14. Completed raw auxiliary results shall be held only in bounded TTL-limited process memory until parsed/consumed, never logged, and explicitly forgotten/expired after normalized capsule/delta storage.
4.15. Disabling/reloading the feature shall prevent new jobs according to the new configuration but shall not erase accounting obligations or leak execution for provider work already submitted.

## Requirement 5: Independently Configurable Route and Detached Session Semantics

5.1. The extractor route/model shall be independently configurable from the main session route/model and may use a completely different provider/model.
5.2. Changing the main session model, alias, or A-leg route override shall not implicitly change the extractor route.
5.3. Extractor routing shall still use normal Go-LIP canonical selector parsing, routing, capability, failover, admission, and attempt machinery; no direct provider client may be opened by the feature.
5.4. The child shall use an explicit configured `Call.Route.Selector` or an explicit `inherit` policy. Missing/invalid route shall not silently inherit the main model.
5.5. Parent A-leg ID shall be correlation only and shall not become child route-override authority.
5.6. The child shall be stamped with stable content-free role/origin equivalent to `compaction_continuity_extractor` / internal auxiliary.
5.7. Tools shall be disabled; the child shall not execute workspace tools, MCP actions, shell commands, or other side effects.
5.8. Extractor output shall be bounded and schema-oriented rather than unconstrained conversational output.
5.9. The preservation plugin shall suppress itself on the child; existing auxiliary-depth recursion limits remain effective.
5.10. Core auxiliary execution shall provide a typed detached-session mode for this workload. It shall preserve authenticated principal/scope and parent correlation while suppressing primary secure-session BeginTurn/turn transcript/last-activity effects.
5.11. Detached execution shall not mutate the primary A-leg route override, primary session turn count, primary session transcript, or client-visible session history.
5.12. Detached-session authority shall be internal execution metadata and shall not be encoded as provider-visible opaque call content.
5.13. The child may use private B2BUA/sessionless execution lineage required for routing/usage/billing, but it shall be clearly distinguishable from the user's primary conversational turn.

## Requirement 6: Originating-User Billing, Usage, and Cost Attribution

6.1. Every semantic extractor invocation is real additional model usage and shall be billable auxiliary inference unless a future explicit operator-funded policy says otherwise.
6.2. By default the child shall inherit the originating authenticated principal/scope so the same customer/account identity is resolved.
6.3. The child shall pass through normal usage/concurrency authority and, where enabled, normal credit screen, route/quote, operational-exposure admission, terminal usage append, customer settlement, and provider-cost processing.
6.4. The auxiliary call shall receive its own BillingCallID and normal B-leg records; it shall not share the primary call's BillingCallID.
6.5. Actual extractor retries/failover B-legs shall be accounted under that auxiliary BillingCallID using existing per-leg semantics.
6.6. User/account aggregate usage/cost totals shall include extractor usage even though the call is not a visible primary turn.
6.7. Primary protocol-visible response usage shall not be inflated by extractor tokens; auxiliary and primary protocol usage remain separate execution records.
6.8. Existing metering/billing/diagnostics shall receive a bounded content-free workload/origin classification identifying continuity extraction, without creating a second money ledger or rating engine.
6.9. Provider COGS shall remain attributed to the actual extractor B-legs/provider/model selected by the extractor route.
6.10. If credit/admission rejects the child before upstream submission, semantic extraction shall skip/fail-open by default; billing policy shall not be bypassed and an unrelated system account shall not be charged.
6.11. If upstream extractor work was submitted, resulting usage remains billable/accountable even if the result is late, invalid, or discarded as stale.
6.12. Enabling the feature shall be documented as potentially generating extra billed inference beyond visible primary turns.
6.13. Operator-funded/system-funded extraction is outside the first implementation and, if later added, must be explicit opt-in accounting policy.

## Requirement 7: Compaction Integration, Barriers, Augmentation, and Reinjection

7.1. For a strict recognized compaction request, semantic extraction shall start no earlier than successful primary compaction B-leg Open, unless completion-only/local evidence is first observable on a request that must be protected pre-open.
7.2. After submission, extraction shall run concurrently with primary compaction work where possible rather than serially blocking the main B-leg for its full inference duration.
7.3. Preservation may await a background job only at a narrow bounded barrier where its result is needed to protect a compaction result or first eligible post-compaction turn.
7.4. Barrier timeout obeys configured failure policy; default fail-open continues native compaction/continuation rather than deadlocking.
7.5. A completed validated capsule may be mechanically added to a compaction result only for a verified mutable plaintext continuation-summary carrier.
7.6. `CompactionItem.EncryptedContent`, opaque provider blobs, signatures, encrypted state, and unknown native compaction payloads shall remain byte-identical.
7.7. If result-side augmentation is unsafe/unavailable, the capsule shall be marked for proxy-owned reinjection on the first eligible post-compaction request.
7.8. Before that first B-leg opens, preservation shall use an already-ready capsule or await its matching in-flight job up to the configured barrier.
7.9. Reinjection shall be canonical-authority-aware: legacy/message-authoritative and item-authoritative calls shall receive a valid bounded proxy-owned instruction/message representation without violating `Call.Validate` authority rules.
7.10. The continuity block shall be versioned/delimited/bounded and distinguishable from user text; it shall state that facts are prior continuation state, not a new user request.
7.11. One capsule revision shall not be injected repeatedly for the same boundary absent an explicit repeated-injection policy.
7.12. Completion-only/local/history compaction first recognized on the post-compaction request may schedule background extraction from the bounded prior source and synchronize before opening that request.
7.13. If deterministic extraction already supplies the necessary capsule, no semantic model job/barrier is required.
7.14. Preservation errors shall not change route selection, failover, no-retry-after-output, or output commitment except for the explicitly bounded pre-output barrier and canonical continuity injection.
7.15. No second LLM pass shall normally rewrite/improve the native summary after one validated semantic extractor result already exists.

## Requirement 8: Repeated Compactions, Branch Scope, Concurrency, Reload, and Restart

8.1. Continuity state shall be keyed by authoritative SessionID when available plus explicit A-leg/branch identity; client session hints alone shall never select another branch's capsule.
8.2. Without secure-session authority, principal-isolated proxy-owned A-leg authority shall be used rather than arbitrary client hints.
8.3. `ScopeSession` may provide the session partition, but the feature key shall additionally include A-leg/branch identity to prevent branch aliasing.
8.4. Capsule updates shall use monotonic revision/compare-and-merge semantics so concurrent turns/jobs cannot overwrite newer explicit intent.
8.5. Duplicate lifecycle signals and B-leg retry/failover shall be idempotent for job submission, merge, and reinjection.
8.6. New unrelated sessions/A-legs start without inherited state.
8.7. Reset/branch replacement shall retire/cancel pending old-branch work under bounded cleanup and shall not leak decisions into the new branch.
8.8. Fork/clone inheritance, if supported, shall be explicit copy-on-fork from a known parent revision; no explicit parent relationship means no inheritance.
8.9. Capsule/source/job state and the background scheduler shall be process-owned so they survive immutable generation replacement/config reload.
8.10. In-flight jobs use immutable route/budget/config and generation captured at submission; later reload affects newly submitted jobs only.
8.11. Three or more successive compactions shall merge `previous capsule + new relevant delta -> new capsule`, not recursively trust only the prior lossy summary.
8.12. Repeated compaction shall not cause monotonic duplicate capsule/prompt growth.
8.13. First implementation restart durability shall be honest: process-local state is not claimed durable across process restart.
8.14. When an authorized durable secure-session transcript already exists, missing capsule state may be reconstructed through the narrow transcript-source adapter; otherwise resume/restart fails open with no hidden capture or cross-session borrowing.
8.15. This spec shall not add a generic durable feature-state/job framework merely to make the capsule restart-durable.

## Requirement 9: Privacy, Security, and Trusted Session Policy

9.1. The feature shall be disabled by default and requires explicit operator enablement.
9.2. A remote extractor route is a new data-egress path and shall be documented/configured as such.
9.3. Applicable redaction/secret policy shall run before extractor egress; raw credentials/secrets shall not be exported merely because they appeared in the session.
9.4. Transcript/source text shall be framed as untrusted data. Embedded user/tool/external instructions cannot override fixed extractor task/schema/system policy.
9.5. The child has no tools and no side-effect capability beyond the configured model request.
9.6. Extractor prompt/output/capsule contents shall not appear in normal logs/metrics/diagnostics by default.
9.7. Content-free IDs, revisions, rule/carrier IDs, counts, sizes, latency, token usage, route/backend/model identifiers, and outcomes may be observed.
9.8. Existing principal/workspace/tenant authorization applies to any transcript/source read.
9.9. Transcript-disabled sessions shall not silently acquire a durable full transcript.
9.10. A bounded sanitized process-local source window is allowed but must be branch-isolated and TTL/size bounded.
9.11. Per-session enable/disable or extractor-route override shall come only from trusted proxy-owned policy/session state; unauthenticated client headers/metadata cannot self-enable egress or choose the billed route.
9.12. Trusted per-session overrides shall remain within operator-defined maxima unless separately authorized.
9.13. Auxiliary lineage/accounting metadata shall not contain raw prompt excerpts or capsule content.

## Requirement 10: Configuration, Failure Policy, and Observability

10.1. Global feature config shall support at least enablement, preserved categories, extractor route, timeout/input/output limits, worker concurrency/queue bounds, barrier timeout, capsule/source/result bounds/TTL, and failure mode.
10.2. Semantic extraction requires an explicit route or explicit inherit policy; no accidental main-model default is allowed.
10.3. Default failure mode shall be fail-open for model traffic: extractor timeout/error/saturation/invalid output/state failure does not make native compaction unusable.
10.4. Fail-open preserves already-valid deterministic capsule state and never injects malformed/partial extractor output.
10.5. Extractor output shall be validated against a strict versioned schema with byte/depth/count limits before merge/injection.
10.6. Observability shall count compaction candidates; deterministic hits; semantic jobs submitted/coalesced/skipped/saturated; latency/outcome; extractor input/output tokens; capsule revision/size/fact counts; barrier waits/timeouts; augmentation/reinjection; stale/conflict rejection.
10.7. Billing/usage observability shall expose auxiliary continuity cost through existing accounting stores rather than duplicating financial truth.
10.8. Config reload shall not mutate active job route/budgets mid-flight.
10.9. Disabling the feature prevents new jobs while allowing bounded cleanup/accounting of already-submitted work.
10.10. No failure path may retry indefinitely, spin, block shutdown indefinitely, or fall back to an unconfigured provider/model.
10.11. Background raw-result retention shall be bounded/TTL-limited and observable only through content-free counts/status; normalized capsule state replaces raw result after consumption.

## Requirement 11: TDD, Architecture, and Non-Interference Gates

11.1. Implementation shall begin with RED tests for capsule merge/supersession, structured carriers, eligibility/sanitization, background lifetime, detached session behavior, independent routing, billing attribution, barriers, repeated compactions, and opaque payload protection.
11.2. Worker packages shall use deterministic scheduling controls plus `goleak`/race tests for saturation, cancellation, shutdown, duplicate submission, stale completion, and barrier races.
11.3. Tests shall prove no extraction job is submitted when a strict compaction-looking request fails before upstream Open.
11.4. Tests shall prove the child is absent from primary secure-session transcript/turn count/last activity and does not mutate primary A-leg route-override/session state.
11.5. Tests shall prove extractor routing can differ from primary routing and that primary A-leg overrides do not rewrite the child selector.
11.6. Billing tests shall prove separate auxiliary BillingCallID/B-leg usage, originating account attribution, aggregate inclusion, continuity workload classification, and unchanged primary protocol usage.
11.7. Tests shall cover pre-submit billing rejection, submitted-but-invalid result, submitted-but-stale result, retry/failover cost, and fail-open behavior.
11.8. Tests shall cover at least three successive compactions with decision supersession, plan progress, dedupe, and bounded growth.
11.9. Tests shall prove encrypted/opaque compaction content is byte-identical with preservation enabled.
11.10. Tests shall prove process generation reload cannot orphan jobs/state and that job submission captured `KindAsync` ownership before the request spawn right ended.
11.11. Architecture gates shall prevent provider/frontend DTO imports, direct provider clients, generic service locators/task runtimes, a second full-transcript store, feature-owned money ledgers, or generation-scoped ownership as the sole worker lifetime.
11.12. The implementation shall preserve canonical call/event validity, output commitment/no-retry-after-output, B2BUA lineage, secure-session ownership, generation pinning, and current billing settlement authorities.
11.13. Focused/repository tests shall run without external model credentials; live extractor tests are optional environment-gated evidence.
11.14. Final review shall remove duplicate rule catalogs, unnecessary persistence/framework layers, unbounded queues/goroutines, provider-specific core branches, and any redundant second LLM summary-rewrite pass.
