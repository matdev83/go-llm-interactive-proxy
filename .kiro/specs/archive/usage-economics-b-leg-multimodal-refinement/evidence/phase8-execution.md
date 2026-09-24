# Phase 8 Producer Execution Evidence

Status: `READY_FOR_REVIEW`

Base revision: `5c306fe825c40ea14c7f6ac21958639a998bff67`

Scope: parent task 8 (8.1-8.5), real upstream economic evidence producers and
the closed Phase 1 producer/consumer census. Rating, revision workers,
settlement, reports, public host binding, and cutover remain out of scope.

## Census completeness

The Phase 1 census remains the closed inventory. A base-revision versus
working-tree count check reported:

- 69 source-anchor rows before and after.
- 33 Phase 8-relevant rows before and after (`provider-producer`,
  `prompt-cache`, `compaction`, and `sideband-finalizer`).
- 16 provider-producer rows before and after.
- Every relevant row now has `v2-certified`, `lossless-v1-bridge`, or
  `unsupported advanced evidence` with a bounded reason and parent task.
  No relevant row remains `pending` or `RED`.

Completeness checks:

```text
go test -count=1 ./internal/qa -run '^TestPhase8ProducerCensusHasExplicitDisposition$'
go test -count=1 ./internal/archtest -run '^TestPhase1ProducerConsumerCensusIsExactAndDispositioned$'
```

Both passed. The architecture check also confirmed the frozen 69-row category
counts and that every source anchor still exists.

## RED fixtures and GREEN implementation

The provider fixtures were added as failing characterization vectors before the
minimal mapping changes were completed. The final GREEN commands and covered
vectors are recorded below.

### 8.1 Anthropic family

Fixtures cover absent versus present-zero counters, malformed/negative values,
overflow-safe totals, cache read/create, 5-minute and 1-hour cache creation,
output/reasoning, server-side web fetch/search, provider request identity,
streaming typed-value presence, and late source revisions:

```text
go test -count=1 ./internal/plugins/backends/protocols/anthropicmessages
go -C connectors/commandcode-anthropic test -count=1 ./...
go -C connectors/gitlabduo test -count=1 ./...
go -C connectors/minimexoauth test -count=1 ./...
```

Named fixture coverage includes `TestAnthropicEvidenceMapsCacheLifetimeAndServerTools`,
`TestAnthropicEvidenceRetainsOnlyNativeTotalWhenSurfaced`,
`TestMsgStream_ProviderUsageDeltaRetainsTypedValueWithoutSDKPresence`,
`TestCommandCodeNativeAnthropicFieldsRetainLifetimeAndServerToolUsage`,
`TestGitLabAnthropicNativeFieldsRetainLifetimeAndServerToolUsage`, and
`TestMiniMaxNativeAnthropicFieldsRetainLifetimeAndServerToolUsage`.

The root Anthropic normalizer retains only provider-surfaced totals and
allowlisted safe lexemes. Connector-local Anthropic-like parsers preserve their
native fields and use the negotiated six-counter bridge; advanced V2 identity,
native measures, and revisions remain explicitly unsupported at the executable
ABI boundary when trusted `StoreID`/B-leg binding is unavailable.

### Phase 8 review repair evidence

The review RED fixtures reproduced all three reported defects before the
repair:

```text
go test -count=1 ./internal/core/metering ./pkg/lipsdk/backendplugin ./internal/plugins/backends/protocols/geminigenerate ./internal/plugins/backends/protocols/anthropicmessages
go -C connectors/commandcode-anthropic test -count=1 -run TestCommandCodeAnthropicStreamBridgesSplitUsageAtTerminal ./...
go -C connectors/gitlabduo test -count=1 -run TestGitLabAnthropicStreamBridgesSplitUsageAtTerminal ./...
go -C connectors/minimexoauth test -count=1 -run TestMiniMaxAnthropicStreamBridgesSplitUsageAtTerminal ./...
```

