# Parent Phase 12 execution evidence

Scope: parent task 12.1 (pure component-quantity reconciliation comparator),
parent task 12.2 (exact E/Q/P metering and monetary decomposition), parent task
12.3A (pure versioned tolerance policy and aggregate discrepancy projection),
parent task 12.3B (durable immutable reconciliation retention with
SQLite/PostgreSQL parity) and parent task 12.4 (operator cost selection without
erasing reconciliation state) in `internal/core/billing`,
`internal/infra/billingstore` and `internal/infra/billingcompose`. Cost-head
compare-and-swap and journal posting (13.3), statement handling (13.x),
report/HTTP/runtime wiring, and Kiro task-status changes are untouched.

Worktree: `go-llm-interactive-proxy-feat-b-leg-usage-economics`; branch
`feat/b-leg-usage-economics`; checkpoint `2301e083`. No commit was made.

## TDD and verification

- RED (compile, expected for a new domain contract):
  `go test -count=1 ./internal/core/billing -run 'TestReconciliationCompare'`
  failed with `build failed` and `undefined: ReconciliationEvidenceSet`,
  `undefined: ComponentQuantityComparison`,
  `undefined: CompareComponentQuantities`, plus the new status/reason types.
- RED (behavioral, first minimal implementation):
  `go test -count=1 ./internal/core/billing -run 'TestReconciliationCompare'`
  failed in `TestReconciliationCompareRetainsQualityAndSourceReferences`
  (expected `discrepant` with quality labels, received
  `incomparable/tokenizer_mismatch`) and in
  `TestReconciliationCompareDistinguishesMissingAndAttemptedUnknown`
  (expected `partial/value_unavailable_local`, received
  `incomparable/tokenizer_mismatch`). This exposed a real design defect: using
  per-measure `MethodRef` as the tokenizer compatibility gate would make the
  normal local-estimator vs provider-tokenizer E/Q comparison incomparable.
  The comparator was corrected so tokenizer compatibility is an explicit
  side-level effective measurement-context declaration, while per-measure
  method references remain informational labels. The behavioral tests then
  passed unchanged.
- GREEN: `go test -count=1 ./internal/core/billing -run 'TestReconciliationCompare'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationCompare'` passed.
- GREEN: `go test -count=1 ./internal/core/billing/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/...` passed.
- GREEN: `go build ./...` passed.
- GREEN: `go vet ./internal/core/billing/... ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/...` passed.
- GREEN: `gofmt -l internal/core/billing` returned nothing; `git diff --check` returned nothing.
- Architecture guard (informational, expected subset):
  `go test -count=1 ./internal/archtest -run 'Billing|UsageRecord|Phase8|UsageEconomics|Package|Boundary|Core'`
  reported three failures. All three reproduce on a pristine `git archive HEAD`
  of checkpoint `2301e083` extracted to a temp directory, so they are
  pre-existing and not introduced here:
  - `TestPackageTreeBudgetsExact/internal/infra/runtimebundle`: measured 13131, ceiling 12567 (unrelated package).
  - `TestBillingFinalConvergenceLOCRatchetActive`: pristine HEAD final LOC 30354 exceeds ceiling 29760; with this change 31072 (+718 from the new domain file), so the already-active failure widens.
  - `TestBillingCoreStaysProviderAndPersistenceFree`: billing's existing dependency tree already reaches `pkg/lipapi` at pristine HEAD.

## Task 12.1 mapping

Contract (`internal/core/billing/reconciliation_compare.go`):

- `CompareComponentQuantities(local, provider ReconciliationEvidenceSet) (ComponentQuantityComparison, error)` is a pure, context-free, persistence-free join. `ReconciliationEvidenceSet` carries the frozen comparison context (subject, payer, currency, scope, period, charge item, explicit tokenizer semantics, coverage, effective qualifiers) plus immutable `metering.Observation` evidence.
- Join key: full canonical `metering.ComponentKey` (direction, component, unit, schema, sorted dimensions) plus the side context; observations must match the side subject, so a quantity from another subject/store cannot pair.
- Arithmetic: `signed_delta = provider - local` and absolute delta are computed with the existing bounded exact `metering.Decimal` helpers (`decimalSub`, `decimalCompare` in `customer_units.go`). No float conversion, no duplicated decimal logic.
- Statuses: `matched`, `discrepant`, `partial`, `incomparable`, `missing_local`, `missing_provider`, `conflict`; typed reasons include subject/period/payer/currency/scope/charge/coverage/context/schema/qualifier/tokenizer/semantics mismatch, value unavailable per side, and duplicate/conflicting sides.
- Bounds: at most 1024 observations and 8192 measures per side, 4096 result items; exceeding a bound returns `ErrReconciliationBoundExceeded` (fail closed, no silent truncation or eager allocation).
- Determinism: evidence groups and result items are sorted by canonical key bytes; no map iteration leaks into the result.

Acceptance-coverage mapping for the task's required RED cases:

1. Full-identity join — `TestReconciliationCompareJoinsExactEconomicIdentity` (payer, effective measurement context, component qualifier mismatch; only full matches join).
2. Exact signed/absolute delta — `TestReconciliationCompareSignedAndAbsoluteDeltaIsExact` (positive, negative signed/positive absolute, exact zero, fractional native unit).
3. Typed incompatibility — `TestReconciliationCompareIncompatibleEvidenceIsTyped` (inclusion-partition schema, explicit tokenizer semantics, aggregation semantics, period, coverage, currency; each yields `incomparable`/`partial` and never matched/zero).
4. Missing vs attempted-unknown — `TestReconciliationCompareDistinguishesMissingAndAttemptedUnknown` (provider-only `missing_local`, local-only `missing_provider`, attempted-unavailable `partial/value_unavailable_local`, observed zero distinct from absent).
5. Quality labels and source refs — `TestReconciliationCompareRetainsQualityAndSourceReferences` (estimated/observed quality, method refs, full `metering.ObservationRef` on both sides).
6. Determinism, fail-closed duplicates/conflicts, bounds — `TestReconciliationCompareIsDeterministicAndFailsClosed` (order independence, duplicate vs conflicting per side, observation and result cardinality bounds, empty evidence and cross-store rejection).
7. Native units/direction — `TestReconciliationCompareKeepsUnitsAndDirectionsDistinct` (input vs output direction, token vs count, native second-unit comparison).

## Explicit exclusions

- No E/Q/P money, cost effect, residual or tolerance logic; `ComponentQuantityComparison` contains quantities and comparison status only.
- No operator selection, posting, statements, storage, migrations, SQL, HTTP or runtime composition changes.
- No changes to `pkg/lipsdk` contracts; the comparator consumes existing canonical `metering`/`economics` types only.
- No Kiro checkbox, spec-status, archive or PR operation was performed.

## Residual risks and skips

- The billing LOC ratchet was already failing at the checkpoint (pristine 30354 > 29760); the domain files raise the measured value to 31072 (12.1), 31731 (12.2), 32721 (12.3A), 33352 (12.3B) and 34052 (12.4). It is recorded rather than hidden; no budget or baseline artifact was edited.
- The repository-wide archtest run still contains the three pre-existing failures above; no new rule violation is attributable to these changes.
- `-race` was not run because the task's verification list does not require it and the comparators are pure, goroutine-free functions; the Windows race host previously failed in `cgo.exe` before test execution (see phase 11 evidence).
- Task 12.1 intentionally leaves `EconomicReconciliation.ResultJSON` and any durable projection of the comparison result to 12.2/12.3 consumers.
- Task 12.2 intentionally leaves tolerance application, signed/absolute aggregate projections and report/query surfaces to 12.3, and E/Q/P/S selection policy to 12.4.

## Task 12.2: decompose metering and monetary discrepancies

Files: `internal/core/billing/reconciliation_monetary.go` and
`internal/core/billing/reconciliation_monetary_test.go`. The 12.1 comparator
files and behavior are unchanged.

### TDD and verification

- RED (compile, expected for a new domain contract):
  `go test -count=1 ./internal/core/billing -run 'TestMonetaryDiscrepancy'`
  failed with `build failed` and `undefined: MonetaryDiscrepancyComparison`,
  `undefined: MonetaryDiscrepancyRow`, `undefined: MonetaryDiscrepancyTerm`,
  `undefined: MonetaryDiscrepancyStatus`, `undefined: DecomposeMonetaryDiscrepancies`.
- RED (fixture contract): the first run after adding the implementation failed
  because the test valuation fixtures predated the V2 `InputSetHash` and
  immutable content-reference requirements for derived valuations; fixtures were
  corrected to build genuinely valid V2 E/Q valuations rather than weakening the
  production contract.
- RED (behavioral, arithmetic direction): the next run failed with
  `metering cost effect amount = -1/1, want 1/1` and
  `metering cost effect rational = 1/12, want -1/12`, showing the formula
  operands were applied in reverse. The comparator now computes the declared
  `minuend - subtrahend` for each formula (`Q-E`, `P-Q`, `P-E`).
- GREEN: `go test -count=1 ./internal/core/billing -run 'TestMonetaryDiscrepancy'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestMonetaryDiscrepancy|TestReconciliationCompare'` passed (12.1 stays green).
- GREEN: `go test -count=1 ./internal/core/billing/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: `go build ./...` passed.
- GREEN: `go vet ./internal/core/billing/... ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: `gofmt -l internal/core/billing` returned nothing; `git diff --check` returned nothing.
- Architecture guard: the same two pre-existing failures reproduce
  (`TestBillingCoreStaysProviderAndPersistenceFree` on the existing lipapi
  dependency and the already-active LOC ratchet). No new rule violation is
  introduced; the new file only imports stdlib plus existing `economics` and
  `metering` packages.

### Task 12.2 mapping

Contract (`internal/core/billing/reconciliation_monetary.go`):

- `DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput) (MonetaryDiscrepancyComparison, error)` consumes up to three immutable `economics.Valuation` values assigned to roles by basis (`local_expected` = E, `provider_quantity_local` = Q, `provider_reported` = P) plus an optional 12.1 `ComponentQuantityComparison`.
- Per currency, exact `MeteringCostEffect = Q-E`, `ReportedPriceResidual = P-Q` and `EndToEndCostDelta = P-E` are computed as `*big.Rat` and rendered as a signed `MonetaryExactAmount`: a canonical bounded decimal when terminating, otherwise a reduced rational bounded by the existing 128-digit rule. Arithmetic reuses `boundedRat` and `decimalFromRat`; no float and no duplicated rating engine.
- Required vector: E=1.00, Q=1.10, P=1.32 yields exactly 0.10 (`1/1`), 0.22 (`22/2`) and 0.32 (`32/2`); each renders as 1/10, 11/50 and 8/25 as rationals.
- Comparability: subject, payer, coverage, per-pair shared currency, and (for the E/Q pair only) the frozen tariff/rater/policy content and measurement/scope context must agree. Any mismatch yields a typed `incomparable` term with the specific reason; disjoint currencies yield `currency_mismatch`.
- Missing roles stay `missing` with `missing_e`/`missing_q`/`missing_p`; a present valuation without an exact amount is `partial/amount_unavailable`; a valuation with non-complete completeness keeps its exact amount but is `partial/valuation_incomplete`; conflicting completeness is `incomparable/valuation_conflict`. Nothing is zero-filled.
- Attribution: a non-zero metering cost effect is labelled `quantity_difference`; a non-zero reported-price residual is labelled `suspected_pricing_difference` because no provider rate detail is present, so it is never a definitive tariff verdict. Zero deltas carry no cause.
- Preservation: all E/Q/P valuations are cloned into the result with their IDs, roles, completeness, line statuses, coverage refs and `InputObservations` source refs.
- Integration: the optional 12.1 result is deep-copied; its `conflict`, `incomparable`, `partial`/`missing_*` status downgrades the overall decomposition without rewriting the exact terms.
- Fail closed: duplicate/conflicting roles return `ErrMonetaryDiscrepancyConflict`; non-E/Q/P bases return `ErrMonetaryDiscrepancyRole`; empty, malformed, or more than three valuations return `ErrMonetaryDiscrepancyInput`; an exact difference outside the bounded contract returns `ErrMonetaryDiscrepancyOverflow`.
- Determinism and bounds: roles, currency rows and term causes are ordered deterministically; output rows are bounded by `MaxMonetaryDiscrepancyRows` (three times the canonical total bound).

### Task 12.2 acceptance coverage

- Acceptance vector: `TestMonetaryDiscrepancyDecomposesAcceptanceVector`.
- Missing/partial/zero separation: `TestMonetaryDiscrepancyKeepsMissingTermsAbsent`.
- Typed incomparability (tariff, coverage, payer, subject, currency): `TestMonetaryDiscrepancyTypedIncomparability`.
- 12.1 integration (partial, incomparable, conflict, compatible): `TestMonetaryDiscrepancyIntegratesQuantityComparison`.
- Evidence, labels and clone independence: `TestMonetaryDiscrepancyPreservesEvidenceAndPartialLabels`.
- Role conflicts, unsupported basis, bounds, malformed/empty input: `TestMonetaryDiscrepancyFailsClosedOnRoleConflictsAndBounds`.
- Exact rational retention and overflow: `TestMonetaryDiscrepancyPreservesExactRationalAndChecksOverflow`.
- Input-order determinism and sorted multi-currency rows: `TestMonetaryDiscrepancyDeterministicCurrencyRows`.

## Task 12.3A: versioned tolerance policy and aggregate discrepancy projection

Files: `internal/core/billing/reconciliation_tolerance.go`,
`internal/core/billing/reconciliation_tolerance_test.go`,
`internal/core/billing/reconciliation_aggregate.go` and
`internal/core/billing/reconciliation_aggregate_test.go`. The 12.1 and 12.2
files and behavior are unchanged; the shared status/reason vocabulary is
extended additively from the new files (`within_tolerance`, `zero_denominator`,
`unit_mismatch`, `tolerance_policy_missing`, `estimated_not_exact`).

### TDD and verification

- RED (compile, expected for a new domain contract):
  `go test -count=1 ./internal/core/billing -run 'TestReconciliationTolerance|TestReconciliationAggregate|TestReconciliationFindings'`
  failed with `build failed` and `undefined: ReconciliationTolerancePolicy`,
  `ReconciliationToleranceRule`, `ReconciliationToleranceScope`,
  `ReconciliationFinding`, `ReconciliationAggregate`,
  `AggregateReconciliationFindings`, `EvaluateReconciliationTolerance` and
  `ReconciliationFindingsFromQuantityComparison`.
- RED (behavioral, first run after implementation):
  `TestReconciliationToleranceExactFormula/negative_expected_uses_absolute_magnitude`
  failed with `threshold = 6/1, want 3/5` and
  `TestReconciliationToleranceNoMatchingRuleAndUnitMismatch/no_matching_rule_is_partial`
  failed with `signed delta = 5/1, want 1/2`. Both were wrong test
  expectations (fraction literals instead of canonical `coefficient/scale`
  strings); the production arithmetic was already exact and the assertions
  were corrected to canonical decimal strings, not the other way around.
