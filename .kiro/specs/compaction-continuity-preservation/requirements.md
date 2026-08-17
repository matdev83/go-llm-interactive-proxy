# Requirements Document

## Introduction

Go-LIP shall preserve the decision state that matters for continuing a long coding-agent session across lossy context-compaction events. The feature is not general memory and is not a replacement compactor. Its purpose is narrower: retain the latest accepted plan, explicit user product/architecture decisions, constraints, useful rationale, meaningful rejected alternatives, current plan progress, and unresolved next actions when ordinary compaction would otherwise discard them.

The preserved state shall be represented as a bounded, versioned **Continuity Capsule** rather than an ever-growing natural-language transcript. Structured planning state already exposed by an agent shall be harvested deterministically. A separately configured auxiliary LLM may be used only for genuinely semantic extraction such as identifying conversational acceptance, user decisions, rationale, or supersession that cannot be recovered safely from structured carriers.

Auxiliary semantic extraction is explicitly **off the primary agent session** and runs as independent background work. It may use a completely different model/provider/route from the main coding session. It is nevertheless real additional model usage: by default its usage and cost belong to the same authenticated user/account that caused the extraction and must flow through normal Go-LIP admission, usage, metering, customer billing, and provider-cost accounting.

This specification depends on the compaction-recognition authority defined by the existing `compaction-event-detection` specification. That detector remains metadata-only and non-mutating. Continuity preservation adds a separate content-bearing preservation/interception capability and shall not turn the detector observer into a request/response mutation surface.

## Boundary Context

- **In scope:** continuity-capsule schema/merge semantics; deterministic structured-plan harvesting; bounded semantic-extraction eligibility; sanitized extraction input; background auxiliary execution; independent extractor routing; originating-user billing; compaction-boundary synchronization; verified plaintext augmentation and post-compaction reinjection; repeated compactions; authoritative session/A-leg scoping; trusted session overrides; privacy, observability, and TDD gates.
- **Dependency:** implementation requires the compaction detector/rule authority specified by `compaction-event-detection`. If that runtime capability has not landed, it is a prerequisite rather than a reason to duplicate compaction signatures in this feature.
- **Existing authorities preserved:** core routing/selector parsing; B2BUA A-leg/B-leg lineage; secure-session authority; generation pinning; `ProcessServices` process lifetime; existing auxiliary execution; usage/concurrency authority; BillingCallID-based post-usage billing; canonical stream/retry commitment; existing feature-extension merge/runtime snapshot model.
- **Out of scope:** general RAG/long-term memory; retaining every conversation fact; replacing an agent's own compaction implementation; inventing a new provider client; modifying encrypted/opaque native compaction blobs; billing the extractor to a hidden system account by default; general-purpose durable job orchestration; general agent identity inference; provider-specific branching in core; a second full-transcript database.

## Requirement 1: Shared Compaction Recognition Without Weakening Detection

1.1. Continuity preservation shall reuse the canonical protocol/signature/history recognition authority defined by `compaction-event-detection`; it shall not maintain a second independent compaction-signature matrix.
1.2. The metadata-only `compaction.Observer` contract defined by that specification shall remain non-mutating, content-free, and fail-open.
1.3. A preservation-specific content-bearing contract may be added separately, but it shall not expose request/response content through ordinary compaction observer events.
1.4. Preservation shall be keyed from the same authoritative A-leg and compaction transaction identity used by the detector whenever those values are available.
1.5. A strict start candidate shall not cause a billable semantic-extraction job merely because a signature appears in a request that never successfully opens upstream.
1.6. When a recognized compaction request successfully opens upstream, continuity preservation may start one extraction job for that logical compaction transaction while the primary compaction request continues independently.
1.7. Completion-only/history evidence shall never invent a historical start event, but it may trigger preservation/reinjection behavior needed for a compaction that was only observable after installation.
1.8. Request-preview logic needed to protect the first post-compaction turn shall use the same deterministic matcher/fingerprint authority as committed detection and shall not itself emit lifecycle events or mutate detector transaction state.
1.9. Provider/frontend wire DTOs and provider SDK types shall not enter core compaction-continuity logic.
1.10. Generic words such as `plan`, `summarize`, `continue`, or `compact` shall not by themselves establish a compaction boundary or accepted plan.

