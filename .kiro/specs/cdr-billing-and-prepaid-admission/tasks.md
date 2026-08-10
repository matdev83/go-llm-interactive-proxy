# Implementation Plan

> TDD is mandatory. Each production slice begins with failing characterization/contract/property tests, then the smallest implementation, then deletion/refactor. No production billing ownership cutover occurs until the immediately preceding RED tests exist.

## Phase 1 — Freeze Behavior and Introduce CDR Contracts

- [ ] 1. Characterize existing execution/evidence boundaries
- [ ] 1.1 Add RED characterization tests proving billing isolation goals against current runtime
  - Prove zero provider work after an insufficient-credit preflight denial fixture.
  - Freeze retry/failover, output-commit, cancellation, client usage emission, and terminal ownership for representative successful/failed turns.
  - Record current runtime fields/functions that exist only for billing so deletion has an objective inventory.
  - _Boundary: tests / core runtime_
  - _Depends: none_
  - _Validation: `go test ./internal/core/runtime/...`_
  - _Requirements: 1.1, 1.2, 1.4, 1.5, 1.6, 1.7, 12.3, 12.4, 12.5, 12.6_

- [ ] 1.2 Add RED adapter/connector final-evidence contract tests
  - Cover cumulative provider usage, absent cost, authoritative zero cost, sideband accounting evidence, and `FinalizeBilling`.
  - Prove provider evidence finalization does not alter canonical content ordering/cancellation.
  - _Boundary: backend plugins / SDK tests_
  - _Depends: 1.1_
  - _Validation: `go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/...`_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 12.1_

- [ ] 1.3 Define the minimal immutable `TurnCDR` / `AttemptCDR` / evidence contracts with validation tests
  - Preserve quantity/money presence, version identity, surfaced outcome, and privacy constraints.
  - Reject conflicting replay payloads for the same turn identity.
  - Keep CDR types internal and protocol-neutral.
  - _Boundary: internal/core/billing domain contract_
  - _Depends: 1.1, 1.2_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 12.2_

