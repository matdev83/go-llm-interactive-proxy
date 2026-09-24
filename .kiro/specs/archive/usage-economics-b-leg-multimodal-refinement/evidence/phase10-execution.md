# Phase 10 slice A execution evidence

Scope: parent task 10.1 and refinement task 6.1, limited to the billing-domain retail B-leg selector and billingcompose policy snapshot detachment.

## TDD evidence

- RED: `go test ./internal/core/billing -run TestPhase10RetailSelector -count=1` failed to compile because the new retail selection policy, result, errors and selector were not yet defined.
- GREEN: `go test ./internal/core/billing -run TestPhase10RetailSelector -count=1` passed after the minimal selector implementation.
- GREEN: `go test ./internal/core/billing -run TestPhase10 -count=1` passed, including the explicit all-attributable exposure bound.
- GREEN: `go test ./internal/infra/billingcompose -run TestPhase10 -count=1` passed.
- GREEN: `go test ./internal/core/billing -count=1` passed.
- GREEN: `go vet ./internal/core/billing ./internal/infra/billingcompose` passed.
- GREEN: `go test ./internal/archtest -run 'TestEvaluateBilling(CustomerOperatorIndependence|AttemptSequenceAuthority)' -count=1` passed.
- GREEN: `go test ./internal/qa -run 'TestRootHygiene_DirtyGoFiles|TestChangeSize_LimitAndOverrideContracts|TestQAFastPreflight_ArchitectureBudgets' -count=1` passed.
- GREEN: `go test ./internal/qa -count=1` passed.
- `go test ./... -count=1` reached the affected packages (billing itself passed) but the repository-wide run remains red on baseline architecture/ratchet findings, the malformed `GOWORK=off` import path containing a space, the existing billingcompose 1003-vs-1000 failover expectation, and an unrelated runtimebundle callback gate.
- The broader `go test ./internal/archtest -run 'Billing|billing|Operator|Customer' -count=1` remains blocked by the existing `TestBillingCoreStaysProviderAndPersistenceFree` lipapi dependency finding; this slice adds no lipapi import.
- `go test -race ./internal/core/billing -run TestPhase10RetailSelector -count=1` was attempted but the host toolchain `cgo.exe` exited with status 2 before tests ran.
- The full `go test ./internal/infra/billingcompose -count=1` currently has one unrelated pre-existing failure: `TestResolveCallRatingFailoverSettlesSurfacedModelCard` reports 1003 instead of its 1000 expectation (the existing default fixed charge is included); the focused Phase 10 compose test passes.

## Residual risks

- This slice freezes B-leg and observation references and policy metadata; V2 quantity valuation/detail-line persistence remains a later rating task.
- Submission identity, customer fees/rating extensions, credit operations and settlement are intentionally out of scope (10.2-10.5).
- Supplier COGS selection remains on the existing independent all-leg path; no provider rate or cost lookup was added to the retail selector.

## Phase 10 slice B execution evidence

Scope: parent task 10.2 only. The slice binds trusted submission identity at the
frontend/runtime authority boundary and carries it through immutable B-leg and
BillingCallID closure records. Fees/rating, credits, and settlement remain out
of scope.

### TDD and verification

