# Implementation Plan

Implementation is TDD-first. Every production task follows RED → GREEN → refactor. No phase contains more than five tasks, and no task may enable backend-visible surrogate replay before the dependency chain reaches Phase 6.

Hard ordering invariant:

```text
exact/disabled RED contracts
-> canonical classifier + config + Poll + optional store state
-> isolated compressor domain
-> original-first shadow submission
-> non-blocking shadow adoption
-> destination-gated active replay
-> certification
```

The completed reasoning-preservation, OpenAI Responses preservation, Codex native compaction, and compaction-continuity specifications are prerequisites/constraints, not implementation targets to rewrite.

## 1. Freeze Safety and Ownership Contracts With RED Tests

- [ ] 1.1 Freeze canonical semantic-text versus exact replay classification
  - Add RED table tests for plain `openai.chat.reasoning_text.v1` text as the only initial semantic-text positive class and for OpenAI Responses exact items, Anthropic signed thinking, redacted/opaque thinking, signature/opaque-bearing mixed parts, unknown dialects, and malformed structures as non-compressible.
  - Prove readable text never overrides exact/signature/opaque authority and no provider/model string is consulted by the classifier.
  - Add architecture/egress negatives proving exact/native/signed payloads cannot reach the compressor request builder.
  - _Boundary: canonical/feature domain / tests_
  - _Depends: none_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./pkg/lipapi/...`_
  - _Requirements: 1.1-1.7, 2.1-2.7, 10.2-10.3, 13.1, 13.4_

- [ ] 1.2 Freeze compression config and disabled non-interference
  - Add RED strict-decode tests for nested `compression` config, explicit route, shadow default, active opt-in, bounds/ratio/pending/surrogate limits, unknown fields, and invalid/missing route.
  - Prove absent/disabled compression constructs no compression runtime dependency, submits no aux work, allocates no optional store state where observable, and emits no compression-specific telemetry.
  - Pin standard injected reasoning-preservation defaults to compression-disabled and preserve explicit feature opt-out behavior.
  - _Boundary: feature configuration / tests_
  - _Depends: none_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./internal/standardplugins/...`_
  - _Requirements: 3.1-3.7, 12.1-12.4, 13.1-13.2_

- [ ] 1.3 Freeze generic background non-blocking Poll contracts
  - Add RED SDK/scheduler tests for `pending`, `completed`, `failed`, and `not_found/expired` states with defensive-copy results and deterministic disabled-client behavior.
  - Prove Poll is non-blocking with a never-completing runner and has no removal side effect; `Forget` remains explicit/idempotent.
  - Prove existing `Await`, coalescing, result TTL, shutdown, and generation-pin behavior remain unchanged by the additive API.
  - _Boundary: public SDK + process-owned auxiliary runtime / tests_
  - _Depends: none_
  - _Validation: `go test ./pkg/lipsdk/auxiliary/... ./internal/core/auxreq/...`_
  - _Requirements: 9.1-9.8, 12.2-12.4, 13.2, 13.6, 13.9_

