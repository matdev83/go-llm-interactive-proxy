package hooks

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// RunSubmit executes submit hooks in order. meta may be nil; a working meta map is allocated.
func (b *Bus) RunSubmit(ctx context.Context, call *lipapi.Call, meta *sdk.SubmitMeta) error {
	if call == nil {
		return fmt.Errorf("hooks: nil call: %w", lipapi.ErrInvalidCall)
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
		dec, err := h.Handle(ctx, call, meta)
		if err != nil {
			if h.FailureMode() == sdk.FailOpen {
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
