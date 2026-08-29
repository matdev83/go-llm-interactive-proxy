package runtimebundle

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolreactornoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stub types for hook projection testing ---

type stubSubmitHook struct {
	id    string
	order int
}

func (h stubSubmitHook) ID() string                        { return h.id }
func (h stubSubmitHook) Order() int                        { return h.order }
func (h stubSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h stubSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type stubRequestPartHook struct {
	id    string
	order int
}

func (h stubRequestPartHook) ID() string                        { return h.id }
func (h stubRequestPartHook) Order() int                        { return h.order }
func (h stubRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h stubRequestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type stubResponsePartHook struct {
	id    string
	order int
}

func (h stubResponsePartHook) ID() string                        { return h.id }
func (h stubResponsePartHook) Order() int                        { return h.order }
func (h stubResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h stubResponsePartHook) HandleEvent(context.Context, *lipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type stubToolReactor struct {
	id    string
	order int
}

func (r stubToolReactor) ID() string { return r.id }
func (r stubToolReactor) Order() int { return r.order }
func (r stubToolReactor) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

type stubLifecycle struct {
	id     string
	starts *int
}

func (l stubLifecycle) Start(context.Context) error {
	if l.starts != nil {
		*l.starts++
	}
	return nil
}
func (stubLifecycle) Stop(context.Context) error { return nil }

func TestHooksConfigFromGenerated_ParityWithFrozenAndExpectedConfig(t *testing.T) {
	t.Parallel()

	b1 := testkit.FeatureBundle(t, "b1", func(cs *lipfeature.ContributionSet) error {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b1", []sdkhooks.SubmitHook{
			stubSubmitHook{id: "sub-1", order: 10},
			stubSubmitHook{id: "sub-2", order: 5},
		}); err != nil {
			return err
		}
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, "b1", []sdkhooks.RequestPartHook{
			stubRequestPartHook{id: "req-1", order: 1},
		}); err != nil {
			return err
		}
		if err := lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, "b1", []sdkhooks.ResponsePartHook{
			stubResponsePartHook{id: "resp-1", order: 20},
		}); err != nil {
			return err
		}
		return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "b1", []sdkhooks.ToolReactor{
			stubToolReactor{id: "reactor-1", order: 15},
		})
	}, []lipplugin.Lifecycle{
		stubLifecycle{id: "life-1"},
	})

	b2 := testkit.FeatureBundle(t, "b2", func(cs *lipfeature.ContributionSet) error {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b2", []sdkhooks.SubmitHook{
			stubSubmitHook{id: "sub-3", order: 1},
		}); err != nil {
			return err
		}
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, "b2", []sdkhooks.RequestPartHook{
			stubRequestPartHook{id: "req-2", order: 0},
		}); err != nil {
			return err
		}
		if err := lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, "b2", []sdkhooks.ResponsePartHook{
			stubResponsePartHook{id: "resp-2", order: 5},
		}); err != nil {
			return err
		}
		return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "b2", []sdkhooks.ToolReactor{
			stubToolReactor{id: "reactor-2", order: 2},
		})
	}, []lipplugin.Lifecycle{
		stubLifecycle{id: "life-2"},
	})

	policies := []sdkhooks.ToolReactorErrorPolicy{
		sdkhooks.ToolReactorErrorPolicyUnspecified,
		sdkhooks.ToolReactorErrorsFailOpen,
		sdkhooks.ToolReactorErrorsFailClosed,
		sdkhooks.ToolReactorErrorsSwallowEvent,
	}

	genMerged, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	for _, pol := range policies {
		t.Run(fmt.Sprintf("policy_%d", pol), func(t *testing.T) {
			expectedCfg := hooks.Config{
				SubmitHooks: []sdkhooks.SubmitHook{
					stubSubmitHook{id: "sub-1", order: 10},
					stubSubmitHook{id: "sub-2", order: 5},
					stubSubmitHook{id: "sub-3", order: 1},
				},
				RequestPartHooks: []sdkhooks.RequestPartHook{
					stubRequestPartHook{id: "req-1", order: 1},
					stubRequestPartHook{id: "req-2", order: 0},
				},
				ResponsePartHooks: []sdkhooks.ResponsePartHook{
					stubResponsePartHook{id: "resp-1", order: 20},
					stubResponsePartHook{id: "resp-2", order: 5},
				},
				ToolReactors: []sdkhooks.ToolReactor{
					stubToolReactor{id: "reactor-1", order: 15},
					stubToolReactor{id: "reactor-2", order: 2},
				},
				ToolReactorErrorPolicy: pol,
			}

			derivedCfg := HooksConfigFromGenerated(genMerged, pol)
			frozenCfg := HooksConfigFromFrozen(genMerged.Frozen, pol)

			assert.True(t, reflect.DeepEqual(expectedCfg, derivedCfg), "HooksConfigFromGenerated must match expected hooks config")
			assert.True(t, reflect.DeepEqual(derivedCfg, frozenCfg), "HooksConfigFromFrozen must equal HooksConfigFromGenerated")

			// Check individual fields
			assert.Equal(t, expectedCfg.SubmitHooks, derivedCfg.SubmitHooks)
			assert.Equal(t, expectedCfg.RequestPartHooks, derivedCfg.RequestPartHooks)
			assert.Equal(t, expectedCfg.ResponsePartHooks, derivedCfg.ResponsePartHooks)
			assert.Equal(t, expectedCfg.ToolReactors, derivedCfg.ToolReactors)
			assert.Equal(t, pol, derivedCfg.ToolReactorErrorPolicy)

			// Verify bus creation and hook chain lengths
			expectedBus := hooks.New(expectedCfg)
			derivedBus := hooks.New(derivedCfg)

			es, erq, ers, et := expectedBus.HookChainLengths()
			ds, drq, drs, dt := derivedBus.HookChainLengths()

			assert.Equal(t, es, ds)
			assert.Equal(t, erq, drq)
			assert.Equal(t, ers, drs)
			assert.Equal(t, et, dt)
			assert.Equal(t, 3, ds)
			assert.Equal(t, 2, drq)
			assert.Equal(t, 2, drs)
			assert.Equal(t, 2, dt)
		})
	}
}

