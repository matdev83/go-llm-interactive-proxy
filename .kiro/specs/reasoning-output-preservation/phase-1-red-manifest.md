# Phase 1 RED Manifest

Phase 1 delivers contracts, fixtures, and intentionally failing acceptance tests only.
Production runners, merge/snapshot wiring, feature restore behavior, and adapter mappings begin in Phase 2–4.

**CI / PR policy:** treat a Phase 1 PR as a **draft / stack base**. It is **not mergeable** while these intentional RED tests fail. Do not add build tags or skips to silence them. Later phase PRs turn the applicable suites green.

## Intentional RED (must fail for the semantic gap named below)

| Package | Tests / pattern | Why RED |
|---|---|---|
| `pkg/lipapi` | `TestReasoningPart_*`, `TestReasoningFixtures_validate`, `TestCloneCall_deepCopiesReasoning*`, `TestRequiredCapabilities_hardReasoningReplay` | Validate/clone/`reasoning_replay` derivation not implemented (Phase 2.1) |
| `internal/core/metering/checkpoint` | reasoning opaque/fingerprint tests | Checkpoint participation incomplete until 2.1 |
| `internal/core/modelcatalog` | `TestDefaultSizeEstimator_countsReasoningParts` | Sizing incomplete until 2.1 |
| `internal/infra/tokenizers/tiktoken` | `TestCountCall_countsReasoningParts` | Token counting incomplete until 2.1 |
| `internal/archtest` | `*attempt_transform_open_order*`, `*final_stream_observation_order*` | Runtime stage runners absent (Phase 2.3/2.4) |
| `internal/core/runtime` | `TestCandidateAttemptTransform_*`, `TestFinalStreamObserver_*` | Merge/snapshot/runner wiring absent (Phase 2.2–2.4) |
| `internal/featurebundle` | `TestMergedFeatureSurface_carriesAttemptTransformsAndStreamObservers_RED`, `TestSnapshotOptions_carriesAttemptTransformsAndStreamObservers_RED`, `TestRequestRuntimeSnapshot_exposesAttemptTransformsAndStreamObservers_RED` | Merge/snapshot ports not wired (Phase 2.2) |
| `internal/plugins/features/reasoningpreservation` | Decode/catalog/anchor/classify/restore/store/privacy projection tests (not `*_contractLock`) | Feature behavior stubs return `ErrNotImplemented` (Phase 3) |
| `internal/core/runtime` | `TestReasoningPreservationComposition_*` except `*_characterization` / harness anti-soft-green | Contribution dropped now (opens/calls=0). Client-preserved/conflicting also wait on Phase 2.1 `Call.Validate` accepting `PartReasoning` before Phase 3 restore semantics. After 2.2, `RestoreMissingReasoning` still `ErrNotImplemented` until Phase 3; `TestReasoningPreservationComposition_unrepresentableReplayAllExcluded_RED` must not green on generic/`ErrNotImplemented` fail-closed — requires stable `unrepresentable_replay` |
| `internal/plugins/frontends/openailegacy` | `TestDecodeChat_assistantReasoning*_RED`, `TestEncode_reasoningNonStreamOutput_RED` | Chat history/nonstream mapping (Phase 4.1) |
| `internal/plugins/backends/openailegacy` | `TestParamsForCall_assistantReasoningChatDialect_RED` | Chat backend encode (Phase 4.1) |
| `internal/plugins/frontends/openairesponses` | `TestDecodeCreate_reasoningInputItem_RED` | Responses input item decode (Phase 4.2) |
| `internal/plugins/backends/openairesponses` | `TestParamsForCall_assistantReasoningResponsesDialect_RED` | Responses backend encode (Phase 4.2) |
| `internal/plugins/frontends/anthropic` | `TestDecodeMessage_assistantThinkingAndRedactedInterleaved_RED` | Thinking decode (Phase 4.3) |
| `internal/plugins/backends/protocols/anthropicmessages` | `TestParamsForCall_assistantThinking*_RED` | Thinking encode (Phase 4.3) |
| `internal/plugins/backends/openaifamily` | flavor accept + `TestReasoningReplayProfile_kimiMoonshotExactFlavorAndModel_RED` + dialect-mismatch reject cases | Flavor-exact replay (Phase 4.4); reject cases must fail with dialect-specific errors, not generic `PartReasoning` unsupported |
| `internal/plugins/backends/protocols/geminigenerate` | `TestStreamParamsForCall_assistantReasoningReplayUnsupported_RED`, `TestStreamParamsForCall_noPositiveReasoningReplayGolden_RED` | Must classify `reasoning replay` / `reasoning_replay` (Phase 4.5); generic text-only / unknown-kind rejection must not green these |

## Intentionally GREEN characterizations / contract locks (not RED evidence)

| Package | Tests | Role |
|---|---|---|
| `pkg/lipapi` | `TestNegotiate_reasoningReplayNotSoftDowngradable_characterization`, `TestApplyNegotiatedDowngrades_doesNotStripHistoricalReasoning_characterization` | Existing negotiate/downgrade invariants; RED for historical replay is `TestRequiredCapabilities_hardReasoningReplay` |
| `pkg/lipapi` | `TestReasoningPart_byteAndCountLimits/per_message_part_count_via_reasoning_alias` | `MaxReasoningPartsPerMessage` aliases `MaxPartsPerMessage`; generic envelope bound is the approved acceptance |
| `internal/plugins/frontends/openailegacy` | `TestEncode_reasoningStream_characterization` | Stream already emits `reasoning_content` |
| `internal/core/runtime` | `TestReasoningPreservationComposition_disabledFeatureNonInterference_characterization`, `…_noRetryAfterFirstOutput_characterization`, `TestReasoningPreservationTransform_errNotImplementedDoesNotBecomeUnrepresentableExclude` | Baseline non-interference / no-retry-after-output; harness must propagate `ErrNotImplemented` (not soft-map to exclude) |
| `internal/plugins/features/reasoningpreservation` | `TestSafeOutcome_wireValuesExact_contractLock`, `TestBuiltinCatalogVersion_nonEmpty_contractLock`, `TestSessionPartition_StringEmptyForPrivacy_contractLock` | Wire/privacy contract locks |
| `internal/plugins/backends/openaifamily` | `TestResolveFlavor_upstreamResponsesExtension` | Existing flavor resolver characterization |

## Stage IDs (frozen)

- `candidate_attempt_transform`
- `final_stream_observation`
