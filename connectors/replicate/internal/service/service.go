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

func WithHTTPClient(hc *http.Client) Option {
	return func(s *Service) {
		s.httpClient = hc
	}
}

type Service struct {
	tokenProvider        TokenProvider
	tokenProviderFactory TokenProviderFactory
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
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true, Tools: false, Vision: false},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("replicate: unexpected factory kind %q", req.FactoryKind)
	}

	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	var tp TokenProvider
	if s.tokenProvider != nil {
		tp = s.tokenProvider
	} else if s.tokenProviderFactory != nil {
		factoryTP, err := s.tokenProviderFactory(ctx, cfg, req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("replicate: token provider: %w", err)
		}
		tp = factoryTP
	} else {
		return nil, fmt.Errorf("replicate: token provider not configured (use NewProduction or provide TokenProvider/TokenProviderFactory)")
	}

	hc := s.httpClient
	if hc == nil {
		var err error
		hc, err = cfg.HTTPClient()
		if err != nil {
			return nil, err
		}
	}

	return &instance{
		cfg:  cfg,
		tp:   tp,
		hc:   hc,
		kind: FactoryKind,
	}, nil
}

type instance struct {
	cfg  Config
	tp   TokenProvider
	hc   *http.Client
	kind string
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
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true, Tools: false, Vision: false},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	return i.client().ListModels(ctx, limit)
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	cl := i.client()
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
		if call.Invocation.Operation == lipapi.OperationOpenAIResponses ||
			call.Invocation.Operation == lipapi.OperationOpenResponsesCreate {
			return nil, fmt.Errorf("replicate: responses operations are not supported")
		}
		if len(call.Tools) > 0 || len(inv.Tools) > 0 {
			return nil, fmt.Errorf("replicate: tools are not supported")
		}

		reqModel := strings.TrimSpace(inv.CanonicalModelID)
		if reqModel == "" || reqModel == FactoryKind {
			if inv.NativeModelID != "" && inv.NativeModelID != FactoryKind {
				reqModel = inv.NativeModelID
			} else {
				reqModel = i.cfg.Model
			}
		} else {
			if after, ok := strings.CutPrefix(reqModel, FactoryKind+"/"); ok {
				reqModel = after
			}
		}
		reqModel = strings.TrimSpace(reqModel)
		if reqModel == "" || reqModel == FactoryKind {
			reqModel = i.cfg.Model
		}

		configuredParts := strings.Split(i.cfg.Model, "/")
		configuredName := configuredParts[len(configuredParts)-1]
		if reqModel != i.cfg.Model && reqModel != configuredName {
			return nil, fmt.Errorf("replicate: model %q does not match configured model %q", reqModel, i.cfg.Model)
		}

		return cl.Open(ctx, call, i.cfg.Model)
	})
}
