package partsnoop

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

const hookOrder = 100

type requestPartHook struct{}

type responsePartHook struct{}

var _ sdk.RequestPartHook = requestPartHook{}
var _ sdk.ResponsePartHook = responsePartHook{}

// NewRequestPartHook returns an inert request-part hook for parts-noop.
func NewRequestPartHook() sdk.RequestPartHook { return requestPartHook{} }

// NewResponsePartHook returns an inert response-part hook for parts-noop.
func NewResponsePartHook() sdk.ResponsePartHook { return responsePartHook{} }

func (requestPartHook) ID() string                   { return ID }
func (requestPartHook) Order() int                   { return hookOrder }
func (requestPartHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (requestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdk.PartMeta) error {
	return nil
}

func (responsePartHook) ID() string                   { return ID }
func (responsePartHook) Order() int                   { return hookOrder }
func (responsePartHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (responsePartHook) HandleEvent(context.Context, *lipapi.Event, sdk.PartMeta) error {
	return nil
}
