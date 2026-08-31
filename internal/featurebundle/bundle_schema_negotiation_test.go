package featurebundle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// --- Minimal stubs for bundle schema negotiation testing ---

type schemaTestSubmitHook struct{ tag string }

func (h schemaTestSubmitHook) ID() string                      { return h.tag }
func (schemaTestSubmitHook) Order() int                        { return 0 }
func (schemaTestSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (schemaTestSubmitHook) Handle(_ context.Context, _ *lipapi.Call, _ *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type schemaTestLifecycle struct{ tag string }

func (schemaTestLifecycle) Start(_ context.Context) error { return nil }
func (schemaTestLifecycle) Stop(_ context.Context) error  { return nil }

type schemaTestOpener struct{ tag string }

func (h schemaTestOpener) ID() string { return h.tag }
func (schemaTestOpener) Open(_ context.Context, _ session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type schemaTestTrafficObserver struct{ tag string }

func (schemaTestTrafficObserver) OnObservation(_ context.Context, _ traffic.Observation) error {
	return nil
}

type schemaTestUsageObserver struct{ tag string }

func (schemaTestUsageObserver) OnUsage(_ context.Context, _ usage.Event) error {
	return nil
}

type fakeFeatureBundleRegistry struct {
	buildFn func(factoryKey string, n yaml.Node) (lipfeature.FeatureBundle, error)
}

func (f fakeFeatureBundleRegistry) BuildFeatureBundle(factoryKey string, n yaml.Node) (lipfeature.FeatureBundle, error) {
	if f.buildFn != nil {
		return f.buildFn(factoryKey, n)
	}
	return lipfeature.FeatureBundle{}, nil
}

func makeSchemaTestPlaneSet(t *testing.T, tag string) lipfeature.FrozenPlaneSet {
	t.Helper()
	cs := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, tag, []sdkhooks.SubmitHook{schemaTestSubmitHook{tag: tag}}))
	return cs.Freeze()
}

// TestFeatureBundle_SchemaNegotiation_EmptyBundlesAccepted characterizes Requirement 2.3:
// When an empty bundle declares either the compatibility zero version or the supported
// schema version (SchemaVersionV1 = 1), the feature composition system accepts it.
func TestFeatureBundle_SchemaNegotiation_EmptyBundlesAccepted(t *testing.T) {
	t.Parallel()

	emptyFrozen := lipfeature.NewContributionSet().Freeze()

	// 1. Empty bundle schema 0 direct contribution
	cs0 := lipfeature.NewContributionSet()
	emptyBundle0 := lipfeature.FeatureBundle{SchemaVersion: 0}
	err0 := ContributeBundle(cs0, "empty-plugin-0", emptyBundle0)
	require.NoError(t, err0)
	require.Equal(t, emptyFrozen, cs0.Freeze())
	require.Nil(t, lipfeature.Get(cs0.Freeze(), lipfeature.PlaneSubmitHooks))

	// 2. Empty bundle V1 direct contribution
	csV1 := lipfeature.NewContributionSet()
	emptyBundleV1 := lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}
	errV1 := ContributeBundle(csV1, "empty-plugin-v1", emptyBundleV1)
	require.NoError(t, errV1)
	require.Equal(t, emptyFrozen, csV1.Freeze())
	require.Nil(t, lipfeature.Get(csV1.Freeze(), lipfeature.PlaneSubmitHooks))

	// 3. MergeBundlesGenerated with empty schema 0 and V1 bundles
	gen, err := MergeBundlesGenerated(emptyBundle0, emptyBundleV1)
	require.NoError(t, err)
	require.Equal(t, emptyFrozen, gen.Frozen)
	require.Empty(t, gen.Lifecycles)

	// 4. Registry and Host merge with empty bundles
	reg := fakeFeatureBundleRegistry{
		buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
			if factoryKey == "empty-v0" {
				return emptyBundle0, nil
			}
			return emptyBundleV1, nil
		},
	}
	regs := []lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "f0", FactoryKind: "empty-v0", Enabled: true},
		{Kind: lipsdk.PluginKindFeature, ID: "f1", FactoryKind: "empty-v1", Enabled: true},
	}
	genReg, errReg := MergeFeatureSurfaceGenerated(reg, regs)
	require.NoError(t, errReg)
	require.Equal(t, emptyFrozen, genReg.Frozen)
	require.Empty(t, genReg.Lifecycles)

	gHost, errHost := MergeFeatureSurfacesWithHost(reg, regs, HostContributions{}, emptyBundle0, emptyBundleV1)
	require.NoError(t, errHost)
	require.Equal(t, emptyFrozen, gHost.Frozen)
	require.Empty(t, gHost.Lifecycles)
}

// TestFeatureBundle_SchemaNegotiation_ValidV1NonEmptyAccepted characterizes Requirement 2.1:
// When a non-empty feature-plane bundle declares the supported schema version (V1),
// the feature composition system accepts it subject to normal plane validation.
func TestFeatureBundle_SchemaNegotiation_ValidV1NonEmptyAccepted(t *testing.T) {
	t.Parallel()

	planes := makeSchemaTestPlaneSet(t, "hook-v1")
	bundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		PlaneSet:      planes,
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-v1"}},
	}

	// 1. Direct ContributeBundle
	cs := lipfeature.NewContributionSet()
	err := ContributeBundle(cs, "plugin-v1-valid", bundle)
	require.NoError(t, err)
	hooks := lipfeature.Get(cs.Freeze(), lipfeature.PlaneSubmitHooks)
	require.Len(t, hooks, 1)
	assert.Equal(t, "hook-v1", hooks[0].ID())

	// 2. MergeBundlesGenerated
	gen, err := MergeBundlesGenerated(bundle)
	require.NoError(t, err)
	genHooks := lipfeature.Get(gen.Frozen, lipfeature.PlaneSubmitHooks)
	require.Len(t, genHooks, 1)
	assert.Equal(t, "hook-v1", genHooks[0].ID())
	require.Len(t, gen.Lifecycles, 1)
	lc, ok := gen.Lifecycles[0].(schemaTestLifecycle)
	require.True(t, ok)
	assert.Equal(t, "life-v1", lc.tag)
}

