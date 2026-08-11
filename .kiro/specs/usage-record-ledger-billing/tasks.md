# Implementation Plan

> **Execution rule:** TDD is mandatory. Each subtask begins with RED characterization/contract tests, then GREEN implementation, then REFACTOR/deletion. Do not cut over financial authority before the relevant store/journal/replay tests exist.
>
> **Scope rule:** this is an architecture migration. Prefer deletion over long-lived compatibility facades. No implementation begins until Kiro approvals are set in `spec.json`.

## Phase 1 — Freeze Runtime/B2BUA Behavior and Define Usage-Record Contracts

- [ ] 1. Establish immutable execution and evidence contracts before changing financial ownership.

- [ ] 1.1 Characterize runtime billing isolation and terminal ownership
  - Add RED tests proving pre-upstream denial performs zero provider/connector work, streaming contains no financial settlement dependency, terminal handoff is exactly once, and billing failure cannot alter retry/output outcome.
  - Preserve client-visible usage wire behavior independently from financial truth.
  - _Boundary: tests + core runtime_
  - _Depends: none_
  - _Validation: `go test ./internal/core/runtime/... ./internal/archtest/...`_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8_

- [ ] 1.2 Define Turn/Leg Usage Record lineage contracts with B2BUA characterization
  - Write RED table tests for one A-leg/zero-one-many B-legs, failover ordering, parallel legs, and multiple A-legs in one session using existing lineage IDs.
  - Introduce internal `TurnUsageRecord` / `LegUsageRecord` value types only after tests define identity and ordering.
  - _Boundary: internal core billing domain + tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/billing/... ./pkg/lipapi/...`_
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5_

- [ ] 1.3 Prove usage-record immutability, privacy, presence, and version binding
  - Add RED tests for authoritative zero vs absent values, price/policy version capture, forbidden prompt/secret fields, identical replay, and conflicting replay.
  - Keep provider wire/SDK objects out of the record.
  - _Boundary: internal core billing domain + tests_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 2.6, 2.7, 2.8, 2.9, 2.10_

- [ ] 1.4 Finalize one B-leg billing-evidence result at adapter/connector boundaries
  - Write RED representative adapter/connector tests for cumulative usage, `FinalizeBilling`, provider zero cost, missing cost, cancellation, and evidence identity.
  - Implement final normalized evidence handoff without changing content-stream ordering or retry semantics.
  - _Boundary: backend plugins/connectors + SDK adapter seam_
  - _Depends: 1.2, 1.3_
  - _Validation: `go test ./internal/plugins/backends/... ./internal/infra/backendplugins/... ./pkg/lipsdk/backendplugin/...`_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8_

## Phase 2 — Specify Customer Exposure and Account Semantics

- [ ] 2. Build pure pessimistic pricing and prepaid/postpaid account policy before persistence.

- [ ] 2.1 Implement RED max-charge tests for normal single-leg customer policies
  - Cover immutable customer price/policy snapshots, input estimation, pessimistic no-discount assumptions, output ceilings, fixed/resource charges, and surfaced-turn charging.
  - Implement `MaxChargeEstimator` only after the test matrix is complete.
  - _Boundary: internal core billing domain policy_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6_

- [ ] 2.2 Extend max-charge tests to multi-leg/multi-model route plans and unknown bounds
  - Cover parallel legs, policies charging multiple B-legs, different candidate model rates, no-upstream-I/O proof, unbounded routes, configured hard ceilings, overflow/currency errors, and diagnostic basis.
  - _Boundary: internal core billing domain policy + routing test seam_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/core/billing/... ./internal/core/routing/...`_
  - _Requirements: 4.7, 4.8, 4.9, 4.10, 4.11, 4.12_

- [ ] 2.3 Implement prepaid/postpaid signed-balance and credit-floor domain tests
  - RED-test account mode/currency, signed `credits - debits` balance, prepaid floor zero, postpaid `-CreditLimit`, and spendable formula with active holds.
  - Use exact integer money and explicit invariants.
  - _Boundary: internal core billing domain policy_
  - _Depends: none_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8_

