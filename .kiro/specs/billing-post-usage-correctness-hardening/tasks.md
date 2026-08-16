# Implementation Plan

## Execution Rules

- TDD mandatory: RED -> GREEN -> REFACTOR.
- Preserve one monetary authority: exposure admission + post-usage financial settlement.
- Do not begin successor-spec deletion work inside this implementation.
- Every phase must remain buildable and preserve no-retry-after-output.
- Four executable subtasks per phase.

## Phase 0 — Characterize the Blocking Defects

- [ ] 0.1 Add RED regression for reversed lexical B-leg IDs versus real attempt sequence.
  - Cover failed/canceled/no-surfaced selection and prove current positional reconstruction selects the wrong leg.
  - _Boundary: core billing + B2BUA characterization tests_
  - _Depends: none_
  - _Validation: go test ./internal/core/billing ./internal/core/b2bua_
  - _Requirements: 2.1-2.8, 8.1, 8.4_

- [ ] 0.2 Add RED mixed-model customer-pricing regressions.
  - Prove admission sees route-specific prices while current settlement loses them.
  - _Boundary: billingcompose + billing rating tests_
  - _Depends: none_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingcompose ./internal/infra/billingadmission_
  - _Requirements: 4.1-4.8, 8.1, 8.4_

- [ ] 0.3 Add RED customer/operator snapshot independence regression.
  - Missing operator rate must currently reproduce the customer-settlement blockage.
  - _Boundary: billingcompose / post-usage worker tests_
  - _Depends: none_
  - _Validation: go test ./internal/infra/billingcompose ./internal/core/billing_
  - _Requirements: 5.1-5.7, 8.2_

- [ ] 0.4 Add retained-state characterization and architecture baseline.
  - Exercise many BillingCallIDs through one executor and record current collector growth plus relevant production symbols.
  - _Boundary: runtime tests / architecture baseline_
  - _Depends: none_
  - _Validation: go test ./internal/core/runtime ./internal/archtest_
  - _Requirements: 6.1-6.9, 8.3, 8.6-8.7_

## Phase 1 — Preserve B-Leg Sequence Durably

- [ ] 1.1 Add sequence-aware `CallLegUsageRecord` contract and v1/v2 replay tests.
  - New writes require positive `AttemptSeq`; existing v1 rows remain verifiable.
  - _Boundary: core billing contracts_
  - _Depends: 0.1_
  - _Validation: go test ./internal/core/billing -run 'CallLeg|Sequence|Replay'_
  - _Requirements: 2.1-2.8, 3.2-3.6_

- [ ] 1.2 Add Bun sequence migration and cross-dialect store support.
  - Nullable legacy column, unique known sequence per call, no guessed backfill.
  - _Boundary: billingstore migrations / persistence_
  - _Depends: 1.1_
  - _Validation: go test ./internal/infra/billingstore -run 'Migration|CallLeg|Sequence'; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Sequence|PostgresBillingStoreContract'_
  - _Requirements: 2.3-2.5, 3.1-3.7, 8.5_

- [ ] 1.3 Thread exact B2BUA sequence through all terminal leg producers.
  - Cover opened, never-started, failed-open, swallowed, parallel loser/winner and cancellation paths.
  - _Boundary: runtime B2BUA -> billing record seam_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/runtime -run 'Billing.*Leg|Parallel|Abort|Failover'_
  - _Requirements: 2.1-2.8, 7.1-7.2_

- [ ] 1.4 Make customer leg selection consume only authoritative sequence.
  - Remove positional reconstruction; legacy unknown sequence fails closed only when order is required.
  - _Boundary: core billing rating_
  - _Depends: 1.1, 1.3_
  - _Validation: go test ./internal/core/billing -run 'RateCall|Sequence|Canceled|Failed'_
  - _Requirements: 2.6-2.8, 3.5-3.6, 8.4_

## Phase 2 — Correct Customer Pricing and Resolver Independence

- [ ] 2.1 Split customer snapshot resolution from operator-rate resolution.
  - Customer path resolves only customer pricing/policy/model cards.
  - _Boundary: billingcompose_
  - _Depends: 0.3_
  - _Validation: go test ./internal/infra/billingcompose_
  - _Requirements: 5.1-5.7_

- [ ] 2.2 Carry model-specific pricing through `CallRatingInput`.
  - Remove unused operator-rate collection from customer rating input.
  - _Boundary: core billing contracts_
  - _Depends: 0.2, 2.1_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingcompose_
  - _Requirements: 4.1-4.7, 5.2, 5.6_

- [ ] 2.3 Rate selected B-legs with their effective backend/model customer cards.
  - Default only when no applicable override exists; missing required card fails explicitly.
  - _Boundary: core billing rating_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/billing -run 'RateCall|Model|Pricing|Failover'_
  - _Requirements: 4.1-4.8_

- [ ] 2.4 Prove provider-rate failure cannot hold customer exposure open.
  - Customer operation settles/closes while provider-cost work stays pending/unreconciled.
  - _Boundary: post-usage integration / billingstore_
  - _Depends: 2.1, 2.3_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/billingcompose -run 'Customer|Provider|Exposure|OperatorRate'_
  - _Requirements: 1.5, 5.1-5.7, 8.4_

## Phase 3 — Replace Executor-Lifetime Billing State

- [ ] 3.1 Introduce private request/BillingCallID-scoped state with RED ownership tests.
  - Allocate once per prepared invocation and define shared retry/parallel/interleaved ownership.
  - _Boundary: runtime private lifecycle_
  - _Depends: 0.4_
  - _Validation: go test ./internal/core/runtime -run 'BillingCallState|BillingCallID|Interleaved|Parallel'_
  - _Requirements: 6.1-6.6_

