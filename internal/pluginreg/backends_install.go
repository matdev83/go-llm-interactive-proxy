package pluginreg

import (
	"cmp"
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
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
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
	BaseURL     string                 `yaml:"base_url"`
	APIKey      string                 `yaml:"api_key"`
	APIKeys     []string               `yaml:"api_keys"`
	Credentials []hostedCredentialYAML `yaml:"credentials"`
}

type hostedCredentialYAML struct {
	ID                string `yaml:"id"`
	APIKey            string `yaml:"api_key"`
	RemoteOrgID       string `yaml:"remote_org_id"`
	RemoteProjectID   string `yaml:"remote_project_id"`
	RemoteWorkspaceID string `yaml:"remote_workspace_id"`
	RemoteAccountID   string `yaml:"remote_account_id"`
	RemoteRegion      string `yaml:"remote_region"`
}

func resolveUpstreamHTTP(upstream *http.Client) *http.Client {
	if upstream != nil {
		return upstream
	}
	return httpclient.Standard()
}

func hostedCredentials(rows []hostedCredentialYAML) []credpool.Credential {
	if len(rows) == 0 {
		return nil
	}
	out := make([]credpool.Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, credpool.Credential{
			ID:                strings.TrimSpace(row.ID),
			Secret:            strings.TrimSpace(row.APIKey),
			RemoteOrgID:       strings.TrimSpace(row.RemoteOrgID),
			RemoteProjectID:   strings.TrimSpace(row.RemoteProjectID),
			RemoteWorkspaceID: strings.TrimSpace(row.RemoteWorkspaceID),
			RemoteAccountID:   strings.TrimSpace(row.RemoteAccountID),
			RemoteRegion:      strings.TrimSpace(row.RemoteRegion),
		})
	}
	return out
}

func backendOpenAIResponses(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openairesponses backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.openai.com/v1")
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.OpenAI)
	cfg := openairesponses.Config{BaseURL: base, APIKeys: ek, Credentials: hostedCredentials(y.Credentials)}
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
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.openai.com/v1")
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.OpenAI)
	cfg := openailegacy.Config{BaseURL: base, APIKeys: ek, Credentials: hostedCredentials(y.Credentials)}
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
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.anthropic.com")
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.Anthropic)
	cfg := anthropic.Config{BaseURL: base, APIKeys: ek, Credentials: hostedCredentials(y.Credentials)}
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
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://generativelanguage.googleapis.com")
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.Gemini)
	cfg := gemini.Config{BaseURL: base, APIKeys: ek, Credentials: hostedCredentials(y.Credentials)}
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
	return acp.New(acp.Config{
		BaseURL:    base,
		HTTPClient: resolveUpstreamHTTP(upstream),
	}), nil
}

func backendNvidia(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("nvidia backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://integrate.api.nvidia.com/v1")
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.Nvidia)
	cfg := nvidia.Config{BaseURL: base, APIKeys: ek, Credentials: hostedCredentials(y.Credentials)}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return nvidia.New(cfg), nil
}

type openRouterBackendYAML struct {
	BaseURL       string                 `yaml:"base_url"`
	APIKey        string                 `yaml:"api_key"`
	APIKeys       []string               `yaml:"api_keys"`
	Credentials   []hostedCredentialYAML `yaml:"credentials"`
	StaticReferer string                 `yaml:"static_referer"`
	StaticTitle   string                 `yaml:"static_title"`
}

func backendOpenRouter(n yaml.Node, _ *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openRouterBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openrouter backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://openrouter.ai/api/v1")
	ek := EffectiveAPIKeys(y.APIKey, y.APIKeys, keys.OpenRouter)
	cfg := openrouter.Config{
		BaseURL:       base,
		APIKeys:       ek,
		Credentials:   hostedCredentials(y.Credentials),
		StaticReferer: strings.TrimSpace(y.StaticReferer),
		StaticTitle:   strings.TrimSpace(y.StaticTitle),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return openrouter.New(cfg), nil
}
