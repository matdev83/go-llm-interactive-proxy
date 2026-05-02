package hooks

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// ToolApplyResult is the outcome of running the tool-reactor chain on one tool event.
type ToolApplyResult struct {
	// Emit is false when a reactor swallowed the event.
	Emit bool
	// Event is the canonical tool event to surface when Emit is true.
	Event lipapi.ToolEvent
	// Err is set when ToolReactorErrorsFailClosed is configured and a reactor returned an error.
	Err error
}

// ApplyToolReactors runs tool reactors in order. Reactor errors follow Config.ToolReactorErrorPolicy
// (default fail-open). Swallow stops the chain and returns Emit=false.
func (b *Bus) ApplyToolReactors(ctx context.Context, te lipapi.ToolEvent, meta sdk.ToolMeta) ToolApplyResult {
	if ctx == nil {
		return ToolApplyResult{Err: fmt.Errorf("hooks: %w", lipapi.ErrNilContext)}
	}
	if v, ok := execctx.FromContext(ctx); ok {
		meta.Principal = v.Principal
		meta.Session = v.Session
		meta.Workspace = v.Workspace
	}
	cur := te
	var tools []sdk.ToolReactor
	var pol sdk.ToolReactorErrorPolicy
	if b != nil {
		tools = b.tools
		pol = b.toolErrPol
	}
	for _, r := range tools {
		dec, next, err := callToolReactor(ctx, r, cur, meta)
		if err != nil {
			switch pol {
			case sdk.ToolReactorErrorsFailClosed:
				return ToolApplyResult{Err: fmt.Errorf("tool reactor %s: %w", r.ID(), err)}
			case sdk.ToolReactorErrorsSwallowEvent:
				return ToolApplyResult{Emit: false, Event: lipapi.ToolEvent{}}
			default:
				var pe *safety.PanicError
				if errors.As(err, &pe) {
					logFailOpenHookPanic(ctx, "tool_reactor", r.ID(), err)
				}
				continue
			}
		}
		switch dec {
		case sdk.ToolPass:
			// Explicit pass-through; ignore next.
		case sdk.ToolRewrite, sdk.ToolReplace:
			if vErr := ValidateToolEventAfterPolicy(r.ID(), &next); vErr != nil {
				switch pol {
				case sdk.ToolReactorErrorsFailClosed:
					return ToolApplyResult{Err: fmt.Errorf("tool reactor %s: %w", r.ID(), vErr)}
				default:
					// Fail-open (default): reject invalid mutation and continue with the current event.
					continue
				}
			}
			cur = next
		case sdk.ToolSwallow:
			return ToolApplyResult{Emit: false, Event: lipapi.ToolEvent{}}
		default:
			continue
		}
	}
	return ToolApplyResult{Emit: true, Event: cur}
}

type toolReactorResult struct {
	dec  sdk.ToolDecision
	next lipapi.ToolEvent
}

// callToolReactor invokes HandleToolEvent and maps a panic to *safety.PanicError like a returned error.
func callToolReactor(
	ctx context.Context,
	r sdk.ToolReactor,
	cur lipapi.ToolEvent,
	meta sdk.ToolMeta,
) (dec sdk.ToolDecision, next lipapi.ToolEvent, err error) {
	res, err := safety.CallValue(safety.BoundaryExtension, "tool_reactor", func() (toolReactorResult, error) {
		d, n, e := r.HandleToolEvent(ctx, cur, meta)
		return toolReactorResult{dec: d, next: n}, e
	})
	return res.dec, res.next, err
}
