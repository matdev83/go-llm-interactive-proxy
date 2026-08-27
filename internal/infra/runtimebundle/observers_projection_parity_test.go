package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/localstubreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
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

type stubStreamObsFactory struct {
	id         string
	ord        int
	openCount  *int
	mu         *sync.Mutex
	createdObs *[]string
}

func (f stubStreamObsFactory) ID() string                        { return f.id }
func (f stubStreamObsFactory) Order() int                        { return f.ord }
func (f stubStreamObsFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (f stubStreamObsFactory) Open(ctx context.Context, meta response.StreamMeta, svc response.Services) (response.StreamObserver, error) {
	if f.mu != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
	}
	if f.openCount != nil {
		*f.openCount++
	}
	if f.createdObs != nil {
		*f.createdObs = append(*f.createdObs, f.id)
	}
	return stubStreamObserver{id: f.id}, nil
}

type stubStreamObserver struct {
	id string
}

func (s stubStreamObserver) Observe(ctx context.Context, ev lipapi.Event) error {
	return nil
}

func (s stubStreamObserver) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	return nil
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
	require.NoError(t, featurebundle.ContributeHost(cs, featurebundle.HostContributions{
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

func TestObserversProjection_ThreeSourceOrdering(t *testing.T) {
	t.Parallel()

	cs := lipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat-plugin", lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "feat-to-1"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "feat-uo-1"},
		},
	}))
	require.NoError(t, featurebundle.ContributeHost(cs, featurebundle.HostContributions{
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "host-to-1"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "host-uo-1"},
		},
	}))
	require.NoError(t, featurebundle.ContributeBundle(cs, "candidate-extra", lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "cand-to-1"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "cand-uo-1"},
		},
	}))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}
	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)

	require.Len(t, ext.TrafficObservers, 3)
	assert.Equal(t, "feat-to-1", ext.TrafficObservers[0].(stubTrafficObs).id)
	assert.Equal(t, "host-to-1", ext.TrafficObservers[1].(stubTrafficObs).id)
	assert.Equal(t, "cand-to-1", ext.TrafficObservers[2].(stubTrafficObs).id)

	require.Len(t, ext.UsageObservers, 3)
	assert.Equal(t, "feat-uo-1", ext.UsageObservers[0].(stubUsageObs).id)
	assert.Equal(t, "host-uo-1", ext.UsageObservers[1].(stubUsageObs).id)
	assert.Equal(t, "cand-uo-1", ext.UsageObservers[2].(stubUsageObs).id)
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
	require.NoError(t, featurebundle.ContributeHost(cs, featurebundle.HostContributions{
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

func obsTestProcessConfig() *config.Config {
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	_ = config.Validate(cfg)
	return cfg
}

func obsTestCandidateConfig(t *testing.T, featurePlugins ...string) *config.Config {
	t.Helper()
	var yamlNode yaml.Node
	_ = yaml.Unmarshal([]byte("text: ok\ninput_tokens: 1\noutput_tokens: 1\n"), &yamlNode)

	features := make([]config.PluginConfig, 0, len(featurePlugins))
	for _, f := range featurePlugins {
		features = append(features, config.PluginConfig{
			ID:      f,
			Enabled: true,
		})
	}

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "stub-backend:default",
		},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features:  features,
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind:    "local-stub",
				ID:      "stub-backend",
				Enabled: true,
				Config:  yamlNode,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate candidate: %v", err)
	}
	return cfg
}

func obsTestFactoryCatalog(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestCompileGeneration_ObserversRealIntegration(t *testing.T) {
	t.Parallel()

	t.Run("feature_then_host_order", func(t *testing.T) {
		t.Parallel()
		var events []string
		var mu sync.Mutex

		reg := obsTestFactoryCatalog(t)
		err := reg.RegisterFeature("obs-feature", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
			return lipfeature.FeatureBundle{
				SchemaVersion: lipfeature.SchemaVersionV1,
				TrafficObservers: []traffic.Observer{
					stubTrafficObs{id: "feat-to", events: &events, mu: &mu},
				},
				UsageObservers: []usage.Observer{
					stubUsageObs{id: "feat-uo", events: &events, mu: &mu},
				},
			}, nil
		})
		require.NoError(t, err)

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: reg,
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "host-to", events: &events, mu: &mu},
					},
					UsageObservers: []usage.Observer{
						stubUsageObs{id: "host-uo", events: &events, mu: &mu},
					},
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t, "obs-feature")

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				obs := in.Frontends.TrafficPorts.Obs
				require.NotNil(t, obs)
				require.NoError(t, obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP}))
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"feat-to", "host-to"}, events)
	})

	t.Run("feature_process_host_candidate_extension_three_source_order", func(t *testing.T) {
		t.Parallel()
		var trafficEvents []string
		var usageEvents []string
		var mu sync.Mutex

		reg := obsTestFactoryCatalog(t)
		err := reg.RegisterFeature("three-source-feature", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
			return lipfeature.FeatureBundle{
				SchemaVersion: lipfeature.SchemaVersionV1,
				TrafficObservers: []traffic.Observer{
					stubTrafficObs{id: "feat-to", events: &trafficEvents, mu: &mu},
				},
				UsageObservers: []usage.Observer{
					stubUsageObs{id: "feat-uo", events: &usageEvents, mu: &mu},
				},
			}, nil
		})
		require.NoError(t, err)

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: reg,
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "host-to", events: &trafficEvents, mu: &mu},
					},
					UsageObservers: []usage.Observer{
						stubUsageObs{id: "host-uo", events: &usageEvents, mu: &mu},
					},
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t, "three-source-feature")

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			CandidateOpts: &BuildOptions{
				Extensions: ExtensionsOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "cand-to", events: &trafficEvents, mu: &mu},
					},
					UsageObservers: []usage.Observer{
						stubUsageObs{id: "cand-uo", events: &usageEvents, mu: &mu},
					},
				},
			},
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				obs := in.Frontends.TrafficPorts.Obs
				require.NotNil(t, obs)
				require.NoError(t, obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP}))

				uobs := in.Core.Executor.RuntimeSnapshot.UsageObserver()
				require.NotNil(t, uobs)
				require.NoError(t, uobs.OnUsage(ctx, usage.Event{}))

				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"feat-to", "host-to", "cand-to"}, trafficEvents, "traffic observers must execute in feature -> host -> candidate order")
		require.Equal(t, []string{"feat-uo", "host-uo", "cand-uo"}, usageEvents, "usage observers must execute in feature -> host -> candidate order")
	})

	t.Run("candidate_production_options_ignored", func(t *testing.T) {
		t.Parallel()
		var events []string
		var mu sync.Mutex

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: obsTestFactoryCatalog(t),
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "process-host-to", events: &events, mu: &mu},
					},
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t)

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			CandidateOpts: &BuildOptions{
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "candidate-prod-to-SHOULD-BE-IGNORED", events: &events, mu: &mu},
					},
				},
			},
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				obs := in.Frontends.TrafficPorts.Obs
				require.NotNil(t, obs)
				require.NoError(t, obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP}))
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"process-host-to"}, events, "candidate Production options must be ignored")
	})

	t.Run("candidate_extensions_raw_and_redactor_preserved", func(t *testing.T) {
		t.Parallel()
		var rawEvents, redEvents []string
		var mu sync.Mutex

		reg := obsTestFactoryCatalog(t)
		err := reg.RegisterFeature("feature-raw-red", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
			return lipfeature.FeatureBundle{
				SchemaVersion: lipfeature.SchemaVersionV1,
				RawCaptureSinks: []traffic.RawCaptureSink{
					stubRawSink{id: "feat-raw", events: &rawEvents, mu: &mu},
				},
				TrafficRedactors: []traffic.Redactor{
					stubRedactor{id: "1-feat-red", prefix: "feat", events: &redEvents, mu: &mu},
				},
			}, nil
		})
		require.NoError(t, err)

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: reg,
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t, "feature-raw-red")

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			CandidateOpts: &BuildOptions{
				Extensions: ExtensionsOptions{
					RawCaptureSinks: []traffic.RawCaptureSink{
						stubRawSink{id: "cand-raw", events: &rawEvents, mu: &mu},
					},
					TrafficRedactors: []traffic.Redactor{
						stubRedactor{id: "2-cand-red", prefix: "cand", events: &redEvents, mu: &mu},
					},
				},
			},
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				raw := in.Frontends.TrafficPorts.Raw
				require.NotNil(t, raw)
				require.NoError(t, raw.WriteRaw(ctx, traffic.LegCTP, traffic.CaptureMeta{}, []byte("data")))

				reds := in.Frontends.TrafficPorts.Red
				require.Len(t, reds, 2)
				assert.Equal(t, "1-feat-red", reds[0].ID())
				assert.Equal(t, "2-cand-red", reds[1].ID())
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"feat-raw", "cand-raw"}, rawEvents)
	})

	t.Run("spare_capacity_inputs_isolated", func(t *testing.T) {
		t.Parallel()
		rawSlice := make([]traffic.Observer, 1, 10)
		rawSlice[0] = stubTrafficObs{id: "orig-to"}

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: obsTestFactoryCatalog(t),
				Production: ProductionOptions{
					TrafficObservers: rawSlice,
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t)

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Len(t, rawSlice, 1)
		assert.Equal(t, "orig-to", rawSlice[0].(stubTrafficObs).id)
	})

	t.Run("nil_and_empty_exact", func(t *testing.T) {
		t.Parallel()
		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: obsTestFactoryCatalog(t),
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t)

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				assert.Equal(t, traffic.NoopObserver{}, in.Frontends.TrafficPorts.Obs)
				assert.Equal(t, traffic.DisabledRawCapture{}, in.Frontends.TrafficPorts.Raw)
				assert.Nil(t, in.Frontends.TrafficPorts.Red)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })
	})
}

