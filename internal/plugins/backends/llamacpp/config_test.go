package llamacpp_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/llamacpp"
)

func TestApplyDefaults_baseURLAndDummyCredential(t *testing.T) {
	t.Parallel()
	cfg := llamacpp.ApplyDefaults(llamacpp.Config{})
	if cfg.BaseURL != "http://localhost:8080/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	key, _, _ := llamacpp.EffectiveCredentials(cfg)
	if key != "llamacpp" {
		t.Fatalf("credential = %q, want llamacpp", key)
	}
}

func TestApplyDefaults_catalogLookupDefaults(t *testing.T) {
	t.Parallel()
	cfg := llamacpp.ApplyDefaults(llamacpp.Config{})
	if !llamacpp.DiscoveryCatalog(cfg.Discovery) {
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
	cfg := llamacpp.Config{APIKey: "my-secret"}
	key, _, _ := llamacpp.EffectiveCredentials(cfg)
	if key != "my-secret" {
		t.Fatalf("credential = %q", key)
	}
}
