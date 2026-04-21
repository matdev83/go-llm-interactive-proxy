package capabilities

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Resolver resolves effective backend capabilities for one routing candidate before upstream
// negotiation (requirement 7.1). Composition wires a registry-backed implementation without
// importing concrete backend plugins from core orchestration.
type Resolver interface {
	DescribeCandidate(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps
}

// BackendCapsFunc resolves caps for a single backend id; used to build MapResolver at composition.
type BackendCapsFunc func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps

// MapResolver dispatches to a per-backend-id function (typically wrapping runtime.Backend.ResolveCaps).
type MapResolver map[string]BackendCapsFunc

// DescribeCandidate implements Resolver.
func (m MapResolver) DescribeCandidate(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
	if m == nil {
		return lipapi.BackendCaps{}
	}
	id := cand.Primary.Backend
	fn := m[id]
	if fn == nil {
		return lipapi.BackendCaps{}
	}
	return fn(ctx, cand, call)
}
