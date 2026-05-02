package pluginreg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"gopkg.in/yaml.v3"
)

type noopOpen struct{}

func (noopOpen) ID() string { return "noop-open" }
func (noopOpen) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type rootRes struct{}

func (rootRes) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type noopCat struct{}

func (noopCat) ID() string                        { return "cat" }
func (noopCat) Order() int                        { return 0 }
func (noopCat) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (noopCat) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type noopRtx struct{}

func (noopRtx) ID() string                        { return "rtx" }
func (noopRtx) Order() int                        { return 0 }
func (noopRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (noopRtx) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type noopRH struct{}

func (noopRH) ID() string                        { return "rh" }
func (noopRH) Order() int                        { return 0 }
func (noopRH) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (noopRH) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type noopCompGate struct{}

func (noopCompGate) ID() string                        { return "cg" }
func (noopCompGate) Order() int                        { return 0 }
func (noopCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (noopCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type noopTrafficObs struct{}

func (noopTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type noopRawSink struct{}

func (noopRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type noopTrafficRed struct{}

func (noopTrafficRed) ID() string { return "red" }

func (noopTrafficRed) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

func TestMergeFeatureSurface_concatTraffic(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-traffic-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return feature.FeatureBundle{
			SchemaVersion:    feature.SchemaVersionV1,
			TrafficObservers: []traffic.Observer{noopTrafficObs{}},
			RawCaptureSinks:  []traffic.RawCaptureSink{noopRawSink{}},
			TrafficRedactors: []traffic.Redactor{noopTrafficRed{}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	m, err := reg.MergeFeatureSurface([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.TrafficObservers) != 1 || len(m.RawCaptureSinks) != 1 || len(m.TrafficRedactors) != 1 {
		t.Fatalf("traffic merge obs=%d raw=%d red=%d", len(m.TrafficObservers), len(m.RawCaptureSinks), len(m.TrafficRedactors))
	}
}

func TestMergeFeatureSurface_concatCompletionGates(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-cg-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return feature.FeatureBundle{
			SchemaVersion:   feature.SchemaVersionV1,
			CompletionGates: []completion.Gate{noopCompGate{}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	m, err := reg.MergeFeatureSurface([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.CompletionGates) != 1 {
		t.Fatalf("completion gates=%d", len(m.CompletionGates))
	}
}

func TestMergeFeatureSurface_concatOpenersAndResolvers(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-ext-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return feature.FeatureBundle{
			SchemaVersion:      feature.SchemaVersionV1,
			SessionOpeners:     []session.Opener{noopOpen{}},
			WorkspaceResolvers: []lipworkspace.Resolver{rootRes{}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	m, err := reg.MergeFeatureSurface([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.SessionOpeners) != 1 || len(m.WorkspaceResolvers) != 1 {
		t.Fatalf("openers=%d resolvers=%d", len(m.SessionOpeners), len(m.WorkspaceResolvers))
	}
}

func TestMergeFeatureSurface_concatCatalogAndTransforms(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-cat-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return feature.FeatureBundle{
			SchemaVersion:      feature.SchemaVersionV1,
			ToolCatalogFilters: []toolcatalog.Filter{noopCat{}},
			RequestTransforms:  []request.Transform{noopRtx{}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	m, err := reg.MergeFeatureSurface([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ToolCatalogFilters) != 1 || len(m.RequestTransforms) != 1 {
		t.Fatalf("catalog=%d transforms=%d", len(m.ToolCatalogFilters), len(m.RequestTransforms))
	}
}

func TestMergeFeatureSurface_concatRouteHints(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-rh-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return feature.FeatureBundle{
			SchemaVersion:      feature.SchemaVersionV1,
			RouteHintProviders: []routehint.Provider{noopRH{}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	m, err := reg.MergeFeatureSurface([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.RouteHintProviders) != 1 {
		t.Fatalf("route hints=%d", len(m.RouteHintProviders))
	}
}

type mergeTracePolicy struct {
	tag string
}

func (p mergeTracePolicy) ID() string                      { return p.tag }
func (mergeTracePolicy) Order() int                        { return 0 }
func (mergeTracePolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (mergeTracePolicy) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type mergeTraceUsage struct {
	tag string
}

func (mergeTraceUsage) OnUsage(context.Context, usage.Event) error { return nil }

func TestMergeFeatureSurface_mergeToolCallPoliciesUsageObserversRegistrationOrderIgnoresDisabled(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	facPol := "fac-merge-pol-" + strings.ReplaceAll(t.Name(), "/", "-")
	facPanic := "fac-merge-panic-" + strings.ReplaceAll(t.Name(), "/", "-")
	facUsage := "fac-merge-use-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(facPol, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return feature.FeatureBundle{
			SchemaVersion:    feature.SchemaVersionV1,
			ToolCallPolicies: []toolpolicy.Policy{mergeTracePolicy{tag: "policy-from-first"}},
			UsageObservers:   []usage.Observer{mergeTraceUsage{tag: "usage-from-first"}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterFeature(facPanic, func(yaml.Node) (feature.FeatureBundle, error) {
		panic("disabled registration must not invoke factory")
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterFeature(facUsage, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return feature.FeatureBundle{
			SchemaVersion:  feature.SchemaVersionV1,
			UsageObservers: []usage.Observer{mergeTraceUsage{tag: "usage-from-second"}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	m, err := reg.MergeFeatureSurface([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "en-a", FactoryKind: facPol, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: lipsdk.PluginKindFeature, ID: "dis", FactoryKind: facPanic, Enabled: false, Config: lipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: lipsdk.PluginKindFeature, ID: "en-b", FactoryKind: facUsage, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ToolCallPolicies) != 1 || m.ToolCallPolicies[0].ID() != "policy-from-first" {
		t.Fatalf("policies %+v", m.ToolCallPolicies)
	}
	if len(m.UsageObservers) != 2 {
		t.Fatalf("usage observers=%d", len(m.UsageObservers))
	}
	u0, ok := m.UsageObservers[0].(mergeTraceUsage)
	if !ok || u0.tag != "usage-from-first" {
		t.Fatalf("first usage observer %#v", m.UsageObservers[0])
	}
	u1, ok := m.UsageObservers[1].(mergeTraceUsage)
	if !ok || u1.tag != "usage-from-second" {
		t.Fatalf("second usage observer %#v", m.UsageObservers[1])
	}
}
