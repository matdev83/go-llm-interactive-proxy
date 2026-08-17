# Implementation Plan

## Execution Rules

- Follow TDD strictly: characterization/RED tests and contract gates precede production implementation in each dependency layer.
- `compaction-event-detection` runtime is a hard prerequisite. Do not duplicate its signature matrix if it is not yet implemented; land that spec first.
- Keep every task independently reviewable with **no more than five concrete actions**.
- Preserve existing routing, B2BUA, secure-session, generation pinning, output commitment, usage authority, BillingCallID accounting, and provider-opaque compaction semantics.
- Auxiliary semantic extraction is a separate off-session background model call, independently routed and billed to the originating user/account by default.
- Do not add provider-specific continuity branches, a direct provider client, second transcript store, feature-owned money ledger, generic task/workflow engine, or second LLM summary-rewrite pass.
- No real model credentials are required for correctness tests; use deterministic local/fake backends and fixture JSON.

## Phase 1 — Freeze Contracts and Failure Boundaries With RED Tests

### Task 1.1 — Freeze prerequisite detector preview and final-release semantics

- Verify the #312 runtime detector exists; if absent, stop this spec's implementation and complete `compaction-event-detection` first rather than adding fallback signatures.
- Add RED tests for pure `PreviewRequest` and `PreviewResponse` sharing the same rule/fingerprint authority as committed detection without mutating detector state or emitting lifecycle events.
- Prove a strict preview on a request that never opens cannot create a started transaction or semantic extraction submission.
- Prove committed `ResponseReleased` receives the exact event after permitted preservation finalization, not a pre-final copy.
- Add near-miss and completion-only preview fixtures from the existing detector rule matrix.

_Requirements: 1.1–1.13, 7.1, 11.3, 11.10_

_Validation: focused `internal/core/compactiondetect` RED tests; no duplicated rule catalog._

### Task 1.2 — Freeze the separate preservation extension contract

- Add RED SDK tests for a content-bearing `compaction.Preserver`-style contract that is distinct from metadata-only `compaction.Observer`.
- Add RED FeatureBundle/merge/runtime-snapshot tests for ordered additive preservers, defensive copies, nil validation and frozen generation semantics.
- Prove ordinary compaction observer event payloads still contain no canonical request/response content and cannot mutate traffic.
- Freeze preservation callback ordering: BeforeRequest -> successful-open callback -> response-preview finalization -> committed detector observation -> metadata observer dispatch.
- Add architecture assertions preventing preservation mutation from being implemented as an ordinary response hook after the finalization seam.

_Requirements: 1.2–1.5, 1.9–1.11, 11.12–11.13_

_Validation: `go test ./pkg/lipsdk/... ./internal/featurebundle/... ./internal/core/extensions/...` is RED for the new contract._

### Task 1.3 — Freeze background auxiliary scheduling and generation ownership

- Add deterministic RED scheduler tests for bounded queue/concurrency, coalescing, result retention, Await/Forget, and saturation without goroutine fallback.
- Prove `SubmitCollect` retains `genpin.KindAsync` synchronously while the parent spawn right is live and a worker never retains a replacement generation later.
- Add enqueue-failure/cancel/timeout/shutdown paths proving every retained pin is released exactly once.
- Add a delayed-start test where the parent request context is canceled before the worker runs but the worker uses its own bounded context safely.
- Add `goleak` and race tests for queue saturation, shutdown, concurrent Await/Forget and late job completion.

_Requirements: 4.4–4.15, 8.9–8.10, 11.2, 11.11_

_Validation: focused `internal/core/auxreq` tests are RED and deterministic._

### Task 1.4 — Freeze detached child A-leg, routing, and session isolation

- Add RED tests proving a detached child creates a private auxiliary A-leg and never reuses the parent's A-leg as execution authority.
- Prove parent SessionID/A-leg/trace survive only as lineage while primary secure-session BeginTurn, transcript, activity and turn count remain unchanged.
- Prove the child uses its explicit extractor selector and a primary A-leg runtime route override cannot rewrite it.
- Prove detached mode is trusted auxiliary metadata unavailable from frontend/wire canonical fields.
- Cover private child B-leg failover/terminal lineage without client-visible session headers/history.

