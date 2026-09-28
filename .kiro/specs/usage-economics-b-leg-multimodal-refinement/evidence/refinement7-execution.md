# Refinement 7.1 execution

Status: `READY_FOR_REVIEW_REFINEMENT_7_1`

## Scope

This record certifies task 7.1 for the OpenAI Responses, OpenAI-compatible,
OpenResponses-compatible, and shared OpenAI usage producer families. The
worktree is `feat/b-leg-usage-economics`, rebased onto `0ac8b0da`.

The certification covers producer-side evidence only: supported input and
assistant-media output references, native modality and direction, provider
units, provider-bound payload observations, final/cumulative usage, late
corrections, provider-reported cost, V1/V2 authority behavior, and explicit
absence. It does not infer prices, convert native media to text tokens, or
invent economics from media references. Gemini, executable connectors,
allocation, rating, admission, and core architecture were not changed.

## TDD and producer evidence

The first compatible-family regression test was run before the implementation
change:

```text
go test -count=1 -run TestParseResource_NativeMediaOnlyUsageIsEmitted ./internal/plugins/backends/openresponsescompat
```

It failed because a valid completed response whose usage contained only native
media fields was parsed without an `EventUsageDelta`. The minimal adapter-local
fix adds the allowlisted native usage measure check to `usagePresent`; no
pricing or core mapping was added.

The new fixtures are:

- `openairesponses/provider_economics_test.go`: final Responses usage with
  image/audio/video/document/file native fields and a genuine provider cost;
  assistant image/file output references; SDK-stream final binding and a
  same-source late correction that produces revision 2 with supersession.
- `openresponsescompat/provider_economics_test.go`: native-only usage
  emission; explicit input/output modality and native-unit mapping with V2
  schema/method/evidence; bound cumulative usage and late replacement; and
  assistant-media references without invented provider measures or charges.

## Certified behavior

### OpenAI Responses and shared OpenAI family

- `usageFromResponse` and the `openaiusage` family mapper retain native media
  fields in the provider evidence draft with distinct input/output direction,
  native units, `openai.usage.v2`, and `openai.usage.native.v2` provenance.
- A provider-supplied `cost` is retained as a provider-reported aggregate
  charge only when the producer marks it present and provider-reported. The
  raw amount lexeme (`0.00125`) is retained as safe evidence; no price-sheet
  lookup or unit inference is involved.
- The final SDK event remains withheld from economic output until a trusted
  B-leg identity is bound. It then emits an observed/provider-response V2
  observation with cumulative semantics. A changed usage payload under the
  same source identity emits a replacement revision and supersedes the first
  observation.
- Existing `openaiusage` fixtures cover Chat and Responses counters, cache and
  reasoning details, genuine provider cost, malformed/negative/overflow cost,
  native media direction/units, and stream binding. `openaicompat` and
  `openailegacy` use the same family evidence path; their focused suites pass.
- Existing `openairesponses` output fixtures verify that supported image/file
  assistant references survive text output. A media reference alone creates no
  provider usage or charge.

### OpenResponses-compatible family

- Native input/output image, audio, video, document, and file fields are
  emitted only in their declared direction and native unit (tokens, items,
  seconds, frames, pages, or bytes). Decimal lexemes are preserved exactly;
  native media is never converted to universal text tokens.
- Native evidence carries `openresponses.usage.v2` and
  `openresponses.usage.native.v2` and is retained alongside standard token
  counters. The provider stream binds the result to the B-leg as an observed
  provider response with cumulative semantics; a later same-source payload is
  a replacement revision with an explicit supersession edge.
- A fractional native media measure is deliberately not projected through the
  V1 six-counter bridge (`ProjectObservationToFact` returns an error). V2 is
  the authoritative representation for that native measure; supported
  integer legacy counters remain separately representable.
- Assistant image/file references without a corresponding provider usage
  field produce no media measures and no charges. The generic capability
  configuration rejects an unsupported `assistant_media_refs` claim, making
  the absence explicit instead of silently advertising a response media
  surface.

### Provider-bound transform boundary

The existing `openairesponses/prepared_boundary_test.go` fixture,
`TestObservePreparedInputUsesFinalResponsesPayloadFields`, proves that the
provider-bound final Responses payload is measured (including final file
bytes) rather than a pre-transform customer representation. No resize,
transcode, or customer-visible output transform is inferred where the
producer did not report one.

