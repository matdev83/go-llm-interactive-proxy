package feature_test

import (
	"context"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type stubLife struct{}

func (stubLife) Start(context.Context) error { return nil }
func (stubLife) Stop(context.Context) error  { return nil }

type stubSubmit struct {
	id  string
	ord int
}

func (s stubSubmit) ID() string                        { return s.id }
func (s stubSubmit) Order() int                        { return s.ord }
func (s stubSubmit) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubSubmit) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type stubTool struct {
	id  string
	ord int
}

func (s stubTool) ID() string { return s.id }
func (s stubTool) Order() int { return s.ord }
func (s stubTool) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

// mergeFeatureBundles mirrors composition-root merge semantics used by the registry
// (concatenate slices in registration order) for contract tests only.
func mergeFeatureBundles(a, b feature.FeatureBundle) feature.FeatureBundle {
	out := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
	}
	if a.SchemaVersion != 0 {
		out.SchemaVersion = a.SchemaVersion
	} else if b.SchemaVersion != 0 {
		out.SchemaVersion = b.SchemaVersion
	} else {
		out.SchemaVersion = feature.SchemaVersionV1
	}
	out.SubmitHooks = append(append([]sdkhooks.SubmitHook(nil), a.SubmitHooks...), b.SubmitHooks...)
	out.RequestPartHooks = append(append([]sdkhooks.RequestPartHook(nil), a.RequestPartHooks...), b.RequestPartHooks...)
	out.ResponsePartHooks = append(append([]sdkhooks.ResponsePartHook(nil), a.ResponsePartHooks...), b.ResponsePartHooks...)
	out.ToolReactors = append(append([]sdkhooks.ToolReactor(nil), a.ToolReactors...), b.ToolReactors...)
	out.Lifecycles = append(append([]lipplugin.Lifecycle(nil), a.Lifecycles...), b.Lifecycles...)
	out.SessionOpeners = append(append([]session.Opener(nil), a.SessionOpeners...), b.SessionOpeners...)
	out.WorkspaceResolvers = append(append([]workspace.Resolver(nil), a.WorkspaceResolvers...), b.WorkspaceResolvers...)
	out.ToolCatalogFilters = append(append([]toolcatalog.Filter(nil), a.ToolCatalogFilters...), b.ToolCatalogFilters...)
	out.ToolCallPolicies = append(append([]toolpolicy.Policy(nil), a.ToolCallPolicies...), b.ToolCallPolicies...)
	out.RequestTransforms = append(append([]request.Transform(nil), a.RequestTransforms...), b.RequestTransforms...)
	out.RouteHintProviders = slices.Concat(a.RouteHintProviders, b.RouteHintProviders)
	out.CompletionGates = append(append([]completion.Gate(nil), a.CompletionGates...), b.CompletionGates...)
	out.TrafficObservers = append(append([]traffic.Observer(nil), a.TrafficObservers...), b.TrafficObservers...)
	out.UsageObservers = append(append([]usage.Observer(nil), a.UsageObservers...), b.UsageObservers...)
	out.RawCaptureSinks = append(append([]traffic.RawCaptureSink(nil), a.RawCaptureSinks...), b.RawCaptureSinks...)
	out.TrafficRedactors = append(append([]traffic.Redactor(nil), a.TrafficRedactors...), b.TrafficRedactors...)
	return out
}

