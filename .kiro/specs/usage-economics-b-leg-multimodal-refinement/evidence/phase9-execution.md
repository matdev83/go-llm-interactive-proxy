# Phase 9 component-rating execution evidence

Status: `READY_FOR_REVIEW`

Base checkpoint: `4a98f977`

Scope: parent tasks 9.1-9.5 and refinement task 2.3. This phase implements
post-usage deterministic E/Q/P component valuation, immutable tariff material,
legacy scalar adaptation, qualifier/tier selection, coverage validation and
durable canonical valuation replay. It does not add stream-time rating, a
provider SDK dependency, price crawling, a tariff database, or monetary writes
to token/receive paths. The public `pkg/lipsdk/economics` package exports only
the provider-neutral post-usage `Rater` seam; it does not export the forbidden
stream-time financial bridge, which remains owned by `internal/core/billing`.

## TDD evidence

The initial Phase 9 fixture compilation was RED because the public tariff/rule
contracts and billing reference-rater entry points did not yet exist. The
focused test compiler reported missing `economics.RatingRule`,
`economics.TariffSnapshot`, `economics.NewReferenceRater`, independent
valuation and catalog tariff APIs.

Additional review RED cases were run before each narrow repair:

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_UsesAvailableLessSpecificRule -count=1
```

Result: `ErrQualifierMissing: service_tier` was returned even though the
less-specific `region=us` rule was available.

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_WholeContextThresholdIncludesSiblingInputUnits -count=1
```

Result: whole-context selection priced four uncached tokens at `4/0` instead
of selecting the tier from the eight-token input context and producing `8/0`.

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_ReasoningOutputIsMeaningfulWhenUnpriced -count=1
```

Result: an unpriced reasoning-output component was incorrectly omitted and
reported only generic missing evidence rather than typed missing-rate status.

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_EAndQRemainIndependentWhenPExists -count=1
```

Result: the shared input's local rater/tariff labels and unrelated observation
references leaked into the independent provider-reported valuation.

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_MinimumOnFreeRateIsNotExplicitFree -count=1
```

Result: a zero unit rate with a positive minimum was labelled `explicit_free`
even though it carried a positive charge. The final-charge status check now
keeps that line rated.

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_AllDeclaredFixedFeesApplyOnce -count=1
```

Result: two distinct fixed fees at the same call scope were incorrectly
rejected as overlapping. Fixed identities are independently declared charges,
so both now apply once at their common scope.

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_InclusiveCoverageAcrossObservationsIsNotDoubleCounted -count=1
```

Result: an unlinked aggregate and component in separate observations were
silently additive because coverage validation only compared observation IDs.
Subject-scoped coverage now rejects that pair while still deduplicating an
explicit inclusive edge across observations.

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_WholeContextExcludesInformationalInputTotal -count=1
```

Result: whole-context tier selection included an already-inclusive input
total and selected the expensive tier from duplicated quantity. Informational
total/reasoning measures are now excluded from threshold context while their
evidence remains available.

Each RED case was followed by a minimal implementation and a GREEN rerun.

### Consolidated review repair

The five review invariants were added as behavioral RED assertions before the
repair. The combined focused command was:

```text
go test ./internal/core/billing -run 'TestPhase9ReferenceRater_(ReducesShuffled|ReducesSame|ReducesSuperseding|AllowsSubNano|RejectsUnrepresentable|RoundingBoundary|RejectsContradictory|PeriodScope)|TestPhase9RateIndependent' -count=1
```

The pre-repair result was RED with the following distinct failures: shuffled
cumulative-plus-delta rated `10/0` instead of `12/0`; same-component separate
streams collapsed to one line; a superseding cumulative correction rated
`10/0` instead of `7/0`; a scale-10 unit price returned
`rating precision is unsupported`; line/call fractional-boundary coverage
could not reach rating under that scale gate; contradictory kinds and a
dimension/qualifier split overlap were accepted (`error=<nil>`); and E/Q/P
all retained the shared `3333...` input-set hash.

The minimal repair delegates reduction to
`internal/core/metering/aggregate.ApplyObservations` and projects its
source-scoped effective measures, carries decimal rates through scale 18 to
the final declared rounding boundary, retains exact call/period lines without
premature rounded values, validates explicit rule-kind material and wildcard
component-domain overlap at publication, and derives basis-plus-sorted-ref
plane hashes. The same command then passed:

```text
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing
```

The genuine overflow case remains covered by
`TestPhase9ReferenceRater_RejectsUnrepresentableRoundedMoney` and continues to
return `ErrRatingPrecision`. The focused review tests also assert that line
rounding sums per-line rounded money, call rounding rounds the exact aggregate
once, period scope is required and retained, and shuffled E/Q/P input produces
stable distinct hashes and valuation IDs.

### Eight-blocker repair

The second review repair added adversarial RED assertions before the replay,
completeness, provider-charge, rule-kind, fixed-scope, valuation-shape and
unavailable-authority changes. The first behavioral run was:

```text
go test ./internal/core/billing -run 'TestPhase9Repair_' -count=1
```

It was RED: identical replay refs were rejected as duplicates; a superseded
unavailable measure poisoned a complete replacement; unresolved E and P
corrections were rated or silently accepted; provider base plus correction
totaled both charges; P line halves were rounded only after exact aggregation;
aggregate overflow became generic invalid valuation; explicit linear/block/
minimum/all-units/graduated contradictions published; fixed scope mismatches
were not classified; call/period lines accepted rounded payloads; and an empty
unavailable authority claim did not create an E plane.

