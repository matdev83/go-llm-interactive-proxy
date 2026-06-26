package pluginreg

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	"gopkg.in/yaml.v3"
)

type openAICodexBackendYAML struct {
	openAIStyleYAML                       `yaml:",inline"`
	AccessToken                           string   `yaml:"access_token"`
	RefreshToken                          string   `yaml:"refresh_token"`
	AccountID                             string   `yaml:"account_id"`
	AuthJSONPath                          string   `yaml:"auth_json_path"`
	OAuthTokenURL                         string   `yaml:"oauth_token_url"`
	OAuthClientID                         string   `yaml:"oauth_client_id"`
	DefaultReasoningEffort                string   `yaml:"default_reasoning_effort"`
	ManagedOAuthEnabled                   bool     `yaml:"managed_oauth_enabled"`
	ManagedOAuthStoragePath               string   `yaml:"managed_oauth_storage_path"`
	ManagedOAuthAccounts                  []string `yaml:"managed_oauth_accounts"`
	ManagedOAuthSelectionStrategy         string   `yaml:"managed_oauth_selection_strategy"`
	ManagedOAuthAllowAuthJSONFallback     bool     `yaml:"managed_oauth_allow_auth_json_fallback"`
	ManagedOAuthSessionAffinityTTLSeconds int      `yaml:"managed_oauth_session_affinity_ttl_seconds"`
	ManagedOAuthSessionAffinityMaxEntries int      `yaml:"managed_oauth_session_affinity_max_entries"`
	RateLimitFallbackSeconds              int      `yaml:"rate_limit_fallback_seconds"`
	GPT55DowngradeDisabled                bool     `yaml:"gpt55_downgrade_disabled"`
	GPT55DowngradeSourceModel             string   `yaml:"gpt55_downgrade_source_model"`
	GPT55DowngradeTargetModel             string   `yaml:"gpt55_downgrade_target_model"`
	PlanTypeHint                          string   `yaml:"plan_type_hint"`
}

func backendOpenAICodex(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openAICodexBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openai-codex backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), openaicodex.DefaultBaseURL)
	models, err := openAICodexModelIDsFromYAML(y.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	primary := cmp.Or(strings.TrimSpace(y.AccessToken), strings.TrimSpace(y.APIKey))
	keysResolved := inventoryAPIKeys(primary, y.APIKeys, y.Credentials, keys.OpenAICodex)
	accessToken := ""
	if len(keysResolved) > 0 {
		accessToken = keysResolved[0]
	}
	cfg := openaicodex.Config{
		BaseURL:                           base,
		AccessToken:                       accessToken,
		RefreshToken:                      strings.TrimSpace(y.RefreshToken),
		AccountID:                         strings.TrimSpace(y.AccountID),
		AuthJSONPath:                      strings.TrimSpace(y.AuthJSONPath),
		OAuthTokenURL:                     strings.TrimSpace(y.OAuthTokenURL),
		OAuthClientID:                     strings.TrimSpace(y.OAuthClientID),
		HTTPClient:                        resolveUpstreamHTTP(upstream),
		Models:                            models,
		DefaultReasoningEffort:            strings.TrimSpace(y.DefaultReasoningEffort),
		ManagedOAuthEnabled:               y.ManagedOAuthEnabled,
		ManagedOAuthStoragePath:           strings.TrimSpace(y.ManagedOAuthStoragePath),
		ManagedOAuthAccounts:              y.ManagedOAuthAccounts,
		ManagedOAuthSelectionStrategy:     strings.TrimSpace(y.ManagedOAuthSelectionStrategy),
		ManagedOAuthAllowAuthJSONFallback: y.ManagedOAuthAllowAuthJSONFallback,
	}
	if y.ManagedOAuthSessionAffinityTTLSeconds > 0 {
		cfg.ManagedOAuthSessionAffinityTTL = time.Duration(y.ManagedOAuthSessionAffinityTTLSeconds) * time.Second
	}
	if y.ManagedOAuthSessionAffinityMaxEntries > 0 {
		cfg.ManagedOAuthSessionAffinityMaxEntries = y.ManagedOAuthSessionAffinityMaxEntries
	}
	if y.RateLimitFallbackSeconds > 0 {
		cfg.RateLimitFallback = time.Duration(y.RateLimitFallbackSeconds) * time.Second
	}
	cfg.GPT55DowngradeDisabled = y.GPT55DowngradeDisabled
	cfg.GPT55DowngradeSourceModel = strings.TrimSpace(y.GPT55DowngradeSourceModel)
	cfg.GPT55DowngradeTargetModel = strings.TrimSpace(y.GPT55DowngradeTargetModel)
	cfg.PlanTypeHint = strings.TrimSpace(y.PlanTypeHint)
	return applyConfiguredModelInventory(openaicodex.New(cfg), y.Models)
}

func openAICodexModelIDsFromYAML(y modelInventoryYAML) ([]string, error) {
	rows, _, ok, err := modelInventoryRows(y, true)
	if err != nil || !ok {
		return nil, err
	}
	prefix := openaicodex.ID + "/"
	ids := make([]string, 0, len(rows))
	for i, row := range rows {
		raw := strings.TrimSpace(row.NativeID)
		switch {
		case strings.HasPrefix(raw, prefix):
			raw = strings.TrimPrefix(raw, prefix)
		case raw == "":
			canonical := strings.TrimSpace(row.CanonicalID)
			if canonical == "" {
				return nil, fmt.Errorf("backend models: item[%d] requires native_id or canonical_id", i)
			}
			if idx := strings.LastIndex(canonical, "/"); idx >= 0 {
				raw = canonical[idx+1:]
			} else {
				raw = canonical
			}
		}
		if raw == "" {
			return nil, fmt.Errorf("backend models: item[%d] requires a model id", i)
		}
		ids = append(ids, raw)
	}
	return ids, nil
}
