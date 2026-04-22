package gemini

import (
	"context"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Config configures the Gemini generateContent backend connector (official genai SDK).
// BaseURL is the API origin (e.g. https://generativelanguage.googleapis.com) without a path suffix;
// the SDK appends /v1beta/models/... for Google AI backend.
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

// New returns a runtime backend that invokes Gemini generateContent streaming via google.golang.org/genai.
func New(cfg Config) runtime.Backend {
	return runtime.Backend{
		Caps: defaultBackendCaps(),
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
			}
			cli, err := newGenaiClient(ctx, cfg)
			if err != nil {
				return nil, fmt.Errorf("gemini: client: %w", err)
			}
			sp, err := StreamParamsForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			seq := cli.Models.GenerateContentStream(ctx, sp.Model, sp.Contents, sp.Config)
			return newGenaiStream(seq, call.MaxPendingWireEvents), nil
		},
	}
}