- RED: `go test ./pkg/lipsdk/submission -count=1` initially failed to compile because the Phase 10.2 neutral authority contract was not yet defined.
- GREEN: `go test ./pkg/lipsdk/submission -count=1` passed with authenticated new/follow-up, tool continuation, historical replay, transport retry, local-command, untrusted-source, scope-isolation, unsupported/incomplete, context-copy and concurrent-binding coverage.
- RED: `go test ./pkg/lipsdk/submission -run 'TestBindSubmissionIdentityMakesMissingAndLifecycleStatesExplicit|TestBindRejectsCrossScopeEvenWhenContinuationIsIncomplete' -count=1` failed when a tool continuation without a BCI was incorrectly treated as incomplete.
- GREEN: the same focused submission tests passed after allowing the runtime to allocate a fresh BillingCallID while retaining the trusted SubmissionID.
- GREEN: `go test ./internal/core/runtime -run 'SubmissionIdentity|StampBillingCallID|BillingLegRecord' -count=1` passed, including fresh follow-up identity, same-call identity conflict rejection, observation spoof protection, and terminal record propagation.
- GREEN: `go test ./pkg/lipsdk/submission ./pkg/lipsdk/auth ./internal/plugins/frontends/openresponses ./internal/core/billing ./internal/core/runtime -count=1` passed.
- GREEN: `go test ./internal/core/runtime/... ./internal/plugins/frontends/... -count=1` passed. An earlier run exposed the existing timing-sensitive `TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome`; the focused test intermittently passes and the complete affected rerun passed without changing that unrelated wire path.
- GREEN: `go vet ./pkg/lipsdk/submission ./pkg/lipsdk/auth ./internal/plugins/frontends/openresponses ./internal/core/billing ./internal/core/runtime` passed.
- GREEN: `go test ./internal/archtest -run '^TestEvaluateBilling(AttemptSequenceAuthorityAcceptsAuthoritativeAdapter|CustomerOperatorIndependenceDetectsCoupling)$' -count=1` passed.
- GREEN: `go test ./internal/qa -run 'TestRootHygiene_DirtyGoFiles|TestChangeSize_LimitAndOverrideContracts|TestQAFastPreflight_ArchitectureBudgets' -count=1` passed.
- `git diff --check` passed.
- `go test -race ./pkg/lipsdk/submission ./internal/core/runtime -run 'SubmissionIdentity|StampBillingCallID' -count=1` was attempted but the Windows host `cgo.exe` exited with status 2 before race tests ran.
- The broader `TestBillingCoreStaysProviderAndPersistenceFree` architecture check remains a pre-existing baseline failure on the repository's `lipapi` dependency finding; this slice adds no billing-to-provider or persistence import.

### Task 10.2 mapping

- Neutral contract: `pkg/lipsdk/submission/identity.go` validates trusted source/kind, keeps continuation SubmissionID stable, supports explicit local/non-billable and unsupported/incomplete outcomes, rejects spoofed sources and cross-scope authorities, and uses defensive context copies.
- Authority adapters: `pkg/lipsdk/auth/decision.go` and OpenResponses HTTP/WebSocket context projection consume only authenticated authority; request metadata is never promoted.
- Runtime consumers: `internal/core/runtime/submission_identity.go`, request preparation and BillingCallID state bind trusted scope, preserve continuation identity, allocate a fresh call identity for a follow-up, and reject same-call SubmissionID changes.
- Durable lineage: terminal facts, attempts, B-leg observations, call closures and complete-call joining carry only the bound SubmissionID; unsupported/provider-carried identity is cleared rather than trusted.

### Residual risks / skips

- WebSocket authentication is handshake-scoped in the existing frontend. Its authenticated decision is carried into each turn; per-frame reclassification remains the caller/adapter responsibility and no last-role heuristic was added.
- The large-body wire lane has no frontend submission authority carrier and therefore remains explicitly unsupported for prompt-priced identity; it continues to use its existing call-only facts.
- No 10.3 fees/rating, 10.4 credits, 10.5 settlement, or lifecycle reopening behavior was implemented.

## Phase 10 slice C execution evidence

Scope: parent task 10.3 plus refinement tasks 6.2 and 6.3. This slice rates
only the frozen customer-retail B-leg quantity set, evaluates call/submission
fixed fees once at their declared trusted scope, and keeps explicit
customer-boundary proxy-service meters separate from inference usage.

### TDD and verification

- RED: `go test ./internal/core/billing -run 'TestPhase10RetailRating|TestPhase10RetailSelector' -count=1` initially failed to compile because `RetailRatingInput`, `RateSelectedRetailBLegs`, and the retail charge-kind constants were not yet defined.
- GREEN: `go test ./internal/core/billing -run 'TestPhase10RetailRating|TestPhase10RetailSelector' -count=1` passed after the focused retail rater, fixed-scope fee pass, proxy-service line assembly, and interrupted-call selector rule were implemented.
- GREEN: `go test ./internal/core/billing/... ./pkg/lipsdk/economics/... -count=1` passed.
- GREEN: `go test ./internal/infra/billingstore/... -count=1` passed; no schema change was needed because the existing valuation persistence seam stores canonical valuation lines (including charge kind and status) independently of supplier/COGS valuations.
- GREEN: `go vet ./internal/core/billing ./internal/infra/billingcompose ./pkg/lipsdk/economics` passed.
- GREEN: `gofmt -w internal/core/billing/retail_rating.go` completed and `git diff --check` passed.
- GREEN: the focused architecture and QA checks from the preceding slices passed: `go test ./internal/archtest -run 'Test(DualPlane|Phase8|UsageAuthority|BillingRuntime|DatabaseParity|CoreOwnership|Hexagonal)' -count=1` and `go test ./internal/qa -run 'Test(RootHygiene_DirtyGoFiles|ChangeSize_LimitAndOverrideContracts|QAFastPreflight_ArchitectureBudgets|DualPlaneReleaseGateEvidencePresent)' -count=1`.
- The complete `go test ./internal/infra/billingcompose/... -count=1` remains red only on the existing `TestResolveCallRatingFailoverSettlesSurfacedModelCard` expectation (got 1003, want 1000); the focused Phase 10 composition path is covered by the billing tests and does not change that baseline expectation.
- `go test -race ./internal/core/billing -run 'TestPhase10RetailRating|TestPhase10RetailSelector' -count=1` was attempted but the Windows host `cgo.exe` exited with status 2 before tests ran.

