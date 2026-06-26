package pluginreg

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"gopkg.in/yaml.v3"
)

func backendOpenAIResponses(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openairesponses backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.openai.com/v1")
	ek, primaryKey := firstAPIKey(y.APIKey, y.APIKeys, y.Credentials, keys.OpenAI)
	cfg := openairesponses.Config{
		BaseURL:     base,
		APIKey:      primaryKey,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
	}
	return applyConfiguredModelInventory(openairesponses.New(cfg), y.Models)
}

func backendOpenAILegacy(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openailegacy backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.openai.com/v1")
	ek, primaryKey := firstAPIKey(y.APIKey, y.APIKeys, y.Credentials, keys.OpenAI)
	cfg := openailegacy.Config{
		BaseURL:     base,
		APIKey:      primaryKey,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
	}
	return applyConfiguredModelInventory(openailegacy.New(cfg), y.Models)
}
