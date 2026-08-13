# Implementation Plan

## Spec status

**Superseded and archived** under `.kiro/specs/archive/usage-accounting-architecture-convergence/`.
Stream-time money accounting was delivered by `usage-record-ledger-billing`; host injection by `billing-host-composition`. Remaining non-money quota/rate-limit work needs a new spec. Do not execute these tasks. See `closeout-evidence.md`.

> Execution is TDD-first. Each phase begins with RED characterization/contract tests, then production migration, then deletion/ratchets. Do not keep old/new production accounting authorities mutating in parallel beyond the minimal cutover step.

## Phase 0 — Baseline and Economic Invariant Freeze

- [ ] 1. Capture the accounting architecture baseline and current behavior inventory
- [ ] 1.1 Record the exact affected production surface and dependency graph
  - Measure non-test lines and symbol/import ownership for runtime accounting files, `internal/core/accounting`, `tokenaccounting/{domain,streamusage,ledger,observability}`, `metering/plane`, control-plane usage/report paths, and relevant runtimebundle wiring.
  - Record current direct writers/readers of the metering journal and token ledger and current direct `UsageAuthority`/coordinator call sites.
  - Produce a checked-in implementation evidence baseline used by the final shrinkage/deletion gates.
  - _Boundary: tests / architecture governance_
  - _Depends: spec approval_
  - _Validation: go test ./internal/archtest/..._
  - _Requirements: 14.1, 14.2, 14.8, 14.9, 14.10, 14.13_

- [ ] 1.2 Add RED regression tests for the known semantic split points
  - Characterize `Reconstructor.Reconciled` vs runtime merge behavior, value-based dedupe, explicit zero/absence, multi-scope authority, estimated/authoritative money, and duplicate usage identity.
  - Add a RED report test where cumulative `5` then cumulative `10` must reduce/report as `10`, not `15`, plus authoritative replacement/correction cases.
  - Add a RED equal-value/different-identity case proving both observations remain billable evidence.
  - _Boundary: tests / domain policy_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/tokenaccounting/... ./internal/core/metering/... ./internal/core/controlplane/..._
  - _Requirements: 3.2, 3.3, 4.2, 4.3, 4.5, 9.1, 9.2, 12.1, 12.2, 12.6_

- [ ] 1.3 Freeze the terminal/accounting scenario matrix before ownership moves
  - Characterize normal winner, sequential failover, parallel loser, swallowed attempt, pre-output error, post-output error, cancellation before/after output, EOF recovery, completion-gate replacement, frontend encoder failure, and late final billing.
  - Assert one customer lifecycle across retries and independent operator attempt lifecycles.
  - _Boundary: tests / runtime orchestration_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/runtime/..._
  - _Requirements: 1.2, 1.3, 1.4, 6.5, 6.6, 6.7, 12.4, 12.5_

## Phase 1 — Make Metering Reduction the Sole Fact Semantics

- [ ] 2. Complete the metering reducer and fix report reconstruction
- [ ] 2.1 Extend reducer RED tests into a reusable metamorphic corpus
  - Cover delta chunking equivalence, replay identity, equal-value distinct facts, cumulative replacement, correction, authoritative replacement, supersession, explicit zero/absence, overflow, mixed currency, and stable stream metadata.
  - Add effective source/authority expectations for token quantities and money.
  - _Boundary: tests / domain policy_
  - _Depends: 1.2_
  - _Validation: go test ./internal/core/metering/aggregate/..._
  - _Requirements: 3.2, 3.4, 3.5, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.9, 12.2_

- [ ] 2.2 Evolve `metering/aggregate` to return complete reduced stream snapshots
  - Preserve stream perspective/boundary/lifecycle/correlation and effective per-component/money provenance/authority.
  - Add fact references and a `ReduceByStream`-style pure helper for bounded fact sets.
  - Reject mixed stream metadata and retain checked arithmetic/currency rules.
  - Do not introduce a new public EconomicStatement DTO.
  - _Boundary: internal core / domain policy_
  - _Depends: 2.1_
  - _Validation: go test ./internal/core/metering/aggregate/..._
  - _Requirements: 3.4, 3.7, 4.1, 4.6, 4.7, 13.5_

