package ollama_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCapsFromOllamaCapabilities_narrows(t *testing.T) {
	t.Parallel()
	full := ollama.CapsFromOllamaCapabilities([]string{"completion", "tools", "thinking", "vision"})
	if _, ok := full[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming")
	}
	if _, ok := full[lipapi.CapabilityTools]; !ok {
		t.Fatal("expected tools")
	}
	if _, ok := full[lipapi.CapabilityReasoning]; !ok {
		t.Fatal("expected reasoning")
	}
	if _, ok := full[lipapi.CapabilityVision]; !ok {
		t.Fatal("expected vision")
	}
	if _, ok := full[lipapi.CapabilityDocuments]; ok {
		t.Fatal("documents must not be advertised")
	}
	if _, ok := full[lipapi.CapabilityParallelToolCalls]; ok {
		t.Fatal("parallel tool calls must not be advertised")
	}
}

func TestCapsFromOllamaCapabilities_completionOnly(t *testing.T) {
	t.Parallel()
	caps := ollama.CapsFromOllamaCapabilities([]string{"completion"})
	if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming")
	}
	if _, ok := caps[lipapi.CapabilityTools]; ok {
		t.Fatal("unexpected tools")
	}
	if _, ok := caps[lipapi.CapabilityVision]; ok {
		t.Fatal("unexpected vision")
	}
}
