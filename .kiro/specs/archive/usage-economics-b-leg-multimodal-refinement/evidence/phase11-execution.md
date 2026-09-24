# Phase 11 slice A execution evidence

Scope: parent task 11.1, provider allowance-window gauge persistence and
current/as-of query projections. Request debit/allocation (11.2) and admission
authority (11.3) remain out of scope.

## TDD and verification

- RED: `go test -count=1 ./internal/core/metering -run 'TestProjectAccountWindows'` failed to compile because `ProjectAccountWindows` and `AccountWindowProjection` were not yet defined.
- RED: `go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindowJournal'` failed to compile because the account-window journal append/query methods and query contract were not yet defined.
- RED: `go test -count=1 ./internal/infra/metering/journalstore -run '^TestPhase11AccountWindowJournal_SearchProjectionDriftFailsClosed$'` initially returned no error after a deliberately corrupted pool search column, showing that the query did not yet verify all search identity fields against canonical JSON.
- RED: `go test -count=1 ./internal/infra/metering/journalstore -run '^TestPhase11AccountWindowJournal_AllowsUnixEpochReset$'` initially rejected a valid Unix-epoch reset because zero Unix nanos had been confused with a zero `time.Time`.
- RED: `go test -count=1 ./internal/infra/metering/journalstore -run '^TestPhase11AccountWindowJournal_PayloadDriftFailsClosed$'` initially accepted a changed canonical measure payload despite the stored fingerprint.
- GREEN: the focused core projection tests passed after the minimal account-window domain contract and reducer were added.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindowJournal'` passed, including immutable history, replay/conflict, partial-field, pool/window/reset isolation, as-of, pagination, concurrent append, and file-restart fixtures.
- GREEN: the focused payload/search-projection consistency and Unix-epoch reset regressions passed after query decode checked the stored fingerprint, tenant/account/pool/window/reset fields, and timestamps against the canonical payload and used `time.Time.IsZero` for reset presence.
- GREEN: `go test -count=1 ./internal/core/metering/... ./internal/infra/metering/journalstore/...` passed.
- GREEN: `go test -count=1 ./internal/testkit/dbparity/...` passed after registering the account-window search-column/index parity contract.
- GREEN: `make test-db-parity-sqlite` passed, including the metering-journal schema and behavior parity entry point.
- GREEN: `go vet ./internal/core/metering/... ./internal/infra/metering/journalstore/... ./internal/testkit/dbparity/...` passed.
- GREEN: `go test -count=1 ./internal/archtest -run 'Metering|DatabaseParity|UsageEconomics|Package|Boundary'` passed.
- GREEN: `go test -count=1 ./internal/qa` passed.
- GREEN: `gofmt` and `git diff --check` passed.
- PostgreSQL direct parity test was added and compiled with `go test -tags=integration -run '^$' ./internal/infra/metering/journalstore`; the live account-window PostgreSQL test was attempted but skipped because no PostgreSQL test DSN was configured on this host.
- Race verification was attempted with `go test -race -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindowJournal_ConcurrentOutOfOrderAppendsHaveOneDeterministicHead|TestPhase11AccountWindowJournal_PersistsHistoryAndProjectsCurrentAsOf'`; the Windows host `cgo.exe` exited with status 2 before race tests ran.

## Task 11.1 mapping

- Account-window domain: `internal/core/metering/account_window.go` defines the
  account/pool/window/reset query contract and deterministic present-field gauge
  reducer. It sorts by observed/effective time with deterministic tie-breakers,
  retains immutable observation references, ignores request lineage for subject
  identity, and never performs arithmetic over percentages or allowance values.
- Journal adapter: `internal/infra/metering/journalstore/account_window_store.go`
  validates account-window subjects, appends through the existing canonical
  observation journal, provides bounded history pagination, and rebuilds
  current/as-of projections from immutable history. SQLite busy retries cover
  concurrent durable observation arrivals.
- Persistence/parity: migration
  `20260914000000_metering_account_window_projection.go` adds provider account,
  pool, window, reset, observed, and received search columns plus a dual-dialect
  account-window index. Canonical payload JSON remains authoritative; columns
  are checked against it during query decode. Schema specs/catalog and migration
  verification include both dialects.
- Acceptance: multiple accounts/pools/windows/resets remain isolated; partial
  fields merge without treating absent as zero; late older snapshots remain in
  history without replacing newer current values; exact replay is idempotent and
  changed payloads conflict; request association is informational; no billing
  store, customer credit, or debit/posting path is called.

## Residual risks and skips

- No live PostgreSQL instance was available for execution; the direct parity
  fixture and dual-dialect migration/query SQL are present, while SQLite parity
  and schema verification are green.
- Race instrumentation is unavailable because the Windows cgo tool failed before
  test execution; normal concurrent SQLite append coverage passes with bounded
  busy retry.
- No request-scoped debit/allocation or admission authority was added; provider
  account-window gauges remain nonmonetary telemetry and are not used as a
  customer balance or billing posting source.

## Phase 11 slice B1: request-scoped provider debits (11.2 first half)

Scope: parent task 11.2's genuine request-scoped provider-unit debit plane
only. Subscription/local allocation (11.2 second half), admission authority
(11.3), customer allowance ledger, and provider producer migration remain out
of scope.

### TDD and verification

- RED: `go test -count=1 ./pkg/lipsdk/metering -run '^TestProviderDebit_'` failed to compile before implementation because `ProviderDebit`, `ProviderDebitVersionV1`, `SubjectProviderDebit`, and `ProviderDebitFromObservation` were undefined.
- RED: `go test -count=1 ./internal/core/metering -run '^TestProviderDebitReduction_'` failed to compile before implementation because the provider-debit reducer, snapshot, and typed errors were undefined.
- GREEN: `go test -count=1 ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./internal/core/metering ./internal/core/metering/aggregate ./internal/core/billing ./internal/infra/metering/journalstore ./internal/infra/billingstore` passed.
- GREEN: `go test -count=1 ./internal/core/metering/... ./internal/infra/metering/journalstore/...` passed.
- GREEN: `go test -count=1 ./internal/archtest -run 'Metering|DatabaseParity|UsageEconomics|Package|Boundary'` passed.
- GREEN: `go test -count=1 ./internal/qa` passed.
- GREEN: `make test-db-parity-sqlite` passed, including the existing V2 metering journal path.
- GREEN: `go vet ./pkg/lipsdk/metering ./pkg/lipsdk/economics ./internal/core/metering ./internal/core/billing ./internal/infra/metering/journalstore ./internal/infra/billingstore` passed.
- GREEN: `gofmt` and `git diff --check` passed.
- Race verification was attempted with `go test -race -count=1 ./internal/core/metering ./internal/infra/metering/journalstore -run 'ProviderDebit|provider_debit'`; Windows `cgo.exe` exited with status 2 before tests ran, so no Windows race pass is claimed.

