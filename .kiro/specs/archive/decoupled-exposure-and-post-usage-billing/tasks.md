# Implementation Plan

## Execution Rules

- TDD mandatory: RED -> GREEN -> REFACTOR/delete.
- Shadow comparison may calculate legacy and target outcomes, but exactly one mechanism may mutate authoritative credit/money state.
- Each phase must remain buildable and preserve no-retry-after-output behavior.
- Four executable subtasks per phase.

## Phase 0 — Freeze Brownfield Behavior and Deletion Targets

- [x] 0.1 Characterize current billing/admission behavior and record a deletion/LOC baseline.
  - Cover prepaid/postpaid, max quote, current hold admission, zero-charge calls, B2BUA failover/parallel evidence and host composition.
  - _Boundary: tests / architecture baseline_
  - _Depends: none_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/billingadmission ./internal/core/runtime ./internal/infra/runtimebundle_
  - _Requirements: 1.1-1.8, 13.1-13.8, 14.1-14.8, 17.10-17.12_

- [x] 0.2 Add failing SafetyMargin and financial/exposure-separation tests.
  - Prove exposure has no financial journal/account mutation and prepaid/postpaid formulas remain safe.
  - _Boundary: domain policy / store tests_
  - _Depends: 0.1_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore_
  - _Requirements: 1.1-1.8, 5.1-5.10, 6.1-6.8_

- [x] 0.3 Add failing identity tests for multiple billable calls on one A-leg/session.
  - _Boundary: runtime/core billing tests_
  - _Depends: 0.1_
  - _Validation: go test ./internal/core/runtime ./internal/core/billing_
  - _Requirements: 2.1-2.8, 17.6_

- [x] 0.4 Add planned architecture ratchets for the end-state.
  - Target stream money mutation, hold/release lifecycle, A-leg-only settlement identity and runtime financial evidence barriers.
  - _Boundary: architecture tests_
  - _Depends: 0.1_
  - _Validation: go test ./internal/archtest_
  - _Requirements: 7.1-7.7, 13.1-13.8, 14.1-14.8, 17.8-17.11_

## Phase 1 — Per-call Identity and Durable Usage Spool

- [x] 1.1 Implement BillingCallID and call/leg identity/fingerprint contracts.
  - One ID per incoming invocation, shared across its retries/parallel B-legs, distinct across later calls on the same A-leg.
  - _Boundary: core billing/domain + runtime request metadata_
  - _Depends: 0.3_
  - _Validation: go test ./internal/core/billing ./internal/core/runtime_
  - _Requirements: 2.1-2.8, 8.5-8.6_

- [x] 1.2 Add Bun-backed independent terminal B-leg usage append.
  - Include explicit rejected/never-started/evidence-unavailable semantics and idempotent replay/conflict tests.
  - _Boundary: driven adapter / billingstore_
  - _Depends: 1.1_
  - _Validation: go test ./internal/infra/billingstore ./internal/core/billing_
  - _Requirements: 8.1-8.2, 8.5-8.10, 15.1-15.8_

- [x] 1.3 Add call-closure append and complete-call storage join.
  - Freeze expected B-leg IDs only after terminal ownership prevents further allocation; claim complete calls independent of append order.
  - _Boundary: core billing + driven adapter + runtime terminal owner_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore ./internal/core/runtime_
  - _Requirements: 8.3-8.5, 8.11, 14.3-14.4, 15.6_

- [x] 1.4 Require a durable usage spool in authoritative composition.
  - Keep memory spool only for tests/non-authoritative mode; prove post-output append failure cannot trigger provider retry.
  - _Boundary: composition root / runtime terminal seam_
  - _Depends: 1.2, 1.3_
  - _Validation: go test ./internal/infra/runtimebundle ./internal/core/runtime ./internal/infra/billingstore_
  - _Requirements: 7.3, 7.5-7.6, 8.7-8.10, 15.8, 17.4_

## Phase 2 — Two-stage Admission and Operational Exposure

- [x] 2.1 Implement the cheap pre-routing credit screen.
  - Read account readiness/currency/settled balance/floor and typed `MinPreRouteHeadroom`; place before route/token/rate work.
  - _Boundary: app orchestration + runtime driving adapter_
  - _Depends: 0.2_
  - _Validation: go test ./internal/core/billing ./internal/core/runtime ./internal/infra/runtimebundle_
  - _Requirements: 3.1-3.8_

