# Residual Package Consumer Census & Transition Table Discharge

This document records the refreshed post-migration consumer census for all residual support, compose, and infrastructure packages touched during Phase 10 consolidation, fulfilling Task 10.1 and Requirement 11.

---

## 1. Residual Package Census & Retention Analysis

The census below records the non-test production consumers, Requirement 11 classification, retention decision, and architectural justification for each residual row touched during Phase 10.

| Residual Row | Package Path | Non-Test Production Consumers | Req-11 Classification | Retention Decision & Justification |
| :--- | :--- | :--- | :--- | :--- |
| **Row 1: Reasoning Replay Helper** | `internal/plugins/features/reasoningpreservation/reasoningreplay` | `internal/plugins/features/reasoningpreservation` (`catalog.go`), `internal/plugins/backends/openaicaps` (`compatible_replay.go`) | One-feature algorithm/policy placed under feature owner (Req 11.1, 11.4) | **RETAIN UNDER FEATURE**: Relocated from historical `internal/reasoningreplay`. Implements reasoning payload token replay extraction and verification. Consumed by parent feature and the OpenAI-compatible backend capability scanner (`openaicaps`) to detect model-level reasoning replay support. Completely eliminated from core and generic runtime composition. |
| **Row 2: Reasoning Compose Adapter** | `internal/standardplugins/featurehost/reasoning` | `internal/standardplugins/featurehost` (`generation.go`, `reasoning.go`) | Standard-feature composition adapter child/detail (Req 11.2, 11.3) | **RETAIN AS FEATUREHOST CHILD**: Relocated from historical `reasoningcompose`. Encapsulates reasoning preservation and compression compilation and auxiliary binding adaptation for standard distribution. It is strictly private to `featurehost` and is never imported or called by `internal/core` or `internal/infra/runtimebundle`. |
| **Row 3: Secret Guard Compose & Audit** | `internal/standardplugins/featurehost/secretguard` | `internal/standardplugins/featurehost` (`secretguard.go`) | Standard-feature composition adapter child/detail (Req 11.2, 11.3) | **RETAIN AS FEATUREHOST CHILD**: Consolidated from historical `secretguardcompose` and `secretaudit`. Encapsulates secret guard compilation, catalog scanning, and engine wiring for standard distribution. Generic `runtimebundle` accesses secret guard strictly through the standard `featurehost.Binding` contract and generic extension planes (specifically the binder-only `secret_guard_execution` plane), with zero concrete engine types leaking into generic build options and `ExtensionsOptions` containing no Secret Guard exception fields. |
| **Row 4a: Compaction Feature Adapter** | `internal/standardplugins/featurehost/compaction` | `internal/standardplugins/featurehost` (`compaction.go`, `process.go`, `runtime.go`) | Standard-feature composition adapter child/detail (Req 11.2, 11.3) | **RETAIN AS FEATUREHOST CHILD**: Relocated from historical `compactioncompose`. Encapsulates compaction feature configuration decoding, state persistence setup, and scheduler bounds extraction (`SchedulerBoundsFromConfig`). Only imported by `featurehost`. |
| **Row 4b: Generic Auxiliary Infrastructure** | `internal/infra/auxiliary` | Direct importer (`go list`): `internal/infra/runtimebundle` only, via 4 call sites — `background_aux_lifecycle.go:24` (`NewProductionBackgroundScheduler`), `build_executor.go:84,238` (`GenerationExecutorRunner` field/construction), `candidate_compile.go:31` (`GenerationRunner` field), `generation_auxiliary.go:12` (`NewGenerationExecutorRunner`). Independent feature sharing of the generic scheduler (Req 11.2 reuse evidence): (a) compaction continuity bounds — `background_aux_lifecycle.go:23` (`featurehost.CompactionSchedulerBounds`) → `featurehost/compaction.go:17` → `featurehost/compaction/scheduler.go:12` (`SchedulerBoundsFromConfig` returns generic `auxreq.SchedulerConfig`) feeding `NewProductionBackgroundScheduler`; (b) reasoning compression background work — `generation_auxiliary.go:11` (`newGenerationAuxiliaryRunner` binds generic `sdkauxiliary.BackgroundClient/Poller` over the auxiliary runner) → `compile_generation.go:117-118` → `featurehost/reasoning/generation.go:18-19,43,98-99` (compression consumes only the generic `auxiliary.BackgroundClient/Poller` ports). `go list` import set is `context, errors, internal/core/auxreq, internal/core/runtime, pkg/lipapi`: zero feature imports. | Genuinely shared generic infrastructure (Req 11.2) | **RETAIN AS GENERIC INFRASTRUCTURE**: `internal/infra/auxiliary` contains zero feature imports and operates solely on generic `auxreq.SchedulerConfig` bounds. It is the single generic execution substrate shared by the process background scheduler (compaction-derived bounds) and generation auxiliary execution (reasoning compression clients). |
| **Row 5: Billing Compose Maintenance Hooks** | `internal/infra/billingcompose` | Sole production consumer (`go list`): `internal/infra/runtimebundle` (`billing_compose.go:19,72,76,80,103`). True `go list` import set: stdlib `context, errors, fmt, slices, strings, sync`; `internal/core/billing` (domain billing contracts: `ChargePolicy`, `VersionRef`, maintenance usage store); `internal/core/runtime` (generic `runtime.BillingIdentity` composition-root identity bundle, `identity.go:19-20`); `pkg/lipapi` (canonical `lipapi.Call` in snapshot/identity signatures, `catalog.go:264`, `identity.go:14-15`); `pkg/lipsdk/scope` (generic principal-scope extraction, `identity.go:29`). Zero imports of `keepwarm` or any feature package (`keepwarm.go` touches only `core/billing`). | Generic infrastructure adapter (Req 11.2) | **RETAIN AS GENERIC INFRASTRUCTURE**: Every non-stdlib import is a generic seam — domain billing contracts, the generic composition-root identity bundle, the canonical call type, and generic scope extraction. Acts as a generic, protocol-agnostic sink for provider maintenance usage accounting. |
| **Row 6: Public Host Composition Seams** | `pkg/lipsdk/featurehost`, `pkg/lipsdk/reasoninghost`, `pkg/lipsdk/secretguardhost` | `pkg/lipruntime`, `internal/infra/runtimebundle` (`production_options.go`, `host_build.go`, `options.go`), `internal/standardplugins/featurehost` (`bindings.go`) | Feature-neutral public SDK host binding contract (Req 9.3, 11.4) | **RETAIN IN PUBLIC SDK**: Eliminates per-feature configuration fields (`ReasoningCompressionOptions`, `SecretGuardInputs`) from generic runtime options. Public hosts supply feature-specific trusted configuration via typed `featurehost.Binding` implementations (`reasoninghost.Binding`, `secretguardhost.Binding`). Generic `runtimebundle` forwards these bindings blindly without type-switching or importing concrete feature packages. |

