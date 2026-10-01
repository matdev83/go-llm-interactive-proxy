package standardplugins

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestStandardBundle_AgentLoopGuardDisabledContributesNoProvider(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: false"), &node); err != nil {
		t.Fatal(err)
	}
	bundle, err := testRegistryWithStdBundle(t).BuildFeatureBundle(agentloopguard.ID, node)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	if prov := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); prov != nil {
		t.Fatal("disabled ALG must contribute no terminal decision provider")
	}
}

func TestStandardBundle_AgentLoopGuardEnabledContributesSingularProvider(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nmax_semantic_continuations: 2"), &node); err != nil {
		t.Fatal(err)
	}
	bundle, err := testRegistryWithStdBundle(t).BuildFeatureBundle(agentloopguard.ID, node)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	if prov := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); prov == nil {
		t.Fatal("enabled ALG must contribute the singular terminal decision provider")
	}
	if id, ok := lipfeature.FrozenIdentity(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); !ok || id == "" {
		t.Fatalf("provider identity missing or empty on PlaneSet: id=%q ok=%v", id, ok)
	}
}

func TestStandardBundle_AgentLoopGuardInvalidConfigFailsBeforeBundle(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nmax_semantic_continuations: 0"), &node); err != nil {
		t.Fatal(err)
	}
	if _, err := testRegistryWithStdBundle(t).BuildFeatureBundle(agentloopguard.ID, node); err == nil {
		t.Fatal("invalid ALG config must fail before FeatureBundle construction")
	}
}

func TestStandardBundle_AgentLoopGuardStrategyPlanes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw         string
		terminal, control bool
	}{
		{"disabled", "enabled: false\nstrategy: attempt_completion\n", false, false},
		{"omitted", "enabled: true\n", true, false},
		{"legacy", "enabled: true\nstrategy: semantic_verifier\n", true, false},
		{"preferred", "enabled: true\nstrategy: attempt_completion\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := mustBuildALGBundle(t, tc.raw)
			require.Equal(t, lipfeature.SchemaVersionV1, b.SchemaVersion)
			require.Equal(t, tc.terminal, lipfeature.Get(b.PlaneSet, lipfeature.PlaneTerminalDecisionProvider) != nil)
			require.Equal(t, tc.control, lipfeature.Get(b.PlaneSet, lipfeature.PlaneControlToolProvider) != nil)
			count := 0
			if tc.terminal {
				count++
			}
			if tc.control {
				count++
			}
			require.Len(t, occupiedStandardPlanes(t, b.PlaneSet), count)
		})
	}
}

type algForbiddenAux struct{ calls int }

func (a *algForbiddenAux) Collect(context.Context, auxiliary.Request) (lipapi.Collected, error) {
	a.calls++
	return lipapi.Collected{}, fmt.Errorf("preferred composition reached auxiliary Collect")
}

func (a *algForbiddenAux) Stream(context.Context, auxiliary.Request) (lipapi.EventStream, error) {
	a.calls++
	return nil, fmt.Errorf("preferred composition reached auxiliary Stream")
}

func assertALGPreferredSnapshot(t *testing.T, snap *extensions.RequestRuntimeSnapshot) {
	t.Helper()
	provider := snap.TerminalDecisionProvider()
	require.NotNil(t, provider)
	require.Equal(t, reflect.TypeOf(agentloopguard.NewCompletionToolProvider()), reflect.TypeOf(snap.ControlToolProvider()))
	control := snap.ControlToolProvider()
	require.NotNil(t, control)
	spec := control.Spec()
	require.Equal(t, "attempt_completion", spec.Tool.Name)
	require.NoError(t, controltool.ValidateProvider(control))
	spec.Tool.Parameters[0] = 'x'
	require.NotEqual(t, byte('x'), control.Spec().Tool.Parameters[0], "spec must be defensively copied")
	aux := &algForbiddenAux{}
	in := algPinnedSemanticInput()
	in.Evidence.ExplicitCompletionExpected = true
	in.Evidence.Lineage.ProgressRef = in.Request.BLegID
	in.Auxiliary = aux
	decision, err := provider.Decide(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, terminaldecision.DecisionContinue, decision.Kind)
	require.NotNil(t, decision.Continue)
	require.NoError(t, decision.Validate())
	in.Evidence.ExplicitCompletion = true
	decision, err = provider.Decide(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, terminaldecision.DecisionAllowStop, decision.Kind)
	require.Nil(t, decision.Continue)
	require.Zero(t, aux.calls)
	for _, tc := range []struct {
		args   string
		kind   controltool.OutcomeKind
		result string
	}{
		{`{"result":"done"}`, controltool.OutcomeComplete, "done"},
		{`{"result":"done","command":"run"}`, controltool.OutcomeInvalid, ""},
	} {
		out, err := control.Handle(context.Background(), controltool.CompletedCall{ToolCallID: "call-1", ToolName: spec.Tool.Name, ArgsJSON: []byte(tc.args)}, controltool.Meta{})
		require.NoError(t, err)
		require.Equal(t, tc.kind, out.Kind)
		require.Equal(t, tc.result, out.ResultText)
	}
}

