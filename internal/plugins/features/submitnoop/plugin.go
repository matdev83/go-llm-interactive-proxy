package submitnoop

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

const hookOrder = 100

// submitHook is a reference no-op submit hook for the standard distribution.
type submitHook struct{}

// NewSubmitHook returns an inert submit hook registered as submit-noop.
func NewSubmitHook() sdk.SubmitHook { return submitHook{} }

func (submitHook) ID() string                   { return ID }
func (submitHook) Order() int                   { return hookOrder }
func (submitHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (submitHook) Handle(context.Context, *lipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	return sdk.SubmitDecision{}, nil
}