The pre-fix root run failed the A/B/A, Gemini service-context, and root
Anthropic split assertions; each executable fixture observed three conflicting
V1 records for the start/output/repeated cumulative snapshots. GREEN reruns
pass with the same package selections and all three connector-local fixtures.

The in-process Anthropic producer now merges presence-aware message_start and
message_delta snapshots into one cumulative V2 source payload. Repeated
cumulative fields are an exact replay, while an interrupted stream still
retains the partial start snapshot. The commandcode, GitLab Duo, and MiniMax
Anthropic-like SSE producers apply the same family-local merge and defer their
negotiated V1 sideband record until terminal, EOF, close, or upstream error;
the bridge therefore emits one lossless input/cache/output record and cannot
surface a same-key partial conflict. Unary/slice paths use the same cumulative
merge before seeding the bridge.

Core and V1 replay tests now cover A/B/A across drains plus consecutive A
suppression. Core draft fingerprints include provider-owned provenance,
semantics, coverage, supersession, measures, charges, identifiers, and safe
evidence, while generated revisions/sequences/timestamps are excluded. An
explicit provider source revision remains its own bounded replay identity, so
distinct provider revisions with equal quantities are retained without letting
host arrival timestamps manufacture revisions. The latest fingerprint key set
is bounded, so an old implicit A is not treated as a permanent replay.

Gemini service context is emitted only by `ProviderUsageEvent`; the duplicate
append in `geminiEvidenceDraft` was removed. `TestGeminiEvidenceDraftWithTrafficTypeRetainsSingleServiceContext`
binds and drains a non-empty TrafficType and confirms one valid context field.

Repair GREEN fixture names are:
`TestProviderEvidenceBufferRetainsABACorrectionAcrossDrains`,
`TestProviderEvidenceBufferFingerprintRetainsSemanticChanges`,
`TestProviderEvidenceBufferIgnoresTimestampOnlyReplay`,
`TestProviderEvidenceBufferRetainsDistinctExplicitSourceRevisions`,
`TestUsageEvidenceBufferRetainsABACorrectionAcrossDrains`,
`TestMsgStream_AnthropicSplitUsagePreservesCumulativeV2Evidence`,
`TestMsgStream_AnthropicPartialUsageSurvivesInterruptedStream`,
`TestCommandCodeAnthropicStreamBridgesSplitUsageAtTerminal`,
`TestGitLabAnthropicStreamBridgesSplitUsageAtTerminal`, and
`TestMiniMaxAnthropicStreamBridgesSplitUsageAtTerminal`.

### Second review repair: authoritative sideband selection

The second review added runtime-boundary regressions before the repair. The
following RED checks reproduced the reported defects:

```text
go test -count=1 ./internal/core/metering -run TestProviderEvidenceBufferRejectsConflictingExplicitSourceRevision
go test -count=1 ./internal/core/runtime -run TestPrepareRecvEventUsesHostOnlyV2AsAuthoritativeEconomicPath
go test -count=1 ./internal/core/runtime -run TestPrepareRecvEventUsesConnectorSidebandAsAuthoritativeV1Path
go test -count=1 ./internal/core/runtime -run TestBillingRecordDoesNotDuplicateV1SidebandCoveredByV2
go test -count=1 ./internal/core/runtime -run TestAuthorityUsageEventDoesNotDoubleCountCumulativeProviderSnapshots
go test -count=1 ./internal/infra/backendplugins/adapter -run 'TestPhase8Adapter_(V1SidebandDoesNotClaimCanonicalAuthority|V2SidebandClaimsCanonicalAuthority)'
go -C connectors/cursorsdk test -count=1 ./internal/product -run TestRunStream_V1BridgeProjectsCanonicalUsageKey
go -C connectors/vertex test -count=1 ./internal/service -run TestVertexV1BridgeProjectsCanonicalUsageKey
go -C connectors/commandcode-anthropic test -count=1 ./internal/anthropic -run TestCommandCodeAnthropicStreamBridgesSplitUsageAtTerminal
go -C connectors/gitlabduo test -count=1 ./internal/service -run TestGitLabAnthropicStreamBridgesSplitUsageAtTerminal
go -C connectors/minimexoauth test -count=1 ./internal/service -run TestMiniMaxAnthropicStreamBridgesSplitUsageAtTerminal
```

