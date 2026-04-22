package anthropic

import (
	"context"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Config configures the Anthropic Messages API backend connector (official SDK).
// BaseURL is the API origin only (e.g. https://api.anthropic.com), without a /v1 suffix;
// the SDK posts to /v1/messages relative to it.
type Config struct {
	BaseURL string
	APIKey  string
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
}

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
func New(cfg Config) runtime.Backend {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(err)
	}
	cli := newSDKClient(cfg)
	return runtime.Backend{
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
			stream := cli.Messages.NewStreaming(ctx, p)
			return newMessageStream(stream, call.MaxPendingWireEvents), nil
		},
	}
}

func newConfigErrorBackend(err error) runtime.Backend {
	return runtime.Backend{
		Caps: defaultBackendCaps(),
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
			return nil, err
		},
	}
}
