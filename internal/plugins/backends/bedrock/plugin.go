package bedrock

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func defaultBackendCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityTools,
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
		lipapi.CapabilityParallelToolCalls,
	)
}

// New returns a runtime backend that invokes Bedrock ConverseStream via the AWS SDK v2.
func New(cfg Config) runtime.Backend {
	cli, err := newRuntimeClient(cfg)
	if err != nil {
		// Surface construction errors at Open time via a backend that always fails.
		return runtime.Backend{
			Caps: defaultBackendCaps(),
			ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
				return ModelCapabilities(resolveModelID(cand, call))
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
				return nil, err
			},
		}
	}
	client := cli
	return runtime.Backend{
		Caps: defaultBackendCaps(),
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModelID(cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
			in, err := ConverseStreamInputForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			out, err := client.ConverseStream(ctx, in)
			if err != nil {
				return nil, fmt.Errorf("bedrock: ConverseStream: %w", err)
			}
			return newConverseStream(out.GetStream()), nil
		},
	}
}