- GREEN: `go test -count=1 ./internal/core/billing -run 'TestReconciliationTolerance|TestReconciliationAggregate|TestReconciliationFindings'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliation|TestMonetaryDiscrepancy'` passed (12.1 and 12.2 stay green).
- GREEN: `go test -count=1 ./internal/core/billing/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: `go build ./...` passed.
- GREEN: `go vet ./internal/core/billing/` passed.
- GREEN: `gofmt -l internal/core/billing` returned nothing; `git diff --check` returned nothing.
- Architecture guard: only the two pre-existing checkpoint failures reproduce
  (`TestBillingCoreStaysProviderAndPersistenceFree` on the existing lipapi
  dependency and the already-active LOC ratchet, now 32721). No new rule
  violation; the new files import stdlib plus the existing `economics` and
  `metering` packages.

### Task 12.3A mapping

Policy contract (`reconciliation_tolerance.go`):

- `ReconciliationTolerancePolicy{Version: ReconciliationTolerancePolicyV1, Ref, Rules}`; each `ReconciliationToleranceRule` has an ID, a wildcard `ReconciliationToleranceScope` (unit, currency, component, schema, context) and optional exact non-negative decimal absolute/relative limits.
- `Validate` rejects unsupported versions, missing policy/rule identities, duplicate rule IDs, duplicate scopes, any pairwise overlapping scope (no precedence is approved, so overlap is ambiguous), rules without limits, negative limits and limits outside the bounded exact decimal contract (scale/precision).
- `EvaluateReconciliationTolerance(policy, target, expected, reported)` applies `abs(P-E) <= max(absolute_limit, relative_limit*abs(E))` with `big.Rat` arithmetic. It always retains exact `SignedDelta` and `AbsoluteDelta`, reports the exact `Threshold`, and classifies `matched` (exact zero), `within_tolerance` or `discrepant`.
- When E=0 the relative difference is absent (`RelativePresent=false`, `ZeroDenominator=true`, typed `zero_denominator`) and only the absolute limit applies; a relative-only rule returns `partial/zero_denominator` without a threshold.
- No matching rule is `partial/tolerance_policy_missing`; mismatched E/P units are `incomparable/unit_mismatch`. Neither path loses the exact deltas it can compute.
- Statuses remain the shared C4 vocabulary; no selection or posting state exists in these types.

Aggregate contract (`reconciliation_aggregate.go`):

- `ReconciliationFinding` retains ID, scope, direction, unit/currency, component/schema/context, source status/reason, quality labels, exact expected/reported amounts, observation refs and valuation IDs.
- Projections: `ReconciliationFindingsFromQuantityComparison` (local = expected, provider = reported, quality labels and observation refs retained, missing sides distinct) and `ReconciliationFindingsFromMonetaryComparison` (E = expected, P = reported from the immutable 12.2 valuations; missing_e/missing_p map to `missing_local`/`missing_provider`, term incomparability is preserved, valuation IDs and source refs retained).
- `AggregateReconciliationFindings` re-evaluates comparable findings under the policy, downgrades an exact match of estimated/partial-quality evidence to `within_tolerance/estimated_not_exact`, and never promotes incomparable/missing/partial/conflict evidence to matched.
- Rows are keyed by scope/currency/unit and sorted; each row keeps gross absolute discrepancy (sum of absolute deltas, never netted), separately the discrepant-only absolute sum, the explicit signed net, affected and estimated-affected counts, deterministic status counts and sorted missing/incomparable/conflict IDs. The result retains the policy reference and every classified finding with its refs.
- Bounds: 4096 findings and 256 rows; exceeded bounds and exact-sum overflow fail closed with typed errors.

### Task 12.3A acceptance coverage

- Policy validation (duplicates, overlap, limits, precision, version): `TestReconciliationTolerancePolicyValidation`.
- Exact rule, threshold selection, retained signed/absolute deltas: `TestReconciliationToleranceExactFormula`.
- Zero denominator and relative absence: `TestReconciliationToleranceZeroDenominator`.
- Unmatched scope and unit mismatch: `TestReconciliationToleranceNoMatchingRuleAndUnitMismatch`.
- Gross never nets offsets, within-tolerance still contributes: `TestReconciliationAggregateGrossNeverNetsOffsets`.
- Estimated/incomparable never exact match, missing/incomparable/conflict IDs: `TestReconciliationAggregateEstimatedAndIncomparableNeverMatched`.
- 12.1/12.2 projection evidence retention: `TestReconciliationFindingsProjectionsPreserveEvidence`.
- Tolerance over projected 12.2 evidence: `TestReconciliationAggregateFromProjectedEvidence`.
- Malformed findings and bounds: `TestReconciliationAggregateValidatesInputsAndBounds`.
- Deterministic multi-scope/currency/unit rows: `TestReconciliationAggregateDeterministicRows`.

### Task 12.3A exclusions and residual risk

- No persistence, migration, SQL or billingstore change; tolerance policies and aggregate projections are value DTOs only.
- No operator cost selection, posting, statement or report/query surface change.
- Largest new production file is `reconciliation_aggregate.go` at 631 lines
  (DTO plus projections plus row builder); `reconciliation_tolerance.go` is
  300 lines. They are cohesive new files rather than additions to the existing
  large billing files; a future 12.3B persistence consumer can split the
  projection section if it needs a narrower dependency.

## Task 12.3B: durable immutable reconciliation retention

Files: `internal/core/billing/reconciliation_retention.go` and its test,
`internal/infra/billingstore/reconciliation_retention_store.go` and its SQLite
test, `internal/infra/billingstore/reconciliation_retention_postgres_test.go`,
`internal/infra/billingstore/20260926000000_billing_reconciliation_retention.go`,
plus additive edits to `store.go`, `20260812000000_billing_baseline.go` and
`v2_economics_store.go`. All 12.1-12.3A behavior and files are preserved.

### Design decision: extend `billing_reconciliations`, do not duplicate it

Phase 4 already created `billing_reconciliations` (canonical envelope, unique
store/id/revision, immutability triggers, subject/basis-input indexes) with
SQLite/PostgreSQL parity and dbparity registration. Task 12.3B therefore
extends that logical table instead of adding a second reconciliation store:

- Core gains the canonical full-result DTO
  `billing.ReconciliationRetentionResult` (schema versioned, validated and
  byte-canonical). It embeds the 12.1 quantity comparison, the 12.2 monetary
  decomposition including all E/Q/P valuations, the 12.3 aggregate projection,
  input hashes, valuation ids, source observation refs, coverage refs, policy
  ref, diagnostics, revision and timestamps. Exact decimals and refs are
  preserved; payloads are bounded at 1 MiB and fail closed.
- The durable envelope gains `result_schema_version` (1 = legacy single-basis
  envelope, 2 = full-result retention). The migration
  `20260926000000_billing_reconciliation_retention` adds the column and the
  `(store_id, result_schema_version, subject_kind, subject_id, created_at_unix,
  reconciliation_id, reconciliation_version, id)` read index on both dialects.
  Legacy rows default to 1; the wire JSON omits the field for version 1 so old
  canonical bytes and replay identities are unchanged.
- `AppendReconciliationRetention` maps the DTO into the existing envelope and
  reuses `AppendReconciliationInTx` for atomicity, uniqueness, replay and conflict
  handling. `GetReconciliationRetention` verifies the envelope version and
  re-parses the stored canonical bytes (`ParseReconciliationRetentionResult`
  rejects non-canonical payloads). `LatestReconciliationRetention` performs a
  required-discriminator bounded `LIMIT 1` lookup with a deterministic
  `(created_at, id, revision, row id)` total order. Existing Get/List paths were
  extended to read and expose the envelope version without changing legacy
  behavior.

### TDD and verification

- RED (compile, captured): `go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationRetention'`
  failed with `store.AppendReconciliationRetention undefined`,
  `store.GetReconciliationRetention undefined` and
  `store.LatestReconciliationRetention undefined`.
- Core retention tests were authored before the implementation, but the first
  executed run was already GREEN (implementation followed in one step); no
  separate core RED run is claimed. The store-layer compile RED above is the
  captured failing state for this task.
- GREEN: `go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationRetention'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationRetention'` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore/...` passed (legacy phase 4 reconciliation tests stay green).
- GREEN: `go test -count=1 ./internal/core/billing/...` and
  `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: `go build ./...`; `go vet ./internal/infra/billingstore/... ./internal/core/billing/...`
  and `go vet -tags=integration ./internal/infra/billingstore`; `gofmt -l` and
  `git diff --check` returned nothing.
- GREEN: `make test-db-parity-sqlite` passed for all registered components,
  including billingstore.
- GREEN: focused live PostgreSQL direct test with `LIP_REQUIRE_POSTGRES=1`
  (`go test -tags=integration -run TestReconciliationRetentionPostgresDirect ./internal/infra/billingstore`)
  passed; billingstore direct PG parity inside `make test-db-parity-postgres-direct`
  passed (`ok ... 67.547s`).
- PRE-EXISTING, unrelated: the repository-wide `make test-db-parity-postgres-direct`
  gate then failed in `internal/infra/metering/journalstore`
  (`metering_components.value_present` is `integer`/`int4` while the parity spec
  expects boolean). This diff touches no metering or journalstore file; the
  failure is in an existing component outside Task 12.3B scope.
- Architecture guard: the same checkpoint failures reproduce (runtimebundle
  package budget, billing lipapi dependency, already-active LOC ratchet now
  33352). No new rule violation.

### Task 12.3B mapping

- Immutable identity/revision: unique `(store_id, reconciliation_id,
  reconciliation_version)` with immutability triggers; revisions append and
  prior results remain byte-identical (SQLite and PG tests).
- Comparison input hashes/refs: `input_set_hash`, `local_input_hash`,
  `provider_input_hash`, retained valuation ids and observation refs.
- Scope/policy version: exact store scope with cross-store append rejected;
  `scope`, `policy_id`, `policy_version` columns plus subject projection.
- Deterministic full result: canonical, key-sorted JSON with SHA-256
  fingerprint; the stored bytes are re-verified on read and on replay, so the
  hash is an accelerator rather than the identity.
- Component findings/deltas/totals/coverage/diagnostic/result revision and
  timestamps: inside the canonical result payload, which is the approved
  canonical content for this table (design D5: "A hash is an accelerator, not a
  substitute for canonical-key comparison"); schema version and revision are
  additionally queryable columns.
- Validation/bounds before DB: DTO validation rejects missing identity/revision,
  missing evidence, malformed hashes/diagnostics, subject/policy mismatch and
  payloads above the bound; the SQLite test proves malformed results never
  reach the table.
- Queries: `GetReconciliationRetention` (one exact id/revision) and
  `LatestReconciliationRetention` (bounded latest by subject/scope/input hash,
  stable order, tenant-scoped). Existing bounded keyset `ListReconciliations`
  remains available and now returns the envelope version.
- Dual dialect: additive migration, `VerifySchema` column/index/migration
  checks on SQLite and PostgreSQL, dbparity catalog unchanged (the billing
  component is already registered and discovery is automatic).

### Task 12.3B acceptance coverage

- Append/replay/conflict/revision/payload fidelity: `TestReconciliationRetentionAppendReplayConflictAndRevisions`.
- Scope isolation, latest ordering and broad-query rejection: `TestReconciliationRetentionScopeIsolationAndLatest`.
- File restart, legacy coexistence and typed mismatch: `TestReconciliationRetentionRestartAndLegacyIsolation`.
- Malformed/bounded input never reaching the database: `TestReconciliationRetentionBoundsAndMalformed`.
- Dual-dialect schema, index, migration history and legacy default: `TestReconciliationRetentionSchemaAddsColumnIndexAndMigration` (SQLite) and `TestReconciliationRetentionPostgresDirect` (live PG).

### Task 12.3B exclusions and residual risk

- No workers, report surfaces, runtime composition or selection wiring; the
  retention API is durable storage only.
- Live PostgreSQL "restart" is represented by recreating the store over the
  same isolated database handle; a true process restart is covered by the
  file-backed SQLite test.
- The repository-wide direct PostgreSQL parity gate fails on the unrelated
  pre-existing `metering_components.value_present` type mismatch; billingstore
  direct parity is green.
- Largest new files: `reconciliation_retention.go` 414 lines,
  `reconciliation_retention_store_test.go` 297 lines,
  `reconciliation_retention_store.go` 148 lines, migration 59 lines, PG test
  67 lines. They are cohesive new files; no existing giant billingstore file was
  grown beyond small, additive edits.

## Task 12.4: select operator cost without erasing reconciliation state

Files: `internal/core/billing/operator_cost_selection.go` and its test,
`internal/infra/billingcompose/operator_cost_selection_catalog.go` and its test,
plus the minimal `SnapshotCatalog` field/constructor edit in
`internal/infra/billingcompose/catalog.go`. All 12.1-12.3B files and behavior
are preserved.

### Design/API

- `OperatorCostSelectionPolicy` is versioned (`OperatorCostSelectionPolicyV1`)
  with an immutable ref, an ordered first-match rule list and explicit
  known-zero provenance authorization. Each rule binds a basis (E/Q/P/S), a
  selection status (final or provisional), whether operator payer is required,
  whether partial evidence is allowed and whether a comparable comparison is
  required. Validation rejects malformed versions/refs, duplicate rule ids,
  unknown bases/statuses, final rules that accept partial evidence and malformed
  known-zero authorization.
- `OperatorCostSelectionInput` binds the exact subject, scope, coverage key,
  context key, native currency or explicit frozen FX basis, trusted payer class
  (operator/customer-BYOK/unallocated/unknown), provenance
  (attempted/never-started/not-billable), comparison state
  (status/complete/reconciliation id/version/fingerprint) and up to one
  candidate per E/Q/P/S basis with exact amounts, completeness, payer, refs and
  optional FX basis.
- `SelectOperatorCost` returns `OperatorCostSelectionResult` with separate
  planes: selection `Status`/`Basis`/`Reason`/`Amount`, `KnownZeroBasis`,
  `ComparisonStatus`/`ComparisonComplete`/`Reconciliation`,
  `PostingState` (unposted or pending only), preserved candidates and policy
  ref. Candidates are deep-cloned, source refs canonicalized and ordered by a
  fixed basis rank, so the result is deterministic and independent of input
  order or later caller mutation.
- Semantics: P/S require scope/coverage/context/payer/currency compatibility and
  policy permission; Q (and any other basis) may be provisional when P/S are
  absent; BYOK never becomes operator-payable; unallocated/unknown payer is
  unknown; currency mismatch without one shared frozen FX basis identity is
  incomparable; attempted/unknown work without selectable payable evidence is
  unknown, never reconciled zero; never-started/not-billable work is known_zero
  only when the policy explicitly authorizes that provenance. Comparison
  conflict fails closed. Duplicate candidate roles return
  `ErrOperatorCostSelectionConflict`.
- No cost-head compare-and-swap, journal entry, settlement or report projection
  is created or touched. `PostingState` is informational metadata; this task
  performs no posting and no head transition.

### Composition seam

`SnapshotCatalog` now freezes one explicit operator selection policy per
version: `PutOperatorCostSelectionPolicy` validates, stores a clone, treats
exact replay as idempotent and rejects changed payloads with
`ErrSnapshotImmutable`; `OperatorCostSelectionPolicy(ref)` returns a
caller-owned clone or `ErrSnapshotNotFound`. No runtime wiring, public Options,
global or DI container was added.

### TDD and verification

- RED (compile, captured): `go test -count=1 ./internal/core/billing -run 'TestOperatorCostSelection'`
  failed with undefined `OperatorCostSelectionBasis/Status`,
  `OperatorCostCandidate`, `OperatorCostSelectionRule/Policy/Input` and
  `SelectOperatorCost`.
- RED (behavioral, captured): the first implementation run failed 12 assertions.
  Two causes were found and fixed with evidence: (a) test candidate fixtures
  omitted the declared scope, which correctly failed the compatibility gate
  (fixtures corrected, not the gate), and (b) a real production defect where
  `sameOperatorCostFX(nil, nil)` returned true, letting a cross-currency
  candidate compare without any FX basis; it now requires both sides to carry
  the same explicit frozen basis, so the mismatch is
  `incomparable/currency_mismatch`.
- RED (compose compile, captured): `go test -count=1 ./internal/infra/billingcompose -run 'TestSnapshotCatalogOperatorCostSelectionPolicyFrozen'`
  failed with undefined `PutOperatorCostSelectionPolicy` and
  `OperatorCostSelectionPolicy`.
- GREEN: `go test -count=1 ./internal/core/billing -run 'TestOperatorCostSelection'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestOperatorCostSelection'` passed.
- GREEN: all Phase12 core tests (`go test -count=1 ./internal/core/billing/...`) passed.
- GREEN: `go test -count=1 ./internal/infra/billingcompose/...` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: live PostgreSQL retention regression with `LIP_REQUIRE_POSTGRES=1`
  (`go test -tags=integration -count=1 -run TestReconciliationRetentionPostgresDirect ./internal/infra/billingstore`) passed.
- GREEN: `go build ./...`; `go vet ./internal/core/billing/... ./internal/infra/billingcompose/... ./internal/infra/billingstore/...`;
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, now 34052).
  No new rule violation.