---

## 2. Process Feature Ownership Transition Table Discharge Proof

Task 2.3 and Task 10.1 require verifying that all process-scoped feature resources have completed their migration out of `internal/infra/runtimebundle.ProcessServices` and into `internal/standardplugins/featurehost`, leaving zero legacy, unassigned, or dual-owner resources.

### Transition Table Final State

| Resource Name | Concrete Type | Ownership Assignment | Status | Test Discharge Reference |
| :--- | :--- | :--- | :--- | :--- |
| `KeepwarmPolicy` | `*keepwarm.PolicyStore` | `featurehost.Runtime.keepwarmPolicy` | **MIGRATED OUT** (Task 6.3) | `process_feature_ownership_transition_table_test.go:38` |
| `KeepwarmRegistry` | `*keepwarm.ManagerRegistry` | `featurehost.Runtime.keepwarmRegistry` | **MIGRATED OUT** (Task 6.3) | `process_feature_ownership_transition_table_test.go:49` |
| `TerminalDecisionPolicy` | `*sessionpolicy.Store` | `featurehost.Runtime.terminalPolicy` | **MIGRATED OUT** (Task 7.3) | `process_feature_ownership_transition_table_test.go:60` |
| `CompactionDetector` | `runtime.CompactionDetector` | `featurehost.Runtime.compactionDetector` | **MIGRATED OUT** (Task 3.3) | `process_feature_ownership_transition_table_test.go:71` |
| `BranchCoordinator` | `*state.BranchCoordinator` | `featurehost.Runtime.branchCoordinator` | **MIGRATED OUT** (Task 3.3) | `process_feature_ownership_transition_table_test.go:82` |
| `CompactionParentPort` | `*compaction.ParentPort` | `featurehost.Runtime.compactionParentPort` | **MIGRATED OUT** (Task 3.3) | `process_feature_ownership_transition_table_test.go:93` |
| `ConversationStore` | `conversationview.Store` | `featurehost.Runtime.conversationStore` | **MIGRATED OUT** (Task 4.3) | `process_feature_ownership_transition_table_test.go:104` |
| `BackgroundAux` | `*auxreq.BackgroundScheduler` | `ProcessServices.BackgroundAux` | **BORROWED GENERIC** (Task 10 / F1) | `process_feature_ownership_transition_table_test.go:115` |

