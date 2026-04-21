package submitnoop

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// DefaultHookOrder is the default submit hook ordering for submit-noop.
const DefaultHookOrder = 100

// submitHook is a reference no-op submit hook for the standard distribution.
type submitHook struct {
	order int
}

// NewSubmitHook returns an inert submit hook with default ordering (HookConfig zero value).
func NewSubmitHook() sdk.SubmitHook {
	return NewSubmitHookWithConfig(HookConfig{})
}

// NewSubmitHookWithConfig returns a submit hook using decoded feature config.
func NewSubmitHookWithConfig(cfg HookConfig) sdk.SubmitHook {
	o := DefaultHookOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return submitHook{order: o}
}

func (submitHook) ID() string                   { return ID }
func (h submitHook) Order() int                 { return h.order }
func (submitHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (submitHook) Handle(context.Context, *lipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	return sdk.SubmitDecision{}, nil
}