The minimal repair now canonicalizes replay survivors before plane refs/hashes,
marks effective reduced measures complete independently of superseded stale
revisions, exposes reducer pending/unavailable state to E/Q, projects effective
reduced charges to P, reuses line-boundary rounded money for P totals and
propagates overflow, makes explicit `RatingRule.Kind` authoritative, requires
submission/call/period fixed-fee scope compatibility, rejects non-line rounded
line payloads, and treats empty unavailable claims as applicable evidence.
Evaluator branches are explicit by kind while empty kind retains the documented
legacy inference path. The GREEN rerun was:

```text
go test ./internal/core/billing -run 'TestPhase9Repair_|TestPhase9ReferenceRater_|TestPhase9RateIndependent' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing
go test ./internal/core/metering/aggregate ./internal/core/metering/replay -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay
```

The focused repair assertions cover stable replay identity and genuine
revision refs for E/Q/P, superseded stale incompleteness and unresolved
corrections, provider correction replacement and exact replay deduplication, P
line rounding and aggregate overflow, contradictory fields for every explicit
quantity kind, all six fixed-fee scope mismatches, non-line rounded amounts,
and an empty unavailable claim. Phase 9 durable/composition tests and existing
economics tests remain green.

## Implementation and requirement mapping

| Requirement | Delivered behavior and evidence |
| --- | --- |
| 9.1 / 7.1, 7.4-7.6 | `economics.TariffSnapshot` and `RatingRule` validate bounded immutable material, canonicalize rule/qualifier order and carry a content hash/ref. `PricingSnapshotToTariff` adapts the existing scalar catalog under named `legacy_scalar_per_million_tokens_v1` semantics. `SnapshotCatalog` freezes and resolves tariff material by ID/version; timestamp-only refresh is replay-stable and changed content is immutable. |
| 9.2 / 2.6, 3.4, 7.3, 8.2 | Billing uses `math/big.Rat` and checked integer nano rounding for linear rates, rational rates, fixed fees, block rounding, minimums and all declared rounding policies. Lines and totals retain exact terminating decimals or reduced bounded rational numerator/denominator plus rounded money. Canonical charge coverage is closed and validated before P rating; inclusive/additive contradictions, cycles, unresolved references and additive aggregate/component overlap fail closed. |
| 9.3 / 7.1, 7.3-7.5 | Conditions match recorded qualifiers deterministically, equal-specificity overlaps are rejected at publication, less-specific matching rules remain usable when a more-specific qualifier is absent, and unrelated missing qualifiers cannot select a rate. Whole-context selection includes sibling native quantities sharing direction/unit/schema; billable quantity remains the charged line quantity. All-units and graduated tiers are tested. Period selection, period rounding and period fixed fees require an explicit period scope. |
| 9.4 / 1.1-1.6, 4.3, 7.2 | `RateIndependentValuations` filters immutable input refs independently for E (local), Q (provider quantity) and P (provider charge), preserving partial planes instead of substituting one result for another. P strips incidental local rater/tariff context and preserves provider aggregate/component claims. Durable append/get compares canonical JSON and retains exact values, refs, status and completeness. |
| 9.5 / 2.2-2.3, 2.5, 7.5-7.6, 15.5, 18.1-18.2 | Generic fixtures cover image/audio/video direction, document/page, request, submission, time, storage-product, credit and a namespaced synthetic meter. Direction, unit and component identity remain separate. Missing/unsupported rate, currency mismatch, unsupported precision, incomplete quantity and explicit free are distinct typed statuses; no missing case becomes a zero charge. Reasoning output remains meaningful unless explicitly priced, while aggregate informational totals are not implicitly billable. |
| Refinement 2.3 | Asymmetric image input/output and audio input/output rules use independent native units/rates with no provider-name branch and no SQL schema change. The full non-token fixture also covers video direction and document/page. |

Production ownership is limited to `internal/core/billing`, the approved
`internal/core/metering/aggregate` projection seam,
`pkg/lipsdk/economics`, and the existing `internal/infra/billingcompose`
catalog/resolver seam, with the enterprise compatibility assertion updated for
the restored provider-neutral public `Rater` seam. Existing billingstore canonical
valuation persistence is reused; exact rational amounts are retained in the
immutable `billing_valuations.canonical_json` source record and survive
projection rebuild/replay.

## Verification

Passed:

```text
go test ./internal/core/billing -count=1
go test ./internal/core/billing -run 'TestPhase9Repair_|TestPhase9ReferenceRater_|TestPhase9RateIndependent' -count=1
go test ./pkg/lipsdk/economics -count=1
go test ./pkg/lipsdk/... -count=1
go test ./internal/core/metering/aggregate -count=1
go test ./internal/core/metering/aggregate ./internal/core/metering/replay -count=1
go test ./internal/infra/billingcompose -run 'TestPhase9' -count=1
go test ./internal/infra/billingstore -run 'TestPhase9' -count=1
go test ./internal/infra/billingstore -count=1
make test-db-parity-sqlite
go test ./internal/archtest -run 'TestBillingFinalConvergencePhase1BridgeForbidIsActive|TestForbiddenDeclarations' -count=1
go vet ./internal/core/billing ./pkg/lipsdk/economics ./internal/infra/billingcompose ./internal/infra/billingstore
go vet ./internal/core/metering/aggregate ./internal/core/metering/replay
gofmt -d <all 19 modified Go files>
git diff --check
```

The focused architecture guard confirms that the public economics
`pkg/lipsdk/economics:type:Rater` declaration is restored and provider-neutral;
no new forbidden internal `RatingInput` declarations were introduced. The nested
enterprise compatibility module also passed `go test ./...`.

## Skipped or baseline failures

