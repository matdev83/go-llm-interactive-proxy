package hooks

import (
	"cmp"
	"slices"

	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// Config wires hook implementations into a Bus. Any slice may be nil or empty.
type Config struct {
	SubmitHooks       []sdk.SubmitHook
	RequestPartHooks  []sdk.RequestPartHook
	ResponsePartHooks []sdk.ResponsePartHook
	ToolReactors      []sdk.ToolReactor
}

// Bus runs hook chains in stable order (Order ascending, then ID ascending).
type Bus struct {
	submit        []sdk.SubmitHook
	requestParts  []sdk.RequestPartHook
	responseParts []sdk.ResponsePartHook
	tools         []sdk.ToolReactor
}

// New constructs a Bus with sorted hook chains.
func New(cfg Config) *Bus {
	return &Bus{
		submit:        sortSubmit(cfg.SubmitHooks),
		requestParts:  sortRequestParts(cfg.RequestPartHooks),
		responseParts: sortResponseParts(cfg.ResponsePartHooks),
		tools:         sortTools(cfg.ToolReactors),
	}
}

func sortSubmit(h []sdk.SubmitHook) []sdk.SubmitHook {
	if len(h) == 0 {
		return nil
	}
	out := append([]sdk.SubmitHook(nil), h...)
	slices.SortFunc(out, func(a, b sdk.SubmitHook) int {
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return out
}

func sortRequestParts(h []sdk.RequestPartHook) []sdk.RequestPartHook {
	if len(h) == 0 {
		return nil
	}
	out := append([]sdk.RequestPartHook(nil), h...)
	slices.SortFunc(out, func(a, b sdk.RequestPartHook) int {
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return out
}

func sortResponseParts(h []sdk.ResponsePartHook) []sdk.ResponsePartHook {
	if len(h) == 0 {
		return nil
	}
	out := append([]sdk.ResponsePartHook(nil), h...)
	slices.SortFunc(out, func(a, b sdk.ResponsePartHook) int {
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return out
}

func sortTools(h []sdk.ToolReactor) []sdk.ToolReactor {
	if len(h) == 0 {
		return nil
	}
	out := append([]sdk.ToolReactor(nil), h...)
	slices.SortFunc(out, func(a, b sdk.ToolReactor) int {
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return out
}
