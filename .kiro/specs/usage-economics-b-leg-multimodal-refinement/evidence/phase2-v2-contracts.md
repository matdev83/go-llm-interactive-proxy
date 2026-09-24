# Phase 2 V2 contracts evidence

## Status

- STATUS: READY_FOR_REVIEW
- TASK: Shared Phase 2 V2 public metering/economics contracts, parent Tasks 2.1-2.5.
- REFINEMENT: `usage-economics-b-leg-multimodal-refinement` Tasks 2.1-2.2 are included in the canonical component identity from the start.
- WORKTREE: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`
- BRANCH: `feat/b-leg-usage-economics`
- BASELINE: `d1847d5e2acdbb873779d94418b6347c92b0ab7d`
- ISSUE: `#620`

## Task brief

The phase adds provider-neutral, storage-agnostic V2 contracts only. It does
not change runtime capture, durable stores, provider adapters, rating engines,
posting, admission, or Kiro metadata. Existing V1 hashes and DTOs remain
unchanged. V1-to-V2 lifting is explicitly marked as legacy; V2-to-V1 is a
nonfinancial, one-way compatibility projection and projected facts cannot be
re-ingested as new observations.

## Requirements checked

- Parent 2.1: bounded exact `Decimal`, scientific notation parsing without
  floating point, 38 coefficient digits, scale 0-18, exact checked ledger
  nanos, fractional precision rejection and explicit presence.
- Parent 2.2: direction-sensitive canonical component keys, sorted unique
  dimensions, schema-qualified unknown components/units, native multimodal
  units and explicit aggregate/subset/partition/transform schema edges.
- Parent 2.3: V2 observation identity/revisions, local/provider/statement
  provenance, trusted correlation, tagged subject union, safe evidence,
  measure quality/presence, store-scoped charge coverage, cycle/overlap and
  supersession validation, and bounded serialized evidence.
- Parent 2.4: immutable E/Q/P/S/R valuation DTOs, exact and checked rounded
  line/total amounts, fixed-fee identity, rational-rate fields, snapshot and
  observation references, completeness, native/reporting currency separation,
  explicit conversion references, public rater/quoter/import/reconciliation
  seams, deep clone and deterministic serialization.
- Parent 2.5: legacy Fact lifting, source-event preservation, absent-versus-zero
  behavior, lossless integer token/count projection, nonrepresentable native
  measures rejected, and projection re-import fenced.
- Refinement 2.1: `FlowDirection` is `none`, `input`, or `output` and is part
  of the full key/fingerprint. Resource/account/request scope remains in the
  subject/component rather than a fake flow direction.
- Refinement 2.2: image/audio/video/document/file/tool/native units and bounded
  qualifiers round-trip without media-body retention or token coercion.

Refinement 2.3 rating execution remains intentionally deferred to the parent
rating-engine task. The checked-in multimodal vectors are used for contract
identity/round-trip validation only; the external fixture contains only
non-executing interface stubs, with no fake rating behavior or production
rating implementation.

## Design checked

Reviewed the approved parent and refinement artifacts, including parent D1-D4,
C1/C3, Additional Contract Details (identity, revisions, exact arithmetic,
public port map and external-module certification), and Migration Strategy
step 2. Reviewed refinement Parent-Spec Amendments, Refined Component Identity,
Multimodal Transform Boundaries, Economic Subject Model and Operator COGS
Selector. The refinement's `none/input/output` direction rule supersedes the
parent's illustrative resource/account direction values.

## RED phase output

Before implementing the V2 contracts, the new validation tests were run with:

```text
go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/...
```

Exit: `1` (expected RED). The compiler reported the absent contract surface,
including undefined `ParseDecimal`, `Decimal`, `Observation`,
`FlowDirection`, and `ComponentKey` in `pkg/lipsdk/metering`, and undefined
`LineItem`, `RoundingScopeLine`, `Valuation`, `BasisProviderQuantityLocal`,
`RatingInput`, `QuoteInput`, `ExposureQuote`, and `ValuationBasis` in
`pkg/lipsdk/economics`. This was the pre-implementation compiler output, not a
test-only simulated failure.

## GREEN and boundary verification