Before the fix, the explicit-revision check accepted a changed payload at an
already accepted source revision, the runtime checks captured/swallowed the
canonical split events before sideband drain, and each connector check found
the canonical usage event still carried its durable dedupe key. The focused
GREEN checks are now:

```text
go test -count=1 ./internal/core/metering -run 'TestProviderEvidenceBuffer'
go test -count=1 ./internal/core/runtime -run 'TestPrepareRecvEventUses|TestProviderEvidence|TestPhase7|TestUsageEvidence|TestConsumeBackendUsageEvidence|TestAttemptSession|TestTerminal.*Sideband|TestCodex'
go test -count=1 ./internal/core/runtime -run 'TestBillingRecordDoesNotDuplicate(CanonicalFallback|V1Sideband)CoveredByV2'
go test -count=1 ./internal/core/runtime -run '^TestAuthorityUsageEventDoesNotDoubleCountCumulativeProviderSnapshots$'
go test -count=1 ./internal/infra/backendplugins/adapter -run 'TestPhase8Adapter_(V1SidebandDoesNotClaimCanonicalAuthority|V2SidebandClaimsCanonicalAuthority)'
go -C connectors/cursorsdk test -count=1 ./internal/product -run TestRunStream_V1BridgeProjectsCanonicalUsageKey
go -C connectors/vertex test -count=1 ./internal/service -run TestVertexV1BridgeProjectsCanonicalUsageKey
go -C connectors/commandcode-anthropic test -count=1 ./...
go -C connectors/gitlabduo test -count=1 ./...
go -C connectors/minimexoauth test -count=1 ./...
```

All listed GREEN checks passed with `GOWORK=off` for connector modules. The
three connector fixtures now also cover EOF-interrupted streams through
`TestCommandCodeAnthropicInterruptedStreamFlushesCumulativeUsage`,
`TestGitLabAnthropicInterruptedStreamFlushesCumulativeUsage`, and
`TestMiniMaxAnthropicInterruptedStreamFlushesCumulativeUsage`; each retains
one merged input/cache/output record. `TestPrepareRecvEventUsesHostOnlyV2AsAuthoritativeEconomicPath`
proves an in-process host-only V2 stream emits both canonical events to the
normal observer path while retaining no parallel V1 durable record. The V1
counterpart proves a reusable connector sideband retains exactly one complete
record with no conflict or double count. `TestBillingRecordDoesNotDuplicateCanonicalFallbackCoveredByV2`
and `TestBillingRecordDoesNotDuplicateV1SidebandCoveredByV2` prove the
terminal mapper suppresses both canonical and legacy V1 fallback when the same
provider source key is already represented by a trusted V2 B-leg observation.
`TestAuthorityUsageEventDoesNotDoubleCountCumulativeProviderSnapshots` covers
the adjacent terminal-authority projection: canonical start/cache, output-only
delta, and repeated cumulative snapshots overlay by their stable provider key,
so authority sees `11/8/3` rather than summing the same economic quantities.

Legacy negotiation coverage is explicit in
`TestCommandCodeAnthropicLegacyStreamRetainsCanonicalUsageKey`,
`TestGitLabAnthropicLegacyStreamRetainsCanonicalUsageKey`, and
`TestMiniMaxAnthropicLegacyStreamRetainsCanonicalUsageKey`: disabling the
optional bridge keeps canonical V1 keys and emits no unnegotiated sideband.
`TestManagedPrependForwardsEconomicNegotiationState` covers the same state
through the stream-peek wrapper; `TestManagedPrependForwardsUsageEvidence`
also confirms that wrapper preserves the legacy V1 drain seam.