### Task mapping

- 10.3: `RateSelectedRetailBLegs` freezes selected observation references, rates only selected B-leg quantities, evaluates call and submission fixed fees outside the B-leg loop, returns typed incomplete/non-payable results for missing rate/qualifier/quantity/identity, keeps inference/commercial/proxy valuation views separate, and recomputes the rounded summary from rounded detail lines.
- 6.2: independent customer tariffs, including route/model tariffs and direction/native-unit components, are selected from customer policy snapshots and do not consult provider pricing or unselected retry B-legs; cost pass-through remains an explicit policy basis.
- 6.3: explicit frontend-boundary customer proxy/service observations are accepted only through a separately named proxy-service tariff and charge kind; they cannot enter the inference B-leg predicate or replace selected B-leg quantities.

### Slice-C files touched

- `internal/core/billing/retail_rating.go`
- `internal/core/billing/retail_rating_phase10_test.go`
- `internal/core/billing/component_rater.go`
- `internal/core/billing/component_rating_contract.go`
- `internal/core/billing/call_rating.go`
- `internal/infra/billingcompose/resolver.go`
- `internal/core/billing/retail_selector.go`
- `internal/core/billing/retail_selector_phase10_test.go`

### Residual risks and skips

- 10.4 customer credit-ledger operations and 10.5 supplier-lag settlement were not implemented.
- No new persistence schema or worker wiring was added; the existing `AppendValuation` adapter remains the persistence boundary for the separate customer valuation lines, while supplier/COGS processing remains independent.
- The repository-wide test/architecture baseline remains outside this slice, including the compose 1003-vs-1000 expectation and the Windows race-toolchain failure.

## Phase 10 slice D1 execution evidence

Scope: domain half of parent task 10.4. This slice defines customer-owned
unit-credit/allowance state and the one consumed atomic mutation port. SQL/Bun
storage, runtime composition, and supplier-lag settlement remain out of scope.

### TDD and verification

- RED: `go test ./internal/core/billing -run 'Test(CustomerUnit|EvaluateIncluded|Transition|ApplyCustomer|Concurrent)' -count=1` failed to compile because the customer-unit key, balance, operation, result, allowance decision, transition, and atomic ledger port were not yet defined.
- GREEN: the same focused command passed after the minimal domain implementation and exact-decimal expectation correction.
- GREEN: `go test ./internal/core/billing/... ./pkg/lipsdk/economics/... -count=1` passed.
- GREEN: `go test ./internal/core/billing -count=1` passed.
- GREEN: `go test ./pkg/lipsdk/economics -count=1` passed.
- GREEN: `go vet ./internal/core/billing ./pkg/lipsdk/economics` passed.
- GREEN: `make test-db-parity-sqlite` passed, including `internal/infra/billingstore` parity; no storage/schema changes were made by this slice.
- GREEN: `go test ./internal/infra/billingstore/... -count=1` passed.
- GREEN: `go test ./internal/archtest -run 'Test(DualPlane|Phase8|UsageAuthority|BillingRuntime|DatabaseParity|CoreOwnership|Hexagonal)' -count=1` passed.
- GREEN: `go test ./internal/qa -run 'Test(RootHygiene_DirtyGoFiles|ChangeSize_LimitAndOverrideContracts|QAFastPreflight_ArchitectureBudgets|DualPlaneReleaseGateEvidencePresent)' -count=1` passed.
- GREEN: `gofmt -w internal/core/billing/customer_units.go internal/core/billing/customer_units_phase10_test.go` completed; `git diff --check` passed.
- `go test -race ./internal/core/billing -run 'Test(CustomerUnit|EvaluateIncluded|Transition|ApplyCustomer|Concurrent)' -count=1` was attempted but the Windows host `cgo.exe` exited with status 2 before tests ran.

### Task 10.4 domain mapping

