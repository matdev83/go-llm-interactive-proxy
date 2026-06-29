package opencodezen_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodezen"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_BackendConfigMapping(t *testing.T) {
	t.Parallel()

	models := []opencodecommon.ModelEntry{
		{RawID: "test-model"},
	}
	creds := []credpool.Credential{
		{Secret: "test-cred"},
	}
	httpClient := &http.Client{}
	retries := 3

	cfg := opencodezen.Config{
		BaseURL:           "http://test.local",
		APIKey:            "test-key",
		APIKeys:           []string{"test-key-2"},
		Credentials:       creds,
		HTTPClient:        httpClient,
		SDKMaxRetries:     &retries,
		Models:            models,
		RateLimitFallback: time.Second,
	}

	be := opencodezen.New(cfg)

	if len(be.BackendPrefixes) == 0 || be.BackendPrefixes[0] != "opencode-zen" {
		t.Fatalf("expected backend prefix 'opencode-zen', got %v", be.BackendPrefixes)
	}

	if be.ModelInventory == nil {
		t.Fatal("ModelInventory is nil")
	}

	caps := be.ResolveCaps(context.Background(), lipapi.Call{}, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "test-model"},
	})

	if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("Expected typical capabilities to be supported")
	}
}