- `go test ./internal/infra/billingcompose -run TestResolveCallRatingFailoverSettlesSurfacedModelCard -count=1` remains the existing scalar failover failure: settlement is `1003`, expected `1000`. It is outside the component-rater changes.
- The full `go test ./internal/archtest -count=1` remains red on existing branch-wide request/attempt ratchets (`353 > 340` direct copies and `18 > 17` context re-reads), the aggregate `internal/core` complexity budget (`105865 > 98581`), and the pre-existing malformed ignored import path `.../ internal/infra/billingstore`. The targeted convergence/forbidden guard passes.
- `make test-db-parity-sqlite` passes all registered SQLite components. The direct PostgreSQL parity run reaches the existing metering-journal schema comparison but remains red because `metering_components.value_present` is `int4` on PostgreSQL while the parity contract expects the boolean category; Phase 9 does not modify that migration.
- A fresh `make test-db-parity-postgres-direct` rerun passed the initial billingstore, leasestore and continuity packages but timed out on the unrelated conversationview `TagCap4096` case (`read tcp ...: i/o timeout`); no Phase 9 persistence schema was changed.
- `go test -race ./internal/core/billing -run 'TestPhase9Repair_|TestPhase9ReferenceRater_|TestPhase9RateIndependent' -count=1` was not executable on this Windows host because the Go toolchain `cgo.exe` exited with status 2; the rating code has no new goroutines or shared mutable state.
- The full root `go test ./...` gate was not claimed because the malformed ignored import path prevents module graph enumeration. Focused billing/economics, durable, architecture and vet evidence above are the relevant phase checks.

## Changed files and risk

There are 19 changed `*.go` files, below the 100-file source gate. Changes
include the billing reference rater/coverage validator/legacy adapter and
tests, the Phase 3 aggregate projection seam, public economics tariff and
valuation extensions, catalog/resolver snapshot composition and tests, the
durable round-trip test, and the enterprise compatibility assertion. No commit
or Kiro status change was made.

Residual risk: exact non-terminating amount values are queryable through the
immutable canonical valuation JSON and replay path; the existing SQL line
projection exposes its bounded decimal columns but has no dedicated rational
amount numerator/denominator columns. A future operator query that needs SQL
predicates over rational amounts would require an additive projection, without
changing the canonical authority.

### Second independent review repair (three blockers)

The three narrowed review invariants were covered by behavioral RED assertions
before implementation:

```text
go test ./internal/core/billing -run 'TestPhase9Repair2_' -count=1
```

RED failed because equivalent receipt timestamps made supersession validation
order-dependent, corrections over unavailable/unknown predecessors rated as
usable, and a whole-context tier rated an available line despite an incomplete
sibling. The tests were:
`TestPhase9Repair2_ReplayReceiptMetadataDoesNotChangeRatingIdentity`,
`TestPhase9Repair2_CorrectionOverUnusablePredecessorIsIncomplete`, and
`TestPhase9Repair2_WholeContextTierWaitsForCompleteSiblings`.

The minimal repair adds a replay-stable semantic fingerprint that normalizes
transport `ReceivedAt` for observation refs, replay comparison, and resolved
supersession validation while preserving the historical full-envelope hash;
the Phase 3 reducer now reports resolved unusable correction predecessors and
marks affected reduced measures incomplete (including transitive correction
chains), and E/Q/P rating refuses to charge those states. Whole-context and
period rules now preflight every matching sibling's completeness before tier
selection or monetary evaluation. Durable observation lookup accepts both the
new stable ref and historical full hash.

The three focused assertions then passed:

```text
go test ./internal/core/billing -run 'TestPhase9Repair2_' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing
```

Additional GREEN verification for the reducer and affected persistence/domain
surfaces:

```text
go test ./internal/core/metering/aggregate ./internal/core/metering/replay -count=1
go test ./internal/core/billing ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/core/metering/replay ./internal/core/metering/aggregate ./internal/infra/metering/journalstore ./internal/infra/billingstore -count=1
go test ./internal/infra/billingcompose -run 'TestPhase9' -count=1
go test ./pkg/lipsdk/... -count=1
```

All commands passed. The touched-package vet command, targeted architecture
and QA commands were:

```text
go vet ./internal/core/billing ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/core/metering/aggregate ./internal/core/metering/replay ./internal/infra/metering/journalstore ./internal/infra/billingcompose ./internal/infra/billingstore
go test ./internal/archtest -run 'TestBillingFinalConvergencePhase1BridgeForbidIsActive|TestForbiddenDeclarations' -count=1
go test ./internal/qa -run 'Test.*(DualPlane|LegacyAbsent|ChangeSize|Forbidden|Billing)' -count=1
gofmt -d <all 23 modified Go files>
git diff --check
```

All listed commands passed. The worktree contains 23 modified/untracked Go files, below the
100-file gate; no commit or Kiro status change was made.

Residual risks are unchanged from the prior section: the host Windows
toolchain cannot execute the focused race build because `cgo.exe` exits with
status 2; full archtest and direct PostgreSQL parity retain their documented
unrelated baseline failures; and full root `go test ./...` remains unclaimed
because of the malformed ignored import path.

### Third independent review repair (four blockers)

This bounded repair restored the approved provider-neutral public Rater seam,
made durable observation replay receipt-time stable, bound generated valuation
identity to immutable rating context, and propagated required-evidence failures
through valuations that retain independently chargeable fixed lines.

Strict-TDD RED evidence preceded each implementation:

```text
go test ./pkg/lipsdk/economics -run '^TestPhase9Repair3_PublicRaterContractRemainsProviderNeutral$' -count=1
RED: build failed with undefined: Rater

go test ./internal/infra/metering/journalstore -run '^TestPhase9Repair3_DurableObservationReplayIgnoresReceiptDrift$' -count=1
RED: receipt-only replay returned metering/journalstore: fact identity collision

go test ./internal/infra/billingstore -run '^TestPhase9Repair3_ValuationIdentityIncludesSnapshotContext$' -count=1
RED: tariff v1 and v2 produced the same valuation ID

go test ./internal/core/billing -run '^TestPhase9Repair3_Fixed' -count=1
RED: fixed-only unavailable and fixed-plus-missing-qualifier valuations both claimed complete
```

