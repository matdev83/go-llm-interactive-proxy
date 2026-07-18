# Phase 1 RED Manifest

Phase 1 delivers contracts, fixtures, and intentionally failing acceptance tests only.
Production runners, merge/snapshot wiring, feature restore behavior, and adapter mappings begin in Phase 2–4.

**CI / PR policy:** treat a Phase 1 PR as a **draft / stack base**. It is **not mergeable** while these intentional RED tests fail. Do not add build tags or skips to silence them. Later phase PRs turn the applicable suites green.

## Fulfilled by Phase 2.1 (now green)

| Package | Tests / pattern | Fulfilled by |
|---|---|---|
| `pkg/lipapi` | `TestReasoningPart_*`, `TestReasoningFixtures_validate`, `TestCloneCall_deepCopiesReasoning*`, `TestRequiredCapabilities_hardReasoningReplay` | Phase 2.1 canonical validation/clone/`reasoning_replay` |
| `internal/core/metering/checkpoint` | reasoning opaque/fingerprint tests | Phase 2.1 CloneCall + billable fingerprint participation |
| `internal/core/modelcatalog` | `TestDefaultSizeEstimator_countsReasoningParts` | Phase 2.1 sizing |
| `internal/infra/tokenizers/tiktoken` | `TestCountCall_countsReasoningParts` | Phase 2.1 token counting |

## Fulfilled by Phase 2.2 (now green)

| Package | Tests / pattern | Fulfilled by |
|---|---|---|
| `internal/featurebundle` | `TestMergedFeatureSurface_carriesAttemptTransformsAndStreamObservers`, `TestSnapshotOptions_carriesAttemptTransformsAndStreamObservers`, `TestRequestRuntimeSnapshot_exposesAttemptTransformsAndStreamObservers` | Phase 2.2 merge/snapshot port wiring (typed; former `*_RED` reflection contracts) |

## Fulfilled by Phase 2.3 (now green)

| Package | Tests / pattern | Fulfilled by |
|---|---|---|
| `internal/archtest` | `TestOpenPlannedCandidate_attemptTransformBetweenShapeAndCapabilities`, `TestOpenPlannedCandidate_excludeCandidateDecisionHandledBeforeOpen` | Phase 2.3 `RunCandidateAttemptTransformStage` wired in `openPlannedCandidate` |
| `internal/core/runtime` | `TestCandidateAttemptTransform_*`, `TestCandidateAttemptTransform_allExcluded_*`, `TestPostHookRederive_*`, `TestAttemptMeta_*`, `TestWeightedFirst_postHookExcludeDoesNotConsumeStoreFlag` | Phase 2.3 candidate attempt-transform runner + exclude/failover + post-hook rederive + stable all-excluded aggregates |
| `internal/core/extensions` | `TestRunCandidateAttemptTransformStage_*` | Phase 2.3 stage runner |

## Fulfilled by Phase 2.4 (now green)

| Package | Tests / pattern | Fulfilled by |
|---|---|---|
| `internal/archtest` | `*final_stream_observation_order*` | Phase 2.4 `RunFinalStreamObservationStage` wired in recv handlers |
| `internal/core/runtime` | `TestFinalStreamObserver_*` | Phase 2.4 final-stream observer session + runtime lifecycle |
| `internal/core/extensions` | `TestRunFinalStreamObservation*` | Phase 2.4 observer stage runner |

## Fulfilled by Phase 2.5 (now green)

| Package | Tests / pattern | Fulfilled by |
|---|---|---|
| `internal/core/extensions` | `TestCanonicalStageMetricLabels_*`, `TestRecordStageObservation_*`, `TestSafeEventObserveBytes_*`, `TestSafeCallReasoningObserveBytes_*`, `TestRunCandidateAttemptTransformStage_absentParticipantsNoStageObservation` | Phase 2.5 bounded stage label collapse + count/byte recording + absent-port no-op |
| `internal/infra/metrics` | `TestExtensionStageSink_*` | Phase 2.5 Prometheus sink allowlist + runs/bytes counters |
| `internal/core/diag` | `TestBuildInventoryExtensions_genericPort*`, `TestBuildInventoryExtensions_absentPortsZeroPosture` | Phase 2.5 generic-port aggregate posture + privacy projection |
| `cmd/lipstd` | `TestRunCommand_inventory_dogfoodLocalStub_matchesGoldenJSON` | Phase 2.5 inventory golden includes `generic_ports` |

## Fulfilled by Phase 3 (now green)

| Package | Tests / pattern | Fulfilled by |
|---|---|---|
| `internal/plugins/features/reasoningpreservation` | Decode/catalog/anchor/classify/restore/store/privacy/observer/bundle tests (not `*_contractLock`) | Phase 3.1–3.5 feature plugin implementation |
| `internal/core/runtime` | `TestReasoningPreservationComposition_*` restore/failover/parallel/weighted/unrepresentable paths | Phase 3.5 restore + composition harness anchors |

## Intentional RED (must fail for the semantic gap named below)

| Package | Tests / pattern | Why RED |
|---|---|---|
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
| `pkg/lipapi` | `TestNegotiate_reasoningReplayNotSoftDowngradable_characterization`, `TestApplyNegotiatedDowngrades_doesNotStripHistoricalReasoning_characterization` | Existing negotiate/downgrade invariants; hard replay derivation is `TestRequiredCapabilities_hardReasoningReplay` (green since Phase 2.1) |
| `pkg/lipapi` | `TestReasoningPart_byteAndCountLimits/per_message_part_count_via_reasoning_alias` | `MaxReasoningPartsPerMessage` aliases `MaxPartsPerMessage`; generic envelope bound is the approved acceptance |
| `internal/plugins/frontends/openailegacy` | `TestEncode_reasoningStream_characterization` | Stream already emits `reasoning_content` |
| `internal/core/runtime` | `TestReasoningPreservationComposition_disabledFeatureNonInterference_characterization`, `…_noRetryAfterFirstOutput_characterization`, `TestReasoningPreservationTransform_unrepresentableRejectExcludesCandidate` | Baseline non-interference / no-retry-after-output; Phase 3 unrepresentable exclude characterization |
| `internal/plugins/features/reasoningpreservation` | `TestSafeOutcome_wireValuesExact_contractLock`, `TestBuiltinCatalogVersion_nonEmpty_contractLock`, `TestSessionPartition_StringEmptyForPrivacy_contractLock` | Wire/privacy contract locks |
| `internal/plugins/backends/openaifamily` | `TestResolveFlavor_upstreamResponsesExtension` | Existing flavor resolver characterization |

## Stage IDs (frozen)

- `candidate_attempt_transform`
- `final_stream_observation`