_Requirements: 5.1–5.15, 8.1–8.3, 9.11, 11.4–11.5_

_Validation: runtime/auxreq/secure-session/B2BUA focused tests are RED._

### Task 1.5 — Freeze capsule semantics, billing attribution, and repeated-compaction outcomes

- Add RED table tests for capsule revision, precedence, decision supersession, rejection, plan-step progress, stale merge rejection and deterministic pruning.
- Add RED structured carrier fixtures for Codex update-plan, OpenCode todo and supported Cline task-progress shapes plus near misses.
- Add RED billing tests requiring the child to inherit the originating account while receiving a separate BillingCallID/B-legs and auxiliary workload classification.
- Prove primary protocol-visible usage excludes extractor tokens while account/operator totals include the independent child usage.
- Add an end-to-end RED scenario with at least three compactions, user decision correction, completed/pending plan steps, dedupe and bounded capsule growth.

_Requirements: 2.1–2.15, 3.1–3.7, 6.1–6.13, 8.4–8.12, 11.6–11.8_

_Validation: feature/billing/integration target tests are RED before production feature code._

## Phase 2 — Implement Minimal Shared Infrastructure

### Task 2.1 — Implement pure detector request/response previews

- Refactor prerequisite detector match/fingerprint code so preview and committed paths call one shared internal recognition authority.
- Implement `PreviewRequest` without state mutation and preserve existing successful-Open commit behavior in `RequestOpened`.
- Implement `PreviewResponse` without completion mutation and keep `ResponseReleased` as the committed final-event boundary.
- Make Task 1.1 positives/near misses/state-snapshot assertions green without changing observer payloads.
- Keep unsupported protocol controls and provider DTOs outside the detector.

_Requirements: 1.1, 1.5–1.13_

_Design: D1; D13–D14_

_Validation: focused detector suites green; existing #312 tests remain unchanged/green._

### Task 2.2 — Implement and compose the preservation SDK surface

- Add the separate preservation types/services/interfaces beside the existing compaction observer contract.
- Add `CompactionPreservers` to FeatureBundle, one merge surface and frozen request-snapshot accessor with existing validation conventions.
- Add core extension dispatch helpers that isolate preserver panic/error under configured preservation fail-open semantics.
- Wire the semantic stage order without introducing a mutating generic StageID or moving ordinary response hooks after finalization.
- Make Task 1.2 SDK/merge/order tests green.

_Requirements: 1.2–1.4, 1.10–1.11, 10.3–10.5_

_Design: D2_

_Validation: SDK/featurebundle/extensions tests green._

### Task 2.3 — Implement the process-owned BackgroundAux collector

- Add additive BackgroundClient/JobID/options APIs while keeping synchronous `auxiliary.Client` source-compatible.
- Implement the bounded `internal/core/auxreq` scheduler with keyed coalescing, worker pool, result registry and process-root context.
- Implement submit-time runner capture/KindAsync retention and exact cleanup on enqueue failure, terminal completion, cancellation and shutdown.
- Implement useful bounded pending-result retention plus Await/Forget without arbitrary callbacks or durable task semantics.
- Register scheduler ownership/Close through ProcessServices and make Task 1.3 green under race/goleak.

_Requirements: 4.4–4.15, 8.9–8.10, 10.11–10.12_

_Design: D3; D22_

_Validation: focused auxreq/runtimebundle tests green; scheduler has no network dependency._

### Task 2.4 — Implement detached auxiliary execution with a private child A-leg

- Add trusted internal detached-session policy on auxiliary execution without adding a frontend/wire `lipapi.Call` control.
- Factor Executor preparation so detached calls preserve principal/scope but skip primary secure-session BeginTurn/activity/transcript/resume effects.
- Create/touch a private child A-leg through existing B2BUA lifecycle and keep parent A-leg/session values as lineage only.
- Ensure detached route planning does not read the parent's route override and ordinary child request authority/B-legs still execute.
- Make Task 1.4 session/B2BUA/routing tests green without copying the Executor.