The minimal GREEN changes are:

- `pkg/lipsdk/economics.Rater` is restored with its post-usage,
  provider-neutral `RatingInput` contract; the enterprise fixture restores
  `_ economics.Rater = enterpriseV2Rater{}`. The obsolete forbidden-symbol and
  billing-hold inventory entries were removed while the remaining retired
  stream-time financial symbols and runtime monetary guards stay active.
- Durable observation replay compares the stored and incoming
  `ReplayFingerprint` values after identity lookup. `ReceivedAt` drift is a
  replay, the first full payload and full fingerprint remain untouched for
  audit, and a semantic mutation still returns `ErrIdentityCollision`.
- Generated valuation IDs now hash basis/input identity plus the accepted
  rater, tariff, policy and qualifier snapshot references/content hashes.
  Snapshot v1 and v2 valuations therefore occupy distinct existing durable
  `(valuation_id, valuation_version)` identities, while the same context
  replays idempotently. The later additive valuation-context migration extends
  durable input uniqueness without rewriting historical rows.
- `rateMeasures` marks the valuation partial whenever fixed-fee evaluation or
  required quantity evidence fails, preserving valid fixed lines without
  returning a payable line alongside `CompletenessComplete`.

The focused GREEN assertions passed:

```text
go test ./pkg/lipsdk/economics -run '^TestPhase9Repair3_PublicRaterContractRemainsProviderNeutral$' -count=1
go test ./internal/infra/metering/journalstore -run '^TestPhase9Repair3_DurableObservationReplayIgnoresReceiptDrift$' -count=1
go test ./internal/infra/billingstore -run '^TestPhase9Repair3_ValuationIdentityIncludesSnapshotContext$' -count=1
go test ./internal/core/billing -run '^TestPhase9Repair3_Fixed' -count=1
```

All four commands returned `ok`. Full affected packages and Phase 3 reducer
parity also passed:

```text
go test ./internal/core/metering/aggregate ./internal/core/metering/replay ./internal/core/billing ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/infra/metering/journalstore ./internal/infra/billingstore -count=1
go test ./internal/infra/billingcompose -run 'TestPhase9|Test.*Tariff|Test.*Snapshot' -count=1
make test-db-parity-sqlite
```

The touched-package vet and boundary checks passed:

```text
go vet ./internal/core/billing ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/core/metering/aggregate ./internal/core/metering/replay
go vet ./internal/infra/metering/journalstore ./internal/infra/billingstore
go vet ./internal/archtest ./internal/infra/billingcompose
go test ./internal/archtest -run '^(TestEnterpriseModulePublicOnlyCompileGate|TestForbiddenDeclarationsAbsent|TestBillingFinalConvergencePhase1BridgeForbidIsActive)$' -count=1
go test ./internal/qa -run 'Test.*(LegacyAbsent|DualPlane|ChangeSize|Forbidden|Billing)' -count=1
$goFiles = git status --short | ForEach-Object { $_.Substring(3) } | Where-Object { $_ -like '*.go' }; if ($goFiles.Count -gt 0) { gofmt -d $goFiles }
git diff --check
```

The enterprise compatibility gate, focused architecture declarations, QA
selection, all vet commands and formatting/diff checks passed. SQLite parity
passed for all registered components. At that checkpoint, the worktree
contained 28 modified or untracked Go files, below the 100-file gate; no
commit or Kiro status change was made.

Requirement mapping: public Rater restoration covers parent requirement 15.2,
task 2.4 and design C3; receipt-stable observation identity preserves the
replayable immutable evidence contract; snapshot-bound valuation identity and
durable round-trip cover the D4/D5 historical valuation contract; and truthful
fixed-line completeness closes the Phase 9 post-usage rating error-state
contract.

Reviewer notes evaluated without scope expansion: Decimal `Normalize` is
already used by tariff canonical JSON/content hashing, so equivalent decimal
spellings have one snapshot hash. The aggregate reducer’s existing fixed-point,
per-field predecessor check handles mixed usable/unusable multi-parent
corrections; an explicit mixed-parent conformance test remains a residual
risk. Caller-supplied non-empty `InputSetHash` is still accepted by direct
`ReferenceRater.Rate`; plane composition derives and overwrites the trusted
hash, but a future trusted-boundary check should reject a mismatched direct
caller hash.

Skipped checks: direct PostgreSQL parity and the full root suite were not
rerun; prior evidence records their unrelated baseline schema/ignored-import
failures. The separate `TestBillingCoreStaysProviderAndPersistenceFree`
architecture check remains red on the existing core `pkg/lipapi` import
baseline. The Windows focused race build remains unavailable because
`cgo.exe` exits with status 2. Status: READY_FOR_REVIEW.

### Fourth bounded repair (four blockers)

The fourth repair stayed within parent tasks 9.1-9.5 and refinement task 2.3.
It addressed local B-leg/component completeness, effective replay links, rule
kind inference, and durable valuation identity. The stale statements above
that described the public `economics.Rater` seam as absent were corrected to
describe its restored provider-neutral post-usage contract.

Strict-TDD RED evidence preceded the implementation:

```text
go test ./internal/core/billing -run '^TestPhase9Repair4_' -count=1
RED: a valid sibling line was lost with an unavailable B-leg; effective E/Q/P
replacements retained unresolved unknown-link diagnostics; and context-only
valuation variants reused one identity.

go test ./pkg/lipsdk/economics -run '^TestPhase9Repair4_' -count=1
RED: legacy conversion/tier material was accepted and explicit conversion
rules with contradictory direct-rate/tier fields published successfully.

go test ./internal/infra/billingstore -run '^TestPhase9Repair4_' -count=1
RED: same evidence/tariff variants conflicted on the old input-set identity
instead of allowing distinct call/period, subject, perspective and payer
contexts.
```