## Verification

All commands below were run with `GOWORK=off` where shown.

```text
go test -count=1 -run 'Test(ParseResource_NativeMediaOnlyUsageIsEmitted|ProviderEvidence_Compatible)' ./internal/plugins/backends/openresponsescompat
  PASS
go test -count=1 -run 'TestProviderEvidence_(ResponseNativeMediaCostAndAssistantOutput|SDKStreamProviderEvidence_BindsFinalAndLateCorrection)' ./internal/plugins/backends/openairesponses
  PASS
go test -count=1 ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openresponsescompat/... ./internal/plugins/backends/openaiusage/... ./internal/plugins/backends/openaicompat/... ./internal/plugins/backends/openailegacy/... ./internal/plugins/protocols/openresponses/...
  PASS (all six provider/protocol package groups)
go vet ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openresponsescompat/... ./internal/plugins/backends/openaiusage/... ./internal/plugins/backends/openaicompat/... ./internal/plugins/backends/openailegacy/... ./internal/plugins/protocols/openresponses/...
  PASS
make parity-checks
  PASS (root contract, provider profile, compatible, conformance, and connector-support parity)
go test -count=1 ./internal/qa
  PASS
gofmt -w internal/plugins/backends/openairesponses/provider_economics_test.go
git diff --check
  PASS
```

`go test -count=1 ./internal/archtest` remains red on broad pre-existing
baselines outside this task: compaction billing-field expectation drift,
request-attempt AST baseline/ratchet drift, the existing billing-to-`lipapi`
architecture assertion, runtimebundle package-budget and import-baseline
drift, the core line-complexity budget, and the malformed `GOWORK=off`
import-path check. No owned provider test failed, and no architecture source
was changed to mask those failures.

## Changed files and residual gaps

Changed files are limited to:

- `internal/plugins/backends/openresponsescompat/response.go`
- `internal/plugins/backends/openresponsescompat/provider_economics_test.go`
- `internal/plugins/backends/openairesponses/provider_economics_test.go`
- this evidence record

The compatible producer contract does not expose a typed provider cost field,
so compatible cost remains explicitly absent rather than inferred. The current
OpenAI/OpenResponses output surface certifies the image/file references it
actually emits; unsupported output audio/video/image-generation economics are
not claimed. No commit, Kiro status/checklist, merge, or PR operation was
performed.

## OpenAI-native certification addendum

The native OpenAI Responses surface was rechecked independently of the
compatible-family fixture. `TestProviderEvidence_ResponseNativeMediaOnlyUsageIsEmitted`
proves a completed Responses payload with only native media usage still emits
provider evidence. `TestProviderEvidence_AssistantMediaRefsAloneDoNotInventUsage`
proves assistant image/file references without a usage object do not create a
usage observation or provider charge. `TestProviderCapabilitiesExplicitlyOmitAssistantMediaRefs`
proves native capability negotiation rejects `assistant_media_refs` because
the adapter does not advertise an unverified response-media surface; supported
image/file output references remain observable when actually present. These
checks add no compatible-vendor, pricing, or core changes.

Native-only verification rerun:

```text
go test -count=1 ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openaiusage/... ./internal/plugins/backends/openailegacy/...
  PASS
go vet ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openaiusage/... ./internal/plugins/backends/openailegacy/...
  PASS
go test -count=1 ./internal/plugins/protocols/openresponses/... ./internal/testkit/contract/... ./internal/testkit/conformance
  PASS
go test -race -count=1 ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openaiusage/... ./internal/plugins/backends/openailegacy/...
  SKIPPED: Windows toolchain cgo.exe exited 2 during build before tests ran
```

The repository-wide supporting gates were also rerun: `make parity-checks`
and `go test -count=1 ./internal/qa` passed. `go test -count=1
./internal/archtest` remains red on the pre-existing runtimebundle budget,
compaction-schema, request-attempt ratchet/baseline, billing-to-`lipapi`, core
complexity, runtimebundle import baseline, and malformed `GOWORK=off` import
path checks; no native OpenAI package or architecture source failed.

## Compatible-only certification addendum

