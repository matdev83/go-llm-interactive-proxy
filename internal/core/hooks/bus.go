package hooks

import (
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
//
// After [New], the bus does not replace its internal hook slices; concurrent [Bus] method calls
// are safe only if hook implementations are safe for concurrent use. Callers must not mutate
// those slices or the bus fields after construction (including when the same bus is embedded in
// [github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions.RequestRuntimeSnapshot]).
type Bus struct {
	submit        []sdk.SubmitHook
	requestParts  []sdk.RequestPartHook
	responseParts []sdk.ResponsePartHook
	tools         []sdk.ToolReactor
	toolErrPol    sdk.ToolReactorErrorPolicy
}

// New constructs a Bus with sorted hook chains.
func New(cfg Config) *Bus {
	sorted := MaterializeSorted(cfg)
	return &Bus{
		submit:        sorted.SubmitHooks,
		requestParts:  sorted.RequestPartHooks,
		responseParts: sorted.ResponsePartHooks,
		tools:         sorted.ToolReactors,
		toolErrPol:    sorted.ToolReactorErrorPolicy,
	}
}

// MaterializeSorted returns a copy of cfg with each hook chain sorted per design §17
// (order, id, registration index). Diagnostics and tests use it without constructing a Bus.
func MaterializeSorted(cfg Config) Config {
	return Config{
		SubmitHooks:            sortSubmit(slices.Clone(cfg.SubmitHooks)),
		RequestPartHooks:       sortRequestParts(slices.Clone(cfg.RequestPartHooks)),
		ResponsePartHooks:      sortResponseParts(slices.Clone(cfg.ResponsePartHooks)),
		ToolReactors:           sortTools(slices.Clone(cfg.ToolReactors)),
		ToolReactorErrorPolicy: cfg.ToolReactorErrorPolicy,
	}
}

// HookChainLengths returns hook counts for diagnostics and tests.
func (b *Bus) HookChainLengths() (submit, requestParts, responseParts, tools int) {
	if b == nil {
		return 0, 0, 0, 0
	}
	return len(b.submit), len(b.requestParts), len(b.responseParts), len(b.tools)
}

// sortSubmit orders submit hooks by StableParticipantLess.
//
// It uses a permutation slice idx initialized to [0..n); slices.SortFunc compares two
// elements of idx (each an original hook index), not two positions in idx. sortRequestParts,
// sortResponseParts, and sortTools follow the same pattern.
func sortSubmit(h []sdk.SubmitHook) []sdk.SubmitHook {
	if len(h) == 0 {
		return nil
	}
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return StableParticipantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
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
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return StableParticipantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
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
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return StableParticipantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
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
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return StableParticipantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
	})
	out := make([]sdk.ToolReactor, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