Runtime now recognizes the neutral host-only V1/V2 source seams before
canonical usage capture; producer-specific executable adapters project the
canonical event without a durable key only while the sideband is negotiated,
while retaining the key for legacy hosts when it is disabled. The merged
sideband snapshot is the sole durable path for a negotiated V1 source that the
producer classifies as sideband-owned, so a partial message_start cannot
preempt a terminal, error, close, or EOF flush.
The executable host adapter intentionally reports only typed V2 as globally
authoritative: V1 has no source-role marker, so Cursor and Vertex project their
canonical keys away locally, while Codex retains its primary response key next
to its auxiliary compaction key. This preserves both Codex request usage and
native maintenance evidence without treating the latter as a foreground
inference. `TestPrepareRecvEventKeepsAuxiliaryV1AlongsideCanonicalPrimary`,
`TestPhase8Adapter_V1SidebandDoesNotClaimCanonicalAuthority`,
`TestRunStream_V1BridgeProjectsCanonicalUsageKey`, and
`TestVertexV1BridgeProjectsCanonicalUsageKey` cover that runtime/adapter
boundary.
Provider evidence explicit source revisions are immutable: identical retries
are suppressed and conflicting same-source-revision payloads fail closed,
while later explicit revisions and implicit A/B/A corrections remain distinct
bounded revisions. The stream-peek wrapper forwards this negotiation state so
an optional drain method cannot suppress legacy canonical capture.

### 8.2 OpenAI/OpenResponses-compatible family

Fixtures distinguish E (legacy input/output counters), Q (cached/reasoning and
native media subsets), and P (a genuine provider-reported cost only when the
provider supplied it). They cover present-zero versus absent, malformed,
negative, overflow/fractional discrete values, image/audio/video/document/file
direction and units, raw cost lexeme retention, request ID/service context,
non-streaming evidence, and incomplete streams without a final usage event:

```text
go test -count=1 ./internal/plugins/backends/openaiusage ./internal/plugins/backends/openaicompat ./internal/plugins/backends/openailegacy ./internal/plugins/backends/openairesponses ./internal/plugins/backends/openresponsescompat ./internal/plugins/protocols/openresponses
```

Named fixtures include `TestNativeUsageMeasuresPreserveMultimodalDirectionAndUnits`,
`TestNativeUsageMeasuresRejectMalformedNegativeAndFractionalDiscreteValues`,
`TestProviderEvidenceDraftRetainsGenuineProviderCostAndRawLexeme`,
`TestProviderEvidenceStreamBindsNonStreamingUsageToTrustedBLeg`, and the
OpenResponses compact explicit-zero/absent-usage tests. Native media units are
never converted to text tokens, and P is never derived from E or Q.

### 8.3 Gemini/Vertex family

Fixtures cover text/image/audio/video/document modality, input/output/cache and
grounded-tool direction, reasoning/total presence, explicit zero, malformed or
negative values, overflow-safe aggregates, versioned unit/quality metadata, and
absence of price-sheet or storage inference:

```text
go test -count=1 ./internal/plugins/backends/protocols/geminigenerate
go -C connectors/vertex test -count=1 ./...
```

Named fixtures include `TestGeminiNativeMeasuresPreserveModalityDirectionCacheReasoningAndGroundedTool`,
`TestGeminiZeroUsageMetadataRetainsPresenceWithoutInventingPrice`,
`TestGeminiCacheOnlyUsageRetainsCachePresence`,
`TestVertexUsageMapsModalityAndGroundedToolEvidence`,
`TestVertexUsagePreservesWireZeroVersusAbsent`, and
`TestVertexNativeMeasuresDropsOverflowingModalityAggregate`, and
`TestVertexV1BridgeProjectsCanonicalUsageKey`. Resource/storage
values are not labelled provider-reported unless surfaced by the response;
Vertex executable advanced V2 attachment is explicitly unsupported without
trusted host identity.

### 8.4 Codex request usage and account-window gauges