The minimal GREEN changes are:

- E/Q reduction now carries source issue refs through each effective
  scope/component partition. Pending, unusable and unavailable evidence makes
  only its affected line incomplete; independently complete B-legs remain
  rateable, while all canonical input observations and issue refs remain
  available for audit. P applies the same source-local filtering to reduced
  provider charges, retaining valid sibling charges and pending coverage refs.
- Effective `pendingCoverage` and `pendingSupersession` skip source and target
  observations already superseded in the effective replay state. Obsolete
  supersession/coverage links remain in retained observations for audit. The
  shuffled unknown-link/valid-replacement vectors pass for E, Q and P.
- Empty `RatingRule.Kind` no longer infers conversion from `ConversionSchema`
  or accepts stray tier material; explicit conversion rules reject
  contradictory rate, fixed, modifier and tier fields at publication, while
  a valid conversion remains explicitly unsupported by the reference evaluator
  at evaluation time.
- Valuation identity now includes basis, input observations/input-set
  identity, Scope, Perspective, Subject, Payer, snapshot identities/content
  and qualifier context. The additive `20260914000000` billing migration
  persists a context hash in durable uniqueness/lookup, leaves historical rows
  with an empty unknown context hash readable, and preserves same-context
  replay idempotency. The durable context hash also carries the
  qualifier-snapshot hash, so equal evidence with a different frozen qualifier
  selection cannot alias.

The focused GREEN and boundary commands passed:

```text
go test ./internal/core/billing -run '^TestPhase9Repair4_' -count=1
go test ./pkg/lipsdk/economics -run '^TestPhase9Repair4_' -count=1
go test ./internal/infra/billingstore -run '^TestPhase9Repair4_' -count=1
go test ./internal/core/metering/aggregate ./internal/core/metering/replay -run 'TestApplyObservations|TestPhase9' -count=1
go test ./internal/core/billing ./internal/core/metering/aggregate ./internal/core/metering/replay ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/infra/billingstore ./internal/infra/metering/journalstore -count=1
go test ./internal/infra/billingcompose -run 'TestPhase9|Test.*Tariff|Test.*Snapshot' -count=1
go test ./internal/archtest -run '^(TestEnterpriseModulePublicOnlyCompileGate|TestForbiddenDeclarationsAbsent|TestBillingFinalConvergencePhase1BridgeForbidIsActive)$' -count=1
go test ./internal/qa -run 'Test.*(LegacyAbsent|DualPlane|ChangeSize|Forbidden|Billing)' -count=1
go vet ./internal/core/billing ./internal/core/metering/aggregate ./internal/core/metering/replay ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/infra/billingstore ./internal/infra/metering/journalstore ./internal/infra/billingcompose
make test-db-parity-sqlite
gofmt -d <all touched Go files>
git diff --check
```

All returned success; SQLite parity covered billingstore and every registered
component. Direct PostgreSQL parity, the Windows race build, and the full root
suite remain skipped or baseline-limited as documented above. No commit,
rebase, merge, Kiro checkbox/status edit, or unrelated cleanup was performed.
The final status scan counted 35 modified or untracked Go files, below the
repository's 100-file source-change gate.

### Fifth bounded repair (component-local diagnostics and durable context guard)

The fifth repair stayed within parent tasks 9.1-9.5 and refinement task 2.3.
It addressed component/charge-local incompleteness when one immutable source
observation contains healthy and affected fields, and made the durable
valuation context preimage identical for in-memory identity and SQLite/
PostgreSQL persistence.

Strict-TDD RED evidence covered the review blockers:

```text
go test ./internal/core/billing -run '^TestPhase9Repair5_' -count=1
RED: authority, mixed correction and pending-coverage vectors lost healthy
siblings or poisoned the whole source observation. Pending-correction and
provider-correction sibling vectors were added alongside the same regression
coverage after the first RED run.
go test ./pkg/lipsdk/economics -run '^TestPhase9Repair5_' -count=1
RED: no canonical valuation context hash API existed for snapshot identity /
content variants.
```

The minimal GREEN changes are:

- Aggregate correction predecessor diagnostics now track each corrected
  measure key or provider charge item independently, including pending and
  unusable baselines. E/Q retain healthy sibling lines while affected lines
  remain incomplete; P filters only affected charge items and retains coverage,
  missing-observation and immutable source audit references.
- `Valuation.CanonicalContextJSON` and `Valuation.ContextHash` define one
  deterministic context preimage containing basis, trusted subject/scope,
  perspective, payer, rater/tariff/policy VersionRef IDs and versions,
  provider/policy IDs, qualifier identity, and all content refs/hashes.
  In-memory valuation IDs and durable lookup/uniqueness/storage use that same
  context hash. Historical rows retain the explicit empty legacy marker.
- Forward migration `20260914000000` recreates the SQLite trigger and
  PostgreSQL function/trigger so `valuation_context_hash` is immutable.

Focused GREEN and boundary verification:

```text
go test ./internal/core/billing -run '^TestPhase9Repair5_' -count=1 -v
go test ./pkg/lipsdk/economics ./internal/infra/billingstore -run '^TestPhase9Repair5_' -count=1 -v
go test ./internal/core/billing ./internal/core/metering/aggregate ./internal/core/metering/replay ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/infra/billingstore ./internal/infra/metering/journalstore
go vet ./internal/core/billing ./internal/core/metering/aggregate ./internal/core/metering/replay ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/infra/billingstore ./internal/infra/metering/journalstore
go test ./internal/archtest -run '^(TestEnterpriseModulePublicOnlyCompileGate|TestForbiddenDeclarationsAbsent|TestBillingFinalConvergencePhase1BridgeForbidIsActive)$' -count=1
go test ./internal/qa -run 'Test.*(LegacyAbsent|DualPlane|ChangeSize|Forbidden|Billing)' -count=1
make test-db-parity-sqlite
go test -tags integration ./internal/infra/billingstore -run '^TestPhase9Repair5_PostgresValuationContextHashIsImmutable$' -count=1 -v
gofmt -d <all touched Repair5 Go files>
git diff --check
```

