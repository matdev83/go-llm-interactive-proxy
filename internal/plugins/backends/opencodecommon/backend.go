package opencodecommon

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/geminigenerate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type BackendConfig struct {
	Kind              BackendKind
	BaseURL           string
	APIKey            string
	APIKeys           []string
	Credentials       []credpool.Credential
	HTTPClient        *http.Client
	SDKMaxRetries     *int
	Models            []ModelEntry
	RateLimitFallback time.Duration
	VendorResolver    VendorResolver
}

func BackendID(kind BackendKind) string {
	return string(kind)
}

func NewBackend(cfg BackendConfig) execbackend.Backend {
	id := BackendID(cfg.Kind)
	if err := checkcfg.RequireNonEmpty(id, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(id, err)
	}
	if _, err := credpool.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials); err != nil {
		return newConfigErrorBackend(id, fmt.Errorf("%s: credentials: %w", id, err))
	}
	staticEntries := cfg.Models
	source := catalog.NewModelSource(cfg.Kind, catalog.ModelLoaderConfig{
		BaseURL:     cfg.BaseURL,
		Kind:        cfg.Kind,
		APIKey:      cfg.APIKey,
		APIKeys:     cfg.APIKeys,
		Credentials: credpool.Secrets(cfg.Credentials),
		HTTPClient:  cfg.HTTPClient,
	}, staticEntries, cfg.VendorResolver)
	delegate := &flavorDelegate{
		id:    id,
		cfg:   cfg,
		cache: make(map[string]execbackend.Backend),
	}
	inventory := catalog.NewInventoryProviderFromSource(source)
	transportCaps := TransportCaps()

	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		TransportCaps:   transportCaps,
		BackendPrefixes: []string{id},
		ModelInventory:  inventory,
		ResolveCaps: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			resolved, err := source.Resolve(ctx, modelFromCandidate(cand))
			if err != nil {
				return defaultBackendCaps()
			}
			return capsForFlavor(resolved.Flavor)
		},
		ResolveTransportCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendTransportCaps {
			return transportCaps
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", id, lipapi.ErrNilContext)
			}
			resolved, err := source.Resolve(ctx, modelFromCandidate(cand))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
			baseURL := EndpointBaseURL(resolved.Entry, cfg.BaseURL, resolved.Flavor)
			be := delegate.backend(resolved.Flavor, baseURL)
			cand.Primary.Model = resolved.WireModel
			return be.Open(ctx, call, cand)
		},
	}
}

type flavorDelegate struct {
	mu    sync.Mutex
	id    string
	cfg   BackendConfig
	cache map[string]execbackend.Backend
}

func (d *flavorDelegate) backend(flavor Flavor, baseURL string) execbackend.Backend {
	key := string(flavor) + "|" + baseURL
	d.mu.Lock()
	defer d.mu.Unlock()
	if be, ok := d.cache[key]; ok {
		return be
	}
	be := buildFlavorBackend(d.id, d.cfg, flavor, baseURL)
	d.cache[key] = be
	return be
}

func buildFlavorBackend(id string, cfg BackendConfig, flavor Flavor, baseURL string) execbackend.Backend {
	apiKey := cfg.APIKey
	if apiKey == "" && len(cfg.APIKeys) > 0 {
		apiKey = cfg.APIKeys[0]
	}
	switch flavor {
	case FlavorOpenAIResponses:
		return openaicompat.NewBackend(openaicompat.BackendSpec{
			ID:                id,
			BaseURL:           baseURL,
			APIKey:            apiKey,
			APIKeys:           cfg.APIKeys,
			Credentials:       cfg.Credentials,
			HTTPClient:        cfg.HTTPClient,
			SDKMaxRetries:     cfg.SDKMaxRetries,
			RateLimitFallback: cfg.RateLimitFallback,
			ResolveFlavor:     func(lipapi.Call) openaicompat.Flavor { return openaicompat.FlavorResponses },
		})
	case FlavorAnthropicMessages:
		return anthropicmessages.NewBackend(anthropicmessages.Config{
			BackendID:     id,
			BaseURL:       baseURL,
			APIKey:        apiKey,
			APIKeys:       cfg.APIKeys,
			Credentials:   cfg.Credentials,
			HTTPClient:    cfg.HTTPClient,
			SDKMaxRetries: cfg.SDKMaxRetries,
		})
	case FlavorGoogleGemini:
		return geminigenerate.NewBackend(geminigenerate.Config{
			BackendID:   id,
			BaseURL:     baseURL,
			APIKey:      apiKey,
			APIKeys:     cfg.APIKeys,
			Credentials: cfg.Credentials,
			HTTPClient:  cfg.HTTPClient,
		})
	default:
		return openaicompat.NewBackend(openaicompat.BackendSpec{
			ID:                id,
			BaseURL:           baseURL,
			APIKey:            apiKey,
			APIKeys:           cfg.APIKeys,
			Credentials:       cfg.Credentials,
			HTTPClient:        cfg.HTTPClient,
			SDKMaxRetries:     cfg.SDKMaxRetries,
			RateLimitFallback: cfg.RateLimitFallback,
			ResolveFlavor:     func(lipapi.Call) openaicompat.Flavor { return openaicompat.FlavorChat },
		})
	}
}

func modelFromCandidate(cand routing.AttemptCandidate) string {
	return cand.Primary.Model
}

func defaultBackendCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityTools,
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
		lipapi.CapabilityParallelToolCalls,
	)
}

func capsForFlavor(flavor Flavor) lipapi.BackendCaps {
	switch flavor {
	case FlavorOpenAIResponses, FlavorOpenAIChat:
		return openaicompat.HostedCaps()
	default:
		return defaultBackendCaps()
	}
}

func newConfigErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		TransportCaps:   TransportCaps(),
		BackendPrefixes: []string{id},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendCaps {
			return defaultBackendCaps()
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
