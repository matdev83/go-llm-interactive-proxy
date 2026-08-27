package runtimebundle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stub types for observer projection testing ---

type stubTrafficObs struct {
	id     string
	events *[]string
	mu     *sync.Mutex
}

func (o stubTrafficObs) OnObservation(ctx context.Context, obs traffic.Observation) error {
	if o.mu != nil && o.events != nil {
		o.mu.Lock()
		*o.events = append(*o.events, o.id)
		o.mu.Unlock()
	}
	return nil
}

type stubUsageObs struct {
	id     string
	events *[]string
	mu     *sync.Mutex
}

func (o stubUsageObs) OnUsage(ctx context.Context, ev usage.Event) error {
	if o.mu != nil && o.events != nil {
		o.mu.Lock()
		*o.events = append(*o.events, o.id)
		o.mu.Unlock()
	}
	return nil
}

type stubRawSink struct {
	id     string
	events *[]string
	mu     *sync.Mutex
}

func (s stubRawSink) WriteRaw(ctx context.Context, leg traffic.Leg, meta traffic.CaptureMeta, payload []byte) error {
	if s.mu != nil && s.events != nil {
		s.mu.Lock()
		*s.events = append(*s.events, s.id)
		s.mu.Unlock()
	}
	return nil
}

type stubRedactor struct {
	id     string
	events *[]string
	mu     *sync.Mutex
	prefix string
}

func (r stubRedactor) ID() string { return r.id }

func (r stubRedactor) Redact(ctx context.Context, leg traffic.Leg, meta traffic.CaptureMeta, payload []byte) ([]byte, error) {
	if r.mu != nil && r.events != nil {
		r.mu.Lock()
		*r.events = append(*r.events, r.id)
		o := r.prefix + ":" + string(payload)
		r.mu.Unlock()
		return []byte(o), nil
	}
	return payload, nil
}

func TestObserversProjection_ParityWithFrozenAndExpectedConfig(t *testing.T) {
	t.Parallel()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "to-1"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "uo-1"},
		},
		RawCaptureSinks: []traffic.RawCaptureSink{
			stubRawSink{id: "raw-1"},
		},
		TrafficRedactors: []traffic.Redactor{
			stubRedactor{id: "red-1", prefix: "r1"},
		},
	}

	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "to-2"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "uo-2"},
		},
		RawCaptureSinks: []traffic.RawCaptureSink{
			stubRawSink{id: "raw-2"},
		},
		TrafficRedactors: []traffic.Redactor{
			stubRedactor{id: "red-2", prefix: "r2"},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)

	// Verify all 4 planes project identically to lipfeature.Get on gen.Frozen
	assert.Equal(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers), ext.TrafficObservers)
	assert.Equal(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers), ext.UsageObservers)
	assert.Equal(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneRawCaptureSinks), ext.RawCaptureSinks)
	assert.Equal(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficRedactors), ext.TrafficRedactors)

	// Verify counts and identities
	require.Len(t, ext.TrafficObservers, 2)
	assert.Equal(t, "to-1", ext.TrafficObservers[0].(stubTrafficObs).id)
	assert.Equal(t, "to-2", ext.TrafficObservers[1].(stubTrafficObs).id)

	require.Len(t, ext.UsageObservers, 2)
	assert.Equal(t, "uo-1", ext.UsageObservers[0].(stubUsageObs).id)
	assert.Equal(t, "uo-2", ext.UsageObservers[1].(stubUsageObs).id)

	require.Len(t, ext.RawCaptureSinks, 2)
	assert.Equal(t, "raw-1", ext.RawCaptureSinks[0].(stubRawSink).id)
	assert.Equal(t, "raw-2", ext.RawCaptureSinks[1].(stubRawSink).id)

	require.Len(t, ext.TrafficRedactors, 2)
	assert.Equal(t, "red-1", ext.TrafficRedactors[0].ID())
	assert.Equal(t, "red-2", ext.TrafficRedactors[1].ID())
}

func TestObserversProjection_HostInjectionOrdering(t *testing.T) {
	t.Parallel()

	cs := lipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat-plugin", lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "feat-to-1"},
			stubTrafficObs{id: "feat-to-2"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "feat-uo-1"},
		},
	}))
	require.NoError(t, featurebundle.ContributeBundle(cs, "host", lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "host-to-1"},
			stubTrafficObs{id: "host-to-2"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "host-uo-1"},
		},
	}))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}
	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)

	// Feature-then-host ordering for TrafficObservers
	require.Len(t, ext.TrafficObservers, 4)
	assert.Equal(t, "feat-to-1", ext.TrafficObservers[0].(stubTrafficObs).id)
	assert.Equal(t, "feat-to-2", ext.TrafficObservers[1].(stubTrafficObs).id)
	assert.Equal(t, "host-to-1", ext.TrafficObservers[2].(stubTrafficObs).id)
	assert.Equal(t, "host-to-2", ext.TrafficObservers[3].(stubTrafficObs).id)

	// Feature-then-host ordering for UsageObservers
	require.Len(t, ext.UsageObservers, 2)
	assert.Equal(t, "feat-uo-1", ext.UsageObservers[0].(stubUsageObs).id)
	assert.Equal(t, "host-uo-1", ext.UsageObservers[1].(stubUsageObs).id)
}

