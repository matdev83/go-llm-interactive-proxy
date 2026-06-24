package pluginreg

import (
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
)

func TestDecodeOpenAIFamilyDiscovery_invalidTimeoutErrors(t *testing.T) {
	t.Parallel()
	_, err := decodeOpenAIFamilyDiscovery("llamacpp", openAIFamilyDiscoveryYAML{
		Timeout: "not-a-duration",
	})
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if !strings.Contains(err.Error(), "llamacpp discovery timeout") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeOpenAIFamilyDiscovery_trimsAndParsesTimeout(t *testing.T) {
	t.Parallel()
	d, err := decodeOpenAIFamilyDiscovery("lmstudio", openAIFamilyDiscoveryYAML{
		CatalogURL: "  https://catalog.example/api.json  ",
		Timeout:    "  30s  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.CatalogURL != "https://catalog.example/api.json" {
		t.Fatalf("CatalogURL = %q", d.CatalogURL)
	}
	if d.Timeout != 30*time.Second {
		t.Fatalf("Timeout = %v", d.Timeout)
	}
}

func TestDecodeOpenAIFamilyDiscovery_emptyTimeoutLeavesZero(t *testing.T) {
	t.Parallel()
	d, err := decodeOpenAIFamilyDiscovery("vllm", openAIFamilyDiscoveryYAML{
		CatalogURL: "https://catalog.example/api.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Timeout != 0 {
		t.Fatalf("Timeout = %v, want zero", d.Timeout)
	}
	if d.CatalogURL != "https://catalog.example/api.json" {
		t.Fatalf("CatalogURL = %q", d.CatalogURL)
	}
}

func TestOpenAIFamilyConfigFromYAML_trimsBaseURLAndAppliesKey(t *testing.T) {
	t.Parallel()
	cfg, err := openAIFamilyConfigFromYAML("llamacpp", openAIFamilyBackendYAML{
		BaseURL: "  http://localhost:8080/v1  ",
		APIKey:  "  my-secret  ",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://localhost:8080/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.APIKey != "my-secret" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
	if !openaifamily.DiscoveryCatalog(cfg.Discovery) {
		t.Fatal("expected catalog enabled by default")
	}
}
