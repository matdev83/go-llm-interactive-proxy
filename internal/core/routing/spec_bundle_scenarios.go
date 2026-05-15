package routing

// RoutingScenarioSpec links a stable scenario identifier to route-selector and planning
// invariants and the primary regression test (specification bundle).
type RoutingScenarioSpec struct {
	ID               string
	InvariantSummary string
	TestName         string // exists in internal/core/routing/*_test.go
}

// SpecBundleRoutingScenarios lists selector, alias, and planner invariants. Keep aligned
// with docs/spec-bundle-routing-scenarios.md and .kiro/steering/routing-and-orchestration.md.
func SpecBundleRoutingScenarios() []RoutingScenarioSpec {
	return []RoutingScenarioSpec{
		{
			ID:               "SB-ROUTE-parse-primaries",
			InvariantSummary: "Route selector parsing extracts primary backend ids in order.",
			TestName:         "TestParsePrimaries",
		},
		{
			ID:               "SB-ROUTE-parse-failover-chain",
			InvariantSummary: "Failover order syntax expands backup primaries after `=>`.",
			TestName:         "TestParseFailoverOrder",
		},
		{
			ID:               "SB-ROUTE-parse-weighted-first",
			InvariantSummary: "Weighted and `[first]` arms parse without ambiguity.",
			TestName:         "TestParseWeightedAndFirst",
		},
		{
			ID:               "SB-ROUTE-parse-ttft-timeouts",
			InvariantSummary: "Global and per-leaf TTFT timeout annotations parse into routing metadata.",
			TestName:         "TestParseTTFTTimeoutAnnotations",
		},
		{
			ID:               "SB-ROUTE-parse-affinity",
			InvariantSummary: "Global route affinity annotations parse as selector-wide metadata and leaf-scoped stickiness is rejected.",
			TestName:         "TestParseGlobalAffinity",
		},
		{
			ID:               "SB-ROUTE-alias-exact",
			InvariantSummary: "Model alias resolver applies exact pattern matches before routing.",
			TestName:         "TestAliasResolver_exactMatch",
		},
		{
			ID:               "SB-ROUTE-planner-failover-order",
			InvariantSummary: "Failover expansion preserves left-to-right primaries when eligible.",
			TestName:         "TestExpandFailoverLeftToRightPrimaries",
		},
		{
			ID:               "SB-ROUTE-weighted-deterministic",
			InvariantSummary: "Weighted selection is deterministic for a fixed branch key and weight table.",
			TestName:         "TestWeightedDeterministic",
		},
		{
			ID:               "SB-ROUTE-model-only-backends",
			InvariantSummary: "Model-only backend hints apply to the resolved route list.",
			TestName:         "TestApplyModelOnlyBackends",
		},
		{
			ID:               "SB-ROUTE-planner-ttft-metadata",
			InvariantSummary: "Failover expansion preserves TTFT timeout metadata without changing candidate identity.",
			TestName:         "TestExpandFailoverPreservesTTFTTimeoutMetadata",
		},
	}
}
