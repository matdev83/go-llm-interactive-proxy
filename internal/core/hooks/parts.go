package hooks

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// RunRequestPartHooks runs request-part hooks in order and re-validates the call.
func (b *Bus) RunRequestPartHooks(ctx context.Context, call *lipapi.Call, meta sdk.PartMeta) error {
	if call == nil {
		return fmt.Errorf("hooks: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("hooks: %w", lipapi.ErrNilContext)
	}
	reqParts := []sdk.RequestPartHook{}
	if b != nil {
		reqParts = b.requestParts
	}
	for _, h := range reqParts {
		err := safety.Call(safety.BoundaryExtension, "request_part_hook", func() error {
			return h.HandleRequestParts(ctx, call, meta)
		})
		if err != nil {
			if h.FailureMode() == sdk.FailOpen {
				logFailOpenHookPanic(ctx, "request_part_hook", h.ID(), err)
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
		return fmt.Errorf("hooks: nil event: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("hooks: %w", lipapi.ErrNilContext)
	}
	respParts := []sdk.ResponsePartHook{}
	if b != nil {
		respParts = b.responseParts
	}
	for _, h := range respParts {
		err := safety.Call(safety.BoundaryExtension, "response_part_hook", func() error {
			return h.HandleEvent(ctx, ev, meta)
		})
		if err != nil {
			if h.FailureMode() == sdk.FailOpen {
				logFailOpenHookPanic(ctx, "response_part_hook", h.ID(), err)
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
