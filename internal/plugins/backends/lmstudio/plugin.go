package lmstudio

import (
	"context"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const rateLimitFallback = 60 * time.Second

func New(cfg Config) execbackend.Backend {
	cfg = ApplyDefaults(cfg)
	apiKey, apiKeys, credentials := EffectiveCredentials(cfg)
	catalogEnabled := DiscoveryCatalog(cfg.Discovery)
	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                ID,
		BaseURL:           cfg.BaseURL,
		APIKey:            apiKey,
		APIKeys:           apiKeys,
		Credentials:       credentials,
		HTTPClient:        cfg.HTTPClient,
		SDKMaxRetries:     cfg.SDKMaxRetries,
		RateLimitFallback: rateLimitFallback,
		ResolveModel:      resolveModel,
		Inventory: modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
			BaseURL:           cfg.BaseURL,
			APIKey:            apiKey,
			APIKeys:           apiKeys,
			Credentials:       credpool.Secrets(credentials),
			HTTPClient:        cfg.HTTPClient,
			CanonicalPrefix:   ID,
			PreserveVendorIDs: true,
			Catalog: modeldiscover.CatalogConfig{
				Enabled: &catalogEnabled,
				URL:     cfg.Discovery.CatalogURL,
				Timeout: cfg.Discovery.Timeout,
			},
		},
		ResolveFlavor: func(call lipapi.Call) openaicompat.Flavor {
			if resolveFlavor(call) == openrouterwire.FlavorResponses {
				return openaicompat.FlavorResponses
			}
			return openaicompat.FlavorChat
		},
	})
	transportCaps := chatOnlyTransportCaps()
	be.TransportCaps = transportCaps
	be.ResolveTransportCaps = func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendTransportCaps {
		return transportCaps
	}
	innerOpen := be.Open
	be.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		if resolveFlavor(call) == openrouterwire.FlavorResponses {
			return nil, fmt.Errorf("lmstudio: responses API is not available")
		}
		if native := resolveModel(cand, call); native != "" {
			cand.Primary.Model = native
		}
		return innerOpen(ctx, call, cand)
	}
	return be
}

func chatOnlyTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}