This addendum covers only the OpenResponses-compatible producer path. The
earlier inventory should be read as also including the compatible production
change in `internal/plugins/backends/openresponsescompat/provider_evidence.go`.

The compatible path now certifies the following bounded behavior:

- Supported image, audio, video, document, and file input representations are
  forwarded losslessly by the pinned request codec where the profile supports
  them; video requires explicit `video_input` negotiation. Unsupported
  assistant-media response output remains rejected by the response codec, and
  `assistant_media_refs` is rejected as an explicit capability claim.
- Allowlisted provider-native usage fields retain input/output direction and
  native units (image/audio/video/document/file tokens or items, seconds,
  frames, pages, and bytes) under `openresponses.usage.v2` with
  `openresponses.usage.native.v2`. No native field is collapsed into text
  tokens or converted to a provider price.
- Safe evidence paths now retain the actual provider alias or nested path (for
  example `$.usage.prompt_audio_seconds` and
  `$.usage.input_tokens_details.images`) rather than assigning an alias to a
  misleading canonical field name.
- Complete resources and terminal SSE responses with native-only usage still
  emit a usage event and bounded raw usage metadata. Provider evidence remains
  unbound until trusted runtime identity is supplied; after binding it emits a
  B-leg-rooted V2 observed/provider-response observation with cumulative
  semantics. A changed payload under the same source key emits revision 2 as a
  replacement with an explicit supersession edge.
- Native fractional/non-token measures remain V2-only: the legacy V1 bridge
  refuses to project them. Assistant image/file references alone produce no
  provider measures or charges.

There is no generic compatible typed provider-cost field or provider-bound
media-transform hook in the pinned adapter. Consequently compatible cost and
transform economics remain explicitly unavailable; this path performs no
price-sheet inference and makes no provider-bound transform claim. The native
OpenAI prepared-boundary fixture recorded above is outside this addendum and
was not modified here.

Fresh compatible verification:

```text
go test -count=1 ./internal/plugins/backends/openresponsescompat ./internal/plugins/backends/openaiusage ./internal/plugins/backends/openaicompat ./internal/plugins/protocols/openresponses
  PASS
go test -count=1 ./internal/testkit/compatibleparity ./internal/testkit/contract/... ./internal/providerprofiles
  PASS
go vet ./internal/plugins/backends/openresponsescompat ./internal/plugins/backends/openaiusage ./internal/plugins/backends/openaicompat ./internal/plugins/protocols/openresponses
  PASS
go vet ./internal/testkit/compatibleparity ./internal/testkit/contract/... ./internal/providerprofiles
  PASS
go test -count=1 ./internal/qa
  PASS
make parity-checks
  PASS
go test -count=1 -run 'Test(GenericCompatible|OpenResponses.*|.*OpenResponses.*|.*OpenaiCompat.*|.*Backend.*Architecture|.*ProviderBoundary.*)' ./internal/archtest
  PASS
git diff --check
  PASS
gofmt -d internal/plugins/backends/openresponsescompat/provider_evidence.go internal/plugins/backends/openresponsescompat/response.go internal/plugins/backends/openresponsescompat/provider_economics_test.go
  PASS (no output)
```

The path-provenance regression followed strict TDD: the new test first failed
because aliases and nested fields were reported under canonical paths, then
passed after the minimal `usageFieldWithKey` change. Full `go test -count=1
./internal/archtest` remains red only on the unrelated current-tree baseline
violations listed above. `go test -race -count=1
./internal/plugins/backends/openresponsescompat` could not build because the
Windows Go 1.26.6 `cgo.exe` exited 2; no race test executed.

Status: `DONE READY_FOR_REVIEW_REFINEMENT_7_1_COMPAT`.

## Gemini/Vertex-family certification (task 7.2)

Status: `DONE READY_FOR_REVIEW_REFINEMENT_7_2_GEMINI`.

This addendum covers only the Gemini-family protocol boundary in
`internal/plugins/backends/protocols/geminigenerate` and its shared usage
fixtures. The Vertex connector was exercised for compatibility evidence but
was not modified; Vertex connector ownership, price sheets, and billing
composition remain outside this task.

The Gemini path now certifies the following bounded behavior:

