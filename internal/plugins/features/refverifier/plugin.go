package refverifier

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

const defaultOrder = 25

type gate struct {
	order int
	role  string
	text  string
}

var _ completion.Gate = gate{}

// NewCompletionGate steers the completion with a short response after a successful aux collect.
// When the aux client is not configured or returns an error, the gate passes the original stream.
func NewCompletionGate(cfg Config) completion.Gate {
	c := fillRole(cfg)
	o := defaultOrder
	if c.Order != nil {
		o = *c.Order
	}
	return gate{order: o, role: c.Role, text: c.SteerText}
}

func (g gate) ID() string { return ID + "-gate" }

func (g gate) Order() int { return g.order }

func (g gate) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (g gate) Handle(ctx context.Context, meta completion.Meta, _ completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	_, err := svc.Aux.Collect(ctx, auxiliary.Request{
		Role:          g.role,
		ParentTraceID: meta.TraceID,
		Call:          &lipapi.Call{},
	})
	if err != nil {
		return completion.PassOriginalOutcome(), nil
	}
	return completion.ReplaceOutcome([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: g.text},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}
