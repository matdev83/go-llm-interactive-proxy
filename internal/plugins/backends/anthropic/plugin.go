package anthropic

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
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Config configures the Anthropic Messages API backend connector (official SDK).
// BaseURL is the API origin only (e.g. https://api.anthropic.com), without a /v1 suffix;
// the SDK posts to /v1/messages relative to it.
type Config struct {
	BaseURL string
	APIKey  string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	// Integration tests that assert a single upstream attempt on 429/401 should use a pointer to 0.
	SDKMaxRetries *int
}

const anthropicRateLimitFallback = 60 * time.Second

func defaultBackendCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityTools,
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
		lipapi.CapabilityParallelToolCalls,
	)
}

// New returns a runtime backend that invokes the Anthropic Messages API using anthropic-sdk-go.
func New(cfg Config) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(err)
	}
	pool, err := credpool.NewPoolFromBackendKeys(cfg.APIKey, cfg.APIKeys)
	if err != nil {
		return newConfigErrorBackend(fmt.Errorf("%s: credentials: %w", ID, err))
	}
	return execbackend.Backend{
		Caps: defaultBackendCaps(),
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
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
				cli := newSDKClientForSecret(cfg, cred.Secret)
				stream := cli.Messages.NewStreaming(ctx, p)
				es := newMessageStream(stream, call.MaxPendingWireEvents)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewPrependFirst(ev, es), nil
				}
				_ = es.Close()
				kind, retryAfter := classifyAnthropicAPIError(rerr)
				// Anchor pool "now" to the post-response instant. Using the iteration-start
				// time for Retry-After math can expire the cooldown before MarkRateLimited if
				// the upstream round trip was slower than the delta (flaky second attempt).
				now = time.Now()
				switch kind {
				case apiFailureAuthInvalid:
					pool.MarkAuthInvalid(cred.ID)
				case apiFailureRateLimited:
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, anthropicRateLimitFallback)
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
		Caps: defaultBackendCaps(),
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
			return nil, err
		},
	}
}