## Requirement 2: Versioned Continuity Capsule and Decision Semantics

2.1. Persisted/in-memory continuity state shall use a versioned schema with at least schema version, monotonic revision, source/high-watermark metadata, and a content digest.
2.2. The capsule shall represent the latest accepted plan separately from user decisions/constraints so a plan can progress without rewriting unrelated product decisions.
2.3. Plan steps shall support at least pending, in-progress, completed, and removed/superseded semantics.
2.4. User decisions shall support at least active, superseded, and rejected semantics.
2.5. Each retained semantic fact shall carry enough source provenance to distinguish explicit user text, explicit user acceptance/correction, deterministic structured-plan state, and semantic extractor inference without retaining the whole transcript.
2.6. Explicit later user intent shall supersede conflicting older active intent; two contradictory decisions shall not remain simultaneously active merely because both occurred historically.
2.7. Assistant brainstorming/proposals shall not become authoritative user decisions unless later user evidence accepts, selects, or instructs execution of them.
2.8. A structured current plan exposed authoritatively by the coding harness may be retained as current plan state even when no conversational acceptance sentence is needed.
2.9. Explicit user rejection of an alternative may be retained when it materially constrains future work; a rejected alternative shall not later be presented as the active plan absent new user intent.
2.10. Useful user-provided rationale/trade-offs may be retained when needed to avoid re-litigating a settled choice, but rationale shall remain subordinate to the associated active/superseded decision status.
2.11. Ambiguous semantic evidence shall be omitted or represented as provisional/non-authoritative; it shall not be promoted to an explicit user decision.
2.12. Merge behavior shall be deterministic given the previous capsule and a validated extraction delta: duplicate facts shall be coalesced, statuses shall not regress, and stale revisions shall not overwrite newer state.
2.13. Capsule size shall be bounded by configured byte/token-equivalent limits. Overflow handling shall deterministically prioritize active decisions, active constraints, pending/in-progress plan steps, useful rationale, then historical/completed material.
2.14. Completed plan steps and superseded history may be condensed or dropped under bounded-retention policy, while currently active decisions and unresolved work remain until explicitly superseded/removed or the continuity branch ends.
2.15. The capsule is continuity state, not an audit transcript. It shall not contain arbitrary shell logs, source-file dumps, binary content, credentials, or unrelated tool output.

## Requirement 3: Deterministic-First Extraction and Bounded Source Preparation

3.1. Before invoking an extractor LLM, the feature shall harvest supported machine-readable planning state mechanically from canonical calls/items/tool data.
3.2. Initial deterministic carrier coverage shall include versioned equivalents of Codex `update_plan`, OpenCode todo state, Cline-style task-progress/checklist state where observable, and other stable structured plan carriers established during implementation research.
3.3. Structured carrier matching shall use canonical item/tool shapes rather than provider wire DTOs, and each carrier rule shall have a stable versioned ID plus positive/near-miss fixtures.
3.4. Structured plan data that can be normalized deterministically shall not be sent to an LLM merely to rediscover the same facts.
3.5. Cheap local eligibility heuristics shall decide whether semantic extraction is likely to add information; those heuristics may suppress an unnecessary model call but shall not themselves declare arbitrary prose to be accepted user intent.
3.6. A compaction with no relevant plan/decision/constraint candidates and no stale/missing capsule state shall perform zero semantic-extractor LLM calls.
3.7. Extraction input shall include the previous capsule and only the bounded new decision-relevant context needed to advance its source high-watermark.
3.8. User messages shall receive highest source priority; assistant planning/proposal/clarification content may be retained when needed to interpret later user acceptance/correction.
3.9. Ordinary tool outputs, shell/compiler logs, large file/code dumps, images, video, binary/file payloads, and unrelated external content shall be dropped or heavily truncated by default.
3.10. Structured plan/TODO tool calls and small results may survive source sanitization when their contents are needed for deterministic plan recovery.
3.11. Untrusted tool-result/external text shall be excluded from semantic decision extraction by default unless a narrowly justified rule requires it; if included, it remains untrusted data rather than extractor instructions.
3.12. Source preparation shall be incremental where possible: repeated compactions should cost approximately according to new relevant context plus the bounded prior capsule, not total historical session age.
3.13. The feature may keep a bounded process-local sanitized source window for local/completion-only compactions, but that window shall not become an unbounded or durable shadow transcript.
3.14. When secure-session transcript capture is already enabled and additional historical source is required, the feature may read that existing transcript subject to its authorization/redaction policy instead of creating a second transcript store.

