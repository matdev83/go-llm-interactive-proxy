# Requirements Document

## Introduction

Issue #369 asks Go-LIP to reduce the future context and cost of replaying very large preserved textual reasoning without breaking reasoning continuity. The brownfield repository already contains three distinct authorities that must remain separate:

1. `reasoning-output-preservation` owns surfaced-winner capture, bounded session-local reasoning artifacts, exact matching, and later restoration.
2. provider/native continuity paths such as direct Codex own exact encrypted/opaque reasoning and native compaction/checkpoints.
3. the generic auxiliary/background execution, generation pinning, routing, billing, and detached-child machinery introduced for `compaction-continuity` provides reusable infrastructure for auxiliary inference.

This SDD is a **follow-up**, not a replacement for those completed specifications. It adds only the missing capability: an optional validated semantic replay surrogate for artifact classes whose capability/profile explicitly permits lossy textual preservation.

The core safety invariant is:

> The original preserved reasoning artifact is always authoritative. Semantic compression may attach an optional replay surrogate; it must never destructively replace, mutate, or become the only copy of the original artifact.

## Relationship to Existing Specifications

This specification does **not** supersede:

- `.kiro/specs/archive/reasoning-output-preservation/`;
- `.kiro/specs/archive/reasoning-preservation-e2e-validation/`;
- `.kiro/specs/archive/openai-responses-reasoning-preservation/`;
- `.kiro/specs/archive/openai-codex-native-compaction/`;
- `.kiro/specs/archive/compaction-continuity-preservation/`.

Those specifications remain historical authorities for the behavior they implemented. This specification depends on their landed contracts where stated and must not duplicate their feature-specific logic.

`compaction-event-detection` is **not** a dependency of this feature. The reusable dependency is the generic auxiliary/background infrastructure that now exists in the repository; semantic reasoning compression must not couple itself to compaction detection or to the continuity-capsule schema.

## Boundary Context

### In scope

- explicit artifact/replay semantics distinguishing exact/native continuity from semantically compressible textual reasoning;
- optional nested compression policy within reasoning preservation;
- original-first storage plus bounded optional surrogate/pending-job state;
- detached, no-tools auxiliary compression using the generic auxiliary routing/execution path;
- originating-principal billing/admission/accounting for compressor inference;
- non-blocking background result adoption and stale-result protection;
- shadow mode and active semantic replay mode;
- target-side capability/profile validation before surrogate replay;
- content-free observability and savings measurement;
- deterministic/local compressor implementations only behind the same contract, where their semantic guarantees are explicitly truthful;
- regression and release evidence covering exact/native continuity, parallel/failover lifecycle, billing, privacy, concurrency, reload, and performance.

### Out of scope

- compressing encrypted/opaque provider reasoning;
- rewriting signed reasoning/thinking blocks or signatures;
- modifying native provider compaction/checkpoints;
- replacing Codex Responses Compaction V2 or other provider-native compaction;
- changing reasoning/action/tool ordering;
- summarizing ordinary visible reasoning that is not persisted by reasoning preservation;
- introducing another provider client, another routing engine, another money ledger, or another generic background task runtime;
- durable/distributed reasoning-preservation state in v1;
- claiming semantic equivalence from deterministic truncation, token dropping, or other lossy local transforms that cannot establish it.

## Requirements

### Requirement 1: Preserve Exact and Native Continuity as an Untouchable Baseline

**Objective:** As an operator, I want semantic compression to be structurally incapable of corrupting exact provider continuity.

#### Acceptance Criteria

1. When a preserved reasoning artifact is encrypted, opaque, provider-signed, signature-bearing where mutation invalidates replay, an exact OpenAI Responses reasoning item, native compaction/checkpoint material, or otherwise marked exact-replay-required, the system shall classify it as non-compressible and shall not submit its payload to a semantic compressor.
2. When an artifact contains both textual fields and exact/native authority, exact/native semantics shall win; the presence of readable text shall not make the artifact compressible.
3. When Anthropic signed thinking or redacted/opaque thinking is preserved, semantic compression shall not alter its text/signature/opaque data as a workaround for size reduction.
4. When direct Codex native context is active, this feature shall not rewrite encrypted reasoning items, continuity markers, native checkpoints, or `/responses/compact` replacement material.
5. When semantic compression is disabled, absent, unsupported, failed, stale, or rejected, the existing exact reasoning-preservation behavior shall remain available unchanged.
6. No core package shall infer compressibility from provider-name/model-name string matching; exactness/compressibility shall come from a typed canonical/feature capability or profile contract.
7. Architecture tests shall fail if exact/native/signed artifact classes are routed into the semantic compressor or if a provider-specific compressor branch is introduced into generic core orchestration.