- [x] 2.2 Refactor current pessimistic estimation into a quote-only post-route path.
  - Preserve cache pessimism, output bounds, fixed/resource charges and surfaced-vs-multi-leg policy.
  - _Boundary: domain policy / admission adapter_
  - _Depends: 0.1_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingadmission_
  - _Requirements: 4.1-4.10_

- [x] 2.3 Implement atomic Bun `CallExposure` admission.
  - Under the account lock: compute settled headroom + indexed SUM(open exposure), then insert immutable exposure only if SafetyMargin covers the new max.
  - No account balance update, financial journal, reserved state or exposure aggregate counter.
  - _Boundary: driven adapter / billingstore_
  - _Depends: 1.1, 2.2_
  - _Validation: go test ./internal/infra/billingstore -run 'Exposure|Admission|Concurrent'_
  - _Requirements: 5.1-5.10, 6.1-6.8, 15.3-15.5_

- [x] 2.4 Cut authoritative admission from monetary holds to cheap-screen + exposure admission.
  - Shadow compare first, then select exactly one hard-credit authority per runtime generation.
  - _Boundary: composition root + runtime_
  - _Depends: 2.1, 2.2, 2.3_
  - _Validation: go test ./internal/core/runtime ./internal/infra/runtimebundle ./internal/infra/billingstore ./internal/infra/billingadmission_
  - _Requirements: 3.1-7.7, 17.2-17.4_

## Phase 3 — Customer Post-usage Billing and Exposure Closure

- [x] 3.1 Remove hold lookup from customer rating input.
  - Resolve pricing/policy from immutable call/exposure refs and validate actual charge against admitted max.
  - _Boundary: domain policy + composition query seam_
  - _Depends: 1.3, 2.3_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingcompose_
  - _Requirements: 4.2, 9.1-9.3, 13.6-13.7_

- [x] 3.2 Implement idempotent customer billing operations including exact-zero calls.
  - Non-zero charge posts balanced journal; zero still persists a processed operation.
  - _Boundary: core billing + driven adapter_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore_
  - _Requirements: 9.4-9.7, 11.1-11.5, 17.7_

- [x] 3.3 Atomically apply customer balance mutation and close exposure.
  - Lock account, verify replay/max/floor, post/update balance, persist operation and close exposure in one transaction.
  - _Boundary: driven adapter / financial transaction_
  - _Depends: 2.3, 3.2_
  - _Validation: go test ./internal/infra/billingstore -run 'Customer|Exposure|Settlement|Concurrent'_
  - _Requirements: 5.6, 6.4-6.6, 9.8-9.10, 11.6-11.8, 12.4-12.6_

- [x] 3.4 Cut the post-usage worker to complete-call customer settlement.
  - Keep exposure open on incomplete/unrateable/invariant-failure calls; drive reconcile-required semantics without runtime state.
  - _Boundary: app orchestration_
  - _Depends: 1.3, 3.3_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle_
  - _Requirements: 8.4, 9.1-9.10, 12.5-12.6, 17.4_

## Phase 4 — Independent Per-B-leg Provider Cost

- [x] 4.1 Extract provider-cost calculation from the all-or-nothing customer settlement contract.
  - Preserve authoritative cost, fallback rate and explicit `unreconciled_cost`.
  - _Boundary: domain policy_
  - _Depends: 1.2_
  - _Validation: go test ./internal/core/billing -run 'Provider|Operator|Cost'_
  - _Requirements: 10.1-10.6_

- [x] 4.2 Implement independent idempotent provider-cost operations.
  - One operation per BillingCallID + B-leg; non-zero COGS posts balanced entries, zero records operation marker.
  - _Boundary: driven adapter / financial journal_
  - _Depends: 4.1_
  - _Validation: go test ./internal/infra/billingstore -run 'Provider|COGS|Replay'_
  - _Requirements: 10.7-10.9, 11.1-11.5_

- [x] 4.3 Decouple provider-cost retry/state from customer processing.
  - Prove customer settlement remains final while provider cost is pending; test arbitrary customer/provider/B-leg ordering.
  - _Boundary: app orchestration / processing state_
  - _Depends: 3.4, 4.2_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore_
  - _Requirements: 10.5-10.9, 16.3, 17.4-17.5_

