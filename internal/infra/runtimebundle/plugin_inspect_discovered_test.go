package runtimebundle_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"gopkg.in/yaml.v3"
)

func TestInspect_DiscoveredOptionalKinds_NoBuiltinCollision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind  string
		stage func(testing.TB) string
	}{
		{kind: "openrouter", stage: bpkit.StageOpenRouter},
		{kind: "opencode-go", stage: bpkit.StageOpenCode},
		{kind: "opencode-zen", stage: bpkit.StageOpenCode},
		{kind: "openai-codex", stage: bpkit.StageCodex},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			root := tc.stage(t)

			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
				t.Fatal(err)
			}
			// Simulate post-InstallDiscoveredExports: discovered provenance must not
			// classify the optional kind as a builtin (false builtin_collision).
			if err := reg.RegisterDiscoveredLifecycleBackendWithProfile(tc.kind, func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
				return pluginreg.BackendBuildResult{Backend: execbackend.Backend{}}, nil
			}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}); err != nil {
				t.Fatal(err)
			}

			cfg := &config.Config{
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins: config.PluginsConfig{
					BackendDiscovery: config.BackendDiscoveryConfig{
						Enabled: true, Paths: []string{root}, DevelopmentMode: true,
					},
					Backends: []config.PluginConfig{{
						Kind: tc.kind, ID: tc.kind + "-1", Enabled: true,
					}},
				},
			}
			rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
			if err != nil {
				t.Fatal(err)
			}
			var sawDiscovered, sawBuiltinCollision, sawKindAsBuiltin bool
			for _, e := range rep.Entries {
				if e.Kind == tc.kind && e.Reason == catalog.ReasonBuiltinCollision {
					sawBuiltinCollision = true
				}
				if e.Source == "builtin" && e.Kind == tc.kind {
					sawKindAsBuiltin = true
				}
				if e.Kind == tc.kind && (e.Source == "discovered" || e.Source == "configured") &&
					e.Reason != catalog.ReasonBuiltinCollision {
					sawDiscovered = true
				}
			}
			if sawBuiltinCollision {
				t.Fatalf("discovered kind %q falsely classified as builtin_collision: %+v", tc.kind, rep.Entries)
			}
			if sawKindAsBuiltin {
				t.Fatalf("discovered kind %q must not appear as builtin source: %+v", tc.kind, rep.Entries)
			}
			if !sawDiscovered {
				t.Fatalf("expected discovered/configured entry for %q: %+v", tc.kind, rep.Entries)
			}
		})
	}
}

func TestInspect_RegistryPollution_OptionalKindNotBuiltin(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"openrouter", "opencode-go", "openai-codex"} {
		if err := reg.RegisterDiscoveredLifecycleBackendWithProfile(kind, func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
			return pluginreg.BackendBuildResult{Backend: execbackend.Backend{}}, nil
		}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{Enabled: false},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Entries {
		if e.Source == "builtin" && (e.Kind == "openrouter" || e.Kind == "opencode-go" || e.Kind == "openai-codex") {
			t.Fatalf("optional kind %q classified as builtin after registry pollution: %+v", e.Kind, rep.Entries)
		}
	}
}

func TestInspect_RealBuiltinCollision_Diagnosed(t *testing.T) {
	t.Parallel()
	root := stageColliderExportingEssential(t, "openai-responses")

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true,
			},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, e := range rep.Entries {
		if e.Kind == "openai-responses" && e.Reason == catalog.ReasonBuiltinCollision && e.Source == "discovered" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected builtin_collision for openai-responses: %+v", rep.Entries)
	}
}

func stageColliderExportingEssential(t *testing.T, kind string) string {
	t.Helper()
	repo := findInspectRepoRoot(t)
	root := t.TempDir()
	binName := "lip-backendplugin-fake"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	rel := filepath.ToSlash(filepath.Join("bin", binName))
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", dst, "./internal/testkit/backendplugin/cmd/lip-backendplugin-fake")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build collider fake: %v\n%s", err, out)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	body := fmt.Sprintf(`{
  "schema":"golip.backendplugin.manifest/v1",
  "plugin_id":"io.golip.backend.collider",
  "version":"1.0.0",
  "build_id":"b",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":%q,
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance"
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH, kind)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func findInspectRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
