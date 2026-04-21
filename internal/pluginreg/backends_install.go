package pluginreg

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"gopkg.in/yaml.v3"
)

type bedrockBackendYAML struct {
	Region          string `yaml:"region"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	SessionToken    string `yaml:"session_token"`
	BaseEndpoint    string `yaml:"base_endpoint"`
	DisableHTTPS    bool   `yaml:"disable_https"`
}

type acpBackendYAML struct {
	BaseURL string `yaml:"base_url"`
}

type openAIStyleYAML struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

func resolveUpstreamHTTP(upstream *http.Client) *http.Client {
	if upstream != nil {
		return upstream
	}
	return httpclient.Standard()
}

func backendOpenAIResponses(n yaml.Node, _ *http.Client) (runtime.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return runtime.Backend{}, err
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return openairesponses.New(openairesponses.Config{
		BaseURL: base,
		APIKey:  resolveOpenAIKey(y.APIKey),
	}), nil
}

func backendOpenAILegacy(n yaml.Node, _ *http.Client) (runtime.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return runtime.Backend{}, err
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return openailegacy.New(openailegacy.Config{
		BaseURL: base,
		APIKey:  resolveOpenAIKey(y.APIKey),
	}), nil
}

func backendAnthropic(n yaml.Node, _ *http.Client) (runtime.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return runtime.Backend{}, err
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return anthropic.New(anthropic.Config{
		BaseURL: base,
		APIKey:  resolveAnthropicKey(y.APIKey),
	}), nil
}

func backendGemini(n yaml.Node, _ *http.Client) (runtime.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return runtime.Backend{}, err
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	return gemini.New(gemini.Config{
		BaseURL: base,
		APIKey:  resolveGeminiKey(y.APIKey),
	}), nil
}

func backendBedrock(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
	var y bedrockBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return runtime.Backend{}, err
	}
	return bedrock.New(bedrock.Config{
		Region:          y.Region,
		AccessKeyID:     y.AccessKeyID,
		SecretAccessKey: y.SecretAccessKey,
		SessionToken:    y.SessionToken,
		BaseEndpoint:    y.BaseEndpoint,
		DisableHTTPS:    y.DisableHTTPS,
		HTTPClient:      resolveUpstreamHTTP(upstream),
	}), nil
}

func backendACP(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
	var y acpBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return runtime.Backend{}, err
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		return runtime.Backend{}, fmt.Errorf("backend acp: base_url is required")
	}
	return acp.New(acp.Config{
		BaseURL:    base,
		HTTPClient: resolveUpstreamHTTP(upstream),
	}), nil
}