### Architectural Discharge Proofs

1. **Zero Transferred Fields on ProcessServices**:
   - `TestProcessFeatureOwnership_TransitionTableIntegrity` validates via reflection that fields for all 7 transferred resources (`KeepwarmPolicy`, `KeepwarmRegistry`, `TerminalDecisionPolicy`, `CompactionDetector`, `BranchCoordinator`, `CompactionParentPort`, `ConversationStore`) do NOT exist on `ProcessServices`.
2. **Single Constructor and Cleanup Registration**:
   - `TestProcessFeatureOwnership_SingleConstructorValidation` proves that instantiating `ProcessServices` wires exactly one constructor per resource, with `featurehost.Runtime` owning the single closer for `TerminalDecisionPolicy` (`ClosersCount() == 1`).
3. **Dual Constructor Wiring Rejection**:
   - `TestProcessFeatureOwnership_DualConstructorWiringRejected` proves that any attempt to wire dual constructors or register cleanup in both legacy and featurehost paths is rejected with `ErrDualConstructorWiring`.
4. **Decoupled Borrowed Resource**:
   - `BackgroundAux` is verified by `TestProcessFeatureOwnership_MissingBorrowedResourceRejected` as the sole borrowed generic scheduler on `ProcessServices`, decoupled from all feature-specific bounds.

---

## 3. Exhaustive Residual Sweep: Concrete-Feature Imports Outside `internal/plugins/features/`

Sweep method (exact commands, run from the worktree root; `_test.go` files excluded from production-consumer candidacy):

- `git grep -n "internal/plugins/features/" -- "*.go" | grep -v "_test.go"` — full candidate enumeration.
- `git grep -n "infra/metrics" -- "*.go" | grep -v "_test.go"` — importers of the generic metrics package (verified: zero feature imports).
- `git grep -n "PrometheusCollector\|SetManager" -- "*.go" | grep -v "_test.go"` — consumers of the keep-warm Prometheus collector and swap method.
- `git grep -n "stdhttp/admin/keepwarm" -- "*.go" | grep -v "_test.go"` — consumers of the keep-warm admin handler.
- `git grep -n "\.Keepwarm\b\|PrometheusCollector\|SetManager" -- "*.go"` filtered to files outside `internal/plugins/features/keepwarm` and `internal/standardplugins/featurehost` — second-consumer check for the metrics collector.

### 3.1 Non-production references (verified, no action)

| Reference | File | Verification |
| :--- | :--- | :--- |
| Architecture-gate string rules | `internal/archtest/budgets.go:52-53` (file-size budget path strings), `internal/archtest/closure_import_rules.go`, `internal/archtest/import_rules.go:214-378` (forbidden-import rule patterns), `internal/archtest/tools/changesurface/report.go:286` (path-prefix classifier) | String literals in test/gate tooling, not production imports. The gates enforce the closure direction (feature tree must not depend on core/runtimebundle/featurehost; `pkg/lipruntime` must not depend on concrete features). |
| Comment-only mentions | `internal/core/config/interleaved.go:7`, `internal/core/runtime/interleaved_steering.go:23`, `pkg/lipsdk/feature/doc.go:146` | Doc comments naming the feature owner; `go` import lists verified clean. No BLOCKER-grade real import exists in `internal/core` production code. |
| Intra-feature imports | `internal/plugins/features/*` importing sibling feature packages (e.g. `catalog.go`, `bundle.go`, `plugin*.go`, `reasoningreplay` consumers) | Inside the feature zone itself; out of scope for this sweep. |
| Approved composition owner | `internal/standardplugins/**` (`features_install.go`, `standard_table.go`, `featurehost/**`, `reasoning_preservation_inject.go`, `tool_call_repair_inject.go`) | The designated standard-distribution composition owner; expected and approved. |
| Prior-census row | `internal/plugins/backends/openaicaps/compatible_replay.go:6` (imports `reasoningpreservation/reasoningreplay`) | Already classified as Row 1 (retained under feature; backend capability scanner reuse). No change. |