- [ ] 2.3 Cut dual-plane economics reports over to reduced snapshots
  - Refactor `DualPlaneReportInputsFromFacts`/metering bridge to group by stream, reduce each stream, then aggregate independent snapshots.
  - Preserve completeness/legacy provenance and explicit cross-plane calculations.
  - Add report==reducer metamorphic tests and cumulative/replacement regression tests.
  - _Boundary: query seam / control plane_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/controlplane/... ./pkg/lipsdk/controlplane/..._
  - _Requirements: 3.9, 9.1, 9.2, 9.3, 9.4, 9.7, 9.8, 12.6_

- [ ] 2.4 Add reducer/read-model architecture guardrails
  - Prevent raw report aggregation helpers from bypassing the reducer for aggregate economics.
  - Record the reducer as the only allowed owner of delta/cumulative/correction/replacement semantics.
  - _Boundary: tests / architecture governance_
  - _Depends: 2.3_
  - _Validation: go test ./internal/archtest/..._
  - _Requirements: 4.8, 12.10, 14.10_

## Phase 2 — Establish One Accounting Application Owner and One Rating Path

- [ ] 3. Introduce concrete request/attempt accounting lifecycles without runtime cutover
- [ ] 3.1 Write RED accounting ingest and lifecycle contracts
  - Define fact projection expectations for canonical usage presence, cost presence, DedupeKey identity, generated sequence identity, local estimated cumulative facts, and final authoritative replacement.
  - Define Request/Attempt lifecycle tests with in-memory fact buffers and existing counter/rater/authority ports.
  - _Boundary: tests / app orchestration_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/accounting/..._
  - _Requirements: 2.3, 3.1, 3.2, 3.8, 5.2, 5.3, 5.4, 5.6, 6.1, 6.2, 12.1_

- [ ] 3.2 Implement concrete `accounting.Service`, `Request`, and `Attempt`
  - Keep contexts method-scoped; no per-request goroutines.
  - Request owns FE ingress/egress customer evidence and request authority/rating state.
  - Attempt owns BE ingress/egress operator facts, provider evidence, attempt authority/rating state, and correction refs.
  - Use one in-memory fact model whether durable recorder is present or nil.
  - _Boundary: internal core / app orchestration_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/accounting/..._
  - _Requirements: 1.8, 2.3, 2.4, 2.8, 3.5, 3.6, 3.8, 6.1, 6.2, 6.5, 6.7, 6.8, 13.2, 13.3, 13.4_

- [ ] 3.3 Move the static price catalog behind `economics.Rater`
  - Freeze current price parsing, optional-zero-rate, cache/reasoning, rounding, overflow, and currency behavior in golden tests.
  - Implement/move a static rater adapter under infrastructure and bind a version/source.
  - Remove any accounting lifecycle need to call `EstimateCost` directly.
  - _Boundary: driven adapter / economics_
  - _Depends: 3.1_
  - _Validation: go test ./internal/infra/economics/... ./internal/core/accounting/..._
  - _Requirements: 7.1, 7.2, 7.3, 7.8, 7.9, 13.1_

- [ ] 3.4 Implement provider-money precedence and versioned rating facts
  - Provider-reported present money remains authoritative operator evidence.
  - If absent, rate reduced operator/customer quantities with the bound rater; if no valid rater, preserve absence/unavailable.
  - Preserve independent customer/operator rating and add the minimal generic rating-version evidence field if needed.
  - _Boundary: internal core / public economics contract_
  - _Depends: 3.2, 3.3_
  - _Validation: go test ./internal/core/accounting/... ./pkg/lipsdk/economics/... ./pkg/lipsdk/metering/..._
  - _Requirements: 1.5, 3.6, 7.3, 7.4, 7.5, 7.6, 7.7, 9.8_

## Phase 3 — Converge Built-In and External Authority Paths

- [ ] 4. Route all usage authority through request/attempt coordinators
- [ ] 4.1 Add RED parity tests for direct UsageAuthority vs coordinator-wrapped behavior
  - Cover request/attempt admission, multi-rule descriptors, clamp preview/enforcement, fail-open/fail-closed, cancellation, partial/final settlement, loser release, and authoritative re-settlement.
  - _Boundary: tests / authority app orchestration_
  - _Depends: 3.2_
  - _Validation: go test ./internal/core/authoritycoord/... ./internal/core/usageauthority/... ./internal/core/runtime/..._
  - _Requirements: 8.1, 8.2, 8.4, 8.5, 8.6, 8.7, 12.4_

