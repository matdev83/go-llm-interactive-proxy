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

func TestHooksConfigProjection_IndependentCharacterization(t *testing.T) {
	t.Parallel()

	t.Run("PopulatedAndRegistrationOrder", func(t *testing.T) {
		t.Parallel()
		b1 := testkit.FeatureBundle(t, "b1", func(cs *lipfeature.ContributionSet) error {
			if err := lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b1", []sdkhooks.SubmitHook{
				stubSubmitHook{id: "sub-1a", order: 50},
				stubSubmitHook{id: "sub-1b", order: 10},
			}); err != nil {
				return err
			}
			if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, "b1", []sdkhooks.RequestPartHook{
				stubRequestPartHook{id: "req-1a", order: 30},
			}); err != nil {
				return err
			}
			if err := lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, "b1", []sdkhooks.ResponsePartHook{
				stubResponsePartHook{id: "resp-1a", order: 40},
			}); err != nil {
				return err
			}
			return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "b1", []sdkhooks.ToolReactor{
				stubToolReactor{id: "reactor-1a", order: 20},
			})
		}, nil)

		b2 := testkit.FeatureBundle(t, "b2", func(cs *lipfeature.ContributionSet) error {
			if err := lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b2", []sdkhooks.SubmitHook{
				stubSubmitHook{id: "sub-2a", order: 5},
			}); err != nil {
				return err
			}
			if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, "b2", []sdkhooks.RequestPartHook{
				stubRequestPartHook{id: "req-2a", order: 10},
			}); err != nil {
				return err
			}
			if err := lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, "b2", []sdkhooks.ResponsePartHook{
				stubResponsePartHook{id: "resp-2a", order: 5},
			}); err != nil {
				return err
			}
			return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "b2", []sdkhooks.ToolReactor{
				stubToolReactor{id: "reactor-2a", order: 1},
			})
		}, nil)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
		require.NoError(t, err)

		cfgGen := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorsFailOpen)
		cfgFrozen := HooksConfigFromFrozen(gen.Frozen, sdkhooks.ToolReactorErrorsFailOpen)

		require.Len(t, cfgGen.SubmitHooks, 3)
		assert.Equal(t, []string{"sub-1a", "sub-1b", "sub-2a"}, []string{cfgGen.SubmitHooks[0].ID(), cfgGen.SubmitHooks[1].ID(), cfgGen.SubmitHooks[2].ID()})

		require.Len(t, cfgGen.RequestPartHooks, 2)
		assert.Equal(t, []string{"req-1a", "req-2a"}, []string{cfgGen.RequestPartHooks[0].ID(), cfgGen.RequestPartHooks[1].ID()})

		require.Len(t, cfgGen.ResponsePartHooks, 2)
		assert.Equal(t, []string{"resp-1a", "resp-2a"}, []string{cfgGen.ResponsePartHooks[0].ID(), cfgGen.ResponsePartHooks[1].ID()})

		require.Len(t, cfgGen.ToolReactors, 2)
		assert.Equal(t, []string{"reactor-1a", "reactor-2a"}, []string{cfgGen.ToolReactors[0].ID(), cfgGen.ToolReactors[1].ID()})

		assert.Equal(t, cfgGen, cfgFrozen)
	})

	t.Run("AbsentPlanes", func(t *testing.T) {
		t.Parallel()
		b := testkit.FeatureBundle(t, "b-partial", func(cs *lipfeature.ContributionSet) error {
			return lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b-partial", []sdkhooks.SubmitHook{
				stubSubmitHook{id: "sub-only", order: 1},
			})
		}, nil)

		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)

		cfg := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorsFailClosed)
		require.Len(t, cfg.SubmitHooks, 1)
		assert.Equal(t, "sub-only", cfg.SubmitHooks[0].ID())
		assert.Nil(t, cfg.RequestPartHooks, "absent RequestPartHooks must project to nil")
		assert.Nil(t, cfg.ResponsePartHooks, "absent ResponsePartHooks must project to nil")
		assert.Nil(t, cfg.ToolReactors, "absent ToolReactors must project to nil")
		assert.Equal(t, sdkhooks.ToolReactorErrorsFailClosed, cfg.ToolReactorErrorPolicy)
	})

	t.Run("NilVsExplicitEmpty", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			contribute func(cs *lipfeature.ContributionSet, isNil bool) error
			extractGen func(cfg hooks.Config) any
		}{
			{
				name: "SubmitHooks",
				contribute: func(cs *lipfeature.ContributionSet, isNil bool) error {
					var s []sdkhooks.SubmitHook
					if !isNil {
						s = make([]sdkhooks.SubmitHook, 0)
					}
					return lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b-nil-empty-submit", s)
				},
				extractGen: func(cfg hooks.Config) any { return cfg.SubmitHooks },
			},
			{
				name: "RequestPartHooks",
				contribute: func(cs *lipfeature.ContributionSet, isNil bool) error {
					var s []sdkhooks.RequestPartHook
					if !isNil {
						s = make([]sdkhooks.RequestPartHook, 0)
					}
					return lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, "b-nil-empty-req", s)
				},
				extractGen: func(cfg hooks.Config) any { return cfg.RequestPartHooks },
			},
			{
				name: "ResponsePartHooks",
				contribute: func(cs *lipfeature.ContributionSet, isNil bool) error {
					var s []sdkhooks.ResponsePartHook
					if !isNil {
						s = make([]sdkhooks.ResponsePartHook, 0)
					}
					return lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, "b-nil-empty-resp", s)
				},
				extractGen: func(cfg hooks.Config) any { return cfg.ResponsePartHooks },
			},
			{
				name: "ToolReactors",
				contribute: func(cs *lipfeature.ContributionSet, isNil bool) error {
					var s []sdkhooks.ToolReactor
					if !isNil {
						s = make([]sdkhooks.ToolReactor, 0)
					}
					return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "b-nil-empty-tool", s)
				},
				extractGen: func(cfg hooks.Config) any { return cfg.ToolReactors },
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				// 1. Nil contribution must project to nil slice in both Generated and Frozen paths
				bNil := testkit.FeatureBundle(t, "b-nil", func(cs *lipfeature.ContributionSet) error {
					return tc.contribute(cs, true)
				}, nil)
				genNil, err := featurebundle.MergeBundlesGenerated(bNil)
				require.NoError(t, err)

				cfgGenNil := HooksConfigFromGenerated(genNil, sdkhooks.ToolReactorErrorPolicyUnspecified)
				assert.Nil(t, tc.extractGen(cfgGenNil), "nil contribution must project to nil slice via generated")

				cfgFrozenNil := HooksConfigFromFrozen(genNil.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified)
				assert.Nil(t, tc.extractGen(cfgFrozenNil), "nil contribution must project to nil slice via frozen")

				// 2. Explicit empty contribution must project to non-nil empty slice in both Generated and Frozen paths
				bEmpty := testkit.FeatureBundle(t, "b-empty", func(cs *lipfeature.ContributionSet) error {
					return tc.contribute(cs, false)
				}, nil)
				genEmpty, err := featurebundle.MergeBundlesGenerated(bEmpty)
				require.NoError(t, err)

				cfgGenEmpty := HooksConfigFromGenerated(genEmpty, sdkhooks.ToolReactorErrorPolicyUnspecified)
				valGen := tc.extractGen(cfgGenEmpty)
				assert.NotNil(t, valGen, "explicit empty slice must project to non-nil empty slice via generated")
				assert.Empty(t, valGen)

				cfgFrozenEmpty := HooksConfigFromFrozen(genEmpty.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified)
				valFrozen := tc.extractGen(cfgFrozenEmpty)
				assert.NotNil(t, valFrozen, "explicit empty slice must project to non-nil empty slice via frozen")
				assert.Empty(t, valFrozen)
			})
		}
	})

	t.Run("DefensiveCopyAndIsolation", func(t *testing.T) {
		t.Parallel()

		t.Run("SubmitHooks", func(t *testing.T) {
			t.Parallel()
			inputHooks := []sdkhooks.SubmitHook{
				stubSubmitHook{id: "orig-1", order: 10},
				stubSubmitHook{id: "orig-2", order: 20},
			}
			b := testkit.FeatureBundle(t, "b-def-submit", func(cs *lipfeature.ContributionSet) error {
				return lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, "b-def-submit", inputHooks)
			}, nil)
			gen, err := featurebundle.MergeBundlesGenerated(b)
			require.NoError(t, err)

			// 1. Mutate input slice after bundle contribution
			inputHooks[0] = stubSubmitHook{id: "mutated-input", order: 999}

			cfg1 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg1.SubmitHooks, 2)
			assert.Equal(t, "orig-1", cfg1.SubmitHooks[0].ID())

			// 2. Mutate first projection's slice
			cfg1.SubmitHooks[0] = stubSubmitHook{id: "mutated-cfg", order: 777}
			cfg1.SubmitHooks = append(cfg1.SubmitHooks, stubSubmitHook{id: "appended", order: 888})

			// 3. Second projection from Generated must remain uncorrupted
			cfg2 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg2.SubmitHooks, 2)
			assert.Equal(t, "orig-1", cfg2.SubmitHooks[0].ID())
			assert.Equal(t, "orig-2", cfg2.SubmitHooks[1].ID())

			// 4. Projection from Frozen must also remain uncorrupted
			cfgFrozen := HooksConfigFromFrozen(gen.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfgFrozen.SubmitHooks, 2)
			assert.Equal(t, "orig-1", cfgFrozen.SubmitHooks[0].ID())
			assert.Equal(t, "orig-2", cfgFrozen.SubmitHooks[1].ID())
		})

		t.Run("RequestPartHooks", func(t *testing.T) {
			t.Parallel()
			inputHooks := []sdkhooks.RequestPartHook{
				stubRequestPartHook{id: "orig-1", order: 10},
				stubRequestPartHook{id: "orig-2", order: 20},
			}
			b := testkit.FeatureBundle(t, "b-def-req", func(cs *lipfeature.ContributionSet) error {
				return lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, "b-def-req", inputHooks)
			}, nil)
			gen, err := featurebundle.MergeBundlesGenerated(b)
			require.NoError(t, err)

			inputHooks[0] = stubRequestPartHook{id: "mutated-input", order: 999}

			cfg1 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg1.RequestPartHooks, 2)
			assert.Equal(t, "orig-1", cfg1.RequestPartHooks[0].ID())

			cfg1.RequestPartHooks[0] = stubRequestPartHook{id: "mutated-cfg", order: 777}
			cfg1.RequestPartHooks = append(cfg1.RequestPartHooks, stubRequestPartHook{id: "appended", order: 888})

			cfg2 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg2.RequestPartHooks, 2)
			assert.Equal(t, "orig-1", cfg2.RequestPartHooks[0].ID())
			assert.Equal(t, "orig-2", cfg2.RequestPartHooks[1].ID())

			cfgFrozen := HooksConfigFromFrozen(gen.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfgFrozen.RequestPartHooks, 2)
			assert.Equal(t, "orig-1", cfgFrozen.RequestPartHooks[0].ID())
			assert.Equal(t, "orig-2", cfgFrozen.RequestPartHooks[1].ID())
		})

		t.Run("ResponsePartHooks", func(t *testing.T) {
			t.Parallel()
			inputHooks := []sdkhooks.ResponsePartHook{
				stubResponsePartHook{id: "orig-1", order: 10},
				stubResponsePartHook{id: "orig-2", order: 20},
			}
			b := testkit.FeatureBundle(t, "b-def-resp", func(cs *lipfeature.ContributionSet) error {
				return lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, "b-def-resp", inputHooks)
			}, nil)
			gen, err := featurebundle.MergeBundlesGenerated(b)
			require.NoError(t, err)

			inputHooks[0] = stubResponsePartHook{id: "mutated-input", order: 999}

			cfg1 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg1.ResponsePartHooks, 2)
			assert.Equal(t, "orig-1", cfg1.ResponsePartHooks[0].ID())

			cfg1.ResponsePartHooks[0] = stubResponsePartHook{id: "mutated-cfg", order: 777}
			cfg1.ResponsePartHooks = append(cfg1.ResponsePartHooks, stubResponsePartHook{id: "appended", order: 888})

			cfg2 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg2.ResponsePartHooks, 2)
			assert.Equal(t, "orig-1", cfg2.ResponsePartHooks[0].ID())
			assert.Equal(t, "orig-2", cfg2.ResponsePartHooks[1].ID())

			cfgFrozen := HooksConfigFromFrozen(gen.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfgFrozen.ResponsePartHooks, 2)
			assert.Equal(t, "orig-1", cfgFrozen.ResponsePartHooks[0].ID())
			assert.Equal(t, "orig-2", cfgFrozen.ResponsePartHooks[1].ID())
		})

		t.Run("ToolReactors", func(t *testing.T) {
			t.Parallel()
			inputHooks := []sdkhooks.ToolReactor{
				stubToolReactor{id: "orig-1", order: 10},
				stubToolReactor{id: "orig-2", order: 20},
			}
			b := testkit.FeatureBundle(t, "b-def-tool", func(cs *lipfeature.ContributionSet) error {
				return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "b-def-tool", inputHooks)
			}, nil)
			gen, err := featurebundle.MergeBundlesGenerated(b)
			require.NoError(t, err)

			inputHooks[0] = stubToolReactor{id: "mutated-input", order: 999}

			cfg1 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg1.ToolReactors, 2)
			assert.Equal(t, "orig-1", cfg1.ToolReactors[0].ID())

			cfg1.ToolReactors[0] = stubToolReactor{id: "mutated-cfg", order: 777}
			cfg1.ToolReactors = append(cfg1.ToolReactors, stubToolReactor{id: "appended", order: 888})

			cfg2 := HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfg2.ToolReactors, 2)
			assert.Equal(t, "orig-1", cfg2.ToolReactors[0].ID())
			assert.Equal(t, "orig-2", cfg2.ToolReactors[1].ID())

			cfgFrozen := HooksConfigFromFrozen(gen.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified)
			require.Len(t, cfgFrozen.ToolReactors, 2)
			assert.Equal(t, "orig-1", cfgFrozen.ToolReactors[0].ID())
			assert.Equal(t, "orig-2", cfgFrozen.ToolReactors[1].ID())
		})
	})

	t.Run("EachHostToolReactorErrorPolicy", func(t *testing.T) {
		t.Parallel()
		policies := []struct {
			name   string
			policy sdkhooks.ToolReactorErrorPolicy
		}{
			{name: "Unspecified", policy: sdkhooks.ToolReactorErrorPolicyUnspecified},
			{name: "FailOpen", policy: sdkhooks.ToolReactorErrorsFailOpen},
			{name: "FailClosed", policy: sdkhooks.ToolReactorErrorsFailClosed},
			{name: "SwallowEvent", policy: sdkhooks.ToolReactorErrorsSwallowEvent},
		}

		gen, err := featurebundle.MergeBundlesGenerated(testkit.FeatureBundle(t, "b-pol", func(cs *lipfeature.ContributionSet) error {
			return lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, "b-pol", []sdkhooks.ToolReactor{
				stubToolReactor{id: "tr-1", order: 1},
			})
		}, nil))
		require.NoError(t, err)

		for _, tc := range policies {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				cfgGen := HooksConfigFromGenerated(gen, tc.policy)
				cfgFrozen := HooksConfigFromFrozen(gen.Frozen, tc.policy)

				assert.Equal(t, tc.policy, cfgGen.ToolReactorErrorPolicy)
				assert.Equal(t, tc.policy, cfgFrozen.ToolReactorErrorPolicy)
			})
		}
	})
}