// TestFeatureBundle_SchemaNegotiation_ValidV1LifecycleOnlyAccepted characterizes Requirement 2.2:
// When a lifecycle-only bundle declares the supported schema version (V1), the feature
// composition system preserves its lifecycles in registration order.
func TestFeatureBundle_SchemaNegotiation_ValidV1LifecycleOnlyAccepted(t *testing.T) {
	t.Parallel()

	emptyFrozen := lipfeature.NewContributionSet().Freeze()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "lc-first"}},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		Lifecycles:    []lipplugin.Lifecycle{schemaTestLifecycle{tag: "lc-second"}},
	}

	// 1. Direct ContributeBundle (no planes contributed)
	cs := lipfeature.NewContributionSet()
	err := ContributeBundle(cs, "lc-plugin", b1)
	require.NoError(t, err)
	require.Equal(t, emptyFrozen, cs.Freeze())

	// 2. MergeBundlesGenerated preserves lifecycle ordering without plane publication
	gen, err := MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)
	require.Equal(t, emptyFrozen, gen.Frozen)
	require.Len(t, gen.Lifecycles, 2)

	lc1, ok1 := gen.Lifecycles[0].(schemaTestLifecycle)
	lc2, ok2 := gen.Lifecycles[1].(schemaTestLifecycle)
	require.True(t, ok1)
	require.True(t, ok2)
	assert.Equal(t, "lc-first", lc1.tag)
	assert.Equal(t, "lc-second", lc2.tag)
}

// TestFeatureBundle_SchemaNegotiation_PreserveBundleOrderAndLifecycles characterizes Requirement 4.1, 4.2:
// Preserves registration ordering across feature bundles, host contributions, candidate extra
// bundles, and the lifecycle side-channel.
func TestFeatureBundle_SchemaNegotiation_PreserveBundleOrderAndLifecycles(t *testing.T) {
	t.Parallel()

	csA := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(csA, lipfeature.PlaneSubmitHooks, "feat-A", []sdkhooks.SubmitHook{schemaTestSubmitHook{tag: "hook-A"}}))
	require.NoError(t, lipfeature.Contribute(csA, lipfeature.PlaneSessionOpeners, "feat-A", []session.Opener{schemaTestOpener{tag: "opener-A"}}))
	bA := lipfeature.BundleFromPlanes(csA.Freeze(), []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-A"}})

	csB := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(csB, lipfeature.PlaneSubmitHooks, "feat-B", []sdkhooks.SubmitHook{schemaTestSubmitHook{tag: "hook-B"}}))
	bB := lipfeature.BundleFromPlanes(csB.Freeze(), []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-B"}})

	csCand := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(csCand, lipfeature.PlaneSubmitHooks, "cand", []sdkhooks.SubmitHook{schemaTestSubmitHook{tag: "hook-cand"}}))
	bCand := lipfeature.BundleFromPlanes(csCand.Freeze(), []lipplugin.Lifecycle{schemaTestLifecycle{tag: "life-cand"}})

	reg := fakeFeatureBundleRegistry{
		buildFn: func(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
			switch factoryKey {
			case "key-A":
				return bA, nil
			case "key-B":
				return bB, nil
			default:
				return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}, nil
			}
		},
	}

	regs := []lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "feat-A", FactoryKind: "key-A", Enabled: true},
		{Kind: lipsdk.PluginKindFeature, ID: "feat-B", FactoryKind: "key-B", Enabled: true},
	}

	host := HostContributions{
		TrafficObservers: []traffic.Observer{schemaTestTrafficObserver{tag: "host-to"}},
		UsageObservers:   []usage.Observer{schemaTestUsageObserver{tag: "host-uo"}},
	}

	gen, err := MergeFeatureSurfacesWithHost(reg, regs, host, bCand)
	require.NoError(t, err)

	// Check hook order: hook-A, hook-B, hook-cand
	hooks := lipfeature.Get(gen.Frozen, lipfeature.PlaneSubmitHooks)
	require.Len(t, hooks, 3)
	assert.Equal(t, "hook-A", hooks[0].ID())
	assert.Equal(t, "hook-B", hooks[1].ID())
	assert.Equal(t, "hook-cand", hooks[2].ID())

	// Check opener
	openers := lipfeature.Get(gen.Frozen, lipfeature.PlaneSessionOpeners)
	require.Len(t, openers, 1)
	assert.Equal(t, "opener-A", openers[0].ID())

	// Check host observer presence
	to := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	require.Len(t, to, 1)
	uo := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	require.Len(t, uo, 1)

	// Check lifecycle ordering: life-A, life-B, life-cand
	require.Len(t, gen.Lifecycles, 3)
	lifeA, ok := gen.Lifecycles[0].(schemaTestLifecycle)
	require.True(t, ok)
	assert.Equal(t, "life-A", lifeA.tag)
	lifeB, ok := gen.Lifecycles[1].(schemaTestLifecycle)
	require.True(t, ok)
	assert.Equal(t, "life-B", lifeB.tag)
	lifeCand, ok := gen.Lifecycles[2].(schemaTestLifecycle)
	require.True(t, ok)
	assert.Equal(t, "life-cand", lifeCand.tag)
}
