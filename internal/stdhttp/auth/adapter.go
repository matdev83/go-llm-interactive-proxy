// Package auth integrates transport-layer [httpauth.Provider] chains into stdhttp (R4, design §13).
package auth

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
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
	ev := authDecisionEvent(now, traceID, p.Policy, meta, d)
	if p.Events != nil {
		if e2 := p.Events.DispatchAuthDecision(ctx, ev); e2 != nil {
			synth := d
			synth.Outcome = auth.OutcomeDeny
			synth.ReasonCode = "event_delivery_failed"
			ev2 := authDecisionEvent(now, traceID, p.Policy, meta, synth)
			rend := p.callRenderer(ctx, frontendID, &meta, synth, ev2, http.StatusServiceUnavailable)
			return resultFromRender(rend, auth.OutcomeDeny), nil
		}
	}

	switch d.Outcome {
	case auth.OutcomeAllow:
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
		ev2 := authDecisionEvent(now, traceID, p.Policy, meta, d2)
		rend := p.callRenderer(ctx, frontendID, &meta, d2, ev2, http.StatusUnauthorized)
		return resultFromRender(rend, auth.OutcomeDeny), nil
	}
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
) auth.AuthDecisionEvent {
	roles := slices.Clone(d.Principal.Roles)
	// PrincipalSafeClaims must not carry claim values on the audit path: only key names are
	// emitted so misconfigured or hostile deciders cannot seed OAuth/access tokens into events.
	var claims map[string]string
	if len(d.Principal.Claims) > 0 {
		claims = make(map[string]string, len(d.Principal.Claims))
		for k := range d.Principal.Claims {
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
	return auth.AuthDecisionEvent{
		Time:                 now,
		TraceID:              traceID,
		AccessMode:           pol.AccessMode,
		RequiredLevel:        pol.RequiredLevel,
		HandlerKind:          pol.HandlerKind,
		Frontend:             meta.Frontend,
		Outcome:              d.Outcome,
		ReasonCode:           d.ReasonCode,
		PrincipalID:          strings.TrimSpace(d.Principal.ID),
		PrincipalDisplayName: strings.TrimSpace(d.Principal.DisplayName),
		PrincipalRoles:       roles,
		PrincipalSafeClaims:  claims,
		DeviceID:             strings.TrimSpace(d.Device.ID),
		DeviceKeyID:          strings.TrimSpace(d.Device.KeyID),
		DeviceFingerprint:    strings.TrimSpace(d.Device.Fingerprint),
		ChallengeKind:        d.Challenge.Kind,
		ChallengeSummary:     d.Challenge.Summary,
	}
}

var _ httpauth.Provider = (*PolicyProvider)(nil)
