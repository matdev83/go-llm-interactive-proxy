package openaicred

import "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"

// NewPool builds a [credpool.Pool] from backend config fields.
func NewPool(apiKey string, apiKeys []string) (*credpool.Pool, error) {
	return credpool.NewPoolFromBackendKeys(apiKey, apiKeys)
}
