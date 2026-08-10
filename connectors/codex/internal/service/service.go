package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/appserver"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/codex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Service struct {
	Starter acp.ProcessStarter
}

func containsFeature(features []string, want string) bool {
	for _, feature := range features {
		if feature == want {
			return true
		}
	}
	return false
}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	httpCaps := backendplugin.CapabilitySummary{
		Streaming: true, Tools: true, Vision: true, Documents: true,
		ParallelToolCalls: true, Reasoning: true, ReasoningReplay: true,
	}
	appCaps := backendplugin.CapabilitySummary{
		Streaming: true, Tools: true, Vision: true, Reasoning: true,
	}
	transport := backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true}
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorAccountingEvidence,
		PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts, Required: true},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence, Required: true},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKindHTTP, DisplayName: "OpenAI Codex", Description: "OpenAI Codex Responses backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeLocalOnly,
			RoutePrefixes: []string{FactoryKindHTTP}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance, StaticCapabilities: httpCaps,
			TransportCapabilities: transport,
		}, {
			Kind: FactoryKindAppServer, DisplayName: "Codex App Server", Description: "Codex CLI app-server backend",
			CredentialMode: backendplugin.CredentialModeNone, AccessScope: backendplugin.AccessScopeLocalOnly,
			RoutePrefixes: []string{FactoryKindAppServer}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance, StaticCapabilities: appCaps,
			TransportCapabilities: transport,
		}},
	}, nil
}

func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	kind := req.FactoryKind
	if kind == "" {
		kind = FactoryKindHTTP
	}
	cfg, err := ParseConfigYAML(kind, req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	cat, src, err := catalog.Load(ctx, cfg.catalogLoadOptions())
	if err != nil {
		return nil, fmt.Errorf("codex connector: catalog: %w", err)
	}
	switch kind {
	case FactoryKindHTTP:
		if secret := strings.TrimSpace(string(req.Secrets.Values["access_token"])); secret != "" {
			cfg.AccessToken = secret
		}
		if strings.TrimSpace(cfg.AccessToken) == "" {
			return nil, fmt.Errorf("codex connector: access_token is required")
		}
		pc, err := cfg.toCodexHTTP(cat)
		if err != nil {
			return nil, err
		}
		pc.DisableNativeCompactionWithoutAccounting = !containsFeature(req.Negotiation.EnabledFeatures, backendplugin.FeatureAccountingEvidence)
		eng, err := codex.New(pc)
		if err != nil {
			return nil, err
		}
		return &instance{kind: kind, http: eng, catalogSrc: src}, nil
	case FactoryKindAppServer:
		cache := &acp.ExecutableCache{}
		pc, err := cfg.toAppServer(cat, src, cache)
		if err != nil {
			return nil, err
		}
		var eng *appserver.Engine
		if s != nil && s.Starter != nil {
			eng = appserver.NewWithStarter(pc, s.Starter)
		} else {
			eng, err = appserver.New(pc)
			if err != nil {
				return nil, err
			}
		}
		if tracking, ok := eng.Inventory().(interface {
			AcceptInventory([]modelinventory.Model)
			LoadModels(context.Context) (modelinventory.Snapshot, error)
		}); ok {
			snap, loadErr := tracking.LoadModels(context.Background())
			if loadErr == nil && len(snap.Models) > 0 {
				tracking.AcceptInventory(snap.Models)
			}
		}
		return &instance{kind: kind, app: eng, catalogSrc: src, exeCache: cache}, nil
	default:
		return nil, fmt.Errorf("codex connector: unexpected factory kind %q", kind)
	}
}

type instance struct {
	kind       string
	http       *codex.Engine
	app        *appserver.Engine
	catalogSrc catalog.Source
	exeCache   *acp.ExecutableCache
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	caps := backendplugin.CapabilitySummary{Streaming: true, Reasoning: true}
	if i.kind == FactoryKindHTTP {
		caps = backendplugin.CapabilitySummary{
			Streaming: true, Tools: true, Vision: true, Documents: true,
			ParallelToolCalls: true, Reasoning: true, ReasoningReplay: true, Compaction: true,
		}
	} else {
		caps = backendplugin.CapabilitySummary{Streaming: true, Tools: true, Vision: true, Reasoning: true}
	}
	profile := backendplugin.ResolvedProfile{
		Capabilities:             caps,
		ReasoningReplaySupported: i.kind == FactoryKindHTTP,
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}
	if i.kind == FactoryKindHTTP {
		profile.DialectSupport.CompactionDialects = []backendplugin.DialectRequirementDTO{{Dialect: "codex.responses.compaction.v2", Implementor: "openai-codex"}}
	}
	return profile, nil
}

func (i *instance) Close(context.Context) error {
	if i.app != nil {
		return i.app.Close()
	}
	if i.http != nil {
		return i.http.Close()
	}
	return nil
}