- `CustomerUnitKey` separates trusted customer account, named pool, entitlement period, and canonical metering component/unit/dimensions. `CanonicalKey` is retained for collision-safe adapter comparison; `IdentityKey` is only a bounded index digest.
- `CustomerUnitBalance` keeps granted, available, reserved, and consumed exact decimals with a complete-state conservation invariant. Missing, partial, and conflicting entitlements are typed and fail closed.
- `CustomerUnitOperation` carries idempotency payload identity, expected balance version, monotonic fence, optional reservation identity, and an optional non-negative monetary fallback bound. `CheckCustomerUnitOperationReplay` excludes compare-and-fence values from the semantic fingerprint so identical retries replay while changed payloads conflict.
- `CustomerUnitLedger` exposes only `ApplyCustomerUnitOperation(context.Context, CustomerUnitOperation)`. The adapter contract requires idempotency lookup, row/reservation locking, compare-and-fence, pure transition, and customer monetary/unit posting in one authoritative transaction; there is no read port that could be combined into stale read-then-spend.
- `EvaluateIncludedAllowance` and `TransitionCustomerUnitBalance` preserve exact included/uncovered quantities, support debit and reserve/commit/release movement, and require a bounded monetary fallback for uncovered quantity. Provider account/window gauges have no accepted operation source or customer-balance fields.

### Adapter contract for the next worker

Implement the driven adapter behind `CustomerUnitLedger` with one transaction per
operation: lock the canonical customer balance and operation/idempotency row,
compare the stored canonical key plus operation fingerprint, reject changed
replays with `ErrCustomerUnitOperationConflict`, reject stale version/fence,
invoke `TransitionCustomerUnitBalance`, bind the reservation ID and quantity in
the same transaction for reserve/commit/release, and atomically persist any
customer monetary fallback/exposure posting with the existing exposure and
settlement authority. Return `CustomerUnitOperationReplayed` for an exact
replay. Never derive customer debit from provider account-window snapshots or
supplier gauges, and never expose a separate balance read that callers can use
to implement read-then-write.

### Residual risks and skips

- No SQL/Bun unit-ledger implementation, migration, runtime wiring, or provider/supplier settlement worker was added.
- The pure domain cannot itself bind reservation rows or post money; those obligations are explicit in the adapter contract above.
- Race execution is unverified because the host Windows `cgo.exe` failed before test execution; deterministic concurrent semantics are covered by the mutex-backed atomic-port test fake.

## Phase 10 slice D2 execution evidence

Scope: parent task 10.4 adapter half. This slice adds the durable SQLite and
PostgreSQL customer-unit ledger, transactionally binds reservation and
idempotency rows, and composes optional customer-unit settlement through the
existing authoritative billing store. Supplier/provider gauge settlement and
stream-time token writes remain out of scope.

### TDD and verification

- RED: `go test ./internal/infra/billingstore -run TestDurableStoreCustomerUnitLedger -count=1` initially failed to compile because `DurableStore` did not implement `ApplyCustomerUnitOperation`.
- GREEN: the focused customer-unit ledger tests passed after the migration, durable adapter, reservation lifecycle, replay/conflict checks, fallback handling, and concurrent compare-and-fence implementation were added.
- GREEN: `go test ./internal/core/billing/... ./internal/infra/billingstore/... -count=1` passed.
- GREEN: `go test ./internal/infra/billingstore -count=1` passed, including the shared SQLite contract and settlement integration tests.
- GREEN: `go test ./internal/infra/billingstore -run 'TestDurableStore(CallSettlement|CustomerUnitLedger)' -count=1` passed.
- GREEN: `TestDurableStoreCustomerUnitLedgerPersistsAcrossStoreReopen` passed, proving operation replay/conflict behavior survives SQLite store close/reopen.
- GREEN: `make test-db-parity-sqlite` passed for all catalog components, including the billing component.
- GREEN: `go test ./internal/testkit/dbparity -count=1` passed, including the catalog contract and evidence checks.
- GREEN: `go test ./internal/infra/runtimebundle -run TestComposeBillingDiscoversCustomerUnitLedgerFromAuthoritativeStore -count=1` passed.
- GREEN: `go vet ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/testkit/dbparity` passed.
- GREEN: `go test ./internal/archtest -run 'TestPackageTreeBudgets(Exact|ReportSection)|TestGenericAggregatesContainNoPerFeatureFields' -count=1` passed; the runtimebundle physical-line budget was kept below its checked-in ceiling.
- GREEN: `go test ./internal/qa -run 'TestDatabaseParity' -count=1` passed.
- Security boundary check: `go test ./internal/archtest -run 'Test.*Security' -count=1` remains red on the shared pre-existing `SubmissionID` field expectation in `TestCompactionContinuitySecurity_ContentFreePublicSurfaces`; the D2 adapter has no provider or wire dependency.
- GREEN: `gofmt` was applied to all D2 Go files and `git diff --check` passed.
- PostgreSQL direct: `go test -tags=integration ./internal/infra/billingstore -run TestDBParity_PostgresDirect -count=1 -v` skipped because `LIP_REQUIRE_POSTGRES=1` and `LIP_TEST_POSTGRES_DSN` were not configured on this host.
- Race verification: `go test -race ./internal/infra/billingstore -run 'TestDurableStoreCustomerUnitLedgerConcurrentVersionFencePreventsDoubleSpend' -count=1` could not build because the Windows host `cgo.exe` exited with status 2 before tests ran.
- The broader affected-package command `go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/testkit/dbparity -count=1` passed billingstore and dbparity but remains red in the pre-existing runtimebundle host-loop/callback architecture tests and malformed-import-path check; no D2 test failed.