- [x] 4.4 Update reports for settled customer spend, open exposure and provider COGS as separate perspectives.
  - Session/A-leg aggregation remains read-only over multiple BillingCallIDs.
  - _Boundary: query seam / control plane_
  - _Depends: 3.3, 4.3_
  - _Validation: go test ./internal/infra/billingstore ./internal/stdhttp/..._
  - _Requirements: 1.6, 16.1-16.7_

## Phase 5 — Runtime Terminal Simplification

- [x] 5.1 Replace `billingTurnCollector` evidence aggregation with direct terminal leg appends.
  - _Boundary: runtime terminal driving adapter_
  - _Depends: 1.2, 2.4, 3.4_
  - _Validation: go test ./internal/core/runtime -run 'Billing|Leg|Terminal'_
  - _Requirements: 7.1-7.7, 14.1, 14.3, 14.5-14.7_

- [x] 5.2 Remove billing-specific parallel barrier/sealed/TUR-builder state.
  - Storage completeness over call closure + expected legs replaces runtime financial barrier logic.
  - _Boundary: runtime_
  - _Depends: 1.3, 5.1_
  - _Validation: go test ./internal/core/runtime ./internal/archtest_
  - _Requirements: 14.1-14.8, 17.8_

- [x] 5.3 Remove runtime hold cleanup and abort-release branches.
  - Pre-provider failures now produce terminal zero/no-work usage and close exposure post-usage.
  - _Boundary: runtime + admission_
  - _Depends: 2.4, 3.4_
  - _Validation: go test ./internal/core/runtime ./internal/infra/billingadmission_
  - _Requirements: 7.7, 13.1, 13.5, 14.6_

- [x] 5.4 Shrink `BillingRuntime`/host composition to final seams.
  - Wire cheap gate, exposure admission, terminal usage sink and post-usage processors without hold release/lookup.
  - _Boundary: composition root / runtime config_
  - _Depends: 5.1, 5.2, 5.3_
  - _Validation: go test ./internal/infra/runtimebundle ./internal/core/runtime ./pkg/lipruntime ./cmd/lipstd_
  - _Requirements: 13.5-13.8, 14.8, 15.7-15.8, 17.11_

## Phase 6 — Reconciliation, Migration, and Hold Retirement

- [x] 6.1 Separate journal-derived financial rebuild from exposure reconstruction.
  - Add reconcile-required transition/re-enable tests and preserve financial/admission diagnostic snapshots.
  - _Boundary: billingstore / admin recovery_
  - _Depends: 3.3_
  - _Validation: go test ./internal/infra/billingstore -run 'Reconcile|Rebuild|Exposure'_
  - _Requirements: 1.7-1.8, 11.6-11.8, 12.1-12.6_

- [x] 6.2 Implement stale-exposure recovery requiring positive durable evidence.
  - No TTL-only close; normal billing or explicit idempotent no-charge repair only.
  - _Boundary: admin recovery / driven adapter_
  - _Depends: 1.3, 6.1_
  - _Validation: go test ./internal/infra/billingstore ./internal/stdhttp/... -run 'Exposure|Recovery|Billing'_
  - _Requirements: 12.7-12.9_

- [x] 6.3 Reconcile/migrate legacy open holds and prevent creation of new holds.
  - Do not drop `authorization_holds`/`reserved_nano` until no authoritative caller/open legacy hold remains.
  - _Boundary: Bun migration / rollout_
  - _Depends: 2.4, 3.4, 6.1_
  - _Validation: go test ./internal/infra/billingstore -run 'Migration|Legacy|Authorization|Exposure'_
  - _Requirements: 13.1-13.4, 15.1-15.6_

- [x] 6.4 Delete retired hold/reserved/authorization-book code and schema ownership.
  - Remove normal-call Authorization/Lookup/Releaser, reserved account math, authorization postings, remainder/release/expiry and hold-bound rating lookup.
  - _Boundary: core billing + billingstore + composition_
  - _Depends: 6.3_
  - _Validation: go test ./internal/core/billing ./internal/infra/billingstore ./internal/infra/billingcompose ./internal/infra/billingadmission ./internal/archtest_
  - _Requirements: 1.1-1.5, 13.1-13.8, 17.9-17.11_

## Phase 7 — Ratchets, Docs, Final Certification

- [x] 7.1 Activate runtime/import/symbol architecture guards.
  - Forbid stream money mutation, monetary holds, authorization-book call postings, runtime financial barriers and A-leg-only customer billing identity.
  - _Boundary: architecture tests_
  - _Depends: 5.4, 6.4_
  - _Validation: go test ./internal/archtest_
  - _Requirements: 7.1-7.7, 13.8, 14.1-14.8, 17.8-17.10_