Fixtures cover request usage separately from primary/secondary/named account
windows, exact decimal gauge values, store/account/pool/window/reset identity,
malformed/negative gauge rejection, response-associated gauges not becoming a
debit, and concurrent out-of-order window snapshots:

```text
go -C connectors/codex test -count=1 ./...
go test -count=1 ./internal/core/runtime -run 'TestProvider|ProviderEvidence|EconomicEvidence|UsageEvidence|AccountingEvidence|Phase7|phase7|AttemptSession|Terminal.*Sideband|Codex'
```

Named fixtures include `TestAccountWindowSnapshots_PreservePoolsResetAndExactGaugeValues`,
`TestAccountWindowSnapshots_DropsMalformedValuesAndNeverCreatesDebit`,
`TestAccountWindowSnapshots_DropsNegativeGaugeValues`, and
`TestCodexStream_PreservesConcurrentOutOfOrderWindowSnapshots`, and
`TestNativeUsageSidebandStreamDoesNotClaimPrimaryUsageAuthority`. The executable
ABI carries request usage through the lossless six-counter bridge. Account
windows remain native gauge snapshots and are explicitly unsupported as B-leg
V2 evidence until a legitimate native subject/StoreID owner exists; no debit is
inferred from a response-associated gauge.

### 8.5 Remaining inventory and auxiliary paths

Bedrock cache-lifetime fields, localstub/reference producers, Cursor, remaining
GitLab/MiniMax/commandcode/Vertex producers, prompt-cache renewal/keep-warm,
compaction, nested-agent and auxiliary paths were dispositioned in the same
census. Auxiliary evidence retains native maintenance/resource or child B-leg
identity and declared additive/inclusive coverage; it is never relabelled as a
successful foreground inference.

```text
go test -count=1 ./internal/plugins/backends/bedrock ./internal/standardplugins/featurehost/...
go test -count=1 ./internal/qa ./internal/archtest -run 'TestPhase8|TestPhase1ProducerConsumerCensusIsExactAndDispositioned|TestKeepwarmFeatureRemainsProviderNeutral|TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata'
```

These checks passed. Rows without a real native V2 subject are explicitly
`lossless-v1-bridge` plus bounded `unsupported advanced evidence` text rather
than silently omitted.

## Host and ABI integration

- In-process families use the host-only `ProviderEvidenceBuffer` and trusted
  one-way B-leg binding. Only a consecutive exact replay is deduplicated;
  changed source payloads, including A/B/A corrections and provider semantic
  changes, become immutable revisions with supersession references.
- Executable connectors use the negotiated public six-counter bridge. Provider
  media, cost, gauge, and advanced revision detail is not coerced into V1.
- Provider account/request/charge lineage and service context remain bounded
  metadata. The core owns store/call/B-leg identity and imports no concrete
  provider SDK.
- Terminal ownership remains in Phase 5. No stream-time rating, monetary
  journal write, remote price lookup, or settlement behavior was added.

## Verification

All Go commands below used task-local `TEMP`, `TMP`, `GOTMPDIR`, `GOCACHE`, and
`GOMODCACHE` under `E:\codex-phase8-producers`; connector-local tests used
`GOWORK=off`.

Green checks:

```text
go test -count=1 ./internal/core/metering ./internal/plugins/backends/openaiusage ./internal/plugins/backends/openaicompat ./internal/plugins/backends/openailegacy ./internal/plugins/backends/openairesponses ./internal/plugins/backends/openresponsescompat ./internal/plugins/backends/protocols/anthropicmessages ./internal/plugins/backends/protocols/geminigenerate ./internal/plugins/backends/bedrock ./internal/plugins/protocols/openresponses ./pkg/lipapi ./pkg/lipsdk/metering ./pkg/lipsdk/backendplugin ./internal/qa ./internal/testkit/compatibleparity
go test -count=1 ./internal/standardplugins/featurehost/...
go test -count=1 ./internal/archtest -run 'TestPhase1ProducerConsumerCensusIsExactAndDispositioned|TestPhase1CoreHasNoProviderNamedBranches|TestProviderBoundary|TestCodex_connectorHasNoInternalImports|TestPhase8_|TestRuntimeBillingBoundaryHasNoStreamMonetarySettlement|TestPhase8KeepsTerminalFinalizeBillingCostMerge|TestKeepwarmFeatureRemainsProviderNeutral|TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata|TestCoreControlPlaneDoesNotImportProviderSDKsOrConcretePlugins|TestInternalCoreRuntimeDoesNotImportProviderSDKsOrProtocolPlugins'
go test -count=1 ./internal/testkit/compatibleparity
go vet ./internal/core/metering ./internal/core/runtime ./internal/plugins/backends/bedrock ./internal/plugins/backends/openaiusage ./internal/plugins/backends/openaicompat ./internal/plugins/backends/openailegacy ./internal/plugins/backends/openairesponses ./internal/plugins/backends/openresponsescompat ./internal/plugins/backends/protocols/anthropicmessages ./internal/plugins/backends/protocols/geminigenerate ./internal/plugins/backends/streampeek ./internal/plugins/protocols/openresponses ./pkg/lipapi ./pkg/lipsdk/metering ./pkg/lipsdk/backendplugin ./internal/qa ./internal/archtest
make parity-checks
```

`make parity-checks` passed on the rerun with the normal root workspace mode.
The first attempt and a direct ACP reproduction failed only because the E:
task cache exhausted the disk during the cross-compile test; only the
task-local caches were removed and the rerun passed.

The six changed connector modules each passed `go -C connectors/<module> test
-count=1 ./...` and `go -C connectors/<module> vet ./...` for `codex`,
`commandcode-anthropic`, `cursorsdk`, `gitlabduo`, `minimexoauth`, and
`vertex` with `GOWORK=off`.

The broad focused root batch also ran `./internal/core/runtime`; it retains
one pre-existing ownership-transfer characterization failure
(`TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome`,
expected canceled but observed winner). The targeted runtime/provider
selection containing the new authority regressions is green, and no baseline
test or expectation was changed.

`go test -race ./internal/core/metering ./pkg/lipsdk/backendplugin` was not
available on this Windows host: the Go race build's `cgo.exe` exited with
status 2. This is recorded as an environment limitation; non-race focused
tests and the concurrent Codex snapshot fixture are green.

The full root `go test ./...` remains a baseline failure outside Phase 8: an
ignored malformed leading-space directory is discovered by package loading,
existing architecture ratchets report request-attempt/context reread/line
complexity drift, an existing billing-boundary guard reports parent fields, an
existing economics `Rater` guard reports a forbidden type, and the
cross-drive temporary-root test cannot compute a relative path between C: and
E:. No baseline was weakened.

`git diff --check` passed. The working tree contains 85 changed `*.go` files,
below the 100-file source-change gate.

## Architecture repair evidence

The review regressions were reproduced RED and repaired with the smallest
contract/ownership changes:

```text
go test -count=1 ./internal/archtest -run '^TestPublicBackendPluginABIBaselineIsExact$'
RED: public backend-plugin ABI got 786 declarations, want 779; first drift was AccountingEvidenceNegotiated.
$env:UPDATE_BASELINE='1'; go test -count=1 ./internal/archtest -run '^TestPublicBackendPluginABIBaselineIsExact$'; Remove-Item Env:UPDATE_BASELINE
GREEN: the prescribed scanner regenerated testdata/backend_plugin_go_abi_baseline.json with the seven negotiated-usage declarations; a clean rerun passed.

go test -count=1 ./internal/archtest -run '^TestCompactionContinuitySecurity_ContentFreePublicSurfaces$'
RED: CallLegUsageRecord exposed EconomicDispositions and EconomicEvidenceVersion but the intentional allowlist omitted them.
go test -count=1 ./internal/archtest -run '^TestCompactionContinuitySecurity_ContentFreePublicSurfaces$'
GREEN: the narrow content-free public-surface allowlist now names both fields.

go test -count=1 ./internal/archtest -run '^TestPhase1_AttemptBoundaryRatchets/raw_stream_mutation_outside_attempt_owner_detected_red$'
RED: response_pipeline_observations.go:70 directly called attemptSession.loadInner.
go test -count=1 ./internal/archtest -run '^(TestPublicBackendPluginABIBaselineIsExact|TestCompactionContinuitySecurity_ContentFreePublicSurfaces|TestPhase1_AttemptBoundaryRatchets/raw_stream_mutation_outside_attempt_owner_detected_red)$'
GREEN: raw stream access is now behind attemptSession.hasHostOnlyEconomicEvidenceSource in attempt_session.go; the pipeline receives only the neutral query result.
```

