package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/catalog/vendor"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/upstream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: 0, PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKindGo, DisplayName: "OpenCode Go", Description: "OpenCode Go multi-vendor backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKindGo}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true, Tools: true, Vision: true, Documents: true, ParallelToolCalls: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation: true, BidirectionalStream: true,
			},
		}, {
			Kind: FactoryKindZen, DisplayName: "OpenCode Zen", Description: "OpenCode Zen multi-vendor backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKindZen}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true, Tools: true, Vision: true, Documents: true, ParallelToolCalls: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation: true, BidirectionalStream: true,
			},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	kind := req.FactoryKind
	if kind == "" {
		kind = FactoryKindGo
	}
	if kind != FactoryKindGo && kind != FactoryKindZen {
		return nil, fmt.Errorf("opencode: unexpected factory kind %q", kind)
	}
	cfg, err := ParseConfigYAML(kind, req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	if secret := strings.TrimSpace(string(req.Secrets.Values["api_key"])); secret != "" {
		cfg.APIKey = secret
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("opencode: api_key is required")
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	backendKind := catalogBackendKind(kind)
	resolver := vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)
	source := catalog.NewModelSource(backendKind, catalog.ModelLoaderConfig{
		BaseURL: cfg.BaseURL, Kind: backendKind, APIKey: cfg.APIKey, HTTPClient: hc,
	}, cfg.Models, resolver)
	return &instance{
		cfg: cfg, hc: hc, kind: kind, backendKind: backendKind,
		source: source, router: upstream.NewRouter(backendKind, cfg.BaseURL, cfg.APIKey, hc),
	}, nil
}

func catalogBackendKind(kind string) catalog.BackendKind {
	switch kind {
	case FactoryKindZen:
		return catalog.BackendZen
	default:
		return catalog.BackendGo
	}
}

type instance struct {
	cfg         Config
	hc          *http.Client
	kind        string
	backendKind catalog.BackendKind
	source      *catalog.ModelSource
	router      *upstream.Router
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{
			Streaming: true, Tools: true, Vision: true, Documents: true, ParallelToolCalls: true,
		},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{
			Cancellation: true, BidirectionalStream: true,
		},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }
