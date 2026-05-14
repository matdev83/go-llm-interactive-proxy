package openairesponses

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
)

// Config configures the OpenAI Responses backend connector (official SDK).
// BaseURL must include the API prefix (e.g. https://api.openai.com/v1).
type Config struct {
	BaseURL string
	APIKey  string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys and preserves non-secret credential IDs for diagnostics.
	Credentials []credpool.Credential
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	// Integration tests that assert a single upstream attempt on 429/401 should use a pointer to 0.
	SDKMaxRetries *int
}

const openAIRateLimitFallback = 60 * time.Second

// New returns a runtime backend that invokes the OpenAI Responses API using openai-go.
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
			p, err := ParamsForCall(&call, cand)
			if err != nil {
				return nil, err
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
					return nil, fmt.Errorf("%s: %w", ID, aerr)
				}
				cli := openaicred.NewOpenAIClient(cfg.BaseURL, cred.Secret, cfg.HTTPClient, cfg.SDKMaxRetries)
				raw := cli.Responses.NewStreaming(ctx, p)
				es := newSDKStream(raw, call.MaxPendingWireEvents)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewManagedPrependFirst(ev, es), nil
				}
				_ = es.Close()
				kind, retryAfter := openaicred.ClassifyOpenAIAPIError(rerr)
				// Anchor pool "now" to the post-response instant. Using the iteration-start
				// time for Retry-After math can expire the cooldown before MarkRateLimited if
				// the upstream round trip was slower than the delta (flaky second attempt).
				now = time.Now()
				switch kind {
				case openaicred.FailureAuthInvalid:
					pool.MarkAuthInvalid(cred.ID)
				case openaicred.FailureRateLimited:
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, openAIRateLimitFallback)
					pool.MarkRateLimited(cred.ID, until)
				default:
					return nil, rerr
				}
			}
		},
	}
}

func newConfigErrorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps: openaicaps.HostedFull,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModel(resolveModel(cand, call))
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