## Requirement 4: Off-Session Background Extraction and Worker Lifetime

4.1. Semantic extraction shall execute as a proxy-created **auxiliary/internal** LLM invocation, not as a visible user/assistant turn in the primary agent conversation.
4.2. The extractor response shall be consumed only by continuity logic and shall not be surfaced to the primary client as an assistant message.
4.3. The extraction call shall have its own request/B-leg execution lifecycle and shall retain parent session/A-leg/trace/principal correlation only as non-authoritative lineage/accounting metadata.
4.4. Background extraction shall run behind a bounded independent asynchronous worker boundary. An in-process worker/goroutine pool is acceptable; an external worker process may be supported later without changing the semantic contract.
4.5. The implementation shall not spawn one unbounded goroutine per candidate/event and shall enforce explicit maximum concurrent jobs and queue capacity.
4.6. Job submission shall acquire any required runtime-generation execution lifetime before the originating request loses its spawn right, so delayed worker execution cannot use a retired generation accidentally.
4.7. Parent request cancellation shall not implicitly convert background execution into inline work; cancellation/retention semantics for a successfully opened compaction shall be explicit and leak-free.
4.8. A background job shall use an independent context/deadline rooted in the worker/service lifetime rather than continuing to depend on a canceled request context for execution.
4.9. Equivalent jobs for the same authoritative continuity key, compaction transaction, and target source revision shall be coalesced/idempotent so retry/failover/lifecycle duplication cannot cause duplicate billable extraction calls.
4.10. Queue saturation, worker unavailability, shutdown, or inability to retain a valid execution generation shall fail according to preservation failure policy and shall not silently fall back to an unbounded goroutine or direct provider call.
4.11. Process shutdown shall stop new job admission, cancel/join worker-owned execution, release retained generation lifetimes exactly once, and leave no goroutines/jobs behind.
4.12. Background job results shall be revision-checked before merge; an older completed job shall not overwrite newer user intent or a newer capsule revision.
4.13. Result/job retention shall be bounded and cleaned after consumption/expiry; the worker subsystem shall not become a general durable queue or arbitrary task scheduler.

## Requirement 5: Independently Configurable Extractor Route and Session Isolation

5.1. The semantic extractor route/model shall be independently configurable from the main session's route/model.
5.2. The extractor may use a completely different provider and model from the primary coding session.
5.3. Changing the primary session route, model alias, or A-leg routing override shall not implicitly change the configured extractor route.
5.4. An extractor route shall still be parsed/resolved by normal Go-LIP routing and capability/admission logic; the feature shall not select/open provider clients directly.
5.5. If the configured extractor selector uses normal failover/alias syntax, its attempts shall follow the same pre-output recovery and no-retry-after-output rules as other canonical calls.
5.6. The auxiliary call shall be stamped with a stable role/origin equivalent to `compaction_continuity_extractor` / internal auxiliary for diagnostics/accounting classification.
5.7. The extractor call shall have tools disabled and shall not be capable of executing workspace tools, MCP actions, shell commands, or other side effects.
5.8. The extractor shall use strict bounded output intended for schema validation, not an unconstrained conversational continuation.
5.9. The preservation feature itself shall be suppressed on its auxiliary extraction call, and existing auxiliary-depth recursion protection shall remain effective.
5.10. Parent session IDs/A-leg IDs carried on the child request are correlation only; they shall not cause the auxiliary call to append a primary secure-session turn, change primary last-activity/turn counts, mutate primary route overrides, or become user-visible session history.
5.11. The child call may have its own private execution/B2BUA lineage as required for routing, attempts, usage, and billing, but that lineage shall remain distinguishable from the primary user's conversational turn lineage.
5.12. A missing/invalid extractor route shall not silently inherit an expensive main-session model unless an explicit `inherit` policy is configured; the default behavior shall be validation failure or semantic-extraction disablement according to configuration rules.