All focused billing/metering/economics/billingstore tests, vet, architecture/
QA checks, SQLite parity, formatting and diff checks passed. The PostgreSQL
direct-mutation test compiled and skipped because no `LIP_TEST_POSTGRES_DSN`
was configured. Windows `-race` remains unavailable because the toolchain's
`cgo.exe` exits with status 2. The broader billingcompose suite still has the
pre-existing `TestResolveCallRatingFailoverSettlesSurfacedModelCard` failure
(`1003`, expected `1000`) in unrelated dirty changes. No commit, rebase,
merge, Kiro checkbox/status edit, or unrelated cleanup was performed.

### Sixth bounded repair (four trust/completeness blockers)

The sixth repair remained within parent tasks 9.1-9.5 and refinement task 2.3.
It closed four review blockers: transitive field-local correction
completeness, canonical input identity trust, effective-qualifier identity,
and exact rational native totals for reporting conversion. The prior fifth
repair's residual about accepting caller-supplied non-empty input hashes is
closed by the shared canonical hash verifier below.

Strict-TDD RED evidence was recorded before each minimal correction:

```text
go test ./internal/core/billing -run '^TestPhase9Repair6_' -count=1
RED: initial Repair6 tests failed to compile because the typed input-hash
sentinel and exact reporting conversion API were absent. After those APIs
were added, the unchanged-sibling regression first failed because a
superseding correction could hide an unresolved sibling field.
go test ./pkg/lipsdk/economics -run '^TestPhase9Repair6_' -count=1
RED: rational native totals had no exact reporting conversion path.
go test ./internal/infra/billingstore -run '^TestPhase9Repair6_' -count=1
RED: durable append rejected a manually constructed derived valuation with an
empty hash before it could apply the trusted canonical identity boundary.
RED: the independent-plane helper wrapped a valid hash mismatch as generic
rating-invalid text and discarded the public typed sentinel.
RED: provider charge projection treated an unrelated pending supersession as
an incomplete replacement for an otherwise known healthy charge sibling.
```

The minimal GREEN changes are:

- Aggregate correction diagnostics now propagate unresolved, unavailable and
  unknown predecessor fields to every correction descendant, including
  superseded intermediates. Every relevant known same-field parent must be
  usable, independent of correction ordering; unchanged sibling measures and
  provider charge items remain incomplete, while unrelated healthy provider
  charge siblings stay payable. Effective charges carry the same field-local
  completeness state into billing finalization.
- `economics.CanonicalInputSetHash` is the single sorted observation-ref
  preimage. Direct reference/provider raters recompute it, fill an empty
  caller value, and reject a non-empty mismatch with the typed deterministic
  error; the independent-plane helper preserves both the invalid-input and
  mismatch classifications. Durable canonicalization repeats the same
  verification/fill step so
  in-memory identity, persistence and idempotent lookup use one trusted hash,
  while legacy empty contexts remain readable.
- `EffectiveQualifiers` are immutable valuation data and are included in a
  canonical, name/value-sorted valuation context preimage. Different
  qualifier values therefore cannot alias rule selections; reordered equal
  sets remain stable, and durable lookup/uniqueness uses that same context
  hash.
- `CurrencyTotal.Validate` and `ConvertReporting` consume exact native
  numerator/denominator totals when decimal `Amount` is nil. The exact rate is
  multiplied and approved rounding is applied once, preserving typed
  overflow/precision errors.

Focused GREEN and boundary verification:

```text
go test ./internal/core/billing -run 'TestPhase9Repair6|TestPhase9Repair5|TestPhase9Repair4|TestPhase9Repair3|TestPhase9Repair2|TestPhase9ReferenceRater_|TestPhase9RateIndependent' -count=1
go test ./pkg/lipsdk/economics -count=1
go test ./internal/core/metering/aggregate ./internal/core/metering/replay ./internal/infra/metering/journalstore -count=1
go test ./internal/infra/billingstore -count=1
go test ./internal/core/billing ./pkg/lipsdk/economics ./internal/core/metering/aggregate ./internal/core/metering/replay ./internal/infra/billingstore ./internal/infra/metering/journalstore -count=1
go test ./internal/infra/billingcompose -run 'TestPhase9|Test.*Tariff|Test.*Snapshot' -count=1
make test-db-parity-sqlite
go vet ./internal/core/billing ./internal/core/metering/aggregate ./internal/core/metering/replay ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/infra/billingstore ./internal/infra/metering/journalstore ./internal/infra/billingcompose
go test ./internal/archtest -run '^(TestEnterpriseModulePublicOnlyCompileGate|TestForbiddenDeclarationsAbsent|TestBillingFinalConvergencePhase1BridgeForbidIsActive)$' -count=1
go test ./internal/qa -run 'Test.*(LegacyAbsent|DualPlane|ChangeSize|Forbidden|Billing)' -count=1
gofmt -l <all touched Go files>
git diff --check
```

All listed focused, touched-package, SQLite parity, vet, architecture/QA,
formatting and diff checks passed. The full billingcompose suite retains the
baseline `TestResolveCallRatingFailoverSettlesSurfacedModelCard` failure
(`1003`, expected `1000`); the Windows race build remains unavailable because
`cgo.exe` exits with status 2; and the direct PostgreSQL test remains skipped
without `LIP_TEST_POSTGRES_DSN`. These are residual verification limits, not
new Repair6 failures. No commit, rebase, merge, Kiro checkbox/status edit, or
unrelated cleanup was performed. The worktree remains `READY_FOR_REVIEW`.

