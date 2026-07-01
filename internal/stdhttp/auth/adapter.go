// Package auth integrates transport-layer [httpauth.Provider] chains into stdhttp (R4, design §13).
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// PolicySnapshot is validated auth policy and access posture, frozen at construction for event emission
// and adapter behavior (see runtimebundle wiring; tests may use literals).
type PolicySnapshot struct {
	AccessMode    auth.AccessMode
	HandlerKind   auth.HandlerKind
	RequiredLevel auth.RequiredLevel
}

// PolicyProvider is an [httpauth.Provider] that calls [coreauth.Authenticator] with HTTP-derived
// [auth.InboundCallMeta], dispatches [auth.AuthDecisionEvent], and maps allow/deny/challenge to
// [httpauth.AuthenticationResult] using an [httpauth.AuthErrorRenderer].
type PolicyProvider struct {
	Auth     coreauth.Authenticator
	Events   *coreauth.EventDispatcher
	Policy   PolicySnapshot
	Renderer httpauth.AuthErrorRenderer
	// RendererByFrontend, when non-nil, overrides [Renderer] for a matching frontend id from
	// [DefaultFrontendIDFromRequest] or [PolicyProvider.FrontendID]. Nil map entries are ignored.
	RendererByFrontend map[string]httpauth.AuthErrorRenderer
	// FrontendID, if set, supplies the frontend id for events and per-frontend renderers. When nil,
	// [DefaultFrontendIDFromRequest] is used.
	FrontendID func(*http.Request) string
}

// NewPolicyProvider wires a policy-bound authenticator, event pipeline, and error renderer.
// If renderer is nil, [DefaultAuthErrorRenderer] is used.
func NewPolicyProvider(authenticator coreauth.Authenticator, events *coreauth.EventDispatcher, pol PolicySnapshot, renderer httpauth.AuthErrorRenderer) *PolicyProvider {
	if renderer == nil {
		renderer = DefaultAuthErrorRenderer{}
	}
	return &PolicyProvider{Auth: authenticator, Events: events, Policy: pol, Renderer: renderer}
}

// DefaultFrontendIDFromRequest returns a best-effort frontend label from the URL path (prefix match).
// When no prefix matches, it returns the empty string (default auth error rendering applies).
func DefaultFrontendIDFromRequest(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/v1beta/") || strings.HasPrefix(p, "/v1beta1/"):
		return "gemini"
	// Anthropic Messages API — must precede generic /v1/ (see pluginreg/frontends_install.go).
	case strings.HasPrefix(p, "/v1/messages"):
		return "anthropic"
	case strings.HasPrefix(p, "/anthropic/"):
		return "anthropic"
	case strings.HasPrefix(p, "/v1/"):
		return "openai_compatible"
	case strings.HasPrefix(p, "/admin") || strings.HasPrefix(p, "/debug"):
		return "stdhttp"
	default:
		return ""
	}
}

// Authenticate implements [httpauth.Provider].
func (p *PolicyProvider) Authenticate(ctx context.Context, w http.ResponseWriter, r *http.Request) (httpauth.AuthenticationResult, error) {
	_ = w
	if p == nil || p.Auth == nil {
		return httpauth.AuthenticationResult{}, fmt.Errorf("stdhttp/auth: nil policy provider or authenticator")
	}
	frontendID := p.frontendID(r)
	meta := inboundMetaFromRequest(r, frontendID)
	now := time.Now().UTC()

	d, err := p.Auth.Authenticate(ctx, meta)
	if err != nil {
		return httpauth.AuthenticationResult{}, fmt.Errorf("stdhttp/auth: policy authenticator: %w", err)
	}

	traceID := diag.TraceID(ctx)
	if traceID == "" {
		traceID = meta.TraceID
	}

	// Normalize an accepted decision into one authoritative safe scope plus the derived
	// principal projection before any evidence is emitted or proxy execution begins
	// (requirements 1.1, 1.5, 2.1, 4.1). Denied/challenged decisions never produce a
	// successful lifecycle scope (requirement 1.6).
	bridged := bridgeScope(d)
	if bridged.err != nil {
		// Credential-like scope material is rejected before execution and evidence.
		d.Outcome = auth.OutcomeDeny
		if d.ReasonCode == "" {
			d.ReasonCode = "unsafe_scope"
		}
	}

	ev := authDecisionEvent(now, traceID, p.Policy, meta, d, bridged.evidence)
	if p.Events != nil {
		if e2 := p.Events.DispatchAuthDecision(ctx, ev); e2 != nil {
			synth := d
			synth.Outcome = auth.OutcomeDeny
			synth.ReasonCode = "event_delivery_failed"
			ev2 := authDecisionEvent(now, traceID, p.Policy, meta, synth, nil)
			rend := p.callRenderer(ctx, frontendID, &meta, synth, ev2, http.StatusServiceUnavailable)
			return resultFromRender(rend, auth.OutcomeDeny), nil
		}
	}

	switch d.Outcome {
	case auth.OutcomeAllow:
		if bridged.lifecycle != nil {
			s := bridged.lifecycle.Scope
			return httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: bridged.lifecycle.Principal, Scope: &s}, nil
		}
		// Allow without a trusted scope or identity (legacy pass-through): attach the
		// legacy principal only; the runtime derives synthetic scope under local mode.
		return httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: d.Principal}, nil
	case auth.OutcomeChallenge, auth.OutcomeDeny:
		st := defaultTerminalHTTPStatus(&d)
		rend := p.callRenderer(ctx, frontendID, &meta, d, ev, st)
		return resultFromRender(rend, d.Outcome), nil
	default:
		d2 := d
		d2.Outcome = auth.OutcomeDeny
		if d2.ReasonCode == "" {
			d2.ReasonCode = "unusable_outcome"
		}
		ev2 := authDecisionEvent(now, traceID, p.Policy, meta, d2, nil)
		rend := p.callRenderer(ctx, frontendID, &meta, d2, ev2, http.StatusUnauthorized)
		return resultFromRender(rend, auth.OutcomeDeny), nil
	}
}

