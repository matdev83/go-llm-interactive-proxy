package runtimebundle

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeatureHost_NoPackageLevelFallback_WhenStandardFeaturesNil(t *testing.T) {
	t.Parallel()

	ps := &ProcessServices{
		StandardFeatures: nil,
	}

	regs := []lipsdk.Registration{
		{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "reasoning-test",
			FactoryKind: "reasoning-preservation-compression",
			Enabled:     true,
			Config: lipsdk.ConfigPayload{
				Node: mustYAMLNode(t, "unsupported_field: true\n"),
			},
		},
	}

	// When ps.StandardFeatures is nil, CompileGeneration returns disabled behavior without fallback.
	out, err := ps.StandardFeatures.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations: regs,
	})
	require.NoError(t, err)
	assert.True(t, out.Planes.IsZero())
	assert.Empty(t, out.Lifecycles)
	assert.Equal(t, featurehost.CorePorts{}, out.CorePorts)
}

func TestCompileCandidate_DirectCallerHasNoSecretGuardFallback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	// Candidate with secret-guard enabled
	candCfg := testProcessServicesOwnershipConfig()
	candCfg.Plugins.Features = []config.PluginConfig{
		{
			ID:      "secrets-guard",
			Enabled: true,
			Config:  mustYAMLNode(t, "action: block\npatterns: [\"secret\"]\n"),
		},
	}

	// Direct CompileCandidate call without composed secret-guard planes.
	// Must NOT construct secret guard via fallback route.
	cand, err := CompileCandidate(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: candCfg,
		CandidateOpts: &BuildOptions{
			PluginRegistry: reg,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cand.Close() })

	// Assert secret guard is nil: direct CompileCandidate callers retain NO separate composition route.
	assert.Nil(t, CandidateSecretGuardInventory(cand), "expected nil secret guard inventory without composed execution planes")
}