### Requirement 2: Define Explicit Replay Semantics Before Compression

**Objective:** As a maintainer, I want one explicit semantic classification so that capture, compression, storage, and replay cannot disagree about safety.

#### Acceptance Criteria

1. The implementation shall define a bounded typed replay/compression semantic classification equivalent to at least: exact replay required, semantic textual replay permitted, and not persisted/not applicable.
2. The classification shall be derived from canonical reasoning dialect/artifact properties plus negotiated capability/profile information, not from ad hoc runtime string checks.
3. In the first production scope, plain textual historical reasoning such as `openai.chat.reasoning_text.v1` may be classified semantically compressible only when the effective profile explicitly permits semantic replay.
4. Unknown dialects, unknown profile values, contradictory capability evidence, and mixed artifacts without a provably safe textual subset shall fail closed to original/exact replay rather than becoming compressible.
5. The same semantic classification authority shall be used by both compressor submission and later surrogate replay eligibility.
6. Provider adapters may expose provider-specific facts needed to build the profile, but provider SDK types and provider-specific policy shall not leak into generic core.

### Requirement 3: Compression Must Be Explicitly Configured and Inert by Default

**Objective:** As an operator, I want a safe opt-in rollout with zero behavior/cost change unless I deliberately enable it.

#### Acceptance Criteria

1. Reasoning preservation shall accept an additive nested `compression` configuration owned by the `reasoning-output-preservation` feature rather than requiring an independent feature with duplicate state ownership.
2. When `compression.enabled` is omitted or false, no compressor request, pending compression state, surrogate allocation, compression-specific telemetry, or additional billable inference shall occur.
3. Enabled configuration shall support at least an explicit compressor route or explicit inheritance policy, timeout, maximum input tokens/bytes, maximum output tokens/bytes, minimum source size, maximum surrogate size, and minimum required savings/effectiveness policy.
4. Enabled configuration shall support `shadow` and `active` modes; shadow mode shall never substitute a surrogate into a backend request.
5. Unknown compression configuration fields, contradictory route/inheritance selection, non-positive bounds, unsafe maxima, or active mode without a valid compressor execution path shall fail configuration/generation validation before serving affected traffic.
6. Compression shall remain off in standard injected/default reasoning-preservation configuration unless an operator explicitly enables it.
7. An explicitly disabled reasoning-preservation feature shall continue to suppress all of its storage, compression, and replay behavior.

### Requirement 4: Original Artifact Must Commit Before Any Compression Work

**Objective:** As a maintainer, I want compression to be an optimization after correctness state exists, never a prerequisite to preserving the response.

#### Acceptance Criteria

1. When a final stream ends with `success_released`, the existing observer shall first validate and append the original `TurnArtifact` using the current authoritative session partition and anchor semantics.
2. Only after the original artifact append succeeds may the feature submit semantic compression work for eligible textual reasoning.
3. If original artifact append fails, is oversized, is ineligible, or the stream outcome is failed/cancelled/closed/replaced/gate-replaced, no compressor work shall be submitted.
4. Parallel-race losers, swallowed retries/failovers, completion-gate-discarded streams, and any B-leg other than the surfaced successful release shall never generate compression work.
5. Compressor submission failure shall not retroactively fail, delete, rewrite, or invalidate the original artifact or client-visible response.
6. The observer shall not synchronously wait for remote semantic compression before completing its final-stream lifecycle.

### Requirement 5: Store Surrogates Non-Destructively and Bound Their Memory Impact

**Objective:** As an operator, I want semantic surrogates without sacrificing retained exact state or creating unbounded memory growth.

#### Acceptance Criteria