- [ ] 1.4 Add architecture tests for the intended two-seam runtime boundary before implementation
  - Encode forbidden provider-SDK imports into billing and forbidden balance/rating calls from stream handlers as RED tests where current code violates target state.
  - _Boundary: architecture tests_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/archtest/...`_
  - _Requirements: 12.7, 13.1, 13.2, 13.3, 13.6, 13.7_

## Phase 2 — Pessimistic Cost Estimation and Atomic Admission

- [ ] 2. Build deterministic maximum-customer-charge estimation
- [ ] 2.1 Add RED table/property tests for `MaxCustomerCharge`
  - Cover client max vs model max, cache-discount pessimism, fixed/non-token charges, surfaced-only policy, multi-attempt charge policy, unknown bounds, currency mismatch, and overflow.
  - _Boundary: internal/core/billing calculation tests_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.8, 4.9, 4.10, 4.11_

- [ ] 2.2 Implement the smallest typed estimator against immutable pricing/policy snapshots
  - Consume a narrow projection of the side-effect-free routing plan.
  - Reuse checked accounting/rating arithmetic where it reduces code.
  - Do not add provider-specific runtime switches or provider calls.
  - _Boundary: internal/core/billing_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 4.1, 4.2, 4.5, 4.8, 4.9, 4.10, 13.2, 13.4_

- [ ] 2.3 Add RED balance-store contract and concurrency tests
  - Prove atomic compare-and-reserve, idempotent replay, insufficient-capacity denial, exact fixed-point math, and reserve/release semantics.
  - Run high-contention concurrent reservations against the same account and, where supported, both SQLite and PostgreSQL adapters.
  - _Boundary: driven adapter / tests_
  - _Depends: 2.1_
  - _Validation: `go test -race ./internal/infra/billingstore/...`_
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9, 6.1, 6.2, 6.3, 6.5, 6.6, 6.7, 6.8, 13.5_

- [ ] 2.4 Implement one atomic monetary reservation adapter and pre-upstream affordability service
  - Extract/reuse current `reserved/consumed` transaction mechanics rather than duplicating a generalized authority engine.
  - Return a stable insufficient-credit/payment-required error before upstream execution.
  - Bind reservation to turn/account/pricing/policy versions and expiry.
  - _Boundary: internal/core/billing + driven balance adapter_
  - _Depends: 2.2, 2.3_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 1.2, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.8_

## Phase 3 — Final Attempt Evidence and Turn CDR Production

- [ ] 3. Make attempt termination produce final billing evidence
- [ ] 3.1 Implement adapter-owned final evidence behind existing backend/connector seams
  - Collapse repeated cumulative samples at the adapter boundary.
  - Reuse `AccountingEvidence`/`FinalizeBilling` where practical.
  - Keep client usage event emission separate.
  - _Boundary: backend plugins / connector adapter_
  - _Depends: 1.2, 1.3_
  - _Validation: `go test ./internal/plugins/backends/... ./internal/infra/backendplugins/... ./pkg/lipsdk/backendplugin/...`_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 1.7_

- [ ] 3.2 Add RED runtime terminal tests for one sealed CDR per logical turn
  - Cover success, cancellation, swallowed failover, parallel losing attempts, zero output, and adapter finalization failure.
  - Prove CDR contains all attempt outcomes but no prompt/completion content or secrets.
  - _Boundary: core runtime tests_
  - _Depends: 3.1_
  - _Validation: `go test ./internal/core/runtime/...`_
  - _Requirements: 1.3, 2.2, 2.3, 2.6, 3.3, 12.6_

- [ ] 3.3 Implement the terminal CDR assembly/handoff with no stream accumulator
  - Build the CDR from normal attempt terminal results and reservation metadata.
  - Do not introduce token/money running totals, usage-event scans, or a second terminal owner.
  - _Boundary: core runtime terminal integration_
  - _Depends: 3.2_
  - _Validation: `go test ./internal/core/runtime/...`_
  - _Requirements: 1.3, 1.4, 1.5, 2.1, 2.2, 2.3, 2.5, 12.6, 13.3_

- [ ] 3.4 Verify client usage compatibility remains independent of billing CDRs
  - Characterize representative OpenAI/OpenResponses/Anthropic/Gemini usage output where applicable.
  - Prove removing billing consumption of those events does not alter wire payloads.
  - _Boundary: frontend protocol tests_
  - _Depends: 3.3_
  - _Validation: `go test ./internal/plugins/frontends/...`_
  - _Requirements: 1.7, 10.6, 12.3, 12.5_

## Phase 4 — Post-Turn Rating and Exact Settlement

- [ ] 4. Implement pure CDR processing and reservation settlement
- [ ] 4.1 Add RED pure calculation tests for `TurnCDR -> BillingResult`
  - Cover operator cost across failed/losing attempts, customer surfaced-only charge, optional pass-through policy, absent cost fallback, authoritative zero, checked arithmetic, and explanation components.
  - _Boundary: internal/core/billing calculation tests_
  - _Depends: 2.2, 3.3_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 7.8, 7.9_

- [ ] 4.2 Implement deterministic CDR calculation using bound snapshots
  - Keep customer charge and operator cost independent.
  - Do not create a generic billing-rule DSL.
  - _Boundary: internal/core/billing_
  - _Depends: 4.1_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 7.1, 7.3, 7.4, 7.5, 7.6, 7.7, 7.8, 7.9, 13.4_

- [ ] 4.3 Add RED settlement idempotency/invariant tests
  - Prove actual consumption + unused release is atomic.
  - Prove replay cannot double-charge.
  - Prove zero-charge failures release the full hold.
  - Make `actual > reserved` a hard invariant failure path.
  - _Boundary: billing + balance-store contract tests_
  - _Depends: 4.1_
  - _Validation: `go test -race ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7_

- [ ] 4.4 Implement post-turn settlement and safety-block behavior
  - Consume actual customer charge, release remainder, preserve operator cost separately, and record bound violations.
  - Keep settlement independent of client connection state.
  - _Boundary: internal/core/billing + balance store_
  - _Depends: 4.2, 4.3_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7_

## Phase 5 — Durable CDR Queue and Crash Recovery

- [ ] 5. Make post-turn billing recoverable with a small durable state machine
- [ ] 5.1 Add RED CDR-store contract tests
  - Cover append idempotency/conflict detection, pending claim, processed/retryable/terminal-error transitions, bounded queries, and pagination.
  - _Boundary: driven CDR store tests_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/infra/billingstore/...`_
  - _Requirements: 9.1, 9.2, 9.7, 9.8_

- [ ] 5.2 Implement durable sealed-CDR persistence and bounded worker/poller
  - Prefer existing terminal-work scheduling where it fits.
  - Do not introduce Kafka, a generic queue abstraction, CQRS, or a workflow engine.
  - _Boundary: driven adapter + composition root_
  - _Depends: 5.1, 4.4_
  - _Validation: `go test ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 9.1, 9.2, 9.3, 9.8, 12.7_

- [ ] 5.3 Add crash/replay fault-matrix tests
  - Inject failure after reserve, after provider terminal, after CDR append, before/after settlement commit, and before processed marking.
  - Prove hold retention and exactly-once eventual settlement.
  - _Boundary: composed billing/store tests_
  - _Depends: 5.2_
  - _Validation: `go test -race ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 9.4, 9.5, 8.2, 8.7_

- [ ] 5.4 Implement conservative stale-hold recovery and operator diagnostics
  - Release only when turn inactivity + maximum execution deadline + grace are proven.
  - Add bounded stuck-CDR/reservation queries.
  - _Boundary: billing recovery / diagnostics_
  - _Depends: 5.3_
  - _Validation: `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/core/controlplane/...`_
  - _Requirements: 9.5, 9.6, 9.7_

## Phase 6 — Cut Over Authoritative Billing and Reporting

- [ ] 6. Make CDR results the sole monetary truth
- [ ] 6.1 Shadow-compare CDR Billing Results against current monetary outcomes
  - Cover representative providers, retry/failover, parallel race, zero/absent cost, cancellation, and successful client-visible turns.
  - Do not mutate balances twice; only one configured path is authoritative.
  - _Boundary: migration tests / control plane_
  - _Depends: 5.3_
  - _Validation: `go test ./internal/core/runtime/... ./internal/core/controlplane/...`_
  - _Requirements: 10.1, 10.2, 10.3, 13.7_

- [ ] 6.2 Cut authoritative monetary settlement to CDR processing
  - Keep non-money usage/rate controls separate until independently migrated.
  - Remove monetary settlement dependence on raw facts/exposure/lifecycle-stage inputs.
  - _Boundary: core billing / usageauthority migration / composition_
  - _Depends: 6.1_
  - _Validation: `go test ./internal/core/billing/... ./internal/core/usageauthority/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 11.6, 1.1, 1.6, 13.1_

- [ ] 6.3 Move authoritative customer/operator reports to Billing Results
  - Preserve separate perspectives and per-turn explanation.
  - Make legacy observers telemetry-only.
  - _Boundary: control-plane query/read model_
  - _Depends: 6.2_
  - _Validation: `go test ./internal/core/controlplane/...`_
  - _Requirements: 10.1, 10.2, 10.3, 10.6, 10.7_

- [ ] 6.4 Inventory and migrate/delete legacy token/metering consumers
  - Project required compatibility views one-way from CDR/Billing Results.
  - Delete unused ledgers instead of retaining duplicate truth.
  - _Boundary: control plane / compatibility read models_
  - _Depends: 6.3_
  - _Validation: `go test ./internal/core/tokenaccounting/... ./internal/core/metering/... ./internal/core/controlplane/...`_
  - _Requirements: 10.4, 10.5, 10.6, 11.7, 11.8_