The Phase 8-focused architecture/QA command passed after these repairs:

```text
go test -count=1 -p 1 ./internal/archtest -run 'TestPublicBackendPluginABIBaselineIsExact|TestCompactionContinuitySecurity_ContentFreePublicSurfaces|TestPhase1_AttemptBoundaryRatchets|TestPhase8_|TestPhase1ProducerConsumerCensusIsExactAndDispositioned|TestPhase1CoreHasNoProviderNamedBranches|TestProviderBoundary|TestCodex_connectorHasNoInternalImports|TestRuntimeBillingBoundaryHasNoStreamMonetarySettlement|TestPhase8KeepsTerminalFinalizeBillingCostMerge|TestKeepwarmFeatureRemainsProviderNeutral|TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata|TestCoreControlPlaneDoesNotImportProviderSDKsOrConcretePlugins|TestInternalCoreRuntimeDoesNotImportProviderSDKsOrProtocolPlugins'
PASS
go test -count=1 -p 1 ./internal/qa -run 'TestPhase8ProducerCensusHasExplicitDisposition|TestPhase8|TestKeepwarmFeatureRemainsProviderNeutral|TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata'
PASS
```

The complete architecture suite was also rerun with `-p 1`. Its remaining
failures are unrelated baseline/environment findings: the ignored malformed
leading-space import path, the pre-existing `Rater` declaration, request/attempt
AST ratchet drift, the cross-drive source-scan test caused by the mandated E:
`GOTMPDIR`, and the aggregate `internal/core` line budget. The exact line
assessment is deterministic: an archive of base `5c306fe8` contains 103331
non-test `internal/core` lines, while the repaired tree contains 104108, a
Phase 8 net increase of 777 lines (`provider_evidence.go` +603; runtime
evidence/authority changes +174). The checked-in ceiling is 98581, already
4750 below the base before Phase 8. Therefore no Phase 8-only line-budget
baseline update was justified and no ceiling was weakened; the full-suite
failure remains explicitly recorded as pre-existing.

The corresponding failing test names were `TestGOWORKOff_RootListBuildModuleGraph`,
`TestForbiddenDeclarationsAbsent`, `TestScanForbiddenDeclarationsIncludingTests_CurrentRepoPasses`,
`TestRequestAttemptStateTargetRatchetFailsIfTypeReappearsOnCurrentAST`,
`TestRequestAttemptStateRatchetsPassOnCurrentCode`,
`TestRequestAttemptStateBaselineMatchesCurrentAST`,
`TestSourceScanCache_RootAliasRelativeAndAbsolute`, and
`TestLineComplexityBudgets/internal/core`; none references the repaired ABI,
public-surface allowlist, or attempt query path.

```text
go test -count=1 ./internal/archtest -run '^TestLineComplexityBudgets/internal/core$'
FAIL: measured 104108 exceeds the pre-existing 98581 ceiling.
```

## Changed files

Implementation and fixture changes are grouped by owned family:

- Core/SDK: `internal/core/metering/provider_evidence.go`,
  `internal/core/metering/provider_evidence_test.go`,
  `internal/core/runtime/executor_open_attempt.go`,
  `pkg/lipapi/token_accounting.go`,
  `pkg/lipsdk/metering/evidence.go`,
  `pkg/lipsdk/backendplugin/accounting_evidence.go`,
  `pkg/lipsdk/backendplugin/usage_evidence_bridge.go`,
  `pkg/lipsdk/backendplugin/usage_evidence_bridge_test.go`, and the
  `forward_execute_test.go` negotiation fixture.
