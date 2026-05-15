# Routing scenario registry (specification bundle)

Stable identifiers for **route selector parsing**, **model alias resolution**, and **routing planner** behavior in `internal/core/routing`. Each row maps to `SpecBundleRoutingScenarios()` in [`spec_bundle_scenarios.go`](../internal/core/routing/spec_bundle_scenarios.go).

| ID | Invariant (summary) | Primary test |
|----|---------------------|--------------|
| `SB-ROUTE-parse-primaries` | Route selector parsing extracts primary backend ids in order. | `TestParsePrimaries` |
| `SB-ROUTE-parse-failover-chain` | Failover order syntax expands backup primaries after `=>`. | `TestParseFailoverOrder` |
| `SB-ROUTE-parse-weighted-first` | Weighted and `[first]` arms parse without ambiguity. | `TestParseWeightedAndFirst` |
| `SB-ROUTE-parse-ttft-timeouts` | Global and per-leaf TTFT timeout annotations parse into routing metadata. | `TestParseTTFTTimeoutAnnotations` |
| `SB-ROUTE-parse-affinity` | Global route affinity annotations parse as selector-wide metadata and leaf-scoped stickiness is rejected. | `TestParseGlobalAffinity` |
| `SB-ROUTE-alias-exact` | Model alias resolver applies exact pattern matches before routing. | `TestAliasResolver_exactMatch` |
| `SB-ROUTE-planner-failover-order` | Failover expansion preserves left-to-right primaries when eligible. | `TestExpandFailoverLeftToRightPrimaries` |
| `SB-ROUTE-weighted-deterministic` | Weighted selection is deterministic for a fixed branch key and weight table. | `TestWeightedDeterministic` |
| `SB-ROUTE-model-only-backends` | Model-only backend hints apply to the resolved route list. | `TestApplyModelOnlyBackends` |
| `SB-ROUTE-planner-ttft-metadata` | Failover expansion preserves TTFT timeout metadata without changing candidate identity. | `TestExpandFailoverPreservesTTFTTimeoutMetadata` |

When adding or splitting tests, update `spec_bundle_scenarios.go`, this table, and keep `TestSpecBundle_routingScenarios_referenceTests` passing (`go test -tags=precommit ./internal/core/routing/...`).
