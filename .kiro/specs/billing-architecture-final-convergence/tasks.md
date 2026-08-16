# Implementation Plan

## Execution Rules

- HARD DEPENDENCY: `billing-post-usage-correctness-hardening` must be implemented on main first; PR #346/spec merge alone does not satisfy this gate.
- TDD mandatory: RED -> GREEN -> REFACTOR/delete.
- Delete superseded production code in the same phase that makes it unnecessary; do not leave feature-flagged legacy paths.
- No phase may weaken predecessor billing correctness.
- Four executable subtasks per phase.

## Phase 0 — Rebase the Design on the Correctness Baseline

- [ ] 0.1 Resolve and verify the predecessor implementation dependency.
  - Verify `spec.json` pins predecessor `billing-post-usage-correctness-hardening`, spec PR #346 and spec merge SHA `9bf9c66a09de50ab3dcad18f0a8a84c2c2d49ed9`. Locate the later `main` commit that actually implements that spec; prove it is implementation, not merely the spec merge; rerun the predecessor's full billing regression/certification suite; then record the verified commit in `spec.json.dependencies[0].implementation_main_sha` and change `verification_status` from `pending_phase_0`. Update only names/paths if necessary; semantic drift triggers requirements/design revalidation.
  - _Boundary: dependency gate / tests_
  - _Depends: billing-post-usage-correctness-hardening implementation completed_
  - _Validation: predecessor final certification commands_
  - _Requirements: 1.1-1.6, 12.6-12.9_

- [ ] 0.2 Materialize the canonical widened production simplification baseline.
  - Create `internal/archtest/testdata/architecture/billing_final_convergence_baseline.json` from the exact verified predecessor implementation SHA from 0.1. Record `physical-go-lines-v1`, denominator, included roots/files/declarations, exclusions, seed symbols, deterministic AST symbol-following version/rules and deletion targets. The denominator must be recomputable from the artifact; tests/docs/generated/testdata and explicitly historical-only migrations are excluded exactly as defined in design Section 12. The artifact baseline SHA must equal `spec.json.dependencies[0].implementation_main_sha`.
  - _Boundary: architecture baseline_
  - _Depends: 0.1_
  - _Validation: go test ./internal/archtest -run 'Billing.*Convergence.*Baseline|Billing.*LOC|Billing.*Symbol'_
  - _Requirements: 10.1-10.5_

- [ ] 0.3 Inventory every live legacy TUR/LUR/reserved/auth-book/money-authority/outbox consumer.
  - Produce deletion targets and prove which historical readers must remain isolated. Record every retiring-table/outbox writer that must be quiesced before destructive migration.
  - _Boundary: architecture/source inventory_
  - _Depends: 0.1_
  - _Validation: go test ./internal/archtest_
  - _Requirements: 2.6-2.7, 3.2-3.7, 4.4-4.8, 5.2-5.8, 7.10, 11.1-11.6, 12.1-12.5_

- [ ] 0.4 Add planned architecture/LOC ratchets in non-activated state.
  - Lock baseline SHA/LOC and structural deletion inventory before implementation changes. Add a reproducibility test that recomputes `physical-go-lines-v1` and the versioned AST fixed-point inventory from the pinned SHA/fixture; scanner-rule changes require an explicit version bump rather than silently changing the denominator.
  - _Boundary: architecture tests_
  - _Depends: 0.2, 0.3_
  - _Validation: go test ./internal/archtest_
  - _Requirements: 10.1-10.8, 12.1-12.5_

## Phase 1 — Native Current-Record Customer Rating

- [ ] 1.1 Add differential tests between legacy bridge and native current-record rating.
  - Cover all predecessor scenarios before implementing the new algorithm, including `all potential` evidence-first semantics: accepted billable legs are filtered before charge scope; planned/never-started/rejected/evidence-unavailable legs do not become charges. Cover surfaced/completed, failover, parallel, interrupted and sequence-unknown cases.
  - _Boundary: pure core billing tests_
  - _Depends: 0.1_
  - _Validation: go test ./internal/core/billing -run 'Rating|Differential|Call|Potential|Sequence'_
  - _Requirements: 1.1-1.6, 2.1-2.5_

- [ ] 1.2 Implement native `RateCustomerCall` over current call/leg records.
  - Preserve authoritative sequence, surfaced/evidence, mixed-model and charge policy semantics. Apply provider-accepted customer-evidence filtering before every charge-scope selector; define `all potential` as all accepted customer-billable evidence legs, never every planned route leg.
  - _Boundary: core billing policy_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/billing_
  - _Requirements: 2.1-2.5_

- [ ] 1.3 Cut customer resolver/worker to native rating only.
  - No legacy record construction remains on authoritative path.
  - _Boundary: billingcompose + post-usage worker_
  - _Depends: 1.2_
  - _Validation: go test ./internal/infra/billingcompose ./internal/core/billing ./internal/infra/billingstore_
  - _Requirements: 2.1-2.5, 8.2_

- [ ] 1.4 Delete legacy TUR/LUR domain rating bridge and activate bridge-forbid ratchet.
  - Remove `TurnUsageRecord`, legacy `LegUsageRecord`, obsolete sealing/adapters once no consumer remains.
  - _Boundary: core billing deletion_
  - _Depends: 1.3_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingcompose ./internal/archtest_
  - _Requirements: 2.2, 2.6-2.7, 10.5, 12.3_

## Phase 2 — Durable Local Terminal Spool and Runtime Cutover

- [ ] 2.1 Add RED process-local spool durability/completeness/restart/idempotency/capacity contracts.
  - Pin the supported production store to Bun + process-local SQLite/WAL with `synchronous=FULL`; success only after commit acknowledgement; durable state-directory placement/permissions; call/leg stable keys from sealed current records; fingerprint-version and same-key/same-fingerprint replay versus conflict semantics; frozen expected-B-leg completeness gating at central claim; crash before/after commit acknowledgement; stale `delivering` restart recovery; typed pending-record/payload/database/free-disk capacity limits; processed retention/pruning; health metrics; transient central failure and fingerprint-conflict state.
  - _Boundary: new narrow driven adapter_
  - _Depends: 0.1_
  - _Validation: go test ./internal/infra/billingspool -run 'Durab|Crash|Replay|Fingerprint|Complete|Capacity|Retention|Recovery|Health'_
  - _Requirements: 7.1-7.9, 8.1-8.5, 12.6-12.8_

- [ ] 2.2 Implement process-owned spool and one flusher.
  - Reuse internal DB/lifecycle ownership; no generic broker or per-request goroutine. Enforce production SQLite durability settings, stable per-instance path, append capacity gate, processed-row pruning/health reporting, `claimed_at`, startup reset/reclaim of stale `delivering`, bounded backoff and exactly one process-owned flusher per spool.
  - _Boundary: persistence adapter + process lifecycle_
  - _Depends: 2.1_
  - _Validation: go test ./internal/infra/billingspool ./internal/infra/runtimebundle -run 'Spool|Lifecycle|Recovery|Capacity|Health'_
  - _Requirements: 7.1-7.9, 8.1, 8.4-8.5_

- [ ] 2.3 Cut runtime terminal handoff to `TerminalUsageSink`.
  - Local durable append only; remove synchronous central store attempt from stream terminal path. Preserve independently terminalized leg appends and sealed call closure with frozen expected IDs. Prove central `ClaimCompleteCall` refuses customer rating until every expected B-leg has a valid sealed central row even when flusher delivery is call-first/out-of-order.
  - _Boundary: runtime terminal seam + central completeness contract_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/runtime ./internal/infra/runtimebundle ./internal/infra/billingstore -run 'Billing|Terminal|Spool|Retry|CompleteCall|ExpectedBLeg|OutOfOrder'_
  - _Requirements: 1.6, 7.1-7.9, 8.2, 8.6-8.7_

- [ ] 2.4 Drain and delete direct+fallback central append layering.
  - First quiesce all old direct/outbox writers. Deterministically replay every pending/deferred `usage_append_outbox` payload into current central call/leg storage, treating identical replay as success; malformed/conflicting/unprovable rows block deletion and require explicit reconciliation. Re-check zero unresolved rows under the same dialect-specific migration critical section used for the schema drop, drop only after the proof, and run post-cutover source-key/fingerprint reconciliation. Then remove retrying appender wrappers, current `UsageAppendWorker`, central outbox live consumers/schema.
  - _Boundary: core billing + billingstore + runtimebundle deletion/migration_
  - _Depends: 2.3_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle ./internal/archtest -run 'UsageAppend|Outbox|Migration|Reconcile|Billing'; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Outbox|Postgres.*Migration'_
  - _Requirements: 7.3-7.10, 8.1, 8.4-8.8, 10.5, 11.1-11.6_