1. A retained artifact shall continue to contain the original reasoning placements and payloads as the authoritative representation.
2. Compression state shall be additive and bounded, representing at most pending compression metadata and/or one validated surrogate for the original artifact revision.
3. A surrogate shall carry enough immutable correlation to prove which artifact ID, original anchor/digest/revision, semantic profile, and compression policy produced it without exposing reasoning contents in diagnostics.
4. Surrogate attachment shall use compare-and-set/equivalent stale-write protection so a late result cannot attach to a replaced, expired, evicted, or mismatched artifact.
5. Surrogate lifetime shall never exceed the original artifact lifetime; original expiry/eviction shall make pending or completed surrogate state unusable.
6. Surrogate/pending state shall be covered by explicit byte/count bounds. Attaching optional compression state shall not evict or destroy an otherwise-retained authoritative original solely to make room for an optimization; if the compression budget is exhausted, the surrogate shall be rejected/skipped.
7. Pending-job attachment failure shall cause the submitted result to be forgotten/cancelled when safely possible, while leaving the original artifact intact.
8. Process restart behavior shall remain honest: v1 does not add durable/distributed reasoning compression state beyond the existing reasoning-preservation store contract.

### Requirement 6: Reuse the Generic Auxiliary Execution Path Without Feature Coupling

**Objective:** As a maintainer, I want compressor inference to reuse proven infrastructure rather than creating another provider or worker subsystem.

#### Acceptance Criteria

1. An LLM-based compressor shall execute through `pkg/lipsdk/auxiliary` and the normal Executor/routing/B2BUA/billing path, not through a feature-local provider SDK/client.
2. The feature shall reuse the process-owned bounded auxiliary/background scheduler capability introduced by prior work rather than spawning an unbounded goroutine per artifact or creating a second generic task runner.
3. The reasoning-compression implementation shall not import or depend on `compactioncontinuity` capsule, source, resultmerge, extractor, detector, or feature semantics; only generic infrastructure may be reused.
4. The auxiliary child shall use detached/private execution, no tools, no primary secure-session turn/transcript/resume mutation, and an independently configurable route unless explicit inheritance is configured.
5. The child request shall disable `reasoning-output-preservation` for itself so compressor output cannot recursively become another preserved/compressed reasoning artifact.
6. Compression input shall contain only the eligible textual reasoning payload plus minimal bounded compressor instructions/metadata; it shall not contain unrelated user transcript, tool outputs, files, media, opaque reasoning, signatures, session/account identifiers, or provider-native checkpoint data.
7. The compressor shall receive source text as untrusted quoted data, not as executable instructions.

### Requirement 7: Auxiliary Compression Must Follow Ordinary Billing and Admission

**Objective:** As an operator, I want additional inference to be visible, attributable, and unable to bypass economic controls.

#### Acceptance Criteria

1. Each submitted LLM compressor inference shall be attributed by default to the same authenticated principal/account that caused the originating preserved artifact.
2. Compressor execution shall participate in ordinary applicable credit/exposure admission, routing, usage metering, provider-cost accounting, retry/failover accounting, and terminal settlement.
3. Compressor work shall use a bounded workload identity equivalent to `class=auxiliary` and `role=reasoning_preservation_compressor` so operator/account reporting can distinguish it from primary inference.
4. Primary frontend protocol-visible usage shall not falsely include compressor usage as though it were primary model output, while account/operator totals shall include incurred compressor usage/cost.
5. If billing/admission rejects the compressor before provider submission, no compressor provider work shall occur and the original artifact shall remain authoritative.
6. Once provider work is submitted, its incurred usage shall remain accountable even if the compressor result is invalid, stale, uneconomical, late, or never used.
7. A future operator-funded compressor policy is out of scope unless separately specified; originating-user attribution is the default contract.

### Requirement 8: Compression Output Must Be Strictly Validated and Economically Worthwhile

**Objective:** As an operator, I want only ordinary bounded text that actually reduces replay cost to become a surrogate.

#### Acceptance Criteria