### Task 12.4 acceptance coverage

- Choose P or Q without erasure: `TestOperatorCostSelectionChoosesPWithoutErasingAlternatives`, `TestOperatorCostSelectionChoosesQProvisional`.
- Attempted missing stays unknown: `TestOperatorCostSelectionAttemptedMissingStaysUnknown`.
- Never-started known_zero only with explicit policy: `TestOperatorCostSelectionKnownZeroRequiresExplicitPolicy`.
- Payer cases: `TestOperatorCostSelectionPayerTreatment`.
- Currency and frozen FX basis: `TestOperatorCostSelectionCurrencyAndFrozenFX`.
- Incomplete/partial evidence and state separation:
  `TestOperatorCostSelectionKeepsStatesSeparate`.
- Scope/coverage/context compatibility: `TestOperatorCostSelectionCompatibility`.
- Policy version, determinism, duplicate roles, bounds, clone independence:
  `TestOperatorCostSelectionDeterminismAndFailClosed`.
- Frozen composition seam: `TestSnapshotCatalogOperatorCostSelectionPolicyFrozen`.

### Task 12.4 exclusions and residual risk

- No posting, journal, cost-head compare-and-swap, adjustment, statement,
  report, runtime or public-API change. `PostingState` remains informational
  (`unposted` for final/unknown states, `pending` for provisional selections).
- No runtime consumer is wired to the frozen catalog policy yet; Task 13.4's
  workers and Task 13.3's posting remain the consumers of this domain API.
- Largest new file is `operator_cost_selection.go` (596 lines) with a focused
  test (527 lines); the compose seam is 48 lines plus a 67-line test. Existing
  giant files were not grown beyond the small catalog field/constructor edit.

## Phase12 R1 remediation: findings 1, 2 and 9

Scope: focused remediation of the Phase12 review findings on
`internal/core/billing/operator_cost_selection.go` and its tests. No Phase13
work, no cost-head compare-and-swap, no journal posting, no git/Kiro operation.
All other Phase12 edits and reviewer evidence are preserved.

### Design choices

- **F1 payer:** a rule that requires operator payability now accepts only an
  explicit `metering.PaymentPartyOperator` on the candidate. An absent payer is
  eligible only when the rule explicitly does not require operator payability;
  explicit unknown, unallocated and customer payers are never eligible under
  any rule. Input payer classes `customer_byok`, `unallocated` and `unknown`
  fail closed before rule evaluation (`not_operator_payable`/`unknown`), so no
  rule can reinterpret them as operator payable. E/Q follow the same rule
  contract: they may be selected without a payer only under a rule that
  explicitly drops the operator-payer requirement.
- **F2 FX:** `OperatorCostFXBasis` now binds `FromCurrency`/`ToCurrency`
  (distinct, normalized), optional direction is no longer optional, positive
  bounded exact `Rate` material and ID/version. Validation rejects nil, zero,
  negative, unbounded or noncanonical rates, missing/equal directions and a
  candidate basis whose source differs from the candidate currency. The input
  basis must convert into the view currency. Cross-currency selection performs
  the smallest correct exact conversion: the view `Amount` is the native amount
  multiplied by the exact frozen rate (rational, never rounded, bounded), the
  `NativeAmount` preserves the untouched native value and `FX` retains the
  frozen basis. A missing or mismatched basis (including same identity with a
  different exact rate) makes the candidate incomparable. The result can never
  expose a view currency with an unconverted foreign amount.
- **F9 durable ref:** the durable reconciliation id/version/fingerprint
  reference is now mandatory for every selection input and is validated as a
  64-hex fingerprint. Ephemeral mode was removed rather than made explicit, so
  no final or provisional choice can be produced without a retained durable
  reference. Final selections assert the retained ref in tests.

### TDD and verification

- Test changes were authored before the implementation edits, but no separate
  pre-implementation RED execution was captured this cycle. The first run after
  the changes produced 5 failures: one design conflict (the new strict
  input-FX destination validation rejected a case the test expected to be
  incomparable; resolved by keeping strict validation and adding a separate
  candidate-side destination-mismatch incomparable case) and 4 inverted
  assertions in the new payer regression test. All were corrected without
  weakening the production contract.
- GREEN: `go test -count=1 ./internal/core/billing -run 'TestOperatorCostSelection'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestOperatorCostSelection'` passed.
- GREEN: all Phase12 core tests (`go test -count=1 ./internal/core/billing/...`) passed.
- GREEN: `go test -count=1 ./internal/infra/billingcompose/...` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: live PostgreSQL retention regression with `LIP_REQUIRE_POSTGRES=1`
  (`go test -tags=integration -count=1 -run TestReconciliationRetentionPostgresDirect ./internal/infra/billingstore`) passed.
- GREEN: `go build ./...`; `go vet ./internal/core/billing/... ./internal/infra/billingcompose/... ./internal/infra/billingstore/...`;
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, now 34135).

### R1 acceptance coverage

- Payer regressions (`TestOperatorCostSelectionRequiresExplicitOperatorPayer`):
  P and E/Q candidates with empty, explicit-unknown, unallocated and customer
  payers never select or produce an amount; explicit operator payer passes for
  P/Q/E; an explicit no-payer rule still selects an absent-payer E/Q; a
  customer payer never passes even without a payer rule.
- FX validation and exactness (`TestOperatorCostSelectionCurrencyAndFrozenFX`,
  `TestOperatorCostSelectionRejectsUnboundedFX`): shared direction and rate
  convert exactly (1.32 EUR * 0.92 = 1.2144 USD) while preserving the native
  1.32 EUR; nil/zero/negative/unbounded rates, missing/equal directions,
  reversed candidate direction, destination mismatch and same-identity
  different-rate all fail closed; caller FX mutation after selection does not
  change the result.
- Durable ref (`TestOperatorCostSelectionRequiresDurableReconciliationRef`):
  empty, partial, malformed and short fingerprints are rejected; a final
  selection retains id/version/64-hex fingerprint.

### R1 residuals

- `operator_cost_selection.go` is 677 lines and its test 790 lines; the LOC
  ratchet remains the pre-existing checkpoint failure (34135 vs pristine
  30354 already failing).
- No posting or head transition exists; `PostingState` stays informational.
- The R1 cycle did not capture a pre-implementation failing run; the honest
  first post-change failures and their resolution are recorded above.

## Phase12 R2 remediation: findings 3 and 4

Scope: focused remediation of review findings 3 (tokenizer declaration) and 4
(P-side measurement context) in `internal/core/billing/reconciliation_compare.go`,
`internal/core/billing/reconciliation_monetary.go` and their tests, plus the
billingstore retention fixture that now declares its tokenizer. No git/Kiro
operation, no Phase13, all other edits and reviewer evidence preserved.

### F3 chosen tokenizer semantics

- One-sided declaration is never compatible: a tokenizer identity present on one
  side and absent on the other returns `incomparable/tokenizer_missing` for the
  whole comparison, regardless of unit. Missing semantic identity is not
  equivalence and no mapping is invented.
- Two different declarations remain `incomparable/tokenizer_mismatch`.
- When both sides omit a declaration, the pair is still evaluated per component:
  components measured in tokens (`UnitToken`) return
  `incomparable/tokenizer_required` because the side-level declaration cannot
  prove tokenizer relevance, while native non-token units (seconds, images,
  bytes, counts) remain comparable.
- Matching declarations remain comparable. The side-level `Tokenizer` field doc
  now states these semantics; per-measure method references stay informational.
- Tokenizer applicability is item-level (the pair carries the side declaration
  through `reconciliationSource.tokenizer`), so missing-status items keep their
  existing `missing_local`/`missing_provider` semantics when the declared
  context is compatible.

### F4 context fields checked for every comparable pair

`monetaryPairContextReason` now requires, for E-Q, P-Q and P-E alike:
economic subject, payer, coverage, effective measurement context (valuation
scope, canonical effective qualifiers, qualifier snapshot and qualifier snapshot
content reference) and a shared native currency. Only the E/Q pair additionally
requires the frozen local tariff/rater/policy identity. Provider tariff/rate
detail may still differ after that identity proof, which keeps a non-zero P-Q
residual labelled `suspected_pricing_difference` rather than a definitive
tariff verdict.

### TDD and verification

- RED (compile, captured): the first post-change build failed with
  `local.tokenizer undefined (type reconciliationSource has no field or method tokenizer)`,
  showing the item-level tokenizer wiring was missing.
- RED (behavioral, captured): after the wiring fix the run failed 6 assertions.
  Five were the acceptance-vector and integration fixtures that omitted the
  provider's shared qualifier snapshot, which the stricter F4 identity check
  correctly rejected as `context_mismatch`; the fixtures were corrected to
  declare the same frozen measurement context (production was not weakened).
  The remaining failure was the missing-sharing assertion path now fixed by the
  fixture change.
- GREEN: `go test -count=1 ./internal/core/billing -run 'TestReconciliationCompare|TestMonetaryDiscrepancy'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationCompare|TestMonetaryDiscrepancy'` passed.
- GREEN: all Phase12 core tests (`go test -count=1 ./internal/core/billing/...`) passed.
- GREEN: `go test -count=1 ./internal/infra/billingcompose/...` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: live PostgreSQL retention regression with `LIP_REQUIRE_POSTGRES=1`
  (`go test -tags=integration -count=1 -run TestReconciliationRetentionPostgresDirect ./internal/infra/billingstore`) passed.
- GREEN: `go build ./...`; `go vet ./internal/core/billing/... ./internal/infra/billingcompose/... ./internal/infra/billingstore/...`;
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, now 34165).

### R2 acceptance coverage

- Tokenizer regressions (`TestReconciliationCompareRequiresTokenizerIdentity`):
  local-undeclared/provider-declared and the reverse are
  `incomparable/tokenizer_missing`; both-undeclared token units are
  `incomparable/tokenizer_required` with no deltas; different declarations have
  no invented mapping; matching declarations compare; native non-token units
  compare without declarations; one-sided declaration stays incompatible even
  for native units.
- P-context regressions (`TestMonetaryDiscrepancyPProviderMeasurementContext`):
  provider scope, effective qualifier and qualifier snapshot mismatches make
  P-Q and P-E `incomparable/context_mismatch` with no amounts while E/Q metering
  stays complete; a matching context still yields the exact 0.22 residual with
  cause `suspected_pricing_difference`; the 1.00/1.10/1.32 vector remains exact.

### R2 residuals

- The stricter tokenizer contract requires every token-measured comparison
  caller to declare tokenizer identities on both sides; tests and the
  billingstore retention fixture now do so. Non-token callers are unaffected.
- No posting or head transition exists; `PostingState` stays informational.
- The LOC ratchet remains the pre-existing checkpoint failure (34165 vs
  pristine 30354 already failing).

## Phase12 R3 remediation: findings 5, 6 and 7

Scope: focused remediation of review findings 5 (canonical retention/deep
validation), 6 (authoritative exact-money validation) and 7 (evidence scope
binding). New cohesive file `internal/core/billing/reconciliation_validation.go`;
targeted edits to `reconciliation_monetary.go`, `reconciliation_tolerance.go`,
`operator_cost_selection.go`, `reconciliation_retention.go`; new core tests and
SQLite/PG store regressions. No git/Kiro operation, no Phase13.

### F6 canonical exact-money rules

One authoritative `MonetaryExactAmount.Validate` (plus `NormalizeCanonical`)
enforces: a normalized unit key (uppercase currency code or lowercase
measurement unit, no mixed case), exactly one decimal or rational
representation, canonical bounded decimals (`metering.Decimal.Validate`),
canonical integer spelling for rational parts (no leading plus/zeros, no `-0`),
positive denominators, non-zero rationals only (zero must be the decimal `0`),
gcd-reduced parts, the existing 128-digit rational operand bound, and no
decimal/rational mixing. `Rat` validates before parsing, so unvalidated or
noncanonical input never reaches arithmetic. Ingreeses that now validate:
`newMonetaryExactAmount` (monetary decomposition intermediates), tolerance
expected/reported amounts and limits, aggregation findings, selection candidates
and FX rate material, and retention nested terms. Regressions cover 2/2,
leading plus/zeros, huge numerator/denominator, decimal+rational both/neither,
negative/zero denominator, noncanonical decimals, huge-cancel-to-small, mixed
case currency, plus canonical round-trip and lowercase unit keys.

### F5 deep retention validation and total canonical keys

`ReconciliationRetentionResult.Validate` now calls `validateRetentionNested`,
which deeply validates every quantity item/evidence/status/key/value, every
monetary valuation/row/term/amount/status/cause, every aggregate
finding/row/count/total/policy, coverage refs and diagnostics through the
existing domain validators (`economics.Valuation.Validate`,
`validateReconciliationFinding`, `MonetaryExactAmount.Validate`,
`metering.Decimal.Validate`, status/quality known sets). Duplicate quantity
items, monetary rows/roles, aggregate findings/rows, coverage edges,
observation refs and per-observation evidence conflicts are rejected. Canonical
sorting now uses total semantic keys (quantity item key+status+reason, evidence
observation+key+quality+method+value, aggregate finding scope/unit/id/status,
coverage full key, rows scope/currency/unit) instead of stable-sort ties, so
`CanonicalJSON` and the fingerprint are input-order independent for true
permutations; permutation, tied-prefix, duplicate and malformed nested
regressions were added.

