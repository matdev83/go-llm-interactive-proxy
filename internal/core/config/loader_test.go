package config_test

import (
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestLoadFileLoadsBootstrapConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Address == "" {
		t.Fatal("expected server address")
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
