package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"gopkg.in/yaml.v3"
)

func TestBackendDiscovery_DefaultsWhenAbsent(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	bd := cfg.Plugins.BackendDiscovery
	if bd.Enabled || bd.Strict || bd.DevelopmentMode || len(bd.Paths) != 0 {
		t.Fatalf("expected zero defaults, got %+v", bd)
	}
	if len(cfg.Plugins.Backends) != 1 || cfg.Plugins.Backends[0].ID != "stub" {
		t.Fatalf("existing backends changed: %+v", cfg.Plugins.Backends)
	}
}

func TestBackendDiscovery_DecodePathsStrictDevelopment(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
access:
  mode: single_user
continuity:
  in_memory: true
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /srv/company/go-lip/plugins
    strict: true
    development_mode: true
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	bd := cfg.Plugins.BackendDiscovery
	if !bd.Enabled || !bd.Strict || !bd.DevelopmentMode {
		t.Fatalf("flags: %+v", bd)
	}
	if len(bd.Paths) != 1 || bd.Paths[0] != "/srv/company/go-lip/plugins" {
		t.Fatalf("paths: %+v", bd.Paths)
	}
}

func TestBackendDiscovery_RejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backend_discovery:
    enabled: true
    codex_catalog: true
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), `unknown config key "codex_catalog"`) {
		t.Fatalf("want unknown key rejection, got %v", err)
	}
}

func TestBackendDiscovery_ProductionRejectsDevelopmentModeMultiUser(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				Paths:           []string{"/tmp/plugins"},
				DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "development_mode") {
		t.Fatalf("want production rejection of development_mode, got %v", err)
	}
}

func TestBackendDiscovery_DevelopmentModeRequiresPaths(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "paths") {
		t.Fatalf("want paths required, got %v", err)
	}
}

func TestBackendDiscovery_ValidateRejectsEmptyPathEntry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true,
				Paths:   []string{"  "},
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "paths[0]") {
		t.Fatalf("want empty path rejection, got %v", err)
	}
}

func TestBackendDiscovery_StrictDecodeHelperRejectsUnknown(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nextra: 1\n"), &n); err != nil {
		t.Fatal(err)
	}
	_, err := config.DecodeBackendDiscovery(n)
	if err == nil || !strings.Contains(err.Error(), `unknown config key "extra"`) {
		t.Fatalf("got %v", err)
	}
}

func TestBackendDiscovery_ExistingBackendRowsUnchanged(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - kind: openrouter
      id: openrouter-primary
      enabled: true
      config:
        api_key: secret-value
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	be := cfg.Plugins.Backends[0]
	if be.Kind != "openrouter" || be.ID != "openrouter-primary" || !be.Enabled {
		t.Fatalf("backend row changed: %+v", be)
	}
	var raw map[string]any
	if err := be.Config.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if raw["api_key"] != "secret-value" {
		t.Fatalf("opaque config changed: %+v", raw)
	}
}