## Phase 7 — Delete Stream-Time Accounting

- [ ] 7. Remove obsolete runtime economic machinery
- [ ] 7.1 Delete stream reconstruction and per-event cost enrichment
  - Remove billing use of `streamusage.Reconstruct`, `enrichUsageCost`, and equivalent raw usage-event pricing/reconciliation paths.
  - _Boundary: core runtime / tokenaccounting_
  - _Depends: 6.2_
  - _Validation: `go test ./internal/core/runtime/... ./internal/core/tokenaccounting/...`_
  - _Requirements: 11.1, 11.2_

- [ ] 7.2 Delete runtime economic dedupe/state and duplicate aggregators
  - Remove `internalUsageKeys`, remembered customer/operator usage, token-accounting-finalized state, economic event merges, and duplicate billing aggregation helpers.
  - Preserve only protocol-specific client usage projection helpers with clear names/ownership.
  - _Boundary: core runtime / metering_
  - _Depends: 7.1_
  - _Validation: `go test ./internal/core/runtime/... ./internal/core/metering/...`_
  - _Requirements: 11.3, 11.4, 11.5_

- [ ] 7.3 Remove direct runtime legacy-ledger writes and raw-fact billing paths
  - Keep `metering.Fact` only if independent non-billing consumers remain.
  - _Boundary: runtime / control plane / compatibility_
  - _Depends: 6.4, 7.2_
  - _Validation: `go test ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/controlplane/...`_
  - _Requirements: 11.7, 11.8, 10.4, 10.5_

- [ ] 7.4 Shrink monetary `usageauthority` ownership to the selected reservation primitive or remove it from billing
  - Do not retain Facts/Exposure/multi-authority descriptors merely as a compatibility wrapper around CDR settlement.
  - Preserve unrelated non-money quota/rate behavior.
  - _Boundary: core usageauthority / billing / driven store_
  - _Depends: 7.3_
  - _Validation: `go test ./internal/core/usageauthority/... ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 11.6, 13.1, 13.8_

## Phase 8 — Ratchet the Final Architecture

- [ ] 8. Prove and document the simple end state
- [ ] 8.1 Turn RED architecture tests GREEN and add forbidden-symbol/import ratchets
  - Runtime stream handlers may not rate, settle, mutate balances, process CDRs, or depend on economic reducers.
  - Billing may not import provider SDKs.
  - _Boundary: architecture tests_
  - _Depends: 7.4_
  - _Validation: `go test ./internal/archtest/...`_
  - _Requirements: 11.9, 12.1, 12.7, 13.6_

- [ ] 8.2 Run property/concurrency/restart certification for prepaid safety
  - Exercise arbitrary reserve/settle/release/replay interleavings and concurrent multi-process-capable store semantics.
  - Prove no negative remaining capacity under the bound invariant.
  - _Boundary: tests / driven store_
  - _Depends: 8.1_
  - _Validation: `go test -race ./internal/core/billing/... ./internal/infra/billingstore/...`_
  - _Requirements: 6.3, 6.6, 6.7, 13.5_

- [ ] 8.3 Update architecture/structure/runtime-flow documentation and mark the older convergence spec superseded for implementation
  - Document the single authoritative flow and ownership map.
  - _Boundary: docs / spec governance_
  - _Depends: 8.1_
  - _Validation: `make quality-checks`_
  - _Requirements: 13.9, 13.10_

- [ ] 8.4 Run final repository-wide quality gates and deletion review
  - Verify no old billing path remains authoritative, no permanent dual-write/dual-settlement facade remains, and all characterization tests either survive with new ownership or are intentionally replaced.
  - _Boundary: repository-wide verification_
  - _Depends: 8.2, 8.3_
  - _Validation: `make test && make qa`_
  - _Requirements: 13.7, 13.8, 1.1, 1.3, 13.9_
