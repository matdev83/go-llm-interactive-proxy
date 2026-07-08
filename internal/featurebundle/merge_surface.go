package featurebundle

import (
	"slices"

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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// MergedFeatureSurface is the concatenated contribution of all enabled feature plugins in
// registration order (session openers and workspace resolvers preserve bundle order within each plugin).
type MergedFeatureSurface struct {
	SubmitHooks            []sdk.SubmitHook
	RequestPartHooks       []sdk.RequestPartHook
	ResponsePartHooks      []sdk.ResponsePartHook
	ToolReactors           []sdk.ToolReactor
	ToolReactorErrorPolicy sdk.ToolReactorErrorPolicy
	Lifecycles             []lipplugin.Lifecycle
	SessionOpeners         []session.Opener
	WorkspaceResolvers     []workspace.Resolver
	ToolCatalogFilters     []toolcatalog.Filter
	ToolCallPolicies       []toolpolicy.Policy
	RequestTransforms      []request.Transform
	PreRequestHandlers     []prerequest.Handler
	RouteHintProviders     []routehint.Provider
	CompletionGates        []completion.Gate
	TrafficObservers       []traffic.Observer
	UsageObservers         []usage.Observer
	RawCaptureSinks        []traffic.RawCaptureSink
	TrafficRedactors       []traffic.Redactor
}

// MergeFeatureSurface merges enabled feature plugins into SDK hook slices plus extension surfaces.
// It calls reg.BuildFeatureBundle for each enabled feature plugin and concatenates the results.
func MergeFeatureSurface(reg *pluginreg.Registry, registrations []lipsdk.Registration) (MergedFeatureSurface, error) {
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
			return MergedFeatureSurface{}, err
		}
		bundles = append(bundles, b)
	}
	var submitLen, reqLen, respLen, toolLen, lifeLen, openLen, wsLen, catLen, polLen, rtxLen, preLen, rhLen, cgLen int
	var trafficObsLen, usageObsLen, rawLen, redLen int
	for _, b := range bundles {
		submitLen += len(b.SubmitHooks)
		reqLen += len(b.RequestPartHooks)
		respLen += len(b.ResponsePartHooks)
		toolLen += len(b.ToolReactors)
		lifeLen += len(b.Lifecycles)
		openLen += len(b.SessionOpeners)
		wsLen += len(b.WorkspaceResolvers)
		catLen += len(b.ToolCatalogFilters)
		polLen += len(b.ToolCallPolicies)
		rtxLen += len(b.RequestTransforms)
		preLen += len(b.PreRequestHandlers)
		rhLen += len(b.RouteHintProviders)
		cgLen += len(b.CompletionGates)
		trafficObsLen += len(b.TrafficObservers)
		usageObsLen += len(b.UsageObservers)
		rawLen += len(b.RawCaptureSinks)
		redLen += len(b.TrafficRedactors)
	}
	submitHooks := slices.Grow([]sdk.SubmitHook(nil), submitLen)
	reqHooks := slices.Grow([]sdk.RequestPartHook(nil), reqLen)
	respHooks := slices.Grow([]sdk.ResponsePartHook(nil), respLen)
	toolHooks := slices.Grow([]sdk.ToolReactor(nil), toolLen)
	lifes := slices.Grow([]lipplugin.Lifecycle(nil), lifeLen)
	openers := slices.Grow([]session.Opener(nil), openLen)
	resolvers := slices.Grow([]workspace.Resolver(nil), wsLen)
	catalog := slices.Grow([]toolcatalog.Filter(nil), catLen)
	policies := slices.Grow([]toolpolicy.Policy(nil), polLen)
	transforms := slices.Grow([]request.Transform(nil), rtxLen)
	preReqs := slices.Grow([]prerequest.Handler(nil), preLen)
	routeHints := slices.Grow([]routehint.Provider(nil), rhLen)
	compGates := slices.Grow([]completion.Gate(nil), cgLen)
	trafficObs := slices.Grow([]traffic.Observer(nil), trafficObsLen)
	usageObs := slices.Grow([]usage.Observer(nil), usageObsLen)
	rawSinks := slices.Grow([]traffic.RawCaptureSink(nil), rawLen)
	redactors := slices.Grow([]traffic.Redactor(nil), redLen)
	for _, b := range bundles {
		submitHooks = append(submitHooks, b.SubmitHooks...)
		reqHooks = append(reqHooks, b.RequestPartHooks...)
		respHooks = append(respHooks, b.ResponsePartHooks...)
		toolHooks = append(toolHooks, b.ToolReactors...)
		lifes = append(lifes, b.Lifecycles...)
		openers = append(openers, b.SessionOpeners...)
		resolvers = append(resolvers, b.WorkspaceResolvers...)
		catalog = append(catalog, b.ToolCatalogFilters...)
		policies = append(policies, b.ToolCallPolicies...)
		transforms = append(transforms, b.RequestTransforms...)
		preReqs = append(preReqs, b.PreRequestHandlers...)
		routeHints = append(routeHints, b.RouteHintProviders...)
		compGates = append(compGates, b.CompletionGates...)
		trafficObs = append(trafficObs, b.TrafficObservers...)
		usageObs = append(usageObs, b.UsageObservers...)
		rawSinks = append(rawSinks, b.RawCaptureSinks...)
		redactors = append(redactors, b.TrafficRedactors...)
	}
	return MergedFeatureSurface{
		SubmitHooks:        submitHooks,
		RequestPartHooks:   reqHooks,
		ResponsePartHooks:  respHooks,
		ToolReactors:       toolHooks,
		Lifecycles:         lifes,
		SessionOpeners:     openers,
		WorkspaceResolvers: resolvers,
		ToolCatalogFilters: catalog,
		ToolCallPolicies:   policies,
		RequestTransforms:  transforms,
		PreRequestHandlers: preReqs,
		RouteHintProviders: routeHints,
		CompletionGates:    compGates,
		TrafficObservers:   trafficObs,
		UsageObservers:     usageObs,
		RawCaptureSinks:    rawSinks,
		TrafficRedactors:   redactors,
	}, nil
}
