# Continuity / B2BUA scenario registry (specification bundle)

Stable identifiers for **continuity store** invariants in `internal/core/b2bua`. Each row maps to `SpecBundleContinuityScenarios()` in [`spec_bundle_scenarios.go`](../internal/core/b2bua/spec_bundle_scenarios.go).

| ID | Invariant (summary) | Primary test |
|----|---------------------|--------------|
| `SB-CONT-round-trip` | Resolve + Create round-trip preserves continuity key semantics. | `TestMemoryStore_ResolveCreate_roundTripContinuity` |
| `SB-CONT-replace-same-key` | Creating with the same continuity key replaces the prior A-leg. | `TestMemoryStore_Create_sameContinuityKeyReplacesOldALeg` |
| `SB-CONT-weighted-first-persisted` | Weighted-first consumed flag persists on the A-leg record. | `TestMemoryStore_WeightedFirstConsumed_persists` |
| `SB-CONT-bleg-monotonic` | B-leg ids allocate monotonic sequence numbers per A-leg. | `TestMemoryStore_NextBLeg_monotonicSeq` |
| `SB-CONT-attempt-lineage-order` | RecordAttempt stores attempts; LoadAttempts returns stable order. | `TestMemoryStore_RecordAttempt_and_LoadAttempts_order` |
| `SB-CONT-attempt-wrong-bleg-rejected` | Recording an attempt with a wrong B-leg id is rejected. | `TestMemoryStore_RecordAttempt_rejectsWrongBLegID` |
| `SB-CONT-ttl-sweep-anonymous` | TTL sweeps stale anonymous legs on create when configured. | `TestMemoryStore_TTL_sweepsStaleAnonymousLegsOnCreate` |
| `SB-CONT-contract-store-interface` | Store interface matches SDK continuity contract shape. | `TestContinuityContract_StoreInterfaceMatchesSDK` |
| `SB-CONT-interleaved-state-round-trip` | Thinker cycle state and memo reference round-trip on the A-leg; memo bodies stay in process-local `MemoStore`, not continuity. Empty state is harmless for routes without thinker. | `TestMemoryStore_InterleavedState` |

When adding or splitting tests, update `spec_bundle_scenarios.go`, this table, and keep `TestSpecBundle_continuityScenarios_referenceTests` passing (`go test -tags=precommit ./internal/core/b2bua/...`).