- [ ] 2.4 Define funding/payment and credit-limit policy behavior
  - RED-test prepaid funding, postpaid payment, credit-limit audit behavior, and unsafe limit reductions.
  - Keep external payment acquisition out of scope and prevent credit-limit changes from generating fake financial postings.
  - _Boundary: internal core billing domain/application_
  - _Depends: 2.3_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 5.9, 5.10, 5.11_

## Phase 3 — Build the Double-Entry and Concurrent Authorization Core

- [ ] 3. Define self-balancing journal rules and concurrent hold invariants in pure/store-contract tests.

- [ ] 3.1 Specify concurrent authorization-hold invariants
  - RED-test same-account concurrent holds, cross-process semantics at the store contract, natural low-credit denial, and no session-scan dependency.
  - _Boundary: billing application/store contract tests_
  - _Depends: 2.1, 2.3_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6_

- [ ] 3.2 Specify denial, idempotency, hold binding, release, and strict-store behavior
  - RED-test payment-required denial before upstream, deterministic hold identity, hold metadata/version binding, execution-never-started release, and fail-closed behavior without atomic durable semantics.
  - _Boundary: billing application/store contract tests_
  - _Depends: 3.1_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 6.7, 6.8, 6.9, 6.10, 6.11_

- [ ] 3.3 Implement double-entry journal value objects and transaction validator
  - RED-test minimum debit/credit counts, debit=credit, one currency/book, immutable posted semantics, idempotent/conflicting source keys, and customer charge posting shape.
  - _Boundary: internal core billing domain policy_
  - _Depends: 2.3_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6_

- [ ] 3.4 Define account-classification, funding, provider-cost, multi-entry revenue, and trial-balance tests
  - RED-test prepaid-liability/postpaid-receivable debit behavior, confirmed funding/payment credits, per-B-leg COGS/payable, multi-credit customer revenue allocation, source correlation, and range trial balance.
  - _Boundary: internal core billing domain policy_
  - _Depends: 3.3_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 7.7, 7.8, 7.9, 7.10, 7.11, 7.12_

## Phase 4 — Add Bun Durable Journal, Holds, and Point-in-Time State

- [ ] 4. Implement the durable billing store using the existing Bun infrastructure.

- [ ] 4.1 Implement the balanced authorization book and replayable reserved exposure
  - RED-test hold/release postings, no financial-balance/revenue effect, reconstructed reserved exposure, materialized reserved parity, independent book balancing, and multi-book operation grouping.
  - _Boundary: billing domain + Bun driven adapter_
  - _Depends: 3.1, 3.3_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8_

- [ ] 4.2 Add Bun billing schema/migrations and durable store models
  - Add RED migration/store tests before schema code for billing accounts, account-policy events, holds, TUR/LUR, journal transactions/entries, and processing state.
  - Reuse `internal/infra/db`; do not add another ORM/connection abstraction.
  - _Boundary: driven adapter + database migrations_
  - _Depends: 1.2, 3.3, 4.1_
  - _Validation: `go test ./internal/infra/billingstore/... ./internal/infra/db/...`_
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5_

- [ ] 4.3 Enforce append-only/idempotent constraints and SQLite/PostgreSQL atomicity
  - RED-test immutable posted rows, DB constraints, same-account locking/version races, no strict-mode memory fallback, and secret-safe database failure.
  - Run optional PostgreSQL integration contract using the repository harness.
  - _Boundary: driven adapter + composition_
  - _Depends: 4.2_
  - _Validation: `go test ./internal/infra/billingstore/...`; `go test -tags=integration ./internal/infra/billingstore/...`_
  - _Requirements: 9.6, 9.7, 9.8, 9.9, 9.10_

- [ ] 4.4 Persist point-in-time customer state inside each user-affecting DB transaction
  - RED-test balance/reserved/available/version before/after for holds, charges, funding/payment; ensure snapshots are transactional and diagnostic-only.
  - _Boundary: billing application + Bun driven adapter_
  - _Depends: 2.3, 4.1, 4.2_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 10.8_

## Phase 5 — Make B2BUA Usage-Record Rating Authoritative in Shadow