- [ ] 1.4 Freeze original-plus-optional-state TurnStore contracts
  - Add RED contract tests for pending attachment, surrogate commit, pending clear, artifact/anchor/source/policy/job CAS, concurrent idempotency, stale/not-found outcomes, and deep-copy/clear behavior.
  - Pin separate pending-count and surrogate-byte budgets so optional admission rejection never evicts an otherwise-retained authoritative original.
  - Cover original TTL/FIFO/delete clearing optional state and late results after eviction/expiry being unable to attach.
  - _Boundary: feature state / driven-adapter contract tests_
  - _Depends: 1.1, 1.2_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/...`_
  - _Requirements: 5.1-5.9, 9.3-9.6, 12.7-12.8, 13.2, 13.6_

- [ ] 1.5 Freeze compressor child, billing, privacy, and lifecycle contracts
  - Add RED tests requiring one detached/private no-tools child per committed artifact, explicit configured route, workload role `reasoning_preservation_compressor`, feature self-disable, trusted parent correlation, and originating-principal attribution.
  - Prove model-facing input contains only eligible local reasoning segments and excludes ordinary transcript/answer text, tools/results, files/media, signatures/opaque/native data, session/account IDs, and credentials.
  - Freeze original-append-before-submit and surfaced-winner-only semantics across failed/cancelled/replaced/gate-replaced/parallel-loser outcomes.
  - Record current request/terminal pipeline owners and make changes from active simplification specs an explicit implementation-time revalidation trigger.
  - _Boundary: feature/runtime/economic/security contracts / tests_
  - _Depends: 1.1-1.4_
  - _Validation: focused `go test ./internal/plugins/features/reasoningpreservation/... ./internal/core/runtime/... ./pkg/lipsdk/auxiliary/...`_
  - _Requirements: 4.1-4.6, 6.1-6.7, 7.1-7.7, 12.5-12.10, 13.3-13.6_

## 2. Implement Minimal Foundations Without Submitting Compression Work

- [ ] 2.1 Implement the conservative canonical artifact/dialect classifier
  - Implement the typed replay-semantics classifier with unknown zero value and exact-over-text precedence.
  - Keep provider/model names and provider SDK types out of the classifier and generic core.
  - Add pure helper(s) for selecting semantic-text placement indexes while preserving exact/mixed parts unchanged.
  - _Boundary: canonical/feature domain_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./pkg/lipapi/...`_
  - _Requirements: 1.1-1.7, 2.1-2.7, 10.2-10.3_

- [ ] 2.2 Implement normalized explicit-route compression configuration
  - Extend strict YAML decode/validation with the nested compression policy, conservative defaults/hard maxima, explicit route requirement, shadow default, and active opt-in.
  - Preserve byte-for-byte/equivalent old config behavior when compression is absent/disabled and keep standard injection disabled.
  - Add immutable per-generation policy snapshot/digest inputs without serializing runtime service handles.
  - _Boundary: feature configuration_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./internal/standardplugins/...`_
  - _Requirements: 3.1-3.7, 8.5-8.8, 12.3_

- [ ] 2.3 Implement generic `BackgroundClient.Poll`/equivalent
  - Add the minimal feature-neutral SDK result-state types and non-blocking method to BackgroundClient and all in-tree disabled/fake implementations.
  - Implement scheduler inspection with bounded locking, defensive result cloning, terminal-error propagation, pending/not-found distinction, and no implicit Forget.
  - Preserve existing Submit/Await/coalescing/retention/shutdown semantics and public documentation.
  - _Boundary: public SDK + process-owned auxiliary runtime_
  - _Depends: 1.3_
  - _Validation: `go test ./pkg/lipsdk/auxiliary/... ./internal/core/auxreq/...`_
  - _Requirements: 9.1-9.8, 12.4, 13.9_

- [ ] 2.4 Implement non-destructive artifact compression state and atomic store operations
  - Add optional pending/surrogate correlation to TurnArtifact (or an artifact-lifetime-coupled store-owned side structure) while leaving original `Reasoning` authoritative.
  - Implement typed CAS-like pending attach, surrogate commit, and pending clear using authoritative partition plus artifact/anchor/source/policy/job correlation.
  - Enforce independent pending/surrogate budgets and deep clone/zero behavior; original FIFO/TTL/session reasoning limits remain unchanged.
  - _Boundary: feature state / memory adapter_
  - _Depends: 1.4, 2.2_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/...`_
  - _Requirements: 5.1-5.9, 9.3-9.6, 12.7-12.8_

- [ ] 2.5 Add the runtime-aware reasoning-preservation composition seam
  - Add a narrow compression runtime/BackgroundClient binding used only when normalized compression is enabled; preserve the old config-only path for disabled construction.
  - Share one feature-owned TurnStore/telemetry plus the generation-bound background client between observer submission and AttemptTransform adoption without widening `response.Services`.
  - Fail generation validation for enabled compression without the required BackgroundAux and add architecture tests preventing direct `compactioncontinuity` feature dependencies.
  - _Boundary: composition root / feature wiring_
  - _Depends: 2.2, 2.3, 2.4_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./internal/infra/runtimebundle/... ./internal/standardplugins/... ./internal/archtest/...`_
  - _Requirements: 6.1-6.4, 12.1-12.4, 12.9-12.10_

## 3. Build the Feature-Specific Compressor in Isolation

- [ ] 3.1 Implement eligible segment/source preparation and child request construction
  - Build one artifact-level source containing only locally indexed `SemanticText` reasoning placements; skip below-threshold/no-eligible artifacts without including exact/non-reasoning content.
  - Construct one detached/private, no-tools auxiliary Call on the explicit compressor route with fixed system policy, untrusted-data delimiters, hard input/token bounds, parent correlation metadata, and reasoning feature self-disable.
  - Derive versioned content-free source/policy/coalescing digests from committed original/policy identity.
  - _Boundary: feature compressor domain_
  - _Depends: 2.1, 2.2, 2.5_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/compressor/... ./internal/plugins/features/reasoningpreservation/...`_
  - _Requirements: 1.1-1.7, 6.1-6.7, 8.1-8.3, 12.8_

