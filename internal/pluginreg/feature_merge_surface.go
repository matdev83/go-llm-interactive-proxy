package pluginreg

import (
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// MergedFeatureSurface is the concatenated contribution of all enabled feature plugins in
// registration order (session openers and workspace resolvers preserve bundle order within each plugin).
type MergedFeatureSurface struct {
	Hooks              hooks.Config
	Lifecycles         []lipplugin.Lifecycle
	SessionOpeners     []session.Opener
	WorkspaceResolvers []workspace.Resolver
	ToolCatalogFilters []toolcatalog.Filter
	RequestTransforms  []request.Transform
	RouteHintProviders []routehint.Provider
	CompletionGates    []completion.Gate
	TrafficObservers   []traffic.Observer
	RawCaptureSinks    []traffic.RawCaptureSink
	TrafficRedactors   []traffic.Redactor
}

// MergeFeatureSurface merges enabled feature plugins into hook configuration plus extension slices.
func (r *Registry) MergeFeatureSurface(registrations []lipsdk.Registration) (MergedFeatureSurface, error) {
	nFeat := 0
	for _, reg := range registrations {
		if reg.Kind == lipsdk.PluginKindFeature && reg.Enabled {
			nFeat++
		}
	}
	bundles := make([]lipfeature.FeatureBundle, 0, nFeat)
	for _, reg := range registrations {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled {
			continue
		}
		factoryKey := reg.RegistryFactoryKey()
		b, err := r.BuildFeatureBundle(factoryKey, reg.Config.Node)
		if err != nil {
			return MergedFeatureSurface{}, err
		}
		bundles = append(bundles, b)
	}
	var submitLen, reqLen, respLen, toolLen, lifeLen, openLen, wsLen, catLen, rtxLen, rhLen, cgLen int
	var obsLen, rawLen, redLen int
	for _, b := range bundles {
		submitLen += len(b.SubmitHooks)
		reqLen += len(b.RequestPartHooks)
		respLen += len(b.ResponsePartHooks)
		toolLen += len(b.ToolReactors)
		lifeLen += len(b.Lifecycles)
		openLen += len(b.SessionOpeners)
		wsLen += len(b.WorkspaceResolvers)
		catLen += len(b.ToolCatalogFilters)
		rtxLen += len(b.RequestTransforms)
		rhLen += len(b.RouteHintProviders)
		cgLen += len(b.CompletionGates)
		obsLen += len(b.TrafficObservers)
		rawLen += len(b.RawCaptureSinks)
		redLen += len(b.TrafficRedactors)
	}
	var out hooks.Config
	out.SubmitHooks = slices.Grow(out.SubmitHooks, submitLen)
	out.RequestPartHooks = slices.Grow(out.RequestPartHooks, reqLen)
	out.ResponsePartHooks = slices.Grow(out.ResponsePartHooks, respLen)
	out.ToolReactors = slices.Grow(out.ToolReactors, toolLen)
	lifes := slices.Grow([]lipplugin.Lifecycle(nil), lifeLen)
	openers := slices.Grow([]session.Opener(nil), openLen)
	resolvers := slices.Grow([]workspace.Resolver(nil), wsLen)
	catalog := slices.Grow([]toolcatalog.Filter(nil), catLen)
	transforms := slices.Grow([]request.Transform(nil), rtxLen)
	routeHints := slices.Grow([]routehint.Provider(nil), rhLen)
	compGates := slices.Grow([]completion.Gate(nil), cgLen)
	trafficObs := slices.Grow([]traffic.Observer(nil), obsLen)
	rawSinks := slices.Grow([]traffic.RawCaptureSink(nil), rawLen)
	redactors := slices.Grow([]traffic.Redactor(nil), redLen)
	for _, b := range bundles {
		out.SubmitHooks = append(out.SubmitHooks, b.SubmitHooks...)
		out.RequestPartHooks = append(out.RequestPartHooks, b.RequestPartHooks...)
		out.ResponsePartHooks = append(out.ResponsePartHooks, b.ResponsePartHooks...)
		out.ToolReactors = append(out.ToolReactors, b.ToolReactors...)
		lifes = append(lifes, b.Lifecycles...)
		openers = append(openers, b.SessionOpeners...)
		resolvers = append(resolvers, b.WorkspaceResolvers...)
		catalog = append(catalog, b.ToolCatalogFilters...)
		transforms = append(transforms, b.RequestTransforms...)
		routeHints = append(routeHints, b.RouteHintProviders...)
		compGates = append(compGates, b.CompletionGates...)
		trafficObs = append(trafficObs, b.TrafficObservers...)
		rawSinks = append(rawSinks, b.RawCaptureSinks...)
		redactors = append(redactors, b.TrafficRedactors...)
	}
	return MergedFeatureSurface{
		Hooks:              out,
		Lifecycles:         lifes,
		SessionOpeners:     openers,
		WorkspaceResolvers: resolvers,
		ToolCatalogFilters: catalog,
		RequestTransforms:  transforms,
		RouteHintProviders: routeHints,
		CompletionGates:    compGates,
		TrafficObservers:   trafficObs,
		RawCaptureSinks:    rawSinks,
		TrafficRedactors:   redactors,
	}, nil
}

// BuildFeatureHooks merges enabled feature plugins into hook bus configuration (brownfield API).
// For the full surface including session openers and workspace resolvers, use [Registry.MergeFeatureSurface].
func (r *Registry) BuildFeatureHooks(registrations []lipsdk.Registration) (hooks.Config, []lipplugin.Lifecycle, error) {
	m, err := r.MergeFeatureSurface(registrations)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return m.Hooks, m.Lifecycles, nil
}