- [ ] 5. Build deterministic A-leg customer charging and per-B-leg operator-cost calculation without mutating live financial state.

- [ ] 5.1 RED-test operator cost for every billable B-leg
  - Cover single leg, failover, swallowed failure, parallel legs, different models/providers/rates, and provider authoritative cost.
  - _Boundary: internal core billing calculator_
  - _Depends: 1.4, 2.1_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 11.1, 11.2, 11.3, 11.4, 11.5_

- [ ] 5.2 RED-test customer-policy leg selection and journal attribution
  - Cover surfaced-only customer policy, multi-leg customer policy, revenue component B-leg correlation, provider cost correlation, session read aggregation, and gross-margin traceability.
  - _Boundary: internal core billing calculator + read model tests_
  - _Depends: 5.1_
  - _Validation: `go test ./internal/core/billing/... ./internal/core/controlplane/...`_
  - _Requirements: 11.6, 11.7, 11.8, 11.9, 11.10_

- [ ] 5.3 Implement pure Turn Usage Record -> Billing Result calculation
  - RED-test plain-value execution, customer/operator separation, provider-cost/pass-through semantics, and fallback operator rating from bound rate snapshots.
  - _Boundary: internal core billing calculator_
  - _Depends: 5.1, 5.2_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 12.1, 12.2, 12.3, 12.4, 12.5_

- [ ] 5.4 Complete calculator invariants and shadow comparisons
  - RED-test authoritative zero, checked arithmetic/currency, explanation components, actual>authorized invariant failure, and zero-customer-charge failed turns that still retain operator costs.
  - Shadow-compare representative current monetary outcomes without dual authoritative writes.
  - _Boundary: internal core billing calculator + characterization tests_
  - _Depends: 5.3_
  - _Validation: `go test ./internal/core/billing/... ./internal/core/runtime/...`_
  - _Requirements: 12.6, 12.7, 12.8, 12.9, 12.10_

## Phase 6 — Cut Customer Settlement to Atomic Journal Transactions

- [ ] 6. Make the Bun double-entry store authoritative for holds, customer balance, and B-leg cost posting.

- [ ] 6.1 RED-test the full atomic settlement transaction and idempotent B-leg posting
  - Prove account lock/version, hold verification, customer charge posting, authorization closure, materialized update, settlement marker, and provider-cost idempotency.
  - _Boundary: billing application + Bun driven adapter_
  - _Depends: 4.4, 5.4_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 13.1, 13.2, 13.3, 13.4, 13.5_

- [ ] 6.2 RED-test rollback, prepaid/postpaid floors, and failure atomicity
  - Inject journal imbalance/storage faults, verify full rollback, enforce prepaid `>=0`, postpaid `>=-CreditLimit`, recompute available, and preserve holds on failed commit.
  - _Boundary: Bun store contract + fault tests_
  - _Depends: 6.1_
  - _Validation: `go test ./internal/infra/billingstore/...`_
  - _Requirements: 13.6, 13.7, 13.8, 13.9, 13.10_

- [ ] 6.3 Implement RED reconciliation tests for balance/reserved/available replay and rebuild
  - Reconstruct signed financial balance and authorization exposure, compute availability, compare materialized row, and exercise maintenance-locked rebuild.
  - _Boundary: billing reconciliation application + Bun query adapter_
  - _Depends: 4.1, 6.1_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 14.1, 14.2, 14.3, 14.4, 14.5_

- [ ] 6.4 Complete reconciliation integrity and cross-dialect parity
  - RED-test immutable journal during rebuild, first bad snapshot/transaction diagnostics, trial-balance/conflicting-link failures, SQLite/PostgreSQL deterministic parity, and explicit materialized-cache semantics.
  - _Boundary: billing reconciliation + durable store tests_
  - _Depends: 6.3_
  - _Validation: `go test ./internal/infra/billingstore/...`; `go test -tags=integration ./internal/infra/billingstore/...`_
  - _Requirements: 14.6, 14.7, 14.8, 14.9, 14.10_

## Phase 7 — Durable Processing and Journal-Backed Reporting

- [ ] 7. Make sealed usage-record processing crash-safe and move financial read models to the new truth.

