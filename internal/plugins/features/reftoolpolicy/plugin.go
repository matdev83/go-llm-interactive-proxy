package reftoolpolicy

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
)

const defaultOrder = 45

type toolFilter struct {
	policy
	order int
	id    string
}

var _ toolcatalog.Filter = toolFilter{}

// NewToolCatalogFilter removes blocked tools from the call before the backend.
func NewToolCatalogFilter(cfg Config) toolcatalog.Filter {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return toolFilter{policy: newPolicy(cfg), order: o, id: ID + "-filter"}
}

func (f toolFilter) ID() string                   { return f.id }
func (f toolFilter) Order() int                   { return f.order }
func (f toolFilter) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (f toolFilter) Handle(_ context.Context, call *lipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	if call == nil {
		return nil
	}
	out := call.Tools[:0]
	for _, t := range call.Tools {
		if f.blocked(t.Name) {
			continue
		}
		out = append(out, t)
	}
	call.Tools = out
	return nil
}

type toolReactor struct {
	policy
	order int
}

var _ sdk.ToolReactor = toolReactor{}

// NewToolReactor swallows tool stream events for blocked tool names.
func NewToolReactor(cfg Config) sdk.ToolReactor {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return toolReactor{policy: newPolicy(cfg), order: o}
}

func (r toolReactor) ID() string { return ID + "-reactor" }
func (r toolReactor) Order() int { return r.order }

func (r toolReactor) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
	if r.blocked(te.ToolName) {
		return sdk.ToolSwallow, lipapi.ToolEvent{}, nil
	}
	return sdk.ToolPass, lipapi.ToolEvent{}, nil
}
