package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type Option func(*Service)

func WithTokenProviderFactory(f TokenProviderFactory) Option {
	return func(s *Service) {
		s.tokenProviderFactory = f
	}
}

func WithTokenProvider(tp TokenProvider) Option {
	return func(s *Service) {
		s.tokenProvider = tp
	}
}

func WithHTTPClient(hc *http.Client) Option {
	return func(s *Service) {
		s.httpClient = hc
	}
}

type Service struct {
	tokenProviderFactory TokenProviderFactory
	tokenProvider        TokenProvider
	httpClient           *http.Client
}

func New(opts ...Option) *Service {
	s := &Service{}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func NewProduction(opts ...Option) *Service {
	s := &Service{
		tokenProviderFactory: DefaultIAMTokenProviderFactory,
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

func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("watsonx: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	var tp TokenProvider
	if s.tokenProvider != nil {
		tp = s.tokenProvider
	} else if s.tokenProviderFactory != nil {
		t, err := s.tokenProviderFactory(ctx, cfg, req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("watsonx: create token provider: %w", err)
		}
		tp = t
	} else {
		return nil, fmt.Errorf("watsonx: requires token provider or IAM credentials (use NewProduction)")
	}

	hc := s.httpClient
	if hc == nil {
		c, err := cfg.HTTPClient()
		if err != nil {
			return nil, err
		}
		hc = c
	}

	return &instance{
		cfg: cfg,
		tp:  tp,
		hc:  hc,
	}, nil
}

type instance struct {
	cfg Config
	tp  TokenProvider
	hc  *http.Client
}

func (i *instance) client() *Client {
	return &Client{
		Config:        i.cfg,
		TokenProvider: i.tp,
		HTTPClient:    i.hc,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           FactoryKind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	return i.client().ListModels(ctx, limit)
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	cl := i.client()
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
		m := strings.TrimSpace(inv.CanonicalModelID)
		if m == "" || m == FactoryKind {
			if i.cfg.ModelID != "" {
				m = i.cfg.ModelID
			} else if i.cfg.DeploymentID != "" {
				m = "deployment/" + i.cfg.DeploymentID
			} else {
				return nil, fmt.Errorf("watsonx: model is required (set model_id or deployment_id in config or provide model in request)")
			}
		} else {
			if after, ok := strings.CutPrefix(m, FactoryKind+"/"); ok {
				m = after
			}
		}
		if m == "" {
			return nil, fmt.Errorf("watsonx: model is required (set model_id or deployment_id in config or provide model in request)")
		}
		return cl.Open(ctx, call, m)
	})
}
