package standardplugins

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/alibabatokenplanintl"
	"gopkg.in/yaml.v3"
)

// alibabaTokenPlanIntlYAML is the config-file schema for the backend. API key
// fields are intentionally present only so they can be rejected: credentials
// must be supplied exclusively via the ALIBABA_TOKEN_PLAN_API_KEY environment
// variable.
type alibabaTokenPlanIntlYAML struct {
	BaseURL       string                 `yaml:"base_url"`
	Models        modelInventoryYAML     `yaml:"models"`
	SDKMaxRetries *int                   `yaml:"sdk_max_retries"`
	APIKey        string                 `yaml:"api_key"`
	APIKeys       []string               `yaml:"api_keys"`
	Credentials   []hostedCredentialYAML `yaml:"credentials"`
}

// backendAlibabaTokenPlanIntl constructs the Alibaba Token Plan (international)
// backend. Credentials are accepted only from the resolved UpstreamAPIKeys
// (ALIBABA_TOKEN_PLAN_API_KEY); any API key material in YAML is a hard error.
func backendAlibabaTokenPlanIntl(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys, idCfg identity.Config) (execbackend.Backend, error) {
	var y alibabaTokenPlanIntlYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("%s backend config: %w", alibabatokenplanintl.ID, err)
	}
	if strings.TrimSpace(y.APIKey) != "" || len(y.APIKeys) > 0 || len(y.Credentials) > 0 {
		return execbackend.Backend{}, fmt.Errorf("%s backend config: credentials must be supplied only through ALIBABA_TOKEN_PLAN_API_KEY", alibabatokenplanintl.ID)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), alibabatokenplanintl.DefaultBaseURL)
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, alibabatokenplanintl.ID+" backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	var primaryKey string
	if len(keys.AlibabaTokenPlan) > 0 {
		primaryKey = keys.AlibabaTokenPlan[0]
	}
	cfg := alibabatokenplanintl.Config{
		BaseURL:       base,
		APIKey:        primaryKey,
		APIKeys:       keys.AlibabaTokenPlan,
		HTTPClient:    httpClient,
		SDKMaxRetries: sdkMaxRetriesOrDefault(y.SDKMaxRetries),
	}
	return applyConfiguredModelInventory(alibabatokenplanintl.New(cfg), y.Models)
}
