package toolreactornoop

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

const hookOrder = 100

type reactor struct{}

var _ sdk.ToolReactor = reactor{}

// NewToolReactor returns an inert tool reactor for tool-reactor-noop.
func NewToolReactor() sdk.ToolReactor { return reactor{} }

func (reactor) ID() string { return ID }
func (reactor) Order() int { return hookOrder }
func (reactor) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
	return sdk.ToolPass, te, nil
}