### 3.2 Residual adapters requiring classification

| Adapter | Non-Test Production Consumers | Req-11 Classification | Decision |
| :--- | :--- | :--- | :--- |
| `internal/plugins/features/keepwarm/metrics.go` (`PrometheusCollector`; formerly `internal/infra/metrics/keepwarm.go` / `KeepwarmProm`) | Featurehost owns the concrete keep-warm collector handle and manager (`featurehost/runtime.go:42`, `featurehost/process.go:159-163`, `featurehost/keepwarm.go:207`). Per-generation feed arrives through the opaque `CorePorts.MetricsSwap func()` registered into `PhasePublish` on the candidate resource ledger (`standard-features-metrics-publish`). Second-consumer check (`PrometheusCollector\|SetManager`) finds no other production consumer outside the feature and featurehost. | One-feature support placed under the feature owner (Req 11.1). | **RETAIN UNDER FEATURE OWNER**: Collector implementation, snapshot reading, bounded-event allowlist, and unit tests live under `internal/plugins/features/keepwarm/metrics.go`. Collector *registration* lifetime stays with metrics infrastructure: the bundle exposes its generic registry, and featurehost constructs + registers the collector via the narrow `MetricsRegistry` process capability (`Register(prometheus.Collector) error`, no feature knowledge). Featurehost owns the collector handle (`SetManager(*keepwarm.Manager)`, typed — no `any`); `GenerationInput.MetricsSink` and the `ManagerMetricsSink` interface are deleted. Generic `runtimebundle` sees only the opaque `MetricsSwap func()` callback and registers it into generic `PhasePublish`, so it runs only after successful generation publication. Rejected candidates cannot change process metrics. Pinned by `keepwarm_publication_timing_test.go` and `TestCompileKeepwarmMetricsSwapAndAdminProjection`. |
| `internal/stdhttp/admin/keepwarm/handler.go` (HTTP transport over feature-owned `keepwarm.SessionPolicy` + typed errors `ErrInvalidConfig`, `ErrPolicyCapacity`, `ErrPolicyNotFound`) | `internal/stdhttp/contract/http_input.go:25,89` (`KeepwarmAdmin adminkeepwarm.Options` port field), `internal/stdhttp/mount_admin.go:12,84` (`NewHandler` mounted at `/admin/keepwarm`), `internal/standardplugins/featurehost/keepwarm.go:12,124` + `runtime.go:19` + `inputs.go:17` (featurehost supplies the `Service` implementation and `Options` projection). | Host/process translation at a driving adapter (Req 11.3 analogue): HTTP request/response translation over feature-owned policy types, same direction and pattern as the classified backend reuse (Row 1, `openaicaps` → `reasoningreplay`). Permitted by the closure gates, which forbid only the reverse direction (feature tree depending on `stdhttp`). | **RETAIN IN `internal/stdhttp/admin/keepwarm` WITH DOCUMENTED JUSTIFICATION** (no move). The handler owns no policy: `Service` is a featurehost-supplied port, `ResolveALegID` keeps request bodies untrusted for policy identity, and error mapping is a pure wire translation of typed feature errors. |

### 3.3 Tree-budget evidence for the move decision

- `go test -count=1 ./internal/archtest/ -run TestPackageTreeBudgetsExact` on the current worktree: **PASS** — `internal/standardplugins/featurehost` measured 3280 against ceiling 3280. `internal/infra/runtimebundle` measured 12310 against ceiling 12333.
- The move adds the collector registration block plus the retained handle to the featurehost tree (offset by deleting the `ManagerMetricsSink` interface and the `MetricsSink` input field) and a self-contained collector file to the keepwarm feature package, which carries no tree budget. No `*.go` files remain in `internal/infra/metrics` importing concrete features (verified by the throwaway RED test pre-move; the passing tree is the post-move proof).