### F7 scope binding

Every retained reference must belong to the parent result: top-level
observation refs, quantity evidence refs, coverage edge endpoints, all
`Monetary.ValuationEvidence` subjects and their source refs, and aggregate
finding source refs are checked against `r.Subject.StoreID` and the exact
economic subject via `sameSubject`. A foreign store, B-leg, account or subject
fails validation before canonicalization; a genuinely different subject needs a
separate retained reconciliation record. SQLite and PostgreSQL tests prove
foreign nested evidence is rejected before insert and that reads from another
store neither fetch nor discover the row.

### TDD and verification

- No separate pre-implementation RED was captured for this cycle
  (implementation and tests were authored together). The first full-package run
  surfaced two fixture issues caused by the stricter contracts:
  `TestReconciliationRetentionCanonicalPermutations/tied prefix with different
  payload` initially constructed a replacement rather than a duplicate (fixture
  corrected), and
  `TestReconciliationAggregateValidatesInputsAndBounds/missing unit key` built
  its fixture through `newMonetaryExactAmount("", ...)`, which the new contract
  correctly rejects in the helper (fixture rebuilt as a finding with no unit
  key). Production contracts were not weakened.
- GREEN: `go test -count=1 ./internal/core/billing/...` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliation|TestMonetary|TestOperatorCostSelection'` passed.
- GREEN: `go test -count=1 ./internal/infra/billingcompose/...` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: `make test-db-parity-sqlite` passed.
- GREEN: full live PostgreSQL billingstore integration suite with
  `LIP_REQUIRE_POSTGRES=1` passed (170.984s), including the retention
  cross-store and foreign-evidence regressions.
- GREEN: `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, now 34657).

### R3 acceptance coverage

- Exact-money contract and ingress rejection:
  `TestMonetaryExactAmountCanonicalContract`,
  `TestMonetaryExactAmountValidationAtIngress`.
- Deep validation, scope binding, permutations and duplicates:
  `TestReconciliationRetentionDeepValidationAndScopeBinding`,
  `TestReconciliationRetentionCanonicalPermutations`.
- Store scope: `TestReconciliationRetentionRejectsForeignEvidenceAndCrossStoreLeak`
  (SQLite) and the cross-store/foreign-nested block in
  `TestReconciliationRetentionPostgresDirect` (live PG).

### R3 residuals

- Retention payloads remain bounded at 1 MiB with the existing cardinality
  bounds; deep validation adds no I/O.
- `reconciliation_validation.go` is 388 lines; the LOC ratchet remains the
  pre-existing checkpoint failure (34657 vs pristine 30354 already failing).
- No posting or head transition exists; `PostingState` stays informational.

## Phase12 R4 remediation: finding 8 (bounded latest-retention work)

Scope: focused remediation of review finding 8 in
`internal/infra/billingstore/reconciliation_retention_store.go` and
`internal/infra/billingstore/reconciliation_retention_latest_test.go` plus the
PostgreSQL integration test. No git/Kiro operation, no Phase13, review evidence
untouched.

### Design choice: subject-scoped latest API

`LatestReconciliationRetention` is narrowed to an explicitly subject-scoped
query: `SubjectKind` and `SubjectID` are required and `TenantID` is an optional
refinement of the same subject partition. The previously supported `scope`- and
`input-set-hash`-only predicates are removed. Rationale against
requirements/design: the approved selective reconciliation surface is the
bounded keyset `ListReconciliations` query with its own indexed filters; the
latest read is a convenience whose approved economic scope is the
subject/charge, and the retention index is subject-leading
`(store_id, result_schema_version, subject_kind, subject_id, created_at_unix,
reconciliation_id, reconciliation_version, id)`. Restricting the API to the
index prefix removes every predicate shape that could scan unrelated rows, with
no approved caller requiring scope/hash-only latest reads. Composite identity
kinds remain queryable by their projected subject id; no new index or migration
is required, so dual-dialect parity is unchanged.

### Identity validation before SQL

The query is validated before any SQL runs: subject kind and id are required
(absent subject returns `ErrQueryTooBroad`), an unknown kind returns
`ErrReconciliationRetentionQuery`, and a B-leg subject probe reuses the
authoritative `metering.SubjectRef.Validate` to bound the subject id and tenant
with exactly the same `MaxSchemaIDBytes`/UTF-8/printable/no-surrounding-
whitespace rules the stored subject fields were validated with. No string is
silently trimmed. Overlong, whitespace-padded, control-character and malformed
identities are rejected.

### Bounded-work proof

- SQLite `EXPLAIN QUERY PLAN` assertions for both supported shapes require a
  `SEARCH` through `idx_billing_reconciliations_retention_scope` and fail on a
  full table scan, with 2000 legacy decoy rows present; the functional latest
  read ignores the decoys and returns the valid retention row.
- PostgreSQL `EXPLAIN (FORMAT JSON)` runs after `ANALYZE` inside a
  `SET LOCAL enable_seqscan = off` transaction (deterministic index-path proof
  rather than planner cost heuristics) and requires the retention index and an
  index scan with no `Seq Scan`, with 1000 decoys seeded in one statement.
- High-cardinality stable-order tests (5000 SQLite legacy decoys, 1000 PG
  decoys) prove the deterministic tie-break `created_at DESC, reconciliation_id
  DESC, reconciliation_version DESC, id DESC` including id ties, revision ties
  and newer timestamps, plus tenant-refinement isolation.

### TDD and verification

- RED (compile, captured): `go test -count=1 ./internal/infra/billingstore -run 'TestLatestReconciliationRetention'`
  failed with `undefined: ErrReconciliationRetentionQuery`,
  `undefined: latestRetentionSubjectSQL` and
  `undefined: latestRetentionSubjectTenantSQL`.
- GREEN: `go test -count=1 ./internal/infra/billingstore -run 'TestLatestReconciliationRetention'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestLatestReconciliationRetention|TestReconciliationRetention'` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore/...` passed.
- GREEN: `go test -count=1 ./internal/core/billing/...` and
  `go test -count=1 ./internal/infra/billingcompose/...` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/...` passed.
- GREEN: `make test-db-parity-sqlite` passed.
- GREEN: full live PostgreSQL billingstore integration suite with
  `LIP_REQUIRE_POSTGRES=1` passed (182.605s), including
  `TestLatestReconciliationRetentionPostgresPlanAndOrder`.
- GREEN: `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the two pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, now 34653).

### R4 residuals

- Legacy `subject_id` projection semantics are not re-derived per kind; the
  query validates identity bounds and filters the exact projection columns.
  This matches the existing list/get projection contract.
- Latest performs one indexed identity lookup plus one unique-key read of the
  immutable row; both are bounded.
- No posting or head transition exists; `PostingState` stays informational.

## Phase12 R5A remediation: blocker 1 (schema-1 legacy wire/fingerprint drift)

Scope: fix the `result_schema_version` wire drift for pre-migration schema-1
reconciliation rows in `internal/infra/billingstore/v2_economics_store.go`,
with frozen checkpoint fixtures and SQLite/PostgreSQL compatibility tests. No
git/Kiro operation, no Phase13, R5B/R5C untouched, review evidence untouched.

### Root cause

The 12.3B envelope version field was emitted on the canonical wire for every
record because `normalized()` maps the zero value to schema 1; at checkpoint
2301e083 `reconciliationWire` had no such field. An unchanged pre-migration row
therefore recomputed different canonical bytes and fingerprint, turning exact
replay into `ErrIdentityConflict` and changing `CanonicalJSON()` for reads.

### Compatibility design

- `reconciliationWireSchemaVersion` emits the field only for the full-result
  retention generation (`>= ReconciliationRecordSchemaRetention` = 2). Legacy
  schema-1 records serialize `result_schema_version` nowhere, so their bytes are
  byte-identical to the checkpoint serializer.
- Internally legacy records keep `ResultSchemaVersion = 1` (matching the
  `result_schema_version` column default and existing rows), but the wire and
  therefore the fingerprint stay pre-migration exact.
- `normalized()` and `decodeCanonicalReconciliation` keep normalizing old,
  field-less wires to the legacy envelope without rewriting bytes; `Get`/`List`
  still expose the envelope version from the column but their canonical bytes
  and fingerprints match the stored pre-migration values.
- Schema 2 (`ReconciliationRecordSchemaRetention`) keeps emitting
  `"result_schema_version":2` and is unchanged and separated.

### Frozen checkpoint fixture

Derived by extracting `git archive 2301e083` into a temporary directory and
running the checkpoint serializer for a fixed record, then frozen as literals:

- canonical bytes: `{"id":"legacy-schema1-fixture","version":1,"subject":{"kind":"b_leg","store_id":"test","tenant_id":"tenant-test","a_leg_id":"a-legacy","billing_call_id":"call-legacy","b_leg_id":"b-legacy"},"scope":"call","basis":"provider_reported","input_set_hash":"aaaa...","local_input_hash":"bbbb...","provider_input_hash":"cccc...","policy_id":"policy-legacy","policy_version":"v1","result":{"a":1,"z":2},"created_at":"2023-11-14T22:21:40Z"}`
- fingerprint: `241bd07977e9b3aa35ffd0b9db3dc058d208b99ecd191cd28fbd06247f0ad970`

### TDD and verification

- RED (captured): `go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationSchema1'`
  failed with the diff `+ "result_schema_version":1` inserted after `basis` in
  the schema-1 canonical bytes, exactly the reviewer's drift.
- GREEN: `go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationSchema1'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationSchema1|TestReconciliationRetention|TestReconciliationSchema'` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore/...` passed.
- GREEN: `go test -count=1 ./internal/core/billing/...` and
  `go test -count=1 ./internal/infra/billingcompose/...` passed.
- GREEN: `make test-db-parity-sqlite` passed.
- GREEN: full live PostgreSQL billingstore integration suite with
  `LIP_REQUIRE_POSTGRES=1` passed (194.012s), including
  `TestReconciliationSchema1PostgresFixtureReplay`.
- GREEN: `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the two pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, 34663).

### Compatibility coverage

- `TestReconciliationSchema1CanonicalBytesAndReplayIdentity` (SQLite): frozen
  bytes/fingerprint equality, absence of the new field, raw pre-migration row
  read preserves stored bytes/fingerprint, exact replay is idempotent, changed
  payload still conflicts, fresh append stores the frozen bytes/fingerprint.
- `TestReconciliationSchema1RestartAndSchema2Separation` (file-backed SQLite):
  bytes/fingerprint survive close/reopen, replay after restart is idempotent,
  and schema-2 records still emit `"result_schema_version":2`.
- `TestReconciliationSchema1PostgresFixtureReplay` (live PG): raw pre-migration
  row replays without conflict, read/reopen preserve the frozen
  bytes/fingerprint, and a changed payload conflicts.

### R5A residuals

- Legacy internal value 1 vs wire-absent is an intentional version-aware
  mapping; schema-1 canonical identity is byte-frozen by the fixture.
- R5B (schema-2 validation gaps) and R5C are untouched.
- No posting or head transition exists; `PostingState` stays informational.

## Phase12 R5B remediation: reviewer HIGH 2 and schema-2 poison rows

Scope: close reviewer HIGH 2 (retention semantic consistency gaps) and the
generic schema-2 poison-row residual in
`internal/core/billing/reconciliation_validation.go`,
`reconciliation_monetary.go`, `reconciliation_compare.go`,
`reconciliation_retention_test.go` and
`internal/infra/billingstore/v2_economics_store.go` with SQLite/PostgreSQL
durable-path tests. No git/Kiro operation, no Phase13, R5C and review evidence
untouched.

### RED

`go test -count=1 ./internal/core/billing -run 'TestReconciliationRetentionDerives|TestReconciliationRetentionRequiresExact|TestReconciliationRetentionObservationIdentity|TestReconciliationRetentionMonetaryStatus'`
failed with 13 discriminating failures, including: false matched/complete
quantity labels accepted; unknown quantity/monetary status and reason accepted;
incomparable item under a discrepant top level accepted; evidence schema and
qualifier mismatches accepted; differing payload-hash duplicates accepted; and
false monetary conflict/partial/complete labels accepted.

### Invariant/guard design

- Status/reason vocabulary is authoritative: `MonetaryDiscrepancyStatus.IsKnown`
  and `MonetaryDiscrepancyReason.IsKnown` (new) plus
  `ReconciliationComparisonReason.IsKnown` (new) list every supported value.
- Derived, not trusted: the producer rollup was extracted into
  `monetaryDiscrepancyRollup` and is now shared by `DecomposeMonetaryDiscrepancies`
  and retention validation; quantity `Status`/`Complete` are derived with the
  existing `summarizeComponentQuantityComparison`. Stored labels that disagree
  with the derived rollup are rejected, and complete/comparable quantity results
  may not carry a reason.
- Nested evidence must equal the parent `ComponentKey` exactly via
  `ComponentKey.Equal` (direction, component, unit, schema, sorted qualifiers),
  not merely the unit.
- Observation identity excludes the payload hash: duplicate
  store/observation/revision with a differing hash is a conflict and with an
  identical hash a duplicate; both are rejected for top-level refs, quantity
  evidence and coverage edges (contradictory coverage relation conflicts).
- Generic `AppendReconciliation` now validates every schema-2 payload through
  `billing.ParseReconciliationRetentionResult` before insert, so the specialized
  reader and the durable writer accept exactly the same values. Schema-1 byte
  compatibility from R5A is preserved unchanged.

### Files changed

`internal/core/billing/reconciliation_validation.go` (deep checks, identity
keys, key equality, derived rollups), `reconciliation_monetary.go`
(IsKnown + rollup extraction), `reconciliation_compare.go` (IsKnown),
`reconciliation_retention_test.go` (fixtures recompute derived rollups),
new `reconciliation_retention_consistency_test.go` (180 lines of discriminating
tests), `internal/infra/billingstore/v2_economics_store.go` (schema-2 append
guard), SQLite/PG durable-path rejection tests.

### Verification

- GREEN: focused core consistency tests passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate'` passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationRetention|TestReconciliationSchema1|TestLatestReconciliationRetention'` passed.
- GREEN: full `./internal/core/billing/...`, `./internal/infra/billingstore/...`,
  `./internal/infra/billingcompose/...` and SDK packages passed.
- GREEN: `make test-db-parity-sqlite` passed.
- GREEN: full live PostgreSQL billingstore integration with
  `LIP_REQUIRE_POSTGRES=1` passed (194.327s), including the schema-2 poison
  rejection subtest.
- GREEN: `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the two pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, 34766).

### R5B residuals

- Durable-path poison tests were authored after the guard implementation; the
  captured RED is the core consistency suite (13 failures) above.
- Aggregate finding reason vocabulary and R5C scope are untouched; no posting or
  head transition exists.