- [ ] 3.2 Implement strict multi-segment result parsing and savings validation
  - Decode exactly one versioned JSON object with exactly the expected local indexes and text-only values; reject unknown/missing/duplicate indexes/fields, surrounding prose, tool calls, invalid encoding/control data, and hard-size violations.
  - Enforce per/aggregate surrogate bounds, strictly-smaller result, minimum saved bytes, and minimum savings ratio before producing a validated surrogate.
  - Return typed/content-free rejection categories suitable for telemetry without embedding source/result text in errors.
  - _Boundary: feature compressor domain / untrusted result parser_
  - _Depends: 3.1_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/compressor/...`_
  - _Requirements: 8.1-8.9, 11.2-11.4, 13.2, 13.7_

- [ ] 3.3 Add compressor parser/request fuzz and adversarial security coverage
  - Fuzz result decoding/index validation/bounds and source preparation with malformed UTF-8/JSON, huge fields, duplicate indexes, hostile source instructions, and nested/unknown structures.
  - Add explicit negative assertions that signed/opaque/native/tool/file/transcript material never appears in the model-facing compressor payload.
  - Prove errors/diagnostics do not contain reasoning/surrogate/prompt/opaque/session content.
  - _Boundary: security / fuzz tests_
  - _Depends: 3.1, 3.2_
  - _Validation: targeted `go test -fuzz` on new parser/source fuzz targets plus ordinary package tests_
  - _Requirements: 6.6-6.7, 8.1-8.9, 11.3-11.4, 12.8-12.9, 13.7_

- [ ] 3.4 Implement compression-specific content-free telemetry taxonomy
  - Add fixed outcomes/counts/size/savings/latency/mode/profile fields under reasoning-preservation ownership without duplicating scheduler/billing economic truth.
  - Cover ineligible/below-threshold/submitted/saturated/denied/timeout/provider-failure/invalid/insufficient/stale/budget/shadow-ready/active-used/original-fallback categories.
  - Add privacy tests preventing raw artifacts, digests, session partitions, child prompts/results, credentials, and high-cardinality IDs from safe inventory/diagnostics.
  - _Boundary: feature observability_
  - _Depends: 2.2, 3.2_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/...`_
  - _Requirements: 11.1-11.6, 12.8, 13.4_

## 4. Integrate Original-First Shadow Submission

- [ ] 4.1 Submit compression only after the surfaced original artifact is retained
  - Extend the existing `OutcomeSuccessReleased` observer Finish path so `TurnStore.Append(original)` remains first and compressor source/build/Submit occurs only after a retained eligible original exists.
  - Keep all compression errors fail-open/local; do not route them into authoritative `on_state_error` or client-visible response failure.
  - Prove failed/cancelled/closed/replaced/gate-replaced outcomes and unretained/oversize artifacts submit zero jobs.
  - _Boundary: final-stream feature/runtime integration_
  - _Depends: 2.5, 3.1-3.4_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./internal/core/runtime/...`_
  - _Requirements: 4.1-4.6, 6.1-6.7, 9.1, 12.6_

- [ ] 4.2 Attach pending job state safely and coalesce duplicate committed submissions
  - Submit with a versioned artifact/source/policy coalescing key, then CAS attach the returned JobID to the authoritative artifact.
  - On stale/budget/attachment failure, best-effort Forget retained job result state without changing the original; preserve billing for already-incurred work.
  - Cover duplicate submission/coalescing and pending-count saturation without double provider work or original eviction.
  - _Boundary: feature state + auxiliary integration_
  - _Depends: 4.1, 2.4_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./internal/core/auxreq/...`_
  - _Requirements: 5.3-5.8, 7.5-7.6, 9.2-9.3, 13.6_

