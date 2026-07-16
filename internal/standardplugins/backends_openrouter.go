package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/httpidentity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"gopkg.in/yaml.v3"
)

type openRouterBackendYAML struct {
	BaseURL       string                 `yaml:"base_url"`
	APIKey        string                 `yaml:"api_key"`
	APIKeys       []string               `yaml:"api_keys"`
	Credentials   []hostedCredentialYAML `yaml:"credentials"`
	StaticReferer string                 `yaml:"static_referer"`
	StaticTitle   string                 `yaml:"static_title"`
	Models        modelInventoryYAML     `yaml:"models"`
}

func backendOpenRouter(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	var y openRouterBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openrouter backend config: %w", err)
	}
	ov, err := decodeIdentityOverride(n)
	if err != nil {
		return execbackend.Backend{}, fmt.Errorf("openrouter backend config: %w", err)
	}
	if err := identity.ValidateBackendOverride(ov); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openrouter backend config: %w", err)
	}

	staticReferer, err := normalizeLegacyStaticReferer(y.StaticReferer)
	if err != nil {
		return execbackend.Backend{}, err
	}
	staticTitle, err := normalizeLegacyStaticTitle(y.StaticTitle)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if err := rejectOpenRouterLegacyConflicts(ov, staticReferer, staticTitle); err != nil {
		return execbackend.Backend{}, err
	}

	g := idCfg
	if err := identity.Validate(&g); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openrouter backend config: %w", err)
	}
	eff := identity.MergeUpstream(g, ov)
	httpClient := httpidentity.WrapClient(resolveUpstreamHTTP(upstream), eff.UserAgent)

	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://openrouter.ai/api/v1")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.OpenRouter)
	cfg := openrouter.Config{
		BaseURL:     base,
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  httpClient,
		AppURL:      eff.AppURL,
		AppTitle:    eff.AppTitle,
	}
	// Legacy static_* is an explicit backend compatibility override for that
	// carrier when the corresponding new backend identity field is absent.
	if staticReferer != "" && (ov == nil || ov.OpenRouter == nil || ov.OpenRouter.AppURL == nil) {
		cfg.LegacyAppURL = true
		cfg.StaticReferer = staticReferer
	}
	if staticTitle != "" && (ov == nil || ov.OpenRouter == nil || ov.OpenRouter.AppTitle == nil) {
		cfg.LegacyAppTitle = true
		cfg.StaticTitle = staticTitle
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(openrouter.New(cfg), y.Models)
}

func normalizeLegacyStaticReferer(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if accepted, ok := identity.AcceptClientAppURL(v); ok {
		return accepted, nil
	}
	return "", fmt.Errorf("openrouter backend config: static_referer: invalid http(s) URL, control characters, or exceeds %d bytes", identity.MaxAppURLBytes)
}

func normalizeLegacyStaticTitle(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if accepted, ok := identity.AcceptClientAppTitle(v); ok {
		return accepted, nil
	}
	return "", fmt.Errorf("openrouter backend config: static_title: invalid title, control characters, or exceeds %d bytes", identity.MaxAppTitleBytes)
}

func rejectOpenRouterLegacyConflicts(ov *identity.BackendOverride, staticReferer, staticTitle string) error {
	if ov == nil || ov.OpenRouter == nil {
		return nil
	}
	if staticReferer != "" && ov.OpenRouter.AppURL != nil {
		return fmt.Errorf("openrouter backend config: static_referer conflicts with identity.openrouter.app_url; configure only one")
	}
	if staticTitle != "" && ov.OpenRouter.AppTitle != nil {
		return fmt.Errorf("openrouter backend config: static_title conflicts with identity.openrouter.app_title; configure only one")
	}
	return nil
}
