package pluginreg

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/lmstudio"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/vllm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

type bedrockBackendYAML struct {
	Region                   string             `yaml:"region"`
	AccessKeyID              string             `yaml:"access_key_id"`
	SecretAccessKey          string             `yaml:"secret_access_key"`
	SessionToken             string             `yaml:"session_token"`
	BaseEndpoint             string             `yaml:"base_endpoint"`
	DisableHTTPS             bool               `yaml:"disable_https"`
	AllowInsecureNonLoopback bool               `yaml:"allow_insecure_non_loopback"`
	Models                   modelInventoryYAML `yaml:"models"`
}

type acpBackendYAML struct {
	BaseURL string             `yaml:"base_url"`
	Models  modelInventoryYAML `yaml:"models"`
}

type openAIStyleYAML struct {
	BaseURL     string                 `yaml:"base_url"`
	APIKey      string                 `yaml:"api_key"`
	APIKeys     []string               `yaml:"api_keys"`
	Credentials []hostedCredentialYAML `yaml:"credentials"`
	Models      modelInventoryYAML     `yaml:"models"`
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

type modelInventoryYAML struct {
	Source string                   `yaml:"source"`
	Path   string                   `yaml:"path"`
	Items  []modelInventoryItemYAML `yaml:"items"`
}

type modelInventoryFileYAML struct {
	Items  []modelInventoryItemYAML `yaml:"items"`
	Models []modelInventoryItemYAML `yaml:"models"`
}

type modelInventoryItemYAML struct {
	CanonicalID string `yaml:"canonical_id"`
	NativeID    string `yaml:"native_id"`
	DisplayName string `yaml:"display_name"`
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

func inventoryAPIKeys(apiKey string, apiKeys []string, credentials []hostedCredentialYAML, fallback []string) []string {
	out := EffectiveAPIKeys(apiKey, apiKeys, fallback)
	for _, cred := range hostedCredentials(credentials) {
		if secret := strings.TrimSpace(cred.Secret); secret != "" {
			out = append(out, secret)
		}
	}
	return out
}

func applyConfiguredModelInventory(be execbackend.Backend, y modelInventoryYAML) (execbackend.Backend, error) {
	provider, ok, err := configuredModelInventory(y)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if ok {
		be.ModelInventory = provider
	}
	return be, nil
}

func configuredModelInventory(y modelInventoryYAML) (modelinventory.Provider, bool, error) {
	source := strings.ToLower(strings.TrimSpace(y.Source))
	path := strings.TrimSpace(y.Path)
	if path != "" && len(y.Items) > 0 {
		return nil, false, fmt.Errorf("backend models: specify either path or items, not both")
	}
	if source == "" {
		if len(y.Items) == 0 && path == "" {
			return nil, false, nil
		}
		if path != "" && len(y.Items) == 0 {
			source = "file"
		} else {
			source = "inline"
		}
	}
	switch source {
	case "inline", "static_inline":
		return staticModelInventory(modelinventory.SourceStaticInline, y.Items)
	case "file", "static_file":
		path := strings.TrimSpace(y.Path)
		if path == "" {
			return nil, false, fmt.Errorf("backend models: path is required for source %q", source)
		}
		items, err := loadModelInventoryFile(path)
		if err != nil {
			return nil, false, err
		}
		return staticModelInventory(modelinventory.SourceStaticFile, items)
	default:
		return nil, false, fmt.Errorf("backend models: unsupported source %q", y.Source)
	}
}

func loadModelInventoryFile(path string) ([]modelInventoryItemYAML, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("backend models: read %q: %w", path, err)
	}
	var y modelInventoryFileYAML
	if err := yaml.Unmarshal(b, &y); err != nil {
		return nil, fmt.Errorf("backend models: decode %q: %w", path, err)
	}
	if len(y.Items) > 0 {
		return y.Items, nil
	}
	return y.Models, nil
}

func staticModelInventory(source modelinventory.Source, rows []modelInventoryItemYAML) (modelinventory.Provider, bool, error) {
	if len(rows) == 0 {
		return nil, false, fmt.Errorf("backend models: at least one item is required")
	}
	models := make([]modelinventory.Model, 0, len(rows))
	for i, row := range rows {
		canonical := strings.TrimSpace(row.CanonicalID)
		native := strings.TrimSpace(row.NativeID)
		if canonical == "" || native == "" {
			return nil, false, fmt.Errorf("backend models: item[%d] requires canonical_id and native_id", i)
		}
		models = append(models, modelinventory.Model{
			CanonicalID: canonical,
			NativeID:    native,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	return modelinventory.StaticProvider{
		Source: source,
		Models: models,
	}, true, nil
}

func backendOpenAIResponses(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openairesponses backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.openai.com/v1")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.OpenAI)
	cfg := openairesponses.Config{
		BaseURL:     base,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(openairesponses.New(cfg), y.Models)
}

func backendOpenAILegacy(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openailegacy backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://api.openai.com/v1")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.OpenAI)
	cfg := openailegacy.Config{
		BaseURL:     base,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(openailegacy.New(cfg), y.Models)
}

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

func backendBedrock(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	var y bedrockBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("bedrock backend config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), bedrock.DefaultLoadConfigTimeout)
	defer cancel()
	return applyConfiguredModelInventory(bedrock.NewWithContext(ctx, bedrock.Config{
		Region:                   y.Region,
		AccessKeyID:              y.AccessKeyID,
		SecretAccessKey:          y.SecretAccessKey,
		SessionToken:             y.SessionToken,
		BaseEndpoint:             y.BaseEndpoint,
		DisableHTTPS:             y.DisableHTTPS,
		AllowInsecureNonLoopback: y.AllowInsecureNonLoopback,
		HTTPClient:               resolveUpstreamHTTP(upstream),
	}), y.Models)
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

type lmstudioDiscoveryYAML struct {
	Catalog    *bool  `yaml:"catalog"`
	CatalogURL string `yaml:"catalog_url"`
	Timeout    string `yaml:"timeout"`
}

type lmstudioBackendYAML struct {
	BaseURL     string                 `yaml:"base_url"`
	APIKey      string                 `yaml:"api_key"`
	APIKeys     []string               `yaml:"api_keys"`
	Credentials []hostedCredentialYAML `yaml:"credentials"`
	Discovery   lmstudioDiscoveryYAML  `yaml:"discovery"`
	Models      modelInventoryYAML     `yaml:"models"`
}

func backendLmstudio(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	var y lmstudioBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("lmstudio backend config: %w", err)
	}
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, nil)
	discovery := lmstudio.DiscoveryConfig{
		Catalog:    y.Discovery.Catalog,
		CatalogURL: strings.TrimSpace(y.Discovery.CatalogURL),
	}
	if timeout := strings.TrimSpace(y.Discovery.Timeout); timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil {
			return execbackend.Backend{}, fmt.Errorf("lmstudio discovery timeout: %w", err)
		}
		discovery.Timeout = d
	}
	cfg := lmstudio.Config{
		BaseURL:     strings.TrimSpace(y.BaseURL),
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
		Discovery:   discovery,
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(lmstudio.New(cfg), y.Models)
}

type vllmDiscoveryYAML struct {
	Catalog    *bool  `yaml:"catalog"`
	CatalogURL string `yaml:"catalog_url"`
	Timeout    string `yaml:"timeout"`
}

type vllmBackendYAML struct {
	BaseURL     string                 `yaml:"base_url"`
	APIKey      string                 `yaml:"api_key"`
	APIKeys     []string               `yaml:"api_keys"`
	Credentials []hostedCredentialYAML `yaml:"credentials"`
	Discovery   vllmDiscoveryYAML      `yaml:"discovery"`
	Models      modelInventoryYAML     `yaml:"models"`
}

func backendVllm(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	var y vllmBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("vllm backend config: %w", err)
	}
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, nil)
	discovery := vllm.DiscoveryConfig{
		Catalog:    y.Discovery.Catalog,
		CatalogURL: strings.TrimSpace(y.Discovery.CatalogURL),
	}
	if timeout := strings.TrimSpace(y.Discovery.Timeout); timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil {
			return execbackend.Backend{}, fmt.Errorf("vllm discovery timeout: %w", err)
		}
		discovery.Timeout = d
	}
	cfg := vllm.Config{
		BaseURL:     strings.TrimSpace(y.BaseURL),
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
		Discovery:   discovery,
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
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

type openRouterBackendYAML struct {
	BaseURL       string                 `yaml:"base_url"`
	APIKey        string                 `yaml:"api_key"`
	APIKeys       []string               `yaml:"api_keys"`
	Credentials   []hostedCredentialYAML `yaml:"credentials"`
	StaticReferer string                 `yaml:"static_referer"`
	StaticTitle   string                 `yaml:"static_title"`
	Models        modelInventoryYAML     `yaml:"models"`
}

func backendOpenRouter(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openRouterBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openrouter backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://openrouter.ai/api/v1")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.OpenRouter)
	cfg := openrouter.Config{
		BaseURL:       base,
		APIKeys:       ek,
		Credentials:   hostedCredentials(y.Credentials),
		HTTPClient:    resolveUpstreamHTTP(upstream),
		StaticReferer: strings.TrimSpace(y.StaticReferer),
		StaticTitle:   strings.TrimSpace(y.StaticTitle),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(openrouter.New(cfg), y.Models)
}

type ollamaDiscoveryYAML struct {
	Enabled        *bool  `yaml:"enabled"`
	LocalModels    *bool  `yaml:"local_models"`
	CloudModels    *bool  `yaml:"cloud_models"`
	Catalog        *bool  `yaml:"catalog"`
	Capabilities   *bool  `yaml:"capabilities"`
	CloudModelsURL string `yaml:"cloud_models_url"`
	CatalogURL     string `yaml:"catalog_url"`
	Timeout        string `yaml:"timeout"`
}

type ollamaBackendYAML struct {
	BaseURL      string                 `yaml:"base_url"`
	APIKey       string                 `yaml:"api_key"`
	APIKeys      []string               `yaml:"api_keys"`
	Credentials  []hostedCredentialYAML `yaml:"credentials"`
	ResponsesAPI string                 `yaml:"responses_api"`
	Discovery    ollamaDiscoveryYAML    `yaml:"discovery"`
	Models       modelInventoryYAML     `yaml:"models"`
}

func backendOllama(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	cfg, models, err := parseOllamaBackendConfig(n, upstream)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredModelInventory(ollama.New(cfg), models)
}

func backendOllamaCloud(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	cfg, models, err := parseOllamaBackendConfig(n, upstream)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredModelInventory(ollama.NewCloud(cfg), models)
}

func parseOllamaBackendConfig(n yaml.Node, upstream *http.Client) (ollama.Config, modelInventoryYAML, error) {
	var y ollamaBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return ollama.Config{}, modelInventoryYAML{}, fmt.Errorf("ollama backend config: %w", err)
	}
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, nil)
	discovery := ollama.DiscoveryConfig{
		Enabled:      y.Discovery.Enabled,
		Local:        y.Discovery.LocalModels,
		Cloud:        y.Discovery.CloudModels,
		Catalog:      y.Discovery.Catalog,
		Capabilities: y.Discovery.Capabilities,
		CloudURL:     strings.TrimSpace(y.Discovery.CloudModelsURL),
		CatalogURL:   strings.TrimSpace(y.Discovery.CatalogURL),
	}
	if timeout := strings.TrimSpace(y.Discovery.Timeout); timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil {
			return ollama.Config{}, modelInventoryYAML{}, fmt.Errorf("ollama discovery timeout: %w", err)
		}
		discovery.Timeout = d
	}
	cfg := ollama.Config{
		BaseURL:      strings.TrimSpace(y.BaseURL),
		APIKeys:      ek,
		Credentials:  hostedCredentials(y.Credentials),
		HTTPClient:   resolveUpstreamHTTP(upstream),
		ResponsesAPI: strings.TrimSpace(y.ResponsesAPI),
		Discovery:    discovery,
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return cfg, y.Models, nil
}
