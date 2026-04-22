package reftool

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// ID is the feature plugin id for YAML registration.
const ID = "ref-tool-prefix"

const defaultOrder = 50

type hook struct {
	order  int
	prefix string
}

var _ sdk.ToolReactor = hook{}

// NewReactor prefixes tool argument deltas (rewrite path).
func NewReactor(cfg Config) sdk.ToolReactor {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	p := cfg.Prefix
	if p == "" {
		p = ">>"
	}
	return hook{order: o, prefix: p}
}

func (hook) ID() string   { return ID }
func (h hook) Order() int { return h.order }

func (h hook) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
	if te.Kind != lipapi.ToolEventArgsDelta || te.ArgsDelta == "" {
		return sdk.ToolPass, lipapi.ToolEvent{}, nil
	}
	out := te
	out.ArgsDelta = h.prefix + strings.TrimPrefix(te.ArgsDelta, h.prefix)
	return sdk.ToolRewrite, out, nil
}
