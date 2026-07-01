package bedrock

import (
	"context"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
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
func New(cfg Config) execbackend.Backend {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultLoadConfigTimeout)
	defer cancel()
	return NewWithContext(ctx, cfg)
}

// ensureLoadConfigDeadline returns a context for awsconfig.LoadDefaultConfig. If ctx is nil, or
// has no deadline, it wraps with [DefaultLoadConfigTimeout] so config load cannot hang
// indefinitely. The caller must invoke the returned CancelFunc.
func ensureLoadConfigDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), DefaultLoadConfigTimeout)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultLoadConfigTimeout)
}

// NewWithContext returns a runtime backend like [New], using ctx for awsconfig.LoadDefaultConfig.
// A deadline is always applied for the load step: either ctx's own deadline, or
// [DefaultLoadConfigTimeout] when ctx is nil or uncancelled without a deadline.
func NewWithContext(ctx context.Context, cfg Config) execbackend.Backend {
	loadCtx, cancel := ensureLoadConfigDeadline(ctx)
	defer cancel()
	cli, err := newRuntimeClient(loadCtx, cfg)
	if err != nil {
		// Surface construction errors at Open time via a backend that always fails.
		return execbackend.Backend{
			Caps:            defaultBackendCaps(),
			BackendPrefixes: []string{ID},
			ModelInventory:  newFoundationModelsProvider(loadCtx, cfg),
			ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
				return ModelCapabilities(resolveModelID(cand, call))
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, err
			},
		}
	}
	client := cli
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		BackendPrefixes: []string{ID},
		ModelInventory:  newFoundationModelsProvider(loadCtx, cfg),
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModelID(cand, call))
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
			}
			in, err := ConverseStreamInputForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			out, err := client.ConverseStream(ctx, in)
			if err != nil {
				return nil, fmt.Errorf("bedrock: ConverseStream: %w", err)
			}
			return newConverseStream(out.GetStream(), call.MaxPendingWireEvents), nil
		},
	}
}
