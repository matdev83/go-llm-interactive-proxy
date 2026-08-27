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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func (projTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

// projMerged builds a merged surface and generated merge surface with one tagged element on the planes the
// projection tests observe.
func projMerged(t *testing.T) (featurebundle.MergedFeatureSurface, featurebundle.GeneratedMergeSurface) {
	t.Helper()
	b := lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		SessionOpeners:    []session.Opener{projOpener{tag: "opener"}},
		RequestTransforms: []request.Transform{projTransform{tag: "transform"}},
		TrafficObservers:  []traffic.Observer{projTrafficObs{tag: "feat-traffic"}},
		UsageObservers:    []usage.Observer{projUsageObs{tag: "feat-usage"}},
		LocalTurnHandlers: []localturn.Handler{wiringHandler{id: "handler", ord: 1}},
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
		require.Len(t, ext.SessionOpeners, len(merged.SessionOpeners))
		require.Len(t, ext.LocalTurnHandlers, len(merged.LocalTurnHandlers))
		require.Len(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms), 1)
		assert.Equal(t, "opener", ext.SessionOpeners[0].ID())
		assert.Equal(t, "handler", ext.LocalTurnHandlers[0].ID())
	})

	t.Run("empty_non_nil_merged_slices_stay_non_nil_empty", func(t *testing.T) {
		t.Parallel()
		merged := featurebundle.MergedFeatureSurface{
			SessionOpeners:    []session.Opener{},
			LocalTurnHandlers: []localturn.Handler{},
		}
		ext := extensionsFromMerged(merged, featurebundle.GeneratedMergeSurface{}, nil)
		for name, got := range map[string]any{
			"SessionOpeners":    ext.SessionOpeners,
			"LocalTurnHandlers": ext.LocalTurnHandlers,
		} {
			rv := reflect.ValueOf(got)
			require.False(t, rv.IsNil(), "%s must stay non-nil empty", name)
			require.Zero(t, rv.Len(), "%s must stay empty", name)
		}
	})
}

// Pins backing-array isolation in both directions: projections are copies with
// zero spare capacity, so mutation or growth of either side never reaches the
// other.
func TestExtensionsFromMerged_backingArrayIsolationBothDirections(t *testing.T) {
	t.Parallel()

	merged, gen := projMerged(t)
	ext := extensionsFromMerged(merged, gen, nil)

	ext.SessionOpeners[0] = projOpener{tag: "mutated"}
	toFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	require.Len(t, toFrozen, 1)
	trafficObs, trafficObsOK := toFrozen[0].(projTrafficObs)
	require.True(t, trafficObsOK)
	require.Equal(t, "feat-traffic", trafficObs.tag)
	require.Equal(t, "opener", merged.SessionOpeners[0].ID())

	uoFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	require.Len(t, uoFrozen, 1)
	require.Equal(t, "feat-usage", uoFrozen[0].(projUsageObs).tag)
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
// destination contributions per plane, the finalizer-cap scalar uses
// overwrite-if-positive (NOT min-reduce), and exclusive slots are first-wins.
func TestOverlayExtensions_appendOrderAndScalarOverrideRules(t *testing.T) {
	t.Parallel()

	dst := &ExtensionsOptions{
		SessionOpeners:                   []session.Opener{projOpener{tag: "d-open"}},
		ToolCallFinalizationMaxArgsBytes: 4096,
	}
	src := ExtensionsOptions{
		SessionOpeners:                   []session.Opener{projOpener{tag: "s-open"}},
		ToolCallFinalizationMaxArgsBytes: 1024,
	}

	overlayExtensions(dst, src)
	require.Equal(t, []string{"d-open", "s-open"},
		[]string{dst.SessionOpeners[0].ID(), dst.SessionOpeners[1].ID()})

	tests := []struct {
		name string
		dstV int
		srcV int
		want int
	}{
		{"src_zero_keeps_dst", 4096, 0, 4096},
		{"src_positive_overwrites_larger_dst", 8192, 1024, 1024},
		{"src_positive_overwrites_smaller_dst", 1024, 8192, 8192},
		{"both_zero", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &ExtensionsOptions{ToolCallFinalizationMaxArgsBytes: tt.dstV}
			overlayExtensions(d, ExtensionsOptions{ToolCallFinalizationMaxArgsBytes: tt.srcV})
			require.Equal(t, tt.want, d.ToolCallFinalizationMaxArgsBytes)
		})
	}

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
