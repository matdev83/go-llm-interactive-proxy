package compatibleutil

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/endpoint"
)

// ParseEndpoint validates base_url for a compatible instance.
func ParseEndpoint(cfg config.CompatibleModeConfig) (endpoint.Descriptor, error) {
	scope := strings.TrimSpace(cfg.BackendPrefix)
	if scope == "" {
		scope = "compatible"
	}
	d, err := endpoint.ParseBaseURL(cfg.BaseURL)
	if err != nil {
		return endpoint.Descriptor{}, fmt.Errorf("compatible backend %q: %w", scope, err)
	}
	return d, nil
}

// OpenAIModelsEndpoint joins the models path for OpenAI-compatible inventory.
func OpenAIModelsEndpoint(d endpoint.Descriptor) (string, error) {
	return d.Join(endpoint.OperationOpenAIModels)
}

// AnthropicModelsEndpoint joins the models path for Anthropic-compatible inventory.
func AnthropicModelsEndpoint(d endpoint.Descriptor) (string, error) {
	return d.Join(endpoint.OperationAnthropicModels)
}