## Requirement 6: Originating-User Billing, Usage, and Cost Attribution

6.1. Every semantic extractor LLM invocation is real additional model usage and shall be treated as billable auxiliary inference unless an explicit future operator-funded policy says otherwise.
6.2. By default, the extractor shall inherit the originating authenticated principal/scope so customer/account identity resolves to the same user/account that owns the triggering session.
6.3. The extractor shall pass through normal usage/concurrency authority and, where authoritative billing is enabled, the normal cheap credit screen, route/quote, atomic operational-exposure admission, terminal usage append, customer settlement, and provider-cost processing.
6.4. The auxiliary extractor shall receive its own BillingCallID and normal B-leg attempt records; its financial state shall not be merged into the primary call's BillingCallID.
6.5. Retries/failover B-legs that actually submit extractor work shall be accounted under the auxiliary BillingCallID using existing per-leg semantics.
6.6. Account/user aggregate usage and cost totals shall include extractor usage even though the call is not visible as a primary conversational turn.
6.7. Primary protocol-visible response usage shall not be inflated by or merged with extractor input/output/cache tokens; auxiliary and primary protocol usage remain separate execution records.
6.8. Operator/user-facing accounting evidence shall make auxiliary continuity-extraction usage distinguishable from primary inference through a content-free role/origin/workload dimension.
6.9. Provider COGS for extractor B-legs shall remain attributable to those actual B-legs and shall not be charged to the provider/model used by the primary session unless that is the extractor route selected.
6.10. If admission/credit policy rejects the auxiliary call before upstream submission, semantic extraction shall be skipped/fail-open by default; the proxy shall not bypass billing policy or silently charge an unrelated system account.
6.11. If upstream extractor work has already been submitted, resulting usage remains attributable/billable according to normal accounting even if its result arrives too late, is invalid, or is discarded as stale.
6.12. Enabling continuity preservation shall be documented as potentially generating extra billed inference beyond primary turns visible to the user.
6.13. An operator-funded/system-funded extractor is outside the first implementation; if introduced later it must be explicit opt-in accounting policy rather than implicit behavior.

## Requirement 7: Compaction Integration, Bounded Barriers, and Reinjection

7.1. For a strict recognized compaction request, semantic extraction shall start no earlier than the point where the primary compaction B-leg has successfully opened unless an already-authoritative local completion requires pre-open recovery.
7.2. Once started, the extractor shall run concurrently with the primary compaction work where possible rather than serially blocking the main B-leg for its full inference duration.
7.3. Preservation may join/wait for a relevant background job only at a narrow explicitly bounded preservation barrier where the result is needed to protect the compaction result or first post-compaction turn.
7.4. A barrier timeout shall obey configured failure policy; default fail-open behavior continues native compaction/continuation rather than deadlocking the session.
7.5. A completed validated capsule may be mechanically added to a compaction result only when Go-LIP has a verified mutable plaintext continuation-summary carrier for that dialect/path.
7.6. `CompactionItem.EncryptedContent`, provider opaque blobs, signatures, encrypted state, and unknown native compaction payloads shall never be rewritten to inject continuity text.
7.7. If the current compaction result cannot be safely augmented, the capsule shall be marked for proxy-owned reinjection on the first eligible post-compaction request.
7.8. Before that first eligible post-compaction B-leg opens, continuity logic shall use an already-ready capsule or await the matching in-flight extraction job up to the configured barrier limit.
7.9. Reinjection shall use canonical authority-aware request mutation: legacy/message-authoritative calls and item-authoritative calls shall receive a valid bounded proxy-owned instruction/message representation without violating canonical call invariants.
7.10. The injected continuity block shall be clearly delimited/versioned, content-bounded, and distinguishable from user text; it shall instruct the model to treat the facts as continuation state rather than new user input.
7.11. A capsule revision shall not be injected more than once for the same post-compaction boundary unless an explicit repeated-injection policy is configured.
7.12. Completion-only/local/history compaction detected on the first post-compaction request may schedule semantic extraction from the bounded pre-compaction source and synchronize before opening that request when necessary.
7.13. When deterministic extraction alone provides a sufficient current capsule, reinjection/augmentation shall not wait for or create a semantic-extractor job.
7.14. Preservation hooks/errors shall not alter routing/retry/output-commit semantics other than the explicitly bounded pre-output synchronization and canonical continuity injection described here.
7.15. No second LLM round trip shall normally be used merely to rewrite/improve the agent's native summary after the continuity extractor already produced a validated capsule.