### Seventh bounded repair (chronological field/charge taint and legacy valuation replay)

The seventh repair remained within parent tasks 9.1-9.5 and refinement task
2.3. It addressed the two remaining review blockers without changing the
immutable observation or valuation payloads.

Strict-TDD RED evidence was recorded before each minimal correction:

```text
go test ./internal/core/metering/aggregate -run TestPhase9Repair7 -count=1
RED: unresolved base -> correction -> delta and partial replacement chains
reported complete reduced measures/charges; sibling scopes were not covered
by chronology-aware taint.
go test ./internal/infra/billingstore -run TestPhase9Repair7 -count=1
RED: direct-insert valid legacy V2 rows with empty InputSetHash failed replay
and RebuildValuationProjections with identity/payload-drift errors.
```

The minimal GREEN changes are:

- Aggregate reduction computes correction-field taint without effective
  supersession filtering, propagates it through every chronological successor
  in the same source scope, and clears only when that exact field has complete
  cumulative/gauge/replacement evidence. Deltas and omitted partial-replacement
  siblings therefore remain incomplete; unrelated fields, charge items and
  source scopes remain payable independently.
- Provider-reported finalization treats incomplete reduced charge lines as a
  partial evidence result and excludes those lines from payable projection,
  while retaining healthy sibling lines and missing-observation diagnostics.
- Durable valuation replay probes the existing identity before strict hash
  verification only for the explicit legacy case (empty caller and stored
  InputSetHash). That path re-canonicalizes without filling the hash and
  compares payload/fingerprint byte-for-byte. New rows and all non-empty hashes
  retain canonical input-set verification. Projection rebuild selects the
  stored hash state and uses the same compatibility boundary without mutating
  historical payloads.

Focused GREEN and boundary verification:

```text
go test ./internal/core/billing ./internal/core/metering/aggregate ./internal/core/metering/replay ./internal/infra/billingstore ./internal/infra/metering/journalstore ./pkg/lipsdk/economics ./pkg/lipsdk/metering -count=1
go vet ./internal/core/billing ./internal/core/metering/aggregate ./internal/core/metering/replay ./pkg/lipsdk/economics ./pkg/lipsdk/metering ./internal/infra/billingstore ./internal/infra/metering/journalstore
go test ./internal/infra/billingcompose -run 'TestPhase9|Test.*Tariff|Test.*Snapshot' -count=1
make test-db-parity-sqlite
go test ./internal/archtest -run '^(TestEnterpriseModulePublicOnlyCompileGate|TestForbiddenDeclarationsAbsent|TestBillingFinalConvergencePhase1BridgeForbidIsActive)$' -count=1
go test ./internal/qa -run 'Test.*(LegacyAbsent|DualPlane|ChangeSize|Forbidden|Billing)' -count=1
go test -tags integration ./internal/infra/billingstore -run '^TestPhase9Repair5_PostgresValuationContextHashIsImmutable$' -count=1 -v
go test -race ./internal/core/metering/aggregate -run TestPhase9Repair7 -count=1
gofmt -l <all touched Repair7 Go files>
git diff --check
```

All focused touched-package tests, vet, SQLite parity, architecture/QA,
formatting and diff checks passed. PostgreSQL skipped without
`LIP_TEST_POSTGRES_DSN`; Windows race remains unavailable because the
toolchain `cgo.exe` exits with status 2. The full root suite retains unrelated
dirty-worktree failures: a malformed ignored import path, request-attempt
ratchet/baseline and line-budget failures, callback owner-reachability, and
the baseline billingcompose failover assertion (`1003`, expected `1000`). No
commit, rebase, merge, Kiro checkbox/status edit, or unrelated cleanup was
performed. The worktree is `READY_FOR_REVIEW`.

### Eighth bounded repair (complete supersession source/charge scope)

The eighth repair remained within parent tasks 9.1-9.5 and refinement task
2.3. It closed the remaining supersession blocker: a resolved correction or
replacement edge is now rejected when its complete effective source/charge
scope differs from the predecessor. The comparison includes trusted store and
lineage, stream ID, acquisition, origin, provider account/request/charge
identity, economic perspective, boundary and lifecycle, while retaining
late-reference pending semantics for unknown predecessors.

Strict-TDD RED evidence was recorded before the minimal correction:

```text
go test ./pkg/lipsdk/metering -run '^TestPhase9Repair8_' -count=1
RED: stream, acquisition, provider-account and provider-charge mismatches
were accepted as resolved supersession edges.
go test ./internal/core/metering/aggregate -run '^TestPhase9Repair8_' -count=1
RED: measure and provider-charge corrections could resolve across those
foreign scopes; same-scope sibling isolation was already the expected path.
go test ./internal/infra/metering/journalstore -run '^TestPhase9Repair8_' -count=1
RED: durable projection rebuild did not validate the loaded V2 supersession
graph and accepted a foreign stream parent.
```

The minimal GREEN changes are:

- `pkg/lipsdk/metering` validates the full effective supersession scope for
  every known reference and returns `ErrInvalidRevision` on mismatch.
- Aggregate measure and provider-charge parent resolution checks source scope
  defensively before deleting an effective predecessor; unrelated sibling
  streams remain isolated. The billing V2 charge reducer applies the same
  source-scope guard.
- Durable observation projection rebuild validates all canonical observations
  with the shared graph validator before deleting/recreating projections, so
  invalid persisted edges fail closed and leave the existing projection
  transaction uncommitted. Late same-scope references remain replayable.

Focused GREEN and boundary verification:

```text
go test ./pkg/lipsdk/metering -run '^TestPhase9Repair8_' -count=1
go test ./internal/core/metering/aggregate -run '^TestPhase9Repair8_' -count=1
go test ./internal/infra/metering/journalstore -run '^TestPhase9Repair8_' -count=1
go test ./pkg/lipsdk/metering ./internal/core/metering/aggregate ./internal/core/billing ./internal/infra/metering/journalstore -count=1
go test -shuffle=on ./pkg/lipsdk/metering ./internal/core/metering/aggregate ./internal/core/billing ./internal/infra/metering/journalstore -count=1
go vet ./pkg/lipsdk/metering ./internal/core/metering/aggregate ./internal/core/billing ./internal/infra/metering/journalstore
make test-db-parity-sqlite
go test ./internal/archtest -run '^(TestEnterpriseModulePublicOnlyCompileGate|TestForbiddenDeclarationsAbsent|TestBillingFinalConvergencePhase1BridgeForbidIsActive)$' -count=1
go test ./internal/qa -run 'Test.*(LegacyAbsent|DualPlane|ChangeSize|Forbidden|Billing)' -count=1
gofmt -l <touched Repair8 Go files>
git diff --check
```

All focused and touched-package tests, shuffle run, vet, SQLite parity,
architecture/QA, formatting and diff checks passed. The targeted race build
remains unavailable because the Windows toolchain `cgo.exe` exits with status
2. PostgreSQL direct parity was not run because no
`LIP_TEST_POSTGRES_DSN` was supplied. No commit, rebase, merge, Kiro
checkbox/status edit or unrelated cleanup was performed. The worktree is
`READY_FOR_REVIEW`.

### Final focused-repair consolidation and composition fix

The five focused repair RED records are retained above from their pre-change
runs. Their behavioral fixtures and observed failures were:

```text
go test ./internal/core/billing -run TestPhase9ReferenceRater_InclusiveCoverageAcrossObservationsIsNotDoubleCounted -count=1
RED: an unlinked aggregate and component in separate observations were
silently additive because coverage validation only compared observation IDs.

go test ./internal/core/metering/aggregate -run TestPhase9Repair7 -count=1
RED: unresolved base -> correction -> delta and partial replacement chains
reported complete reduced measures/charges; sibling scopes were not covered
by chronology-aware taint.

go test ./internal/core/billing -run '^TestPhase9Blocker3_OperatorCOGSRejectsUnlinkedAggregateComponentOverlap$' -count=1
RED: an additive aggregate/component overlap was accepted instead of returning
the typed invalid-coverage result.

go test ./internal/core/billing -run '^TestPhase9Blocker4_COGSCardinalityMatchingIsDeterministicWhenShuffled$' -count=1
RED: one-to-many/many-to-one replacement matching was not cardinality-safe
under shuffled evidence.

go test ./pkg/lipsdk/metering -run '^TestPhase9Blocker5_' -count=1
RED: equivalent Subject/Correlation carrier placement did not share the
normalized lineage/source identity used by replay and supersession.
```

The final focused GREEN rerun used the same named fixtures and current
cross-package boundaries:

```text
go test ./internal/core/metering/aggregate -run 'TestPhase9Blocker5_|TestPhase9Repair7_|TestPhase9Repair8_|TestPhase9Repair9_' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate
go test ./internal/core/billing -run 'TestPhase9Blocker2_|TestPhase9Blocker3_|TestPhase9Blocker4_|TestPhase9Blocker5_|TestPhase9Repair7_|TestPhase9Repair9_|TestPhase9ReferenceRater_InclusiveCoverageAcrossObservationsIsNotDoubleCounted' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing
go test ./pkg/lipsdk/metering ./internal/core/metering/replay ./internal/infra/metering/journalstore -run 'TestPhase9Blocker5_|TestPhase9Repair8_' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay
ok   github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore
```

The final composition RED repros were captured before the fix:

```text
go test ./internal/core/metering/aggregate -run '^TestPhase9Repair9_PartialReplacementRetainsCoverageDiagnostic$' -count=1
RED: retained aggregate total=10/0 was Complete with no pending coverage
diagnostic.
go test ./internal/core/billing -run '^TestPhase9Repair9_COGSPartialReplacementRetainsCoverageDiagnostic$' -count=1
RED: known subtotal was 13 with Completeness=known, Payable=true and no
PendingCoverage diagnostic.
```

The minimal composition fix evaluates coverage against effective charge-item
identities. Partial replacement therefore retains `total=10` and
`surcharge=3`, marks only the retained aggregate item incomplete, preserves
the unresolved child diagnostic, and makes COGS subtotal `13` partial and
non-payable. A fully replaced aggregate item remains consistent with the
existing audit-only stale-edge behavior.

```text
go test ./internal/core/metering/aggregate -run '^TestPhase9Repair9_PartialReplacementRetainsCoverageDiagnostic$' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate
go test ./internal/core/billing -run '^TestPhase9Repair9_COGSPartialReplacementRetainsCoverageDiagnostic$' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing
go test -shuffle=on ./internal/core/metering/aggregate -run 'TestPhase9Repair9_' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate
go test -shuffle=on ./internal/core/billing -run 'TestPhase9Repair9_' -count=1
ok   github.com/matdev83/go-llm-interactive-proxy/internal/core/billing
```

The affected-package verification passed for billing, aggregate, replay,
metering/economics SDK contracts, journalstore, billingstore and composition
except the existing `TestResolveCallRatingFailoverSettlesSurfacedModelCard`
baseline (`1003`, expected `1000`). Touched-package vet, targeted archtest and
QA, SQLite parity, `gofmt -l` over all dirty Go files, and `git diff --check`
all passed. Windows `-race` remains unavailable because `cgo.exe` exits with
status 2; direct PostgreSQL parity remains skipped without
`LIP_TEST_POSTGRES_DSN`; no commit or Kiro status change was made.
