package runtimebundle_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

const cursorCLIACPKind = "cursorcliacp"

func TestBuild_cursorSDKAndCLIACPCoexistWithProvenance(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	ws := t.TempDir()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.InstallBundleOn(reg, standardplugins.Bundle{Backends: []standardplugins.BackendRegistration{
		standardplugins.ExperimentalCursorSDKRegistration(standardplugins.UpstreamAPIKeys{}),
		{
			ID: cursorCLIACPKind,
			Factory: func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
				return execbackend.Backend{
					Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					BackendPrefixes: []string{cursorCLIACPKind},
					ModelInventory: modelinventory.StaticProvider{
						Models: []modelinventory.Model{{
							CanonicalID: "cursor/composer-2-fast",
							NativeID:    "composer-2-fast",
							DisplayName: "Composer 2 Fast",
						}},
					},
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						return nil, fmt.Errorf("cursorcliacp stub: open not used")
					},
				}, nil
			},
			Profile: pluginreg.BackendSecurityProfile{
				CredentialMode: pluginreg.CredentialNone,
				AccessScope:    pluginreg.BackendAccessLocalOnly,
			},
		},
	}}); err != nil {
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
	var acpNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}\n"), &acpNode); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 2},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: cursorsdk.ID, ID: "sdk-a", Enabled: true, Config: sdkNode},
			{Kind: cursorCLIACPKind, ID: "acp-b", Enabled: true, Config: acpNode},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	refs, ok := built.ModelRegistry().Lookup("cursor/composer-2-fast")
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
	if byKind[cursorsdk.ID] != "sdk-a" || byKind[cursorCLIACPKind] != "acp-b" {
		t.Fatalf("provenance=%#v", byKind)
	}

	sdkBE := built.Executor().Backends["sdk-a"]
	acpBE := built.Executor().Backends["acp-b"]
	if len(sdkBE.BackendPrefixes) != 1 || sdkBE.BackendPrefixes[0] != cursorsdk.ID {
		t.Fatalf("sdk prefixes=%v", sdkBE.BackendPrefixes)
	}
	if len(acpBE.BackendPrefixes) != 1 || acpBE.BackendPrefixes[0] != cursorCLIACPKind {
		t.Fatalf("acp prefixes=%v", acpBE.BackendPrefixes)
	}
}