- [ ] 7.1 Implement RED Turn Usage Record persistence/claim/retry contract
  - Prove durable terminal handoff, bounded processing states, simple worker/terminal-work compatibility, and crash-safe idempotent claims.
  - _Boundary: runtime terminal seam + Bun driven adapter_
  - _Depends: 1.3, 4.2_
  - _Validation: `go test ./internal/core/runtime/... ./internal/infra/billingstore/... ./internal/infra/terminalwork/...`_
  - _Requirements: 15.1, 15.2, 15.3, 15.4_

- [ ] 7.2 Implement conservative failure/stale-hold handling and operator queries
  - RED-test hold retention after processor failure, safe stale release gating, bounded queries for stuck records/holds/journal/reconciliation, and no unbounded in-memory history.
  - _Boundary: billing application + query adapter_
  - _Depends: 7.1_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 15.5, 15.6, 15.7, 15.8_

- [ ] 7.3 Move authoritative customer/operator reports to journal + processed usage records
  - RED-test customer balance/spend, operator B-leg costs, separated revenue/COGS, and per-turn linked explanation.
  - _Boundary: control-plane/query/read model_
  - _Depends: 6.1, 7.1_
  - _Validation: `go test ./internal/core/controlplane/... ./internal/core/billing/...`_
  - _Requirements: 16.1, 16.2, 16.3, 16.4_

- [ ] 7.4 Add account-state, trial-balance, reconciliation, and legacy projection reports
  - RED-test mode/balance/limit/reserved/available output, trial balance, reconciliation status, and one-way legacy usage/metering projections.
  - _Boundary: control-plane/query/read model_
  - _Depends: 6.4, 7.3_
  - _Validation: `go test ./internal/core/controlplane/... ./internal/infra/billingstore/...`_
  - _Requirements: 16.5, 16.6, 16.7, 16.8_

## Phase 8 — Cut Over, Delete Legacy Financial Paths, and Ratchet Architecture

- [ ] 8. Complete convergence so the new ledger is the only financial authority.

- [ ] 8.1 Remove stream reconstruction, price mutation, runtime economic dedupe, and raw-event financial settlement
  - Keep RED characterization tests while deleting old production paths and update imports/wiring only after journal settlement is authoritative.
  - _Boundary: core runtime + tokenaccounting/metering cleanup_
  - _Depends: 6.2, 7.4_
  - _Validation: `go test ./internal/core/runtime/... ./internal/core/tokenaccounting/... ./internal/core/metering/...`_
  - _Requirements: 17.1, 17.2, 17.3, 17.4_

- [ ] 8.2 Retire direct legacy financial writes and prove public/adapter boundaries
  - Remove direct runtime token-ledger financial writes; ensure metering is telemetry-only; keep `lipapi` finance-free and provider SDKs at adapter edges.
  - _Boundary: core/runtime, canonical contract, adapter boundaries_
  - _Depends: 8.1_
  - _Validation: `go test ./pkg/lipapi/... ./internal/core/runtime/... ./internal/archtest/...`_
  - _Requirements: 17.5, 17.6, 17.7, 17.8_

- [ ] 8.3 Delete permanent dual-write/compatibility architecture and reject framework creep
  - Inventory consumers, delete obsolete financial paths/facades, and add tests forbidding DI/event-bus/DSL/service-locator alternatives.
  - _Boundary: repository architecture/refactor_
  - _Depends: 8.2_
  - _Validation: `make quality-checks`; `go test ./internal/archtest/...`_
  - _Requirements: 17.9, 17.10_

- [ ] 8.4 Add final architecture/TDD ratchets and run release-grade validation
  - Add forbidden-import/symbol rules proving `route -> authorize -> execute -> TUR -> calculate -> double-entry settle -> journal reports`.
  - Update architecture/package documentation and run focused, unit, race/concurrency where practical, and full QA gates.
  - _Boundary: architecture tests + docs + repository QA_
  - _Depends: 8.3_
  - _Validation: `make quality-checks`; `make test`; `make qa`_
  - _Requirements: 17.11, 17.12_
