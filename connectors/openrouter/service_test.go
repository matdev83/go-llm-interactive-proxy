package openrouter_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/openrouter/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func mustCfg(t *testing.T, yaml string) backendplugin.ConfigureRequest {
	t.Helper()
	return backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKind,
		InstanceID:    "t1",
		ConfigYAML:    []byte(yaml),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

func TestConfigure_DefaultBaseURL(t *testing.T) {
	t.Parallel()
	cfg, err := service.ParseConfigYAML([]byte("api_key: k\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != service.DefaultURL {
		t.Fatalf("base=%s", cfg.BaseURL)
	}
	_, err = service.New().Configure(context.Background(), mustCfg(t, "api_key: k\n"))
	if err != nil {
		t.Fatal(err)
	}
}