1. The compressor request shall forbid tools, side effects, and non-text result channels.
2. A result containing tool calls, opaque/provider objects, malformed encoding, empty output where non-empty is required, or output exceeding configured/hard limits shall be rejected.
3. The validator shall enforce a configured maximum surrogate size and minimum savings/effectiveness threshold before attachment.
4. A result that is not smaller enough than the eligible source according to the configured policy shall be discarded as `insufficient_savings` rather than stored/replayed.
5. Input and output token/byte bounds and timeout shall be hard upper limits independent of model cooperation.
6. Validation shall not claim or infer semantic equivalence beyond the contract of the selected compressor. Active semantic replay is an operator-approved lossy mode, not a mathematical guarantee of identical internal reasoning.
7. A deterministic/local compressor may implement the same interface only if its limitations are documented; truncation/token dropping shall not be mislabeled as semantic preservation.

### Requirement 9: Adopt Background Results Without Adding Response or Replay Latency by Default

**Objective:** As a user, I want compression to reduce future context without making primary response release or ordinary follow-up turns wait for auxiliary inference.

#### Acceptance Criteria

1. Background compressor submission shall return without waiting for compressor completion.
2. The generic background auxiliary capability shall expose, or this implementation shall add, a safe non-blocking result inspection/poll operation so callers can distinguish `pending`, `completed`, `failed`, and `not_found/expired` without using timing tricks or zero-duration race-prone waits.
3. The artifact shall record only a bounded pending job reference plus immutable validation/correlation data needed to adopt the result later.
4. On a later matching replay attempt, the feature may poll an already-submitted result once without blocking; if it is not completed, the feature shall replay the original for that attempt.
5. When a completed result is first observed, the feature shall validate it, CAS-attach a surrogate if still current, forget/release the background result, and then apply shadow/active policy.
6. V1 shall not add an auxiliary completion callback that mutates retired feature state asynchronously and shall not add a feature-owned maintenance goroutine solely to consume results.
7. Any future bounded wait-before-replay optimization requires separate evidence/configuration and is not required for v1.

### Requirement 10: Surrogate Replay Must Revalidate the Destination Candidate

**Objective:** As a maintainer, I want a surrogate created safely at capture time to be used only where the destination also permits semantic replay.

#### Acceptance Criteria

1. The existing reasoning-preservation `AttemptTransform` shall remain the owner of historical reasoning reinjection.
2. Before substituting a surrogate, the transform shall resolve the destination candidate's replay/compression profile using the same semantic classification authority as submission.
3. If the destination requires exact replay, has unknown/contradictory semantics, lacks the semantic-text replay capability, or cannot legally represent the surrogate dialect, the transform shall use the retained original or existing unrepresentable policy; it shall never force the surrogate.
4. Shadow mode shall always replay the original even when a valid surrogate is available.
5. Active mode shall substitute only eligible textual reasoning payloads while preserving the existing `BeforeNonReasoningPart` placement and all non-reasoning structure.
6. In a turn containing a mixture of exact and semantically compressible reasoning parts, only the explicitly compressible textual subset may use surrogates; exact/signed/opaque parts shall remain byte/structure equivalent to the original.
7. Tool calls, tool outputs, IDs, signatures, images/files, ordinary assistant text, and reasoning/action/observation ordering shall remain unchanged by surrogate substitution.
8. Client-supplied reasoning shall continue to win according to existing reasoning-preservation classification; compression shall not overwrite reasoning the client already preserved.

### Requirement 11: Shadow Mode Must Produce Evidence Before Active Rollout

**Objective:** As an operator, I want production evidence that compression is useful and stable before it changes backend-visible replay.

#### Acceptance Criteria

1. In shadow mode, eligible artifacts may be compressed and validated, but every backend replay shall still use the original artifact.
2. Content-free observability shall record bounded original size, surrogate size, estimated/hypothetical saved reinjected tokens/bytes, compressor latency, compressor cost/usage via ordinary billing surfaces, and categorical outcomes.
3. Outcome taxonomy shall distinguish at least: exact/ineligible, below threshold, submitted, queue saturated, admission denied, timeout, provider failure, invalid output, insufficient savings, stale/evicted, shadow-ready, active-used, and original fallback.
4. Observability shall not log reasoning text, surrogate text, signatures, opaque JSON, tool content, raw anchors/digests, authoritative session IDs, branch keys, prompts, or child credentials.
5. Active mode shall be documented as a second rollout stage and shall remain explicitly configured; completing implementation shall not silently flip existing deployments from exact replay to semantic replay.
6. Release evidence shall include a measured shadow dataset or deterministic evaluation harness showing compression ratio/savings and failure behavior without claiming quality improvement unless semantic/agent-task quality is separately measured.