## Phase 3 — Decouple Provider COGS from Customer Credit Lock

- [ ] 3.1 Add RED concurrency tests showing current provider COGS/customer admission serialization.
  - PostgreSQL is authoritative for row-lock proof; SQLite tests preserve correctness under its writer model.
  - _Boundary: billingstore concurrency tests_
  - _Depends: 0.1_
  - _Validation: go test ./internal/infra/billingstore -run 'Provider.*Concurrent|Exposure.*Concurrent'; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Provider.*Exposure'_
  - _Requirements: 6.1-6.7_

- [ ] 3.2 Implement the single provider-independent journal ordering contract.
  - Customer-balance-affecting journal rows retain non-null per-account `account_sequence`. New provider COGS rows require `account_sequence=NULL` and deterministic database-recorded `(recorded_at, transaction_id)` order; `recorded_at` is immutable/DB-assigned and globally unique `transaction_id` is the tie-break. Make account sequence nullable with preserved non-null customer uniqueness, add account/book provider-order indexes, update validators, and migrate PostgreSQL/SQLite without rewriting historical provider sequence/timestamps or introducing a global sequence counter.
  - _Boundary: journal/store persistence + migration_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore -run 'Journal|Provider|Sequence|Order|TrialBalance|Migration'; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Journal|Postgres.*Provider.*Order'_
  - _Requirements: 6.1-6.6, 9.3, 9.5, 11.1-11.6_

- [ ] 3.3 Cut `ApplyProviderCost` off customer account locking/version mutation.
  - Preserve correlation/reporting and balanced COGS/payable entries. New provider COGS writes must leave `account_sequence` NULL and must not increment customer account version.
  - _Boundary: billingstore provider COGS_
  - _Depends: 3.2_
  - _Validation: go test ./internal/infra/billingstore -run 'Provider|COGS|Replay|Concurrent|Sequence'_
  - _Requirements: 1.5, 6.1-6.7_

- [ ] 3.4 Update reports/reconciliation for the two explicit ordering domains.
  - Customer balance replay uses non-null `account_sequence`; provider COGS/report traversal uses deterministic `(recorded_at, transaction_id)` and BillingCallID/A/B-leg correlation. Trial balance remains independent of either order. Test historical provider rows with old non-null account sequence remain auditable while new rows follow the new contract.
  - _Boundary: reporting/reconciliation_
  - _Depends: 3.3_
  - _Validation: go test ./internal/infra/billingstore ./internal/stdhttp/... -run 'Report|Trial|Reconcile|Provider|Order'_
  - _Requirements: 6.4-6.6, 9.1-9.6_

## Phase 4 — Remove Reserved/Authorization Current-Domain Residue

- [ ] 4.1 Add RED source/domain tests forbidding current `ReservedNano`.
  - Characterize legacy migration readers separately from current domain.
  - _Boundary: core billing / architecture tests_
  - _Depends: 0.3_
  - _Validation: go test ./internal/core/billing ./internal/archtest_
  - _Requirements: 4.1-4.8_

- [ ] 4.2 Remove reserved fields from Account/current snapshots/commands/reports.
  - Keep settled headroom and operational exposure separate.
  - _Boundary: core billing + current store contracts_
  - _Depends: 4.1_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore_
  - _Requirements: 4.1-4.4, 9.1-9.5_

- [ ] 4.3 Isolate historical authorization-book decode and remove it from current writers.
  - Current journal validation/writer accepts only current books.
  - _Boundary: journal domain + migration/report compatibility_
  - _Depends: 4.2_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore -run 'Journal|Authorization|Historical'_
  - _Requirements: 4.5-4.8, 9.6-9.7_

