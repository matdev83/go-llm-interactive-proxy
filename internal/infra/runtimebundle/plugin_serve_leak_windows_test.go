//go:build windows

package runtimebundle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func listTempDirsServeForTest(prefix string) []string {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(os.TempDir(), e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func assertNoNewServeDirsForTest(t *testing.T, before []string, prefix string) {
	t.Helper()
	after := listTempDirsServeForTest(prefix)
	beforeSet := make(map[string]struct{}, len(before))
	for _, b := range before {
		beforeSet[b] = struct{}{}
	}
	for _, d := range after {
		if _, ok := beforeSet[d]; !ok {
			t.Errorf("leaked temp dir %q (prefix %q)", d, prefix)
		}
	}
}

// TestProduction_ServeAndValidateNoStagingLeak proves both the BuildHost
// (serve) and ValidateDistribution paths release every go-lip-plugin-serve-*
// staging root they create: verified artifact handles are closed before the
// staging directory is removed, so no new matching dirs remain under
// os.TempDir after normal operation on Windows.
func TestProduction_ServeAndValidateNoStagingLeak(t *testing.T) {
	const prefix = "go-lip-plugin-serve-"
	before := listTempDirsServeForTest(prefix)

	kind := "prod-leak-serve-kind"
	pluginRoot := stageProductionFakePlugin(t, kind)
	cfgPath := writeProductionDiscoveryConfig(t, pluginRoot, kind)

	if err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	}); err != nil {
		t.Fatalf("ValidateDistribution: %v", err)
	}
	assertNoNewServeDirsForTest(t, before, prefix)

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("host close: %v", err)
	}

	assertNoNewServeDirsForTest(t, before, prefix)
}