func TestStreamObserverFactoriesProjection_ParityWithFrozenAndExpectedConfig(t *testing.T) {
	t.Parallel()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{
			stubStreamObsFactory{id: "so-1"},
		},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{
			stubStreamObsFactory{id: "so-2"},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)

	// Verify plane projects identically to lipfeature.Get on gen.Frozen
	assert.Equal(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneStreamObserverFactories), ext.StreamObserverFactories)

	// Verify counts and identities
	require.Len(t, ext.StreamObserverFactories, 2)
	assert.Equal(t, "so-1", ext.StreamObserverFactories[0].ID())
	assert.Equal(t, "so-2", ext.StreamObserverFactories[1].ID())
}

func TestStreamObserverFactoriesProjection_OverlayBranchesOmitted(t *testing.T) {
	t.Parallel()

	dst := &ExtensionsOptions{
		StreamObserverFactories: []response.StreamObserverFactory{stubStreamObsFactory{id: "dst-so"}},
	}
	src := ExtensionsOptions{
		StreamObserverFactories: []response.StreamObserverFactory{stubStreamObsFactory{id: "src-so"}},
	}

	overlayExtensions(dst, src)

	// Since StreamObserverFactories is consolidated through generated plane adapters,
	// overlayExtensions must not contain hand-coded append branches for it.
	// dst must retain its original slices without source appending.
	require.Len(t, dst.StreamObserverFactories, 1, "overlayExtensions must not append StreamObserverFactories")
	assert.Equal(t, "dst-so", dst.StreamObserverFactories[0].ID())
}

