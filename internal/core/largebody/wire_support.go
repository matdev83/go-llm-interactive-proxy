package largebody

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

// WireSupportReason classifies the compatibility or decline reason for a backend
// wire resolution (design section 9, Requirement 8).
type WireSupportReason uint8

const (
	WireSupportReasonNone WireSupportReason = iota
	WireSupportReasonUnsupported
	WireSupportReasonProfileUnsupported
	WireSupportReasonOperationUnsupported
	WireSupportReasonDeliveryUnsupported
	WireSupportReasonBodyModeUnsupported
	WireSupportReasonModelUnsupported
	WireSupportReasonRewriteUnsupported
	WireSupportReasonClampUnsupported
)

func (r WireSupportReason) String() string {
	switch r {
	case WireSupportReasonNone:
		return "none"
	case WireSupportReasonUnsupported:
		return "unsupported"
	case WireSupportReasonProfileUnsupported:
		return "profile_unsupported"
	case WireSupportReasonOperationUnsupported:
		return "operation_unsupported"
	case WireSupportReasonDeliveryUnsupported:
		return "delivery_unsupported"
	case WireSupportReasonBodyModeUnsupported:
		return "body_mode_unsupported"
	case WireSupportReasonModelUnsupported:
		return "model_unsupported"
	case WireSupportReasonRewriteUnsupported:
		return "rewrite_unsupported"
	case WireSupportReasonClampUnsupported:
		return "clamp_unsupported"
	default:
		return "unknown"
	}
}

// WireRequestSupport represents the pure exact wire compatibility outcome
// for one candidate (design section 9, Requirements 8, 9).
type WireRequestSupport struct {
	Compatible        bool
	NeedsModelRewrite bool
	Reason            WireSupportReason
}

// Validate checks internal consistency of the support outcome.
func (s WireRequestSupport) Validate() error {
	if s.Reason.String() == "unknown" {
		return fmt.Errorf("largebody: unknown wire support reason %d", uint8(s.Reason))
	}
	if s.Compatible && s.Reason != WireSupportReasonNone {
		return fmt.Errorf("largebody: compatible wire request support must have reason none")
	}
	if !s.Compatible && s.Reason == WireSupportReasonNone {
		return fmt.Errorf("largebody: incompatible wire request support must have a non-none reason")
	}
	return nil
}

// WireDomainSupport represents the pure late-route domain wire compatibility
// outcome (design section 9, Requirement 7).
type WireDomainSupport struct {
	Compatible       bool
	AnyAcceptedModel bool
	Reason           WireSupportReason
}

// Validate checks internal consistency of the domain support outcome.
func (s WireDomainSupport) Validate() error {
	if s.Reason.String() == "unknown" {
		return fmt.Errorf("largebody: unknown wire support reason %d", uint8(s.Reason))
	}
	if s.Compatible && s.Reason != WireSupportReasonNone {
		return fmt.Errorf("largebody: compatible wire domain support must have reason none")
	}
	if !s.Compatible && s.Reason == WireSupportReasonNone {
		return fmt.Errorf("largebody: incompatible wire domain support must have a non-none reason")
	}
	return nil
}

// WireBackend is the optional internal interface implemented by backends
// capable of evaluating direct wire compatibility (Task 8.1, Requirements 7, 8, 9).
type WireBackend interface {
	ResolveWireRequest(ctx context.Context, facts WireRequestFacts, cand routing.AttemptCandidate) WireRequestSupport
	ResolveWireDomain(ctx context.Context, facts WireDomainFacts) WireDomainSupport
}

// WireBackendProvider is an optional interface for types that adapt into a WireBackend.
type WireBackendProvider interface {
	AsWireBackend() WireBackend
}

// AsWireBackend probes a value for the internal WireBackend capability.
func AsWireBackend(v any) (WireBackend, bool) {
	if v == nil {
		return nil, false
	}
	if wb, ok := v.(WireBackend); ok && wb != nil {
		return wb, true
	}
	if p, ok := v.(WireBackendProvider); ok && p != nil {
		wb := p.AsWireBackend()
		if wb != nil {
			return wb, true
		}
	}
	return nil, false
}
