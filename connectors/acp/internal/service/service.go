package service

import (
	"context"
	"fmt"
	"net/http"

	acpsupport "github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: 0,
		PluginID:      "io.golip.backend.acp",
		Version:       "0.1.0",
		BuildID:       "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:               FactoryKind,
			DisplayName:        "ACP HTTP",
			Description:        "Agent Client Protocol HTTP JSON-RPC backend",
			CredentialMode:     backendplugin.CredentialModeStatic,
			AccessScope:        backendplugin.AccessScopeLocalOnly,
			RoutePrefixes:      []string{FactoryKind},
			ProcessSharing:     backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true, Vision: true, Documents: true, Reasoning: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation: true, BidirectionalStream: true,
			},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("acp: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, id: req.InstanceID}, nil
}

type instance struct {
	cfg Config
	hc  *http.Client
	id  string
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{Streaming: true, Vision: true, Documents: true, Reasoning: true},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{
			Cancellation: true, BidirectionalStream: true,
		},
		RoutePrefixes:  []string{FactoryKind},
		EvidenceSource: "acp",
		ProfileVersion: "1",
	}, nil
}

func (i *instance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: "acp/agent",
			NativeModelID:    "agent",
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		}},
		InventorySource: "acp",
		FetchedUnixMS:   1,
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, _ backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
		return acpsupport.OpenHTTPPrompt(ctx, acpsupport.Config{
			BaseURL:    i.cfg.BaseURL,
			HTTPClient: i.hc,
		}, call)
	})
}

var (
	_ backendplugin.Service            = (*Service)(nil)
	_ backendplugin.ConfiguredInstance = (*instance)(nil)
)
