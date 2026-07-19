package comparison_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/comparison"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestExperimental_notInStandardDistributionRequirements(t *testing.T) {
	t.Parallel()
	for _, req := range lipsdk.StandardDistributionRequirements() {
		require.NotEqual(t, cursorsdk.ID, req.ID)
	}
}

func TestExperimental_configExampleKeepsNonSDKDefaultRoute(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "config", "examples", "cursor-sdk-experimental.yaml"))
	require.NoError(t, err)
	var doc struct {
		Routing struct {
			DefaultRoute string `yaml:"default_route"`
		} `yaml:"routing"`
		Plugins struct {
			Backends []struct {
				Kind    string `yaml:"kind"`
				Enabled *bool  `yaml:"enabled"`
			} `yaml:"backends"`
		} `yaml:"plugins"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	require.NotContains(t, doc.Routing.DefaultRoute, "cursorsdk")
	require.True(t, strings.HasPrefix(doc.Routing.DefaultRoute, "dogfood-local:"))
	var found bool
	for _, b := range doc.Plugins.Backends {
		if b.Kind == cursorsdk.ID {
			found = true
			require.NotNil(t, b.Enabled)
			require.False(t, *b.Enabled, "example must keep cursorsdk disabled by default")
		}
	}
	require.True(t, found, "example must register cursorsdk")
}

func TestReport_replacementStatusRetainBoth(t *testing.T) {
	t.Parallel()
	rep, err := comparison.BuildReport(comparison.SyntheticDocument())
	require.NoError(t, err)
	require.Equal(t, "retain_both_connectors", rep.ReplacementStatus)
}
