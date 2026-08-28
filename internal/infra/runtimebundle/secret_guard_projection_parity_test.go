package runtimebundle

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdksg "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Secret Guard Parity Tests ---

type parityTestSGGuard struct {
	id  string
	ord int
}

func (g parityTestSGGuard) ID() string                   { return g.id }
func (g parityTestSGGuard) Order() int                   { return g.ord }
func (parityTestSGGuard) FailureMode() sdksg.FailureMode { return sdksg.FailClosed }
func (parityTestSGGuard) Evaluate(context.Context, *lipapi.Call, sdksg.Meta, sdksg.Services) (sdksg.Decision, error) {
	return sdksg.Decision{Outcome: sdksg.OutcomePass}, nil
}

type parityTestCountingEnv struct {
	vals      map[string]string
	lookups   int
	snapshots int
}

func (e *parityTestCountingEnv) Lookup(name string) (string, bool) {
	e.lookups++
	if e.vals == nil {
		return "", false
	}
	v, ok := e.vals[name]
	return v, ok
}

func (e *parityTestCountingEnv) Snapshot() []string {
	e.snapshots++
	if e.vals == nil {
		return nil
	}
	out := make([]string, 0, len(e.vals))
	for k, v := range e.vals {
		out = append(out, k+"="+v)
	}
	return out
}

type parityTestPanicEnv struct {
	calls int
}

func (p *parityTestPanicEnv) Lookup(string) (string, bool) {
	p.calls++
	panic("secret guard environment must not be consulted")
}

func (p *parityTestPanicEnv) Snapshot() []string {
	p.calls++
	panic("secret guard environment must not be consulted")
}

type parityTestCustomObserver struct {
	events []sdksg.DecisionEvent
}

func (o *parityTestCustomObserver) OnSecretDecision(_ context.Context, ev sdksg.DecisionEvent) error {
	o.events = append(o.events, ev)
	return nil
}

// --- Parity and Registration Order Tests ---

func TestSecretGuardProjection_ParityWithFrozenAndRegistrationOrder(t *testing.T) {
	t.Parallel()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SecretGuards: []sdksg.Guard{
			parityTestSGGuard{id: "sg-b1-20", ord: 20},
			parityTestSGGuard{id: "sg-b1-10", ord: 10},
		},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SecretGuards: []sdksg.Guard{
			parityTestSGGuard{id: "sg-b2-5", ord: 5},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	// 1. Verify Frozen plane contents preserve registration order
	frozenGuards := lipfeature.Get(gen.Frozen, lipfeature.PlaneSecretGuards)
	require.Len(t, frozenGuards, 3)
	assert.Equal(t, "sg-b1-20", frozenGuards[0].ID())
	assert.Equal(t, "sg-b1-10", frozenGuards[1].ID())
	assert.Equal(t, "sg-b2-5", frozenGuards[2].ID())

	// 2. Build secret guard runtime from FeaturePlanes
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	res, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, nil)
	require.NoError(t, err)
	require.NotNil(t, res)

	// 3. Plane.Guards retains registration order (defensive clone)
	require.Len(t, res.Plane.Guards, 3)
	assert.Equal(t, "sg-b1-20", res.Plane.Guards[0].ID())
	assert.Equal(t, "sg-b1-10", res.Plane.Guards[1].ID())
	assert.Equal(t, "sg-b2-5", res.Plane.Guards[2].ID())

	// 4. Snapshot SecretGuardExecutionPlane materializes in sorted order (ord ascending, then ID)
	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{SecretGuardPlane: res.Plane})
	execGuards := snap.SecretGuardExecutionPlane().Guards
	require.Len(t, execGuards, 3)
	assert.Equal(t, "sg-b2-5", execGuards[0].ID())
	assert.Equal(t, "sg-b1-10", execGuards[1].ID())
	assert.Equal(t, "sg-b1-20", execGuards[2].ID())

	// 5. Snapshot SecretGuardPlane returns sorted defensive clone
	snapGuards := snap.SecretGuardPlane().Guards
	require.Len(t, snapGuards, 3)
	assert.Equal(t, "sg-b2-5", snapGuards[0].ID())
	assert.Equal(t, "sg-b1-10", snapGuards[1].ID())
	assert.Equal(t, "sg-b1-20", snapGuards[2].ID())
}

