package runtime

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type testFeatureBundle struct {
	SchemaVersion                    int
	CompletionGates                  []completion.Gate
	RequestTransforms                []request.Transform
	PreRequestHandlers               []prerequest.Handler
	RouteHintProviders               []routehint.Provider
	AttemptTransforms                []request.AttemptTransform
	SessionOpeners                   []session.Opener
	WorkspaceResolvers               []lipworkspace.Resolver
	ToolCatalogFilters               []toolcatalog.Filter
	ToolCallPolicies                 []toolpolicy.Policy
	ToolCallFinalizers               []toolcall.Finalizer
	ToolCallFinalizationMaxArgsBytes int
	CompactionObservers              []compaction.Observer
	CompactionPreservers             []compaction.Preserver
	TrafficObservers                 []traffic.Observer
	UsageObservers                   []usage.Observer
	RawCaptureSinks                  []traffic.RawCaptureSink
	TrafficRedactors                 []traffic.Redactor
	StreamObserverFactories          []response.StreamObserverFactory
	LocalTurnHandlers                []localturn.Handler
	TerminalDecisionProvider         terminaldecision.Provider
	SecretGuards                     []secretguard.Guard
	Lifecycles                       []lipplugin.Lifecycle
}

func freezeBundle(b testFeatureBundle) lipfeature.FrozenPlaneSet {
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
	if len(b.WorkspaceResolvers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneWorkspaceResolvers, "test", b.WorkspaceResolvers)
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
	if b.ToolCallFinalizationMaxArgsBytes > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", b.ToolCallFinalizationMaxArgsBytes)
	}
	if len(b.CompactionObservers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneCompactionObservers, "test", b.CompactionObservers)
	}
	if len(b.CompactionPreservers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneCompactionPreservers, "test", b.CompactionPreservers)
	}
	if len(b.TrafficObservers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, "test", b.TrafficObservers)
	}
	if len(b.UsageObservers) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneUsageObservers, "test", b.UsageObservers)
	}
	if len(b.RawCaptureSinks) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneRawCaptureSinks, "test", b.RawCaptureSinks)
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
