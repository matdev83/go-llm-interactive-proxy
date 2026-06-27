package openaifamily

import (
	"context"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const rateLimitFallback = 60 * time.Second

func New(profile Profile, cfg Config) execbackend.Backend {
	cfg = ApplyDefaults(profile, cfg)
	apiKey, apiKeys, credentials := EffectiveCredentials(profile, cfg)
	resolveModel := profile.ResolveModel
	if resolveModel == nil {
		resolveModel = func(cand routing.AttemptCandidate, call lipapi.Call) string {
			return ResolveModel(profile.ModelResolution, profile.ID, cand, call)
		}
	}
	transportCaps := TransportCaps(profile.Transport)
	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                profile.ID,
		BaseURL:           cfg.BaseURL,
		APIKey:            apiKey,
		APIKeys:           apiKeys,
		Credentials:       credentials,
		HTTPClient:        cfg.HTTPClient,
		SDKMaxRetries:     cfg.SDKMaxRetries,
		RateLimitFallback: rateLimitFallback,
		ClientOptions:     profile.ClientOptions,
		RequestOptions:    profile.RequestOptions,
		ResolveModel:      resolveModel,
		Inventory:         inventoryProvider(profile, cfg, apiKey, apiKeys, credentials),
		ResolveFlavor:     ResolveFlavor,
	})
	be.TransportCaps = transportCaps
	be.ResolveTransportCaps = func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendTransportCaps {
		return transportCaps
	}
	innerOpen := be.Open
	be.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		if profile.Transport == TransportChatOnly && IsResponsesFlavor(call) {
			return nil, fmt.Errorf("%s: responses API is not available", profile.ID)
		}
		if native := resolveModel(cand, call); native != "" {
			cand.Primary.Model = native
		}
		return innerOpen(ctx, call, cand)
	}
	return be
}
