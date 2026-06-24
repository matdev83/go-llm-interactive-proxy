package pluginreg

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/llamacpp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/lmstudio"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/vllm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

var ErrCustomBackendPrefix = errors.New("custom backend prefix")

const (
	CustomOpenAILegacyCompatibleID    = "custom-openai-legacy-compatible"
	CustomOpenAIResponsesCompatibleID = "custom-openai-responses-compatible"
	CustomAnthropicCompatibleID       = "custom-anthropic-compatible"
)

type customCompatibleBackendYAML struct {
	BackendPrefix    string                 `yaml:"backend_prefix"`
	BaseURL          string                 `yaml:"base_url"`
	APIKey           string                 `yaml:"api_key"`
	APIKeys          []string               `yaml:"api_keys"`
	Credentials      []hostedCredentialYAML `yaml:"credentials"`
	APIKeyEnvVarRoot string                 `yaml:"api_key_env_var_root"`
	Models           modelInventoryYAML     `yaml:"models"`
}

type customBackendPrefixEntry struct {
	Enabled       bool
	BackendPrefix string
	InstanceID    string
}

func decodeCustomCompatibleBackendYAML(n yaml.Node) (customCompatibleBackendYAML, error) {
	var y customCompatibleBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return customCompatibleBackendYAML{}, err
	}
	return y, nil
}

func resolveCustomCompatibleAPIKeys(y customCompatibleBackendYAML) []string {
	if creds := hostedCredentials(y.Credentials); len(creds) > 0 {
		out := make([]string, 0, len(creds))
		for _, cred := range creds {
			if secret := strings.TrimSpace(cred.Secret); secret != "" {
				out = append(out, secret)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	root := strings.TrimSpace(y.APIKeyEnvVarRoot)
	var envFallback []string
	if root != "" {
		envFallback = collectNumberedEnvKeys(root)
	}
	return inventoryAPIKeys(y.APIKey, y.APIKeys, nil, envFallback)
}

func validateCustomBackendPrefix(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("custom backend: backend_prefix is required")
	}
	if strings.Contains(prefix, "/") || strings.Contains(prefix, ":") {
		return fmt.Errorf("custom backend: backend_prefix %q must not contain '/' or ':'", prefix)
	}
	if _, reserved := reservedStandardBackendPrefixes[prefix]; reserved {
		return fmt.Errorf("custom backend: backend_prefix %q is reserved by a standard connector", prefix)
	}
	return nil
}

func validateEnabledCustomBackendPrefixes(entries []customBackendPrefixEntry) error {
	seen := make(map[string]string)
	for _, row := range entries {
		if !row.Enabled {
			continue
		}
		prefix := strings.TrimSpace(row.BackendPrefix)
		if err := validateCustomBackendPrefix(prefix); err != nil {
			return err
		}
		if prev, ok := seen[prefix]; ok {
			return fmt.Errorf("custom backend: duplicate backend_prefix %q (instances %q and %q)", prefix, prev, row.InstanceID)
		}
		seen[prefix] = row.InstanceID
	}
	return nil
}

func IsCustomCompatibleBackendKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case CustomOpenAILegacyCompatibleID, CustomOpenAIResponsesCompatibleID, CustomAnthropicCompatibleID:
		return true
	default:
		return false
	}
}

func ValidateCustomCompatibleBackendPrefixes(rows []config.PluginConfig) error {
	entries := make([]customBackendPrefixEntry, 0, len(rows))
	for _, row := range rows {
		kind := row.FactoryID()
		if !IsCustomCompatibleBackendKind(kind) {
			continue
		}
		y, err := decodeCustomCompatibleBackendYAML(row.Config)
		if err != nil {
			return fmt.Errorf("custom backend instance %s (factory %s): %w", row.InstanceID(), kind, err)
		}
		entries = append(entries, customBackendPrefixEntry{
			Enabled:       row.Enabled,
			BackendPrefix: y.BackendPrefix,
			InstanceID:    row.InstanceID(),
		})
	}
	if err := validateEnabledCustomBackendPrefixes(entries); err != nil {
		return fmt.Errorf("%w: %w", ErrCustomBackendPrefix, err)
	}
	return nil
}

func customOpenAILegacyTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}

func customOpenAIResponsesTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIResponses,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}

func buildCustomOpenAICompatibleBackend(
	y customCompatibleBackendYAML,
	upstream *http.Client,
	flavor openaicompat.Flavor,
	transportCaps lipapi.BackendTransportCaps,
) (execbackend.Backend, error) {
	prefix := strings.TrimSpace(y.BackendPrefix)
	if err := validateCustomBackendPrefix(prefix); err != nil {
		return execbackend.Backend{}, err
	}
	base := strings.TrimSpace(y.BaseURL)
	ek := resolveCustomCompatibleAPIKeys(y)
	creds := hostedCredentials(y.Credentials)
	apiKey := ""
	if len(ek) > 0 {
		apiKey = ek[0]
	}
	inventory := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:         base,
		APIKey:          apiKey,
		APIKeys:         ek,
		Credentials:     credpool.Secrets(creds),
		HTTPClient:      resolveUpstreamHTTP(upstream),
		CanonicalPrefix: prefix,
	}
	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:            prefix,
		BaseURL:       base,
		APIKey:        apiKey,
		APIKeys:       ek,
		Credentials:   creds,
		HTTPClient:    resolveUpstreamHTTP(upstream),
		Inventory:     inventory,
		ResolveFlavor: func(lipapi.Call) openaicompat.Flavor { return flavor },
	})
	be.TransportCaps = transportCaps
	return applyConfiguredModelInventory(be, y.Models)
}

func backendCustomOpenAILegacyCompatible(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	y, err := decodeCustomCompatibleBackendYAML(n)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("%s backend config: %w", CustomOpenAILegacyCompatibleID, err)
	}
	return buildCustomOpenAICompatibleBackend(y, upstream, openaicompat.FlavorChat, customOpenAILegacyTransportCaps())
}

func backendCustomOpenAIResponsesCompatible(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	y, err := decodeCustomCompatibleBackendYAML(n)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("%s backend config: %w", CustomOpenAIResponsesCompatibleID, err)
	}
	return buildCustomOpenAICompatibleBackend(y, upstream, openaicompat.FlavorResponses, customOpenAIResponsesTransportCaps())
}

var reservedStandardBackendPrefixes = map[string]struct{}{
	openairesponses.ID: {},
	openailegacy.ID:    {},
	anthropic.ID:       {},
	gemini.ID:          {},
	bedrock.ID:         {},
	acp.ID:             {},
	openrouter.ID:      {},
	nvidia.ID:          {},
	ollama.ID:          {},
	ollama.CloudID:     {},
	llamacpp.ID:        {},
	lmstudio.ID:        {},
	vllm.ID:            {},
	localstub.ID:       {},
}