### Task 10.4 adapter mapping

- `internal/infra/billingstore/20260915000000_billing_customer_units.go` adds additive, dual-dialect tables for canonical customer-unit balances, immutable operation/idempotency records, and reservation rows. Exact decimal coefficients/scales remain separate columns; canonical keys are retained beside SHA-256 identity indexes; SQLite and PostgreSQL both install operation immutability triggers.
- `internal/infra/billingstore/customer_unit_store.go` implements the sole `billing.CustomerUnitLedger` port. Each operation runs in one retryable local transaction, locks/reloads the balance and reservation rows, rechecks operation identity after contention, invokes `TransitionCustomerUnitBalance`, and conditionally writes the expected version/fence. Exact semantic replays return `CustomerUnitOperationReplayed`; changed payloads, canonical identities, or fingerprints return `ErrCustomerUnitOperationConflict`.
- Reserve, commit, and release bind the reservation identity and exact included quantity in the same transaction. Rollback leaves balance, reservation, and operation rows unchanged. The adapter has no provider-account/window or gauge input and exposes no balance-read port.
- `internal/infra/billingstore/call_settlement.go` accepts an optional customer-owned unit operation and concrete fallback charge. It applies the unit mutation in the existing call-settlement transaction, validates the operation's monetary bound, and rolls back both account posting and unit mutation on failure. Settlement fingerprinting includes the unit semantic fingerprint for idempotent replay/conflict behavior.
- `internal/infra/runtimebundle/billing_compose.go` discovers the ledger only from the authoritative store and places it on internal `ProductionOptions`; `pkg/lipruntime.Options` remains untouched. `internal/testkit/dbparity/catalog.go` registers the customer-unit contract and shared behavioral evidence under the existing billing component.

### Residual risks and skips

- No live PostgreSQL direct run was possible without a configured DSN; the shared contract is wired for PostgreSQL and the SQLite parity command is green.
- Race instrumentation is unavailable on this Windows host because `cgo.exe` fails before test execution; normal concurrent SQLite execution proved one applied debit and stale-version rejection for all competing operations.
- The broader runtimebundle baseline remains red on unrelated host-loop settling timeouts, callback-authority graph loading, and malformed import-path checks in the shared Phase 10 worktree. Supplier-lag worker isolation remains Phase 10.5.

## Phase 10 slice E1 execution evidence

Scope: parent task 10.5 first half. Independent customer settlement remains
constructible and runnable when supplier/provider-cost worker infrastructure is
absent or unhealthy. Supplier valuation remains a separate bounded worker and
durable pending path. Explicit cost-pass-through provisional adjustment (E2),
new pricing/rating, submission identity, and credit semantics are out of scope.

### TDD and verification