### B1 mapping and acceptance

- Public metering: `pkg/lipsdk/metering/provider_debit.go` defines an exact-Decimal, nonmonetary `ProviderDebit` requiring provider account, pool, window/reset, request, BillingCallID, and B-leg identity; it requires observed provider authority/quality and a supported provider acquisition channel. It rejects gauge semantics, percentage/remaining/utilization/limit gauge components, estimated/unavailable evidence, missing quantities, and invalid correction/replacement links.
- Canonical separation: `SubjectProviderDebit` is distinct from `SubjectAccountWindow` and `SubjectProviderCharge`; the debit projection has exactly one measure and no `ReportedCharge`. `BasisProviderUnitDebit` is a known nonmonetary basis separate from provider money P, local Q, and local allocation bases. No conversion, customer credit mutation, or ledger posting is introduced.
- Reduction: `internal/core/metering/provider_debit.go` validates typed evidence or canonical V2 debit observations, then uses the existing deterministic replay/supersession reducer. Exact replays are no-ops, changed payloads under one source identity return `ErrProviderDebitIdentityConflict`, corrections/replacements require resolvable immutable references and matching components, unresolved/partial evidence returns `ErrProviderDebitIncomplete`, and output exposes measures/observations only—never charges or money.
- Isolation and persistence: focused tests prove deterministic out-of-order additive reduction plus request, provider-account/pool/window, component, BillingCallID, and B-leg isolation. The existing V2 journal/component projections round-trip the complete debit binding and query by the dedicated subject without a debit-specific SQL migration.
- Money boundary: `internal/core/billing` explicitly excludes `SubjectProviderDebit` from provider-quantity rating and B1 has a focused test proving it reaches none of the independent E/Q/P money rating planes. Unsupported producers remain unsupported: empty input returns `ErrProviderDebitAbsent`; no account-window gauge or request association is accepted as a debit.

### Residual risks and skips

- No subscription/account-period allocation records were added; non-request subscription/resource economics remain on their native subjects for the later B2 allocation owner.
- No provider adapter/connector was changed to emit a debit. A producer must supply the authoritative typed record; absent or unsupported producer output is not synthesized from account-window deltas.
- No live PostgreSQL DSN was available for execution on this host; the existing dual-dialect schema/parity path remains covered by SQLite parity and parent Phase 11 evidence.
- The uncached full `go test -count=1 ./...` run still has shared-branch failures unrelated to B1, including request-attempt AST ratchets/baseline drift, compaction field expectations, the existing billing-to-`lipapi` architecture check, core line-complexity budget, malformed `GOWORK=off` import path, billing-compose settlement expectation, and runtimebundle timeout tests. The focused B1 suites and targeted architecture/QA gates pass independently.

## Phase 11 slice B2: explicit conserved allocation records (11.2 second half)

Scope: parent task 11.2 allocation domain and minimal billing-store persistence
only. Admission influence (11.3), producer migrations, and refinement 7.4
certification remain out of scope.

### TDD and verification

