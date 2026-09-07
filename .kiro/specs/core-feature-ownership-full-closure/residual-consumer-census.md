# Residual Package Consumer Census & Transition Table Discharge

This document records the refreshed post-migration consumer census for all residual support, compose, and infrastructure packages touched during Phase 10 consolidation, fulfilling Task 10.1 and Requirement 11.

---

## 1. Residual Package Census & Retention Analysis

The census below records the non-test production consumers, Requirement 11 classification, retention decision, and architectural justification for each residual row touched during Phase 10.

| Residual Row | Package Path | Non-Test Production Consumers | Req-11 Classification | Retention Decision & Justification |
| :--- | :--- | :--- | :--- | :--- |
| **Row 1: Reasoning Replay Helper** | `internal/plugins/features/reasoningpreservation/reasoningreplay` | `internal/plugins/features/reasoningpreservation` (`catalog.go`), `internal/plugins/backends/openaicaps` (`compatible_replay.go`) | One-feature algorithm/policy placed under feature owner (Req 11.1, 11.4) | **RETAIN UNDER FEATURE**: Relocated from historical `internal/reasoningreplay`. Implements reasoning payload token replay extraction and verification. Consumed by parent feature and the OpenAI-compatible backend capability scanner (`openaicaps`) to detect model-level reasoning replay support. Completely eliminated from core and generic runtime composition. |
| **Row 2: Reasoning Compose Adapter** | `internal/standardplugins/featurehost/reasoning` | `internal/standardplugins/featurehost` (`generation.go`, `reasoning.go`) | Standard-feature composition adapter child/detail (Req 11.2, 11.3) | **RETAIN AS FEATUREHOST CHILD**: Relocated from historical `reasoningcompose`. Encapsulates reasoning preservation and compression compilation and auxiliary binding adaptation for standard distribution. It is strictly private to `featurehost` and is never imported or called by `internal/core` or `internal/infra/runtimebundle`. |
| **Row 3: Secret Guard Compose & Audit** | `internal/standardplugins/featurehost/secretguard` | `internal/standardplugins/featurehost` (`secretguard.go`) | Standard-feature composition adapter child/detail (Req 11.2, 11.3) | **RETAIN AS FEATUREHOST CHILD**: Consolidated from historical `secretguardcompose` and `secretaudit`. Encapsulates secret guard compilation, catalog scanning, and engine wiring for standard distribution. Generic `runtimebundle` accesses secret guard strictly through the standard `featurehost.Binding` contract and generic extension planes, with zero concrete engine types leaking into generic build options. |
| **Row 4a: Compaction Feature Adapter** | `internal/standardplugins/featurehost/compaction` | `internal/standardplugins/featurehost` (`compaction.go`, `process.go`, `runtime.go`) | Standard-feature composition adapter child/detail (Req 11.2, 11.3) | **RETAIN AS FEATUREHOST CHILD**: Relocated from historical `compactioncompose`. Encapsulates compaction feature configuration decoding, state persistence setup, and scheduler bounds extraction (`SchedulerBoundsFromConfig`). Only imported by `featurehost`. |
| **Row 4b: Generic Auxiliary Infrastructure** | `internal/infra/auxiliary` | `internal/infra/runtimebundle` (`background_aux_lifecycle.go`, `build_executor.go`, `candidate_compile.go`, `generation_auxiliary.go`) | Genuinely shared generic infrastructure (Req 11.2) | **RETAIN AS GENERIC INFRASTRUCTURE**: Following F1 remediation, `internal/infra/auxiliary` contains zero feature imports and zero feature-specific configuration decoding. It operates solely on generic `auxreq.SchedulerConfig` bounds, providing shared process background scheduling and generation auxiliary execution across multiple independent consumers in `runtimebundle`. |
| **Row 5: Billing Compose Maintenance Hooks** | `internal/infra/billingcompose` | `internal/infra/runtimebundle` (`billing_compose.go`) | Generic infrastructure adapter (Req 11.2) | **RETAIN AS GENERIC INFRASTRUCTURE**: Adapts durable store maintenance accounting (`ComposeMaintenanceAccounting`, `DurableMaintenanceObserver`). Imports only `internal/core/billing` and standard library; contains zero imports of `keepwarm` or any feature package. Acts as a generic, protocol-agnostic sink for provider maintenance usage accounting. |
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