func TestStreamObserverFactoriesProjection_CandidateOverlayOrdering(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	err := reg.RegisterFeature("feature-so", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			StreamObserverFactories: []response.StreamObserverFactory{
				stubStreamObsFactory{id: "1-feat-so-1", ord: 1},
				stubStreamObsFactory{id: "2-feat-so-2", ord: 2},
			},
		}, nil
	})
	require.NoError(t, err)

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "feature-so")
	var snap *extensions.RequestRuntimeSnapshot

	bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			Extensions: ExtensionsOptions{
				StreamObserverFactories: []response.StreamObserverFactory{
					stubStreamObsFactory{id: "3-cand-so-1", ord: 3},
				},
			},
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			snap = in.Core.Executor.RuntimeSnapshot
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Close() })

	require.NotNil(t, snap)

	soFactories := snap.StreamObserverFactories()
	require.Len(t, soFactories, 3)
	assert.Equal(t, "1-feat-so-1", soFactories[0].ID())
	assert.Equal(t, "2-feat-so-2", soFactories[1].ID())
	assert.Equal(t, "3-cand-so-1", soFactories[2].ID())
}

func TestStreamObserverFactoriesProjection_ThreeSourceOrdering(t *testing.T) {
	t.Parallel()

	cs := lipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat-plugin", lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{
			stubStreamObsFactory{id: "feat-so-1"},
		},
	}))
	require.NoError(t, featurebundle.ContributeBundle(cs, "candidate-extra", lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{
			stubStreamObsFactory{id: "cand-so-1"},
		},
	}))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}
	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)

	require.Len(t, ext.StreamObserverFactories, 2)
	assert.Equal(t, "feat-so-1", ext.StreamObserverFactories[0].ID())
	assert.Equal(t, "cand-so-1", ext.StreamObserverFactories[1].ID())
}

