package pluginreg

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"gopkg.in/yaml.v3"
)

func backendAnthropic(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("anthropic backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.anthropic.com")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.Anthropic)
	cfg := anthropic.Config{
		BaseURL:     base,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(anthropic.New(cfg), y.Models)
}

func backendCustomAnthropicCompatible(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	y, err := decodeCustomCompatibleBackendYAML(n)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("%s backend config: %w", CustomAnthropicCompatibleID, err)
	}
	prefix := strings.TrimSpace(y.BackendPrefix)
	if err := validateCustomBackendPrefix(prefix); err != nil {
		return execbackend.Backend{}, err
	}
	base := strings.TrimSpace(y.BaseURL)
	ek := resolveCustomCompatibleAPIKeys(y)
	cfg := anthropic.Config{
		BaseURL:       base,
		BackendPrefix: prefix,
		APIKeys:       ek,
		Credentials:   hostedCredentials(y.Credentials),
		HTTPClient:    resolveUpstreamHTTP(upstream),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(anthropic.New(cfg), y.Models)
}
