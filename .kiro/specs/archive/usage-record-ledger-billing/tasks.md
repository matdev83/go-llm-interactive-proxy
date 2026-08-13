# Implementation Plan

Implementation is RED -> GREEN -> REFACTOR. Each phase has at most four executable subtasks. Characterization/shadow evidence remains until the replacement path is proven and the old financial path is deleted.

## Phase 1 — Freeze Boundaries and Define Immutable Evidence

- [x] 1.1 Characterize current runtime/B2BUA/provider billing behavior before ownership changes
  - Freeze pre-upstream denial, failover/parallel B-leg lineage, terminal ownership, client usage wire behavior, and representative provider/connector final evidence.
  - _Boundary: tests / runtime characterization_
  - _Depends: none_
  - _Validation: focused runtime/backend tests plus `make test-unit`_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.7, 11.8, 17.7, 17.8_

- [x] 1.2 Add RED contracts for TUR/LUR values and B2BUA attribution
  - Define protocol-neutral immutable TUR/LUR types and tests for A-leg/B-leg/model/provider/outcome/presence semantics.
  - _Boundary: internal/core/billing domain values_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.7, 11.8_

- [x] 1.3 Add durable TUR/LUR keys and semantic replay fingerprints
  - Test account+turn TUR identity, TUR+BLeg LUR identity, same-key/same-fingerprint replay, and same-key/different-fingerprint rejection.
  - _Boundary: billing domain + store contract tests_
  - _Depends: 1.2_
  - _Validation: focused identity/property tests_
  - _Requirements: 2.2, 2.3, 2.9, 2.10, 13.4, 13.5, 13.6_

- [x] 1.4 Implement final adapter/connector B-leg evidence handoff in shadow mode
  - Reuse existing accounting evidence/FinalizeBilling seams; do not alter stream financial behavior yet.
  - _Boundary: backend adapters / connector SDK adapter seam_
  - _Depends: 1.2_
  - _Validation: representative backend and connector tests_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 17.8_

## Phase 2 — Build the Durable Bun Journal Foundation

- [x] 2.1 Add RED Money/account/journal invariant tests
  - Cover signed prepaid/postpaid balance, floor, spendable formula, exact money arithmetic, balanced transaction rules, and immutability.
  - _Boundary: billing domain policy_
  - _Depends: 1.2_
  - _Validation: pure table/property tests_
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.9, 7.1, 7.2, 7.3, 7.11_

- [x] 2.2 Add Bun billing schema and migrations
  - Create accounts, policy audit, holds, immutable TUR/LUR, separate processing state, journal transactions/entries, indexes and constraints.
  - _Boundary: internal/infra/billingstore / DB migrations_
  - _Depends: 2.1_
  - _Validation: migration tests on SQLite and optional PostgreSQL harness_
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7, 9.8, 15.1, 15.2, 15.3_

- [x] 2.3 Add journal semantic idempotency, correction links, and deterministic AccountSequence
  - RED tests first for fingerprint compare-before-no-op, reversal/replacement/group linkage, `(account, sequence)` uniqueness, and concurrent sequence allocation.
  - _Boundary: billing store + journal domain_
  - _Depends: 2.2_
  - _Validation: store contract/concurrency tests_
  - _Requirements: 7.5, 7.6, 7.7, 7.13, 14.1, 14.2, 14.3, 14.4_

- [x] 2.4 Add SQLite/PostgreSQL parity contract suite
  - Prove balanced posting, replay, correction, snapshot, account locking/versioning, and crash rollback semantics are dialect-equivalent.
  - _Boundary: tests / durable adapters_
  - _Depends: 2.2, 2.3_
  - _Validation: SQLite default + PostgreSQL integration when configured_
  - _Requirements: 9.2, 9.6, 9.7, 9.8, 14.7_

## Phase 3 — Implement Pessimistic Authorization

- [x] 3.1 Add pure MaxCustomerCharge estimator with exact snapshot binding
  - RED tables for input/output bounds, fixed/resource charges, route/model alternatives, customer policy, unknown/unbounded cases, and overflow.
  - _Boundary: billing domain/application_
  - _Depends: 2.1_
  - _Validation: pure estimator tests_
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.8, 4.9, 4.10_

