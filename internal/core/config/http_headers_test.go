package config_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func TestHTTPHeadersYAML_emptyKeepsDefaults(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte("server:\n  address: 127.0.0.1:8080\n"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg.HTTPHeaders.Effective()
	want := lipsdk.DefaultHTTPHeaders()
	if got.APIKey[0] != want.APIKey[0] || got.Route[0] != want.Route[0] {
		t.Fatalf("defaults: api_key=%v route=%v", got.APIKey, got.Route)
	}
}

func TestHTTPHeadersYAML_aliasesAppendAfterDefaults(t *testing.T) {
	t.Parallel()
	const y = `
http_headers:
  api_key: [X-Gateway-Key]
  route: [X-Custom-Route]
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg.HTTPHeaders.Effective()
	if got.APIKey[0] != lipsdk.HeaderAuthorization {
		t.Fatalf("api_key[0]=%q", got.APIKey[0])
	}
	if got.APIKey[len(got.APIKey)-1] != "X-Gateway-Key" {
		t.Fatalf("api_key last=%q", got.APIKey[len(got.APIKey)-1])
	}
	if got.Route[0] != lipsdk.HeaderRoute || got.Route[1] != "X-Custom-Route" {
		t.Fatalf("route=%v", got.Route)
	}
}
