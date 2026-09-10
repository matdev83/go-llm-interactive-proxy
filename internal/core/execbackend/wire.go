package execbackend

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

// WireBackend is the optional internal interface implemented by backends
// supporting direct wire execution (design section 9, Requirements 7, 8, 9).
type WireBackend = largebody.WireBackend

// backendWireAdapter adapts a Backend value into largebody.WireBackend.
type backendWireAdapter struct {
	be Backend
}

func (a backendWireAdapter) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	return EffectiveWireRequestSupport(ctx, a.be, facts, cand)
}

func (a backendWireAdapter) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	return EffectiveWireDomainSupport(ctx, a.be, facts)
}

var _ largebody.WireBackend = backendWireAdapter{}

// AsWireBackend adapts be into largebody.WireBackend.
func (be Backend) AsWireBackend() largebody.WireBackend {
	return backendWireAdapter{be: be}
}

var _ largebody.WireBackendProvider = Backend{}

// EffectiveWireRequestSupport resolves pure exact wire compatibility for one
// candidate (design section 9, Requirements 8, 9).
//
// Rules (Task 8.1):
//   - Nil resolver / unknown => canonical (Compatible: false).
//   - Output can declare rewrite need only if supplied rewrite semantics support it;
//     declaring NeedsModelRewrite when facts.Rewrite does not support it fails closed to canonical.
//   - Pure, no I/O.
func EffectiveWireRequestSupport(
	ctx context.Context,
	be Backend,
	facts largebody.WireRequestFacts,
	cand routing.AttemptCandidate,
) largebody.WireRequestSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if err := facts.Validate(largebody.SemanticFactBudget(ctx)); err != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	var res largebody.WireRequestSupport
	switch {
	case be.ResolveWireRequest != nil:
		res = be.ResolveWireRequest(ctx, facts, cand)
	case be.WireBackend != nil:
		res = be.WireBackend.ResolveWireRequest(ctx, facts, cand)
	default:
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	// Output can declare rewrite need only if supplied rewrite semantics support it.
	if res.NeedsModelRewrite && !facts.Rewrite.NeedsModelRewrite() {
		res.Compatible = false
		res.NeedsModelRewrite = false
		res.Reason = largebody.WireSupportReasonRewriteUnsupported
		return res
	}

	if !res.Compatible {
		res.NeedsModelRewrite = false
		if res.Reason == largebody.WireSupportReasonNone {
			res.Reason = largebody.WireSupportReasonUnsupported
		}
		return res
	}

	res.Reason = largebody.WireSupportReasonNone
	return res
}

// EffectiveWireDomainSupport resolves pure late-route domain wire compatibility
// (Requirement 7, design section 9).
//
// Rules (Task 8.1):
// - Nil resolver / unknown => canonical (Compatible: false).
// - Universal domain requires AnyAcceptedModel; otherwise fails closed to canonical.
// - Pure, no I/O.
func EffectiveWireDomainSupport(
	ctx context.Context,
	be Backend,
	facts largebody.WireDomainFacts,
) largebody.WireDomainSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if err := facts.Validate(largebody.SemanticFactBudget(ctx)); err != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	var res largebody.WireDomainSupport
	switch {
	case be.ResolveWireDomain != nil:
		res = be.ResolveWireDomain(ctx, facts)
	case be.WireBackend != nil:
		res = be.WireBackend.ResolveWireDomain(ctx, facts)
	default:
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}

	// Universal domain requires backend universal model certification.
	if facts.UniversalModel && !res.AnyAcceptedModel {
		res.Compatible = false
		res.Reason = largebody.WireSupportReasonModelUnsupported
		return res
	}

	if !res.Compatible {
		if res.Reason == largebody.WireSupportReasonNone {
			res.Reason = largebody.WireSupportReasonUnsupported
		}
		return res
	}

	res.Reason = largebody.WireSupportReasonNone
	return res
}
