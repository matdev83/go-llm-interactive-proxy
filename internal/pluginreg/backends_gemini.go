package pluginreg

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"gopkg.in/yaml.v3"
)

func backendGemini(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("gemini backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://generativelanguage.googleapis.com")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.Gemini)
	cfg := gemini.Config{
		BaseURL:     base,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(gemini.New(cfg), y.Models)
}