- RED: `go test ./internal/infra/runtimebundle -run TestBuildProcessBillingRuntimeAllowsIndependentRetailWithoutSupplierWorker -count=1` failed because `buildProcessBillingRuntime` returned `ErrAuthoritativeBillingRequired` after starting the customer worker when provider-cost interfaces were unavailable.
- GREEN: `go test ./internal/infra/runtimebundle -run 'TestBuildProcessBillingRuntime(OwnsResourcesAcrossGenerationLifetime|AllowsIndependentRetailWithoutSupplierWorker)' -count=1` passed after supplier worker construction became optional; the customer worker and terminal sink still register and clean up independently.
- GREEN: `go test ./internal/core/billing -run 'TestPhase10Supplier' -count=1` passed for supplier queue error, supplier valuation error, bounded backlog, delayed valuation, supplier cancellation, and independent customer settlement.
- GREEN: `go test ./internal/core/billing -count=1` passed.
- GREEN: `go test ./internal/infra/billingstore -count=1` passed, including `TestSQLiteCustomerSettlementIndependentOfProviderCostOrdering`, `TestSQLiteCustomerSettlementClosesWhileOperatorRateLookupFails`, and the 10.4 atomic/idempotent settlement tests.
- GREEN: `go test ./internal/infra/billingcompose -run TestPhase10SnapshotCatalogFreezesRetailSelectionPolicy -count=1` passed. The full compose package remains red only on the existing `TestResolveCallRatingFailoverSettlesSurfacedModelCard` expectation (1003 versus 1000).
- GREEN: `go vet ./internal/core/billing ./internal/infra/runtimebundle` passed.
- GREEN: `go test ./internal/archtest -run 'Test(BillingRuntime|CoreOwnership|Hexagonal|DatabaseParity|PackageTreeBudgets)' -count=1` passed after keeping the runtimebundle line budget unchanged; `go test ./internal/qa -run 'Test(RootHygiene_DirtyGoFiles|ChangeSize_LimitAndOverrideContracts|QAFastPreflight_ArchitectureBudgets|DualPlaneReleaseGateEvidencePresent)' -count=1` passed.
- GREEN: `gofmt` and `git diff --check` passed.
- Race verification: `go test -race ./internal/core/billing -run 'TestPhase10Supplier' -count=1` could not build because the Windows host `cgo.exe` exited with status 2 before tests ran.
- The full `go test ./internal/infra/runtimebundle -count=1` remains red on pre-existing host-loop settlement timeouts, callback-authority graph loading, and malformed import-path checks; the focused process-billing lifecycle tests pass.

### E1 isolation mapping

- `internal/infra/runtimebundle/process_billing.go` keeps customer call-post-usage worker startup authoritative, but treats provider-cost worker wiring as optional. Missing supplier queue interfaces or a supplier resolver no longer reject independent customer runtime construction or leak the already-created customer worker through an error return.
- `internal/infra/runtimebundle/process_billing_test.go` supplies a customer/reporting/call-usage store without provider-cost ports and proves terminal sink plus customer worker ownership is registered and closed.
- `internal/core/billing/supplier_isolation_phase10_test.go` proves supplier queue and valuation errors do not prevent a frozen customer result; a batch-limited backlog drains separately; and canceling a delayed supplier resolver terminates only supplier work while customer settlement completes with the same retail amount.
- Existing durable-store tests prove customer settlement and provider COGS can occur in either order without changing customer balance/exposure, and missing operator-rate evidence leaves customer settlement complete while provider work remains pending/unreconciled. Existing store code retains separate customer/provider transactions and customer account-lock ownership.

### Residual risks / skips

- No statement-import worker or explicit cost-pass-through provisional adjustment was added; E2 remains intentionally unimplemented.
- A missing supplier worker is now fail-open for independent retail composition, but strict terminal append durability still owns the immutable B-leg plus provider-work enqueue transaction. A database outage at that shared terminal durability boundary remains a degraded/recoverable accounting condition rather than a silently accepted record.
- The full compose/runtimebundle package baselines and Windows race toolchain remain outside this slice as recorded above.

## Phase 10 slice E2 execution evidence

Scope: parent task 10.5 explicit cost-pass-through customer settlement. This
slice adds only the frozen customer policy, bounded pending/provisional outcome,
and one durable late-authoritative adjustment path. Independent retail never
consults provider cost and remains unchanged when no explicit cost-pass-through
policy is present.

### TDD and verification