- RED: `go test ./pkg/lipsdk/economics` failed to compile before implementation because the explicit allocation record, target/fraction, residual policy, and conservation constructor were undefined.
- RED: `go test ./internal/core/billing -run TestRollupAllocatedCostsPreservesSourcePlaneAndUnallocatedRemainder` failed to compile before implementation because the source-preserving rollup was undefined.
- RED: `go test ./internal/infra/billingstore -run TestPhase11Allocation` failed to compile before implementation because durable allocation append/get/list methods were undefined.
- GREEN: `go test ./pkg/lipsdk/economics ./internal/core/billing ./internal/infra/billingstore -run 'TestAllocation|TestRollupAllocatedCosts|TestPhase11Allocation'` passed, covering exact rational conservation, deterministic target ordering/residual assignment, scope/plane mismatch, typed zero/negative/refund/correction semantics, source-preserving rollup, idempotent replay/conflict, immutable same-ID version correction, and schema verification.
- GREEN: `go test ./internal/infra/billingstore` passed (`19.498s`), including existing billing persistence regressions and the allocation migration.
- GREEN: `go test ./internal/testkit/dbparity` passed after registering the explicit allocation replay capability.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics ./internal/core/billing ./internal/infra/billingstore ./internal/testkit/dbparity` passed.
- GREEN: `go test -count=1 ./internal/core/billing/... ./internal/core/metering/...` passed, including aggregate/checkpoint/normalization/plane/reconcile/replay packages.
- GREEN: `go vet ./pkg/lipsdk/economics ./internal/core/billing ./internal/infra/billingstore ./internal/testkit/dbparity` passed.
- GREEN: `go test -count=1 ./internal/archtest -run 'Metering|DatabaseParity|UsageEconomics|Package|Boundary'` passed.
- GREEN: `go test -count=1 ./internal/qa` passed.
- GREEN: `make test-db-parity-sqlite` passed, including the billing allocation schema and round-trip tests.
- GREEN: `go test -tags=integration -run '^$' ./internal/infra/billingstore` compiled the direct PostgreSQL integration path (no tests run).
- GREEN: `gofmt` and focused `git diff --check` passed.

### B2 mapping and acceptance

- Public allocation domain: `pkg/lipsdk/economics/allocation.go` defines an immutable source-preserving envelope with original resource/period/account subject, exact source amount or quantity, currency/unit, policy method/version/hash, source observation/valuation lineage, immutable supersession refs, exact rational weights/shares, explicit unallocated targets, and declared integer residual policy.
- Plane separation: account-window gauges and `SubjectProviderDebit` cannot be allocation sources/targets; provider-unit debit basis cannot be converted into an allocation; allocation rollups retain `BasisAllocatedCost` (or their declared source basis) and never become operator COGS/B-leg inference evidence.
- Conservation and replay: fractions are normalized as bounded non-negative rationals and must sum exactly to one. Canonical target/ref ordering makes replay independent of input order. Monetary targets are rounded from exact source/share values with a declared policy; residuals are rejected, assigned to an explicit unallocated target, or assigned deterministically to the final economic target. Negative values are accepted only for typed refunds/corrections; zero requires the zero operation.
- Durable boundary: `20260918000000_billing_allocations.go` adds dual-dialect immutable parent/target history, source/target search indexes, and FK protection. `allocation_store.go` stores canonical JSON as authority, projects search/target rows atomically, collapses exact replays, rejects same identity payload drift, and appends versioned corrections without rewriting. File-backed restart round-trip is covered by `phase11_allocation_restart_test.go`.

### Residual risks and skips

- No live PostgreSQL DSN was available for execution on this host; PostgreSQL DDL and schema verification checks are present, while SQLite schema/persistence and dbparity catalog tests are green.
- No admission, credit mutation, journal posting, provider producer, or customer retail path was changed. A B-leg target is a linkage in the allocation view only; `InferenceEligible` is always false, so allocation cannot fabricate request inference.
- Parent Phase 11 B1 changes and evidence remain in this shared worktree. This slice does not certify refinement 7.4 because its formal 7.1-7.3 dependencies remain unchecked.

## Phase 11 slice C1: nonfinancial provider-quota policy (11.3 first half)

Scope: the consumer-owned authority policy/evaluator contract for optional
provider account-window telemetry. Host/runtime configuration, store wiring,
provider producers, customer credit, billing posting, provider request debit,
and allocation remain out of scope.

### TDD and verification

- RED: `go test -count=1 ./pkg/lipsdk/authority -run '^TestQuota'` failed to compile before implementation because the quota policy config, binding, typed thresholds, evaluator, and decision contracts were undefined.
- GREEN: `go test -count=1 ./pkg/lipsdk/authority -run '^TestQuota'` passed. The focused tests cover fresh matching admission, deterministic order/replay, immutable policy hashing/accessors, percentage and absolute threshold typing, missing/stale/partial/unavailable configured postures, account/pool/window/reset isolation, as-of head selection, and non-additive gauge history.
- GREEN: `go test -count=1 ./pkg/lipsdk/authority ./pkg/lipsdk/metering` passed.
- GREEN: `go vet ./pkg/lipsdk/authority ./pkg/lipsdk/metering` passed.
- GREEN: `go test -count=1 ./internal/archtest -run '^TestDualPlaneEconomicsPublicPackageDAG$'` passed; the new authority contract imports only the neutral metering package and does not add a billing dependency.
- GREEN: `go test -count=1 ./internal/qa` passed.
- GREEN: `gofmt` and `git diff --check` passed for the C1 files.
- The requested broad validation `go test -count=1 ./pkg/lipsdk/authority/... ./internal/core/runtime/... ./internal/archtest/...` passed for authority and runtime but the shared `internal/archtest` suite failed on existing unrelated drift: compaction public-field expectations, request-attempt AST baseline/ratchets, billing-to-`lipapi` boundary, core line-complexity budget, and the malformed `GOWORK=off` import path. The focused dual-plane guard and QA suite remain green.
- Race, PostgreSQL, and database-parity execution were not run for C1: this slice has no persistence or concurrency mutation; those boundaries remain covered by the Phase 11 A/B evidence and the C2 integration contract below.

### C1 mapping and acceptance

- `pkg/lipsdk/authority/quota.go` defines `QuotaBinding` with provider account,
  named pool, window and exact reset epoch; optional store/tenant scope is
  fail-closed when input is ambiguous. `QuotaPolicyConfig` requires a positive
  bounded freshness duration, required canonical nondirectional fields, exact
  typed thresholds, and explicit missing/stale/partial/unavailable/mismatch/
  future failure actions. Omitted failure actions normalize to deny, so no
  incomplete telemetry silently allows admission.
- `CompileQuotaPolicy` returns an immutable `QuotaPolicy` whose normalized
  required fields, thresholds, binding, freshness and failure posture are
  content-addressed by a SHA-256 `QuotaPolicyRef`. Input/accessor slices and
  canonical bytes are copied. Percentage thresholds require a percent field and
  `[0,100]`; absolute thresholds require a non-percent native unit and a
  nonnegative exact decimal. Comparisons use `metering.Decimal`/`big.Rat`, never
  floating point.
- `QuotaPolicy.Evaluate` accepts an explicit evaluation time and persisted V2
  account-window observations only. It canonicalizes and validates provider
  observed gauge evidence, removes exact source replays, rejects conflicting
  immutable identities, sorts out-of-order history deterministically, excludes
  observations not effective as of the supplied time, and merges gauges as
  replacement/present-field state. It never sums percentages or allowance
  values, differences snapshots, infers request consumption, or creates money,
  payable postings, customer credit debits, provider debits, or allocations.
- A complete fresh matching pool returns `QuotaDecisionAllow` or a deterministic
  typed threshold/headroom `QuotaDecisionDeny`. Missing, stale, partial,
  unavailable, future, wrong-account, wrong-pool, wrong-window, wrong-store,
  wrong-tenant, and wrong-reset input retains its `QuotaEvidenceStatus`, reason,
  policy method/version/hash, and any source `ObservationRef` evidence relevant
  to the result. A reset epoch is part of identity, so an older window cannot be
  used for the current policy.

### C2 integration contract

1. Compile one explicit `authority.QuotaPolicy` from host configuration and
   retain its `PolicyRef` with the request authority snapshot. If no policy is
   configured, do not register an implicit quota authority and do not infer an
   allow decision from telemetry.
2. Query the existing
   `internal/core/metering.AccountWindowStore.ListAccountWindowObservations`
   using the policy binding (`StoreID`, optional tenant, provider account, pool,
   window, exact reset) and `AsOf` equal to the explicit admission time. Follow
   every bounded page until `NextCursor` is empty; never truncate history and
   then call C1 as if the page were complete. If the bounded history cannot be
   completed, map that condition to the declared missing/unavailable posture
   (never allow).
3. Pass the complete immutable observation history to
   `QuotaPolicy.Evaluate(QuotaEvaluationInput{At: admissionTime,
   Observations: ..., TelemetryUnavailable: ...})`. Set
   `TelemetryUnavailable` when the provider/store reports an unavailable
   source; retain the returned unavailable status rather than substituting a
   stale historical allow. Do not pass `SubjectProviderDebit`,
   `SubjectProviderCharge`, customer-credit, or allocation records.
4. Map `QuotaDecisionAllow` to generic `authority.DecisionAllow` and
   `QuotaDecisionDeny` to `authority.DecisionDeny`. Generic authority has no
   indeterminate kind: C2 must preserve C1's typed status/reason and policy
   ref in safe nonfinancial evidence, then apply the host's explicit
   fail-closed indeterminate posture (an advisory result must not become an
   implicit allow). Keep `EvidenceRefs` as immutable metering provenance.
5. Keep account-window gauges on the quota-authority/evidence plane only. C2
   must not populate `authority.Reservation.Quantity`/`Money`, billing calls,
   customer credit balances, payable postings, provider-unit debits, or
   allocation records from a quota decision. No C1 database write is required;
   the canonical metering journal remains the sole producer-owned evidence
   source.

### Residual risks and skips

- No host/runtime adapter or provider producer was changed, so the C2 page,
  unavailable-source and generic-indeterminate mapping contract above is not
  yet executable in production wiring.
- No live PostgreSQL, race, or persistence test was needed for this pure
  policy domain. Phase 11 A covers account-window journal durability and query
  parity; C1 intentionally adds no second persistence or ledger path.
- The broad archtest command remains red only on the shared-branch failures
  listed under verification; the authority/metering tests, dual-plane import
  guard, focused QA and vet checks are green.

## C2 implementation evidence: quota authority integration (11.3)

The C2 integration is now implemented. The earlier residual note that host
wiring was not executable applied before this slice and is superseded by the
evidence below.

### TDD and focused verification

- RED: the initial quota-provider test run failed to compile because the C2
  reader/authority adapter types did not yet exist.
- RED: `go test -count=1 ./internal/core/configreload -run
  '^TestReloadabilityClassify_QuotaPolicyIsGenerationReloadable$'` failed
  because quota policy replacement was not classified as generation
  reloadable (`got []configreload.SafeChange(nil)`).
- RED: `go test -count=1 ./internal/core/runtime -run
  '^TestAdmitRequestAuthority_UsesCandidateGenerationScopedQuotaWithBoundSnapshot$'`
  failed to compile because request authority state had no selected
  coordinator field.
- GREEN: `go test -count=1 ./internal/core/authoritycoord -run
  '^TestQuotaRequestProvider_'` passed.
- GREEN: `go test -count=1 ./internal/core/runtime` passed.
- GREEN: `go test -count=1 ./internal/core/authoritycoord ./internal/core/config
  ./internal/core/configreload` passed.
- GREEN: the focused quota/generation/runtimebundle matrix passed:
  `go test -count=1 ./internal/core/authoritycoord ./internal/core/runtime
  ./internal/core/config ./internal/core/configreload
  ./internal/infra/runtimebundle -run
  'Quota|GenerationScoped|GenerationBind|Phase5Remediation_BindBeforeAdmitUsesBoundGeneration|RequestAuthority|ReloadabilityClassify_Quota|TestBuild_Quota|TestQuotaRequestRegistration'`.
- GREEN: `go test -count=1 ./internal/infra/runtimebundle -run
  'Quota'` passed, including generation build wiring and request evaluation.
- GREEN: `go test -count=1 ./pkg/lipruntime` passed, including public-options
  nonfinancial guards.
- GREEN: `go test -count=1 ./internal/archtest -run
  'TestQuotaAuthorityAdapterHasNoFinancialInfrastructureImport|TestDualPlaneEconomicsPublicPackageDAG'`
  passed.
- GREEN: `go vet ./pkg/lipsdk/authority ./pkg/lipsdk/metering
  ./internal/core/authoritycoord ./internal/core/runtime ./internal/core/config
  ./internal/core/configreload ./internal/infra/runtimebundle ./internal/archtest
  ./pkg/lipruntime` passed.
- GREEN: `git diff --check` passed.

### C2 implementation and acceptance mapping

- `internal/core/authoritycoord/quota_provider.go` adds an optional,
  generation-local request authority adapter. It compiles and freezes one
  immutable `QuotaPolicy`/`PolicyRef`, queries the existing account-window
  reader with exact provider/account/pool/window/reset and admission `AsOf`,
  follows bounded cursors, clones observations, propagates cancellation and
  deadlines, and returns typed bounded reader/protocol errors. Missing stores
  and reader failures are evaluated through C1's unavailable posture and never
  become an implicit allow. Quota decisions map to generic allow/deny while
  preserving C1 status, reason, policy ref and immutable observation refs in
  safe evidence.
- `internal/core/config/accounting_authority.go` and
  `internal/infra/runtimebundle/{authority_coord.go,build_executor.go,production_options.go}`
  add explicit YAML/config and injection seams for provider account, pool,
  reset, freshness, required gauges, thresholds and failure actions. No quota
  policy means no registration and existing admission behavior is unchanged;
  configured policies are attached to the candidate generation only.
- `internal/core/configreload/policy.go` classifies quota changes as safe
  generation reloads while leaving non-quota authority changes restart-bound.
  `internal/core/authoritycoord/generation_scope.go` and
  `internal/core/runtime/authority_request.go` ensure a generation-scoped
  quota provider is selected over an older process-bound executable snapshot,
  and the selected coordinator remains fixed for that request's settle/release
  lifecycle. Later gauge reads therefore affect only later decisions.
- `internal/archtest/quota_authority_boundaries_test.go` proves the adapter
  cannot import billingstore/core-billing money infrastructure or reference
  money, customer-unit or payable selectors. Focused behavior tests also
  prove no reservation, exposure, journal, debit, charge, credit or allocation
  mutation is emitted, and `pkg/lipruntime` tests keep public options free of
  billing seams.

### Residual risks and skips

- `go test -race -count=1 ./internal/core/authoritycoord ./internal/core/runtime
  -run 'Quota|GenerationScoped'` was attempted but the Windows Go 1.26.6
  toolchain's `runtime/cgo` build failed before tests (`cgo.exe: exit status 2`).
- A broad, unrelated `internal/infra/runtimebundle` run still has existing
  failures in owner-reachable-load architecture fixtures and billing-host-loop
  timeout tests; focused quota runtimebundle tests are green.
- No live PostgreSQL DSN was available; this adapter performs no persistence
  writes and uses the existing account-window reader contract. Phase 11 A/B
  evidence covers journal durability and query parity.

The parent task 11.3 C2 implementation is complete and ready for slice review.

## Phase 11 slice A blocker 1: account-window migration backfill

Scope: repair the `20260914000000` account-window search projection for
pre-existing canonical observations. No other Phase 11 blocker or producer,
debit, allocation, or authority behavior was changed.

### TDD and verification

- RED: `go test -count=1 ./internal/infra/metering/journalstore -run '^TestPhase11AccountWindowProjectionUpgrade_'` initially showed a legacy row remained invisible after its denormalized search columns were cleared, and accepted an invalid canonical payload without a migration error.
- GREEN: the focused upgrade fixtures passed, covering recovery of provider account, pool, window, reset, observed/received timestamps, stream/sequence and observation search columns; canonical payload, fingerprint and source identity remain byte-identical; invalid canonical data rolls back derived updates and leaves the migration retryable.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore` passed.
- GREEN: `go test -count=1 ./internal/core/metering/... ./internal/infra/metering/journalstore/...` passed.
- GREEN: `go test -count=1 ./internal/testkit/dbparity/...` passed.
- GREEN: `make test-db-parity-sqlite` passed, including the metering-journal schema and behavior parity entry point.
- GREEN: `go vet ./internal/core/metering/... ./internal/infra/metering/journalstore/... ./internal/testkit/dbparity/...` passed.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore -run 'Postgres|Schema|Migration'` passed for the static schema/migration contracts.
- GREEN: `go test -tags=integration -run '^$' ./internal/infra/metering/journalstore` compiled the PostgreSQL integration surface without running tests.
- PostgreSQL direct account-window and parity tests were attempted with `-tags=integration -v` and skipped because `LIP_TEST_POSTGRES_DSN` is not configured on this host.
- GREEN: `gofmt` and focused `git diff --check` passed.

### Implementation and acceptance mapping

- `20260914000000_metering_account_window_projection.go` now reads existing
  `metering_facts` in deterministic primary-key batches of 256, classifies
  canonical observations without disturbing V1 facts, validates every marked
  observation and account-window subject, and updates only denormalized
  stream/search columns. `payload_json` and `observation_fingerprint` are
  never written; non-empty fingerprints and store scope are checked before
  repair.
- Backfill updates run in one SQLite/PostgreSQL-logically-equivalent Bun
  transaction. A malformed or inconsistent canonical candidate aborts the
  transaction, so no partial search projection is published.
- The metering migrator now records migration history only after a successful
  `Up`, allowing invalid historical data to fail closed and retry on reopen.

### Residual risks and skips

- No live PostgreSQL instance was available; PostgreSQL SQL compiled and static
  schema checks passed, while the direct runtime tests remain unexecuted.
- The backfill intentionally leaves canonical payload/fingerprint bytes and
  unrelated V1 fact rows untouched. It repairs only the observation and
  account-window denormalized search fields needed by the query/index.

Status: READY_FOR_REVIEW_11_1_B1

## Phase 11 slice A blocker 3: account-window stream ID search drift

Scope: fail closed when the denormalized `stream_id` search/order column no
longer matches the canonical account-window observation. No migration,
pagination contract, producer, debit, allocation, or authority behavior was
changed.

### TDD and verification

- RED: `go test -count=1 ./internal/infra/metering/journalstore -run '^TestPhase11AccountWindowJournal_StreamIDSearchProjectionDriftFailsClosed$'` returned nil for history, the history compatibility spelling, and current projection after only `metering_facts.stream_id` was mutated.
- GREEN: the same focused test passed after `decodeAccountWindowRow` compared canonical `Observation.StreamID` with the denormalized row value and returned the existing typed `ErrIdentityCollision` on mismatch.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore -run '^TestPhase11AccountWindow(Journal_(StreamIDSearchProjectionDriftFailsClosed|SearchProjectionDriftFailsClosed|HistoryAndProjectionCursorsAreFilterBound)|ProjectionUpgrade_BackfillsLegacySearchColumns)$'` passed, covering stream drift, existing search-column drift, valid history/projection keyset pagination, and legacy backfill rows.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore` passed (`1.947s`).
- GREEN: `go test -count=1 ./internal/core/metering/... ./internal/infra/metering/journalstore/...` passed.
- GREEN: `go vet ./internal/infra/metering/journalstore` passed.
- GREEN: `gofmt.exe -d internal/infra/metering/journalstore/account_window_store.go internal/infra/metering/journalstore/phase11_account_window_test.go` passed with no formatting diff. `git diff --check -- internal/infra/metering/journalstore/account_window_store.go internal/infra/metering/journalstore/phase11_account_window_test.go` also returned no diagnostics; these files are currently untracked in the shared worktree, so the check has no tracked patch to inspect.

### Implementation and acceptance mapping

- `decodeAccountWindowRow` now treats `stream_id` as part of the canonical
  identity/search projection consistency boundary. Since history ordering and
  history cursors use `stream_id`, a corrupt value cannot silently reorder rows
  or change keyset pagination while returning a valid payload.
- The regression mutates only `metering_facts.stream_id` and proves that
  `ListAccountWindowObservations`, `QueryAccountWindowObservations`, and
  `ProjectAccountWindows` all fail with `errors.Is(err,
  journalstore.ErrIdentityCollision)`.
- Existing valid pagination and legacy backfill fixtures remain green; the
  canonical payload remains authoritative for both freshly appended and
  backfilled rows.

### Residual risks and skips

- No live PostgreSQL runtime or race execution was attempted for this focused
  decoder-only guard. The SQL shape and dual-dialect migration are unchanged;
  the same post-scan validation is shared by both query paths.

Status: READY_FOR_REVIEW_11_1_B3

## Phase 11 slice A blocker 2: reset-filter cursor binding

Scope: bind optional `ResetAt` filter presence separately from its Unix-nanosecond
value in account-window history and current/as-of projection cursor identity.
No migration, producer, debit, allocation, or authority behavior was changed.

### TDD and verification

- RED: `go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindowJournal_(HistoryResetFilterCursorBinding|ProjectionAsOfResetFilterCursorBinding)$'` failed in both new regressions. The history assertion reported that a cursor from the reset-less filter was accepted by the explicit epoch filter (`expected error ... invalid cursor ... got nil`); the projection/as-of assertion reported the same cross-filter acceptance.
- GREEN: after `accountWindowFilterHash` included a separate `ResetAtSet` boolean alongside the reset value, `go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindowJournal_(HistoryResetFilterCursorBinding|ProjectionAsOfResetFilterCursorBinding)$'` passed (`0.032s`).
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore -run '^TestPhase11AccountWindowJournal'` passed (`0.417s`), including same-filter history and projection/as-of page-two assertions.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore` passed (`2.082s`).
- GREEN: `go vet ./internal/infra/metering/journalstore` passed with no diagnostics.
- GREEN: `gofmt -d internal/infra/metering/journalstore/account_window_store.go internal/infra/metering/journalstore/phase11_account_window_test.go` produced no formatting diff.
- GREEN: `git diff --check` returned no diagnostics. Because the two owned files are untracked in the shared worktree, a separate trailing-whitespace check over both files also passed.