func TestObserversProjection_ExactNilAndEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_generated_surface_projects_nil_slices", func(t *testing.T) {
		t.Parallel()
		ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, featurebundle.GeneratedMergeSurface{}, nil)
		assert.Nil(t, ext.TrafficObservers)
		assert.Nil(t, ext.UsageObservers)
		assert.Nil(t, ext.RawCaptureSinks)
		assert.Nil(t, ext.TrafficRedactors)
	})

	t.Run("empty_explicit_slices_project_nil_slices", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{
			SchemaVersion:    lipfeature.SchemaVersionV1,
			TrafficObservers: []traffic.Observer{},
			UsageObservers:   []usage.Observer{},
			RawCaptureSinks:  []traffic.RawCaptureSink{},
			TrafficRedactors: []traffic.Redactor{},
		})
		require.NoError(t, err)

		ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)
		assert.Nil(t, ext.TrafficObservers, "TrafficObservers must normalize explicit empty to nil")
		assert.Nil(t, ext.UsageObservers, "UsageObservers must normalize explicit empty to nil")
		assert.Nil(t, ext.RawCaptureSinks, "RawCaptureSinks must normalize explicit empty to nil")
		assert.Nil(t, ext.TrafficRedactors, "TrafficRedactors must normalize explicit empty to nil")
	})
}

func TestObserversProjection_OverlayBranchesOmitted(t *testing.T) {
	t.Parallel()

	dst := &ExtensionsOptions{
		TrafficObservers: []traffic.Observer{stubTrafficObs{id: "dst-to"}},
		UsageObservers:   []usage.Observer{stubUsageObs{id: "dst-uo"}},
		RawCaptureSinks:  []traffic.RawCaptureSink{stubRawSink{id: "dst-raw"}},
		TrafficRedactors: []traffic.Redactor{stubRedactor{id: "dst-red"}},
	}
	src := ExtensionsOptions{
		TrafficObservers: []traffic.Observer{stubTrafficObs{id: "src-to"}},
		UsageObservers:   []usage.Observer{stubUsageObs{id: "src-uo"}},
		RawCaptureSinks:  []traffic.RawCaptureSink{stubRawSink{id: "src-raw"}},
		TrafficRedactors: []traffic.Redactor{stubRedactor{id: "src-red"}},
	}

	overlayExtensions(dst, src)

	// Since observer families are consolidated through generated plane adapters,
	// overlayExtensions must not contain hand-coded append branches for them.
	// dst must retain its original slices without source appending.
	require.Len(t, dst.TrafficObservers, 1, "overlayExtensions must not append TrafficObservers")
	assert.Equal(t, "dst-to", dst.TrafficObservers[0].(stubTrafficObs).id)

	require.Len(t, dst.UsageObservers, 1, "overlayExtensions must not append UsageObservers")
	assert.Equal(t, "dst-uo", dst.UsageObservers[0].(stubUsageObs).id)

	require.Len(t, dst.RawCaptureSinks, 1, "overlayExtensions must not append RawCaptureSinks")
	assert.Equal(t, "dst-raw", dst.RawCaptureSinks[0].(stubRawSink).id)

	require.Len(t, dst.TrafficRedactors, 1, "overlayExtensions must not append TrafficRedactors")
	assert.Equal(t, "dst-red", dst.TrafficRedactors[0].ID())
}

func TestObserversProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	gen, err := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "orig-to"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "orig-uo"},
		},
		RawCaptureSinks: []traffic.RawCaptureSink{
			stubRawSink{id: "orig-raw"},
		},
		TrafficRedactors: []traffic.Redactor{
			stubRedactor{id: "orig-red"},
		},
	})
	require.NoError(t, err)

	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)

	// Mutate projected slices
	ext.TrafficObservers[0] = stubTrafficObs{id: "mutated-to"}
	ext.UsageObservers[0] = stubUsageObs{id: "mutated-uo"}
	ext.RawCaptureSinks[0] = stubRawSink{id: "mutated-raw"}
	ext.TrafficRedactors[0] = stubRedactor{id: "mutated-red"}

	// Re-reading from Frozen must return untouched original values
	toFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	assert.Equal(t, "orig-to", toFrozen[0].(stubTrafficObs).id)

	uoFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	assert.Equal(t, "orig-uo", uoFrozen[0].(stubUsageObs).id)

	rawFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneRawCaptureSinks)
	assert.Equal(t, "orig-raw", rawFrozen[0].(stubRawSink).id)

	redFrozen := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficRedactors)
	assert.Equal(t, "orig-red", redFrozen[0].ID())
}

