package compatmode

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ApplyCapabilityCeiling keeps model-aware family resolution from elevating a
// validated profile above its declared effective capability surface.
func ApplyCapabilityCeiling(be execbackend.Backend, ceiling lipapi.BackendCaps) execbackend.Backend {
	be.Caps = ceiling
	base := be.ResolveCaps
	be.ResolveCaps = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
		resolved := ceiling
		if base != nil {
			resolved = base(ctx, call, cand)
		}
		out := lipapi.NewBackendCaps()
		for capability := range resolved {
			if _, ok := ceiling[capability]; ok {
				out[capability] = struct{}{}
			}
		}
		return out
	}
	return be
}