### Implementation and acceptance mapping

- `accountWindowFilterHash` now hashes reset-filter presence (`ResetAtSet`) and the normalized reset epoch value independently. An omitted reset filter and an explicit Unix-epoch reset therefore have distinct query identities even though both numeric values are zero; cursor decoding fails closed on cross-filter reuse.
- `TestPhase11AccountWindowJournal_HistoryResetFilterCursorBinding` proves both directions of nil-versus-epoch cursor rejection and verifies that same-filter history pagination remains stable.
- `TestPhase11AccountWindowJournal_ProjectionAsOfResetFilterCursorBinding` applies an explicit `AsOf` bound, proves both directions of nil-versus-epoch cursor rejection for reduced projections, and verifies stable same-filter projection pagination.

### Residual risks and skips

- Account-window cursors issued before this hash-shape repair use the previous filter identity and will fail closed as `ErrInvalidCursor`; no approved backward-compatibility requirement was identified for those cursors.
- No live PostgreSQL runtime or race execution was run for this cursor-only fix. Existing Phase 11 evidence records the unavailable PostgreSQL DSN and the Windows cgo limitation for race tests; the focused and full SQLite journalstore suites, vet, formatting, and diff checks passed.

Status: READY_FOR_REVIEW_11_1_B2

## Phase 11 slice A blocker 4: provider-origin allowance evidence