- Prompt, candidate, cache, and grounded-tool modality detail lists retain
  distinct input/output direction, text/image/audio/video/document components,
  native token units, evidence-plane dimensions, and observed quality under
  `gemini.usage.v2` / `gemini.generate.v2`.
- A scalar grounded-tool count is retained only when its decoded value is
  non-zero. The generated SDK has no scalar presence bit, so an absent or
  explicitly zero scalar cannot be distinguished at this boundary and is left
  unavailable; grounded-tool modality details are emitted independently and
  never imply an aggregate. Total-token fallback removes a reported non-zero
  grounded-tool count before deriving legacy output tokens.
- Prepared provider-bound media preserves native image/audio/video/document/file
  kind and byte evidence. Gemini video metadata preserves duration in
  milliseconds and frame count from the native FPS; `MediaResolution` is a
  tokenization hint rather than pixel dimensions, so width/height remain
  unavailable instead of being inferred.
- Assistant `FileData` output references retain their MIME modality, while URI
  names do not create usage, resource, storage, or charge evidence. No
  price-sheet inference is performed.
- Trusted B-leg/BillingCallID binding is retained for cumulative terminal
  checkpoints. A changed post-terminal provider snapshot appends a replacement
  revision with an explicit supersession edge; an unchanged late replay is
  deduplicated.

The focused details-only JSON regression for absent grounded-tool presence
followed strict TDD: the new test first failed because the SDK zero value was
emitted as a native grounded-tool measure alongside the preserved AUDIO detail
measure, then passed after the minimal presence-safe condition was added. The
total fallback regression likewise first attributed grounded-tool tokens to
output and then passed after subtracting the provider-reported tool count.

The RED observations were:

```text
go test -run '^TestGeminiDetailsOnlyJSONPreservesNativeModalityWithoutScalarAggregate$' ./internal/plugins/backends/protocols/geminigenerate
  FAIL: details-only usage fabricated a zero grounded-tool aggregate
go test -count=1 -run TestGeminiAbsentGroundedToolDoesNotInventNativeMeasure ./internal/plugins/backends/protocols/geminigenerate
  FAIL: absent Gemini grounded-tool field became a zero measure
go test -count=1 -run TestGeminiTotalFallbackDoesNotAttributeGroundedToolToOutput ./internal/plugins/backends/protocols/geminigenerate
  FAIL: grounded-tool tokens became output tokens
```

All focused regressions passed in the GREEN run and in the complete package
run listed below.

Fresh Gemini/Vertex verification:

```text
go test -count=1 ./internal/plugins/backends/protocols/geminigenerate
  PASS
go test -count=1 ./internal/plugins/backends/gemini
  PASS
go test -count=1 ./internal/plugins/frontends/gemini
  PASS
go test -count=1 ./internal/testkit/conformance
  PASS
go test -count=1 ./internal/testkit/compatibleparity
  PASS
go -C connectors/vertex test -count=1 ./...
  PASS
make parity-checks
  PASS
go vet ./internal/plugins/backends/protocols/geminigenerate ./internal/plugins/backends/gemini ./internal/plugins/frontends/gemini
  PASS
go -C connectors/vertex vet ./...
  PASS
go test -count=1 ./internal/qa
  PASS
go test -count=1 ./internal/archtest -run 'TestPhase8|TestPhase1ProducerConsumerCensusIsExactAndDispositioned|TestKeepwarmFeatureRemainsProviderNeutral|TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata'
  PASS
gofmt -d internal/plugins/backends/protocols/geminigenerate/map_events.go internal/plugins/backends/protocols/geminigenerate/provider_evidence.go internal/plugins/backends/protocols/geminigenerate/provider_evidence_test.go
  PASS (no output)
git diff --check
  PASS
```

Files changed for this addendum:

- `internal/plugins/backends/protocols/geminigenerate/map_events.go`
- `internal/plugins/backends/protocols/geminigenerate/provider_evidence.go`
- `internal/plugins/backends/protocols/geminigenerate/provider_evidence_test.go`

