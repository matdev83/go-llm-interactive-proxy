package openrouter

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
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Config configures the OpenRouter backend connector.
// BaseURL should be https://openrouter.ai/api/v1 (no trailing slash).
type Config struct {
	BaseURL string
	APIKey  string
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys.
	Credentials []credpool.Credential
	HTTPClient  *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	SDKMaxRetries *int
	// StaticHeaders are always sent. Per-request headers from Call.Extensions take precedence.
	StaticReferer string
	StaticTitle   string
}

const defaultBaseURL = "https://openrouter.ai/api/v1"
const rateLimitFallback = 60 * time.Second

// New returns a runtime backend that invokes OpenRouter via the openai-go SDK.
func New(cfg Config) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(err)
	}
	pool, err := openaicred.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
	if err != nil {
		return newConfigErrorBackend(fmt.Errorf("%s: credentials: %w", ID, err))
	}
	return execbackend.Backend{
		Caps: openaicaps.HostedFull,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModel(resolveModel(cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
			}
			flavor := resolveFlavor(call)
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
					return nil, fmt.Errorf("%s: %w", ID, aerr)
				}
				opts := buildRequestOptions(call, cfg)
				cli := openaicred.NewOpenAIClientWithOptions(cfg.BaseURL, cred.Secret, cfg.HTTPClient, cfg.SDKMaxRetries, opts)

				var es lipapi.ManagedEventStream
				var openErr error
				switch flavor {
				case openrouterwire.FlavorResponses:
					es, openErr = openResponsesStream(ctx, cli, call, cand)
				default:
					es, openErr = openChatStream(ctx, cli, call, cand)
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
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, rateLimitFallback)
					pool.MarkRateLimited(cred.ID, until)
				default:
					return nil, openErr
				}
			}
		},
	}
}

func newConfigErrorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps: openaicaps.HostedFull,
		ResolveCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.HostedFull
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
