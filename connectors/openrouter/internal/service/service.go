package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: 0, PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKind, DisplayName: "OpenRouter", Description: "OpenRouter OpenAI-compatible backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKind}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("openrouter: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	if secret := strings.TrimSpace(string(req.Secrets.Values["api_key"])); secret != "" {
		cfg.APIKey = secret
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("openrouter: api_key is required")
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc}, nil
}

type instance struct {
	cfg Config
	hc  *http.Client
}

func (i *instance) client() *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc)
}

// NewCompatClient builds the OpenAI-compatible client with OpenRouter hooks (tests).
func NewCompatClient(cfg Config, hc *http.Client) *openaicompat.Client {
	inst := &instance{cfg: cfg, hc: hc}
	return &openaicompat.Client{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, HTTPClient: hc,
		Transport: openaicompat.TransportChatAndResponses,
		Hooks: openaicompat.RequestHooks{
			PrepareHeaders: inst.prepareHeaders,
			MutateBody:     inst.mutateBody,
		},
	}
}

func (i *instance) prepareHeaders(h http.Header, call lipapi.Call, model string, flavor openaicompat.Flavor) {
	i.applyAttribution(h, call)
	_ = model
	_ = flavor
}

func (i *instance) applyAttribution(h http.Header, call lipapi.Call) {
	referer := resolveAppURL(i.cfg, extString(call, extHTTPReferer))
	if referer != "" {
		h.Set("HTTP-Referer", referer)
	}
	title := resolveAppTitle(i.cfg, extString(call, extTitle))
	if title != "" {
		h.Set("X-OpenRouter-Title", title)
	}
	if v := extString(call, extCategories); v != "" {
		h.Set("X-OpenRouter-Categories", v)
	}
	if v := extString(call, extMetadataHeader); v != "" {
		h.Set("X-OpenRouter-Metadata", v)
	}
}

func (i *instance) mutateBody(body map[string]any, call lipapi.Call, model string, flavor openaicompat.Flavor) error {
	_ = model
	_ = flavor
	return applyOpenRouterBody(body, call)
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind}, EvidenceSource: FactoryKind, ProfileVersion: "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	models, err := i.client().ListModels(ctx, limit)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(models))
	for _, m := range models {
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + m.ID, NativeModelID: m.ID, FactoryKind: FactoryKind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: FactoryKind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	cl := i.client()
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel: "openrouter/auto",
		ResolveModel: func(inv backendplugin.Invocation, _ lipapi.Call) string {
			return strings.TrimPrefix(strings.TrimSpace(inv.CanonicalModelID), FactoryKind+"/")
		},
		ResolveFlavor: resolveFlavor,
		Open:          cl.Open,
	})
}

func resolveFlavor(call lipapi.Call) openaicompat.Flavor {
	if extString(call, extUpstreamFlavor) == "responses" {
		return openaicompat.FlavorResponses
	}
	if call.Invocation.Operation == lipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}
