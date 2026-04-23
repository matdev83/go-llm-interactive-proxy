package openailegacy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicaps"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Config configures the legacy OpenAI Chat Completions backend connector (official SDK).
// BaseURL must include the API prefix (e.g. https://api.openai.com/v1).
type Config struct {
	BaseURL string
	APIKey  string
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
}

// New returns a runtime backend that invokes the OpenAI Chat Completions API using openai-go.
func New(cfg Config) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(err)
	}
	cli := newSDKClient(cfg)
	return execbackend.Backend{
		Caps: openaicaps.HostedFull,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModel(resolveModel(cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
			}
			p, err := ParamsForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			stream := cli.Chat.Completions.NewStreaming(ctx, p)
			return newChatStream(stream, call.MaxPendingWireEvents), nil
		},
	}
}

func newConfigErrorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps: openaicaps.HostedFull,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModel(resolveModel(cand, call))
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
			return nil, err
		},
	}
}
