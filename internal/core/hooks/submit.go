package hooks

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// RunSubmit executes submit hooks in order. meta may be nil; a working meta map is allocated.
func (b *Bus) RunSubmit(ctx context.Context, call *lipapi.Call, meta *sdk.SubmitMeta) error {
	if call == nil {
		return fmt.Errorf("hooks: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("hooks: %w", lipapi.ErrNilContext)
	}
	if meta == nil {
		meta = &sdk.SubmitMeta{}
	}
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	submit := []sdk.SubmitHook{}
	if b != nil {
		submit = b.submit
	}
	for _, h := range submit {
		if execctx.IsSuppressedPluginID(ctx, h.ID()) {
			continue
		}
		dec, err := safety.CallValue(safety.BoundaryExtension, "submit_hook", func() (sdk.SubmitDecision, error) {
			return h.Handle(ctx, call, meta)
		})
		if err != nil {
			if h.FailureMode() == sdk.FailOpen {
				logFailOpenHookPanic(ctx, "submit_hook", h.ID(), err)
				continue
			}
			return fmt.Errorf("submit hook %q: %w", h.ID(), err)
		}
		if dec.Reject {
			return &sdk.SubmitRejectError{HookID: h.ID(), Reason: dec.Reason}
		}
	}
	if err := call.Validate(); err != nil {
		return fmt.Errorf("submit hooks: invalid canonical call after submit chain: %w", err)
	}
	return nil
}
