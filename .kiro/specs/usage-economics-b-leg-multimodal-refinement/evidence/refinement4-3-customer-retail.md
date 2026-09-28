# Refinement 4.3 customer-retail evidence

## Scope and policy

The default independent-retail path remains call-scoped. `DurableStore` now
requires the immutable `usage_call_records` row before applying
`ApplyCallBillingResult`; a constructed but not durably appended
`CallUsageRecord` is rejected with `billing.ErrCallIncomplete`. This fence is
at the settlement boundary, so direct callers cannot create a speculative
customer debit even when an exposure has already been admitted.

The existing complete-call worker still obtains the closure and complete
B-leg set before rating. The settlement operation remains keyed by
`BillingCallID`, with the existing operation snapshot and journal idempotency
fences. Identical replays are no-ops; changed post-closure retail results are
`ErrOperationConflict` and do not post a correction. There is no approved
provisional independent-retail policy: an unsupported `provisional` selection
mode fails `ErrRetailSelectionInvalid`. Explicit cost pass-through correction
behavior remains on its existing separate seam.

## TDD and verification

- RED: `go test ./internal/infra/billingstore -run '^TestRefinement43CustomerRetailRequiresDurableCallClosure$' -count=1 -v` failed because pre-closure settlement returned nil.
- GREEN: the same command passed after the closure fence was added.
- GREEN: `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/billingcompose/... -count=1` passed.
- GREEN: `make test-db-parity-sqlite` passed, including billingstore.
- PostgreSQL direct parity reached and passed billingstore, then the repository gate failed in the unrelated metering-journal component because PostgreSQL reported `metering_components.value_present` as `int4` while the parity contract expects boolean.
- `-race` was attempted for the focused core and billingstore tests but could not build on this Windows host: the Go 1.26.6 toolchain `cgo.exe` exited with status 2.

Focused regression tests cover no pre-closure debit, worker settlement only
after the durable closure plus complete B-leg set, closure settlement exactly
once, late changed evidence without rebilling, rejected provisional mode, and
independent settlement of resumed calls with distinct `BillingCallID` values
on one A-leg/session. Existing phase-10 selector/rating tests continue to
prove that the independent default charges only the surfaced/winning B-leg;
retry/loser COGS remains on the separate operator path.

## V1/V2 selector review blocker

The scalar V1 adapter now resolves the same `RetailSelectionPolicy` and calls
the same canonical surfaced/winner, named-outcome, and all-attributable
selector used by V2. V1 contributes only its accepted-evidence compatibility
filter; V2 still performs its stricter observation lineage checks. A non-empty
explicit `Retail.Mode` is authoritative over a contradictory legacy `Scope`.
When the explicit mode is empty, `ResolveRetailSelectionPolicy` retains the
approved migration from legacy `Scope`; no historical policy is reinterpreted.

- RED: `go test ./internal/core/billing -run '^TestRefinement43V1' -count=1 -v` failed all four new cases before the shared dispatch: legacy all-potential leakage charged `1103` instead of `103`, an unsurfaced unique winner charged `0`, multiple surfaced candidates returned nil, and V1/V2 selected keys differed (`[]` versus `b-winner`).
- GREEN: the same command passed after the shared selector integration.
- GREEN: `go test ./internal/core/billing/... -count=20` passed, covering repeated selector/rating ordering and existing phase-10 regressions.
- GREEN: shuffled reruns passed: `go test ./internal/core/billing/... -shuffle=on -count=20`, `go test ./internal/infra/billingcompose/... -shuffle=on -count=5`, and `go test ./internal/infra/billingstore/... -shuffle=on -count=1`.
- GREEN: `go test ./internal/core/billing/... -count=1`, `go test ./internal/infra/billingstore/... -count=1`, and `go test ./internal/infra/billingcompose/... -count=1` passed.
- GREEN: focused `go vet` passed for billing, billingstore, and billingcompose; `gofmt -d` and `git diff --check` reported no issues.
- PostgreSQL direct parity was rerun: billingstore and the concurrency lease store passed; the repository gate remains blocked by the unrelated metering-journal `metering_components.value_present` PostgreSQL `int4` versus boolean contract mismatch.

The new cases are
`TestRefinement43V1ScalarExplicitRetailModeOverridesContradictoryLegacyScope`,
`TestRefinement43V1ScalarSurfacedWinnerFallsBackToUniqueOutcomeWinner`,
`TestRefinement43V1ScalarRejectsMultipleSurfacedCandidates`, and
`TestRefinement43V1AndV2ShareFrozenRetailSelectionAndTotal`. They preserve
call-scoped fixed-fee arithmetic and exact V1/V2 equivalent totals while
proving retries and losers remain outside default customer retail.