- [ ] 4.3 Prove surfaced-winner-only behavior across routing lifecycle scenarios
  - Extend existing reasoning-preservation runtime/E2E fixtures for sequential failover, weighted routing, parallel races, completion-gate replacement, cancellation, and client close.
  - Assert exactly one compressor child at most for the final retained winner and zero children for swallowed/loser/discarded attempts.
  - Preserve existing no-retry-after-output and original reasoning restoration behavior in all scenarios.
  - _Boundary: full-stack runtime tests_
  - _Depends: 4.1, 4.2_
  - _Validation: focused `go test ./internal/core/runtime/... ./internal/stdhttp/...` reasoning-preservation suites_
  - _Requirements: 4.1-4.6, 12.5-12.6, 13.3_

- [ ] 4.4 Certify detached child billing/admission and principal attribution on submission
  - Prove compressor child uses the configured route, detached/private lineage, originating trusted principal, separate auxiliary workload classification and ordinary admission/routing/B2BUA path.
  - Cover pre-submit credit/exposure denial producing no provider work and provider-submitted retry/failover usage remaining accountable even if optional pending attachment/result use later fails.
  - Assert primary frontend-visible usage excludes compressor inference while account/operator accounting includes incurred child usage.
  - _Boundary: economic/runtime integration_
  - _Depends: 4.1, 4.2_
  - _Validation: focused `go test` across reasoning feature, auxreq, runtime, billing/metering/authority packages_
  - _Requirements: 7.1-7.7, 12.8, 13.5_

## 5. Adopt Results Non-Blockingly in Shadow Mode

- [ ] 5.1 Poll and adopt terminal compressor results fail-open inside AttemptTransform
  - After authoritative store Snapshot, non-blockingly Poll relevant pending jobs; Pending immediately keeps original, Failed/NotFound clears best-effort and keeps original, Completed passes through strict validator.
  - CAS commit a validated surrogate only when artifact/source/policy/job correlation is still current; terminal results are Forgotten explicitly/idempotently.
  - Keep compression-specific Poll/validation/CAS errors outside authoritative `on_state_error=reject` and preserve existing RestoreMissingReasoning decisions.
  - _Boundary: candidate AttemptTransform / feature state_
  - _Depends: 2.3, 2.4, 3.2, 4.2_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./internal/core/runtime/...`_
  - _Requirements: 5.3-5.9, 9.2-9.7, 10.1-10.4, 12.6-12.8_

- [ ] 5.2 Prove concurrent/idempotent adoption and stale-result safety
  - Race concurrent follow-up attempts polling the same completed job and prove at most one effective store attach with all callers retaining valid original behavior.
  - Cover original eviction/expiry/delete, policy/source mismatch, pending budget changes, result expiry/Forget, and late completion so stale work never resurrects or crosses partitions.
  - Cover scheduler close and generation reload/retirement without callback/goroutine leaks or mutation of unrelated/new-generation state.
  - _Boundary: concurrency/lifecycle tests_
  - _Depends: 5.1_
  - _Validation: repeated/race-capable focused tests for reasoning preservation, auxreq and runtime lifecycle_
  - _Requirements: 5.4-5.9, 9.5-9.8, 12.7-12.8, 13.6, 13.8_

- [ ] 5.3 Prove shadow mode never changes backend-visible historical reasoning
  - With valid attached surrogates, assert `mode: shadow` always feeds original artifacts into RestoreMissingReasoning across matching/unrepresentable/client-preserved cases.
  - Measure hypothetical source/surrogate/saved sizes and shadow-ready outcomes without exposing contents.
  - Add a fake never-completing compressor proving follow-up AttemptTransform returns without waiting and replays original.
  - _Boundary: feature behavior / E2E tests_
  - _Depends: 5.1, 5.2, 3.4_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/... ./internal/stdhttp/...`_
  - _Requirements: 9.4-9.8, 11.1-11.6, 13.9_

