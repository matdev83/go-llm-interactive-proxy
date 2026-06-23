package lmstudio_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/lmstudio"
)

func TestApplyDefaults_baseURLAndDummyCredential(t *testing.T) {
	t.Parallel()
	cfg := lmstudio.ApplyDefaults(lmstudio.Config{})
	if cfg.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	key, _, _ := lmstudio.EffectiveCredentials(cfg)
	if key != "lmstudio" {
		t.Fatalf("credential = %q, want lmstudio", key)
	}
}

func TestApplyDefaults_catalogLookupDefaults(t *testing.T) {
	t.Parallel()
	cfg := lmstudio.ApplyDefaults(lmstudio.Config{})
	if !lmstudio.DiscoveryCatalog(cfg.Discovery) {
		t.Fatal("expected catalog lookup enabled by default")
	}
	if cfg.Discovery.CatalogURL != "https://models.dev/api.json" {
		t.Fatalf("CatalogURL = %q", cfg.Discovery.CatalogURL)
	}
	if cfg.Discovery.Timeout != 15*time.Second {
		t.Fatalf("Timeout = %v", cfg.Discovery.Timeout)
	}
}

func TestEffectiveCredentials_explicitAPIKeyPreserved(t *testing.T) {
	t.Parallel()
	cfg := lmstudio.Config{APIKey: "my-secret"}
	key, _, _ := lmstudio.EffectiveCredentials(cfg)
	if key != "my-secret" {
		t.Fatalf("credential = %q", key)
	}
}