_Requirements: 5.5–5.15, 9.11, 11.4–11.5, 11.13_

_Design: D4–D5_

_Validation: focused runtime/auxreq/B2BUA/secure-session tests green._

### Task 2.5 — Implement the process branch coordinator

- Add a narrow ProcessServices-owned coordinator keyed by authoritative SessionID + A-leg/branch or principal-isolated A-leg fallback.
- Serialize branch revision/high-watermark/pending-job/injection updates and use process ExtensionState as opaque serialized backing where practical.
- Enforce max entries/TTL/lazy cleanup and never call model/provider/plugin code while coordinator locks are held.
- Expose only the preservation state operations needed by the compaction services; keep capsule/source semantics opaque to core.
- Add reload/concurrency tests proving old/new generations and workers cannot overwrite branch state out of order.

_Requirements: 8.1–8.15, 10.12, 11.11–11.12_

_Design: D7; D22–D23_

_Validation: coordinator unit/race tests green and no generic transactional state framework appears._

## Phase 3 — Implement the Continuity Feature Semantics

### Task 3.1 — Implement feature configuration and prerequisite validation

- Register the official `compaction-continuity` feature and decode bounded preserve/extractor/worker/barrier/capsule/source/result/failure settings.
- Require explicit extractor route or explicit inherit when semantic extraction is enabled; feature remains disabled by default.
- Validate finite positive maxima and consistency of pending-result/source/branch retention.
- Fail enabled generation/startup composition clearly when detector preview/commit, BranchCoordinator or BackgroundAux services are absent.
- Add config reload tests proving in-flight jobs retain immutable submission-time config while new jobs use new generation config.

_Requirements: 9.1–9.2, 10.1–10.3, 10.9–10.12_

_Design: Dependency Gate; D17–D19_

_Validation: config/runtimebundle feature tests green._

### Task 3.2 — Implement Continuity Capsule v1 and deterministic carrier rules

- Implement versioned capsule/fact/plan types, stable IDs, source authority/status enums and strict validation.
- Implement deterministic precedence, stale-revision rejection, dedupe and whole-fact bounded pruning.
- Add versioned canonical carrier rules for the researched Codex/OpenCode/Cline plan shapes without agent-brand inference.
- Normalize supported carrier updates into capsule plan state without an LLM call.
- Make Task 1.5 capsule/carrier tests green including malformed/near-miss fixtures.

_Requirements: 2.1–2.15, 3.1–3.5, 8.4, 8.11–8.12_

_Design: D8–D9_

_Validation: feature pure-unit tests green._

### Task 3.3 — Implement sanitized incremental source preparation and eligibility

- Walk canonical calls into a bounded source envelope prioritizing user decisions, relevant assistant planning and recognized plan carriers.
- Drop/truncate ordinary tool results, logs, file/code dumps, media/binaries, unnecessary reasoning and unrelated external content.
- Mark any narrowly retained external/tool material as untrusted data and apply existing redaction/secret treatment before egress.
- Implement local semantic-eligibility heuristics that only decide whether to pay for extraction and never establish accepted intent themselves.
- Commit sanitized source/high-watermark only after successfully opened primary requests and make zero-call/no-candidate tests green.

_Requirements: 3.6–3.16, 9.3–9.10_

_Design: D10; D12_

_Validation: sanitizer/eligibility privacy fixtures green with no external calls._

### Task 3.4 — Implement the semantic extractor child call and strict result parser

- Build one detached no-tools auxiliary child using the configured independent selector, prior capsule, deterministic plan facts and sanitized delta.
- Suppress the continuity plugin and stamp private role/origin/parent lineage without primary session authority.
- Define the fixed extraction prompt/schema requiring explicit-user precedence, accepted/current plan semantics, supersession and ambiguity omission.
- Parse exactly one bounded JSON result with schema/base-revision/enums/depth/count/string/source-ref validation; discard malformed/authority-escalating output.
- Add fake-backend tests proving no second LLM summary-rewrite call is generated.

_Requirements: 3.8–3.13, 5.1–5.9, 9.4–9.6, 10.6, 11.15_

_Design: D5; D11–D12_