func TestSecretGuardProjection_NilAndEmptySlicePreservation(t *testing.T) {
	t.Parallel()

	t.Run("nil_contributions", func(t *testing.T) {
		t.Parallel()
		var zeroFrozen lipfeature.FrozenPlaneSet
		guards := lipfeature.Get(zeroFrozen, lipfeature.PlaneSecretGuards)
		assert.Nil(t, guards)
	})

	t.Run("empty_contributions", func(t *testing.T) {
		t.Parallel()
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneSecretGuards, "p1", []sdksg.Guard{})
		require.NoError(t, err)
		frozen := cs.Freeze()
		guards := lipfeature.Get(frozen, lipfeature.PlaneSecretGuards)
		assert.NotNil(t, guards)
		assert.Empty(t, guards)
	})

	t.Run("literal_and_boxed_typed_nil_slice_elements", func(t *testing.T) {
		t.Parallel()
		var typedNil *parityTestSGGuard
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneSecretGuards, "p1", []sdksg.Guard{
			nil,
			typedNil,
			parityTestSGGuard{id: "valid-sg", ord: 1},
			nil,
		})
		require.NoError(t, err)

		frozen := cs.Freeze()
		guards := lipfeature.Get(frozen, lipfeature.PlaneSecretGuards)
		require.Len(t, guards, 4)
		assert.Nil(t, guards[0])
		assert.True(t, guards[1] != nil, "boxed typed-nil must not equal untyped nil interface")
		assert.True(t, sdksg.IsNilGuard(guards[1]), "IsNilGuard must report true for typed nil")
		assert.NotNil(t, guards[2])
		assert.Nil(t, guards[3])

		// Safe runtime projection does not invoke methods on nil elements
		opts := &BuildOptions{
			FeaturePlanes: frozen,
		}
		res, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, nil)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Len(t, res.Plane.Guards, 4)

		// Snapshot execution plane filters both literal nil and typed nil
		snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{SecretGuardPlane: res.Plane})
		execGuards := snap.SecretGuardExecutionPlane().Guards
		require.Len(t, execGuards, 1)
		assert.Equal(t, "valid-sg", execGuards[0].ID())
	})
}

func TestSecretGuardProjection_CandidateFeaturePlanesOverlay(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	require.NoError(t, reg.RegisterFeature("sg-feature", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			SecretGuards: []sdksg.Guard{
				parityTestSGGuard{id: "base-guard-1", ord: 10},
			},
		}, nil
	}))

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

	cand := obsTestCandidateConfig(t, "sg-feature")

	candBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SecretGuards: []sdksg.Guard{
			parityTestSGGuard{id: "cand-guard-1", ord: 5},
		},
	}
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	// Compile with candidate overlay
	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, genRuntime)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	exec := bundle.execution.executor
	require.NotNil(t, exec)
	require.NotNil(t, exec.RuntimeSnapshot)

	// Runtime snapshot should contain both base and candidate guards in sorted order (5 then 10)
	snap := exec.RuntimeSnapshot
	execGuards := snap.SecretGuardExecutionPlane().Guards
	require.Len(t, execGuards, 2)
	assert.Equal(t, "cand-guard-1", execGuards[0].ID())
	assert.Equal(t, "base-guard-1", execGuards[1].ID())
}

func TestSecretGuardProjection_HostCapabilitiesEnvironmentPreserved(t *testing.T) {
	t.Parallel()

	t.Run("environment_consulted_when_feature_enabled_in_single_user", func(t *testing.T) {
		t.Parallel()
		env := &parityTestCountingEnv{
			vals: map[string]string{
				"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
			},
		}
		opts := &BuildOptions{
			Extensions: ExtensionsOptions{
				SecretGuardEnvironment: env,
			},
		}
		regs := []lipsdk.Registration{{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: redact\n")},
		}}

		res, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Greater(t, env.lookups+env.snapshots, 0)
		assert.Equal(t, "single_user", res.Plane.AccessMode)
		assert.NotNil(t, res.Inventory)
		assert.Greater(t, res.Inventory.SecretGuardCatalogEntryCount, 0)
	})

	t.Run("environment_not_consulted_when_feature_disabled", func(t *testing.T) {
		t.Parallel()
		env := &parityTestPanicEnv{}
		cs := lipfeature.NewContributionSet()
		_ = lipfeature.Contribute(cs, lipfeature.PlaneSecretGuards, "p", []sdksg.Guard{parityTestSGGuard{id: "injected", ord: 1}})
		opts := &BuildOptions{
			FeaturePlanes: cs.Freeze(),
			Extensions: ExtensionsOptions{
				SecretGuardEnvironment: env,
			},
		}
		regs := []lipsdk.Registration{{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     false,
		}}

		res, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, 0, env.calls)
	})

	t.Run("environment_not_consulted_in_multi_user_mode", func(t *testing.T) {
		t.Parallel()
		env := &parityTestPanicEnv{}
		opts := &BuildOptions{
			Extensions: ExtensionsOptions{
				SecretGuardEnvironment: env,
			},
		}
		regs := []lipsdk.Registration{{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: block\n")},
		}}
		cfg := &config.Config{Access: config.AccessConfig{Mode: "multi_user"}}

		res, err := buildSecretGuardRuntime(cfg, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, 0, env.calls)
		assert.Equal(t, "multi_user", res.Plane.AccessMode)
		assert.Equal(t, "multi_user", res.Inventory.SecretGuardAccessMode)
	})
}

func TestSecretGuardProjection_HostCapabilitiesObserverFallbackAndChaining(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 1. Explicit observer chained
	customObs := &parityTestCustomObserver{}
	cs1 := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs1, lipfeature.PlaneSecretGuards, "p", []sdksg.Guard{parityTestSGGuard{id: "g1", ord: 1}})
	opts1 := &BuildOptions{
		FeaturePlanes: cs1.Freeze(),
		Extensions: ExtensionsOptions{
			SecretDecisionObserver: customObs,
		},
	}
	res1, err := buildSecretGuardRuntime(&config.Config{}, log, opts1, nil)
	require.NoError(t, err)
	require.NotNil(t, res1.Plane.DecisionObserver)
	err = res1.Plane.DecisionObserver.OnSecretDecision(context.Background(), sdksg.DecisionEvent{EventID: "ev-1"})
	require.NoError(t, err)
	require.Len(t, customObs.events, 1)

	// 2. Typed-nil observer falls back to slog
	var typedNilObs *parityTestCustomObserver
	cs2 := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cs2, lipfeature.PlaneSecretGuards, "p", []sdksg.Guard{parityTestSGGuard{id: "g1", ord: 1}})
	opts2 := &BuildOptions{
		FeaturePlanes: cs2.Freeze(),
		Extensions: ExtensionsOptions{
			SecretDecisionObserver: typedNilObs,
		},
	}
	res2, err := buildSecretGuardRuntime(&config.Config{}, log, opts2, nil)
	require.NoError(t, err)
	require.NotNil(t, res2.Plane.DecisionObserver)
	logBuf.Reset()
	err = res2.Plane.DecisionObserver.OnSecretDecision(context.Background(), sdksg.DecisionEvent{EventID: "ev-typed-nil"})
	require.NoError(t, err)
	assert.Contains(t, logBuf.String(), "lip.secret_guard.decision")
}