- RED: `go test ./internal/core/billing -run 'TestPhase10CostPassThrough|TestPhase10RateCallPassThrough|TestPhase10IndependentRetailRejectsEmbeddedPassThroughPolicy' -count=1` initially failed at compile time because the cost-pass-through policy, settlement, provider-cost, and result fields did not exist.
- RED: `go test ./internal/infra/billingstore -run 'TestSQLitePhase10CostPassThrough' -count=1` initially failed at compile time because `DurableStore` had no cost-pass-through revision port or persisted head implementation.
- GREEN: the focused core rating tests passed. Explicit pending returns zero customer charge; provisional returns the approved safe bound; an authoritative provider amount is accepted only when complete, reconciled, authoritative, hash-qualified, currency-matching, and within the bound. A policy with independent retail basis plus embedded pass-through policy is rejected.
- GREEN: the focused SQLite tests passed. A revision from 100 to 80 posts one negative 20 delta; a superseding 70 revision posts 10 from the current 80 head; replay is a no-op; stale/out-of-order input is a no-op; concurrent identical workers produce one applied result and seven replays; negative/refund deltas and currency fail-closed behavior are covered.
- GREEN: `go test ./internal/core/billing -run 'TestPhase10CostPassThrough|TestPhase10RateCallPassThrough|TestPhase10IndependentRetailRejectsEmbeddedPassThroughPolicy' -count=1` passed.
- GREEN: `go test ./internal/infra/billingstore -run 'TestSQLitePhase10CostPassThrough|TestSQLiteBillingSchemaCreatesRequiredTablesAndIndexes|TestSQLiteBillingSchemaMigrationIsIdempotent' -count=1` passed.
- GREEN: `go test ./internal/infra/runtimebundle -run 'TestComposeBillingDiscoversOptionalCostPassThroughSettlementStore' -count=1` passed; the seam is optional and discovered only from the authoritative store.
- GREEN: `go test ./internal/testkit/dbparity -count=1` passed.
- GREEN: `make test-db-parity-sqlite` passed for all catalog components, including the new billing head/adjustment schema.
- GREEN: `go vet ./internal/core/billing ./internal/infra/billingstore ./internal/infra/billingcompose ./internal/infra/runtimebundle ./internal/testkit/dbparity` passed.
- GREEN: `go test ./internal/archtest -run 'TestPackageTreeBudgets(Exact|ReportSection)' -count=1` passed after keeping the runtimebundle measured line count within its existing ceiling.
- GREEN: `go test ./internal/qa -run 'Test(RootHygiene_DirtyGoFiles|ChangeSize_LimitAndOverrideContracts|QAFastPreflight_ArchitectureBudgets|DualPlaneReleaseGateEvidencePresent)' -count=1` passed.
- GREEN: `gofmt` was applied to touched Go files and `git diff --check` passed.
- Race verification: `go test -race ./internal/core/billing ./internal/infra/billingstore -run 'TestPhase10CostPassThrough' -count=1` could not build because the Windows host `cgo.exe` exited with status 2 before test execution.
- PostgreSQL direct execution was not available because no `LIP_TEST_POSTGRES_DSN` was configured; SQLite parity and the dual-dialect migration/SQL paths are covered, but live PostgreSQL behavior remains unverified.
- The broader affected-package command still has the pre-existing billingcompose failover expectation mismatch (1003 versus 1000) and runtimebundle host-loop/callback/malformed-import-path failures; core billing and billingstore pass, and no E2-focused test failed.

### E2 implementation mapping

- `internal/core/billing/cost_pass_through.go` defines explicit missing-cost policy, safe-bound validation, trusted provider revision identity, immutable settlement state, signed deltas, and typed fail-closed errors. `internal/core/billing/retail_rating.go`, `call_rating.go`, `retail_selector.go`, and `estimate.go` route only an explicit cost-pass-through basis through this state; ordinary retail pricing and admission remain independent.
- `internal/infra/billingstore/20260916000000_billing_cost_pass_through.go` adds the dual-dialect mutable head schema. `cost_pass_through_store.go` validates provider input before opening the account transaction, locks/reloads the current head, rejects conflicting/stale revisions, derives each adjustment from the current posted amount, writes balanced customer/clearing journal entries, and advances head version/fence plus an idempotent operation snapshot atomically.
- `internal/infra/billingstore/call_settlement.go` persists the initial pending/provisional/final state in the same customer settlement transaction. The original transaction identity or pending settlement operation key is retained as the correction group; immutable journal history is never rewritten.
- `internal/core/billing/append.go`, `internal/infra/runtimebundle/production_options.go`, `internal/infra/runtimebundle/billing_compose.go`, and `internal/testkit/dbparity/catalog.go` add only the optional settlement-store seam and parity registration. No provider worker, tariff, customer-unit ledger, or public runtime-money option was broadened.

### Residual risks and skips

- No live PostgreSQL or race-instrumented execution was possible on this host. The normal concurrent SQLite test proves the transaction/idempotency outcome; it is not a substitute for race-toolchain evidence.
- Late adjustment depends on an authoritative, reconciled provider revision supplied by the caller; provider acquisition remains outside this slice. Missing, untrusted, incomplete, mismatched, over-bound, or conflicting revisions fail closed.
- `READY_FOR_SLICE_REVIEW_10_5_PASSTHROUGH`: the explicit pass-through provisional/pending policy, bounded settlement, deterministic current-head adjustment, replay/out-of-order convergence, refund delta, policy-forbidden late adjustment, and independent-retail isolation scope are implemented and verified within the slice.

