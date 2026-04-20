package hooks

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// RunRequestPartHooks runs request-part hooks in order and re-validates the call.
func (b *Bus) RunRequestPartHooks(ctx context.Context, call *lipapi.Call, meta sdk.PartMeta) error {
	if call == nil {
		return fmt.Errorf("nil call")
	}
	for _, h := range b.requestParts {
		if err := h.HandleRequestParts(ctx, call, meta); err != nil {
			if h.FailureMode() == sdk.FailOpen {
				continue
			}
			return fmt.Errorf("request part hook %q: %w", h.ID(), err)
		}
		if err := ValidateCallAfterRequestHooks(h.ID(), call); err != nil {
			return err
		}
	}
	return nil
}

// RunResponsePartHooks runs response-part hooks in order and validates each mutation.
func (b *Bus) RunResponsePartHooks(ctx context.Context, ev *lipapi.Event, meta sdk.PartMeta) error {
	if ev == nil {
		return fmt.Errorf("nil event")
	}
	for _, h := range b.responseParts {
		if err := h.HandleEvent(ctx, ev, meta); err != nil {
			if h.FailureMode() == sdk.FailOpen {
				continue
			}
			return fmt.Errorf("response part hook %q: %w", h.ID(), err)
		}
		if err := ValidateEventAfterResponseHook(h.ID(), ev); err != nil {
			return err
		}
	}
	return nil
}
