package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

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
			Kind: FactoryKind, DisplayName: "Ollama", Description: "Local Ollama OpenAI-compatible backend",
			CredentialMode: backendplugin.CredentialModeNone, AccessScope: backendplugin.AccessScopeLocalOnly,
			RoutePrefixes: []string{FactoryKind}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}, {
			Kind: FactoryKindCloud, DisplayName: "Ollama Cloud", Description: "Ollama Cloud OpenAI-compatible backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKindCloud}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	kind := req.FactoryKind
	if kind == "" {
		kind = FactoryKind
	}
	if kind != FactoryKind && kind != FactoryKindCloud {
		return nil, fmt.Errorf("ollama: unexpected factory kind %q", kind)
	}
	cfg, err := ParseConfigYAML(kind, req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	if secret := strings.TrimSpace(string(req.Secrets.Values["api_key"])); secret != "" {
		cfg.APIKey = secret
	}
	if kind == FactoryKindCloud && cfg.APIKey == "" {
		return nil, fmt.Errorf("ollama-cloud: api_key is required")
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, kind: kind}, nil
}

type instance struct {
	cfg  Config
	hc   *http.Client
	kind string
}

func (i *instance) client() *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc, providerHooks())
}

func NewCompatClient(cfg Config, hc *http.Client, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, HTTPClient: hc,
		Transport: openaicompat.TransportChatAndResponses, Hooks: hooks,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind}, EvidenceSource: i.kind, ProfileVersion: "1",
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	cl := i.client()
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel:  "default",
		ResolveModel:  func(inv backendplugin.Invocation, call lipapi.Call) string { return resolveModel(i.kind, inv, call) },
		ResolveFlavor: resolveFlavor,
		Open:          cl.Open,
	})
}
