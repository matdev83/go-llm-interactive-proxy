package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"gopkg.in/yaml.v3"
)

// anthropicHostedYAML is the direct Anthropic backend config shape. Cache
// enrollment stays independent from keep-warm orchestration: the default
// disabled value preserves existing foreground request behavior.
type anthropicHostedYAML struct {
	openAIStyleYAML `yaml:",inline"`
	// CacheEnrollment is one of "disabled" or "automatic" (default disabled).
	CacheEnrollment anthropic.CacheEnrollmentMode `yaml:"cache_enrollment"`
	// CacheTTL is valid only for automatic enrollment and must be 5m or 1h.
	CacheTTL string `yaml:"cache_ttl"`
}

func backendAnthropic(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	var y anthropicHostedYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("anthropic backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), anthropic.DefaultBaseURL)
	ek, primaryKey := firstAPIKey(y.APIKey, y.APIKeys, y.Credentials, keys.Anthropic)
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, "anthropic backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	cfg := anthropic.Config{
		BaseURL:         base,
		APIKey:          primaryKey,
		APIKeys:         ek,
		Credentials:     hostedCredentials(y.Credentials),
		HTTPClient:      httpClient,
		SDKMaxRetries:   sdkMaxRetriesOrDefault(y.SDKMaxRetries),
		CacheEnrollment: y.CacheEnrollment,
		CacheTTL:        y.CacheTTL,
	}
	return applyConfiguredModelInventory(anthropic.New(cfg), y.Models)
}