- Anthropic/Bedrock/Gemini/OpenAI root families: the touched files under
  `internal/plugins/backends/{protocols/anthropicmessages,bedrock,protocols/geminigenerate,openaiusage,openaicompat,openailegacy,openairesponses,openresponsescompat,streampeek}` plus
  `internal/plugins/protocols/openresponses/wire_types.go`.
- Executable families: touched files under
  `connectors/{codex,commandcode-anthropic,cursorsdk,gitlabduo,minimexoauth,vertex}`.
- Census/guards: `.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/phase1-producer-consumer-census.tsv`,
  `internal/archtest/phase1_usage_economics_guards_test.go`, and
  `internal/qa/phase8_producer_census_test.go`.

Review-repair files changed in this worktree are:

- Replay contract: `internal/core/metering/provider_evidence.go`,
  `internal/core/metering/provider_evidence_test.go`,
  `pkg/lipsdk/backendplugin/usage_evidence_bridge.go`, and
  `pkg/lipsdk/backendplugin/usage_evidence_bridge_test.go`.
- Runtime authority boundary: `internal/core/runtime/response_pipeline_observations.go`,
  `internal/core/runtime/attempt_session.go`,
  `internal/core/runtime/attempt_usage_evidence.go`,
  `internal/core/runtime/executor_settlement.go`,
  `internal/core/runtime/phase8_provider_authority_test.go`, and
  `internal/infra/backendplugins/adapter/stream.go`.
- V1 canonical projection: `connectors/cursorsdk/internal/product/stream.go`,
  `connectors/cursorsdk/internal/product/stream_test.go`,
  `connectors/vertex/internal/service/client.go`, and
  `connectors/vertex/internal/service/provider_evidence_test.go`.
- In-process Anthropic/Gemini: `internal/plugins/backends/protocols/anthropicmessages/map_events.go`,
  `internal/plugins/backends/protocols/anthropicmessages/map_events_internal_test.go`,
  `internal/plugins/backends/protocols/geminigenerate/provider_evidence.go`, and
  `internal/plugins/backends/protocols/geminigenerate/provider_evidence_test.go`.
- Executable Anthropic-like streams: `connectors/commandcode-anthropic/internal/anthropic/provider_evidence.go`,
  `connectors/commandcode-anthropic/internal/anthropic/provider_evidence_test.go`,
  `connectors/commandcode-anthropic/internal/anthropic/stream.go`,
  `connectors/gitlabduo/internal/service/provider_evidence.go`,
  `connectors/gitlabduo/internal/service/provider_evidence_test.go`,
  `connectors/gitlabduo/internal/service/client.go`,
  `connectors/minimexoauth/internal/service/provider_evidence.go`,
  `connectors/minimexoauth/internal/service/provider_evidence_test.go`,
  and `connectors/minimexoauth/internal/service/client.go`.
- Codex auxiliary authority: `connectors/codex/internal/codex/attempt.go` and
  `connectors/codex/internal/codex/usage_aggregation_test.go`.
- Negotiation-preserving wrapper: `internal/plugins/backends/streampeek/prepend.go`.
- Architecture contracts: `internal/archtest/compaction_continuity_security_test.go`
  and `internal/archtest/testdata/backend_plugin_go_abi_baseline.json`.

## Residual risks

Executable connectors cannot claim complete advanced V2 transport until the
approved ABI supplies trusted StoreID/B-leg binding and a legitimate native
subject path for account-window/resource evidence. The current disposition is
deliberately explicit and lossless for six legacy counters. Provider monetary
evidence is retained only where a provider response supplied it; no price
sheet or gauge is converted into a debit. Later phases still own durable
revision workers, rating, settlement, reports, and release cutover.