Scope: enforce the provider-origin and observed-provider-authority boundary at
the provider allowance-window journal API. Generic account-window observation
append/replay contracts remain able to retain local and statement evidence;
those records are not provider allowance gauges. No producer, debit,
allocation, authority policy, or Kiro status behavior was changed.

### TDD and verification

- RED: `go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindowJournal_(ProviderAllowanceAppendRequiresObservedProviderClaim|ProviderAllowanceQueryRejectsNonProviderRows|ProviderOriginReplayQueryAndProjectionRebuild|GenericAppendStillAllowsNonProviderOrigins)$'` failed. The four append cases (local observed, statement verified, provider estimated, and provider unavailable) returned nil instead of rejecting; the local, statement, and provider-estimated query cases also returned nil instead of the typed `metering.ErrInvalidObservation` classification.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindowJournal_(ProviderAllowanceAppendRequiresObservedProviderClaim|ProviderAllowanceQueryRejectsNonProviderRows|ProviderOriginReplayQueryAndProjectionRebuild|GenericAppendStillAllowsNonProviderOrigins)$'` passed (`0.064s`) after the provider-specific guard and query decode check were added.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore -run 'TestPhase11AccountWindow(Journal_(ProviderAllowanceAppendRequiresObservedProviderClaim|ProviderAllowanceAppendInTxRequiresObservedProviderClaim|ProviderAllowanceQueryRejectsNonProviderRows|ProviderOriginReplayQueryAndProjectionRebuild|GenericAppendStillAllowsNonProviderOrigins)|ProjectionUpgrade_DoesNotPromoteGenericNonProviderRows)$'` passed (`0.076s`), including transactional append and migration-backfill coverage.
- GREEN: `go test -count=1 ./internal/infra/metering/journalstore` passed (`1.976s`).
- GREEN: `go test -count=1 ./internal/core/metering/... ./internal/infra/metering/journalstore/...` passed; all listed core metering packages and journalstore passed.
- GREEN: `go vet ./internal/infra/metering/journalstore/... ./internal/core/metering/...` passed with no diagnostics.
- GREEN: `make test-db-parity-sqlite` passed, including the metering-journal parity entry point.
- GREEN: `go test -count=1 ./internal/archtest -run '^Test(DualPlaneEconomicsPublicPackageDAG|QuotaAuthorityAdapterHasNoFinancialInfrastructureImport|DatabaseParity|Metering)'` passed.
- GREEN: `go test -count=1 ./internal/qa` passed.
- GREEN: `gofmt -d internal/infra/metering/journalstore/account_window_store.go internal/infra/metering/journalstore/20260914000000_metering_account_window_projection.go internal/infra/metering/journalstore/phase11_account_window_test.go internal/infra/metering/journalstore/phase11_account_window_upgrade_test.go` produced no output, and `git diff --check` produced no diagnostics.

