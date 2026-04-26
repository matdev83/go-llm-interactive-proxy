package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

func TestLoadFile_omittedServerAddress_defaultsToExplicitLoopback(t *testing.T) {
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
	if want := "127.0.0.1:8080"; cfg.Server.Address != want {
		t.Fatalf("server.address: want %q got %q", want, cfg.Server.Address)
	}
	if !config.IsExplicitLoopbackListenAddress(cfg.Server.Address) {
		t.Fatalf("expected explicit loopback default, got %q", cfg.Server.Address)
	}
}

func TestLoadFileLoadsBootstrapConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("routing model_aliases: %v", err)
	}

	if cfg.Server.Address == "" {
		t.Fatal("expected server address")
	}
	if !config.IsExplicitLoopbackListenAddress(cfg.Server.Address) {
		t.Fatalf("expected loopback server address from sample config, got %q", cfg.Server.Address)
	}
	if len(cfg.Plugins.Frontends) == 0 {
		t.Fatal("expected frontend plugin scaffold entries")
	}
	if !cfg.Continuity.InMemory {
		t.Fatal("expected reference config to use in-memory continuity")
	}
	if cfg.Continuity.Store != "memory" {
		t.Fatalf("continuity.store default/normalize: got %q want memory", cfg.Continuity.Store)
	}
}

func TestLoadFile_rejectsModelAliasInvalidPattern(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
model_aliases:
  - pattern: "("
    replacement: "stub:m"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err == nil {
		t.Fatal("expected routing validation error")
	}
}

func TestLoadFile_rejectsModelAliasInvalidReplacement(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
model_aliases:
  - pattern: "^x$"
    replacement: "|bad"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err == nil {
		t.Fatal("expected routing validation error")
	}
}