func TestStreamObserverFactoriesProjection_LazyLifecycleAndInvocation(t *testing.T) {
	t.Parallel()

	var openCount int
	var mu sync.Mutex
	var createdObs []string

	factory := stubStreamObsFactory{
		id:         "lazy-so",
		openCount:  &openCount,
		mu:         &mu,
		createdObs: &createdObs,
	}

	reg := obsTestFactoryCatalog(t)
	err := reg.RegisterFeature("feature-lazy-so", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			StreamObserverFactories: []response.StreamObserverFactory{
				factory,
			},
		}, nil
	})
	require.NoError(t, err)

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "feature-lazy-so")
	var snap *extensions.RequestRuntimeSnapshot

	bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			snap = in.Core.Executor.RuntimeSnapshot
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Close() })

	// Prove factories are NOT eagerly invoked or opened during compilation
	mu.Lock()
	require.Equal(t, 0, openCount, "factories must not be opened during CompileGeneration")
	mu.Unlock()

	require.NotNil(t, snap)

	// Accessing snapshot does not invoke Open
	factories := snap.StreamObserverFactories()
	require.Len(t, factories, 1)
	mu.Lock()
	require.Equal(t, 0, openCount, "factories must not be opened on Snapshot() accessor reads")
	mu.Unlock()

	// Open session 1
	sess1 := &extensions.FinalStreamObservationSession{Log: testkit.DiscardLogger()}
	err = sess1.Open(context.Background(), factories, response.StreamMeta{BLegID: "bleg-1"}, response.Services{})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, 1, openCount, "factory Open should be invoked exactly once for session 1")
	mu.Unlock()

	// Open session 2 with same factory produces separate observer instance
	sess2 := &extensions.FinalStreamObservationSession{Log: testkit.DiscardLogger()}
	err = sess2.Open(context.Background(), factories, response.StreamMeta{BLegID: "bleg-2"}, response.Services{})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, 2, openCount, "factory Open should be invoked for session 2")
	require.Equal(t, []string{"lazy-so", "lazy-so"}, createdObs)
	mu.Unlock()
}

func TestStreamObserverFactoriesProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	gen, err := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{
			stubStreamObsFactory{id: "orig-so"},
		},
	})
	require.NoError(t, err)

	ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)
	require.Len(t, ext.StreamObserverFactories, 1)

	// Mutate returned slice
	ext.StreamObserverFactories[0] = stubStreamObsFactory{id: "mutated-so"}

	// Re-reading from Frozen should not reflect mutation
	frozenAgain := lipfeature.Get(gen.Frozen, lipfeature.PlaneStreamObserverFactories)
	require.Len(t, frozenAgain, 1)
	assert.Equal(t, "orig-so", frozenAgain[0].ID(), "FrozenPlaneSet backing array must be isolated from caller mutations")
}

func TestStreamObserverFactoriesProjection_ExactNilAndEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_generated_surface_projects_nil_slices", func(t *testing.T) {
		t.Parallel()
		ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, featurebundle.GeneratedMergeSurface{}, nil)
		assert.Nil(t, ext.StreamObserverFactories)
	})

	t.Run("empty_explicit_slices_project_nil_slices", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{
			SchemaVersion:           lipfeature.SchemaVersionV1,
			StreamObserverFactories: []response.StreamObserverFactory{},
		})
		require.NoError(t, err)

		ext := extensionsFromMerged(featurebundle.MergedFeatureSurface{}, gen, nil)
		assert.Nil(t, ext.StreamObserverFactories, "StreamObserverFactories must normalize explicit empty to nil")
	})
}

func TestStreamObserverFactoriesProjection_TypedNilRejection(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{
			nil,
		},
	}

	_, err := featurebundle.MergeBundlesGenerated(b)
	require.Error(t, err, "MergeBundlesGenerated must reject nil StreamObserverFactory")
}
