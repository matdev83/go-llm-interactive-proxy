package ollama_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
)

func TestApplyDefaults_baseURLAndDummyCredential(t *testing.T) {
	t.Parallel()
	cfg := ollama.ApplyDefaults(ollama.Config{})
	if cfg.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	key, _, _ := ollama.EffectiveCredentials(cfg)
	if key != "ollama" {
		t.Fatalf("credential = %q, want ollama", key)
	}
}

func TestApplyDefaults_responsesAPIAndDiscovery(t *testing.T) {
	t.Parallel()
	cfg := ollama.ApplyDefaults(ollama.Config{})
	if cfg.ResponsesAPI != "auto" {
		t.Fatalf("ResponsesAPI = %q", cfg.ResponsesAPI)
	}
	if !ollama.DiscoveryEnabled(cfg.Discovery) {
		t.Fatal("expected discovery enabled")
	}
	if !ollama.DiscoveryLocal(cfg.Discovery) {
		t.Fatal("expected local discovery")
	}
	if !ollama.DiscoveryCloud(cfg.Discovery) {
		t.Fatal("expected cloud discovery")
	}
	if !ollama.DiscoveryCatalog(cfg.Discovery) {
		t.Fatal("expected catalog lookup")
	}
	if !ollama.DiscoveryCapabilities(cfg.Discovery) {
		t.Fatal("expected capability probing")
	}
	if cfg.Discovery.CloudURL != "https://ollama.com/api/tags" {
		t.Fatalf("CloudURL = %q", cfg.Discovery.CloudURL)
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
	cfg := ollama.Config{APIKey: "my-secret"}
	key, _, _ := ollama.EffectiveCredentials(cfg)
	if key != "my-secret" {
		t.Fatalf("credential = %q", key)
	}
}