- [x] 3.2 Add atomic prepaid/postpaid authorization hold
  - Require `SpendableBefore >= MaxCustomerCharge`, then prove post-hold spendable non-negative in the same transaction.
  - _Boundary: billing application + Bun store_
  - _Depends: 2.2, 2.3, 2.4, 3.1_
  - _Validation: store authorization contract tests_
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9, 6.1, 6.2, 6.3, 6.4, 6.8, 6.9, 8.1, 8.2, 8.3, 8.4, 8.5_

- [x] 3.3 Prove concurrent/multi-process overspend protection
  - Race many same-account holds; accepted holds must never exceed prepaid funds or postpaid credit capacity.
  - _Boundary: integration/concurrency tests_
  - _Depends: 3.2_
  - _Validation: concurrent SQLite/PostgreSQL contract tests; race where practical_
  - _Requirements: 6.2, 6.3, 6.4, 6.5, 6.6, 6.7_

- [x] 3.4 Cut runtime admission to the single authorization seam
  - Insert only after side-effect-free route planning and before provider/connector work; preserve routing/stream semantics.
  - _Boundary: runtime driving adapter_
  - _Depends: 3.1, 3.2, 3.3_
  - _Validation: zero-upstream-work denial tests plus existing routing/runtime suites_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 6.8, 17.9, 17.10_

## Phase 4 — Trusted Financial Operations and Processing State

- [x] 4.1 Implement trusted funding, payment, adjustment, and authorization-release commands
  - Use balanced postings, closed reason semantics, source identity/fingerprint, point-in-time snapshots, and conflict replay rejection; expose no arbitrary posting API.
  - _Boundary: billing application + store_
  - _Depends: 2.3, 2.4_
  - _Validation: command/store tests_
  - _Requirements: 5.6, 5.7, 5.8, 7.7, 7.8, 7.9, 8.3, 8.6, 8.7, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6_

- [x] 4.2 Persist sealed TUR/LUR and separate mutable processing metadata
  - Worker claim/lease/retry/status/result updates must never mutate immutable evidence rows.
  - _Boundary: billing store / processing query seam_
  - _Depends: 1.3, 2.2_
  - _Validation: immutability/replay/restart tests_
  - _Requirements: 2.8, 2.9, 2.10, 9.5, 15.1, 15.2, 15.3, 15.4_

- [x] 4.3 Add durable pending-work claiming and retry behavior
  - Cover crash before/after claim and before/after financial settlement, bounded metadata and stuck-work queries.
  - _Boundary: billing processing adapter_
  - _Depends: 4.2_
  - _Validation: restart/replay tests_
  - _Requirements: 15.2, 15.3, 15.4, 15.5, 15.6, 15.7_

- [x] 4.4 Wire terminal TUR handoff through the existing terminal owner
  - Persist idempotently after final B-leg evidence; no second terminal owner or financial stream callback.
  - _Boundary: runtime terminal driving adapter_
  - _Depends: 1.4, 4.2, 4.3_
  - _Validation: terminal/cleanup/duplicate-handoff tests_
  - _Requirements: 1.3, 1.4, 1.5, 1.6, 15.1, 17.10_

## Phase 5 — Deterministic Post-Turn Rating

- [x] 5.1 Add pure customer-charge calculation from TUR policy
  - Cover surfaced-only completed turns, OpenRouter-style observed-usage billing on failure/cancel (including unsurfaced completion tokens), multi-leg/pass-through policies, rejected-provider zero charge, and actual<=authorized bound.
  - _Boundary: billing calculation policy_
  - _Depends: 1.2, 3.1_
  - _Validation: pure table/property tests_
  - _Requirements: 11.6, 11.7, 11.8, 12.1, 12.5, 12.6, 12.7, 12.9, 12.10, 12.11, 12.12, 12.13, 12.14_

- [x] 5.2 Add per-LUR operator-cost calculation
  - Include failed/losing/parallel legs and independent provider/model/rate evidence.
  - _Boundary: billing calculation policy_
  - _Depends: 1.4_
  - _Validation: B2BUA cost scenarios_
  - _Requirements: 11.2, 11.3, 11.4, 11.5, 12.5, 12.6, 12.7_

