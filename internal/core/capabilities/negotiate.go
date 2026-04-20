// Package capabilities provides the core-owned capability negotiation boundary.
//
// It wraps the canonical lipapi negotiation types with a package-level home
// for future capability expansion, cross-plugin capability aggregation,
// and capability-aware routing integration.
package capabilities

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Negotiator compares required capabilities against a backend's declared set.
// The default implementation delegates to lipapi.Negotiate.
type Negotiator interface {
	Negotiate(ctx context.Context, required []lipapi.Capability, backend lipapi.BackendCaps) lipapi.NegotiationResult
}

// DefaultNegotiator delegates to the canonical lipapi.Negotiate function.
type DefaultNegotiator struct{}

var _ Negotiator = DefaultNegotiator{}

func (DefaultNegotiator) Negotiate(_ context.Context, required []lipapi.Capability, backend lipapi.BackendCaps) lipapi.NegotiationResult {
	return lipapi.Negotiate(required, backend)
}

// Require derives required capabilities from a canonical call and negotiates
// against the given backend capabilities.
func Require(ctx context.Context, c lipapi.Call, backend lipapi.BackendCaps, n Negotiator) lipapi.NegotiationResult {
	if n == nil {
		n = DefaultNegotiator{}
	}
	required := lipapi.RequiredCapabilities(c)
	return n.Negotiate(ctx, required, backend)
}