### Implementation and acceptance mapping

- `internal/infra/metering/journalstore/account_window_store.go` applies one
  provider-specific validation helper to both append forms. It requires an
  account-window subject, `OriginProvider`, and
  `AuthorityObservedClaim`, while leaving generic `AppendObservation`
  semantics unchanged. History, compatibility-query, and current/as-of
  projection decoding applies the same check after canonical validation and
  returns `metering.ErrInvalidObservation` when a stored local, statement, or
  non-observed-provider row is selected.
- `20260914000000_metering_account_window_projection.go` does not promote a
  valid generic local/statement account-window row during legacy search-column
  backfill. Provider rows continue to backfill and remain queryable; malformed
  canonical rows still fail the migration closed.
- `phase11_account_window_test.go` proves negative local/statement and
  untrusted-provider append behavior, transactional enforcement, generic
  append compatibility, query/projection rejection after a generic component
  rebuild, and exact provider replay/query/projection rebuild preservation.
- `phase11_account_window_upgrade_test.go` proves a legacy backfill containing
  generic non-provider rows exposes only the provider-origin allowance row.
  The core generic account-window reducer was intentionally not tightened.

### Residual risks and skips

- `go test -count=1 ./internal/archtest -run 'Metering|DatabaseParity|UsageEconomics|Package|Boundary'` was also attempted and failed only on the pre-existing runtimebundle package-tree budget (`measured 12622 exceeds budget ceiling 12567`); the narrower directly relevant architecture checks passed.
- `go test -race -count=1 ./internal/infra/metering/journalstore -run 'ProviderAllowance|AccountWindowJournal_ConcurrentOutOfOrderAppendsHaveOneDeterministicHead'` could not start because the Windows Go toolchain `runtime/cgo` `cgo.exe` exited with status 2.
- No live PostgreSQL DSN was configured, so no PostgreSQL runtime execution was claimed. SQLite parity and the existing static dual-dialect migration/schema checks passed.

Status: READY_FOR_REVIEW_11_1_B4

## Phase 11 slice B1: allocation supersession resolution (11.2 blocker 1)

Scope: fail-closed allocation supersession across the public allocation
domain, effective rollup, and durable append/replay boundary. Target-query
isolation, provider producers, admission, and ledger posting remain out of
scope.

### TDD and verification

