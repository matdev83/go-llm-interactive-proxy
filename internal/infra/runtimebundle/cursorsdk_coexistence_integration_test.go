package runtimebundle_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorcliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"gopkg.in/yaml.v3"
)

func TestBuild_cursorSDKAndCLIACPCoexistWithProvenance(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	ws := t.TempDir()
	acpExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	sdkYAML := fmt.Sprintf(`api_key: coexist-key
bridge_executable: %q
default_workspace: %q
models:
  source: inline
  items:
    - canonical_id: cursor/composer-2-fast
      native_id: composer-2-fast
      display_name: Composer 2 Fast
`, exe, ws)
	var sdkNode yaml.Node
	if err := yaml.Unmarshal([]byte(sdkYAML), &sdkNode); err != nil {
		t.Fatal(err)
	}
	acpYAML := fmt.Sprintf(`executable: %q
default_workspace: %q
auto_accept: true
trust_workspace: true
models:
  source: inline
  items:
    - canonical_id: cursor/composer-2-fast
      native_id: composer-2-fast
      display_name: Composer 2 Fast
`, acpExe, ws)
	var acpNode yaml.Node
	if err := yaml.Unmarshal([]byte(acpYAML), &acpNode); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 2},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: cursorsdk.ID, ID: "sdk-a", Enabled: true, Config: sdkNode},
			{Kind: cursorcliacp.ID, ID: "acp-b", Enabled: true, Config: acpNode},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		for i := len(built.Closers) - 1; i >= 0; i-- {
			if built.Closers[i] != nil {
				_ = built.Closers[i]()
			}
		}
	})

	refs, ok := built.ModelRegistry.Lookup("cursor/composer-2-fast")
	if !ok {
		t.Fatal("canonical row missing")
	}
	if len(refs) != 2 {
		t.Fatalf("len(refs)=%d want 2", len(refs))
	}
	byKind := map[string]string{}
	for _, r := range refs {
		if r.CanonicalID != "cursor/composer-2-fast" {
			t.Fatalf("canonical=%q", r.CanonicalID)
		}
		if r.NativeID != "composer-2-fast" {
			t.Fatalf("native=%q", r.NativeID)
		}
		byKind[r.Kind] = r.BackendID
	}
	if byKind[cursorsdk.ID] != "sdk-a" || byKind[cursorcliacp.ID] != "acp-b" {
		t.Fatalf("provenance=%#v", byKind)
	}

	sdkBE := built.Executor.Backends["sdk-a"]
	acpBE := built.Executor.Backends["acp-b"]
	if len(sdkBE.BackendPrefixes) != 1 || sdkBE.BackendPrefixes[0] != cursorsdk.ID {
		t.Fatalf("sdk prefixes=%v", sdkBE.BackendPrefixes)
	}
	if len(acpBE.BackendPrefixes) != 1 || acpBE.BackendPrefixes[0] != cursorcliacp.ID {
		t.Fatalf("acp prefixes=%v", acpBE.BackendPrefixes)
	}
}