_Validation: deterministic child-call/parser tests green._

### Task 3.5 — Integrate validated deltas with branch state and late results

- On Await success, validate base revision then merge the extractor delta through the BranchCoordinator into a new bounded capsule revision.
- Reject/forget stale results without changing active decisions or reinjection watermarks.
- Keep PendingJobID/result useful across a timed-out response barrier until bounded next-turn consumption or coherent expiry.
- Immediately Forget raw collected output after validation/merge and store only normalized capsule/source/job metadata.
- Add concurrent explicit-user-correction versus late-worker tests proving newer intent always wins.

_Requirements: 2.12–2.15, 4.13–4.15, 8.4–8.5, 10.12_

_Design: D3; D7–D8_

_Validation: branch/feature concurrency tests green under `-race`._

## Phase 4 — Integrate Preservation at Compaction Boundaries

### Task 4.1 — Schedule extraction after a real strict compaction Open

- At successful primary request Open, obtain committed detector events and invoke preservation with the effective canonical baseline/correlation.
- Commit the sanitized source and deterministic plan updates before deciding whether semantic extraction is necessary.
- Submit at most one coalesced semantic job for the detector transaction/source revision; never submit from an unopened strict preview.
- Let the primary compaction stream proceed immediately after submission without waiting for extractor completion.
- Cover retry/failover start dedupe and failed-Open zero-billing behavior.

_Requirements: 1.5–1.8, 3.13, 4.9–4.10, 7.1–7.2, 11.3_

_Design: D13_

_Validation: runtime integration tests green with deterministic delayed extractor backend._

### Task 4.2 — Implement response-preview finalization and bounded completion barrier

- Route each final selected event through pure detector `PreviewResponse` before committed `ResponseReleased`.
- Let preservation resolve/await the matching job only for the configured bounded barrier and merge a valid ready result.
- Keep ordinary hooks/gates/finalizers before this stage and committed detector observation/metadata observers/client delivery after it.
- On timeout/error, mark the appropriate pending state and continue fail-open without blocking indefinitely.
- Prove committed detector and client both see the exact post-preservation final event.

_Requirements: 1.10–1.11, 7.2–7.5, 10.4–10.5, 11.10_

_Design: D1–D2; D13_

_Validation: final-stream release/ordering tests green across live/gated/recovery paths._

### Task 4.3 — Protect completion-only/local first post-compaction turns

- Before B-leg Open, use pure request preview to recognize installed/completion-only local compaction without committing detector state.
- Load prior sanitized source/capsule and apply deterministic plan updates; submit one background job only if semantic eligibility requires it.
- Await only up to the bounded barrier and inject a valid ready capsule before the first post-compaction B-leg opens.
- Continue fail-open on timeout/failure and commit detector completion only after the primary request successfully opens.
- Cover reset/new-A-leg/near-miss cases so an unrelated short rewrite does not trigger extraction/injection.

_Requirements: 1.8–1.9, 7.3–7.4, 7.8, 7.12–7.14, 8.5–8.7_

_Design: D14_

_Validation: local-compaction history/integration tests green._

### Task 4.4 — Implement authority-aware first-turn reinjection

- Serialize the current capsule deterministically into one versioned/delimited bounded continuity block.
- Inject through a canonical helper that uses legal message-authority versus item-authority representation and preserves `Call.Validate` invariants.
- Track PendingInjectionRevision/LastInjectedRevision so one boundary/revision is injected at most once by default.
- Ensure injected text is proxy-owned prior context, not rewritten as a user message or copied into unrelated branch state.
- Add legacy-message and item-authority tests plus duplicate/retry/reload idempotency tests.

_Requirements: 7.7–7.11, 8.1–8.5, 11.13_

_Design: D15_

_Validation: lipapi/runtime feature tests green for both canonical authorities._

### Task 4.5 — Implement verified plaintext augmentation and opaque exact-preservation fallback