- [ ] 4.2 Register built-in usage authority as coordinator providers
  - Compose built-in request/attempt authority adapters through the same provider stack as external authorities.
  - Ensure generation-bound policy/rating refs and compensation stacks remain unchanged.
  - Make clamp preview use the same provider/coordinator architecture.
  - _Boundary: composition root / authority coordination_
  - _Depends: 4.1_
  - _Validation: go test ./internal/core/authoritycoord/... ./internal/infra/runtimebundle/..._
  - _Requirements: 8.1, 8.2, 8.7, 8.8, 13.1_

- [ ] 4.3 Make accounting Request/Attempt settle from reduced fact-backed amounts
  - Pass fact/exposure refs and reduced per-unit authority into coordinators.
  - Keep token/request authority independent from money authority.
  - Prove losing/swallowed attempts settle incurred operator exposure and release residual only.
  - _Boundary: app orchestration / authority coordination_
  - _Depends: 3.4, 4.2_
  - _Validation: go test ./internal/core/accounting/... ./internal/core/usageauthority/..._
  - _Requirements: 8.3, 8.4, 8.5, 8.6_

- [ ] 4.4 Remove new-call dependence on direct UsageAuthority lifecycle APIs
  - Keep temporary compatibility only where required for staged migration tests.
  - Add an architecture rule preventing new runtime direct admission/settlement calls.
  - _Boundary: architecture governance / runtime_
  - _Depends: 4.2, 4.3_
  - _Validation: go test ./internal/archtest/... ./internal/core/runtime/..._
  - _Requirements: 2.7, 8.1, 14.4, 14.10_

## Phase 4 — Cut Runtime Over to Accounting Handles

- [ ] 5. Remove economic policy/state from `retryRecvStream`
- [ ] 5.1 Add RED runtime integration tests using accounting lifecycle handles
  - Prove canonical event ordering, synthesized client usage ordering, no-retry-after-output, and terminal behavior for the frozen scenario matrix.
  - Assert the accounting handle does not decide routes or terminal winner.
  - _Boundary: tests / runtime orchestration_
  - _Depends: 1.3, 3.2, 4.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/accounting/..._
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.7, 6.3, 6.6, 6.7, 12.4_

- [ ] 5.2 Replace runtime economic fields with Request/Attempt handles
  - Begin Request at the logical-request accounting boundary and Attempt after final B-leg input is frozen/authorized.
  - Route backend usage/sideband observations and released client output to the appropriate handle.
  - Preserve request/generation/scope reattachment without storing contexts inside accounting objects.
  - _Boundary: internal core / runtime orchestration_
  - _Depends: 5.1_
  - _Validation: go test ./internal/core/runtime/..._
  - _Requirements: 2.2, 6.1, 6.3, 6.4, 6.9_

- [ ] 5.3 Cut final/cancel/error settlement to accounting results
  - Use reduced attempt/request results for authority settlement, egress facts, and client usage projection.
  - Remove production reliance on `StreamUsage.Reconstruct`/runtime event-array merges after parity is green.
  - Keep client usage emission timing owned by runtime.
  - _Boundary: internal core / runtime + accounting_
  - _Depends: 5.1, 5.2_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/accounting/..._
  - _Requirements: 2.1, 2.2, 5.3, 5.5, 5.6, 6.2, 6.3, 6.5, 6.9, 14.3_

- [ ] 5.4 Unify terminal accounting calls and separate performance telemetry
  - Make normal finish/cancel/EOF/error/gated/close paths call the same request/attempt finalize APIs from the winning terminal path.
  - Keep terminal CAS in runtime and preserve terminal-work durable recovery.
  - Rename/extract TTFT/TPS tracking so it is not economic accounting state.
  - _Boundary: internal core / runtime orchestration_
  - _Depends: 5.2, 5.3_
  - _Validation: go test -race ./internal/core/runtime/... ./internal/core/accounting/..._
  - _Requirements: 6.6, 6.7, 6.8, 6.10, 11.6, 12.9_

## Phase 5 — Make Metering the Only Writable Usage Evidence and Simplify Read Models

- [ ] 6. Retire dual-write and legacy authoritative reporting paths
- [ ] 6.1 Stop direct runtime writes to the token-accounting ledger
  - Add RED proof that live accounting succeeds from in-memory facts with optional durable metering sink.
  - Remove runtime ledger writer/observability coupling from request execution.
  - _Boundary: runtime / driven adapter_
  - _Depends: 5.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/accounting/... ./internal/core/tokenaccounting/..._
  - _Requirements: 3.8, 10.1, 10.6, 14.6_