## Phase12 R5C remediation: reviewer HIGH 3 (tenant-refined latest was post-index filtered)

Scope: make the tenant-refined `LatestReconciliationRetention` bounded by an
index condition in `internal/infra/billingstore` with a dual-dialect additive
migration and SQLite/PostgreSQL plan proofs. No git/Kiro operation, no Phase13,
review evidence untouched.

### RED

`go test -count=1 ./internal/infra/billingstore -run 'TestLatestReconciliationRetention|TestReconciliationRetentionSchema'`
failed to compile with
`undefined: billingReconciliationRetentionTenantIndex` and
`undefined: BillingReconciliationRetentionTenantIndexMigrationName`. The new
plan assertions require the tenant shape to name a tenant-bearing index and
carry `tenant_id` as an index constraint, which the subject-leading
`idx_billing_reconciliations_retention_scope` could not satisfy.

### Design choice and plan proof

Kept tenant refinement and added the smallest exact-matching index instead of
removing it: the stored subject id is not unique across tenants, so a
subject-only latest could not disambiguate same-id subjects from different
tenants, and tenant isolation is a first-class requirement. The additive
index-only migration `20260927000000_billing_reconciliation_retention_tenant_index`
creates `idx_billing_reconciliations_retention_tenant_scope` on
`(store_id, result_schema_version, subject_kind, subject_id, tenant_id,
created_at_unix, reconciliation_id, reconciliation_version, id)` on SQLite and
PostgreSQL. The subject-only shape keeps using
`idx_billing_reconciliations_retention_scope`; the tenant shape uses the new
index; the deterministic tie-break is unchanged.

- SQLite `EXPLAIN QUERY PLAN` for the tenant shape must name the tenant index
  and carry `tenant_id=?` as an index constraint with an indexed range search.
- PostgreSQL `EXPLAIN (FORMAT JSON)` inside an ANALYZEd
  `SET LOCAL enable_seqscan = off` transaction must show an Index Scan/Index
  Only Scan on the tenant index whose `Index Cond` contains `tenant_id`, and no
  `tenant_id` in any `Filter` and no `Seq Scan`.
- High-cardinality isolation: 3000 legacy decoy rows plus two tenants sharing
  one subject id (tenant B newer); each tenant query returns its own newest row
  and a missing tenant returns not found.

### Files changed

New `20260927000000_billing_reconciliation_retention_tenant_index.go`;
`20260812000000_billing_baseline.go` registration; `store.go`
(`RequiredMigrationNames`, SQLite index list, PostgreSQL migration/index schema
checks); `reconciliation_retention_latest_test.go` (tenant plan constraint,
tenant isolation and 3000-row decoy coverage);
`reconciliation_retention_store_test.go` (tenant index + migration assertions);
`reconciliation_retention_postgres_test.go` (recursive plan-tree Index Cond
assertion, tenant index expectation, live tenant isolation).

### Verification

- GREEN: focused SQLite latest/retention/schema tests passed.
- GREEN: `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestLatestReconciliationRetention|TestReconciliationRetention|TestReconciliationSchema1'` passed.
- GREEN: full `./internal/infra/billingstore/...`, `./internal/core/billing/...`
  and `./internal/infra/billingcompose/...` passed.
- GREEN: `make test-db-parity-sqlite` passed.
- GREEN: focused live PostgreSQL retention integration with
  `LIP_REQUIRE_POSTGRES=1` passed; full live PostgreSQL billingstore
  integration suite with `LIP_REQUIRE_POSTGRES=1` passed (199.438s), including
  the tenant plan and isolation tests.
- GREEN: `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l` and `git diff --check` returned nothing.
- Architecture guard: only the two pre-existing checkpoint failures reproduce
  (billing lipapi dependency and the already-active LOC ratchet, 34768).

### R5C residuals

- The subject-only latest shape intentionally returns the newest row across
  tenants for that subject id; callers needing tenant isolation pass the now
  index-backed `TenantID` refinement.
- No posting or head transition exists; R5A/R5B behavior is unchanged.

## Phase12 UNIT1 debug retry: retained quantity exact-delta rederivation (review HIGH 3)

Scope: close only review HIGH 3 (retained quantity items did not revalidate the
exact signed/absolute delta or the one-source-per-side comparable shape) in
`internal/core/billing/reconciliation_compare.go`,
`internal/core/billing/reconciliation_validation.go` and the
SQLite/PostgreSQL durable mutation tests. Reviewer findings 1, 2, 4 and 5 (and
units 2-5) are untouched; all prior Phase12 edits, both evidence files,
migrations, tenant/index behavior and Kiro status are preserved. No
git/Kiro/PR operation was performed.

### RED (exact captured output)

Core, before the fix
(`go test -count=1 ./internal/core/billing -run 'TestReconciliationRetentionRederivesQuantityDeltasAndSourceShape' -v`):
7 discriminating failures, each
`reconciliation_retention_consistency_test.go:243: Validate error = <nil>, want ErrInvalidReconciliationRetention`
for wrong signed delta, wrong absolute delta, wrong signed delta sign, nil local
value, nil provider value, second local source and second provider source; the
valid-producer control passed.

SQLite durable, before the fix
(`go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationRetentionRejectsForgedQuantityDeltasDurably' -v`):

- `wrong_signed_delta_specialized_append`: expected
  `billing: invalid reconciliation retention result`, got nil.
- `wrong_signed_delta_generic_schema-2_append`: expected
  `billingstore: invalid reconciliation`, got nil.
- `second_provider_source_specialized_append`: expected
  `billing: invalid reconciliation retention result`, got nil.
- `second_provider_source_generic_schema-2_append`: expected
  `billingstore: invalid reconciliation`, got nil.
- Post-run assertion: `Should be zero, but was 4` — all four byte-canonical
  forged payloads had been persisted. The forged payloads are produced by
  decoding a valid canonical result and mutating one quantity item, so only
  semantic revalidation can reject them; they are not malformed-JSON cases.

PostgreSQL durable, before the fix
(`$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration ./internal/infra/billingstore -run '^TestReconciliationRetentionPostgresDirect$' -count=1 -v`):

- `generic_schema-2_forged_delta_rejected`: expected
  `billingstore: invalid reconciliation`, got nil.
- `specialized_append_rejects_forged_absolute_delta`: expected
  `billing: invalid reconciliation retention result`, got nil.

### Design and helper reuse

- The authoritative arithmetic was extracted from the live
  `compareReconciliationPair` comparable branch into
  `reconciliationExactDelta(local, provider metering.Decimal) (signed, absolute
  metering.Decimal, comparison int, err error)` in `reconciliation_compare.go`.
  It keeps the existing bounded exact `decimalSub`/`decimalCompare` helpers and
  error wrapping; the live comparator now calls it, so no arithmetic semantics
  are duplicated between production and retention validation.
- `validateRetentionQuantityComparison` (R3 validation file) now, for
  `matched`/`discrepant` items only: requires exactly one local and one provider
  source; requires both retained evidence values to be present; rederives the
  signed/absolute deltas with `reconciliationExactDelta`; requires the retained
  deltas to equal the rederived values exactly (`metering.Decimal.Equal`); and
  requires the item status to match the rederived delta sign (`matched` iff
  zero, else `discrepant`).
- `partial`, `incomparable`, `conflict` and
  `missing_local`/`missing_provider` shapes and the
  `summarizeComponentQuantityComparison` top-level rollup are unchanged;
  conflict items that legitimately retain multiple sources per side still
  validate. The valid-producer control and all existing 12.1-12.4 suites pass
  unchanged.
- Durable proofs exercise both the specialized
  (`AppendReconciliationRetention`) and the raw generic schema-2
  (`AppendReconciliation` with a crafted `ReconciliationRecord`) paths. The
  generic path canonicalizes the supplied JSON before the existing R5B
  `ParseReconciliationRetentionResult` guard, so forged bytes reach the same
  semantic validator on append and on replay.

### Files changed (this unit only)

- `internal/core/billing/reconciliation_compare.go` (768 -> 780 lines): shared
  `reconciliationExactDelta`; live pair delegates to it.
- `internal/core/billing/reconciliation_validation.go` (439 -> 463 lines):
  comparable-item rederivation and source-shape block.
- `internal/core/billing/reconciliation_retention_consistency_test.go`
  (207 -> 395 lines): 7 mutations through Validate/CanonicalJSON/Parse plus the
  valid control and a canonical-bytes forge helper.
- `internal/infra/billingstore/reconciliation_retention_store_test.go`
  (404 -> 524 lines): canonical forge helpers plus the SQLite
  specialized/generic mutation and no-insert proof.
- `internal/infra/billingstore/reconciliation_retention_postgres_test.go`
  (225 -> 254 lines): live PG generic/specialized mutation subtests.

### Verification (fresh, shared dirty worktree)

- GREEN focused core mutation suite and SQLite durable mutation suite after the
  fix; PG mutation subtests re-run GREEN on the restored fix bytes.
- GREEN `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate|TestOperatorCostSelection'`.
- GREEN `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationRetention|TestReconciliationSchema1|TestLatestReconciliationRetention'` (18.593s).
- GREEN full affected packages: `./internal/core/billing/...`,
  `./internal/infra/billingstore/...` (32.225s),
  `./internal/infra/billingcompose/...`, `./pkg/lipsdk/economics/...`,
  `./pkg/lipsdk/metering/...`.
- GREEN `make test-db-parity-sqlite` (all registered components; billingstore
  16.168s).
- GREEN focused live PostgreSQL retention mutation with `LIP_REQUIRE_POSTGRES=1`:
  `TestReconciliationRetentionPostgresDirect` (forged delta, second source,
  poison and valid paths), `TestReconciliationSchema1PostgresFixtureReplay` and
  `TestLatestReconciliationRetentionPostgresPlanAndOrder` (29.931s).
- GREEN `go build ./...`; `go vet ./internal/core/billing/...
  ./internal/infra/billingstore/... ./internal/infra/billingcompose/...` and
  `go vet -tags=integration ./internal/infra/billingstore`;
  `gofmt -l internal/core/billing internal/infra/billingstore
  internal/infra/billingcompose` and `git diff --check` returned nothing.
- Schema-1 wire/fingerprint compatibility and R5C tenant latest/plan tests stay
  green in the SQLite and PostgreSQL runs above.

### UNIT1 residuals

- Only review HIGH 3 is closed. Reviewer findings 1, 2, 4 and 5 remain open.
- A schema-2 row persisted by an earlier build through the unguarded generic
  append with a semantically forged quantity delta would now fail closed on
  parse/read instead of being returned. Stored bytes and fingerprints are never
  rewritten (Requirement 17.2); valid producer rows and schema-1 records are
  unaffected.
- The change is validation-only: no migration, no new API, no posting, no
  journal, no Kiro/git operation. Dirty Go file count is 29, under the
  100-file gate.

## Phase12 UNIT2 debug retry: retained monetary invariant rederivation (review BLOCKER 2)

Scope: close only review BLOCKER 2 (retained monetary terms accepted forged exact
amounts and invalid status/reason/cause combinations) in
`internal/core/billing/reconciliation_validation.go`, the retention test
fixtures and the SQLite/PostgreSQL durable mutation tests. Unit1 quantity
rederivation, schema1 byte compatibility, R5B/R5C behavior and reviewer findings
1, 4 and 5 (units 3-5) are untouched; both evidence files are preserved. No
git/Kiro/PR operation was performed.

### RED (exact captured output)

Core, before the fix
(`go test -count=1 ./internal/core/billing -run 'TestReconciliationRetentionRederivesMonetaryTerms' -v`):
12 discriminating failures, each
`reconciliation_retention_consistency_test.go:669: Validate error = <nil>, want ErrInvalidReconciliationRetention`,
for wrong Q-E amount, wrong P-Q amount, wrong P-E amount, complete term with a
reason, complete term with an underived cause, missing term without a reason,
missing term with an unknown reason, incomparable term with an unknown reason,
partial term without a reason, retained valuation total changed under copied
terms, row currency not derived from the retained valuations, and no retained
valuation evidence. The four producer-derived positive controls (complete,
missing_p, incomparable multi-currency, partial/valuation_incomplete with an
amount and cause) all passed.

SQLite durable, before the fix
(`go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationRetentionRejectsForgedMonetaryTermsDurably' -v`):

- `wrong_complete_metering_amount_specialized_append` and
  `..._generic_schema-2_append`: expected
  `billing: invalid reconciliation retention result` /
  `billingstore: invalid reconciliation`, got nil.
- `missing_term_without_a_reason_specialized_append` and
  `..._generic_schema-2_append`: got nil.
- `incomparable_term_with_an_unknown_reason_specialized_append` and
  `..._generic_schema-2_append`: got nil.
- Post-run assertion: `Should be zero, but was 6` — all six byte-canonical
  forged payloads had been persisted. Forged bytes come from JSON surgery on the
  valid canonical result.

PostgreSQL durable, before the fix
(`$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration ./internal/infra/billingstore -run '^TestReconciliationRetentionPostgresDirect$' -count=1 -v`):

- `generic_schema-2_forged_monetary_term_rejected`: expected
  `billingstore: invalid reconciliation`, got nil.
- `specialized_append_rejects_forged_monetary_term`: expected
  `billing: invalid reconciliation retention result`, got nil.

### Authoritative rederivation design

- `validateRetentionMonetaryTerm` now enforces the deliberate vocabulary and
  combinations: status and cause stay enumerated, `Reason.IsKnown()` replaces the
  former bounded-identity-only check, complete terms require an amount and no
  reason, partial terms require an explicit reason, and missing/incomparable
  terms require an explicit reason and carry no amount and no cause.
- `validateRetentionMonetaryRederivation` reconstructs
  `MonetaryDiscrepancyInput` from the retained E/Q/P `Valuations` plus the
  retained optional `Quantity` comparison, re-runs the authoritative
  `DecomposeMonetaryDiscrepancies`, and requires semantic equality: matching
  top-level status/reason, the same row currency set, and every
  Q-E/P-Q/P-E term with identical status, reason and cause. Amounts are compared
  by currency and exact rational value (`MonetaryExactAmount.Rat`), so a decimal
  and a rational spelling of the same exact value are accepted; representation
  equality is deliberately not required.
- No arithmetic or producer semantics are duplicated: the rederivation uses
  `monetaryTotalRat`, `newMonetaryExactAmount`, the `monetaryFormula` term
  construction and `monetaryDiscrepancyRollup` through the public
  `DecomposeMonetaryDiscrepancies` entry point.
- Valid incomplete/incomparable producer output is preserved and covered by
  positive controls: missing_p (two E/Q valuations), incomparable
  multi-currency (two rows), and partial/valuation_incomplete with a retained
  amount and `quantity_difference` cause all validate, canonicalize and parse.
- The `TestReconciliationRetentionCanonicalPermutations` fixture now derives its
  second (EUR) row by adding a EUR total to the retained Q valuation and
  re-running the producer, instead of appending an invented underived partial
  row; the permutation identity assertion is unchanged and still passes.

### Files changed (this unit only)