type scopeBridgeResult struct {
	lifecycle *coreauth.ScopeBuildResult
	evidence  *scope.PrincipalScopeView
	err       error
}

// bridgeScope normalizes an accepted auth decision into one authoritative safe scope and the
// derived legacy principal projection. It returns the built scope/principal for accepted
// decisions, an evidence-safe scope pointer (set for both accepted and rejected decisions when
// identity attribution is available), and a non-nil error only when a trusted scope value looks
// like credential material and must be rejected before execution (requirement 2.6, 5.4).
func bridgeScope(d auth.Decision) scopeBridgeResult {
	if d.Outcome == auth.OutcomeAllow {
		res, bErr := coreauth.BuildScope(coreauth.ScopeBuildInput{Decision: d})
		switch {
		case bErr == nil:
			s := res.Scope
			return scopeBridgeResult{lifecycle: &res, evidence: &s}
		case errors.Is(bErr, coreauth.ErrNoIdentity):
			// Legacy allow with no trusted identity; runtime derives scope when permitted.
			return scopeBridgeResult{}
		default:
			// Unsafe scope material or any other normalization failure rejects before execution.
			return scopeBridgeResult{err: bErr}
		}
	}
	// Denied/challenged decisions: emit safe attribution from a trusted scope when the
	// authenticator supplied one, without creating a lifecycle scope (requirement 6.1, 1.6).
	// The scope is run through the Phase 2 safety filter so credential-like material in a
	// rejected decision's scope is omitted from evidence rather than emitted (requirement 2.6, 5.4).
	if d.Scope != nil {
		s := d.Scope.Clone()
		if err := coreauth.SanitizeScope(s); err != nil {
			return scopeBridgeResult{}
		}
		return scopeBridgeResult{evidence: &s}
	}
	return scopeBridgeResult{}
}

func (p *PolicyProvider) frontendID(r *http.Request) string {
	if p.FrontendID != nil {
		return p.FrontendID(r)
	}
	return DefaultFrontendIDFromRequest(r)
}

func (p *PolicyProvider) callRenderer(
	ctx context.Context,
	frontendID string,
	meta *auth.InboundCallMeta,
	d auth.Decision,
	ev auth.AuthDecisionEvent,
	defaultStatus int,
) httpauth.AuthErrorRenderResult {
	renderer := p.rendererForRequest(frontendID)
	// ChallengeHeaders is nil: [auth.Decision] carries challenge metadata in Decision.Challenge only;
	// extra wire headers would require extending the decision or render input when deciders need them.
	return renderer.RenderAuthError(ctx, httpauth.AuthErrorRenderInput{
		FrontendID:       ev.Frontend,
		RequestPath:      meta.Path,
		Decision:         d,
		DefaultStatus:    defaultStatus,
		ChallengeHeaders: nil,
		AccessMode:       p.Policy.AccessMode,
		HandlerKind:      p.Policy.HandlerKind,
		RequiredLevel:    p.Policy.RequiredLevel,
		TraceID:          ev.TraceID,
		RemoteAddr:       meta.ClientAddr,
	})
}

func (p *PolicyProvider) rendererForRequest(frontendID string) httpauth.AuthErrorRenderer {
	if p.RendererByFrontend != nil {
		if rdr := p.RendererByFrontend[frontendID]; rdr != nil {
			return rdr
		}
	}
	if p.Renderer != nil {
		return p.Renderer
	}
	return DefaultAuthErrorRenderer{}
}

func resultFromRender(rend httpauth.AuthErrorRenderResult, outcome auth.DecisionOutcome) httpauth.AuthenticationResult {
	typ := httpauth.TypeReject
	if outcome == auth.OutcomeChallenge {
		typ = httpauth.TypeChallenge
	}
	st := rend.Status
	if st == 0 {
		st = http.StatusUnauthorized
	}
	return httpauth.AuthenticationResult{
		Type:        typ,
		HTTPStatus:  st,
		Headers:     cloneHeader(rend.Headers),
		Body:        slices.Clone(rend.Body),
		ContentType: rend.ContentType,
	}
}