Residual unavailability is intentional: the pinned Gemini usage metadata does
not expose provider-reported media duration/resolution/storage/resource
charges, and its scalar token counters do not carry presence bits. The
protocol therefore preserves only evidence actually surfaced by the API and
leaves unsupported qualifiers unavailable. The focused race command was not
executed because the Windows Go 1.26.6 `cgo.exe` failed during build
(`go test -race -count=1 ./internal/plugins/backends/protocols/geminigenerate`,
exit 1); no race result is claimed.

## Vertex connector certification (task 7.2)

Status: `DONE READY_FOR_REVIEW_REFINEMENT_7_2_VERTEX`.

This addendum covers only `connectors/vertex` and its executable connector
contract fixtures. It does not add pricing or duplicate the in-process Gemini
protocol implementation.

Certified connector behavior:

- Canonical image references and file references carrying audio, video, or
  document MIME types are sent as Vertex `fileData` with the original URI and
  MIME type. Provider output `fileData` now projects to assistant image/file
  reference events with the URI and MIME preserved in both JSON and SSE
  paths. Provider `inlineData` output bytes remain unavailable rather than
  being copied into a canonical reference or treated as a charge.
- Vertex usage detail lists retain all supported provider modalities (text,
  image, audio, video, and document) in distinct input and output directions,
  with native token units, `vertex.usage.v2` schema identity, and prompt or
  candidate evidence-plane dimensions. Existing fixtures also cover cache and
  grounded-tool planes, presence-safe scalar counters, malformed values, and
  provider `trafficType` service context. These native measures are a
  connector-local evidence draft; the executable process transports only the
  representable V1 six-counter compatibility subset until a trusted V2
  identity seam is available.
- No non-token measure, duration/resolution qualifier, resource charge, or
  storage charge is fabricated from a Vertex URI or token detail. The current
  Vertex `UsageMetadata` API exposes token counters, modality token detail
  lists, and traffic context, but no request-scoped duration, resolution,
  resource, or storage charge fields. The Vertex `Content.Part` API exposes
  `fileData` plus media metadata fields such as `videoMetadata` and
  `mediaResolution`; this connector's canonical output contract carries the
  file URI/MIME only, so those economic qualifiers remain explicitly
  unavailable. See the [Vertex GenerateContentResponse reference](https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/rest/v1/GenerateContentResponse)
  and [Vertex Content reference](https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/rest/v1/Content).
- The service advertises protocol minor 8 and the V1 accounting sideband. A
  host offer containing V2 still negotiates minor 8, enables V1 only, and
  resolves `SupportsAccountingEvidenceV2=false`; the descriptor does not
  advertise the V2 feature. The connector has no trusted store identity in
  its invocation ABI, so it does not synthesize a V2 observation or B-leg.
  The V1 bridge leaves the canonical dedupe key when disabled, owns it in the
  sideband when enabled, and is liftable by the host with trusted B-leg,
  BillingCallID, provider-account, source, boundary, and provider authority
  identity. The lifted disposition is explicitly partial/token-only.
- The V1 bridge records a terminal usage snapshot, retains a changed
  post-terminal payload as a late correction, and suppresses an exact replay.
  Typed cumulative/replacement revisions are unavailable at the connector
  boundary and are not claimed; the host lift remains `delta` with partial
  coverage.

The output-reference regression followed strict TDD. Before the minimal
shared decoder helper, the focused tests reported no output references and no
message start for `fileData`-only candidates. After the helper, the focused
tests passed.

Fresh Vertex and host-bound verification:

```text
GOWORK=off go test ./... -count=1                         PASS (connectors/vertex)
GOWORK=off go vet ./...                                  PASS (connectors/vertex)
go test -count=1 ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/adapter/... ./internal/testkit/contract/... ./internal/testkit/compatibleparity ./internal/testkit/conformance
                                                           PASS
go test -count=1 ./internal/qa                            PASS
go test -count=1 ./internal/archtest -run 'TestPhase8|TestPhase1ProducerConsumerCensusIsExactAndDispositioned|TestKeepwarmFeatureRemainsProviderNeutral|TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata'
                                                           PASS
make parity-checks                                       PASS
gofmt -d connectors/vertex/internal/service/client.go connectors/vertex/internal/service/client_media_test.go connectors/vertex/internal/service/provider_evidence_test.go
                                                           PASS (no output)
git diff --check                                         PASS
```

Files changed for this connector addendum:

- `connectors/vertex/internal/service/client.go`
- `connectors/vertex/internal/service/client_media_test.go`
- `connectors/vertex/internal/service/provider_evidence_test.go`
- `connectors/vertex/contracttest_test.go`

The Vertex race suite was not run; no race result is claimed. No connector
price sheet, tariff inference, resource allocation, or V2 identity synthesis
was added.

## Non-request resource attribution certification (task 7.4)

Status: `DONE READY_FOR_REVIEW_REFINEMENT_7_4_RESOURCE_ATTRIBUTION`.

The allocation-aware COGS seam now consumes only canonical, conserved
allocation records from non-request resource or statement-line subjects. The
expanded line retains the source subject, source basis, immutable allocation
ID/version/revision, policy method/version/hash, exact share, rounded target
amount and explicit unallocated remainder. A pending supersession remains
visible and makes the COGS result non-payable while resolved lines remain
available for reporting.

An allocation target is attributable only when it names a real supplied
BillingCallID or B-leg. A missing B-leg target fails closed with a typed error;
validation runs before the informational-line monetary exemption, so an
informational line cannot bypass target validation. The seam never constructs
a synthetic B-leg and never appends an allocation target to `IncludedLegKeys`.
Resource and statement-line amounts are added to
the native-currency operator COGS subtotal, while unallocated, informational,
and exact non-monetary quantity lines remain source-preserving and outside the
money subtotal. Request-scoped provider-charge allocations are rejected at
this seam so provider charge evidence cannot be paid twice.

Prompt-cache observations and keep-warm maintenance remain B-leg/operation
evidence with no fabricated resource charge. A genuine prompt-cache storage,
reservation, subscription, or shared-resource charge can use the existing
`SubjectResource` or `SubjectStatementLine` allocation contract without
changing prompt-cache producers or adding a schema/store path.

The focused regression followed strict TDD. Before the implementation, the
new allocation-aware tests failed to compile because the COGS seam, source
policy projection, and synthetic-target error were absent:

```text
go test -count=1 -run 'TestPhase7(ResourceAllocationAddsPayableCOGSWithoutInferenceLeg|ResourceAllocationRejectsSyntheticBLegTarget|PendingResourceAllocationIsNotPayable)' ./internal/core/billing
  FAIL: undefined AttributeOperatorCOGSWithAllocations, AllocatedCostLine.Policy, and ErrAllocationTargetNotAttributable
```

After the minimal implementation, the focused RED vectors and package tests
passed:

```text
go test -count=1 -run 'TestPhase7' ./internal/core/billing
  PASS
go test -count=1 ./internal/core/billing/... ./pkg/lipsdk/economics/... ./pkg/lipsdk/promptcache/... ./internal/standardplugins/featurehost/...
  PASS
go vet ./internal/core/billing ./pkg/lipsdk/economics ./pkg/lipsdk/promptcache ./internal/standardplugins/featurehost
  PASS
gofmt -d internal/core/billing/allocation.go internal/core/billing/cost_selection.go internal/core/billing/resource_attribution_phase7_test.go
  PASS (no output)
git diff --check
  PASS
```

The tests cover a payable shared prompt-cache resource allocation with an
explicit half-source target and half-source unallocated remainder, source
resource/account/period and policy preservation, statement-line account
ownership, exact non-conserved-weight rejection, pending allocation
non-payability, ordinary and informational synthetic B-leg rejection, valid
informational non-monetary handling, unchanged provider-leg inclusion, and
unchanged selected B-leg inference evidence. The review regression was RED
before reordering because an informational synthetic target returned no error;
the focused Phase 7 billing tests are GREEN after validation was moved ahead
of the informational skip. No infrastructure schema or store changes were
needed.

Files changed for this addendum:

- `internal/core/billing/allocation.go`
- `internal/core/billing/cost_selection.go`
- `internal/core/billing/resource_attribution_phase7_test.go`

The focused race attempt was not a result: Windows `cgo.exe` failed during
the build (`go test -race -count=1 -run 'TestPhase7' ./internal/core/billing`).
The full `go test -count=1 ./internal/archtest` run remains red on existing
branch-wide request-attempt baseline, package-budget, import-baseline, and
current field-surface checks; no task-specific allocation regression failed.