- `internal/core/billing/reconciliation_validation.go` (463 -> 552 lines):
  tightened term vocabulary/combination checks plus
  `validateRetentionMonetaryRederivation` and `monetaryTermMatches`.
- `internal/core/billing/reconciliation_retention_consistency_test.go`
  (395 -> 762 lines): 12 bypass mutations through Validate/CanonicalJSON/Parse,
  four producer-derived positive controls and the shared canonical forge helper.
- `internal/core/billing/reconciliation_retention_test.go` (454 -> 466 lines):
  producer-derived EUR row in the canonical permutation fixture.
- `internal/infra/billingstore/reconciliation_retention_store_test.go`
  (524 -> 631 lines): generic canonical forge helper plus the SQLite
  specialized/generic monetary mutation and no-insert proof.
- `internal/infra/billingstore/reconciliation_retention_postgres_test.go`
  (254 -> 297 lines): live PG generic/specialized monetary mutation subtests.

### Verification (fresh, shared dirty worktree)

- GREEN focused core monetary mutation suite and SQLite durable mutation suite
  after the fix; PG monetary mutation subtests re-run GREEN.
- GREEN `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate|TestOperatorCostSelection'`.
- GREEN `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationRetention|TestReconciliationSchema1|TestLatestReconciliationRetention'` (22.710s).
- GREEN full affected packages: `./internal/core/billing/...`,
  `./internal/infra/billingstore/...` (50.212s),
  `./internal/infra/billingcompose/...`, `./pkg/lipsdk/economics/...`,
  `./pkg/lipsdk/metering/...`.
- GREEN `make test-db-parity-sqlite` (all registered components; billingstore
  16.047s).
- GREEN focused live PostgreSQL with `LIP_REQUIRE_POSTGRES=1`:
  `TestReconciliationRetentionPostgresDirect` (including both new monetary
  mutation subtests), `TestReconciliationSchema1PostgresFixtureReplay` and
  `TestLatestReconciliationRetentionPostgresPlanAndOrder` (29.134s).
- GREEN `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l internal/core/billing internal/infra/billingstore
  internal/infra/billingcompose` and `git diff --check` returned nothing.
- Unit1 quantity mutation suites, schema1 compatibility and R5C tenant
  latest/plan tests stay green in the runs above.

### UNIT2 residuals

- Only review BLOCKER 2 is closed. Reviewer findings 1, 4 and 5 remain open.
- A schema-2 row persisted by an earlier build with a forged complete amount or
  invalid term vocabulary now fails closed on parse/read. Stored bytes and
  fingerprints are never rewritten (Requirement 17.2); valid producer rows,
  unit1 quantity rederivation and schema-1 records are unaffected.
- Rederivation is pure in-memory domain work: no migration, no new API, no
  posting, no journal, no Kiro/git operation. Dirty Go file count is 29, under
  the 100-file gate.

## Phase12 UNIT3 debug retry: retained aggregate/tolerance/diagnostic invariants (review HIGH 4)

Scope: close only review HIGH 4 (retained aggregate findings, tolerance
evaluations and diagnostics accepted unknown vocabularies and forged derived
state) in `internal/core/billing/reconciliation_aggregate.go`,
`reconciliation_tolerance.go`, `reconciliation_validation.go`,
`reconciliation_retention.go` and the SQLite/PostgreSQL durable mutation tests.
Unit1 quantity and Unit2 monetary rederivation, schema1 byte compatibility,
R5B/R5C behavior and reviewer findings 1 and 5 (units 4-5) are untouched; both
evidence files are preserved. No git/Kiro/PR operation was performed.

### RED (exact captured output)

Core, before the fix
(`go test -count=1 ./internal/core/billing -run 'TestReconciliationRetentionRederivesAggregateToleranceAndDiagnostics' -v`):
29 discriminating failures, each
`reconciliation_retention_consistency_test.go:1182: Validate error = <nil>, want ErrInvalidReconciliationRetention`:
unknown finding reason; unknown and empty evaluated status; unknown evaluation
reason; non-comparable finding with an evaluation; non-comparable finding with a
mismatched evaluated label; evaluation policy id/version mismatch; target
mismatch; expected/reported/signed/absolute/relative amount mismatch; relative
presence lie; zero-denominator lie; threshold mismatch; within-tolerance lie;
status and reason mismatch; estimated quality keeping an exact match; row gross,
discrepant, net, affected-count, status-count and missing-ID mismatches; extra
underived row; unknown diagnostic reason. The eight producer-derived positive
controls (within_tolerance, matched, monetary missing_p, monetary incomparable,
tolerance_policy_missing, relative-only zero denominator, absolute-limit zero
denominator, monetary diagnostic reason) all passed.

SQLite durable, before the fix
(`go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationRetentionRejectsForgedAggregateTermsDurably' -v`):

- forged evaluation signed delta, unknown evaluated status and forged row gross
  absolute total, each through the specialized and the generic schema-2 append:
  expected `billing: invalid reconciliation retention result` /
  `billingstore: invalid reconciliation`, got nil.
- Post-run assertion: `Should be zero, but was 6` — all six byte-canonical
  forged payloads had been persisted.

PostgreSQL durable, before the fix
(`$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration ./internal/infra/billingstore -run '^TestReconciliationRetentionPostgresDirect$' -count=1 -v`):

- `generic_schema-2_forged_evaluation_delta_rejected`: expected
  `billingstore: invalid reconciliation`, got nil.
- `specialized_append_rejects_forged_aggregate_row_total`: expected
  `billing: invalid reconciliation retention result`, got nil.

### Vocabulary and rederivation design

- Deliberate documented union: `reconciliationFindingReasonKnown`
  (`reconciliation_aggregate.go`) accepts a finding/diagnostic reason when it is
  in the Task 12.1 comparison vocabulary or the Task 12.2 monetary vocabulary,
  because `ReconciliationFindingsFromMonetaryComparison` preserves the typed
  monetary reason (for example `missing_p`) in the shared field. Both member
  vocabularies keep their own `IsKnown` gate, so no unenumerated value passes.
  It gates `finding.Reason`, `finding.EvaluationReason` and
  `diagnostic.Reason`; the diagnostic `Code` stays a bounded identity.
- `validateRetentionFindingClassification` validates the finding/evaluated
  status vocabularies and then requires: non-comparable findings to carry no
  evaluation and evaluated labels equal to their status/reason; comparable
  findings without both exact amounts to be `partial/tolerance_policy_missing`
  with no evaluation; comparable findings with both amounts to carry an
  evaluation.
- `validateRetentionToleranceEvaluation` rederives the nested evaluation from
  retained material only: policy `VersionRef` binding to the aggregate policy,
  target equality with the finding identity, expected/reported exact-value
  equality, signed/absolute deltas recomputed with the authoritative
  `toleranceExactAmount` helper, zero-denominator flag, no-matching-rule branch
  invariants (no limits/threshold/relative/within state,
  `partial/tolerance_policy_missing`), exact non-negative limits via the shared
  `validateToleranceLimit` (extracted from rule validation), threshold
  recomputed as `max(absolute, relative*|E|)`, relative difference/presence,
  and the status/reason/within-tolerance classification including the estimated
  exact-match downgrade to `within_tolerance/estimated_not_exact`. Amounts are
  compared as rationals, so representation differences that preserve the exact
  value are accepted.
- Rows are rebuilt with the existing `buildReconciliationAggregateRows` over the
  retained findings and compared semantically: row identity set, exact totals,
  affected/estimated counts, status counts and missing/incomparable/conflict ID
  sets. No aggregation logic is duplicated.
- Validity controls cover producer output with `missing_p`,
  `context_mismatch`, `tolerance_policy_missing`, relative-only and
  absolute-limit zero denominators, exact matched evidence and estimated
  downgrades.

### Documented boundary

`ReconciliationAggregate` retains only the policy `VersionRef`, not the frozen
rules. Rule selection (which rule matched the target) and limit provenance
(whether the retained exact limits actually belong to the retained policy
version) therefore cannot be verified from retained material; they are
documented as a boundary rather than reconstructed by inventing a policy
snapshot or silently expanding the retention schema. Everything derivable from
the retained finding, evaluation and limits is validated, and a future schema
version that retains the policy rules could close this boundary.

### Files changed (this unit only)

- `internal/core/billing/reconciliation_aggregate.go` (671 -> 682 lines): the
  documented finding/diagnostic reason union.
- `internal/core/billing/reconciliation_tolerance.go` (331 -> 340 lines):
  `validateToleranceLimit` extracted for reuse by rule and evaluation
  validation.
- `internal/core/billing/reconciliation_validation.go` (552 -> 852 lines):
  finding classification and tolerance-evaluation rederivation, row rebuild
  comparison and helpers.
- `internal/core/billing/reconciliation_retention.go` (435 lines): diagnostic
  reason now gated by the deliberate union.
- `internal/core/billing/reconciliation_retention_consistency_test.go`
  (762 -> 1314 lines): 29 bypass mutations through Validate/CanonicalJSON/Parse
  plus the eight producer-derived controls.
- `internal/core/billing/reconciliation_retention_test.go` (466 -> 471 lines):
  permutation fixture rows rebuilt from its findings.
- `internal/infra/billingstore/reconciliation_retention_store_test.go`
  (631 -> 780 lines): aggregate-with-evaluation fixture plus the SQLite
  specialized/generic mutation and no-insert proof.
- `internal/infra/billingstore/reconciliation_retention_postgres_test.go`
  (297 -> 341 lines): live PG evaluation/row mutation subtests.

### Verification (fresh, shared dirty worktree)

- GREEN focused core aggregate mutation suite, SQLite durable mutation suite
  and PG mutation subtests after the fix.
- GREEN `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate|TestOperatorCostSelection'`.
- GREEN `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationRetention|TestReconciliationSchema1|TestLatestReconciliationRetention'` (20.114s).
- GREEN full affected packages: `./internal/core/billing/...`,
  `./internal/infra/billingstore/...` (48.533s),
  `./internal/infra/billingcompose/...`, `./pkg/lipsdk/economics/...`,
  `./pkg/lipsdk/metering/...`.
- GREEN `make test-db-parity-sqlite` (all registered components; billingstore
  16.037s).
- GREEN focused live PostgreSQL with `LIP_REQUIRE_POSTGRES=1`:
  `TestReconciliationRetentionPostgresDirect` (including both new aggregate
  mutation subtests), `TestReconciliationSchema1PostgresFixtureReplay` and
  `TestLatestReconciliationRetentionPostgresPlanAndOrder` (29.340s).
- GREEN `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l internal/core/billing internal/infra/billingstore
  internal/infra/billingcompose` and `git diff --check` returned nothing.
- Unit1/Unit2 mutation suites, schema1 compatibility and R5C tenant
  latest/plan tests stay green in the runs above.

### UNIT3 residuals and boundary

- Only review HIGH 4 is closed. Reviewer findings 1 and 5 remain open.
- Rule selection and exact-limit provenance relative to the retained policy
  version cannot be verified while the retention schema stores only the policy
  `VersionRef`; documented above and in code rather than papered over with an
  invented resolver.
- A schema-2 row persisted by an earlier build with unknown vocabularies or
  forged aggregate/evaluation state now fails closed on parse/read. Stored bytes
  and fingerprints are never rewritten (Requirement 17.2); valid producer rows,
  unit1/unit2 rederivation and schema-1 records are unaffected.
- The change is validation-only: no migration, no new API, no posting, no
  journal, no Kiro/git operation. Dirty Go file count is 29, under the
  100-file gate.

## Phase12 UNIT4 debug retry: same-side source identity semantics (review MEDIUM 5)

Scope: close only review MEDIUM 5 (same-side duplicate/conflict classification
ignored observation semantics) in
`internal/core/billing/reconciliation_compare.go` and focused tests. Unit1-3
rederivation, schema1 byte compatibility, R5B/R5C behavior and reviewer findings
1 and 5 (unit 5) are untouched; both evidence files are preserved. No
git/Kiro/PR operation was performed.

### RED (exact captured output)

Core, before the fix
(`go test -count=1 ./internal/core/billing -run 'TestReconciliationCompareSameSideSemanticsIdentity|TestReconciliationRetentionPreservesProducerConflictReason' -v`):

- `local semantics-only difference is conflicting`:
  `reconciliation_compare_test.go:812: reason = "duplicate_local", want conflicting_local`.
- `provider semantics-only difference is conflicting`:
  `reconciliation_compare_test.go:834: reason = "duplicate_provider", want conflicting_provider`.
- `TestReconciliationRetentionPreservesProducerConflictReason`:
  `producer comparison = [... Reason:duplicate_local ...], want one
  conflicting_local item` for a delta/cumulative local pair with identical
  value, quality and method.
- Controls passed before the fix: identical-semantics local and provider
  duplicates remain `duplicate_local`/`duplicate_provider`, and a quality-only
  difference remains `conflicting_local`.

### Identity design

- `reconciliationSourcePayload` now includes `source.semantics` and the
  side-level `source.tokenizer` in addition to value, quality and method, so two
  same-side sources identical in value/quality/method but differing only in
  valid observation semantics are classified `conflicting_local` /
  `conflicting_provider`, not duplicates. The tokenizer is constant for every
  source of one side and is included so the payload names the full declared
  measurement identity; it cannot change same-side classification.
- Cross-side comparison is unchanged: a semantics mismatch still yields
  `incomparable/semantics_mismatch`, and conflict items keep both sources with
  no deltas.
- The retained quantity DTO omits observation semantics by design. The
  canonical retention regression therefore proves the producer-classified
  `conflicting_local` reason, both retained sources and the absence of deltas
  survive `CanonicalJSON`/`ParseReconciliationRetentionResult`; it does not and
  cannot reconstruct an omitted semantic from retained material.

### Files changed (this unit only)

- `internal/core/billing/reconciliation_compare.go` (780 -> 783 lines):
  `reconciliationSourcePayload` includes semantics and tokenizer.
- `internal/core/billing/reconciliation_compare_test.go` (848 -> 954 lines):
  five focused same-side identity subtests (local/provider semantics conflict,
  local/provider duplicate controls, quality-only conflict control).
- `internal/core/billing/reconciliation_retention_consistency_test.go`
  (1314 -> 1369 lines): canonical retention preservation of the producer
  conflict reason.

### Verification (fresh, shared dirty worktree)

- GREEN focused same-side identity and retention-preservation tests after the
  fix.
- GREEN `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate|TestOperatorCostSelection'` (1.301s).
- GREEN `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationRetention|TestReconciliationSchema1|TestLatestReconciliationRetention'` (23.508s).
- GREEN full affected packages: `./internal/core/billing/...`,
  `./internal/infra/billingstore/...` (42.294s),
  `./internal/infra/billingcompose/...`, `./pkg/lipsdk/economics/...`,
  `./pkg/lipsdk/metering/...`.
- `make test-db-parity-sqlite` and focused live PostgreSQL were not re-run:
  this unit does not touch a store path, migration, index or durable test
  fixture, so there is no dialect-parity delta.
