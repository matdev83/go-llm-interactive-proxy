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
| `SB-ROUTE-parse-parallel-basic` | Parallel '!' separator produces a parallel group with correct branch count and targets. | `TestParseParallelBasic` |
| `SB-ROUTE-parse-parallel-handicap` | Per-leg [handicap=N] annotations parse into parallel branch metadata. | `TestParseParallelHandicap` |
| `SB-ROUTE-parse-parallel-user-example` | Full user-provided parallel selector with mixed handicap and ttft_timeout annotations parses correctly. | `TestParseParallelUserExample` |
| `SB-ROUTE-parse-parallel-failover-of-groups` | Failover '\|' of parallel groups produces separate parallel arms. | `TestParseParallelFailoverOfParallelGroups` |
| `SB-ROUTE-parse-parallel-rejects-weighted-mix` | Parallel '!' mixed with weighted '^'/[weight]/[first] is rejected. | `TestParseParallelRejectsMixedWithWeighted` |
| `SB-ROUTE-planner-parallel-handicap-metadata` | Failover expansion preserves handicap metadata on parallel legs. | `TestExpandFailoverParallelPreservesHandicapMetadata` |
| `SB-ROUTE-model-only-parallel` | Model-only backend fill applies to parallel branches. | `TestApplyModelOnlyBackendsParallelBranches` |
| `SB-ROUTE-parse-thinker-forms` | `[thinker]` accepts bare and true-valued forms on weighted branches and rejects false/duplicate/misplaced/first-plus-thinker forms. | `TestParseThinkerAcceptedForms` |
| `SB-ROUTE-parse-thinker-hybrid` | One thinker branch plus one non-thinker weighted branch targeting a parallel executor group parses; general weighted/parallel mixing stays rejected. | `TestParseThinkerParallelHybridAccepted` |
| `SB-ROUTE-planner-thinker-cycle` | Thinker-aware weighted cycle repeats non-thinker branches by weight, appends thinker once, advances the cursor, and resets stale selector state. | `TestPickThinkerCycle_AdvancesCursorAndWraps` |
| `SB-ROUTE-planner-thinker-suppression` | Thinker suppression skips the thinker position for continuation turns and surfaces no-eligible-route when no executor remains. | `TestPickThinkerCycle_SuppressThinkerPicksExecutor` |
| `SB-ROUTE-regress-non-thinker-noninterference` | Selectors without `[thinker]` preserve weighted, failover, parallel, `[first]`, health, and context-size behavior with empty interleaved role and selector key. | `TestNonThinkerSelectorsPreserveExistingBehavior` |

When adding or splitting tests, update `spec_bundle_scenarios.go`, this table, and keep `TestSpecBundle_routingScenarios_referenceTests` passing (`go test -tags=precommit ./internal/core/routing/...`).
