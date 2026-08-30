//go:build red

package featurebundle

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// TestRED_LifecycleSurface_ExplicitEmptyPreserved characterizes Requirement 4.2 (Task 1.4):
// An explicit non-nil empty lifecycle bundle (Lifecycles: []lipplugin.Lifecycle{}) must preserve
// a non-nil length-zero slice in GeneratedMergeSurface.
//
// On the review baseline (Phase 1), feature composition collapses non-nil empty lifecycles to nil
// through slice append. This test fails now (RED) and will naturally turn GREEN when Task 4.1 fixes
// lifecycle assembly.
func TestRED_LifecycleSurface_ExplicitEmptyPreserved(t *testing.T) {
	t.Parallel()

	bEmpty := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		Lifecycles:    []lipplugin.Lifecycle{},
	}

	t.Run("MergeBundlesGenerated_ExplicitEmptyPreserved", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(bEmpty)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles, "explicit non-nil empty lifecycles must be preserved as non-nil in GeneratedMergeSurface")
		require.Len(t, gen.Lifecycles, 0)
	})

	t.Run("MergeFeatureSurfaceGenerated_ExplicitEmptyPreserved", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (lipfeature.FeatureBundle, error) {
				return bEmpty, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "empty-feat", FactoryKind: "empty-feat", Enabled: true},
		}
		gen, err := MergeFeatureSurfaceGenerated(reg, regs)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles, "explicit non-nil empty lifecycles must be preserved as non-nil in GeneratedMergeSurface")
		require.Len(t, gen.Lifecycles, 0)
	})

	t.Run("MergeFeatureSurfacesWithHost_ExplicitEmptyPreserved", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (lipfeature.FeatureBundle, error) {
				return bEmpty, nil
			},
		}
		regs := []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "empty-feat", FactoryKind: "empty-feat", Enabled: true},
		}
		_, gen, err := MergeFeatureSurfacesWithHost(reg, regs, HostContributions{}, bEmpty)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles, "explicit non-nil empty lifecycles must be preserved as non-nil in GeneratedMergeSurface")
		require.Len(t, gen.Lifecycles, 0)
	})
}