// Compile through the real registry, enabled-registration merge, and immutable
// request snapshot builder used by production generation composition.
func TestStandardBundle_AgentLoopGuardStrategyGenerationSnapshots(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	compile := func(raw string, outerEnabled bool, extra ...lipfeature.FeatureBundle) (*extensions.RequestRuntimeSnapshot, error) {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
			return nil, err
		}
		registrations := []lipsdk.Registration{{ID: agentloopguard.ID, Kind: lipsdk.PluginKindFeature, Enabled: outerEnabled, Config: lipsdk.ConfigPayload{Node: node}}}
		merged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{}, extra...)
		if err != nil {
			return nil, err
		}
		return extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: merged.Frozen}), nil
	}
	g1, err := compile("enabled: true\nstrategy: attempt_completion\n", true)
	require.NoError(t, err)
	assertALGPreferredSnapshot(t, g1)
	g2, err := compile("enabled: true\nstrategy: semantic_verifier\n", true)
	require.NoError(t, err)
	require.NotNil(t, g2.TerminalDecisionProvider())
	require.Nil(t, g2.ControlToolProvider())
	assertALGProviderTripsSemanticCap(t, g2.TerminalDecisionProvider(), agentloopguard.DefaultMaxSemanticContinuations)
	omitted, err := compile("enabled: true\n", true)
	require.NoError(t, err)
	require.Equal(t, reflect.TypeOf(g2.TerminalDecisionProvider()), reflect.TypeOf(omitted.TerminalDecisionProvider()))
	assertALGProviderTripsSemanticCap(t, omitted.TerminalDecisionProvider(), agentloopguard.DefaultMaxSemanticContinuations)
	g3, err := compile("enabled: false\nstrategy: attempt_completion\n", true)
	require.NoError(t, err)
	require.Nil(t, g3.TerminalDecisionProvider())
	require.Nil(t, g3.ControlToolProvider())
	removed, err := compile("enabled: true\nstrategy: attempt_completion\n", false)
	require.NoError(t, err)
	require.Nil(t, removed.TerminalDecisionProvider())
	require.Nil(t, removed.ControlToolProvider())
	for _, raw := range []string{
		"enabled: true\nstrategy: attempt_completion\nverifier_role: loop_guard\n",
		"enabled: false\nstrategy: attempt_completion\nverifier_role: loop_guard\n",
		"enabled: true\nstrategy: unknown\n",
	} {
		failed, err := compile(raw, true)
		require.Error(t, err)
		require.Nil(t, failed, "invalid candidate must not produce a snapshot")
	}
	// Exercise each exclusive-plane rejection without mutating a pinned snapshot.
	for _, control := range []bool{false, true} {
		cs := lipfeature.NewContributionSet()
		if control {
			require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneControlToolProvider, "other", agentloopguard.NewCompletionToolProvider()))
		} else {
			p, err := agentloopguard.NewConfiguredProvider(agentloopguard.Config{Enabled: true, Strategy: agentloopguard.StrategySemanticVerifier})
			require.NoError(t, err)
			require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, "other", p))
		}
		failed, err := compile("enabled: true\nstrategy: attempt_completion\n", true, lipfeature.BundleFromPlanes(cs.Freeze(), nil))
		require.Error(t, err)
		require.Nil(t, failed)
	}
	// A registry with ALG withdrawn still compiles an empty generic surface.
	withdrawn := pluginreg.NewRegistry()
	stock := StandardBundle()
	withoutALG := Bundle{}
	for _, feature := range stock.Features {
		if feature.ID != agentloopguard.ID {
			withoutALG.Features = append(withoutALG.Features, feature)
		}
	}
	require.NoError(t, InstallBundleOn(withdrawn, withoutALG))
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(withdrawn, nil, featurebundle.HostContributions{})
	require.NoError(t, err)
	withdrawnSnap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: merged.Frozen})
	require.Nil(t, withdrawnSnap.TerminalDecisionProvider())
	require.Nil(t, withdrawnSnap.ControlToolProvider())
	assertALGPreferredSnapshot(t, g1)
	require.Nil(t, g2.ControlToolProvider())
}