- [x] 7.2 Prove cross-dialect concurrency and crash/replay behavior.
  - SQLite contracts/property tests plus PostgreSQL integration where configured.
  - _Boundary: integration tests_
  - _Depends: 6.4_
  - _Validation: go test ./internal/infra/billingstore ./internal/core/billing; LIP_REQUIRE_POSTGRES=1 go test ./internal/infra/billingstore -run Postgres_
  - _Requirements: 5.1-6.8, 8.1-12.9, 15.1-15.8, 17.2-17.7_

- [x] 7.3 Update steering, architecture and host-composition docs.
  - Replace old hold-authorize/TUR-handoff invariant with two-stage admission + terminal usage; document BillingCallID/A-leg semantics and failure posture.
  - _Boundary: docs / steering_
  - _Depends: 7.1_
  - _Validation: make docs-check; go test ./internal/archtest_
  - _Requirements: 1.1-3.8, 7.1-8.11, 16.1-17.12_

- [x] 7.4 Run final shrinkage and full behavior certification.
  - Verify every acceptance criterion, net production-LOC reduction, deletion manifest, B2BUA matrices, zero-charge accounting, hard-credit property tests and absence of dual monetary authority.
  - _Boundary: final certification_
  - _Depends: 7.1, 7.2, 7.3_
  - _Validation: `go test ./internal/archtest`, `go test ./internal/infra/billingstore`, `go test ./...`, `make quality-checks`, and `make test-race` where supported_
  - _Requirements: 1.1-1.8, 2.1-2.8, 3.1-3.8, 4.1-4.10, 5.1-5.10, 6.1-6.8, 7.1-7.7, 8.1-8.11, 9.1-9.10, 10.1-10.9, 11.1-11.8, 12.1-12.9, 13.1-13.8, 14.1-14.8, 15.1-15.8, 16.1-16.7, 17.1-17.12_

## Implementation Notes

- 0.1 locked production LOC at 12765 across six billing-convergence surfaces in `internal/archtest/testdata/architecture/billing_exposure_deletion_baseline.json`; Phase 7 activates both deletion and net-shrinkage ratchets against this pre-spec baseline.
- 0.2 added pure SafetyMargin/CallExposure policy in `internal/core/billing/exposure.go` (core-billing LOC lock now 4466 / total 11129). Live hold admission is unchanged; Bun atomic `call_exposures` insert is 2.3. CallID remains a string until 1.1.
- 0.3 added `BillingCallID` (`bc_` + 16 random bytes) and `stampBillingCallID` on `preparedRequest`; hold TUR/AuthorizationID still A-leg based. LOC lock now core-billing 4572 / runtime-billing 1083 / total 11261. Task 1.1 should strengthen Execute-resume proof that two later calls share one A-leg and still get distinct IDs.
- 0.4 planned ratchets live in `internal/archtest/billing_exposure_ratchet.go`; `forbid_hold_symbols` and `require_net_loc_reduction` stay false until 7.1/7.4. 7.1 must also update `TestBillingExposureDeletionTargetsCurrentlyExist` which still requires present=true. `internal/core` LineBudget is 75792 (measured+25).
- 1.1 added independent `CallUsageRecord` / `CallLegUsageRecord` in `internal/core/billing/call_usage.go` (nested TUR `LegUsageRecord` unchanged). LOC lock core-billing 4882 / total 11571. Task 1.2 should persist evidence quantity/cost presence in replay tests, trim BLegID consistently in key vs fingerprint, and treat ExpectedBLegIDs as a set (stable order) per 8.11.
- 1.2 persists independent legs in `usage_leg_records` via `DurableStore.AppendCallLegUsage`; nested TUR `leg_usage_records` unchanged. `LegOutcomeRejected`/`LegOutcomeNeverStarted` are shared with TUR Seal. Call-closure table is 1.3. `internal/core` LineBudget now 76123.
- 1.3 added `usage_call_records`, `JoinCompleteCall`/`ClaimCompleteCall`, and request-terminal `CallUsageAppender` freeze. Collector/TUR remain. Call-closure is split from the TUR command filter so Panic/GateReplacement still seal. Independent leg append from runtime is 5.1; ComposeBilling durable-spool requirement is 1.4.