## Requirement 8: Repeated Compactions, Scope, Concurrency, and Reload Behavior

8.1. Continuity state shall be keyed by authoritative branch identity: authoritative SessionID when available plus A-leg/branch identity; client-supplied session hints alone shall not authorize access to another branch's capsule.
8.2. Where secure-session authority is unavailable, the proxy-owned A-leg plus principal/scope isolation shall be used rather than trusting arbitrary client hints.
8.3. Each capsule merge shall use monotonic revision/compare-and-merge semantics so concurrent turns/jobs cannot let stale extraction overwrite a newer explicit decision.
8.4. Replayed/duplicate compaction lifecycle signals and B-leg retry/failover shall be idempotent with respect to job submission, capsule merge, and reinjection.
8.5. A new unrelated session or unrelated A-leg shall begin without inherited capsule state.
8.6. Reset/branch replacement shall retire/cancel pending work for the old branch according to bounded cleanup policy; it shall not leak decisions into the new branch.
8.7. Fork/clone inheritance, if supported, shall be explicit copy-on-fork from a known parent revision. Absence of an explicit fork relationship means no inheritance.
8.8. Process-owned capsule/job/source state shall survive immutable runtime-generation replacement and configuration reload while the process remains alive.
8.9. In-flight extraction shall use the extractor configuration/route snapshot captured at job submission; a later config reload affects only newly submitted jobs.
8.10. Three or more successive compactions shall merge `previous capsule + new relevant delta -> new capsule` rather than recursively trusting only the previous lossy summary.
8.11. Repeated compaction shall not cause monotonic duplicate prompt/capsule growth; dedupe and size bounds apply at every revision.
8.12. Process restart durability shall be explicit: this feature shall not claim restart-surviving capsule state unless an existing authorized durable source can reconstruct it or a separately designed durable capsule store is configured.
8.13. If a durable secure-session transcript is available and policy allows it, a resumed session may reconstruct missing capsule state from that transcript; if not, resume fails open with no hidden transcript capture or cross-session borrowing.

## Requirement 9: Privacy, Security, and Trusted Session Policy

9.1. The feature shall be disabled by default unless the operator explicitly enables it.
9.2. A remote extractor route creates a new data-egress path and shall be documented/configured as such.
9.3. Extractor input shall pass through applicable redaction/secret-handling policy before remote egress; raw credentials/secrets shall not be sent merely because they appeared in the session.
9.4. Transcript text supplied to the extractor shall be framed as untrusted data. Instructions embedded in user/tool/external content shall not override the fixed extractor task/schema/system policy.
9.5. The extractor shall have no tools and no direct filesystem/network side-effect capability beyond the configured model request.
9.6. Extractor prompt/output/capsule contents shall not be logged, emitted in normal metrics, or placed in ordinary diagnostics by default.
9.7. Metrics/diagnostics may expose content-free IDs, revisions, rule IDs, counts, sizes, latency, token usage, route/backend/model identifiers, and outcome/error classes.
9.8. Existing secure-session principal/workspace/tenant isolation shall apply to any transcript/source read used for extraction.
9.9. When transcript capture is disabled, this feature shall not silently enable durable full-transcript capture solely to support extraction.
9.10. A bounded process-local sanitized source window is permitted when needed, but it shall be isolated by authoritative continuity key and cleared/expired under bounded retention policy.
9.11. Per-session enable/disable or extractor-route override shall come only from trusted proxy-owned policy/session state. Unauthenticated client headers/metadata shall not self-enable external transcript export or choose the billed extractor route.
9.12. Trusted per-session policy shall override global defaults only within operator-defined allowed bounds; it shall not increase egress/worker/token limits beyond configured maxima unless explicitly authorized.
9.13. Auxiliary lineage/correlation metadata exposed to logs/accounting shall not include raw prompt excerpts or capsule content.