func TestEmptyFeatureBundle(t *testing.T) {
	t.Parallel()
	var b feature.FeatureBundle
	if b.SchemaVersion != 0 {
		t.Fatalf("zero SchemaVersion: %d", b.SchemaVersion)
	}
	if b.SubmitHooks != nil || b.RequestPartHooks != nil || b.ResponsePartHooks != nil || b.ToolReactors != nil {
		t.Fatal("expected all hook slices nil on zero value")
	}
	if b.Lifecycles != nil {
		t.Fatal("expected Lifecycles nil")
	}
	if b.SessionOpeners != nil || b.WorkspaceResolvers != nil {
		t.Fatal("expected session/workspace slices nil on zero value")
	}
	if b.ToolCatalogFilters != nil || b.RequestTransforms != nil || b.RouteHintProviders != nil {
		t.Fatal("expected catalog/transform/route-hint slices nil on zero value")
	}
	if b.CompletionGates != nil {
		t.Fatal("expected CompletionGates nil on zero value")
	}
	if b.TrafficObservers != nil || b.RawCaptureSinks != nil || b.TrafficRedactors != nil {
		t.Fatal("expected traffic slices nil on zero value")
	}
	if b.ToolCallPolicies != nil || b.UsageObservers != nil {
		t.Fatal("expected tool policy and usage observer slices nil on zero value")
	}
}

func TestFeatureBundlePreservesHooksAndLifecycles(t *testing.T) {
	t.Parallel()
	h1 := stubSubmit{id: "a", ord: 1}
	h2 := stubSubmit{id: "b", ord: 2}
	b := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		SubmitHooks:   []sdkhooks.SubmitHook{h1, h2},
		Lifecycles:    []lipplugin.Lifecycle{stubLife{}, stubLife{}},
	}
	if len(b.SubmitHooks) != 2 {
		t.Fatalf("submit hooks: %d", len(b.SubmitHooks))
	}
	if len(b.Lifecycles) != 2 {
		t.Fatalf("lifecycles: %d", len(b.Lifecycles))
	}
}

func TestFeatureBundle_Validate_emptyAndV1(t *testing.T) {
	t.Parallel()
	if err := (feature.FeatureBundle{}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}).Validate(); err != nil {
		t.Fatal(err)
	}
	bad := feature.FeatureBundle{
		SubmitHooks: []sdkhooks.SubmitHook{stubSubmit{id: "x", ord: 0}},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for hooks with schema version 0")
	}
	fixed := bad
	fixed.SchemaVersion = feature.SchemaVersionV1
	if err := fixed.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMergeFeatureBundlesAbsentChainsStayAbsent(t *testing.T) {
	t.Parallel()
	submitOnly := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		SubmitHooks:   []sdkhooks.SubmitHook{stubSubmit{id: "s", ord: 0}},
	}
	toolOnly := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		ToolReactors:  []sdkhooks.ToolReactor{stubTool{id: "t", ord: 0}},
	}
	merged := mergeFeatureBundles(submitOnly, toolOnly)
	if len(merged.SubmitHooks) != 1 {
		t.Fatalf("submit: %d", len(merged.SubmitHooks))
	}
	if merged.RequestPartHooks != nil {
		t.Fatalf("expected nil RequestPartHooks, got len=%d", len(merged.RequestPartHooks))
	}
	if merged.ResponsePartHooks != nil {
		t.Fatalf("expected nil ResponsePartHooks")
	}
	if len(merged.ToolReactors) != 1 {
		t.Fatalf("tool reactors: %d", len(merged.ToolReactors))
	}
}

type stubOpen struct{ id string }

func (s stubOpen) ID() string { return s.id }
func (stubOpen) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type stubWS struct{}

func (stubWS) Resolve(context.Context) (workspace.WorkspaceView, error) {
	return workspace.WorkspaceView{}, nil
}

func TestFeatureBundle_Validate_sessionWorkspaceRequiresSchemaV1(t *testing.T) {
	t.Parallel()
	bad := feature.FeatureBundle{SessionOpeners: []session.Opener{stubOpen{id: "x"}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected schema error")
	}
	ok := feature.FeatureBundle{
		SchemaVersion:  feature.SchemaVersionV1,
		SessionOpeners: []session.Opener{stubOpen{id: "x"}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	wsOnly := feature.FeatureBundle{
		SchemaVersion:      feature.SchemaVersionV1,
		WorkspaceResolvers: []workspace.Resolver{stubWS{}},
	}
	if err := wsOnly.Validate(); err != nil {
		t.Fatal(err)
	}
}