- Define the minimal verified plaintext continuation-carrier capability/matcher; default unknown/native compaction paths to no response mutation.
- Mechanically merge the ready capsule projection only on an allowed plaintext carrier; never use another LLM to rewrite it.
- Mark pending first-turn reinjection whenever carrier is opaque/unsupported or extractor result is not ready.
- Add exact byte comparisons for `CompactionItem.EncryptedContent`, `Opaque`, signatures and unknown extension blobs with feature enabled/disabled.
- Prove reinjection alone preserves continuity when result augmentation is unavailable.

_Requirements: 7.5–7.7, 7.15, 11.9, 11.15_

_Design: D16_

_Validation: opaque/native protocol fixtures green with byte identity._

## Phase 5 — Complete Billing, Policy, Privacy, and Observability

### Task 5.1 — Project auxiliary workload identity through normal billing/metering

- Reuse existing auxiliary lineage as the source of a bounded workload class/role in usage, metering, billing and report correlation.
- Keep pricing/rating behavior unchanged unless existing operator policy explicitly differentiates the selected route/model.
- Prove the child receives its own BillingCallID and per-B-leg provider COGS while account identity remains the originating principal.
- Cover child failover, pre-submit credit rejection and submitted-but-discarded result accounting.
- Make Task 1.5 billing tests green without adding a feature-owned financial store.

_Requirements: 6.1–6.13, 10.8, 11.6–11.7_

_Design: D6_

_Validation: focused billing/metering/report tests green._

### Task 5.2 — Certify primary protocol-usage and secure-session isolation

- Prove primary frontend usage events/totals contain only primary call usage even when the extractor runs concurrently.
- Prove account/operator totals include both primary and auxiliary records and can distinguish continuity workload.
- Prove detached child causes no primary session transcript entry, TurnID increment, last-activity mutation, resume effect or client session header.
- Prove private child A-leg/attempt lineage remains available for operator/accounting diagnostics.
- Cover cancellation/timeout/failover paths for both parent and child without cross-settlement.

_Requirements: 4.1–4.3, 5.10–5.15, 6.4–6.9, 11.4, 11.6_

_Validation: frontend/session/B2BUA/billing integration tests green._

### Task 5.3 — Implement trusted per-session policy and egress controls

- Resolve effective continuity policy as operator hard maxima > trusted session values > global feature defaults.
- Allow only explicitly approved per-session enable/category/route/tighter-limit overrides; reject/ignore unauthenticated client attempts as specified.
- Apply existing redaction/secret treatment before semantic child egress and preserve tenant/workspace authorization on any optional transcript read.
- Keep transcript-disabled sessions from acquiring a hidden durable transcript and isolate bounded source by branch key.
- Add adversarial prompt-injection/secret/tool-output fixtures proving source text cannot override extractor instructions or leak excluded payloads.

_Requirements: 9.1–9.13, 10.1–10.2_

_Design: D17; D20_

_Validation: feature/security/session-policy tests green._

### Task 5.4 — Implement failure handling and content-free observability

- Add metrics for previews/events, carrier hits, eligibility skips, job queue/outcomes, token usage, capsule sizes/revisions, barriers, augmentation/reinjection and stale conflicts.
- Log only bounded IDs/hashes/status/counts and never extractor prompt/output/capsule text.
- Implement explicit fail-open handling for queue saturation, generation retain failure, child admission denial, provider failure, invalid schema, stale result and barrier timeout.
- Ensure disable/reload stops new jobs but preserves bounded completion/accounting of already-submitted work.
- Add tests proving no failure path spins/retries indefinitely, chooses an unconfigured model, or blocks shutdown indefinitely.

_Requirements: 9.6–9.7, 10.3–10.12_

_Design: D19–D21_

_Validation: metrics/log/failure-path tests green._

### Task 5.5 — Document operator behavior, billing cost, and durability limits

- Document feature enablement, independent extractor selector/model, worker/barrier/capsule bounds and fail-open semantics in standard configuration/operator docs.
- State explicitly that extractor inference is additional billable user-attributed usage and show how auxiliary cost is distinguished from primary inference.
- Document remote history egress/privacy implications and that the extractor is off-session/no-tools.
- Document v1 process/generation durability versus process-restart limitations and optional authorized transcript reconstruction behavior.
- Document #312 prerequisite and troubleshooting for missing detector/background services or rejected extractor billing/admission.

