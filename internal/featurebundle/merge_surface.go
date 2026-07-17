package featurebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// MergedFeatureSurface is the concatenated contribution of all enabled feature plugins in
// registration order (session openers and workspace resolvers preserve bundle order within each plugin).
type MergedFeatureSurface struct {
	SubmitHooks                      []sdk.SubmitHook
	RequestPartHooks                 []sdk.RequestPartHook
	ResponsePartHooks                []sdk.ResponsePartHook
	ToolReactors                     []sdk.ToolReactor
	ToolReactorErrorPolicy           sdk.ToolReactorErrorPolicy
	Lifecycles                       []lipplugin.Lifecycle
	SessionOpeners                   []session.Opener
	WorkspaceResolvers               []workspace.Resolver
	ToolCatalogFilters               []toolcatalog.Filter
	ToolCallPolicies                 []toolpolicy.Policy
	ToolCallFinalizers               []toolcall.Finalizer
	ToolCallFinalizationMaxArgsBytes int
	RequestTransforms                []request.Transform
	PreRequestHandlers               []prerequest.Handler
	RouteHintProviders               []routehint.Provider
	CompletionGates                  []completion.Gate
	TrafficObservers                 []traffic.Observer
	UsageObservers                   []usage.Observer
	RawCaptureSinks                  []traffic.RawCaptureSink
	TrafficRedactors                 []traffic.Redactor
}

// Append concatenates all fields from bundle b into the receiver. This is the single
// merge point: every new FeatureBundle field requires exactly one append line here.
func (m *MergedFeatureSurface) Append(b lipfeature.FeatureBundle) {
	m.SubmitHooks = append(m.SubmitHooks, b.SubmitHooks...)
	m.RequestPartHooks = append(m.RequestPartHooks, b.RequestPartHooks...)
	m.ResponsePartHooks = append(m.ResponsePartHooks, b.ResponsePartHooks...)
	m.ToolReactors = append(m.ToolReactors, b.ToolReactors...)
	m.Lifecycles = append(m.Lifecycles, b.Lifecycles...)
	m.SessionOpeners = append(m.SessionOpeners, b.SessionOpeners...)
	m.WorkspaceResolvers = append(m.WorkspaceResolvers, b.WorkspaceResolvers...)
	m.ToolCatalogFilters = append(m.ToolCatalogFilters, b.ToolCatalogFilters...)
	m.ToolCallPolicies = append(m.ToolCallPolicies, b.ToolCallPolicies...)
	m.ToolCallFinalizers = append(m.ToolCallFinalizers, b.ToolCallFinalizers...)
	// Non-positive values are not merge contributions (zero = unset; negatives are
	// rejected by FeatureBundle.Validate before a valid bundle is merged).
	if b.ToolCallFinalizationMaxArgsBytes > 0 {
		if m.ToolCallFinalizationMaxArgsBytes <= 0 || b.ToolCallFinalizationMaxArgsBytes < m.ToolCallFinalizationMaxArgsBytes {
			m.ToolCallFinalizationMaxArgsBytes = b.ToolCallFinalizationMaxArgsBytes
		}
	}
	m.RequestTransforms = append(m.RequestTransforms, b.RequestTransforms...)
	m.PreRequestHandlers = append(m.PreRequestHandlers, b.PreRequestHandlers...)
	m.RouteHintProviders = append(m.RouteHintProviders, b.RouteHintProviders...)
	m.CompletionGates = append(m.CompletionGates, b.CompletionGates...)
	m.TrafficObservers = append(m.TrafficObservers, b.TrafficObservers...)
	m.UsageObservers = append(m.UsageObservers, b.UsageObservers...)
	m.RawCaptureSinks = append(m.RawCaptureSinks, b.RawCaptureSinks...)
	m.TrafficRedactors = append(m.TrafficRedactors, b.TrafficRedactors...)
}

// MergeBundles concatenates one or more FeatureBundles into a single MergedFeatureSurface,
// preserving bundle order across all slice fields.
func MergeBundles(bundles ...lipfeature.FeatureBundle) MergedFeatureSurface {
	var out MergedFeatureSurface
	for _, b := range bundles {
		out.Append(b)
	}
	return out
}

// buildEnabledFeatureBundles builds FeatureBundles from enabled feature registrations in order.
func buildEnabledFeatureBundles(reg *pluginreg.Registry, registrations []lipsdk.Registration) ([]lipfeature.FeatureBundle, error) {
	nFeat := 0
	for _, regEntry := range registrations {
		if regEntry.Kind == lipsdk.PluginKindFeature && regEntry.Enabled {
			nFeat++
		}
	}
	bundles := make([]lipfeature.FeatureBundle, 0, nFeat)
	for _, regEntry := range registrations {
		if regEntry.Kind != lipsdk.PluginKindFeature || !regEntry.Enabled {
			continue
		}
		factoryKey := regEntry.RegistryFactoryKey()
		b, err := reg.BuildFeatureBundle(factoryKey, regEntry.Config.Node)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, b)
	}
	return bundles, nil
}

// MergeFeatureSurface merges enabled feature plugins into SDK hook slices plus extension surfaces.
// It calls reg.BuildFeatureBundle for each enabled feature plugin and concatenates the results.
func MergeFeatureSurface(reg *pluginreg.Registry, registrations []lipsdk.Registration) (MergedFeatureSurface, error) {
	bundles, err := buildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return MergedFeatureSurface{}, err
	}
	return MergeBundles(bundles...), nil
}