- [ ] 6.2 Inventory token-ledger consumers and choose projection or deletion
  - Enumerate admin/public/tool/test consumers of memory/SQLite/PostgreSQL token-ledger stores.
  - If required, implement query-time or rebuildable one-way metering->legacy projection; otherwise delete durable token-ledger store/schema and wiring.
  - Document the retained consumer/retirement trigger when compatibility remains.
  - _Boundary: query seam / compatibility / composition_
  - _Depends: 1.1, 6.1_
  - _Validation: go test ./internal/core/tokenaccounting/... ./internal/infra/tokenaccounting/... ./internal/stdhttp/..._
  - _Requirements: 10.2, 10.3, 10.4, 10.5, 14.11_

- [ ] 6.3 Remove legacy usage-observer input from authoritative control-plane economics
  - Keep `pkg/lipsdk/usage.Observer` available for best-effort extension telemetry.
  - Make economic usage/report queries depend on metering facts/reduced snapshots and explicit completeness.
  - Remove/demote `UsageObserverAdapter` from economic truth paths.
  - _Boundary: control-plane query seam / SDK observer_
  - _Depends: 2.3, 6.1_
  - _Validation: go test ./internal/core/controlplane/... ./internal/infra/controlplane/... ./pkg/lipsdk/usage/..._
  - _Requirements: 2.6, 3.9, 9.4, 9.5, 9.6, 10.8_

- [ ] 6.4 Add deterministic economic replay/debug projection
  - Expose safe request/attempt -> stream/fact -> reduced value -> rating version -> authority mutation correlation for tests/diagnostics.
  - Do not persist raw prompt/completion content or credentials.
  - Reuse the production reducer for replay.
  - _Boundary: diagnostics / query seam_
  - _Depends: 2.2, 4.3, 6.3_
  - _Validation: go test ./internal/core/controlplane/... ./internal/core/accounting/..._
  - _Requirements: 11.1, 11.2, 11.3, 11.4, 11.5_

## Phase 6 — Certify Provider and Connector Usage Evidence Semantics

- [ ] 7. Make adapter usage evidence obey one canonical contract
- [ ] 7.1 Add a reusable backend usage-evidence contract suite
  - Test additive canonical deltas, presence/all-zero usage, provider zero cost where available, cache/reasoning inclusion, stable DedupeKey replay, equal-value distinct evidence, and finalization correction.
  - Reuse real `lipapi`/metering types and small probes; no internal call-graph mocks.
  - _Boundary: tests / backend contract_
  - _Depends: 3.1_
  - _Validation: go test ./internal/testkit/... ./internal/plugins/backends/..._
  - _Requirements: 5.1, 5.2, 5.3, 5.7, 12.3, 12.8_

- [ ] 7.2 Normalize built-in backends that can emit repeated cumulative vendor usage
  - Characterize each backend first; make no change to final-only providers unnecessarily.
  - Convert subsequent cumulative snapshots to additive canonical usage while preserving presence and cost identity.
  - _Boundary: backend plugins / provider adapters_
  - _Depends: 7.1_
  - _Validation: go test ./internal/plugins/backends/..._
  - _Requirements: 5.1, 5.2, 5.7, 13.6_

- [ ] 7.3 Map executable connector sideband directly to metering facts
  - Preserve mandatory DedupeKey/presence/source/authority from `AccountingEvidence`.
  - Ensure host-only evidence never becomes client-visible stream output merely to reach accounting.
  - Apply `FinalizeBilling` complete evidence as cumulative/replacement/correction according to lifecycle role.
  - _Boundary: backend-plugin adapter / accounting ingest_
  - _Depends: 3.2, 7.1_
  - _Validation: go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/adapter/..._
  - _Requirements: 5.5, 5.6, 5.7, 5.8_

- [ ] 7.4 Prove adapter contract coverage without provider-SDK leakage into core
  - Add architecture/import assertions and connector-facing contract coverage where appropriate.
  - _Boundary: tests / architecture governance_
  - _Depends: 7.2, 7.3_
  - _Validation: go test ./internal/archtest/... ./pkg/lipsdk/backendplugin/..._
  - _Requirements: 1.6, 2.5, 5.7, 12.10_

## Phase 7 — Remove Legacy Reconciliation and Authority Compatibility Debt

- [ ] 8. Delete competing semantic owners after cutover is green
- [ ] 8.1 Remove tokenaccounting stream reconciliation and value-based dedupe
  - Delete/migrate `tokenaccounting/domain.Reconcile`, `streamusage` ownership, and tests superseded by fact-reducer/accounting lifecycle coverage.
  - Keep token measurement/preflight packages focused.
  - _Boundary: internal core / deletion_
  - _Depends: 5.4, 7.3_
  - _Validation: go test ./internal/core/tokenaccounting/... ./internal/core/accounting/... ./internal/core/metering/..._
  - _Requirements: 4.8, 10.7, 14.4, 14.7_