func TestHooksConfigFromGenerated_EmptySurfaceYieldsNilOrEmptySlices(t *testing.T) {
	t.Parallel()

	emptyGen := featurebundle.GeneratedMergeSurface{
		Frozen: lipfeature.NewContributionSet().Freeze(),
	}

	cfg := HooksConfigFromGenerated(emptyGen, sdkhooks.ToolReactorErrorsFailOpen)
	assert.Empty(t, cfg.SubmitHooks)
	assert.Empty(t, cfg.RequestPartHooks)
	assert.Empty(t, cfg.ResponsePartHooks)
	assert.Empty(t, cfg.ToolReactors)
	assert.Equal(t, sdkhooks.ToolReactorErrorsFailOpen, cfg.ToolReactorErrorPolicy)

	bus := hooks.New(cfg)
	ns, nrq, nrs, nt := bus.HookChainLengths()
	assert.Equal(t, 0, ns)
	assert.Equal(t, 0, nrq)
	assert.Equal(t, 0, nrs)
	assert.Equal(t, 0, nt)
}

func TestBuildFeatureHooks_DerivesFromGeneratedMergeSurface(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	submitFacID := "test-fac-submit-" + strings.ReplaceAll(t.Name(), "/", "-")
	toolFacID := "test-fac-tool-" + strings.ReplaceAll(t.Name(), "/", "-")

	var startsSubmit, startsTool int

	require.NoError(t, reg.RegisterFeature(submitFacID, func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		cfg, err := submitnoop.DecodeHookConfig(n)
		if err != nil {
			return lipfeature.FeatureBundle{}, err
		}
		return testkit.FeatureBundle(t, submitFacID, func(cs *lipfeature.ContributionSet) error {
			return lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, submitFacID, []sdkhooks.SubmitHook{submitnoop.NewSubmitHookWithConfig(cfg)})
		}, []lipplugin.Lifecycle{stubLifecycle{id: "submit-life", starts: &startsSubmit}}), nil
	}))

	require.NoError(t, reg.RegisterFeature(toolFacID, func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, toolFacID, func(cs *lipfeature.ContributionSet) error {
			return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, toolFacID, []sdkhooks.ToolReactor{toolreactornoop.NewToolReactor()})
		}, []lipplugin.Lifecycle{stubLifecycle{id: "tool-life", starts: &startsTool}}), nil
	}))

	var cfgNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &cfgNode))

	// Both plugins enabled
	hookCfg, lifecycles, err := BuildFeatureHooks(reg, []lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "inst-submit", FactoryKind: submitFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: lipsdk.PluginKindFeature, ID: "inst-tool", FactoryKind: toolFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.NoError(t, err)
	require.Len(t, hookCfg.SubmitHooks, 1)
	require.Len(t, hookCfg.ToolReactors, 1)
	require.Empty(t, hookCfg.RequestPartHooks)
	require.Empty(t, hookCfg.ResponsePartHooks)
	require.Equal(t, sdkhooks.ToolReactorErrorPolicyUnspecified, hookCfg.ToolReactorErrorPolicy)
	require.Len(t, lifecycles, 2)
	assert.Equal(t, 0, startsSubmit, "BuildFeatureHooks must not start lifecycles")
	assert.Equal(t, 0, startsTool, "BuildFeatureHooks must not start lifecycles")

	// Disabled plugin contributes nothing
	hookCfgDisabled, lifecyclesDisabled, err := BuildFeatureHooks(reg, []lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "inst-submit", FactoryKind: submitFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: lipsdk.PluginKindFeature, ID: "inst-tool", FactoryKind: toolFacID, Enabled: false, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.NoError(t, err)
	require.Len(t, hookCfgDisabled.SubmitHooks, 1)
	require.Empty(t, hookCfgDisabled.ToolReactors, "disabled tool plugin must not contribute to hook config")
	require.Len(t, lifecyclesDisabled, 1, "disabled tool plugin must not contribute lifecycles")
}

func TestConfigOwnedToolReactorErrorPolicy_FeatureContributionsCannotSetPolicy(t *testing.T) {
	t.Parallel()

	// FeatureBundle has no ToolReactorErrorPolicy field (verified via reflection)
	fbType := reflect.TypeFor[lipfeature.FeatureBundle]()
	_, hasField := fbType.FieldByName("ToolReactorErrorPolicy")
	assert.False(t, hasField, "FeatureBundle must NOT have a ToolReactorErrorPolicy field")

	// MergedFeatureSurface created via MergeBundlesGenerated does NOT set ToolReactorErrorPolicy
	gen, err := featurebundle.MergeBundlesGenerated(testkit.FeatureBundle(t, "reactor-1", func(cs *lipfeature.ContributionSet) error {
		return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "reactor-1", []sdkhooks.ToolReactor{
			stubToolReactor{id: "reactor-1", order: 1},
		})
	}, nil))
	require.NoError(t, err)

	// Policy is purely injected at projection time from host/config
	cfgDefault := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
	assert.Equal(t, sdkhooks.ToolReactorErrorPolicyUnspecified, cfgDefault.ToolReactorErrorPolicy)

	cfgFailClosed := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorsFailClosed)
	assert.Equal(t, sdkhooks.ToolReactorErrorsFailClosed, cfgFailClosed.ToolReactorErrorPolicy)

	cfgSwallow := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorsSwallowEvent)
	assert.Equal(t, sdkhooks.ToolReactorErrorsSwallowEvent, cfgSwallow.ToolReactorErrorPolicy)
}
