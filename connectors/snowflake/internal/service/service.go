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

func New() *Service {
	return &Service{}
}

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID:      PluginID,
		Version:       "0.1.0",
		BuildID:       "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     FactoryKind,
			DisplayName:              DisplayName,
			Description:              Description,
			CredentialMode:           backendplugin.CredentialModeStatic,
			AccessScope:              backendplugin.AccessScopeAny,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("snowflake-cortex: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	if cfg.Account == "" {
		return nil, fmt.Errorf("snowflake-cortex: account is required")
	}

	token := strings.TrimSpace(string(req.Secrets.Values["pat"]))
	if token == "" {
		token = strings.TrimSpace(string(req.Secrets.Values["api_key"]))
	}
	if token == "" {
		token = strings.TrimSpace(string(req.Secrets.Values["token"]))
	}
	if token == "" {
		return nil, fmt.Errorf("snowflake-cortex: pat (or api_key) is required in secrets")
	}

	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, token: token, hc: hc, kind: FactoryKind}, nil
}

type instance struct {
	cfg   Config
	token string
	hc    *http.Client
	kind  string
}

func (i *instance) client() *openaicompat.Client {
	return NewCompatClient(i.cfg, i.token, i.hc, ProviderHooks(i.cfg))
}

func NewCompatClient(cfg Config, token string, hc *http.Client, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL:    cfg.BaseURL(),
		APIKey:     token,
		HTTPClient: hc,
		Transport:  openaicompat.TransportChatAndResponses,
		Hooks:      hooks,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	models, err := i.client().ListModels(ctx, 0)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(models))
	for _, m := range models {
		if !isCodingCapableModel(m.ID) {
			continue
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + m.ID,
			NativeModelID:    m.ID,
			FactoryKind:      i.kind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	return backendplugin.ListModelsResponse{
		Models:          out,
		InventorySource: i.kind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	cl := i.client()
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel: "default",
		ResolveModel: func(inv backendplugin.Invocation, call lipapi.Call) string {
			return resolveModel(i.kind, inv, call)
		},
		ResolveFlavor: ResolveFlavor,
		Open:          cl.Open,
	})
}