All commands below were run from the phase worktree after implementation.

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/...` | 0 | `metering` and `economics` pass (`0.313s`, `0.295s` on the final run). |
| `go test -count=1 -run '^TestPhase2V2' ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/...` | 0 | All new V2 contract tests pass. |
| `go vet ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/...` | 0 | No vet diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | External-module compile fixture passes (`3.396s`); it imports only public metering/economics packages. |
| `gofmt -d` over all phase production/tests/fixture files | 0 | No formatting diff. |
| `git diff --check` | 0 | No whitespace errors. |
| Source import guard over phase production/fixture files | 0 | No internal package, provider SDK, SQL, or HTTP imports. |

The dirty Go-file count is 18, including the pre-existing Phase 1 test files,
well below the repository limit of 100. No tracked V1 file was modified.

## Files changed by this phase

- `pkg/lipsdk/metering/decimal.go`
- `pkg/lipsdk/metering/component_key.go`
- `pkg/lipsdk/metering/observation.go`
- `pkg/lipsdk/metering/observation_compat.go`
- `pkg/lipsdk/metering/ports.go`
- `pkg/lipsdk/metering/phase2_v2_contracts_red_test.go`
- `pkg/lipsdk/economics/valuation.go`
- `pkg/lipsdk/economics/contracts.go`
- `pkg/lipsdk/economics/phase2_v2_contracts_red_test.go`
- `testdata/enterprise_module/v2_contracts.go`
- this evidence report

The existing untracked Phase 1 evidence/tests/fixtures were preserved and not
modified. No runtime, store, provider, rating-engine or monetary-writer
production path was touched.

## Concerns and deferred gates

- Runtime/store/provider-side capture and parent rating/reconciliation tasks
  still need to consume these contracts. Their absence is intentional phase
  scope, not a claim of end-to-end feature completion.
- The repository-wide Phase 1 repair suite still contains intentional RED
  defects owned by later production phases. Repository-wide unit/QA, database
  parity, `make test-cost`, race, and broad quality gates were therefore not
  used as Phase 2 completion gates. A broad exploratory root test invocation
  was stopped and is not represented as passing evidence.
- Windows cgo race is unavailable in this environment; no race result is
  claimed.

## Evidence conclusion

The public V2 contracts compile and are exercised by real package tests and an
external-module compile fixture. They preserve V1 compatibility and establish
the direction/native-unit boundary needed by the refinement. The phase is
ready for parent review and handoff to runtime/store/rating implementation
owners.

## Review repair evidence

The Phase 2 review regressions were retained and a bridge-specific regression
file was added for the rejected lineage mappings. The repair is limited to the
public V2 metering contracts and their compatibility tests:

- Built-in text/media component keys now require `input` or `output`; `none`
  remains valid for request, tool, credit, storage, and schema-qualified
  non-directional quantities.
- Identity text validation rejects malformed UTF-8 before canonical JSON
  serialization, preventing distinct invalid byte sequences from collapsing to
  the JSON replacement character.
- V1 lifting leaves `CallID` absent when only `RequestID` is proven, rejects a
  fact with no proven request/A-leg/B-leg subject instead of using `FactID`, and
  V2-to-V1 projection leaves `TraceID` absent rather than deriving it from
  `ParentWorkID`.

RED was reproduced before the production repair with:

```text
go test -count=1 -run '^TestPhase2Review' ./pkg/lipsdk/metering
FAIL: media DirectionNone, invalid UTF-8 identities, RequestID->CallID, and ParentWorkID->TraceID regressions
```

Fresh repair verification:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` | 0 | All requested metering, economics, authority, and core metering packages pass. |
| `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | No diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | External public-module fixture passes. |

The full Phase 1/feature suite remains outside this repair boundary; its three
known intentional RED tests remain owned by the Phase 1 implementation phases.

## Bounded V2 contract repair evidence

This appended repair covers the remaining Phase 2 SDK evidence, provenance and
compatibility defects identified by the full review. It does not alter the
review file or any Phase 1/V1 production implementation.

### RED

The new repair tests were first run before the corresponding fixes:

```text
go test -count=1 -run '^TestPhase2Repair' ./pkg/lipsdk/metering
FAIL: missing explicit perspective/boundary/lifecycle fields and unsafe
X-Usage-Output-Tokens evidence suffix validation