func TestObserversProjection_EndToEndSnapshotDispatch(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var trafficEvents, usageEvents, rawEvents, redactorEvents []string

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "to-1", events: &trafficEvents, mu: &mu},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "uo-1", events: &usageEvents, mu: &mu},
		},
		RawCaptureSinks: []traffic.RawCaptureSink{
			stubRawSink{id: "raw-1", events: &rawEvents, mu: &mu},
		},
		TrafficRedactors: []traffic.Redactor{
			stubRedactor{id: "red-1", prefix: "r1", events: &redactorEvents, mu: &mu},
		},
	}

	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "to-2", events: &trafficEvents, mu: &mu},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "uo-2", events: &usageEvents, mu: &mu},
		},
		RawCaptureSinks: []traffic.RawCaptureSink{
			stubRawSink{id: "raw-2", events: &rawEvents, mu: &mu},
		},
		TrafficRedactors: []traffic.Redactor{
			stubRedactor{id: "red-2", prefix: "r2", events: &redactorEvents, mu: &mu},
		},
	}

	cs := lipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "b1", b1))
	require.NoError(t, featurebundle.ContributeBundle(cs, "b2", b2))
	require.NoError(t, featurebundle.ContributeBundle(cs, "host", lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "host-to", events: &trafficEvents, mu: &mu},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "host-uo", events: &usageEvents, mu: &mu},
		},
	}))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}
	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)

	bus := hooks.New(hooks.Config{})
	var err error
	snap := buildRuntimeSnapshot(bus, &config.Config{}, &BuildOptions{Extensions: ext}, time.Now, nil, &controlPlaneRuntime{}, nil, extensions.SecretGuardPlane{}, nil)
	require.NotNil(t, snap)

	// 1. Dispatch TrafficObserver
	obs := snap.TrafficObserver()
	require.NotNil(t, obs)
	err = obs.OnObservation(context.Background(), traffic.Observation{})
	require.NoError(t, err)
	assert.Equal(t, []string{"to-1", "to-2", "host-to"}, trafficEvents)

	// 2. Dispatch UsageObserver
	uobs := snap.UsageObserver()
	require.NotNil(t, uobs)
	err = uobs.OnUsage(context.Background(), usage.Event{})
	require.NoError(t, err)
	assert.Equal(t, []string{"uo-1", "uo-2", "host-uo"}, usageEvents)

	// 3. Dispatch RawCapture
	raw := snap.RawCapture()
	require.NotNil(t, raw)
	err = raw.WriteRaw(context.Background(), traffic.LegCTP, traffic.CaptureMeta{}, []byte("test"))
	require.NoError(t, err)
	assert.Equal(t, []string{"raw-1", "raw-2"}, rawEvents)

	// 4. Dispatch TrafficRedactors
	redactors := snap.TrafficRedactors()
	require.Len(t, redactors, 2)
	assert.Equal(t, "red-1", redactors[0].ID())
	assert.Equal(t, "red-2", redactors[1].ID())

	// 5. TrafficPortBundle returns unified triple
	portBundle := snap.TrafficPortBundle()
	assert.NotNil(t, portBundle.Obs)
	assert.NotNil(t, portBundle.Raw)
	assert.Len(t, portBundle.Red, 2)
}

func TestObserversProjection_PluginRegistryLifecyclePreserved(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	toFacID := "test.to"
	uoFacID := "test.uo"

	err := reg.RegisterFeature(toFacID, func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			TrafficObservers: []traffic.Observer{
				stubTrafficObs{id: "reg-to"},
			},
		}, nil
	})
	require.NoError(t, err)

	err = reg.RegisterFeature(uoFacID, func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			UsageObservers: []usage.Observer{
				stubUsageObs{id: "reg-uo"},
			},
		}, nil
	})
	require.NoError(t, err)

	var cfgNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &cfgNode))

	// Both plugins enabled
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "inst-to", FactoryKind: toFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: lipsdk.PluginKindFeature, ID: "inst-uo", FactoryKind: uoFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.NoError(t, err)

	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)
	require.Len(t, ext.TrafficObservers, 1)
	require.Len(t, ext.UsageObservers, 1)
	assert.Equal(t, "reg-to", ext.TrafficObservers[0].(stubTrafficObs).id)
	assert.Equal(t, "reg-uo", ext.UsageObservers[0].(stubUsageObs).id)

	// Disabled plugin contributes nothing
	genDisabled, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "inst-to", FactoryKind: toFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: lipsdk.PluginKindFeature, ID: "inst-uo", FactoryKind: uoFacID, Enabled: false, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.NoError(t, err)

	extDisabled := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, genDisabled, nil)
	require.Len(t, extDisabled.TrafficObservers, 1)
	require.Empty(t, extDisabled.UsageObservers, "disabled usage plugin must not contribute to usage observers")
}