- [ ] 5.4 Certify generation-bound job execution and process shutdown semantics
  - Prove a job submitted under generation N retains N's captured runner/route/config semantics across reload to N+1 using existing async generation pinning.
  - Prove shutdown/retirement releases scheduler jobs/pins according to existing contracts and no new feature goroutine/callback survives.
  - Revalidate runtime/terminal ownership against current `main` after active simplification specs; adapt names/placement without changing semantic ordering.
  - _Boundary: runtime generation/lifecycle_
  - _Depends: 4.1, 5.1, 5.2_
  - _Validation: `go test ./internal/infra/runtimebundle/... ./internal/core/auxreq/... ./internal/core/runtime/...` plus supported race/goleak lanes_
  - _Requirements: 12.2, 12.7, 12.10, 13.6, 13.8_

## 6. Enable Destination-Gated Active Semantic Replay

- [ ] 6.1 Implement defensive effective-artifact surrogate projection
  - Clone stored artifacts and, only in active mode, replace `Reasoning.Text` for validated correlated semantic-text placement indexes while preserving dialect and `BeforeNonReasoningPart`.
  - Leave exact/signed/opaque/non-reasoning/tool/file/image structure untouched and never mutate the stored original.
  - Feed projected clones through existing RestoreMissingReasoning rather than adding a second matching/reinjection engine.
  - _Boundary: feature domain / AttemptTransform_
  - _Depends: 5.1-5.3_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/...`_
  - _Requirements: 5.1-5.5, 10.1, 10.4-10.8, 12.1_

- [ ] 6.2 Revalidate canonical semantics and existing destination ReplaySupport before substitution
  - Require each substituted original part to still classify `SemanticText` and require the current candidate's existing `ReasoningReplaySupport` to represent its original dialect.
  - On exact/unknown/unrepresentable/stale conditions use the retained original or existing unrepresentable policy; a stored surrogate is never permission by itself.
  - Cover mixed artifacts so only semantic-text segments substitute while exact/signed/opaque segments remain structurally/byte equivalent to originals.
  - _Boundary: candidate capability/profile policy_
  - _Depends: 6.1, 2.1_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/...`_
  - _Requirements: 1.1-1.7, 2.1-2.7, 10.2-10.8_

- [ ] 6.3 Add hard exact/native regression proof
  - Run/add targeted regression cases for OpenAI Responses exact reasoning items, Anthropic signed/redacted thinking, and direct Codex encrypted continuity/native compaction companion behavior with compression enabled in shadow and active modes.
  - Assert exact/signed/opaque/native payloads never appear in compressor input and backend-visible exact replay remains unchanged.
  - Prove readable text inside an exact/native structure cannot trigger semantic compression.
  - _Boundary: protocol/native regression tests_
  - _Depends: 6.2_
  - _Validation: focused reasoning-preservation/OpenAI Responses/Anthropic/Codex connector and parity suites_
  - _Requirements: 1.1-1.7, 10.6-10.7, 13.4_

- [ ] 6.4 Extend full-stack E2E with capability fixtures instead of provider matrices
  - Add semantic-text positive fixtures and exact/unknown negative fixtures through existing standard HTTP/runtime topology, covering stream/nonstream where relevant.
  - Exercise active restoration with sequential/failover/weighted/parallel routing and client-preserved reasoning precedence without adding provider×provider Cartesian cells.
  - Assert tool/order/IDs/ordinary assistant content remain unchanged while only eligible historical reasoning text shrinks.
  - _Boundary: full-stack conformance/E2E_
  - _Depends: 6.1-6.3_
  - _Validation: existing reasoning HTTP E2E/precommit matrix plus focused new semantic-compression cases_
  - _Requirements: 10.1-10.8, 12.5, 13.3-13.4, 13.11_

- [ ] 6.5 Document explicit active rollout and semantic-quality limitations
  - Update operator docs/config reference with disabled→shadow→active rollout, explicit route/cost/privacy implications, exact/native exclusions, failure fallback, process-local durability, and rollback.
  - State clearly that active semantic compression is lossy and does not prove identical hidden reasoning/agent quality; describe what shadow metrics do and do not demonstrate.
  - Add deterministic config/check-config/inventory examples without enabling billable remote compression in default dogfood.
  - _Boundary: operator documentation / examples_
  - _Depends: 6.4_
  - _Validation: `make docs-check`; `make example-config-check`; relevant `lipstd check-config/routes/inventory` examples_
  - _Requirements: 3.3-3.7, 7.3-7.7, 8.8-8.9, 11.1-11.6_