- GREEN `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l internal/core/billing internal/infra/billingstore
  internal/infra/billingcompose` and `git diff --check` returned nothing.

### UNIT4 residuals

- Only review MEDIUM 5 is closed. Reviewer finding 5 remains open.
- Same-side classification now includes the full declared measurement payload;
  a semantics-only pair is `conflicting_*` while true duplicates remain
  `duplicate_*`.
- The change is classification-only: no migration, no new API, no persistence
  schema change, no posting, no journal, no Kiro/git operation. Dirty Go file
  count is 29, under the 100-file gate.

## Phase12 UNIT5 debug retry: schema-2 outer envelope binding (review BLOCKER 1)

Scope: close only review BLOCKER 1 (generic schema-2 append accepted a valid
canonical inner result wrapped in an unrelated outer envelope, and readers did
not bind the envelope to the inner result) in
`internal/infra/billingstore/reconciliation_retention_store.go`,
`v2_economics_store.go` and the SQLite/PostgreSQL durable tests. Units1-4
invariant suites, R5A schema-1 wire/fingerprint compatibility and R5C tenant
index/plan are preserved; both evidence files are untouched. No git/Kiro/PR
operation was performed.

### RED (exact captured output)

SQLite, before the fix
(`go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationRetentionEnvelopeBinding' -v`):

- 12 independent outer mutations of the authoritative
  `ReconciliationRecordFromRetention` mapping (reconciliation id, revision,
  subject account/b-leg/tenant, scope, input-set/local/provider hashes, policy
  id, policy version, creation time) each returned nil and inserted the row.
- Subject store mutation was rejected only by the pre-existing store-scope check
  (`billingstore: economics record out of scope`), not by any envelope binding.
- `mismatch is rejected before replay lookup`: got
  `billingstore: idempotency identity conflict` instead of
  `ErrReconciliationRetentionMismatch`, proving the replay lookup ran before any
  envelope comparison.
- `canonical wire outer mismatch` raw row: `GetReconciliation` returned the row
  with nil error.
- `projection column mismatch` raw row: `GetReconciliationRetention` returned
  the inner result with nil error and `LatestReconciliationRetention` could
  return it through a forged tenant projection.
- Controls passed: a foreign subject kind was rejected by subject validation,
  and the authoritative mapping stayed appendable/readable.

PostgreSQL, before the fix
(`$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration ./internal/infra/billingstore -run '^TestReconciliationRetentionPostgresDirect$' -count=1 -v`,
captured by temporarily no-oping the two binding helpers and restoring the fix
byte-identically with matching SHA-256 file hashes afterwards):

- `generic schema-2 envelope mismatch rejected`:
  `reconciliation_retention_postgres_test.go:237: Expected error with
  "billingstore: invalid reconciliation" in chain but got nil.`
- `raw wire mismatch fails closed`:
  `reconciliation_retention_postgres_test.go:255: Expected error with
  "billingstore: reconciliation retention record mismatch" in chain but got
  nil.`
- `projection column mismatch fails closed`:
  `reconciliation_retention_postgres_test.go:268: Expected error with
  "billingstore: reconciliation retention record mismatch" in chain but got
  nil.`

All other PG subtests (Units1-4 poison/forgery cases) stayed green in that RED
run, confirming the new failures are attributable to the missing binding.

### Envelope binding design and field map

- `bindReconciliationRetentionEnvelope(record, result)` is the single
  outer-to-derived-inner comparison. It requires schema 2 and rejects any
  mismatch in: reconciliation id, revision, full subject (compared through its
  canonical JSON encoding, covering kind, store, tenant, account, A-leg,
  billing call, B-leg, attempt and period/window fields), scope, input-set hash,
  local input hash, provider input hash, policy id, policy version and creation
  instant. It never rewrites caller outer values; the authoritative
  `ReconciliationRecordFromRetention` remains the only inner-to-outer mapping.
- `bindReconciliationStoredProjection(record, projection)` compares the decoded
  canonical envelope with the stored projection columns (`subject_kind`,
  `subject_id`, `subject_json`, `tenant_id`, `scope`, `basis`, all input hashes,
  policy id/version, `created_at_unix`), so subject/tenant projections and
  latest lookups cannot return a row the envelope does not own.
- Write chokepoint: `AppendReconciliationInTx` parses the inner result, binds
  the envelope, and only then performs the replay identity lookup or insert, so
  a mismatched envelope is rejected before replay/conflict handling and no
  projection row can be written.
- Read chokepoints: `decodeCanonicalReconciliation` (used by both
  `GetReconciliation` and `ListReconciliations`) parses and binds the inner
  result; `GetReconciliation`/`ListReconciliations` additionally bind the
  decoded envelope to the stored projection columns for schema-2 rows; and
  `reconciliationRetentionFromRecord` re-binds before returning the
  specialized retention result.
- Schema-1 path untouched: no new wire field, no change to
  `reconciliationWire`/`normalized`/`decodeCanonicalReconciliation` behavior
  for schema 1, and the new bindings run only when the envelope is schema 2.
  R5A frozen bytes/fingerprint/read/replay/restart tests stay green.

### Files changed (this unit only)

- `internal/infra/billingstore/reconciliation_retention_store.go`
  (155 -> 254 lines): envelope binding, subject equality, stored-projection
  binding and the specialized-reader bind.
- `internal/infra/billingstore/v2_economics_store.go` (1423 -> 1462 lines):
  write-path bind before replay/insert, decode-path bind, and projection binds
  in Get/List.
- `internal/infra/billingstore/reconciliation_envelope_binding_test.go` (new,
  217 lines): 12 outer-field mutations, named-store subject mismatch,
  before-replay proof, authoritative control, and raw wire/projection mismatch
  rows with get/retention/latest/restart fail-closed assertions.
- `internal/infra/billingstore/reconciliation_retention_postgres_test.go`
  (341 -> 384 lines): live PG generic envelope mismatch, raw wire mismatch and
  projection column mismatch subtests.

### Verification (fresh, shared dirty worktree)

- GREEN focused SQLite envelope binding suite; live PG envelope subtests GREEN.
- GREEN `go test -count=5 -shuffle=on ./internal/core/billing -run 'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate|TestOperatorCostSelection'` (1.192s).
- GREEN `go test -count=5 -shuffle=on ./internal/infra/billingstore -run 'TestReconciliationRetention|TestReconciliationSchema1|TestLatestReconciliationRetention|TestReconciliationEnvelopeBinding'` (32.092s).
- GREEN full affected packages: `./internal/core/billing/...`,
  `./internal/infra/billingstore/...` (47.393s),
  `./internal/infra/billingcompose/...`, `./pkg/lipsdk/economics/...`,
  `./pkg/lipsdk/metering/...` (includes Unit1-4 invariant suites).
- GREEN `make test-db-parity-sqlite` (all registered components; billingstore
  16.086s).
- GREEN focused live PostgreSQL with `LIP_REQUIRE_POSTGRES=1`:
  `TestReconciliationRetentionPostgresDirect` (all 10 subtests including the
  three new envelope ones), `TestReconciliationSchema1PostgresFixtureReplay`
  and `TestLatestReconciliationRetentionPostgresPlanAndOrder` (29.280s).
- GREEN `go build ./...`; `go vet` (including `-tags=integration`);
  `gofmt -l internal/core/billing internal/infra/billingstore
  internal/infra/billingcompose` and `git diff --check` returned nothing.

### UNIT5 residuals

- Only review BLOCKER 1 is closed by this unit. Reviewer BLOCKER 2, HIGH 3,
  HIGH 4 and MEDIUM 5 were closed by Units 2, 1, 3 and 4 respectively; the
  reviewer's original finding 5 (deep full-result validation and total
  canonical ordering) is the aggregate of those units plus this binding and the
  review verdict is left to the reviewer.
- A schema-2 row persisted by an earlier build through the unguarded generic
  append with a mismatched envelope now fails closed on
  get/retention/list/latest instead of being returned or repaired; stored bytes
  and fingerprints are never rewritten (Requirement 17.2), and schema-1
  compatibility is byte-frozen and untouched.
- The change is store-boundary validation only: no migration, no schema change,
  no new API, no posting, no journal, no Kiro/git operation. Dirty Go file count
  is 30, under the 100-file gate.

## Phase12 RR1 debug retry: durable schema-generation and replay/projection binding (review BLOCKER P12-RR1)

Scope: close only review BLOCKER P12-RR1 (decoded wire generation overwritten
by the SQL discriminator before validation, and replay accepting rows through
`result_json`/empty-fingerprint shortcuts across generations) in
`internal/infra/billingstore/v2_economics_store.go`,
`internal/infra/billingstore/reconciliation_retention_store.go` and the
SQLite/PostgreSQL durable tests. Units 1-5, R5A frozen schema-1 bytes, R5B
controls and R5C tenant index/plan are preserved. Review BLOCKER P12-RR2 is
deliberately untouched. Both evidence files are preserved; only this execution
file was appended. No git/Kiro/PR operation was performed.

### ROOT_CAUSE (confirmed by RED)

- `decodeCanonicalReconciliation` derives the generation from the wire
  (field-less wire normalizes to legacy 1), but `GetReconciliation` and
  `ListReconciliations` overwrote `record.ResultSchemaVersion` with the SQL
  `result_schema_version` column before any comparison, then conditionally
  bound the projection. A schema-1 wire with a valid legacy basis and a valid
  inner full result, marked generation 2 with matching projection columns, was
  returned as a schema-2 retention result; the inverse mutation returned a
  schema-2 wire as generation 1 and skipped the schema-2 projection checks.
- The canonical-empty fallback (pre-migration rows without `canonical_json`)
  synthesized whatever generation the column claimed, so a generation-2 row
  with no canonical bytes returned a "schema-2" record and its inner result
  from projections alone.
- Replay read only `id, reconciliation_id, reconciliation_version,
  canonical_json, result_json, fingerprint`. `resolveReconciliationReplay`
  accepted `(stored canonical bytes equal || result_json equal) &&
  (fingerprint empty || fingerprint equal)`. A pre-existing row with altered
  canonical outer data, the same `result_json` and an empty fingerprint
  replayed as schema 2; an exact-bytes schema-2 row with a forged projection
  column and an empty fingerprint also replayed; generation was never
  compared. The `result_schema_version` column is not covered by the
  immutability trigger, so raw rows can forge it.

### RED (exact captured output)

SQLite generation cross-binding and replay poison, before the fix
(`go test -count=1 ./internal/infra/billingstore -run 'TestReconciliationGenerationBinding' -v`;
captured in `rr1-red-sqlite.txt`, replay re-captured after fixing two test
setup defects — the poison row must reuse the incoming `result_json`, and the
frozen fixture needed a distinct ID — in `rr1-red-replay.txt`):

- `fieldless schema-1 wire stored as generation 2`: generic get, specialized
  retention, latest, list and restart each
  `Expected error with "billingstore: reconciliation retention record mismatch" in chain but got nil`
  (`a schema-1 wire must not be returned as generation 2`, `...satisfy a
  schema-2 retention read`, `...listed as generation 2`).
- `explicit schema-2 wire stored as generation 1`: generic get, list and
  restart `...but got nil` (`a schema-2 wire must not be returned as generation
  1`, `...listed as generation 1`); specialized retention already failed
  closed and latest correctly did not discover the row (controls passed).
- `canonical-empty row may only be legacy generation`: generic get, specialized
  retention and latest `...but got nil` (`schema 2 must never be synthesized
  from projections without canonical bytes`).
- `canonical-empty legacy row stays readable and replayable`: explicit zero
  discriminator read returned `actual : 0x0` where `expected: 0x1`
  (`an explicit zero discriminator normalizes to the legacy generation`).
- Replay poison, five independent nil-error failures:
  `matching result_json plus an empty fingerprint must not satisfy a schema-2
  replay`; `a schema-2 row whose projection disagrees with its envelope must
  not replay`; `a schema-1 wire marked generation 2 must not satisfy schema-2
  replay`; `a canonical-empty row marked generation 2 must not replay as
  legacy`; `the frozen schema-1 fixture must not replay through a forged
  generation 2 discriminator`. The exact schema-2 replay and canonical-empty
  legacy replay controls passed before the fix.

PostgreSQL, before the fix
(`$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -count=1
./internal/infra/billingstore -run 'TestReconciliationRetentionPostgresDirect|TestReconciliationSchema1PostgresFixtureReplay|TestReconciliationSchema1PostgresForgedGenerationFailsClosed' -v`,
captured in `rr1-red-pg.txt`):

- `wire column generation mutation fails closed`:
  `Expected error with "billingstore: reconciliation retention record mismatch"
  in chain but got nil.` for direction A; the first direction-B generic get
  also returned nil.
- `existing-row replay poison fails closed`:
  `Expected error with "billingstore: idempotency identity conflict" in chain
  but got nil.` (`matching result_json plus an empty fingerprint must not
  satisfy replay`).
- `TestReconciliationSchema1PostgresForgedGenerationFailsClosed`:
  `Expected error with "billingstore: reconciliation retention record mismatch"
  in chain but got nil.` (`a schema-1 wire must not be returned as generation
  2`).
- All pre-existing PG subtests (Units 1-4 poisons, envelope, projection, plan,
  tenant and frozen schema-1 fixture replay) stayed green in the RED run.

### Design: one generation gate and self-validating replay rows

- `durableReconciliationGeneration(column)` is the only place the durable
  discriminator is normalized: explicit zero is the legacy generation (the
  migration default and the wire normalization agree), known generations pass,
  any other value returns `ErrInvalidReconciliation`. No other call site
  casts the column.
- `bindReconciliationReadGeneration(wire, column)` is the single gate: it
  normalizes the column, compares it to the decoded wire generation and
  returns `ErrReconciliationRetentionMismatch` on any disagreement. It is
  invoked by `GetReconciliation`, `ListReconciliations` and
  `rebuildReconciliationProjections` before assignment, projection binding or
  projection repair.
- `GetReconciliation`/`ListReconciliations` assign the gate result instead of
  the raw column and only bind `bindReconciliationStoredProjection` for
  generation 2. The canonical-empty fallback now requires the normalized
  generation to be legacy; a generation-2 or unknown discriminator without
  canonical bytes fails closed instead of synthesizing schema 2 from
  projections.
- `reconciliationReplaySelect` and `reconciliationRow` now carry
  `result_schema_version`, `projection_version` and the full projection
  identity (subject kind/id/json, tenant, scope, basis, three input hashes,
  policy id/version, created_at). `resolveReconciliationReplay` decodes the
  existing canonical envelope, requires it to carry the durable generation,
  binds the full schema-2 projection for generation 2, and only then compares
  identity: exact canonical bytes are mandatory for canonical rows; schema 2
  requires a matching nonempty fingerprint and never uses the `result_json`
  fallback; the empty-fingerprint/result-only compatibility path is restricted
  to explicitly legacy canonical-empty rows. Conflict errors keep
  `ErrIdentityConflict` and also wrap the underlying
  `ErrReconciliationRetentionMismatch`/`ErrInvalidReconciliation` cause
  (`%w` both) so callers can classify either layer.
