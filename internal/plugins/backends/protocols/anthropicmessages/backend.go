// Package anthropicmessages provides shared Anthropic Messages protocol execution.
package anthropicmessages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Config struct {
	BackendID         string
	BaseURL           string
	APIKey            string
	APIKeys           []string
	Credentials       []credpool.Credential
	HTTPClient        *http.Client
	SDKMaxRetries     *int
	RateLimitFallback time.Duration
	ModelInventory    modelinventory.Provider
	ProviderCounter   accountingapp.ProviderCounter
}

const defaultRateLimitFallback = 60 * time.Second

func NewBackend(cfg Config) execbackend.Backend {
	id := strings.TrimSpace(cfg.BackendID)
	if id == "" {
		id = "anthropic"
	}
	if err := checkcfg.RequireNonEmpty(id, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(id, err)
	}
	pool, err := credpool.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
	if err != nil {
		return newConfigErrorBackend(id, fmt.Errorf("%s: credentials: %w", id, err))
	}
	rateLimitFallback := cfg.RateLimitFallback
	if rateLimitFallback <= 0 {
		rateLimitFallback = defaultRateLimitFallback
	}
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		BackendPrefixes: []string{id},
		ProviderCounter: cfg.ProviderCounter,
		ModelInventory:  cfg.ModelInventory,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", id, lipapi.ErrNilContext)
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
					return nil, fmt.Errorf("%s: %w", id, aerr)
				}
				cli := newSDKClientForSecret(cfg, cred.Secret)
				stream := cli.Messages.NewStreaming(ctx, p)
				es := newMessageStream(stream, call.MaxPendingWireEvents)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewManagedPrependFirst(ev, es), nil
				}
				_ = es.Close()
				kind, retryAfter := classifyAnthropicAPIError(rerr)
				now = time.Now()
				switch kind {
				case apiFailureAuthInvalid:
					pool.MarkAuthInvalid(cred.ID)
				case apiFailureRateLimited:
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, rateLimitFallback)
					pool.MarkRateLimited(cred.ID, until)
				default:
					return nil, rerr
				}
			}
		},
	}
}

func newConfigErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		BackendPrefixes: []string{id},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
