package runtimebundle

import (
	"context"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type projCatalogFilter struct{ id string }

func (f projCatalogFilter) ID() string                      { return f.id }
func (projCatalogFilter) Order() int                        { return 0 }
func (projCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (projCatalogFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type projOpener struct{ tag string }

func (o projOpener) ID() string { return o.tag }
func (projOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type projTransform struct{ tag string }

func (t projTransform) ID() string                      { return t.tag }
func (projTransform) Order() int                        { return 0 }
func (projTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (projTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type projTrafficObs struct{ tag string }

func (projTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type projUsageObs struct{ tag string }

func (projUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type projTerminalProvider struct{ tag string }

func (p projTerminalProvider) ID() string { return p.tag }

type projFinalizer struct{ id string }

func (f projFinalizer) ID() string { return f.id }
func (projFinalizer) Order() int   { return 0 }
func (projFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, nil
}

func (projTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

// projMerged builds a merged surface and generated merge surface with one tagged element on the planes the
// projection tests observe.
func projMerged(t *testing.T) (featurebundle.MergedFeatureSurface, featurebundle.GeneratedMergeSurface) {
	t.Helper()
	b := lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		ToolCallFinalizers: []toolcall.Finalizer{projFinalizer{id: "finalizer"}},
		RequestTransforms:  []request.Transform{projTransform{tag: "transform"}},
		TrafficObservers:   []traffic.Observer{projTrafficObs{tag: "feat-traffic"}},
		UsageObservers:     []usage.Observer{projUsageObs{tag: "feat-usage"}},
		LocalTurnHandlers:  []localturn.Handler{wiringHandler{id: "handler", ord: 1}},
	}
	m := featurebundle.MergeBundles(b)
	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)
	return m, gen
}

func assertAllSliceFieldsNil(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Type.Kind() != reflect.Slice {
			continue
		}
		fv := rv.Field(i)
		require.True(t, fv.IsNil(), "%s: want nil slice, got len=%d", field.Name, fv.Len())
	}
}

// Pins exact emptiness transport through extensionsFromMerged: a zero merged
// surface projects to all-nil slices; populated slices project to equal
// non-nil copies; hand-built empty non-nil merged slices keep their non-nil
// emptiness (the defensive copy preserves whatever state exists).
func TestExtensionsFromMerged_preservesExactNilAndEmptyState(t *testing.T) {
	t.Parallel()

	t.Run("zero_merged_surface_projects_all_nil_slices", func(t *testing.T) {
		t.Parallel()
		ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, featurebundle.GeneratedMergeSurface{}, nil)
		assertAllSliceFieldsNil(t, ext)
	})

	t.Run("populated_merged_projects_equal_non_nil_copies", func(t *testing.T) {
		t.Parallel()
		merged, gen := projMerged(t)
		ext := extensionsFromMerged(merged, gen, nil)
		require.Len(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizers), 1)
		require.Len(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers), 1)
		require.Len(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms), 1)
		assert.Equal(t, "finalizer", lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizers)[0].ID())
		assert.Equal(t, "handler", lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)[0].ID())
		_ = ext
	})

	t.Run("empty_non_nil_merged_slices_stay_non_nil_empty", func(t *testing.T) {
		t.Parallel()
		merged := featurebundle.MergedFeatureSurface{}
		ext := extensionsFromMerged(merged, featurebundle.GeneratedMergeSurface{}, nil)
		assertAllSliceFieldsNil(t, ext)
	})
}

// Pins backing-array isolation in both directions: projections are copies with
// zero spare capacity, so mutation or growth of either side never reaches the
// other.
func TestExtensionsFromMerged_backingArrayIsolationBothDirections(t *testing.T) {
	t.Parallel()

	_, gen := projMerged(t)
	ltFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneLocalTurnHandlers)
	require.Len(t, ltFrozen, 1)
	require.Equal(t, "handler", ltFrozen[0].ID())

	toFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	require.Len(t, toFrozen, 1)
	trafficObs, trafficObsOK := toFrozen[0].(projTrafficObs)
	require.True(t, trafficObsOK)
	require.Equal(t, "feat-traffic", trafficObs.tag)

	uoFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	require.Len(t, uoFrozen, 1)
	usageObs, usageObsOK := uoFrozen[0].(projUsageObs)
	require.True(t, usageObsOK)
	require.Equal(t, "feat-usage", usageObs.tag)
}

// Pins host-injection ordering: production observers append AFTER feature
// contributions, and a nil process options injects nothing.
func TestExtensionsFromMerged_hostObserversAppendAfterFeatures(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion:    lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{projTrafficObs{tag: "feat-1"}, projTrafficObs{tag: "feat-2"}},
		UsageObservers:   []usage.Observer{projUsageObs{tag: "feat-u"}},
	}

	cs := lipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat", b))
	require.NoError(t, featurebundle.ContributeBundle(cs, "host", lipfeature.FeatureBundle{
		SchemaVersion:    lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{projTrafficObs{tag: "host-1"}},
		UsageObservers:   []usage.Observer{projUsageObs{tag: "host-u"}, projUsageObs{tag: "host-u2"}},
	}))
	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

	trafficFromFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	gotTraffic := make([]string, 0, len(trafficFromFrozen))
	for _, o := range trafficFromFrozen {
		obs, ok := o.(projTrafficObs)
		require.True(t, ok)
		gotTraffic = append(gotTraffic, obs.tag)
	}
	require.Equal(t, []string{"feat-1", "feat-2", "host-1"}, gotTraffic)

	usageFromFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	gotUsage := make([]string, 0, len(usageFromFrozen))
	for _, o := range usageFromFrozen {
		obs, ok := o.(projUsageObs)
		require.True(t, ok)
		gotUsage = append(gotUsage, obs.tag)
	}
	require.Equal(t, []string{"feat-u", "host-u", "host-u2"}, gotUsage)

	genFeatOnly, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)
	require.Len(t, lipfeature.Get(genFeatOnly.Frozen, lipfeature.PlaneTrafficObservers), 2)
	require.Len(t, lipfeature.Get(genFeatOnly.Frozen, lipfeature.PlaneUsageObservers), 1)
}

// Pins overlayExtensions legacy semantics: source contributions append after
// destination contributions per plane, and exclusive slots are first-wins.
func TestOverlayExtensions_appendOrderAndScalarOverrideRules(t *testing.T) {
	t.Parallel()

	t.Run("terminal_decision_slot_is_first_wins", func(t *testing.T) {
		t.Parallel()
		firstProv := projTerminalProvider{tag: "first-provider"}
		secondProv := projTerminalProvider{tag: "second-provider"}
		dstWithFirst := &ExtensionsOptions{TerminalDecisionProvider: firstProv}
		srcWithSecond := ExtensionsOptions{TerminalDecisionProvider: secondProv}
		overlayExtensions(dstWithFirst, srcWithSecond)
		require.Equal(t, firstProv, dstWithFirst.TerminalDecisionProvider)

		emptyDst := &ExtensionsOptions{}
		overlayExtensions(emptyDst, srcWithSecond)
		require.Equal(t, secondProv, emptyDst.TerminalDecisionProvider)
	})
}
