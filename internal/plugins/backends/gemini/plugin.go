package gemini

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Config configures the Gemini generateContent backend connector (official genai SDK).
// BaseURL is the API origin (e.g. https://generativelanguage.googleapis.com) without a path suffix;
// the SDK appends /v1beta/models/... for Google AI backend.
//
// google.golang.org/genai does not apply the same automatic multi-attempt retries on
// generateContent/streamGenerateContent as openai-go and anthropic-sdk-go; credential tests
// can count httptest invocations without an SDKMaxRetries-style knob.
type Config struct {
	BaseURL string
	APIKey  string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
}

const geminiRateLimitFallback = 60 * time.Second

func defaultBackendCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityTools,
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
		lipapi.CapabilityParallelToolCalls,
	)
}

// New returns a runtime backend that invokes Gemini generateContent streaming via google.golang.org/genai.
func New(cfg Config) execbackend.Backend {
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
			sp, err := StreamParamsForCall(&call, cand)
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
				cli, cerr := newGenaiClient(ctx, cfg, cred.Secret)
				if cerr != nil {
					return nil, fmt.Errorf("gemini: client: %w", cerr)
				}
				seq := cli.Models.GenerateContentStream(ctx, sp.Model, sp.Contents, sp.Config)
				es := newGenaiStream(seq, call.MaxPendingWireEvents)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewPrependFirst(ev, es), nil
				}
				_ = es.Close()
				kind, retryAfter := classifyGenaiAPIError(rerr)
				// Anchor pool "now" to the post-response instant. Using the iteration-start
				// time for Retry-After math can expire the cooldown before MarkRateLimited if
				// the upstream round trip was slower than the delta (flaky second attempt).
				now = time.Now()
				switch kind {
				case apiFailureAuthInvalid:
					pool.MarkAuthInvalid(cred.ID)
				case apiFailureRateLimited:
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, geminiRateLimitFallback)
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