func defaultTerminalHTTPStatus(d *auth.Decision) int {
	if d == nil {
		return http.StatusUnauthorized
	}
	rc := strings.TrimSpace(strings.ToLower(d.ReasonCode))
	switch rc {
	case "remote_unavailable", "api_key_sso_misconfigured", "remote_misconfigured",
		"local_noop_misconfigured", "local_api_key_misconfigured", "event_delivery_failed":
		if rc == "remote_unavailable" {
			return http.StatusServiceUnavailable
		}
		// many misconfig codes are 503; policy validation should catch before traffic
		return http.StatusServiceUnavailable
	case "forbidden", "insufficient", "remote_denied":
		return http.StatusForbidden
	}
	if d.Outcome == auth.OutcomeChallenge {
		return http.StatusUnauthorized
	}
	if d.Outcome == auth.OutcomeDeny {
		return http.StatusUnauthorized
	}
	return http.StatusUnauthorized
}

func inboundMetaFromRequest(r *http.Request, frontendID string) auth.InboundCallMeta {
	if r == nil {
		return auth.InboundCallMeta{Frontend: frontendID}
	}
	var path string
	if r.URL != nil {
		path = r.URL.Path
	}
	bearer := authorizationBearerFromHeader(r.Header.Get("Authorization"))
	return auth.InboundCallMeta{
		TraceID:             diag.TraceID(r.Context()),
		Frontend:            frontendID,
		Method:              r.Method,
		Path:                path,
		ClientAddr:          r.RemoteAddr,
		AuthorizationBearer: bearer,
		SessionHint:         strings.TrimSpace(r.Header.Get("X-LIP-Session-Hint")),
	}
}

// authorizationBearerFromHeader returns the token only when the header uses the Bearer scheme
// (case-insensitive). Other schemes (Basic, Digest, etc.) and inputs without a space yield "".
func authorizationBearerFromHeader(raw string) string {
	scheme, cred := splitAuthorizationScheme(raw)
	if scheme != "bearer" {
		return ""
	}
	return cred
}

// splitAuthorizationScheme returns the first token (lower case) and the remainder after the first
// ASCII space. A value with no space returns ("", trimmedValue) for fail-closed parsing.
func splitAuthorizationScheme(v string) (scheme, token string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	sp := strings.IndexByte(v, ' ')
	if sp <= 0 {
		return "", v
	}
	return strings.ToLower(v[:sp]), strings.TrimSpace(v[sp+1:])
}

func authDecisionEvent(
	now time.Time,
	traceID string,
	pol PolicySnapshot,
	meta auth.InboundCallMeta,
	d auth.Decision,
	evidenceScope *scope.PrincipalScopeView,
) auth.AuthDecisionEvent {
	// Prefer the authoritative scope projection for compatibility fields so legacy event
	// consumers see the same identity as the request lifecycle (requirements 1.5, 4.6, 7.3);
	// fall back to the legacy principal when no scope is available.
	src := d.Principal
	if evidenceScope != nil {
		src = evidenceScope.Principal()
	}
	roles := slices.Clone(src.Roles)
	// PrincipalSafeClaims must not carry claim values on the audit path: only key names are
	// emitted so misconfigured or hostile deciders cannot seed OAuth/access tokens into events.
	var claims map[string]string
	if len(src.Claims) > 0 {
		claims = make(map[string]string, len(src.Claims))
		for k := range src.Claims {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			claims[k] = ""
		}
		if len(claims) == 0 {
			claims = nil
		}
	}
	ev := auth.AuthDecisionEvent{
		Time:                 now,
		TraceID:              traceID,
		AccessMode:           pol.AccessMode,
		RequiredLevel:        pol.RequiredLevel,
		HandlerKind:          pol.HandlerKind,
		Frontend:             meta.Frontend,
		Outcome:              d.Outcome,
		ReasonCode:           d.ReasonCode,
		PrincipalID:          strings.TrimSpace(src.ID),
		PrincipalDisplayName: strings.TrimSpace(src.DisplayName),
		PrincipalRoles:       roles,
		PrincipalSafeClaims:  claims,
		DeviceID:             strings.TrimSpace(d.Device.ID),
		DeviceKeyID:          strings.TrimSpace(d.Device.KeyID),
		DeviceFingerprint:    strings.TrimSpace(d.Device.Fingerprint),
		ChallengeKind:        d.Challenge.Kind,
		ChallengeSummary:     d.Challenge.Summary,
	}
	if evidenceScope != nil {
		s := evidenceScope.Clone()
		ev.Scope = &s
	}
	return ev
}

var _ httpauth.Provider = (*PolicyProvider)(nil)
