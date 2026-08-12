package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

	// CompatibleModeAuth enables optional credentials for built-in compatible
	// modes: an empty resolved key set proceeds without Authorization headers.
	// Native hosted backends must leave this false (dummy-credential policy).
	CompatibleModeAuth bool

	ClientOptions  func(lipapi.Call, routing.AttemptCandidate) []option.RequestOption
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
	pool, noAuth, err := buildCompatibleOrRequiredPool(spec)
	if err != nil {
		return newConfigErrorBackend(spec.ID, fmt.Errorf("%s: credentials: %w", spec.ID, err))
	}
	prefixes := []string{spec.ID}
	return execbackend.Backend{
		Caps:          openaicaps.HostedFull,
		ReplaySupport: lipapi.ReasoningReplaySupport{},
		ResolveReplaySupport: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.ReasoningReplaySupport {
			model := resolveModel(spec, cand, call)
			flavor := openaicaps.FlavorChat
			if resolveFlavor(spec, call) == FlavorResponses {
				flavor = openaicaps.FlavorResponses
			}
			return openaicaps.ResolveCompatibleReplaySupport(flavor, model, prefixes)
		},
		TransportCaps:                        hostedTransportCaps(),
		BackendPrefixes:                      prefixes,
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: execbackend.IgnoresClampViaCodexUnsupportedGenParams,
		ModelInventory:                       spec.Inventory,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModelCompatibleReplay(resolveModel(spec, cand, call), prefixes)
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
			if noAuth {
				return openOnce(ctx, spec, req, call, cand, "")
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
				es, openErr := openOnce(ctx, spec, req, call, cand, cred.Secret)
				if openErr == nil {
					return es, nil
				}
				// openOnce returns either a prepended stream or a raw open/recv error.
				kind, retryAfter := openaicred.ClassifyOpenAIAPIError(openErr)
				now = time.Now()
				switch kind {
				case openaicred.FailureAuthInvalid:
					pool.MarkAuthInvalid(cred.ID)
				case openaicred.FailureRateLimited:
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, spec.RateLimitFallback)
					pool.MarkRateLimited(cred.ID, until)
				case openaicred.FailureRetryable:
					// Open/first-Recv failed before the stream was returned: still pre-output,
					// so a transient upstream/transport failure is a core failover candidate.
					// openErr already carries this backend's ID prefix from the stream layer.
					return nil, lipapi.RecoverablePreOutputError(openErr)
				default:
					return nil, openErr
				}
			}
		},
	}
}

func buildCompatibleOrRequiredPool(spec BackendSpec) (*credpool.Pool, bool, error) {
	if !spec.CompatibleModeAuth {
		pool, err := openaicred.NewPoolFromCredentials(spec.APIKey, spec.APIKeys, spec.Credentials)
		return pool, false, err
	}
	creds, err := optionalCompatibleCredentials(spec.APIKey, spec.APIKeys, spec.Credentials)
	if err != nil {
		return nil, false, err
	}
	if len(creds) == 0 {
		return nil, true, nil
	}
	pool, err := credpool.New(creds)
	return pool, false, err
}

func optionalCompatibleCredentials(apiKey string, apiKeys []string, credentials []credpool.Credential) ([]credpool.Credential, error) {
	if len(credentials) > 0 {
		out := make([]credpool.Credential, 0, len(credentials))
		for i, c := range credentials {
			if c.Secret == "" {
				return nil, fmt.Errorf("credpool: empty secret at index %d", i)
			}
			out = append(out, c)
		}
		return out, nil
	}
	seen := make(map[string]struct{})
	out := make([]credpool.Credential, 0, 1+len(apiKeys))
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, credpool.Credential{Secret: s})
	}
	add(apiKey)
	for _, k := range apiKeys {
		add(k)
	}
	return out, nil
}

func openOnce(ctx context.Context, spec BackendSpec, req InvokeRequest, call lipapi.Call, cand routing.AttemptCandidate, apiSecret string) (lipapi.ManagedEventStream, error) {
	var clientOptions []option.RequestOption
	if spec.ClientOptions != nil {
		clientOptions = spec.ClientOptions(call, cand)
	}
	cli := openaicred.NewClientWithOptions(spec.BaseURL, apiSecret, spec.HTTPClient, spec.SDKMaxRetries, clientOptions)

	var es lipapi.ManagedEventStream
	var openErr error
	switch resolveFlavor(spec, call) {
	case FlavorResponses:
		es, openErr = OpenResponses(ctx, cli, req)
	default:
		es, openErr = OpenChat(ctx, cli, req)
	}
	if openErr != nil {
		return nil, openErr
	}
	ev, rerr := es.Recv(ctx)
	if rerr == nil {
		return streampeek.NewManagedPrependFirst(ev, es), nil
	}
	return nil, errors.Join(rerr, es.Close())
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
