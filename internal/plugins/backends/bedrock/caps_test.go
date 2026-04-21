package bedrock_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestModelCapabilities_anthropicSonnet(t *testing.T) {
	t.Parallel()
	c := bedrock.ModelCapabilities("arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-5-sonnet-20240620-v1:0")
	if _, ok := c[lipapi.CapabilityParallelToolCalls]; !ok {
		t.Fatal("expected parallel tool calls")
	}
}

func TestModelCapabilities_anthropicClaude2OnBedrock(t *testing.T) {
	t.Parallel()
	c := bedrock.ModelCapabilities("anthropic.claude-v2")
	if _, ok := c[lipapi.CapabilityParallelToolCalls]; ok {
		t.Fatal("did not expect parallel tool calls for claude-v2")
	}
}

func TestModelCapabilities_titanText(t *testing.T) {
	t.Parallel()
	c := bedrock.ModelCapabilities("amazon.titan-text-express-v1")
	if _, ok := c[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming only baseline")
	}
	if len(c) != 1 {
		t.Fatalf("expected single capability, got %d", len(c))
	}
}
