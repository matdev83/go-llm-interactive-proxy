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
	// ToolReactorErrorPolicy controls reactor error propagation (zero = unspecified; treated as fail-open).
	ToolReactorErrorPolicy sdk.ToolReactorErrorPolicy
}

// Bus runs hook chains in stable order (Order ascending, then ID ascending, then
// registration index ascending so equal Order+ID hooks remain deterministic).
type Bus struct {
	submit        []sdk.SubmitHook
	requestParts  []sdk.RequestPartHook
	responseParts []sdk.ResponsePartHook
	tools         []sdk.ToolReactor
	toolErrPol    sdk.ToolReactorErrorPolicy
}

// New constructs a Bus with sorted hook chains.
func New(cfg Config) *Bus {
	return &Bus{
		submit:        sortSubmit(cfg.SubmitHooks),
		requestParts:  sortRequestParts(cfg.RequestPartHooks),
		responseParts: sortResponseParts(cfg.ResponsePartHooks),
		tools:         sortTools(cfg.ToolReactors),
		toolErrPol:    cfg.ToolReactorErrorPolicy,
	}
}

// HookChainLengths returns hook counts for diagnostics and tests.
func (b *Bus) HookChainLengths() (submit, requestParts, responseParts, tools int) {
	if b == nil {
		return 0, 0, 0, 0
	}
	return len(b.submit), len(b.requestParts), len(b.responseParts), len(b.tools)
}

func sortSubmit(h []sdk.SubmitHook) []sdk.SubmitHook {
	if len(h) == 0 {
		return nil
	}
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(i, j int) int {
		a, b := h[i], h[j]
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ID(), b.ID()); c != 0 {
			return c
		}
		return cmp.Compare(i, j) // stable tie-break: registration order
	})
	out := make([]sdk.SubmitHook, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}

func sortRequestParts(h []sdk.RequestPartHook) []sdk.RequestPartHook {
	if len(h) == 0 {
		return nil
	}
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(i, j int) int {
		a, b := h[i], h[j]
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ID(), b.ID()); c != 0 {
			return c
		}
		return cmp.Compare(i, j)
	})
	out := make([]sdk.RequestPartHook, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}

func sortResponseParts(h []sdk.ResponsePartHook) []sdk.ResponsePartHook {
	if len(h) == 0 {
		return nil
	}
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(i, j int) int {
		a, b := h[i], h[j]
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ID(), b.ID()); c != 0 {
			return c
		}
		return cmp.Compare(i, j)
	})
	out := make([]sdk.ResponsePartHook, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}

func sortTools(h []sdk.ToolReactor) []sdk.ToolReactor {
	if len(h) == 0 {
		return nil
	}
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(i, j int) int {
		a, b := h[i], h[j]
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ID(), b.ID()); c != 0 {
			return c
		}
		return cmp.Compare(i, j)
	})
	out := make([]sdk.ToolReactor, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