## 7. Certify Release Readiness and Close the Spec

- [ ] 7.1 Complete economic/accounting certification
  - Run deterministic billing/admission cases for success, pre-submit denial, provider failure/failover, invalid result, insufficient savings, stale/unused result, and optional-budget rejection.
  - Prove primary protocol usage stays separate while account/operator totals and provider-cost evidence include all incurred compressor attempts exactly once.
  - Record content-free workload/lineage evidence and no feature-owned money/rating logic.
  - _Boundary: economics / release evidence_
  - _Depends: 4.4, 5.1, 6.4_
  - _Validation: focused billing/metering/authority/store tests plus integration lanes used by current main_
  - _Requirements: 7.1-7.7, 13.5_

- [ ] 7.2 Complete privacy/security/fuzz certification
  - Run compressor parser/source fuzz campaigns and privacy/adversarial tests including prompt-injection source text, malformed outputs, huge payloads, exact/opaque leakage canaries, and diagnostic redaction.
  - Run architecture gates preventing direct provider compressor clients, `compactioncontinuity` feature dependencies, second ledgers/stores, and provider-name semantic branches in core.
  - Verify child scope/session isolation and that untrusted client session hints cannot select another partition/job.
  - _Boundary: security / architecture / fuzz_
  - _Depends: 3.3, 5.2, 6.3_
  - _Validation: targeted fuzz runs; `go test ./internal/archtest/...`; focused privacy/security suites_
  - _Requirements: 1.6-1.7, 6.3-6.7, 11.4, 12.8-12.9, 13.4, 13.7_

- [ ] 7.3 Complete concurrency, race, goleak, reload, and soak evidence
  - Stress concurrent submit/coalesce/Poll/CAS/Forget, artifact eviction, multiple sessions, generation reload and scheduler shutdown with deterministic repeated tests.
  - Run supported `-race`, goleak/checkptr and reasoning-preservation soak/precommit lanes; document unsupported-platform limitations truthfully.
  - Reconcile any active pipeline-refactor movement and prove semantic owner/order invariants still hold on final `main`-based implementation.
  - _Boundary: concurrency/lifecycle / release evidence_
  - _Depends: 5.2, 5.4, 6.4_
  - _Validation: repository-supported race/goleak/checkptr/reasoning E2E soak commands_
  - _Requirements: 12.6-12.10, 13.6, 13.8_

- [ ] 7.4 Produce performance and shadow usefulness evidence
  - Benchmark compression-disabled path, below-threshold observer path, pending Poll path, shadow-ready path, and active surrogate projection against an appropriate baseline.
  - Produce a deterministic/shadow evaluation showing source/surrogate compression ratios, hypothetical reinjection savings, latency/failure categories and auxiliary cost without recording reasoning contents.
  - Do not claim semantic/agent-task quality improvement unless a separate quality evaluation is explicitly measured; document break-even/cost trade-offs honestly.
  - _Boundary: performance / product evidence_
  - _Depends: 5.3, 6.4, 7.1_
  - _Validation: targeted benchmarks/evaluation harness plus `make quality-checks` performance-sensitive gates_
  - _Requirements: 8.5-8.9, 11.1-11.6, 13.9_

- [ ] 7.5 Run final repository gates and reconcile Kiro completion evidence
  - Run focused changed-package tests, `make quality-checks`, `make test-unit`, `make test`, relevant parity/reasoning E2E gates, `go mod verify`, lint/build/smoke and release-manifest/coverage-sensitive checks required by current repository policy.
  - Record exact-head implementation evidence, changed architecture boundaries, defaults/rollback and known limitations in an implementation ledger/release review without overstating unavailable platform/live-provider evidence.
  - Mark requirements/design/tasks complete only after all mandatory evidence passes or explicitly approved waivers are recorded; archive the spec using the repository's completed-spec convention.
  - _Boundary: repository release / Kiro closeout_
  - _Depends: 7.1-7.4_
  - _Validation: current repository release/quality commands plus Kiro spec checker_
  - _Requirements: 13.1-13.11_
