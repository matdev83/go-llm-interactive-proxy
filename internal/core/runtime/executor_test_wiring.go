package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// TestExecutor returns an empty executor for tests that assign fields via promoted
// grouped-runtime accessors after construction. Prefer [NewExecutor] with [ExecutorConfig]
// for new composition-root wiring.
type testTerminalSink struct {
	appendCall func(context.Context, billing.CallUsageRecord) error
	appendLeg  func(context.Context, billing.CallLegUsageRecord) error
}

func (s testTerminalSink) AppendCall(ctx context.Context, record billing.CallUsageRecord) error {
	if s.appendCall == nil {
		return nil
	}
	return s.appendCall(ctx, record)
}

func (s testTerminalSink) AppendLeg(ctx context.Context, record billing.CallLegUsageRecord) error {
	if s.appendLeg == nil {
		return nil
	}
	return s.appendLeg(ctx, record)
}

func TestExecutor() *Executor {
	ex := &Executor{}
	ex.TerminalUsageSink = testTerminalSink{}
	ex.ExecutionCompositionPolicy = config.ExecutionCompositionSafe
	ex.BackendExecutionResolver = routing.BackendExecutionResolverFunc(func(id string) (lipsdk.BackendExecutionClass, bool) {
		if ex.Backends != nil {
			if _, ok := ex.Backends[id]; ok {
				return lipsdk.BackendExecutionInference, true
			}
		}
		return lipsdk.BackendExecutionUnknown, false
	})
	return ex
}
