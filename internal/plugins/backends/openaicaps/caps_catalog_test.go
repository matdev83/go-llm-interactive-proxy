package openaicaps

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestForHostedModel_gpt35_dropsVision(t *testing.T) {
	t.Parallel()
	c := ForHostedModel("gpt-3.5-turbo")
	if _, ok := c[lipapi.CapabilityVision]; ok {
		t.Fatal("expected vision stripped")
	}
	if _, ok := c[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming retained")
	}
}

func TestForHostedModel_o1_dropsParallelTools(t *testing.T) {
	t.Parallel()
	c := ForHostedModel("o1-mini")
	if _, ok := c[lipapi.CapabilityParallelToolCalls]; ok {
		t.Fatal("expected parallel tool calls stripped for o-series catalog row")
	}
	if _, ok := c[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming retained")
	}
}

func TestForHostedModel_gpt4o_full(t *testing.T) {
	t.Parallel()
	c := ForHostedModel("gpt-4o-mini")
	if len(c) < len(HostedFull) {
		t.Fatalf("expected full hosted surface, got %d caps", len(c))
	}
}
