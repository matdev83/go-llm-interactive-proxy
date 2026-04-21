package acp

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_setsResolveCaps(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "http://127.0.0.1:9"}) // valid URL shape; client may fail handshake — we only inspect caps
	if be.ResolveCaps == nil {
		t.Fatal("expected ResolveCaps")
	}
	c := be.ResolveCaps(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if _, ok := c[lipapi.CapabilityReasoning]; !ok {
		t.Fatal("expected reasoning in default ACP caps")
	}
}