- [ ] 3.2 Move allocated-leg set and terminal timing bounds into call-scoped state.
  - Closure reads set/timing only; no financial evidence aggregation.
  - _Boundary: runtime terminal billing seam_
  - _Depends: 1.3, 3.1_
  - _Validation: go test ./internal/core/runtime -run 'Billing.*Closure|Billing.*Leg|Abort'_
  - _Requirements: 6.1-6.7, 7.1-7.2_

- [ ] 3.3 Move `FinalizeBilling` single-flight into call-scoped state.
  - External backend finalization occurs outside call-state lock; racing terminal paths share result.
  - _Boundary: runtime/backend billing-finalization seam_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/runtime -run 'FinalizeBilling|Parallel|Close|Terminal'_
  - _Requirements: 6.2-6.6, 6.9, 7.4-7.5_

- [ ] 3.4 Delete executor-global lifetime-growing billing-call maps.
  - Remove obsolete collector eviction helpers and prove no completed call remains reachable from `Executor`.
  - _Boundary: runtime simplification_
  - _Depends: 3.2, 3.3_
  - _Validation: go test ./internal/core/runtime ./internal/archtest_
  - _Requirements: 6.1-6.9, 8.6_

## Phase 4 — Brownfield and B2BUA Integration Hardening

- [ ] 4.1 Add legacy sequence-unknown processing tests.
  - Completed surfaced and charge-all sequence-independent cases may settle; sequence-dependent ambiguous cases reconcile.
  - _Boundary: billing processor/store_
  - _Depends: 1.2, 1.4_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore -run 'Legacy|Sequence|CompleteCall|Reconcile'_
  - _Requirements: 3.1-3.7, 8.5_

- [ ] 4.2 Add full failover/parallel/mixed-model B2BUA billing matrix.
  - Include opaque reverse-order IDs, different rates, rejected/never-started legs, surfaced winner and cancellations.
  - _Boundary: runtime + billing integration_
  - _Depends: 1.4, 2.3, 3.4_
  - _Validation: go test ./internal/core/runtime ./internal/core/billing ./internal/infra/billingstore_
  - _Requirements: 2.8, 4.7-4.8, 7.1-7.5, 8.4-8.5_

- [ ] 4.3 Prove post-output persistence failures still cannot cause provider retry.
  - Preserve current durable append/outbox semantics while using call-scoped state.
  - _Boundary: runtime terminal failure tests_
  - _Depends: 3.4_
  - _Validation: go test ./internal/core/runtime ./internal/infra/runtimebundle -run 'Billing|Append|Retry|Output'_
  - _Requirements: 1.4, 7.3, 7.6_

- [ ] 4.4 Run targeted race and retained-memory tests.
  - Repeated calls on one executor, Recv/Close races, parallel terminalization, finalization single-flight.
  - _Boundary: concurrency / runtime lifecycle_
  - _Depends: 3.4, 4.2_
  - _Validation: go test -race ./internal/core/runtime -run 'Billing|Parallel|Close|CallState'_
  - _Requirements: 6.8-6.9, 7.4, 8.8_

## Phase 5 — Ratchets and Final Certification

- [ ] 5.1 Add architecture guards for sequence, pricing, resolver and state ownership.
  - Forbid positional/lexical financial order, customer->operator-rate coupling, and executor-global billing call registries.
  - _Boundary: architecture tests_
  - _Depends: 1.4, 2.4, 3.4_
  - _Validation: go test ./internal/archtest_
  - _Requirements: 8.6-8.7_

- [ ] 5.2 Re-run billingstore SQLite/PostgreSQL contract and replay tests.
  - Include migration from pre-sequence schema plus new writes.
  - _Boundary: persistence certification_
  - _Depends: 4.1_
  - _Validation: go test ./internal/infra/billingstore; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Billing|Postgres.*Sequence'_
  - _Requirements: 3.1-3.7, 8.5, 8.8_

- [ ] 5.3 Update billing architecture/host docs for corrected semantics.
  - Document attempt sequence as authoritative fact and customer/provider snapshot independence; do not document successor deletions as completed.
  - _Boundary: docs / steering_
  - _Depends: 5.1_
  - _Validation: make docs-check; go test ./internal/archtest_
  - _Requirements: 1.1-1.6, 2.1-2.8, 4.1-5.7, 6.1-6.9_

- [ ] 5.4 Perform final spec conformance review and establish successor baseline.
  - Verify every criterion, no hold regression, no wrong-price/sequence behavior, bounded runtime state, and record exact main SHA/production shape for the convergence spec.
  - _Boundary: final certification_
  - _Depends: 5.1, 5.2, 5.3_
  - _Validation: go test ./internal/core/billing ./internal/core/runtime ./internal/infra/billingstore ./internal/infra/billingcompose ./internal/infra/billingadmission ./internal/infra/runtimebundle ./internal/archtest; make test-unit; make quality-checks; make test-race where supported_
  - _Requirements: 1.1-1.6, 2.1-2.8, 3.1-3.7, 4.1-4.8, 5.1-5.7, 6.1-6.9, 7.1-7.6, 8.1-8.9_

## Completion Gate

Do not mark this spec completed unless all P0 defects from the post-merge review have dedicated regression tests and the resulting implementation is the baseline used by `billing-architecture-final-convergence`.
