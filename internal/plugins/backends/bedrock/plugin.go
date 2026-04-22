package bedrock

import (
	"context"
	"fmt"
	"time"

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

// DefaultLoadConfigTimeout bounds AWS SDK default configuration loading during backend construction.
// New applies it automatically; composition roots calling NewWithContext should wrap their
// context with context.WithTimeout using this duration (or shorter) unless it already carries a deadline.
const DefaultLoadConfigTimeout = 30 * time.Second

// New returns a runtime backend that invokes Bedrock ConverseStream via the AWS SDK v2.
//
// Deprecated: prefer [NewWithContext] with a context whose deadline reflects your bootstrap
// budget. New applies [DefaultLoadConfigTimeout] around AWS config load only.
func New(cfg Config) runtime.Backend {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultLoadConfigTimeout)
	defer cancel()
	return NewWithContext(ctx, cfg)
}

// NewWithContext returns a runtime backend like [New], using ctx for awsconfig.LoadDefaultConfig.
// If ctx is nil, [context.Background] is used (no deadline). Prefer passing a context with a deadline.
func NewWithContext(ctx context.Context, cfg Config) runtime.Backend {
	if ctx == nil {
		ctx = context.Background()
	}
	cli, err := newRuntimeClient(ctx, cfg)
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