- [ ] 4.4 Forward-migrate/remove always-zero reserved column where safe.
  - Quiesce current account writers for the destructive column/table-rebuild step and apply the same dialect-critical-section rule: PostgreSQL transactional DDL/locking; SQLite one-connection writer exclusion/table rebuild. Balances, versions and policies must be byte/value equivalent after migration and reconciliation.
  - _Boundary: Bun migrations_
  - _Depends: 4.2_
  - _Validation: go test ./internal/infra/billingstore -run 'Migration|Reserved|Account|Reconcile'; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Reserved|PostgresBillingStoreContract'_
  - _Requirements: 4.4, 4.7-4.8, 11.1-11.6_

## Phase 5 — Make UsageAuthority Quantity-Only

- [ ] 5.1 Add RED compatibility/config tests for retired monetary UsageAuthority rules.
  - Pin request/token behavior and require explicit migration error for money rules.
  - _Boundary: usageauthority domain/config_
  - _Depends: 0.3_
  - _Validation: go test ./internal/core/usageauthority/... ./internal/core/config_
  - _Requirements: 5.1-5.8, 11.3_

- [ ] 5.2 Remove money unit and money-specific admission/settlement/release fields.
  - Preserve request/token quota and concurrency semantics.
  - _Boundary: usageauthority core_
  - _Depends: 5.1_
  - _Validation: go test ./internal/core/usageauthority/..._
  - _Requirements: 5.1-5.7_

- [ ] 5.3 Remove money reservation/store mapping and runtime money compatibility code.
  - No hidden disabled second financial implementation remains.
  - _Boundary: usageauthority infra + runtime adapters_
  - _Depends: 5.2_
  - _Validation: go test ./internal/infra/usageauthority/... ./internal/core/runtime ./internal/infra/runtimebundle_
  - _Requirements: 5.2-5.8, 10.5, 12.4_

- [ ] 5.4 Activate architecture guards proving one monetary authority.
  - Metering money remains telemetry-only; billing is sole financial authority.
  - _Boundary: architecture tests_
  - _Depends: 5.3_
  - _Validation: go test ./internal/archtest_
  - _Requirements: 5.6-5.8, 12.1-12.5_

## Phase 6 — Retire Legacy Usage Persistence and Processing

- [ ] 6.1 Add migration characterization and race tests for legacy TUR/LUR/processing state.
  - Enumerate pending/processing/error/current-equivalent states and every legacy writer. RED tests must prove unresolved financial/usage work blocks retirement and a concurrent legacy writer cannot commit work between successful proof and `DROP` on either supported dialect.
  - _Boundary: billingstore migration/concurrency tests_
  - _Depends: 1.4, 0.3_
  - _Validation: go test ./internal/infra/billingstore -run 'Legacy|Migration|Processing|TUR|LUR|Concurrent'; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Legacy|Postgres.*Migration'_
  - _Requirements: 3.1-3.7, 11.1-11.6_

- [ ] 6.2 Delete remaining live legacy append/processing/shadow workers and interfaces.
  - No feature flag or dormant production implementation remains. Deployment/cutover documentation must identify the point after which mixed-version processes capable of legacy writes are no longer permitted.
  - _Boundary: core billing + billingstore deletion_
  - _Depends: 1.4, 6.1_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore ./internal/archtest_
  - _Requirements: 2.6-2.7, 3.5, 10.5, 12.3_

- [ ] 6.3 Preserve/resolve legacy work and retire TUR/LUR/processing in one critical section.
  - Before destructive DDL, quiesce all legacy writers; migrate any deterministically convertible retained records to current usage storage; block on pending/error/conflicting/unprovable state; acquire dialect-specific migration writer exclusion; re-run the unresolved-work proof under that lock; drop retired tables/indexes/triggers in the same transaction/connection; update `VerifySchema`; then run post-migration reconciliation proving fresh/upgraded current-record and journal state. PostgreSQL must use transaction-scoped migration serialization plus `ACCESS EXCLUSIVE` retiring-table locks; SQLite must use one connection with `BEGIN IMMEDIATE` or stronger repository-supported writer exclusion.
  - _Boundary: Bun schema / migration critical section_
  - _Depends: 6.2_
  - _Validation: go test ./internal/infra/billingstore -run 'Migration|VerifySchema|Legacy|Reconcile|Concurrent'; LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore -run 'Postgres.*Migration|Postgres.*Legacy|PostgresBillingStoreContract'_
  - _Requirements: 3.1-3.8, 11.1-11.6_

