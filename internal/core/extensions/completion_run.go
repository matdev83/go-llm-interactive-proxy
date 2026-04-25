package extensions

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// ApplyCompletionGateChain runs sorted gates over the buffered completion (design §6, §17).
// When outputCommitted is true, replacement outcomes are ignored (original buffer preserved).
// Handler errors honor per-gate FailureMode; the stage default is fail-open (see DefaultFailurePolicyForStage).
// log may be nil; when set, completion-gate panics are logged via [logFailOpenExtensionPanic] before
// they are returned to the runtime stream boundary (panics are not swallowed by fail-open policy).
func ApplyCompletionGateChain(ctx context.Context, gates []completion.Gate, meta completion.Meta, original []lipapi.Event, outputCommitted bool, svc completion.Services, log *slog.Logger) ([]lipapi.Event, error) {
	if len(gates) == 0 {
		return slices.Clone(original), nil
	}
	sorted := completion.MaterializeSorted(gates)
	originalCopy := slices.Clone(original)
	current := slices.Clone(original)
	for _, g := range sorted {
		if g == nil {
			continue
		}
		buf := completion.NewBuffered(current)
		out, err := safety.CallValue(safety.BoundaryExtension, "completion_gate_handle", func() (completion.Outcome, error) {
			return g.Handle(ctx, meta, buf, svc)
		})
		if err != nil {
			var pe *safety.PanicError
			if errors.As(err, &pe) {
				// Panics are not ordinary fail-open handler errors: they must surface so the runtime
				// stream mapper can apply pre-output vs post-output crash semantics (see completion_executor_test).
				logFailOpenExtensionPanic(ctx, log, "completion_gate", g.ID(), err)
				return nil, err
			}
			if g.FailureMode() == sdkhooks.FailClosed {
				return nil, err
			}
			continue
		}
		if err := out.Validate(); err != nil {
			if g.FailureMode() == sdkhooks.FailClosed {
				return nil, err
			}
			continue
		}
		switch out.Kind {
		case completion.OutcomePassOriginal:
			// unchanged
		case completion.OutcomeReplayOriginal:
			current = slices.Clone(originalCopy)
		case completion.OutcomeReplace:
			if outputCommitted {
				continue
			}
			current = slices.Clone(out.Events)
		case completion.OutcomeReject:
			if out.Err == nil {
				return nil, errors.New("extensions: completion reject without error")
			}
			return nil, out.Err
		default:
			if g.FailureMode() == sdkhooks.FailClosed {
				return nil, errors.New("extensions: unknown completion outcome")
			}
		}
	}
	return current, nil
}

// CompletionGateBufferExceeded reports whether buffering should fail open to live passthrough (R8).
func CompletionGateBufferExceeded(limits completion.BufferLimits, n int) bool {
	return limits.OverCapacity(n)
}

// StreamFinished reports whether the canonical stream reached a terminal completion marker.
func StreamFinished(events []lipapi.Event) bool {
	if len(events) == 0 {
		return false
	}
	return events[len(events)-1].Kind == lipapi.EventResponseFinished
}
