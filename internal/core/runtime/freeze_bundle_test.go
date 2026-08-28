package runtime

import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

func freezeBundle(b lipfeature.FeatureBundle) lipfeature.FrozenPlaneSet {
	cs := lipfeature.NewContributionSet()
	if len(b.CompletionGates) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneCompletionGates, "test", b.CompletionGates)
	}
	if len(b.RequestTransforms) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneRequestTransforms, "test", b.RequestTransforms)
	}
	if len(b.PreRequestHandlers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlanePreRequestHandlers, "test", b.PreRequestHandlers)
	}
	if len(b.RouteHintProviders) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneRouteHintProviders, "test", b.RouteHintProviders)
	}
	if len(b.AttemptTransforms) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "test", b.AttemptTransforms)
	}
	if len(b.SessionOpeners) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneSessionOpeners, "test", b.SessionOpeners)
	}
	if len(b.ToolCatalogFilters) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCatalogFilters, "test", b.ToolCatalogFilters)
	}
	if len(b.ToolCallPolicies) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallPolicies, "test", b.ToolCallPolicies)
	}
	if len(b.ToolCallFinalizers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, "test", b.ToolCallFinalizers)
	}
	if len(b.CompactionObservers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneCompactionObservers, "test", b.CompactionObservers)
	}
	if len(b.CompactionPreservers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneCompactionPreservers, "test", b.CompactionPreservers)
	}
	if len(b.TrafficRedactors) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneTrafficRedactors, "test", b.TrafficRedactors)
	}
	if len(b.StreamObserverFactories) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "test", b.StreamObserverFactories)
	}
	if len(b.LocalTurnHandlers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneLocalTurnHandlers, "test", b.LocalTurnHandlers)
	}
	if b.TerminalDecisionProvider != nil {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "test", b.TerminalDecisionProvider)
	}
	if len(b.SecretGuards) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneSecretGuards, "test", b.SecretGuards)
	}
	return cs.Freeze()
}
