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

type Option func(*Service)

func WithTokenProvider(tp TokenProvider) Option {
	return func(s *Service) {
		s.tokenProvider = tp
	}
}

func WithTokenProviderFactory(f TokenProviderFactory) Option {
	return func(s *Service) {
		s.tokenProviderFactory = f
	}
}

type Service struct {
	tokenProvider        TokenProvider
	tokenProviderFactory TokenProviderFactory
}

// New returns a Service. When opts are omitted, tokenProvider and tokenProviderFactory are unconfigured.
func New(opts ...Option) *Service {
	s := &Service{}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// NewProduction returns a Service wired with the production Microsoft Entra credential chain.
func NewProduction(opts ...Option) *Service {
	s := &Service{
		tokenProviderFactory: DefaultTokenProviderFactory,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
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
		return nil, fmt.Errorf("azure-openai: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	if cfg.Endpoint == "" && cfg.ResourceName == "" {
		return nil, fmt.Errorf("azure-openai: endpoint or resource_name is required")
	}
	if cfg.APIVersion == "" {
		return nil, fmt.Errorf("azure-openai: api_version is required")
	}

	mode := cfg.CredentialMode
	if mode == "" {
		mode = CredentialModeAPIKey
	}

	var apiKey string
	var tp TokenProvider

	switch mode {
	case CredentialModeAPIKey:
		apiKey = strings.TrimSpace(string(req.Secrets.Values["api_key"]))
		if apiKey == "" {
			apiKey = strings.TrimSpace(string(req.Secrets.Values["api-key"]))
		}
		if apiKey == "" {
			return nil, fmt.Errorf("azure-openai: api_key is required in secrets")
		}
	case CredentialModeEntra:
		if s.tokenProvider != nil {
			tp = s.tokenProvider
		} else if s.tokenProviderFactory != nil {
			factoryTP, err := s.tokenProviderFactory(cfg, req.Secrets)
			if err != nil {
				return nil, fmt.Errorf("azure-openai: entra token provider: %w", err)
			}
			tp = factoryTP
		} else {
			return nil, fmt.Errorf("azure-openai: entra mode requires token provider or production credential chain (use NewProduction)")
		}
	default:
		return nil, fmt.Errorf("azure-openai: unsupported credential_mode %q", mode)
	}

	if len(cfg.Deployments) == 0 {
		return nil, errMissingDeployments()
	}

	hc, err := cfg.HTTPClientWithTokenProvider(apiKey, tp)
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, kind: FactoryKind}, nil
}

type instance struct {
	cfg  Config
	hc   *http.Client
	kind string
}

func (i *instance) client() *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc, ProviderHooks(i.cfg))
}

func NewCompatClient(cfg Config, hc *http.Client, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL:    cfg.BaseURL(),
		APIKey:     "",
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

func (i *instance) ListModels(_ context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if len(i.cfg.Deployments) == 0 {
		return backendplugin.ListModelsResponse{}, errMissingDeployments()
	}
	// Deployment-driven inventory: Azure inference routes by deployment name,
	// while /openai/v1/models lists available models, not the deployment-name
	// mapping. Advertising bare model IDs would misroute, so each configured
	// deployment is one routable identity and the models endpoint is not consulted.
	out := make([]backendplugin.ModelDescriptor, 0, len(i.cfg.Deployments))
	for _, name := range sortedDeploymentNames(i.cfg.Deployments) {
		model := i.cfg.Deployments[name]
		if !isResponsesCapableModel(model) {
			continue
		}
		display := name
		if model != name {
			display = name + " (" + model + ")"
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + name,
			NativeModelID:    name,
			DisplayName:      display,
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
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
		model, err := resolveDeployment(i.cfg, i.kind, inv.CanonicalModelID)
		if err != nil {
			return nil, err
		}
		return cl.Open(ctx, call, model, ResolveFlavor(call))
	})
}
