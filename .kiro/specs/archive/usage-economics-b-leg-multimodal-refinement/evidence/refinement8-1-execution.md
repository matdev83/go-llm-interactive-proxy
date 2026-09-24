# Refinement 8.1 execution: multimodal direction and transform certification

Status: `READY_FOR_REVIEW_REFINEMENT_8_1`

## Scope

This record certifies Kiro Task 8.1 (`Run multimodal direction and transform
certification`) for spec `usage-economics-b-leg-multimodal-refinement`,
requirements 6.1 and 6.2 only.

Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`,
branch `feat/b-leg-usage-economics`, HEAD `797fbd55`
(`chore(usage-economics): record task 5.3 blocker`).

Out of scope (untouched): Task 5.3 blocker, stashes, A-leg report work,
task checkboxes/spec status, commits/staging, production media codecs,
routing, provider adapters, migrations, public APIs.

This is a certification/test task. No production `*.go` file was changed.
Three test files were added; one evidence file (this one) was added.

## Requirement-to-test matrix (6.1 / 6.2)

Prior state: fixture inventory only
(`pkg/lipsdk/metering/usage_economics_refinement_phase1_red_test.go` asserts
fixture JSON shape, provider/customer values differ, video token/rate
qualifiers). Direction identity was proven generically
(`TestPhase2V2_ComponentKeyDirectionQualifiersAndCanonicalBytes`,
`TestPhase2ReviewMediaRequiresDirection`). Asymmetric media rating was proven
generically (`TestPhase9ReferenceRater_AsymmetricMediaAndFixedFee` with
image-in/out counts and audio 10s/3s; `TestPhase9ReferenceRater_AllNativeNonTokenDirectionsRemainDistinct`
with video-in seconds / video-out frames). None of them asserted the exact
Task 8.1 acceptance vectors: 12.5s input vs 8.0s output audio with different
rates, video input in provider-native tokens vs video output per generated
second, document input per page plus document output, per-direction
transform separation with supplier rating on provider-bound/provider-origin
representations, independent customer/provider rate selection per direction,
durable round-trip for all modalities/directions, or missing-stays-missing
across every modality.

Certification files (all test-only; production `NewReferenceRater`,
`metering.Observation`, `economics.Valuation`, journalstore `DurableStore`
and billingstore `DurableStore` contracts reused unchanged):

- `internal/core/billing/refinement81_multimodal_direction_transform_certification_test.go`
  (package `billing`, 668 lines): in-memory neutral-evidence rating, full
  economic line-tuple assertions, envelope canonicalization.
- `internal/infra/metering/journalstore/refinement81_multimodal_observation_durability_test.go`
  (package `journalstore_test`, 315 lines): real SQLite observation
  append/readback with file-backed close/reopen.
- `internal/infra/billingstore/refinement81_multimodal_valuation_durability_test.go`
  (package `billingstore`, 421 lines): real SQLite valuation
  append/readback with file-backed close/reopen plus full line-tuple
  assertions on readback.

| Acceptance vector | Certifying test | What is asserted |
|---|---|---|
| image input, native count, direction rate | `TestRefinement81_MultimodalDirectionRateCertification/image-input` + `TestRefinement81_JournalDurability/image-input` + `TestRefinement81_ValuationDurability/image-input` | 2 images x provider 3 = 6 (Q plane) and x customer 5 = 10 (R plane); full line tuple (rule/tariff ID+version, quantity, unit price, amount, USD, rated status, subject/B-leg/observation/valuation lineage); text-token-only tariff fails `ErrRateMissing`; SQLite append + reopen readback of subject, key, quantity, boundary/source and projections |
| image output, native count, direction rate | rate/journal/valuation `image-output` subtests | 1 x 4 = 4 vs 1 x 7 = 7; same full-tuple and durability proof |
| audio input 12.5s, distinct rate | rate/journal/valuation `audio-input[-12.5s]` subtests | 12.5s x 0.5 = 6.25 vs x 2 = 25; same full-tuple and durability proof |
| audio output 8.0s, distinct rate | rate/journal/valuation `audio-output[-8.0s]` subtests | 8.0s x 1 = 8 vs x 3 = 24; input/output second totals never merged (separate direction-qualified lines) |
| video input provider-native tokens | rate/journal/valuation `video-input[-provider-native-tokens]` subtests | 4096 tokens x 0.001 = 4.096 vs x 0.002 = 8.192; native token unit preserved, no conversion to seconds |
| video output per generated second | rate/journal/valuation `video-output[-generated-seconds]` subtests | 8s x 2 = 16 vs x 5 = 40; native second unit preserved |
| document input per page | rate/journal/valuation `document-input[-pages]` subtests | 4 pages x 1.5 = 6 vs x 2.5 = 10; no token conversion unless provider schema defines it (none does here) |
| document output pages | rate/journal/valuation `document-output[-pages]` subtests | 2 x 3 = 6 vs x 6 = 12 |
| ingress transform: customer input resized before provider; supplier uses provider-bound | `TestRefinement81_InputTransformUsesProviderBoundRepresentation` + journal `image-input` (both representations) + valuation `image-input` (R with both) | backend_egress 2 images vs frontend_ingress 1 image; Q full line 6 (provider-bound only, customer diagnostic excluded and asserted absent from line refs; alone-vs-both rating identical); R full line 10 (backend quantity at customer rate; frontend_ingress excluded by `isRetailBLegObservation`); both representations durably appended and read back |
| egress transform: provider image output transcoded before client; supplier stays provider-origin | `TestRefinement81_OutputTransformPreservesProviderOrigin` + journal `image-output` (both representations) + valuation `image-output` (R with both) | backend_ingress 3 vs frontend_egress 1; Q full line 12 with alone-vs-both identity; R full line 21; customer-visible output never replaces supplier usage; both representations durable |
| durable round-trip all modalities/directions | `TestRefinement81_JournalDurability` (8 subtests, 16 observations) + `TestRefinement81_ValuationDurability` (8 subtests, 16 valuations) + `TestRefinement81_DurableRoundTripPreservesMultimodalIdentity` (envelope canonicalization) | Real `AppendObservations` batch + idempotent replay, file-backed close/reopen, then `GetObservation`/`ListObservations`/`ListObservationComponents` readback asserting subject/call/B-leg, component, direction, unit, schema/dimensions, quantity coefficient/scale/presence, boundary/origin/acquisition/authority/perspective, fingerprint; `AppendValuation`/`GetValuation` readback asserting the full line tuple and envelope lineage; no raw-media payload on the wire |
| missing stays missing, never zero/text-token | `TestRefinement81_MissingDetailNeverBecomesZeroOrTextTokens` (8 subtests) | nil value + `QualityUnavailable` validates, then rates `ErrQuantityIncomplete` per modality; error never coerces toward text tokens |
| direction is canonical identity | `TestRefinement81_DirectionIsCanonicalIdentity` | input/output keys differ in `Equal`/`CanonicalKey`/`Fingerprint` for image/audio/video/document; video tokens-in vs seconds-out distinct; `request` scope rejected as flow direction |

Properties covered: direction in fingerprint; provider-native units only;
backend boundaries (`backend_egress`/`backend_ingress`) drive supplier (Q)
and retail inference (R) economics while customer boundaries
(`frontend_ingress`/`frontend_egress`) are validated diagnostic evidence
excluded from inference planes by the production predicates; neutral
evidence -> rating -> real SQLite durable round-trip (append, file-backed
close/reopen, consumer-API readback) is exact on identity, quantity,
line economics and lineage; every rated line additionally asserts the full
tuple (rule ID, rated status, canonical component+direction, native unit,
quantity, unit price, amount, USD total, envelope basis/perspective/
completeness/scope, rater/tariff/policy refs, single observation ref)
against literal expectations; missing stays missing; customer/provider
rates independently asserted per direction (each total equals qty x its own
rate, and transform supplier amounts are identical with and without the
customer representation); no raw-media persistence (wire-shape assertion);
no production codec/routing change.

## RED / GREEN

RED (before additions, clean HEAD `797fbd55`):

```text
go test ./internal/core/billing/ -run 'TestRefinement81' -count=1
# ok ... [no tests to run]  -> zero coverage for the 8.1 vectors
go test ./internal/infra/metering/journalstore/ -run 'TestRefinement81' -count=1
# ok ... [no tests to run]  -> no real persistence coverage
go test ./internal/infra/billingstore/ -run 'TestRefinement81' -count=1
# ok ... [no tests to run]  -> no durable valuation coverage
```

Gap probe: no existing billing test asserted `12.5`, exact `8.0` audio
pairing, video tokens-vs-seconds, or transform boundary rating separation
(one generic `document-page` input rate existed in
`TestPhase9ReferenceRater_AllNativeNonTokenDirectionsRemainDistinct`; it
does not cover the 8.1 matrix). No existing test asserted the full
economic line tuple per direction (rule/status/unit-price/refs/policy),
and no multimodal test exercised the real journalstore/billingstore SQLite
append/readback path (prior durability was `CanonicalJSON` envelope
comparison only).

GREEN (after adding the three test-only certification files; no production edit):

```text
go test ./internal/core/billing/ -run 'TestRefinement81' -count=1
# PASS: 6 tests, 28 subtests (8 rate + 8 envelope round-trip + 8 missing + 4 direction pairs)
go test ./internal/infra/metering/journalstore/ -run 'TestRefinement81' -count=1
# PASS: TestRefinement81_JournalDurability, 8 subtests (16 observations, append + reopen + 3 readback APIs)
go test ./internal/infra/billingstore/ -run 'TestRefinement81' -count=1
# PASS: TestRefinement81_ValuationDurability, 8 subtests (16 valuations, rate + append + reopen + full-tuple readback)
```

`go vet` clean on all three packages; `gofmt -l` clean on all three files.

## Validation gates

- Focused: all three `TestRefinement81*` suites PASS with `-count=1` and on
  repeat (billing 6 tests; journalstore 1 test/8 subtests; billingstore
  1 test/8 subtests).
- Relevant packages: `go test ./internal/core/billing/
  ./pkg/lipsdk/metering/ ./pkg/lipsdk/economics/
  ./internal/infra/metering/journalstore/ ./internal/infra/billingstore/
  -count=1` -> PASS (all five packages ok).
- `make test-unit` -> FAIL on pre-existing `internal/archtest` budget/ratchet
  tests only (line-complexity ceilings, package-tree budgets, attempt-sequence
  and compaction ratchets, billing lipapi-import boundary); the remediated
  failure set is byte-identical to the pre-remediation baseline (same tests,
  same messages, same `internal/core` measured 115814 vs ceiling 98581 and
  same `runtimebundle` figures), so the three added test-only files introduce
  no new budget/ratchet failure. Billing, metering, economics, journalstore
  and billingstore packages pass.
- `make parity-checks` -> PASS (exit 0, all contract/conformance packages ok).
- `make test-db-parity` -> FAIL on pre-existing postgres-direct schema
  mismatch in `internal/infra/metering/journalstore` (`value_present`
  integer vs boolean); no migration/store code touched by this task and the
  failing assertion does not involve the new tests. No new dbparity
  registration was added: both touched store components were already
  parity-registered and the new tests are SQLite-harness integration tests
  that fit the existing catalog without changing it.
  `make test-db-parity-sqlite` -> PASS (exit 0, all components ok).
- `go vet` clean on all three touched packages; `gofmt -l` clean on all
  three new files.
- No race/fuzz delta: no goroutines, channels, parsers, or codecs touched.

## Skips (honest)

- No provider-adapter or connector fixture was added: Task 7.x already
  owns family conformance; 8.1 reuses the neutral contract harness plus the
  existing store harnesses only.
- No PostgreSQL live run was added: the durability proof uses the supported
  file-backed SQLite harness with close/reopen through production
  migrations/constructors; the postgres-direct failure above is a
  pre-existing schema mismatch unrelated to this task.
- No proxy-service tariff line was posted: customer-boundary evidence is
  proven excluded from inference planes and retained as valid diagnostic
  observations; explicit proxy-service pricing remains Task 6.3/retail
  territory already covered by
  `TestPhase10RetailRatingKeepsProxyServiceMetersSeparateFromInference`.

## Files changed

- Added: `internal/core/billing/refinement81_multimodal_direction_transform_certification_test.go` (668 lines)
- Added: `internal/infra/metering/journalstore/refinement81_multimodal_observation_durability_test.go` (315 lines)
- Added: `internal/infra/billingstore/refinement81_multimodal_valuation_durability_test.go` (421 lines)
- Updated: `.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement8-1-execution.md`
- Modified production code: none (no checkboxes, no spec status, no commits).