### Requirement 12: Preserve Existing Lifecycle, Security, and Architecture Invariants

**Objective:** As a maintainer, I want this optimization to fit existing ownership rather than create another cross-cutting subsystem.

#### Acceptance Criteria

1. `reasoning-output-preservation` shall remain the sole owner of its artifact store, matching, capture, and historical reinjection; no second reasoning transcript/store shall be introduced.
2. The generic auxiliary scheduler shall remain process-owned infrastructure; the reasoning feature shall receive a narrow generation-bound auxiliary capability/port only when compression is enabled.
3. Compression-disabled composition shall not require BackgroundAux or widen the generic final-stream observer `response.Services` bag merely to preserve old behavior.
4. If a new generic SDK operation such as non-blocking background result polling is required, it shall be additive, capability-neutral, bounded, independently tested, and usable without importing feature-specific types.
5. Provider SDKs shall remain edge-only, canonical streaming shall remain authoritative, and non-streaming shall remain collection over canonical streams.
6. Compression failures shall never create new primary retry/failover authority and shall never violate the no-retry-after-output invariant.
7. Generation reload/shutdown shall not leak goroutines/jobs/pins or allow a late compressor result to mutate unrelated/new-generation artifact state.
8. The implementation shall preserve session isolation and derive attribution/partition authority only from trusted runtime scope, not from client-supplied session hints or compressor child identity.
9. Architecture tests shall prevent direct dependencies from reasoning compression onto `compactioncontinuity` feature packages, provider-specific compression clients, or an additional billing ledger.

### Requirement 13: Verification and Release Gates

**Objective:** As a maintainer, I want executable evidence that semantic compression cannot regress existing exact continuity or runtime behavior.

#### Acceptance Criteria

1. TDD shall freeze exact/native/signed non-compressibility and compression-disabled non-interference before production behavior is added.
2. Unit tests shall cover semantic classification, config validation, compressor request construction, output validation, store CAS/pending/surrogate lifecycle, stale/evicted results, savings thresholds, and shadow/active replay selection.
3. Full-stack reasoning-preservation tests shall prove surfaced-winner-only submission across sequential, failover, weighted, and parallel routing, including cancellation/replacement/gate outcomes.
4. Regression tests shall prove OpenAI Responses exact artifacts, Anthropic signed/redacted thinking, and direct Codex native reasoning/compaction remain unchanged and never appear in compressor input.
5. Billing tests shall prove originating-principal attribution, child workload classification, pre-submit admission rejection, and incurred-usage settlement for invalid/late results.
6. Concurrency/reload tests shall cover artifact eviction versus result completion, duplicate/coalesced submission, concurrent follow-up attempts, generation retirement, scheduler shutdown, and exactly-once result adoption.
7. Parser/result validation shall receive fuzz coverage where untrusted compressor output is decoded or normalized.
8. Race/goleak/checkptr coverage shall be run for changed concurrency/lifecycle packages on supported platforms; documented platform limitations shall not be misreported as passing.
9. Performance evidence shall prove compression disabled has negligible/no material request-path regression and that shadow/active result polling is non-blocking by contract.
10. Repository gates shall include relevant focused tests, `make quality-checks`, `make test-unit`, reasoning E2E/parity suites, architecture gates, `go mod verify`, build/smoke checks, and any coverage-sensitive workflow commands for changed packages.

## Implementation-Order Invariants

The implementation tasks generated from this specification must preserve the following dependency order:

1. freeze exact/native and disabled-mode RED contracts;
2. define semantic classification and configuration;
3. add generic non-blocking auxiliary-result inspection if required;
4. extend artifact/store state non-destructively with pending/surrogate CAS semantics;
5. build and validate the feature-specific compressor over generic auxiliary infrastructure;
6. submit compression only after authoritative original artifact commit;
7. operate in shadow mode and consume completed results without blocking;
8. only then permit destination-revalidated active surrogate replay;
9. certify exact/native regressions, billing, privacy, concurrency, reload, and measured rollout evidence.

No task may reorder these dependencies by making compressor availability a prerequisite for original preservation or by enabling active replay before shadow-safe storage/result adoption exists.
