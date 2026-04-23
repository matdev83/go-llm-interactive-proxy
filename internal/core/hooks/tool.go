package hooks

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
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
	tools := []sdk.ToolReactor{}
	var pol sdk.ToolReactorErrorPolicy
	if b != nil {
		tools = b.tools
		pol = b.toolErrPol
	}
	for _, r := range tools {
		dec, next, err := r.HandleToolEvent(ctx, cur, meta)
		if err != nil {
			switch pol {
			case sdk.ToolReactorErrorsFailClosed:
				return ToolApplyResult{Err: fmt.Errorf("tool reactor %s: %w", r.ID(), err)}
			case sdk.ToolReactorErrorsSwallowEvent:
				return ToolApplyResult{Emit: false, Event: lipapi.ToolEvent{}}
			default:
				continue
			}
		}
		switch dec {
		case sdk.ToolPass:
			// Explicit pass-through; ignore next.
		case sdk.ToolRewrite, sdk.ToolReplace:
			cur = next
		case sdk.ToolSwallow:
			return ToolApplyResult{Emit: false, Event: lipapi.ToolEvent{}}
		default:
			continue
		}
	}
	return ToolApplyResult{Emit: true, Event: cur}
}