_Requirements: 6.12–6.13, 8.13–8.15, 9.1–9.2, 10.1–10.3_

_Validation: docs/config examples pass repository docs/config validation._

## Phase 6 — Certify Repeated Compaction, Concurrency, ROI, and Simplicity

### Task 6.1 — Run the full repeated-compaction semantic matrix

- Exercise at least three successive compactions with accepted plan, product decisions, constraints, rationale, rejections and open questions.
- Change a decision after the first compaction and prove the older value remains superseded through later compactions.
- Advance plan steps through pending -> in-progress -> completed and prove pending/current work survives while completed history can be pruned.
- Exercise deterministic-only, semantic-extractor and mixed paths and prove no duplicate model call when deterministic state is sufficient.
- Verify capsule/prompt size remains bounded without monotonically duplicated facts/injection blocks.

_Requirements: 2.6–2.14, 3.5–3.7, 8.10–8.12, 11.8_

_Validation: deterministic multi-compaction integration suite green._

### Task 6.2 — Certify concurrency, generation reload, and stale-result safety

- Race concurrent primary turns, late extractor completion and explicit user correction on one branch; newer explicit intent must win.
- Reload to a new immutable generation while an extractor job is queued/running and prove BranchCoordinator/job ownership survives correctly.
- Change/disable extractor config across reload and prove old jobs retain captured route/budgets while new jobs use new policy or do not submit.
- Exercise branch reset/new A-leg/fork-no-parent and prove no capsule/job leakage between branches.
- Verify pending result/job expiry coherently clears BranchState without stale injection.

_Requirements: 4.13–4.15, 8.1–8.10, 10.9–10.12, 11.11_

_Validation: focused `-race` reload/concurrency suite green._

### Task 6.3 — Certify worker/resource shutdown and leak freedom

- Run queue saturation, worker timeout, client cancellation, process shutdown and late completion under `-race` and goleak.
- Assert every successful background submission retains/releases exactly one `KindAsync` pin and no job starts after scheduler close linearizes.
- Assert no provider/model call or plugin callback runs while scheduler/BranchCoordinator internal locks are held.
- Assert bounded queue/result/branch state under sustained synthetic compaction load.
- Run existing generation pin/ProcessServices shutdown tests to ensure no ownership regression.

_Requirements: 4.4–4.15, 8.9, 11.2, 11.11–11.13_

_Validation: race/goleak/process lifecycle tests green._

### Task 6.4 — Run architecture and security scope gates

- Add/execute architecture tests forbidding provider/frontend DTO imports, direct provider clients and provider-specific continuity branches in core/feature packages.
- Prove detached mode cannot be set through frontend/wire fields and continuity source/capsule/raw result is absent from ordinary logs/money records.
- Prove no second transcript database, feature-owned money ledger, generic arbitrary-task scheduler/service locator or durable job framework was added.
- Prove prerequisite detector retains one signature authority and metadata observers remain content-free/non-mutating.
- Prove opaque/encrypted compaction bytes and existing no-retry-after-output/request authority semantics remain unchanged.

_Requirements: 1.1–1.4, 7.6, 9.3–9.13, 11.9, 11.12–11.15_

_Validation: `go test ./internal/archtest/...` plus security/contract suites green._

### Task 6.5 — Run final repository gates and simplification review

- Run focused packages, `make quality-checks`, `make test-unit`, required race/goleak suites and deterministic config/docs checks without external model credentials.
- Review implementation diff for redundant abstractions, duplicate rule/state ownership, unbounded work, hidden provider/session/billing bypasses and unnecessary public API expansion.
- Confirm normal requests with feature disabled/no candidate state incur only negligible bounded checks and no auxiliary model calls.
- Record implementation evidence separating UX/cost trade-off: preserved continuity and extra billed auxiliary usage/latency only at bounded barriers.
- If the feature requires a general workflow engine, second money path, or unsafe opaque mutation to pass tests, re-scope rather than weaken the frozen requirements.

_Requirements: 10.3–10.12, 11.1–11.15_

_Validation: full repository release-quality gates green; final architecture remains within the spec boundaries._
