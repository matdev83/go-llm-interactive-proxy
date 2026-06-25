package opencodezen

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon"
)

const ID = "opencode-zen"

type Config struct {
	BaseURL           string
	APIKey            string
	APIKeys           []string
	Credentials       []credpool.Credential
	HTTPClient        *http.Client
	SDKMaxRetries     *int
	Models            []opencodecommon.ModelEntry
	RateLimitFallback time.Duration
	VendorResolver    opencodecommon.VendorResolver
}

func New(cfg Config) execbackend.Backend {
	return opencodecommon.NewBackend(opencodecommon.BackendConfig{
		Kind:              opencodecommon.BackendZen,
		BaseURL:           cfg.BaseURL,
		APIKey:            cfg.APIKey,
		APIKeys:           cfg.APIKeys,
		Credentials:       cfg.Credentials,
		HTTPClient:        cfg.HTTPClient,
		SDKMaxRetries:     cfg.SDKMaxRetries,
		Models:            cfg.Models,
		RateLimitFallback: cfg.RateLimitFallback,
		VendorResolver:    cfg.VendorResolver,
	})
}