- RED: `go test ./pkg/lipsdk/economics -run 'TestAllocationSupersession' -count=1` failed to compile before the resolver implementation because the typed supersession result/status, replacement operation, and graph error classifications were undefined.
- RED: `go test ./internal/infra/billingstore -run 'TestPhase11AllocationSupersession' -count=1` reached the new durable regressions before append validation and failed because cross-scope, forked-successor, and cycle appends returned nil.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics ./internal/core/billing ./internal/infra/billingstore` passed. Domain tests cover missing predecessor pending state, valid correction/replacement in shuffled order, payload-hash and source resource/tenant/account/period/basis/policy/currency/unit incompatibility, self/cycle, duplicate payload, active-head and fork rejection. Core tests cover pending successors excluded from effective lines and non-payable/incomplete rollup status. Durable tests cover out-of-order predecessor arrival, idempotent replay, cross-scope/hash/fork/cycle rejection, and file-backed restart convergence.
- GREEN: `go vet ./pkg/lipsdk/economics ./internal/core/billing ./internal/infra/billingstore` passed.
- GREEN: `make test-db-parity-sqlite` passed, including the billing allocation dual-dialect schema/parity path.
- GREEN: `gofmt -d` over all B1-owned Go files produced no output; `git diff --check` produced no diagnostics.

### Implementation and acceptance mapping

- `pkg/lipsdk/economics/allocation.go` now exposes a deterministic supersession resolver. It canonicalizes and replay-deduplicates immutable identities, verifies known predecessor payload hashes and source ownership/valuation compatibility, rejects self/cycles, payload conflicts, cross-scope edges, and multiple successor/head forks, and retains absent references as typed pending state. Pending active records are excluded from `Effective`, with `Complete=false` and `Payable=false`; no history is rewritten.
- `internal/core/billing/allocation.go` resolves the domain graph before expanding target lines. The compatibility rollup remains available, while `RollupAllocatedCostsDetailed`/`RollupAllocatedCostsWithStatus` expose pending references and fail-closed payable/completeness state.
- `internal/infra/billingstore/allocation_store.go` validates the complete canonical store history atomically before each new append. A successor may arrive first and remains durable/pending; a later predecessor resolves it. Hash, source-scope, fork, and cycle violations roll back the append. `ResolveAllocationSupersession` reads canonical JSON authority after restart, so replay and arrival order converge without projection-based history reconstruction.
- `phase11_allocation_test.go` and `phase11_allocation_restart_test.go` provide focused durable evidence. The existing allocation migration and immutable target projections remain unchanged; target-query isolation was intentionally not modified.

### Residual risks and skips

- `go test -race -count=1 ./pkg/lipsdk/economics ./internal/core/billing -run 'Allocation|RollupAllocatedCosts'` could not start because the Windows Go toolchain `runtime/cgo` `cgo.exe` exited with status 2.
- No live PostgreSQL DSN was configured. SQLite parity passed; the existing static dual-dialect migration/schema checks remain the available PostgreSQL evidence.
- No admission, provider producer, customer settlement, journal posting, or target-query isolation behavior was changed or certified by this slice.

Status: READY_FOR_REVIEW_11_2_B1

## Phase 11 slice B1 domain follow-up: transitive pending and frozen policy

Scope: in-memory allocation graph reduction only; durable store, query,
producer, admission, and posting behavior remain unchanged.

- RED: `go test -count=1 ./pkg/lipsdk/economics -run TestAllocationSupersession_PendingAncestorTaintsDescendantsUntilResolved` exposed a descendant becoming effective while its ancestor predecessor was absent; a policy-version mismatch was also accepted.
- GREEN: the resolver now propagates unresolved state through known descendants, reports deterministic pending references, excludes every unresolved descendant from `Effective`, and requires exact source ownership/basis/policy/currency/unit compatibility. Shuffled correction/replacement chains converge.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics ./internal/core/billing`, `go test -count=1 ./internal/infra/billingstore -run "Allocation"`, `go vet ./pkg/lipsdk/economics ./internal/core/billing`, and `gofmt -d` over the four domain files passed. Full `internal/infra/billingstore` also passed.

Status: READY_FOR_REVIEW_11_2_B1_DOMAIN

## Phase 11 slice B2 allocation target-scope isolation

Scope: durable allocation target query/migration only. Source query behavior,
allocation authority payloads, and unrelated admission/producer/posting paths
remain unchanged.

- RED: `go test -count=1 ./internal/infra/billingstore -run '^TestPhase11AllocationTarget'` failed to compile before implementation because `BillingAllocationTargetScopeMigrationName` was absent.
- GREEN: target queries now bind the canonical full `TargetSubject` JSON, including tenant/account/period and request/B-leg/pool/window/reset/start/end scope. Cursor hashes include that canonical target binding, so cross-scope cursor reuse is rejected.
- GREEN: denormalized target scope columns are indexed for both SQLite and PostgreSQL, backfilled in stable batches by a transactional forward migration, and checked against canonical allocation authority on target-filtered reads. Drift fails closed with `ErrIdentityConflict`; canonical payload and fingerprint are preserved.
- GREEN: `go test -count=1 ./internal/infra/billingstore -run '^TestPhase11AllocationTarget'`, full `go test -count=1 ./internal/infra/billingstore`, `go test -count=1 ./internal/testkit/dbparity`, `make test-db-parity-sqlite`, `go vet ./internal/infra/billingstore ./internal/testkit/dbparity`, `go test -tags=integration -run '^$' ./internal/infra/billingstore`, `gofmt -d` over owned Go files, and `git diff --check` passed.

### Residual risks and skips

- `go test -race -count=1 ./internal/infra/billingstore -run 'TestPhase11AllocationTarget|TestPhase11AllocationScope'` could not start because the Windows Go toolchain `runtime/cgo` `cgo.exe` exited with status 2.
- No live PostgreSQL DSN was configured; SQLite parity and static dual-dialect migration/schema checks are the available PostgreSQL evidence.

Status: READY_FOR_REVIEW_11_2_B2

## Phase 11 slice B2 follow-up: legacy allocation rollup compatibility gate

Scope: preserve the legacy `RollupAllocatedCosts` signature while refusing to
return lines from a detailed rollup that is incomplete or non-payable. No
allocation resolver, durable store, producer, admission, or posting behavior
was changed.

### TDD and verification