- [ ] 6.4 Update reconciliation/report queries to current records only.
  - Historical audit compatibility may use isolated legacy decode, never old processing. Reconciliation must certify the post-retirement current usage/journal state before migration completion is considered operationally successful.
  - _Boundary: reporting/reconciliation_
  - _Depends: 6.3_
  - _Validation: go test ./internal/infra/billingstore ./internal/stdhttp/... -run 'Report|Reconcile|Legacy'_
  - _Requirements: 3.7, 9.1-9.7, 11.5_

## Phase 7 — Composition Collapse and Final Certification

- [ ] 7.1 Shrink final `BillingRuntime` and authoritative composition to an all-or-none contract.
  - Remove `BillingAuthoritative` or any equivalent runtime mode boolean. Runtime receives only cheap gate, exposure admission, terminal sink and identity. Stock non-billing construction has all billing ports absent; billing-enabled construction requires all four dependencies and rejects partial wiring. Customer/provider workers stay process-owned outside Executor. Add construction/architecture tests proving no legacy/current mode selector remains.
  - _Boundary: runtime config + runtimebundle_
  - _Depends: 2.4, 3.4, 5.3, 6.4_
  - _Validation: go test ./internal/core/runtime ./internal/infra/runtimebundle ./internal/archtest -run 'Billing|Construction|Authoritative|Wiring|Runtime'_
  - _Requirements: 8.1-8.8, 12.1, 12.5_

- [ ] 7.2 Activate all structural deletion and >=10% production LOC ratchets.
  - Recompute the canonical Phase-0 artifact denominator from its pinned SHA, fail on any mismatch, then measure final `physical-go-lines-v1` with the same versioned AST symbol-following algorithm. No code movement gaming; explicit symbols/tables/workers must be absent.
  - _Boundary: architecture tests_
  - _Depends: 4.4, 5.4, 6.3, 7.1_
  - _Validation: go test ./internal/archtest -run 'Billing.*Convergence|Billing.*LOC|Billing.*Deletion|Billing.*Symbol'_
  - _Requirements: 10.1-10.8, 12.1-12.5_

- [ ] 7.3 Update steering/architecture/host billing docs to one final flow.
  - Remove migration-era alternatives and document local spool durability/capacity/restart behavior, customer completeness gate, all-or-none runtime composition, provider ordering, worker ownership and destructive migration operating procedure.
  - _Boundary: docs / steering_
  - _Depends: 7.1, 7.2_
  - _Validation: make docs-check; go test ./internal/archtest_
  - _Requirements: 8.6-8.8, 10.8, 12.9_

- [ ] 7.4 Add and run one fail-fast final billing-convergence certification target.
  - Add a checked-in `make billing-convergence-certify` target (or equivalently named checked-in certification entry point) whose recipe stops on the first failed prerequisite/suite. It must run the predecessor regressions plus current core/runtime/UsageAuthority/billingstore/spool/compose/admission/runtimebundle/architecture suites, configured PostgreSQL integration gates, unit/quality/docs checks, and race tests through explicit platform/tool availability conditional logic. Do not encode optionality as prose passed to `make`, and do not use semicolon-separated shell commands whose final status can mask an earlier failure.
  - _Boundary: final certification_
  - _Depends: 7.1, 7.2, 7.3_
  - _Validation: make billing-convergence-certify_
  - _Requirements: 1.1-1.6, 2.1-2.7, 3.1-3.8, 4.1-4.8, 5.1-5.8, 6.1-6.7, 7.1-7.10, 8.1-8.8, 9.1-9.7, 10.1-10.8, 11.1-11.6, 12.1-12.9_

## Completion Gate

Final GO requires both:

1. `make billing-convergence-certify` (or the checked-in equivalent) returns success with fail-fast semantics and all required behavior/integrity suites pass; and
2. the repository visibly has one current monetary architecture with the structural deletion targets and >=10% canonical production-surface reduction satisfied.

Passing tests alone is insufficient if duplicate legacy financial concepts remain.
