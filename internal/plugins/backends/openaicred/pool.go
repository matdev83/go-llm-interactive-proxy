package openaicred

import "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"

// NewPool builds a [credpool.Pool] from backend config fields. A nil clock uses time.Now inside credpool.
func NewPool(apiKey string, apiKeys []string, clock credpool.Clock) (*credpool.Pool, error) {
	return credpool.NewPoolFromBackendKeys(apiKey, apiKeys, clock)
}
