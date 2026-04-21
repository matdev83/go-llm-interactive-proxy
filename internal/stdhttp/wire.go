package stdhttp

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
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

func decodeYAMLNode(n yaml.Node, into any) error {
	if n.Kind == 0 {
		return nil
	}
	return n.Decode(into)
}

func resolveOpenAIKey(apiKey string) string {
	if strings.TrimSpace(apiKey) != "" {
		return strings.TrimSpace(apiKey)
	}
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

func resolveAnthropicKey(apiKey string) string {
	if strings.TrimSpace(apiKey) != "" {
		return strings.TrimSpace(apiKey)
	}
	return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
}

func resolveGeminiKey(apiKey string) string {
	if strings.TrimSpace(apiKey) != "" {
		return strings.TrimSpace(apiKey)
	}
	return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
}

// BuildExecutor wires enabled backends from configuration into a core executor.
func BuildExecutor(cfg *config.Config, bus *hooks.Bus) (*runtime.Executor, b2bua.Store, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("stdhttp: nil config")
	}
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	backends := make(map[string]runtime.Backend)
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		be, err := backendFromPlugin(p)
		if err != nil {
			return nil, nil, err
		}
		backends[p.ID] = be
	}
	store := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	exec := &runtime.Executor{
		Store:    store,
		Bus:      bus,
		Backends: backends,
	}
	return exec, store, nil
}

func backendFromPlugin(p config.PluginConfig) (runtime.Backend, error) {
	switch p.ID {
	case openairesponses.ID:
		var y openAIStyleYAML
		if err := decodeYAMLNode(p.Config, &y); err != nil {
			return runtime.Backend{}, fmt.Errorf("backend %s: %w", p.ID, err)
		}
		base := strings.TrimSpace(y.BaseURL)
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		return openairesponses.New(openairesponses.Config{
			BaseURL: base,
			APIKey:  resolveOpenAIKey(y.APIKey),
		}), nil
	case openailegacy.ID:
		var y openAIStyleYAML
		if err := decodeYAMLNode(p.Config, &y); err != nil {
			return runtime.Backend{}, fmt.Errorf("backend %s: %w", p.ID, err)
		}
		base := strings.TrimSpace(y.BaseURL)
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		return openailegacy.New(openailegacy.Config{
			BaseURL: base,
			APIKey:  resolveOpenAIKey(y.APIKey),
		}), nil
	case anthropic.ID:
		var y openAIStyleYAML
		if err := decodeYAMLNode(p.Config, &y); err != nil {
			return runtime.Backend{}, fmt.Errorf("backend %s: %w", p.ID, err)
		}
		base := strings.TrimSpace(y.BaseURL)
		if base == "" {
			base = "https://api.anthropic.com"
		}
		return anthropic.New(anthropic.Config{
			BaseURL: base,
			APIKey:  resolveAnthropicKey(y.APIKey),
		}), nil
	case gemini.ID:
		var y openAIStyleYAML
		if err := decodeYAMLNode(p.Config, &y); err != nil {
			return runtime.Backend{}, fmt.Errorf("backend %s: %w", p.ID, err)
		}
		base := strings.TrimSpace(y.BaseURL)
		if base == "" {
			base = "https://generativelanguage.googleapis.com"
		}
		return gemini.New(gemini.Config{
			BaseURL: base,
			APIKey:  resolveGeminiKey(y.APIKey),
		}), nil
	case bedrock.ID:
		var y bedrockBackendYAML
		if err := decodeYAMLNode(p.Config, &y); err != nil {
			return runtime.Backend{}, fmt.Errorf("backend %s: %w", p.ID, err)
		}
		return bedrock.New(bedrock.Config{
			Region:          y.Region,
			AccessKeyID:     y.AccessKeyID,
			SecretAccessKey: y.SecretAccessKey,
			SessionToken:    y.SessionToken,
			BaseEndpoint:    y.BaseEndpoint,
			DisableHTTPS:    y.DisableHTTPS,
			HTTPClient:      http.DefaultClient,
		}), nil
	case acp.ID:
		var y acpBackendYAML
		if err := decodeYAMLNode(p.Config, &y); err != nil {
			return runtime.Backend{}, fmt.Errorf("backend %s: %w", p.ID, err)
		}
		base := strings.TrimSpace(y.BaseURL)
		if base == "" {
			return runtime.Backend{}, fmt.Errorf("backend acp: base_url is required")
		}
		return acp.New(acp.Config{
			BaseURL:    base,
			HTTPClient: http.DefaultClient,
		}), nil
	default:
		return runtime.Backend{}, fmt.Errorf("stdhttp: unknown backend plugin id %q", p.ID)
	}
}
