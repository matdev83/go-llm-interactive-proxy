package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicaps"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"github.com/openai/openai-go/v3/option"
)

type Flavor string

const (
	FlavorChat      Flavor = "chat"
	FlavorResponses Flavor = "responses"
)

type BackendSpec struct {
	ID                string
	BaseURL           string
	APIKey            string
	APIKeys           []string
	Credentials       []credpool.Credential
	HTTPClient        *http.Client
	SDKMaxRetries     *int
	RateLimitFallback time.Duration

	ClientOptions  func(lipapi.Call) []option.RequestOption
	RequestOptions func(lipapi.Call) []option.RequestOption
	ResolveModel   func(routing.AttemptCandidate, lipapi.Call) string
	ResolveFlavor  func(lipapi.Call) Flavor
	Inventory      modelinventory.Provider
}

func HostedCaps() lipapi.BackendCaps {
	return openaicaps.HostedFull
}

func NewBackend(spec BackendSpec) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(spec.ID, "base_url", spec.BaseURL); err != nil {
		return newConfigErrorBackend(spec.ID, err)
	}
	pool, err := openaicred.NewPoolFromCredentials(spec.APIKey, spec.APIKeys, spec.Credentials)
	if err != nil {
		return newConfigErrorBackend(spec.ID, fmt.Errorf("%s: credentials: %w", spec.ID, err))
	}
	return execbackend.Backend{
		Caps:            openaicaps.HostedFull,
		TransportCaps:   hostedTransportCaps(),
		BackendPrefixes: []string{spec.ID},
		ModelInventory:  spec.Inventory,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModel(resolveModel(spec, cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", spec.ID, lipapi.ErrNilContext)
			}
			req := InvokeRequest{
				ProviderID: spec.ID,
				Call:       call,
				Candidate:  cand,
			}
			if spec.RequestOptions != nil {
				req.SDKOptions = spec.RequestOptions(call)
			}
			now := time.Now()
			for {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				cred, aerr := pool.Acquire(now, nil)
				if aerr != nil {
					if errors.Is(aerr, credpool.ErrNoUsableCredential) {
						return nil, lipapi.RecoverablePreOutputError(aerr)
					}
					return nil, fmt.Errorf("%s: %w", spec.ID, aerr)
				}
				var clientOptions []option.RequestOption
				if spec.ClientOptions != nil {
					clientOptions = spec.ClientOptions(call)
				}
				cli := openaicred.NewOpenAIClientWithOptions(spec.BaseURL, cred.Secret, spec.HTTPClient, spec.SDKMaxRetries, clientOptions)

				var es lipapi.ManagedEventStream
				var openErr error
				switch resolveFlavor(spec, call) {
				case FlavorResponses:
					es, openErr = OpenResponses(ctx, cli, req)
				default:
					es, openErr = OpenChat(ctx, cli, req)
				}
				if openErr == nil {
					ev, rerr := es.Recv(ctx)
					if rerr == nil {
						return streampeek.NewManagedPrependFirst(ev, es), nil
					}
					openErr = errors.Join(rerr, es.Close())
				}

				kind, retryAfter := openaicred.ClassifyOpenAIAPIError(openErr)
				now = time.Now()
				switch kind {
				case openaicred.FailureAuthInvalid:
					pool.MarkAuthInvalid(cred.ID)
				case openaicred.FailureRateLimited:
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, spec.RateLimitFallback)
					pool.MarkRateLimited(cred.ID, until)
				default:
					return nil, openErr
				}
			}
		},
	}
}

func newConfigErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            openaicaps.HostedFull,
		TransportCaps:   hostedTransportCaps(),
		BackendPrefixes: []string{id},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.HostedFull
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}

func hostedTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIResponses,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}

func resolveModel(spec BackendSpec, cand routing.AttemptCandidate, call lipapi.Call) string {
	if spec.ResolveModel == nil {
		return cand.Primary.Model
	}
	return spec.ResolveModel(cand, call)
}

func resolveFlavor(spec BackendSpec, call lipapi.Call) Flavor {
	if spec.ResolveFlavor == nil {
		return FlavorChat
	}
	return spec.ResolveFlavor(call)
}