- [x] 5.3 Enforce exact customer/operator snapshot identity binding
  - Reject mismatched pricing/policy/rate references before any rating/posting, even when numeric rates match.
  - _Boundary: billing calculator_
  - _Depends: 5.1, 5.2_
  - _Validation: snapshot mismatch/immutability tests_
  - _Requirements: 12.1, 12.2, 12.3, 12.4_

- [x] 5.4 Implement explicit `unreconciled_cost` path
  - Provider-billable LUR without authoritative or reproducibly rateable cost must remain visible and unprocessed; no zero/omission fallback.
  - _Boundary: billing calculator + processing state_
  - _Depends: 4.2, 5.2, 5.3_
  - _Validation: missing quantity/rate/provider-cost tests_
  - _Requirements: 3.5, 3.6, 12.7, 12.8, 15.3, 15.4, 15.5_

## Phase 6 — Atomic Double-Entry Settlement and Reconciliation

- [x] 6.1 Post customer charge and per-B-leg provider COGS with durable identities
  - Customer source key is TUR-scoped; provider source key is TUR+BLeg scoped; all use semantic fingerprints.
  - _Boundary: billing application + journal store_
  - _Depends: 2.3, 5.1, 5.2, 5.3, 5.4_
  - _Validation: journal posting/idempotency tests_
  - _Requirements: 7.8, 7.9, 7.10, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.7, 11.8, 13.3, 13.4, 13.5, 13.6_

- [x] 6.2 Apply customer settlement, hold close, materialized state, and snapshots atomically
  - RED fault-injection tests must prove no partial financial visibility on crash.
  - _Boundary: billing store transaction_
  - _Depends: 3.2, 4.1, 6.1_
  - _Validation: transactional fault matrix on SQLite/PostgreSQL_
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 13.1, 13.2, 13.3, 13.4, 13.5, 13.6, 13.7, 13.8_

- [x] 6.3 Implement deterministic journal replay and reconciliation
  - Replay by AccountSequence; verify balances, holds, correction links, semantic fingerprints, snapshots and materialized state.
  - _Boundary: billing reconciliation domain + query adapter_
  - _Depends: 2.3, 2.4, 6.2_
  - _Validation: property/rebuild/dialect parity tests_
  - _Requirements: 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7_

- [x] 6.4 Implement `reconcile_required` safety lifecycle
  - Inject journal/fingerprint/correction/sequence/snapshot/materialized corruption; prove atomic block, fail-closed hard-credit admission, and explicit verified reconciliation as the only clear path.
  - _Boundary: billing reconciliation + authorization policy_
  - _Depends: 3.2, 6.3_
  - _Validation: state transition/block/rebuild/re-enable tests_
  - _Requirements: 14.8, 14.9, 14.10, 6.8_

## Phase 7 — Reporting and Authoritative Cutover

- [x] 7.1 Add journal/TUR-backed account and per-turn explanation queries
  - Expose balance/mode/credit/reserved/spendable and link authorization, TUR/LUR, result, journal and snapshots.
  - _Boundary: billing query/read side_
  - _Depends: 6.1, 6.2, 6.3_
  - _Validation: query/pagination/redaction tests_
  - _Requirements: 16.1, 16.2, 16.3, 16.4, 16.5, 15.7_

- [x] 7.2 Add operator-cost/gross-margin and trial-balance reports
  - Preserve customer revenue vs provider COGS perspectives and B-leg attribution.
  - _Boundary: reporting/query seam_
  - _Depends: 7.1_
  - _Validation: report fixtures against journal truth_
  - _Requirements: 11.2, 11.3, 11.4, 11.5, 11.7, 11.8, 16.2, 16.3, 16.4, 16.5_

- [x] 7.3 Shadow-compare TUR/journal outcomes with current financial behavior
  - Use representative success/failover/parallel/cancel/zero/absent-provider-cost scenarios before authoritative cutover.
  - _Boundary: migration tests / observability_
  - _Depends: 5.1, 5.2, 5.3, 5.4, 6.1, 6.2, 6.3, 6.4, 7.1, 7.2_
  - _Validation: deterministic shadow comparison suite_
  - _Requirements: 17.1, 17.2, 17.3, 17.4, 17.5, 17.6, 17.11_

