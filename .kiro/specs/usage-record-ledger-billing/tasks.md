# Implementation Plan

Implementation is RED -> GREEN -> REFACTOR. Each phase has at most four executable subtasks. Characterization/shadow evidence remains until the replacement path is proven and the old financial path is deleted.

## Phase 1 — Freeze Boundaries and Define Immutable Evidence

- [ ] 1.1 Characterize current runtime/B2BUA/provider billing behavior before ownership changes
  - Freeze pre-upstream denial, failover/parallel B-leg lineage, terminal ownership, client usage wire behavior, and representative provider/connector final evidence.
  - _Boundary: tests / runtime characterization_
  - _Depends: none_
  - _Validation: focused runtime/backend tests plus `make test-unit`_
  - _Requirements: 1.1–1.8, 3.1–3.7, 11.1–11.8, 17.7–17.8_

- [ ] 1.2 Add RED contracts for TUR/LUR values and B2BUA attribution
  - Define protocol-neutral immutable TUR/LUR types and tests for A-leg/B-leg/model/provider/outcome/presence semantics.
  - _Boundary: internal/core/billing domain values_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/billing/...`_
  - _Requirements: 2.1–2.8, 11.1–11.8_

- [ ] 1.3 Add durable TUR/LUR keys and semantic replay fingerprints
  - Test account+turn TUR identity, TUR+BLeg LUR identity, same-key/same-fingerprint replay, and same-key/different-fingerprint rejection.
  - _Boundary: billing domain + store contract tests_
  - _Depends: 1.2_
  - _Validation: focused identity/property tests_
  - _Requirements: 2.2–2.3, 2.9–2.10, 13.4–13.6_

- [ ] 1.4 Implement final adapter/connector B-leg evidence handoff in shadow mode
  - Reuse existing accounting evidence/FinalizeBilling seams; do not alter stream financial behavior yet.
  - _Boundary: backend adapters / connector SDK adapter seam_
  - _Depends: 1.2_
  - _Validation: representative backend and connector tests_
  - _Requirements: 3.1–3.7, 17.8_

## Phase 2 — Build the Durable Bun Journal Foundation

- [ ] 2.1 Add RED Money/account/journal invariant tests
  - Cover signed prepaid/postpaid balance, floor, spendable formula, exact money arithmetic, balanced transaction rules, and immutability.
  - _Boundary: billing domain policy_
  - _Depends: 1.2_
  - _Validation: pure table/property tests_
  - _Requirements: 5.1–5.9, 7.1–7.4, 7.8–7.12_

- [ ] 2.2 Add Bun billing schema and migrations
  - Create accounts, policy audit, holds, immutable TUR/LUR, separate processing state, journal transactions/entries, indexes and constraints.
  - _Boundary: internal/infra/billingstore / DB migrations_
  - _Depends: 2.1_
  - _Validation: migration tests on SQLite and optional PostgreSQL harness_
  - _Requirements: 9.1–9.8, 15.1–15.3_

- [ ] 2.3 Add journal semantic idempotency, correction links, and deterministic AccountSequence
  - RED tests first for fingerprint compare-before-no-op, reversal/replacement/group linkage, `(account, sequence)` uniqueness, and concurrent sequence allocation.
  - _Boundary: billing store + journal domain_
  - _Depends: 2.2_
  - _Validation: store contract/concurrency tests_
  - _Requirements: 7.5–7.7, 7.13, 14.1–14.4_

- [ ] 2.4 Add SQLite/PostgreSQL parity contract suite
  - Prove balanced posting, replay, correction, snapshot, account locking/versioning, and crash rollback semantics are dialect-equivalent.
  - _Boundary: tests / durable adapters_
  - _Depends: 2.2–2.3_
  - _Validation: SQLite default + PostgreSQL integration when configured_
  - _Requirements: 9.2, 9.6–9.8, 14.7_

## Phase 3 — Implement Pessimistic Authorization

- [ ] 3.1 Add pure MaxCustomerCharge estimator with exact snapshot binding
  - RED tables for input/output bounds, fixed/resource charges, route/model alternatives, customer policy, unknown/unbounded cases, and overflow.
  - _Boundary: billing domain/application_
  - _Depends: 2.1_
  - _Validation: pure estimator tests_
  - _Requirements: 4.1–4.10_

- [ ] 3.2 Add atomic prepaid/postpaid authorization hold
  - Require `SpendableBefore >= MaxCustomerCharge`, then prove post-hold spendable non-negative in the same transaction.
  - _Boundary: billing application + Bun store_
  - _Depends: 2.2–2.4, 3.1_
  - _Validation: store authorization contract tests_
  - _Requirements: 5.1–5.9, 6.1–6.4, 6.8–6.9, 8.1–8.5_

- [ ] 3.3 Prove concurrent/multi-process overspend protection
  - Race many same-account holds; accepted holds must never exceed prepaid funds or postpaid credit capacity.
  - _Boundary: integration/concurrency tests_
  - _Depends: 3.2_
  - _Validation: concurrent SQLite/PostgreSQL contract tests; race where practical_
  - _Requirements: 6.2–6.7_

- [ ] 3.4 Cut runtime admission to the single authorization seam
  - Insert only after side-effect-free route planning and before provider/connector work; preserve routing/stream semantics.
  - _Boundary: runtime driving adapter_
  - _Depends: 3.1–3.3_
  - _Validation: zero-upstream-work denial tests plus existing routing/runtime suites_
  - _Requirements: 1.1–1.8, 6.8, 17.9–17.10_

## Phase 4 — Trusted Financial Operations and Processing State

- [ ] 4.1 Implement trusted funding, payment, adjustment, and authorization-release commands
  - Use balanced postings, closed reason semantics, source identity/fingerprint, point-in-time snapshots, and conflict replay rejection; expose no arbitrary posting API.
  - _Boundary: billing application + store_
  - _Depends: 2.3–2.4_
  - _Validation: command/store tests_
  - _Requirements: 5.6–5.8, 7.7–7.9, 8.3, 8.6–8.7, 10.1–10.6_

- [ ] 4.2 Persist sealed TUR/LUR and separate mutable processing metadata
  - Worker claim/lease/retry/status/result updates must never mutate immutable evidence rows.
  - _Boundary: billing store / processing query seam_
  - _Depends: 1.3, 2.2_
  - _Validation: immutability/replay/restart tests_
  - _Requirements: 2.8–2.10, 9.5, 15.1–15.4_

- [ ] 4.3 Add durable pending-work claiming and retry behavior
  - Cover crash before/after claim and before/after financial settlement, bounded metadata and stuck-work queries.
  - _Boundary: billing processing adapter_
  - _Depends: 4.2_
  - _Validation: restart/replay tests_
  - _Requirements: 15.2–15.7_

- [ ] 4.4 Wire terminal TUR handoff through the existing terminal owner
  - Persist idempotently after final B-leg evidence; no second terminal owner or financial stream callback.
  - _Boundary: runtime terminal driving adapter_
  - _Depends: 1.4, 4.2–4.3_
  - _Validation: terminal/cleanup/duplicate-handoff tests_
  - _Requirements: 1.3–1.6, 15.1, 17.10_

## Phase 5 — Deterministic Post-Turn Rating

- [ ] 5.1 Add pure customer-charge calculation from TUR policy
  - Cover surfaced-only, zero-charge failure/cancel, multi-leg/pass-through policies, and actual<=authorized bound.
  - _Boundary: billing calculation policy_
  - _Depends: 1.2, 3.1_
  - _Validation: pure table/property tests_
  - _Requirements: 11.6–11.8, 12.1, 12.5–12.7, 12.9–12.10_

- [ ] 5.2 Add per-LUR operator-cost calculation
  - Include failed/losing/parallel legs and independent provider/model/rate evidence.
  - _Boundary: billing calculation policy_
  - _Depends: 1.4_
  - _Validation: B2BUA cost scenarios_
  - _Requirements: 11.2–11.5, 12.5–12.7_

- [ ] 5.3 Enforce exact customer/operator snapshot identity binding
  - Reject mismatched pricing/policy/rate references before any rating/posting, even when numeric rates match.
  - _Boundary: billing calculator_
  - _Depends: 5.1–5.2_
  - _Validation: snapshot mismatch/immutability tests_
  - _Requirements: 12.1–12.4_

- [ ] 5.4 Implement explicit `unreconciled_cost` path
  - Provider-billable LUR without authoritative or reproducibly rateable cost must remain visible and unprocessed; no zero/omission fallback.
  - _Boundary: billing calculator + processing state_
  - _Depends: 4.2, 5.2–5.3_
  - _Validation: missing quantity/rate/provider-cost tests_
  - _Requirements: 3.5–3.6, 12.7–12.8, 15.3–15.5_

## Phase 6 — Atomic Double-Entry Settlement and Reconciliation

- [ ] 6.1 Post customer charge and per-B-leg provider COGS with durable identities
  - Customer source key is TUR-scoped; provider source key is TUR+BLeg scoped; all use semantic fingerprints.
  - _Boundary: billing application + journal store_
  - _Depends: 2.3, 5.1–5.4_
  - _Validation: journal posting/idempotency tests_
  - _Requirements: 7.8–7.10, 11.1–11.8, 13.3–13.6_

- [ ] 6.2 Apply customer settlement, hold close, materialized state, and snapshots atomically
  - RED fault-injection tests must prove no partial financial visibility on crash.
  - _Boundary: billing store transaction_
  - _Depends: 3.2, 4.1, 6.1_
  - _Validation: transactional fault matrix on SQLite/PostgreSQL_
  - _Requirements: 8.1–8.7, 10.1–10.6, 13.1–13.8_

- [ ] 6.3 Implement deterministic journal replay and reconciliation
  - Replay by AccountSequence; verify balances, holds, correction links, semantic fingerprints, snapshots and materialized state.
  - _Boundary: billing reconciliation domain + query adapter_
  - _Depends: 2.3–2.4, 6.2_
  - _Validation: property/rebuild/dialect parity tests_
  - _Requirements: 14.1–14.7_

- [ ] 6.4 Implement `reconcile_required` safety lifecycle
  - Inject journal/fingerprint/correction/sequence/snapshot/materialized corruption; prove atomic block, fail-closed hard-credit admission, and explicit verified reconciliation as the only clear path.
  - _Boundary: billing reconciliation + authorization policy_
  - _Depends: 3.2, 6.3_
  - _Validation: state transition/block/rebuild/re-enable tests_
  - _Requirements: 14.8–14.10, 6.8_

## Phase 7 — Reporting and Authoritative Cutover

- [ ] 7.1 Add journal/TUR-backed account and per-turn explanation queries
  - Expose balance/mode/credit/reserved/spendable and link authorization, TUR/LUR, result, journal and snapshots.
  - _Boundary: billing query/read side_
  - _Depends: 6.1–6.3_
  - _Validation: query/pagination/redaction tests_
  - _Requirements: 16.1–16.5, 15.7_

- [ ] 7.2 Add operator-cost/gross-margin and trial-balance reports
  - Preserve customer revenue vs provider COGS perspectives and B-leg attribution.
  - _Boundary: reporting/query seam_
  - _Depends: 7.1_
  - _Validation: report fixtures against journal truth_
  - _Requirements: 11.10, 16.2–16.5_

- [ ] 7.3 Shadow-compare TUR/journal outcomes with current financial behavior
  - Use representative success/failover/parallel/cancel/zero/absent-provider-cost scenarios before authoritative cutover.
  - _Boundary: migration tests / observability_
  - _Depends: 5.*, 6.*, 7.1–7.2_
  - _Validation: deterministic shadow comparison suite_
  - _Requirements: 17.1–17.6, 17.11_

- [ ] 7.4 Cut authoritative monetary settlement and reports to the new path
  - Keep non-money usage/rate-limit behavior outside this change unless independently migrated.
  - _Boundary: composition/config + control-plane read paths_
  - _Depends: 7.3_
  - _Validation: `make test` plus focused billing/control-plane suites_
  - _Requirements: 16.1–16.6, 17.1–17.6_

## Phase 8 — Delete Legacy Financial Runtime Paths and Ratchet

- [ ] 8.1 Delete stream-time financial reconciliation, price enrichment, and economic accumulators
  - Remove only after authoritative cutover and preserved protocol usage behavior is proven.
  - _Boundary: runtime/tokenaccounting/metering cleanup_
  - _Depends: 7.4_
  - _Validation: runtime/tokenaccounting tests plus architecture tests_
  - _Requirements: 17.1–17.6_

- [ ] 8.2 Delete direct runtime financial/token-ledger writes and obsolete monetary authority compatibility paths
  - Preserve genuinely non-money quota/rate-limit features until separately redesigned.
  - _Boundary: runtime/controlplane/usageauthority cleanup_
  - _Depends: 8.1_
  - _Validation: focused authority/control-plane tests_
  - _Requirements: 17.3–17.6_

- [ ] 8.3 Add architecture and forbidden-symbol/import ratchets
  - Prove runtime has only authorization + terminal TUR handoff, billing has no provider SDKs, and no raw usage/metering settlement path returns.
  - _Boundary: internal/archtest_
  - _Depends: 8.1–8.2_
  - _Validation: `go test ./internal/archtest/...` and `make quality-checks`_
  - _Requirements: 1.8, 17.7–17.10_

- [ ] 8.4 Run final rebuild/concurrency/accounting certification and document ownership
  - Certify all requirements, both durable dialects where available, B2BUA cases, semantic replay, trial balance, reconcile-required recovery, and final package/change surface.
  - _Boundary: tests + docs/steering_
  - _Depends: 8.3_
  - _Validation: `make test`; `make qa` for release-grade validation where environment supports it_
  - _Requirements: 1.1–17.11_