- [ ] 8.2 Delete runtime usage merge/rating/ledger semantic mirrors
  - Remove `authorityUsageEvent`, `mergeUsageEventsForClient`, duplicate aggregated-counter projection, per-event cost enrichment, customer evidence accumulator, runtime usage dedupe map, and direct token-ledger writer where migrated.
  - _Boundary: internal core / runtime deletion_
  - _Depends: 5.4, 6.1_
  - _Validation: go test ./internal/core/runtime/... ./internal/archtest/..._
  - _Requirements: 6.9, 14.5, 14.6, 14.7, 14.10_

- [ ] 8.3 Retire direct UsageAuthority fallback and scalar mutation mirrors
  - Delete direct runtime/service fallback after coordinator parity is proven.
  - Migrate in-repo authority callers to reservation/settlement descriptor sets; retain one compatibility adapter only if consumer inventory requires it.
  - Stop generating new/default `legacy_provider_preferred_attempt` rule configuration and document migration/retirement.
  - _Boundary: authority app / compatibility deletion_
  - _Depends: 4.4, 5.4_
  - _Validation: go test ./internal/core/usageauthority/... ./internal/core/authoritycoord/... ./internal/infra/runtimebundle/..._
  - _Requirements: 8.1, 8.9, 8.10, 14.11_

- [ ] 8.4 Remove redundant `metering/plane`/control-plane compatibility helpers
  - Keep only helpers with a distinct remaining boundary responsibility; move pure event->fact/client projection to accounting owner and reducer semantics to aggregate.
  - _Boundary: internal core / deletion_
  - _Depends: 2.3, 5.3, 6.3_
  - _Validation: go test ./internal/core/metering/... ./internal/core/controlplane/... ./internal/core/accounting/..._
  - _Requirements: 4.8, 13.6, 14.7_

## Phase 8 — Architecture Ratchets, Documentation, and Release Certification

- [ ] 9. Prove the final architecture and shrinkage
- [ ] 9.1 Add permanent economic dependency and deleted-symbol architecture rules
  - Forbid runtime imports of retired reconcile/streamusage/legacy-ledger packages and reintroduction of deleted semantic helpers/direct authority fallback.
  - Require control-plane aggregate economics to use reducer-backed projection.
  - Require accounting core to remain free of provider SDKs/SQL/stdhttp/concrete plugins.
  - _Boundary: tests / architecture governance_
  - _Depends: 8.1, 8.2, 8.3, 8.4_
  - _Validation: go test ./internal/archtest/..._
  - _Requirements: 2.7, 10.8, 12.10, 14.10_

- [ ] 9.2 Enforce package/file budgets and measured contraction
  - Compare final affected surfaces to the Phase 0 baseline.
  - Require >=40% runtime accounting-specific line reduction, >=20% defined legacy economic-surface reduction, and no net `internal/core` growth attributable to the spec.
  - Do not weaken the gate by moving code into generic helper packages.
  - _Boundary: architecture governance_
  - _Depends: 9.1_
  - _Validation: make arch-report && go test ./internal/archtest/..._
  - _Requirements: 13.7, 14.8, 14.9_

- [ ] 9.3 Update architecture/steering/accounting documentation
  - Document metering vs rating vs authority vs financial accounting boundaries.
  - Update core package classifications and operator debugging/replay guidance.
  - Document canonical additive usage semantics and provider adapter obligations.
  - _Boundary: docs / steering_
  - _Depends: 9.1_
  - _Validation: make quality-checks_
  - _Requirements: 5.1, 11.2, 13.8, 14.11_

- [ ] 9.4 Run final correctness, race, persistence, and release-grade certification
  - Run focused reducer/accounting/authority/control-plane/provider contract suites.
  - Run applicable race tests and durable SQLite/PostgreSQL metering/authority tests, including existing pooler requirements.
  - Run `make quality-checks`, `make test`, and relevant integration/QA gates; record any environment-gated evidence truthfully.
  - Produce final architecture/deletion/shrinkage evidence and verify no second production accounting authority remains.
  - _Boundary: tests / release certification_
  - _Depends: 9.2, 9.3_
  - _Validation: make quality-checks && make test && make test-race_
  - _Requirements: 12.7, 12.9, 14.12_
