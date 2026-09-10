// Package frontendpipe provides a shared HTTP create pipeline for wire frontends.
package frontendpipe

import (
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ResponseContext is the bounded shared frontend response context (Task 14.1, Requirement 18).
// It couples frontend-owned proof state (FrontendWireState) with ExecutionResult (ResponseFacts,
// sensitive SessionResponseCarrier, and canonical EventStream) to supply stream wrapping and
// response writers without fabricating a partial or fake lipapi.Call.
// Core contracts never import this type (Requirement 18.8).
type ResponseContext struct {
	// State is the frontend-owned proof state including response seeds and extra protocol state.
	State FrontendWireState
	// ResponseFacts contains bounded provider-neutral facts returned by ExecuteLargeBody.
	ResponseFacts largebody.ResponseFacts
	// Session carries sensitive session response carriers returned by ExecuteLargeBody.
	Session largebody.SessionResponseCarrier
	// Stream is the canonical event stream returned by ExecuteLargeBody.
	Stream lipapi.EventStream
}

// NewResponseContext creates an initialized ResponseContext from frontend wire state and wire execution result.
func NewResponseContext(state FrontendWireState, res largebody.ExecutionResult) ResponseContext {
	return ResponseContext{
		State:         state,
		ResponseFacts: res.Facts,
		Session:       res.Session,
		Stream:        res.Stream,
	}
}

// ProfileID returns the profile ID identifying the protocol-specific handler.
func (c ResponseContext) ProfileID() string {
	return c.State.ProfileID
}

// Proof returns the underlying provider-neutral largebody.Proof.
func (c ResponseContext) Proof() largebody.Proof {
	return c.State.Proof
}

// Seeds returns the underlying bounded ResponseStateSeeds.
func (c ResponseContext) Seeds() ResponseStateSeeds {
	return c.State.Seeds
}

// Extra returns any frontend-owned per-request protocol state.
func (c ResponseContext) Extra() any {
	return c.State.Extra
}

// Facts returns the bounded provider-neutral response facts.
func (c ResponseContext) Facts() largebody.ResponseFacts {
	return c.ResponseFacts
}

// SessionCarrier returns the sensitive session response carrier.
func (c ResponseContext) SessionCarrier() largebody.SessionResponseCarrier {
	return c.Session
}

// CallID returns the effective request / call ID: explicit request ID if present,
// otherwise the deterministic call ID derived from canonical semantic identity.
func (c ResponseContext) CallID() string {
	if c.ResponseFacts.RequestID != "" {
		return c.ResponseFacts.RequestID
	}
	return c.State.Seeds.EffectiveCallID()
}

// DeterministicCallID returns the path-stable call ID derived from canonical semantic identity.
func (c ResponseContext) DeterministicCallID() string {
	return c.State.Seeds.DeterministicCallID
}

// DeterministicTimestamp returns the Unix timestamp derived from canonical semantic identity.
func (c ResponseContext) DeterministicTimestamp() int64 {
	return c.State.Seeds.DeterministicTimestamp
}

// ExplicitRequestID returns any caller-provided or header-supplied request ID.
func (c ResponseContext) ExplicitRequestID() string {
	return c.State.Seeds.ExplicitRequestID
}

// RouteSelector returns the effective routing selector.
func (c ResponseContext) RouteSelector() string {
	if c.State.Seeds.RouteSelector != "" {
		return c.State.Seeds.RouteSelector
	}
	return c.State.Proof.RouteSelector
}

// ClientModel returns the client-requested model name.
func (c ResponseContext) ClientModel() string {
	if c.State.Seeds.ClientModel != "" {
		return c.State.Seeds.ClientModel
	}
	return c.State.Proof.ClientModel
}

// EffectiveModel returns the candidate/provider effective model if resolved, otherwise the client model.
func (c ResponseContext) EffectiveModel() string {
	if c.ResponseFacts.EffectiveModel != "" {
		return c.ResponseFacts.EffectiveModel
	}
	return c.ClientModel()
}

// IsStream reports whether streaming delivery was requested.
func (c ResponseContext) IsStream() bool {
	if c.ResponseFacts.Delivery != "" {
		return c.ResponseFacts.Delivery == lipapi.DeliveryModeStreaming
	}
	return c.State.Seeds.Stream || c.State.Proof.Delivery == lipapi.DeliveryModeStreaming
}

// CancellationID returns the frontend cancellation identifier seed.
func (c ResponseContext) CancellationID() string {
	return c.State.Seeds.CancellationID
}

// SessionID returns the authoritative session identifier.
func (c ResponseContext) SessionID() string {
	if c.ResponseFacts.SessionID != "" {
		return c.ResponseFacts.SessionID
	}
	if c.Session.AuthoritativeSessionID != "" {
		return c.Session.AuthoritativeSessionID
	}
	return c.State.Seeds.SessionID
}

// ALegID returns the authoritative A-leg identifier.
func (c ResponseContext) ALegID() string {
	if c.ResponseFacts.ALegID != "" {
		return c.ResponseFacts.ALegID
	}
	if c.Session.ALegID != "" {
		return c.Session.ALegID
	}
	return c.State.Seeds.ALegID
}

// ResumeToken returns the sensitive resume token carrier.
func (c ResponseContext) ResumeToken() largebody.SensitiveString {
	return c.Session.ResumeToken
}

// TraceID returns the trace identifier.
func (c ResponseContext) TraceID() string {
	if c.ResponseFacts.TraceID != "" {
		return c.ResponseFacts.TraceID
	}
	return c.CallID()
}

// WriteSessionHeaders sets LIP session response headers on w from the sensitive session carrier
// (Requirements 14.6, 18.2).
func (c ResponseContext) WriteSessionHeaders(w http.ResponseWriter) {
	if w == nil {
		return
	}
	c.WriteSessionHeadersTo(w.Header())
}

// WriteSessionHeadersTo sets LIP session response headers on h from the sensitive session carrier.
func (c ResponseContext) WriteSessionHeadersTo(h http.Header) {
	if h == nil {
		return
	}
	sessionwire.WriteSessionResponseCarrierHeaders(h, c.Session)
}

// Validate validates the response context under the given semantic-fact budget.
func (c ResponseContext) Validate(maxFactBytes int64) error {
	if maxFactBytes <= 0 {
		return fmt.Errorf("frontendpipe: fact budget must be > 0, got %d", maxFactBytes)
	}
	if err := c.State.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("frontendpipe: response context wire state: %w", err)
	}

	// Validate ResponseFacts if populated
	if c.ResponseFacts.RequestID != "" {
		if err := c.ResponseFacts.Validate(maxFactBytes); err != nil {
			return fmt.Errorf("frontendpipe: response context facts: %w", err)
		}
		// Cross-consistency checks
		if c.State.Seeds.SessionID != "" && c.ResponseFacts.SessionID != "" && c.ResponseFacts.SessionID != c.State.Seeds.SessionID {
			return fmt.Errorf("frontendpipe: response context session ID %q disagrees with seeds session ID %q",
				c.ResponseFacts.SessionID, c.State.Seeds.SessionID)
		}
		if c.State.Seeds.ALegID != "" && c.ResponseFacts.ALegID != "" && c.ResponseFacts.ALegID != c.State.Seeds.ALegID {
			return fmt.Errorf("frontendpipe: response context A-leg ID %q disagrees with seeds A-leg ID %q",
				c.ResponseFacts.ALegID, c.State.Seeds.ALegID)
		}
		isStream := c.ResponseFacts.Delivery == lipapi.DeliveryModeStreaming
		if c.State.Seeds.Stream != isStream {
			return fmt.Errorf("frontendpipe: response context delivery mode %q disagrees with seeds stream flag %t",
				string(c.ResponseFacts.Delivery), c.State.Seeds.Stream)
		}
	}

	// Validate Session carrier if populated
	if c.Session.AuthoritativeSessionID != "" || c.Session.ALegID != "" || !c.Session.ResumeToken.IsZero() {
		if err := c.Session.Validate(maxFactBytes); err != nil {
			return fmt.Errorf("frontendpipe: response context session carrier: %w", err)
		}
		if c.State.Seeds.SessionID != "" && c.Session.AuthoritativeSessionID != "" && c.Session.AuthoritativeSessionID != c.State.Seeds.SessionID {
			return fmt.Errorf("frontendpipe: response context carrier session ID %q disagrees with seeds session ID %q",
				c.Session.AuthoritativeSessionID, c.State.Seeds.SessionID)
		}
		if c.State.Seeds.ALegID != "" && c.Session.ALegID != "" && c.Session.ALegID != c.State.Seeds.ALegID {
			return fmt.Errorf("frontendpipe: response context carrier A-leg ID %q disagrees with seeds A-leg ID %q",
				c.Session.ALegID, c.State.Seeds.ALegID)
		}
	}

	return nil
}