- `bindReconciliationStoredProjection` now also requires
  `projection_version = BillingEconomicsProjectionVersion`, closing the last
  projection identity field for reads and replay.
- Schema 1 is untouched on the write side: no wire field, no byte or
  fingerprint change; the R5A frozen fixture read/replay/restart and the PG
  fixture replay stay green.

### Files changed (this round only)

- `internal/infra/billingstore/v2_economics_store.go` (1462 -> 1614 lines):
  generation normalization/gate helpers, replay SELECT and row struct with the
  generation and projection identity columns, self-validating replay, gated
  get/list canonical and canonical-empty paths, gated projection repair.
- `internal/infra/billingstore/reconciliation_retention_store.go`
  (254 -> 256 lines): `projection_version` added to the stored projection
  identity and binding.
- `internal/infra/billingstore/reconciliation_generation_binding_test.go`
  (new, 322 lines): both wire/column mutation directions on get, list,
  specialized retention, latest and restart; canonical-empty generation
  restriction; explicit-zero and unknown-discriminator controls; five replay
  poison cases plus exact-replay and canonical-empty legacy controls.
- `internal/infra/billingstore/reconciliation_retention_postgres_test.go`
  (384 -> 460 lines): live PG generation mutation (both directions) and
  existing-row replay poison subtests.
- `internal/infra/billingstore/reconciliation_schema1_postgres_test.go`
  (58 -> 79 lines): live PG frozen schema-1 row with a forged generation
  discriminator fails closed on read and replay.

### Verification (fresh, shared dirty worktree)

- GREEN focused new suites:
  `go test -count=1 ./internal/infra/billingstore -run
  'TestReconciliationGenerationBinding'` (all subtests, including the zero and
  unknown discriminator controls).
- GREEN existing focused suites:
  `go test -count=1 ./internal/infra/billingstore -run
  'TestReconciliationSchema1|TestReconciliationRetention|TestLatestReconciliationRetention|TestReconciliationEnvelopeBinding|TestPhase4' -v`
  including all Unit1-5 poison, tolerance, aggregate, schema-1 byte/fingerprint,
  restart and projection tests.
- GREEN `go test -count=5 -shuffle=on ./internal/infra/billingstore -run
  'TestReconciliationRetention|TestReconciliationSchema1|TestLatestReconciliationRetention|TestReconciliationEnvelopeBinding|TestReconciliationGenerationBinding'`
  (24.445s) and `go test -count=5 -shuffle=on ./internal/core/billing -run
  'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate|TestOperatorCostSelection'`
  (1.168s).
- GREEN full affected packages: `./internal/core/billing/...` (0.468s),
  `./internal/infra/billingstore/...` (36.132s),
  `./internal/infra/billingcompose/...` (0.544s),
  `./pkg/lipsdk/economics/...` (0.412s), `./pkg/lipsdk/metering/...` (0.397s).
- GREEN `make test-db-parity-sqlite` for all registered components
  (billingstore 16.031s).
- GREEN focused live PostgreSQL with `LIP_REQUIRE_POSTGRES=1`
  (`go test -tags=integration -count=1 ./internal/infra/billingstore -run
  'TestReconciliationRetentionPostgresDirect|TestLatestReconciliationRetentionPostgresPlanAndOrder|TestReconciliationSchema1PostgresFixtureReplay|TestReconciliationSchema1PostgresForgedGenerationFailsClosed' -v`,
  37.860s): retention direct with all 12 subtests including both generation
  directions and replay poison; latest plan and tenant refinement isolation;
  frozen schema-1 fixture replay; forged-generation schema-1 fail-closed.
- GREEN `go build ./...`; `go vet ./internal/core/billing
  ./internal/infra/billingcompose ./internal/infra/billingstore` and the same
  with `-tags=integration`; `gofmt -l` over the five changed packages and
  `git diff --check` returned nothing.
- Dirty Go file count 31, under the 100-file gate.

### RR1 residuals

- Only review BLOCKER P12-RR1 is closed. P12-RR2 (retained aggregate source
  status/reason not fully re-derived) remains open by explicit round scope and
  was not implemented.
- A schema-2 row whose canonical bytes match an incoming append but whose
  projection columns disagree now fails replay closed; this is intentional
  fail-closed behavior for contradictory durable state and never rewrites
  stored bytes or projections.
- Projection repair (`RebuildValuationProjections`) now refuses a
  cross-generation row instead of repairing its projections; valid rows and
  the legacy canonical-empty path are unchanged.
- No migration, schema, API, posting, journal, Kiro or git operation. Stored
  schema-1 bytes/fingerprints, R5B arithmetic controls and R5C index/plan
  behavior are unchanged.

## Phase12 RR2 debug retry: authoritative aggregate finding source cross-binding (review BLOCKER P12-RR2)

Scope: close only review BLOCKER P12-RR2 (retained aggregate finding source
Status/Reason is not cross-bound to the retained quantity/monetary evidence) in
`internal/core/billing/reconciliation_validation.go` and the SQLite/PostgreSQL
durable tests, plus the retention fixtures that previously dropped the source
comparison. RR1 generation/replay binding, Units 1-5, R5A schema-1
compatibility, R5B arithmetic controls, the R5C tenant index/plan and the
accepted Unit3 tolerance/evaluated layer and policy `VersionRef` boundary are
preserved. Both evidence files are preserved; only this execution file was
appended. No git/Kiro/PR operation was performed.

### ROOT_CAUSE (confirmed by RED)

- `validateRetentionAggregate` validated each retained finding through
  `validateReconciliationFinding` and `validateRetentionFindingClassification`,
  then rederived rows from the retained findings. `validateRetentionFindingClassification`
  checks the deliberate reason vocabulary and, for comparable findings,
  rederives the nested tolerance evaluation, but it never compares the
  producer-facing `finding.Status`/`finding.Reason` with the retained quantity
  or monetary comparison. Row rederivation counts and amounts are driven by the
  *evaluated* labels, so a forged source label that leaves the evaluated layer
  untouched survives every existing check.
- A comparable finding with exact amounts could be relabelled `matched` while
  its evaluation stayed discrepant, or carry an invented nonempty source
  reason; non-comparable monetary findings could swap `missing_provider` to
  `missing_local` or replace the producer's partial/incomparable reason with any
  other known reason (evaluated labels matching the swap); a comparable
  quantity item could carry a known source reason; valuation ids and source
  observation refs could be appended; and an aggregate with no retained
  quantity or monetary comparison was accepted because its rows were rederived
  only from its own findings. There was no requirement that any retained
  finding be a producer projection at all.

### RED (exact captured output)

Core, before the fix
(`go test -count=1 ./internal/core/billing -run
'TestReconciliationRetentionBindsAggregateFindingsToProducerSources' -v`;
captured in `rr2-red-core.txt`). Ten producer-shaped controls (quantity
matched, discrepant, missing_local/missing_provider, incomparable, conflict;
monetary matched, complete, missing_p, partial, incomparable) passed before the
fix, and nine forged source classifications returned nil where rejection was
required:

- `comparable monetary source status changed to matched`:
  `Validate error = <nil>, want ErrInvalidReconciliationRetention`.
- `comparable monetary source carries an invented reason`: same.
- `monetary partial reason swapped`: same.
- `monetary incomparable reason swapped`: same.
- `quantity comparable finding marked matched`: same.
- `comparable quantity item carries a source reason`: same.
- `monetary finding forged valuation ids`: same.
- `monetary finding forged source refs`: same.
- `aggregate findings without retained comparisons`: same.

Four further mutations (`absent-value monetary finding marked matched`,
`monetary missing side swapped`, `quantity missing side swapped`, `quantity
comparable finding forged amounts`) were already rejected before the fix by the
Unit3 row/status-count rederivation; they remain in the suite as closed
controls.

Durable SQLite, before the fix, with the new binding temporarily bypassed
(`go test -count=1 ./internal/infra/billingstore -run
'TestReconciliationRetentionRejectsUnboundAggregateSourceFindingsDurably' -v`;
captured in `rr2-red-store-sqlite.txt`): `specialized append`
`Expected error with "billing: invalid reconciliation retention result" in
chain but got nil`; `generic schema-2 append` `Expected error with
"billingstore: invalid reconciliation" in chain but got nil`; and the durable
forged row (`get`, `list`, `latest`, restart) also returned nil.

Durable PostgreSQL, before the fix
(`$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -count=1
./internal/infra/billingstore -run
'TestReconciliationRetentionPostgresDirect/unbound_aggregate_source_classification_fails_closed'
-v`; captured in `rr2-red-store-pg.txt`): `Expected error with "billingstore:
invalid reconciliation" in chain but got nil`. A first test draft built the
poison outer record manually and surfaced the Unit5 envelope binding
(`policy_id does not match the canonical inner result`) instead; the subtest
now uses the authoritative `ReconciliationRecordFromRetention` outer record
with only the inner payload forged.

The temporary bypass was applied with a file backup and restored
byte-identically; SHA-256 of `reconciliation_validation.go` after restore is
`27CA6CA156E2E011A7AAB98EE3EDC658F835D4D0D41BE7558D37A8EF74D572AE`, matching
the pre-bypass file.

### Design: producer regeneration and exact-one source binding

- `validateRetentionNested` passes the retained `r.Quantity` and `r.Monetary`
  comparisons into `validateRetentionAggregate`, so aggregate validation runs
  after both nested comparisons have been validated.
- `validateRetentionAggregateSourceBinding` regenerates candidate findings with
  `ReconciliationFindingsFromQuantityComparison` and
  `ReconciliationFindingsFromMonetaryComparison`. The producer scope is the
  finding's own retained `Scope`, because both producers copy their scope
  argument into every finding they emit; candidates are generated once per
  distinct scope and indexed by `(scope, currency, unit, id)`, so the work
  stays bounded by the existing finding/evidence bounds. No ID-prefix or other
  heuristic is used.
- Every retained aggregate finding must match exactly one candidate under
  `reconciliationFindingSourceEqual`: full target identity (id, scope,
  direction, unit, currency, component, schema id, qualifier context), source
  `Status` and `Reason`, local/provider quality labels, exact
  expected/reported amounts compared as currency plus exact rational value
  (decimal and rational spellings of one value are equal), canonicalized
  source observation refs (`ObservationRef.Equal`) and canonicalized valuation
  ids. Zero or multiple matches fail closed. The evaluated tolerance layer
  (`EvaluatedStatus`, `EvaluationReason`, `Evaluation`) is deliberately
  excluded and stays Unit3's derived-projection responsibility.
- Findings retained with both comparisons absent are rejected explicitly
  instead of having their provenance inferred. Complete comparable producer
  outcomes require `ReasonNone`: `validateRetentionQuantityComparison` now
  rejects a matched/discrepant quantity item that carries a reason (the live
  comparator never emits one), and complete monetary terms already forbid
  reasons, so an invented complete source reason cannot match any regenerated
  candidate.
- The aggregate policy `VersionRef` contract is unchanged: rule material is
  still not required or reconstructed. Only fixtures that previously dropped
  the source comparison changed: the Unit3 `retentionWithAggregate` helper and
  the store `retentionAggregateResultForStore` helper now retain the monetary
  comparison the aggregate was built from, and the canonical-permutation
  fixture rebuilds its aggregate from the producers over its updated evidence
  instead of cloning a synthetic finding.

### Files changed (this round only)

- `internal/core/billing/reconciliation_validation.go` (852 -> 959 lines):
  aggregate signature now receives the retained comparisons, the single
  producer cross-binding, exact-one matching with semantic amount/ref/id
  equality, and the comparable quantity-item `ReasonNone` rule.
- `internal/core/billing/reconciliation_retention_source_binding_test.go`
  (new, 360 lines): ten producer controls and thirteen forged-classification
  mutations.
- `internal/core/billing/reconciliation_retention_consistency_test.go`
  (1369 -> 1371 lines): Unit3 helper retains the source monetary comparison.
- `internal/core/billing/reconciliation_retention_test.go` (471 -> 480 lines):
  permutation fixture rebuilds the aggregate from the producers.
- `internal/infra/billingstore/reconciliation_source_binding_test.go` (new,
  91 lines): specialized append, generic schema-2 append and a durable forged
  row failing closed on get/list/latest/restart.
- `internal/infra/billingstore/reconciliation_retention_store_test.go`
  (780 -> 782 lines): aggregate fixture retains the source monetary comparison.
- `internal/infra/billingstore/reconciliation_retention_postgres_test.go`
  (460 -> 491 lines): live PG generic append and raw-row read poison subtest.

### Verification (fresh, shared dirty worktree)

- GREEN focused core suite
  `go test -count=1 ./internal/core/billing -run
  'TestReconciliationRetentionBindsAggregateFindingsToProducerSources'`.
- GREEN full affected packages: `./internal/core/billing/...` (0.483s),
  `./internal/infra/billingstore/...` (35.978s),
  `./internal/infra/billingcompose/...` (0.548s),
  `./pkg/lipsdk/economics/...` (0.400s), `./pkg/lipsdk/metering/...` (0.412s),
  including all Unit1-5, schema-1, envelope, generation and tenant tests.
- GREEN `go test -count=5 -shuffle=on ./internal/core/billing -run
  'TestReconciliationRetention|TestReconciliationCompare|TestMonetaryDiscrepancy|TestReconciliationTolerance|TestReconciliationAggregate|TestOperatorCostSelection'`
  (1.314s) and the store equivalent including the new durable poison suite
  (28.104s).
- GREEN `make test-db-parity-sqlite` for all registered components
  (billingstore 16.045s).
- GREEN live PostgreSQL with `LIP_REQUIRE_POSTGRES=1` (38.233s):
  `TestReconciliationRetentionPostgresDirect` all 13 subtests (Units 1-4
  poisons, envelope, projection, both RR1 generation directions, RR1 replay
  poison, RR2 unbound source classification), latest plan and tenant
  refinement isolation, frozen schema-1 fixture replay and forged-generation
  schema-1 fail-closed.
- GREEN `go build ./...`; `go vet ./internal/core/billing
  ./internal/infra/billingcompose ./internal/infra/billingstore` and the same
  with `-tags=integration`; `gofmt -l` over the five changed packages and
  `git diff --check` returned nothing.
- Dirty Go file count 33, under the 100-file gate.

### RR2 residuals

- Both Phase12 blockers P12-RR1 and P12-RR2 are now implemented and verified;
  the review verdict is left to the reviewer. No Phase13 or speculative work
  was started.
- The binding proves every retained aggregate finding is exactly one producer
  projection. It intentionally does not require the aggregate to retain every
  producer candidate; aggregate row rederivation already rejects rows that are
  not derived from the retained findings, and subsetting policy is outside the
  RR2 boundary.
- The producer scope is taken from each retained finding (both producer
  functions only copy their scope argument), so finding scopes are not
  cross-checked against the parent result `Scope`; this is unchanged
  evidence-driven behavior, not a new gap.
- The evaluated tolerance layer, the aggregate policy `VersionRef` boundary
  and the R5A/R5B/R5C/RR1 behaviors are unchanged. No migration, schema, API,
  posting, journal, Kiro or git operation.