func TestSecretGuardProjection_HostCapabilitiesSingleUserInputsPreserved(t *testing.T) {
	t.Parallel()

	customMatcher := coresg.MatcherOptions{
		PreserveKnownPrefixes: false,
		MaskByte:              '#',
	}
	inputs := SecretGuardInputs{
		SingleUser: coresg.SingleUserOptions{
			MatcherConfigured: true,
			Matcher:           customMatcher,
		},
	}
	runtimeCfg := featuresg.RuntimeConfig{
		Enabled:               true,
		PreserveKnownPrefixes: true,
		MaskByte:              '*',
	}
	out := composeSecretGuardSingleUser(runtimeCfg, inputs)
	assert.True(t, out.MatcherConfigured)
	assert.Equal(t, byte('#'), out.Matcher.MaskByte, "custom matcher mask byte must be preserved")
	assert.False(t, out.Matcher.PreserveKnownPrefixes, "custom matcher prefix preservation must be preserved")
}

func TestSecretGuardProjection_CompositionRootUniqueness(t *testing.T) {
	t.Parallel()

	t.Run("duplicate_enabled_registrations_exact_error", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{
			Extensions: ExtensionsOptions{
				SecretGuardEnvironment: &parityTestPanicEnv{},
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "sg-1", FactoryKind: "secrets-guard", Enabled: true, Config: lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: log\n")}},
			{Kind: lipsdk.PluginKindFeature, ID: "sg-2", FactoryKind: "secrets-guard", Enabled: true, Config: lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: redact\n")}},
		}

		res, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.Error(t, err)
		assert.Nil(t, res)
		assert.Equal(t, "runtimebundle: multiple enabled secrets-guard registrations", err.Error())
	})

	t.Run("case_insensitive_factory_key_duplicate_rejected", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{}
		cases := []struct {
			name string
			k1   string
			k2   string
		}{
			{"uppercase_duplicate", "SECRETS-GUARD", "secrets-guard"},
			{"mixed_case_duplicate", "Secrets-Guard", "SECRETS-GUARD"},
			{"whitespace_padded", " secrets-guard ", "secrets-guard"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				regs := []lipsdk.Registration{
					{Kind: lipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: tc.k1, Enabled: true, Config: lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: log\n")}},
					{Kind: lipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: tc.k2, Enabled: true, Config: lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: log\n")}},
				}
				_, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
				require.Error(t, err)
				assert.Equal(t, "runtimebundle: multiple enabled secrets-guard registrations", err.Error())
			})
		}
	})

	t.Run("one_enabled_one_disabled_succeeds", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "sg-disabled", FactoryKind: "secrets-guard", Enabled: false, Config: lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: log\n")}},
			{Kind: lipsdk.PluginKindFeature, ID: "sg-enabled", FactoryKind: "secrets-guard", Enabled: true, Config: lipsdk.ConfigPayload{Node: mustNodeForRuntimebundle(t, "action: block\n")}},
		}
		res, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, "block", res.Inventory.SecretGuardAction)
	})

	t.Run("zero_enabled_registrations_succeeds_empty", func(t *testing.T) {
		t.Parallel()
		opts := &BuildOptions{}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "sg-disabled", FactoryKind: "secrets-guard", Enabled: false},
		}
		res, err := buildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, regs)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Empty(t, res.Plane.Guards)
		assert.Nil(t, res.Inventory)
	})
}

func TestSecretGuardProjection_DiagnosticsAndOperatorInventory(t *testing.T) {
	t.Parallel()

	guards := []sdksg.Guard{
		parityTestSGGuard{id: "sg-z", ord: 20},
		parityTestSGGuard{id: "sg-a", ord: 10},
		nil,
	}

	occupants := lipfeature.PlaneSecretGuards.Diagnostics.Materialize(guards)
	require.Len(t, occupants, 2, "Materialize must filter nil guards")
	assert.Equal(t, "secret_guard:sg-a", occupants[0].Label)
	assert.Equal(t, "secret_guard:sg-z", occupants[1].Label)
	assert.Equal(t, lipfeature.StageIDSecretGuard, lipfeature.PlaneSecretGuards.Diagnostics.StageID)
}
