package pluginreg

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/llamacpp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/lmstudio"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/vllm"
	"gopkg.in/yaml.v3"
)

type acpBackendYAML struct {
	BaseURL string             `yaml:"base_url"`
	Models  modelInventoryYAML `yaml:"models"`
}

func backendLocalStub(n yaml.Node, _ *http.Client) (execbackend.Backend, error) {
	return localstub.NewFromYAML(n)
}

func backendACP(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	var y acpBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("acp backend config: %w", err)
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		return execbackend.Backend{}, fmt.Errorf("backend acp: base_url is required")
	}
	return applyConfiguredModelInventory(acp.New(acp.Config{
		BaseURL:    base,
		HTTPClient: resolveUpstreamHTTP(upstream),
	}), y.Models)
}

func backendLlamacpp(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIFamilyBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("llamacpp backend config: %w", err)
	}
	cfg, err := openAIFamilyConfigFromYAML("llamacpp", y, upstream)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredModelInventory(llamacpp.New(cfg), y.Models)
}

func backendLmstudio(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIFamilyBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("lmstudio backend config: %w", err)
	}
	cfg, err := openAIFamilyConfigFromYAML("lmstudio", y, upstream)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredModelInventory(lmstudio.New(cfg), y.Models)
}

func backendVllm(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIFamilyBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("vllm backend config: %w", err)
	}
	cfg, err := openAIFamilyConfigFromYAML("vllm", y, upstream)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredModelInventory(vllm.New(cfg), y.Models)
}

func backendNvidia(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("nvidia backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://integrate.api.nvidia.com/v1")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.Nvidia)
	cfg := nvidia.Config{
		BaseURL:     base,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(nvidia.New(cfg), y.Models)
}