## Requirement 10: Configuration, Failure Policy, and Observability

10.1. Global feature configuration shall support at minimum enablement, deterministic/semantic preservation categories, extractor route, extractor timeout/input/output bounds, background concurrency/queue bounds, barrier timeout, capsule bound, retention/TTL, and failure mode.
10.2. Semantic extraction shall require an explicit route or explicit inherit policy; configuration shall not silently select a main-session model by accident.
10.3. Default failure mode shall be fail-open for model traffic: extractor timeout/error, saturation, invalid output, or persistence/state failure shall not make ordinary compaction unusable.
10.4. Fail-open shall preserve any already-valid deterministic capsule state and native compaction behavior rather than injecting malformed/partial model output.
10.5. Extractor output shall be parsed against a strict versioned schema with byte/depth/count limits before any merge or injection; malformed or extra-authority output shall be rejected.
10.6. Observability shall count compaction candidates, deterministic hits, semantic jobs submitted/coalesced/skipped/saturated, job latency/outcome, extractor input/output tokens, capsule revision/size/fact counts, barrier waits/timeouts, augmentations, reinjections, and stale/conflict rejections.
10.7. Billing/usage observability shall expose auxiliary continuity cost separately from primary inference without duplicating financial truth outside existing billing/metering stores.
10.8. Configuration reload shall not mutate active job routing/budgets mid-flight; new jobs use the newly compiled generation/config.
10.9. Disabling the feature for future turns shall prevent new extraction jobs while allowing bounded cleanup/settlement of already-submitted billable jobs.
10.10. No extractor failure path shall retry indefinitely, spin, block shutdown indefinitely, or fall back to an unconfigured provider/model.

## Requirement 11: TDD, Architecture, and Non-Interference Gates

11.1. Implementation shall begin with RED tests for capsule merge/supersession, structured carriers, eligibility/sanitization, background job lifetime, session isolation, independent routing, billing attribution, compaction barriers, repeated compactions, and opaque payload protection.
11.2. Worker/scheduler packages shall use deterministic scheduling controls and `goleak`/race tests for queue saturation, cancellation, shutdown, duplicate submissions, stale completion, and barrier races.
11.3. Tests shall prove an extraction job is not submitted when the compaction-looking request fails before upstream Open.
11.4. Tests shall prove the auxiliary child is absent from the primary secure-session transcript/turn count and does not mutate primary A-leg route-override/session state.
11.5. Tests shall prove the extractor may route to a different backend/model than the primary call and that a primary A-leg routing override does not rewrite the extractor selector.
11.6. Billing tests shall prove the child has its own BillingCallID/B-leg usage, inherits the originating account, contributes to account totals, remains distinguishable as auxiliary continuity work, and does not alter primary protocol-visible usage.
11.7. Tests shall cover extractor rejection before upstream submission, submitted-but-invalid result, submitted-but-stale result, retry/failover cost, and fail-open behavior.
11.8. Tests shall cover at least three successive compactions with decision supersession, completed/pending plan progress, dedupe, and bounded capsule growth.
11.9. Tests shall prove encrypted/opaque compaction content is byte-identical when continuity preservation is enabled.
11.10. Architecture gates shall prevent provider/frontend DTO imports, direct provider clients, generic service locators/task runtimes, a second full-transcript store, and financial mutation outside existing billing authorities.
11.11. The implementation shall preserve canonical request/event validity, output-commit/no-retry-after-output semantics, B2BUA lineage, secure-session ownership, generation pinning, and current billing settlement authorities.
11.12. Focused and repository quality tests shall run without external model credentials; live extractor tests, if any, are optional environment-gated evidence rather than a correctness prerequisite.
11.13. Final review shall remove duplicate rule catalogs, unnecessary persistence/framework layers, unbounded queues/goroutines, provider-specific core branches, and any second LLM summary-rewrite pass not required by the accepted design.