- RED: `go test ./internal/core/billing -run "Test(RollupAllocatedCostsDetailedExcludesPendingSuccessor|LegacyAllocationLines)" -count=1` failed to compile before the compatibility gate because its typed error and gate helper were undefined.
- GREEN: the same focused compatibility tests passed after the legacy wrapper began returning a typed error and no lines for pending, incomplete, and non-payable detailed results; resolved results retain their lines-only behavior.
- GREEN: `go test -count=1 ./internal/core/billing` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/economics` passed.
- GREEN: `go test -count=1 ./internal/infra/billingstore -run "Allocation|allocation|Economics"` passed.
- GREEN: `go vet ./internal/core/billing`, `go vet ./pkg/lipsdk/economics`, and `go vet ./internal/infra/billingstore` passed.
- GREEN: `gofmt -d internal/core/billing/allocation.go internal/core/billing/allocation_test.go` produced no output, and `git diff --check` produced no diagnostics. The owned Go files are untracked in this shared worktree, so `git diff --check` has no tracked patch to inspect.

### Acceptance and caller census

- The legacy wrapper calls `RollupAllocatedCostsDetailed` and returns `nil` lines plus `*AllocationRollupIncompleteError` whenever either `Complete` or `Payable` is false. The error unwraps to `ErrAllocationRollupIncomplete` and retains status, pending refs, and both safety flags for `errors.As`/`errors.Is` classification.
- The detailed API remains the status-aware path and continues to expose known resolved lines plus pending audit references. Resolved legacy calls retain deterministic lines and nil error.
- A repository-wide caller census found no production callers of `RollupAllocatedCosts`; only the focused billing tests use the legacy wrapper, so no consumer migration was required.

### Residual risks and skips

- Race verification was attempted with `go test -race -count=1 ./internal/core/billing ./pkg/lipsdk/economics -run "Allocation|RollupAllocatedCosts"`, but Windows `runtime/cgo` exited with status 2 before tests ran; adjacent allocation persistence concurrency coverage remains in the existing Phase 11 B2 evidence.
- No live PostgreSQL execution was attempted for this wrapper-only change; the adjacent allocation persistence evidence records the host's unavailable PostgreSQL DSN.

Status: READY_FOR_REVIEW_11_2_WRAPPER

## Phase 11 slice C2 blocker: quota policy total ordering (11.3)

Scope: deterministic reduction of distinct as-of provider gauge observations
that share observed/received time, stream, sequence, source-event key, ID and
revision. No quota policy thresholds, source isolation, host wiring, provider
producers, persistence or financial behavior was changed.

### TDD and focused verification

- RED: `go test -count=1 ./pkg/lipsdk/authority -run
  '^TestQuotaPolicy_SameTimeAcquisitionAndLineageHaveTotalOrder$'` failed at
  the new total-order assertion because the old comparator treated distinct
  acquisition/lineage claims as equal.
- GREEN: the focused regression passed after the comparator appended the
  source `IdentityKey` and replay fingerprint as canonical tie-breakers.
  It covers repeated shuffled input and JSON round-trip restart input, with
  different acquisition channels, normalized lineage, and threshold outcomes.
- GREEN: `go test -count=100 ./pkg/lipsdk/authority -run
  '^TestQuotaPolicy_SameTimeAcquisitionAndLineageHaveTotalOrder$'` passed.
- GREEN: `go test -count=1 ./pkg/lipsdk/authority -run '^TestQuota'`,
  `go test -count=1 ./pkg/lipsdk/authority ./pkg/lipsdk/metering`, and
  `go vet ./pkg/lipsdk/authority ./pkg/lipsdk/metering` passed.
- GREEN: the focused dual-plane architecture checks and `go test -count=1
  ./internal/qa` passed; `gofmt -d` over the two owned Go files produced no
  output.

### Implementation and acceptance mapping

- `quotaObservationLess` retains observed/received chronology, stream,
  sequence, source-event, ID and revision ordering, then compares a canonical
  key made from `Observation.IdentityKey()` and `Observation.ReplayFingerprint()`.
- `IdentityKey` contributes acquisition and the complete normalized lineage;
  the replay fingerprint keeps the final comparison aligned with the
  immutable semantic payload. Existing canonical replay conflict handling
  remains the fail-closed path for one identity with divergent payloads.
- As-of filtering, account/pool/window/reset source isolation, gauge
  replacement semantics, thresholds and nonfinancial authority boundaries
  remain unchanged.

Residual risk: race instrumentation and live PostgreSQL execution were not
needed for this pure in-memory policy ordering fix; no persistence or mutable
concurrency path changed.

Status: READY_FOR_REVIEW_11_3_POLICY_ORDER

## Phase 11 slice C2 blocker: cancellation/error decision evidence (11.3)

Scope: preserve the configured nonfinancial quota-unavailable decision and its
safe policy/evidence metadata when a quota reader is canceled or fails, while
retaining the typed context/reader error through authority coordination and the
runtime adapter. No policy, billing, debit, allocation, or persistence scope
was changed.

### TDD and focused verification

- RED: the quota regression observed a zero decision for pre-canceled calls;
  the stage regression observed `ReadinessReady` with no provider
  decision/evidence when a provider returned a decision together with an error;
  and the runtime adapter regression observed an error-only zero decision for a
  populated admission result.
- GREEN: the reader-error assertions confirmed the pre-existing reader failure
  path also retains its unavailable decision after the shared assertions were
  added.
- GREEN: `gofmt -w` on the focused authority/runtime files passed.
- GREEN: `go test -count=1 ./internal/core/authoritycoord -run
  'QuotaRequestProvider_(ReaderErrors|MidReadCancellation|ConfiguredUnavailable)|RequestCoordinator_StageAdmitErrorPreservesProviderDecisionEvidence'`
  passed.
- GREEN: `go test -count=1 ./internal/core/runtime -run
  '^TestUsageAuthorityProviderAdapter_PreservesAdmissionDecisionOnError$'`
  passed.
- GREEN: `go test -count=1 ./internal/core/authoritycoord ./internal/core/runtime`
  passed.
- GREEN: `go test -count=1 ./internal/core/authoritycoord ./internal/core/runtime
  -run 'Quota|Authority|Request|Admission|Concurrency|ProviderAdapter'` passed.
- GREEN: `go vet ./internal/core/authoritycoord ./internal/core/runtime` and
  `git diff --check` passed.
- `go test -race -count=1 ./internal/core/authoritycoord ./internal/core/runtime
  -run 'Quota|ProviderAdapter|StageAdmitErrorPreservesProviderDecisionEvidence'`
  could not start because the Windows Go 1.26.6 `runtime/cgo` tool exited with
  status 2 before test execution.
- The focused architecture checks
  (`go test -count=1 ./internal/archtest -run
  'TestQuotaAuthorityAdapterHasNoFinancialInfrastructureImport|TestDualPlaneEconomicsPublicPackageDAG'`)
  and `go test -count=1 ./internal/qa` passed. A broader archtest selector also
  ran existing package-tree budget checks and remained red because unrelated
  `internal/infra/runtimebundle` measured 12622 versus its 12567 ceiling.

### Implementation and acceptance mapping

- `QuotaRequestProvider` evaluates the policy's explicit unavailable posture
  before returning a pre-cancel error and reuses that path for reader and
  mid-read cancellation failures. It returns generic `DecisionDeny` for both
  quota deny and indeterminate failure actions, preserves `ReadinessUnavailable`,
  the immutable `QuotaPolicyRef`, status/reason attributes, and observation
  provenance where available, and still returns the bounded typed
  `QuotaReaderError` wrapping the context or reader cause.
- Stage admission records a non-zero provider `Decision` returned with an error,
  aggregates its unavailable readiness and bound policy refs/evidence, and
  carries the decision on `UnavailableError`. Required stages still return a
  top-level deny and compensate prior holds; an absent/zero provider decision
  keeps the previous error-only behavior.
- The runtime UsageAuthority adapter maps a populated admission result even when
  the service also returns an error, while retaining zero-result error behavior
  when no decision was supplied. Runtime error wrapping preserves the typed
  `UnavailableError`, provider decision, and reader cause.
- The focused regressions cover pre-cancel, mid-read cancel, reader failure,
  configured deny/indeterminate unavailable postures, typed error identity,
  immutable policy/evidence propagation, stage aggregation, and runtime mapping.

### Residual risks and skips

- No live PostgreSQL or persistence execution was needed: this blocker changes
  only in-memory authority decision/error propagation and does not add a store
  write path.
