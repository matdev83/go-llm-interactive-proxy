package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"gopkg.in/yaml.v3"
)

func backendAnthropic(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("anthropic backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.anthropic.com")
	ek, primaryKey := firstAPIKey(y.APIKey, y.APIKeys, y.Credentials, keys.Anthropic)
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, "anthropic backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	cfg := anthropic.Config{
		BaseURL:       base,
		APIKey:        primaryKey,
		APIKeys:       ek,
		Credentials:   hostedCredentials(y.Credentials),
		HTTPClient:    httpClient,
		SDKMaxRetries: sdkMaxRetriesOrDefault(y.SDKMaxRetries),
	}
	return applyConfiguredModelInventory(anthropic.New(cfg), y.Models)
}
