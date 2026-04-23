package pluginreg

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
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
	Region                   string `yaml:"region"`
	AccessKeyID              string `yaml:"access_key_id"`
	SecretAccessKey          string `yaml:"secret_access_key"`
	SessionToken             string `yaml:"session_token"`
	BaseEndpoint             string `yaml:"base_endpoint"`
	DisableHTTPS             bool   `yaml:"disable_https"`
	AllowInsecureNonLoopback bool   `yaml:"allow_insecure_non_loopback"`
}

type acpBackendYAML struct {
	BaseURL string `yaml:"base_url"`
}

type openAIStyleYAML struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	APIKeys []string `yaml:"api_keys"`
}

func resolveUpstreamHTTP(upstream *http.Client) *http.Client {
	if upstream != nil {
		return upstream
	}
	return httpclient.Standard()
}

func backendOpenAIResponses(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openairesponses backend config: %w", err)
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.OpenAI)
	cfg := openairesponses.Config{BaseURL: base, APIKeys: ek}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return openairesponses.New(cfg), nil
}

func backendOpenAILegacy(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openailegacy backend config: %w", err)
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.OpenAI)
	cfg := openailegacy.Config{BaseURL: base, APIKeys: ek}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return openailegacy.New(cfg), nil
}

func backendAnthropic(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("anthropic backend config: %w", err)
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://api.anthropic.com"
	}
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.Anthropic)
	cfg := anthropic.Config{BaseURL: base, APIKeys: ek}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return anthropic.New(cfg), nil
}

func backendGemini(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("gemini backend config: %w", err)
	}
	base := strings.TrimSpace(y.BaseURL)
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.Gemini)
	cfg := gemini.Config{BaseURL: base, APIKeys: ek}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return gemini.New(cfg), nil
}

func backendBedrock(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	var y bedrockBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("bedrock backend config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), bedrock.DefaultLoadConfigTimeout)
	defer cancel()
	return bedrock.NewWithContext(ctx, bedrock.Config{
		Region:                   y.Region,
		AccessKeyID:              y.AccessKeyID,
		SecretAccessKey:          y.SecretAccessKey,
		SessionToken:             y.SessionToken,
		BaseEndpoint:             y.BaseEndpoint,
		DisableHTTPS:             y.DisableHTTPS,
		AllowInsecureNonLoopback: y.AllowInsecureNonLoopback,
		HTTPClient:               resolveUpstreamHTTP(upstream),
	}), nil
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
	return acp.New(acp.Config{
		BaseURL:    base,
		HTTPClient: resolveUpstreamHTTP(upstream),
	}), nil
}