- [x] 7.4 Cut authoritative monetary settlement and reports to the new path
  - Keep non-money usage/rate-limit behavior outside this change unless independently migrated.
  - _Boundary: composition/config + control-plane read paths_
  - _Depends: 7.3_
  - _Validation: `make test` plus focused billing/control-plane suites_
  - _Requirements: 16.1, 16.2, 16.3, 16.4, 16.5, 16.6, 17.1, 17.2, 17.3, 17.4, 17.5, 17.6_

## Phase 8 — Delete Legacy Financial Runtime Paths and Ratchet

- [x] 8.1 Delete stream-time financial reconciliation, price enrichment, and economic accumulators
  - Remove only after authoritative cutover and preserved protocol usage behavior is proven.
  - _Boundary: runtime/tokenaccounting/metering cleanup_
  - _Depends: 7.4_
  - _Validation: runtime/tokenaccounting tests plus architecture tests_
  - _Requirements: 17.1, 17.2, 17.3, 17.4, 17.5, 17.6_

- [x] 8.2 Delete direct runtime financial/token-ledger writes and obsolete monetary authority compatibility paths
  - Preserve genuinely non-money quota/rate-limit features until separately redesigned.
  - _Boundary: runtime/controlplane/usageauthority cleanup_
  - _Depends: 8.1_
  - _Validation: focused authority/control-plane tests_
  - _Requirements: 17.3, 17.4, 17.5, 17.6_

- [x] 8.3 Add architecture and forbidden-symbol/import ratchets
  - Prove runtime has only authorization + terminal TUR handoff, billing has no provider SDKs, and no raw usage/metering settlement path returns.
  - _Boundary: internal/archtest_
  - _Depends: 8.1, 8.2_
  - _Validation: `go test ./internal/archtest/...` and `make quality-checks`_
  - _Requirements: 1.8, 17.7, 17.8, 17.9, 17.10_

- [x] 8.4 Run final rebuild/concurrency/accounting certification and document ownership
  - Certify all requirements, both durable dialects where available, B2BUA cases, semantic replay, trial balance, reconcile-required recovery, and final package/change surface.
  - _Boundary: tests + docs/steering_
  - _Depends: 8.3_
  - _Validation: `make test`; `make qa` for release-grade validation where environment supports it_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 2.10, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.8, 4.9, 4.10, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.8, 6.9, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 7.8, 7.9, 7.10, 7.11, 7.12, 7.13, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7, 9.8, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.7, 11.8, 12.1, 12.2, 12.3, 12.4, 12.5, 12.6, 12.7, 12.8, 12.9, 12.10, 13.1, 13.2, 13.3, 13.4, 13.5, 13.6, 13.7, 13.8, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 15.1, 15.2, 15.3, 15.4, 15.5, 15.6, 15.7, 16.1, 16.2, 16.3, 16.4, 16.5, 16.6, 17.1, 17.2, 17.3, 17.4, 17.5, 17.6, 17.7, 17.8, 17.9, 17.10, 17.11_

## Implementation Notes

Phase 8 keeps these post-cutover decisions:

- Authoritative Bun billing is injected through `runtimebundle.ProductionOptions` (store, admission, identity, rating resolver). YAML `accounting.billing.authoritative` fails closed without that injection; `lipstd` and public `lipruntime.Options` do not invent accounts.
- Connector `FinalizeBilling` stays token-only. Terminal TUR sealing may copy stream-observed `CostPresent` (including authoritative zero) when the ABI cannot express money.
- Production `accounting.authority` YAML rejects monetary `budget` / `spend_cap` / `money_nano` rules. Domain Budget/SpendCap types remain for unit tests and a later non-money quota redesign.
- `accounting.ledger.*` YAML is leftover compatibility only and is not opened. `accounting.pricing` / `EstimateCost` remain snapshot and shadow helpers.
- Stale-hold cleanup is `ReleaseStaleSafe` / unused-hold release, not TTL reclaim.
- Detached TUR handoff retries stay unlimited while the process is up; Host close bounds the wait so quiesce cannot hang forever.
- Runtime's driving adapter is a collector that records LURs and seals one TUR; it is not stream-time settlement.
- The earlier unapproved `usage-accounting-architecture-convergence` spec is archived as superseded for stream-time money accounting. Non-money quota/rate-limit leftover still needs a separate spec if pursued.