go test -count=1 -run '^TestPhase2RepairCompatibilityFailsClosedForNonRepresentableSemantics$' ./pkg/lipsdk/metering
FAIL: explicit V1 unavailable semantics had no V2 mapping and was rejected
```

These were real validation failures, not simulated assertions.

### GREEN and scope checks

- `pkg/lipsdk/metering/evidence.go` adds generic, provider-neutral economic
  path/header admission, rejects sensitive/raw paths and malformed UTF-8, and
  bounds location/lexeme text. Provider names and wire mappings remain outside
  the SDK; unknown schema roots are admitted only with an economic lexeme and
  the same sensitive-content exclusions.
- `pkg/lipsdk/metering/provenance.go` enforces B-leg ownership for public
  request/provider evidence, keeps `AttemptSeq` attached to B-leg lineage,
  separates nondirectional account gauges from resource costs, and leaves
  `ProviderChargeID` optional on B-legs.
- `pkg/lipsdk/metering/observation.go` retains explicit perspective, boundary,
  lifecycle and cloned principal scope, validates subject attempt ownership,
  and permits an explicitly unavailable empty envelope without treating it as
  an observed zero.
- `pkg/lipsdk/metering/observation_compat.go` copies proven axes/scope without
  inference, confines the historical request-level provider exception to the
  private V1 lift, preserves source-event identity, maps representable
  unavailable evidence explicitly, and rejects unsupported V1 provenance,
  reservation, gauge, statement, and contradictory unavailable projections
  instead of silently changing their semantics.
- `pkg/lipsdk/metering/phase2_v2_contracts_repair_red_test.go` covers the
  allowlist/UTF-8, explicit-axis/scope, B-leg/attempt, account/resource and
  compatibility invariants. `phase2_v2_contracts_red_test.go` helper data now
  supplies the mandatory explicit V2 axes.

The V2 JSON decoders reject malformed UTF-8 before `encoding/json` can replace
invalid bytes, including raw evidence lexemes. The legacy provider/request
exception is only restored for the explicit `legacy_v1_fact` acquisition and
mapping, while derived/configured and delegated/advisory V1 provenance fails
closed rather than being relabelled as ordinary observed evidence.

Fresh post-repair commands all passed:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 -run '^TestPhase2Repair|^TestPhase2Review' ./pkg/lipsdk/metering` | 0 | Repair and retained review regressions pass. |
| `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` | 0 | Requested focused SDK, authority and core metering suites pass. |
| `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | No diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | External public-module fixture passes. |
| `gofmt -d` over owned Go files | 0 | No formatting diff. |
| `git diff --check` | 0 | No whitespace errors. |

No tracked V1 production file, economics production file, runtime, store,
provider adapter or Phase 1 implementation was changed by this repair. Known
Phase 1 RED gates and later review repairs remain outside this bounded scope.

## Debug round 1 trust-boundary repair

This bounded repair addresses the final two Phase 2 evidence defects from the
latest review: generic JSON decoding could restore the private legacy
provider/request exception from mutable wire fields, and safe evidence
locations were admitted by word/prefix heuristics.

### RED

The replay regression was run before the repair:

```text
go test -count=1 -run '^TestPhase2ReviewLiftedObservationCanReplay$' ./pkg/lipsdk/metering
FAIL: wire-controlled legacy markers bypass B-leg ownership in a non-legacy store
```

The new assertions were then run before their production implementation:

```text
go test -count=1 -run '^TestPhase2ReviewLiftedObservationCanReplay$|^TestPhase2RepairSafeEvidenceUsesEconomicLocationsAndValidLexemes$' ./pkg/lipsdk/metering
FAIL: undefined: metering.ReadLegacyV1Observation
```

These are real failing test/compiler outputs captured before the minimal
implementation.

### GREEN and boundary verification

- Ordinary `Observation.UnmarshalJSON` now only decodes the wire envelope and
  never restores `legacyProviderRequest`.
- `ReadLegacyV1Observation` is the explicit versioned reader for a selected
  historical durable record. It checks the legacy marker, acquisition, local
  or provider origin, supported subject kind, and both `Subject.StoreID` and
  `Correlation.StoreID` values before restoring the private provider/request
  allowance and validating the observation. Its documentation makes clear
  that wire values do not authenticate provenance.
- The private ownership predicate rechecks the complete legacy tuple on every
  validation, so mutating either store identity after a trusted lift cannot
  bypass B-leg ownership.
- `evidence.go` now uses finite exact path/header allowlists. Explicit safe
  locations remain available, while `$.arbitrary.tokens`,
  `X-Usage-Anything-Bytes`, and arbitrary header suffixes/prefixes are
  rejected. Provider-family raw mapping remains adapter-owned for the later
  normalization task.
- Historical V1 source-event keys and hashes are not rewritten; no V1
  production implementation was changed.

Fresh verification:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 -run '^TestPhase2Repair|^TestPhase2Review' ./pkg/lipsdk/metering` | 0 | Replay, trusted-reader, direct-mutation, and finite-location regressions pass. |
| `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` | 0 | Focused SDK, economics, authority, and core-metering suites pass. |
| `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | No diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | External public-module compile fixture passes. |
| `gofmt -d` over owned Go files | 0 | No formatting diff. |
| `git diff --no-index --check -- /dev/null <owned-file>` over owned Go files | 0 | No whitespace errors. |

The focused race command was attempted but is unavailable in this Windows
environment because the Go 1.26 cgo compiler exits before package build;
no race result is claimed. Existing unrelated dirty Phase 1 files were
preserved. The changed production/test files are limited to
`pkg/lipsdk/metering/{evidence,observation,observation_compat,provenance}.go`
and the related Phase 2 regression tests.

## Graph and schema repair evidence

This bounded repair addresses review findings 7, 8 (metering graph only) and
12 (component schema count only). It preserves the existing V1 reader and
private trust predicates, does not modify historical V1 DTOs or hashes, and
does not add a runtime/economics implementation.

### RED

The new tests were run before the production changes with:

```text
go test -count=1 -run '^TestPhase2Repair_(ComponentSchemaRelationshipCountIsBounded|CoverageGraphRejectsDuplicateRevisionNodes|CoverageGraphPreservesLateUnresolvedReferences|SupersessionUnknownHashRequiresTrustedLegacyTuple|SupersessionRequiresExactHashForV2AndPreservesTrustedV1Hash|SupersessionRejectsMixedUnknownAndExactHashes)$' ./pkg/lipsdk/metering
```

Exit: `1` (expected RED). The compiler reported the missing explicit schema
bound: `undefined: metering.MaxComponentSchemaRelationships`.

### GREEN and scope checks

- `ComponentSchema.Validate` rejects more than 128 relationships while
  accepting the exact boundary. The bound reuses the existing 128-entry
  per-observation contract; no arbitrary graph batch total was introduced.
- V2 corrections reject `LegacyV1UnknownHash` unless the current record is an
  explicitly lifted/read trusted V1 tuple. A resolved unknown-hash reference
  additionally requires the referenced prior record to be a trusted V1
  tuple; resolved V2 references must equal the prior canonical fingerprint.
  Generic JSON decoding cannot grant either trust bit, and mixed unknown/exact
  references for one revision are rejected.
- Coverage validation rejects duplicate/conflicting store/observation/revision
  nodes and duplicate/contradictory edges, while retaining cycle and
  overlapping-inclusive-owner checks. Unresolved late cross-record coverage
  and exact supersession references remain accepted as pending at this
  refs-only validation boundary. Cycle traversal is iterative, so graph input
  does not add an unbounded recursive call stack or an invented batch limit.

Fresh verification:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 -run '^TestPhase2Repair_' ./pkg/lipsdk/metering` | 0 | New schema, graph, trust, exact-hash and pending-reference regressions pass. |
| `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` | 0 | Requested metering, economics, authority and core-metering suites pass. |
| `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | No diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | Public external-module fixture passes. |
| `gofmt -w` over owned Go files | 0 | Formatting applied; no follow-up formatting diff. |
| `git diff --check` | 0 | No whitespace errors. |

Files changed by this repair are limited to
`pkg/lipsdk/metering/{component_key,observation,observation_compat,provenance}.go`,
`pkg/lipsdk/metering/phase2_graph_repair_red_test.go`, and this evidence
append. Existing unrelated Phase 1 and prior Phase 2 files were preserved.
Known Phase 1 RED gates and later economics/reference repairs remain outside
this bounded scope.

## Economics validation repair evidence

This bounded repair addresses review repairs 4 (V2 economics UTF-8), 5
(integer token/count quantities), 8 (economics-side references and coverage),
11 (V2 money presence and quote completeness), and 12 (quote observation-ref
bound). It changes only the new V2 economics contracts in
`pkg/lipsdk/economics/{valuation,contracts}.go`, their adversarial tests, and
this evidence append. V1 money validation remains unchanged.

### RED

The new tests were written first and run before the production repair:

```text
go test -count=1 -run '^TestPhase2EconomicsRepair' ./pkg/lipsdk/economics
```

Exit: `1` (expected RED). The compiler reported the missing V2 quote bound:
`undefined: economics.MaxQuoteObservationRefs`.

### GREEN and contract checks

- V2 public-reference validation checks `utf8.ValidString` before delegating
  to the unchanged shared V1-safe-reference validator, so invalid bytes cannot
  be rewritten by JSON canonicalization into the same replacement character.
- Token/count `LineItem.Quantity` values must normalize to an exact integer;
  fractional credits, seconds, and other native units remain representable.
- Every V2 observation-reference collection keys identity by store, observation
  ID, and revision before comparing payload hashes. Exact duplicates and
  conflicting hashes are rejected. Adjustment refs are deduplicated and a
  containing valuation rejects adjustment refs from another store.
- Valuation coverage refs reject duplicate or contradictory local edges but do
  not require referenced external observations to be present. Closed-graph
  resolution remains the responsibility of the resolver that has those records;
  unresolved late refs stay pending at this DTO boundary.
- V2 valuation and exposure-quote money paths reject absent values carrying
  nonzero nanos or currency while preserving the historical `Money.Validate`
  behavior. `ExposureQuote.Completeness` must be a documented value, and
  `QuoteInput.ObservationRefs` is bounded by `MaxQuoteObservationRefs`.

Fresh verification:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 -run '^TestPhase2EconomicsRepair' ./pkg/lipsdk/economics` | 0 | All economics repair regressions pass. |
| `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` | 0 | Requested metering, economics, authority, and core-metering suites pass. |
| `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | No diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | Public external-module fixture passes (`1.031s`). |

No tracked V1 production file, metering file, runtime, store, provider adapter,
or Phase 1 implementation was changed by this repair. Known Phase 1 billing and
runtime RED gates remain outside this bounded scope.

## Snapshot, statement, and payer contract repair evidence

This bounded repair addresses the remaining Phase 2 findings 9, 10 and 13:
replayable V2 snapshot/input identity, exact statement linkage, and explicit
payer ownership. Historical `VersionRef` and `snapshot.go` contracts remain
unchanged; no provider parser, runtime binding or payer policy execution was
added.

### RED

The focused regressions were written first and run before the V2 fields and
validators were available:

```text
go test -count=1 -run '^TestPhase2Repair_(DerivedValuationRequiresReplayableSnapshotMaterialAndInputIdentity|DirectProviderAndStatementValuationsDoNotInventTariffMaterial|RatingAndQuoteRefsRequireV2MaterialForDerivedPlanes|StatementLinesResolveExactIncludedObservationChargeOrDeclareUnmatched|PayerOwnershipIsTypedAndNeverDefaultsToOperator)$' ./pkg/lipsdk/economics
```

Exit: `1` (expected RED). The compiler reported the missing V2 snapshot
content reference type/fields (`SnapshotContentRef`, snapshot `Content` and
`QualifierSnapshotRef`). No production behavior was claimed from this RED.

### GREEN and contract checks

- `SnapshotContentRef` is a bounded resolver key paired with a canonical
  lowercase SHA-256 content hash. Derived E/Q/R rating inputs, valuations,
  and derived quote envelopes require input-set identity and the immutable
  material for the snapshots they use. Qualifier material is required for
  derived contexts; malformed hashes, arbitrary qualifier text and a content
  reference that merely repeats a snapshot ID are rejected. The resolver is
  deliberately outside this DTO package.
- V2 content refs are additive `RaterContent`, `TariffContent` and
  `PolicyContent` envelope fields. The historical `VersionRef`,
  `RatingSnapshotRef`, `PolicySnapshotRef` and `snapshot.go` wire/hash behavior
  remain untouched. Direct P/S valuations can remain valid without tariff or
  policy material, so no source is invented.
- `StatementBatch.Validate` now resolves each matched line to one included
  statement observation revision and one charge item, checking store,
  provider-account, statement, period and statement-line identities. It
  rejects missing/foreign references, changed revision/hash, duplicate or
  conflicting observation/charge links, and accepts account-period records
  only through an explicit bounded unmatched outcome with a reason.
- `ReportedCharge` and `Valuation` carry a neutral typed `PaymentParty`.
  Operator, customer-BYOK, unknown and unallocated states remain distinct;
  absent payer data stays unknown and never defaults to operator. Validation
  only checks DTO shape and does not execute payer policy. Aggregate charges
  remain aggregate-only and currencies are not merged.
- The enterprise fixture references the additive snapshot, qualifier and
  payer fields through public packages only.

Fresh verification:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` | 0 | Requested metering, economics, authority and core-metering suites pass. |
| `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | No diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | External public-module fixture passes. |
| `gofmt -d` over owned Go files | 0 | No formatting diff. |
| `git diff --check` plus untracked-file whitespace checks | 0 | No whitespace errors. |

Files added or updated for this bounded repair are the new V2 economics
snapshot/statement contracts and regressions, the neutral metering payer
contract plus `ReportedCharge` field, the public enterprise fixture, and this
evidence append. No tracked V1 production file (`version.go` or `snapshot.go`),
runtime/store/provider adapter, or Phase 1 implementation was changed.

## Final residual contract repair evidence

This bounded repair closes the three residual findings from the full Phase 2
re-review. It changes only new V2 SDK contracts, their Phase 2 regressions, and
this evidence append. No tracked V1 production path or Phase 1 implementation
was changed.

### RED

The residual assertions were written first and run before the corresponding
production fixes:

```text
go test -count=1 -run '^TestPhase2FinalV2JSONDecode|^TestPhase2EconomicsRepair_UnitBound|^TestPhase2Repair_DerivedValuationRequiresReplayableSnapshotMaterialAndInputIdentity$' ./pkg/lipsdk/metering ./pkg/lipsdk/economics
```

Exit: `1` (expected RED). The run reproduced the snapshot content-ref
inequality failure, accepted fractional token/count `UnitBound` values, and
accepted malformed raw UTF-8 at the new metering and economics decode
boundaries. Existing `Observation` and `SafeEvidenceField` raw checks were
already green. The final regression table covers both `0xff` and `0xfe` bytes
across the complete boundary table.

### GREEN and scope checks

- Files changed: `pkg/lipsdk/metering/{json_v2,decimal,observation}.go`,
  `pkg/lipsdk/metering/phase2_final_contract_red_test.go`,
  `pkg/lipsdk/economics/{json_v2,contracts,snapshot_v2}.go`,
  `pkg/lipsdk/economics/{phase2_final_contract_red_test,phase2_economics_repair_red_test,phase2_snapshot_statement_payer_red_test}.go`,
  and this evidence append.
- `validateSnapshotMaterial` now accepts a content reference equal to the
  snapshot ID when the reference also carries a valid lowercase SHA-256 hash.
  Missing references, malformed hashes and invalid material still fail closed.
- `UnitBound.Validate` rejects non-integral normalized amounts for `token` and
  `count`, while fractional native units such as `second` remain supported.
- `metering/json_v2.go` provides one bounded raw-byte UTF-8 helper and
  alias-specific `UnmarshalJSON` methods for standalone V2 component,
  dimension, observation, subject, charge, measurement and payer objects.
  Existing decimal, safe-evidence and observation readers use the same gate.
- `economics/json_v2.go` applies the raw-byte gate at `RatingInput`,
  `QuoteInput`, `ExposureQuote`, `Valuation`, `StatementBatch`,
  `SnapshotContentRef`, and their public V2 nested value-object boundaries.
  Valid U+FFFD remains accepted; malformed raw bytes are rejected before
  `encoding/json` replacement.

Fresh verification:

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/...` | 0 | Full metering/economics suites pass. |
| `go test -count=1 -run '^TestPhase2Review|^TestPhase2Repair|^TestPhase2Graph|^TestPhase2EconomicsRepair|^TestPhase2V2|^TestPhase2FinalV2' ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | Retained review, repair, graph, V2 and residual tests pass. |
| `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./pkg/lipsdk/authority ./internal/core/metering/...` | 0 | Focused SDK, authority and core-metering suites pass. |
| `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics` | 0 | No diagnostics. |
| `go test -count=1 ./...` from `testdata/enterprise_module` | 0 | External public-module compile fixture passes. |

The checked contracts are parent D2-D4, requirements 2.3, 2.6, 16.2 and
17.2, and refinement requirements 1.1-1.3. Known Phase 1 billing/runtime RED
tests remain outside this SDK ownership boundary and are not represented as
passing evidence.