## Phase 10 slice E3 execution evidence

Scope: parent tasks 10.2–10.3 submission fixed-fee settlement. A trusted
`SubmissionID` may produce one durable customer submission-fee claim per
account and store, while call-scoped fees remain charged for every distinct
`BillingCallID`. Tariff/policy snapshot identity is frozen into the claim;
new submissions remain independently billable.

### TDD and verification

- RED: `go test ./internal/infra/billingstore -run 'TestSQLiteSubmissionFeeSettlement' -count=1` initially failed before the claim implementation: two concurrent same-submission calls left balance `970` instead of `975`, replay/scope coverage left balance `970` instead of `975`, and the frozen-context case returned nil instead of `ErrOperationConflict`.
- GREEN: `go test ./internal/infra/billingstore -run 'TestSQLiteSubmissionFeeSettlement|TestSQLiteSubmissionFeeClaim' -count=1` passed. Coverage includes concurrent same-submission settlement, same-call replay, same-call tariff/policy conflicts, different-call context conflict, distinct account/store scope, distinct `SubmissionID`, and rollback-safe claim insertion.
- GREEN: `go test ./internal/core/billing ./internal/infra/billingstore -count=1` passed.
- GREEN: `go test ./internal/testkit/dbparity -count=1` passed, including the billing component catalog and durable evidence checks.
- GREEN: `make test-db-parity-sqlite` passed for every registered parity component, including billing submission-fee claims.
- GREEN: `go vet ./internal/core/billing ./internal/infra/billingstore ./internal/testkit/dbparity` passed.
- GREEN: `gofmt` was applied to touched Go files and `git diff --check` passed.
- Race verification: `go test -race ./internal/infra/billingstore -run 'TestSQLiteSubmissionFeeSettlementIsClaimedOnceConcurrently' -count=1` could not build because the Windows host `cgo.exe` exited with status 2 before test execution.
- PostgreSQL direct execution: `go test -tags=integration ./internal/infra/billingstore -run TestDBParity_PostgresDirect -count=1 -v` skipped because `LIP_TEST_POSTGRES_DSN` and `LIP_REQUIRE_POSTGRES` were not configured; dual-dialect DDL and schema-verification paths are covered, but live PostgreSQL behavior remains unverified.
- `go test ./internal/archtest ./internal/qa -count=1` passed the QA package but the shared Phase 10 worktree's archtest remains red on pre-existing request-attempt ratchets/baseline, content-free public-surface expectation, billing import boundary, AST file-count baseline, core line budget, and malformed import-path checks.

### E3 implementation mapping

- `internal/core/billing/submission_fee.go` derives the claim only from the
  complete customer valuation's submission-scoped fixed lines and the trusted
  call `SubmissionID`; its fingerprint includes the frozen tariff/policy
  references/content and immutable fee-line context, never client subject
  identity or B-leg observations.
- `internal/infra/billingstore/20260917000000_billing_submission_fee_claims.go`
  adds the forward SQLite/PostgreSQL claim table with a unique
  `(store_id, account_id, submission_id)` scope, account foreign key,
  nonnegative amount check, and immutable update/delete protection. Migration
  registration, schema verification, and `internal/testkit/dbparity` coverage
  are included.
- `internal/infra/billingstore/call_settlement.go` loads and compares the
  frozen claim under the existing account settlement transaction, subtracts a
  previously claimed submission fee so each call still posts its own fee, and
  inserts the first claim in that same transaction. Account locking plus the
  unique scope key makes concurrent attempts converge; rollback leaves no
  claim or debit.

### Residual risks / skips

- No live PostgreSQL or race-instrumented execution was possible on this host;
  normal concurrent SQLite settlement and retryable account transactions are
  green.
- The settlement boundary expects the complete customer valuation produced by
  the trusted retail path when a submission fee is present. A result that omits
  both the submission-fee line and any durable claim signal remains outside
  this narrowly scoped adapter contract.

`READY_FOR_SLICE_REVIEW_10_2_10_3_SUBMISSION_FEE_ONCE`: durable once-per-submission claim, tariff/policy conflict protection, replay/concurrency/rollback/scope coverage, and dual-dialect schema wiring are implemented and verified within this slice.
