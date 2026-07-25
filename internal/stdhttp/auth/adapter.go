// Package auth integrates transport-layer [httpauth.Provider] chains into stdhttp.
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

type PolicySnapshot struct {
	AccessMode    auth.AccessMode
	HandlerKind   auth.HandlerKind
	RequiredLevel auth.RequiredLevel
}

type PolicyProvider struct {
	Auth               coreauth.Authenticator
	Events             *coreauth.EventDispatcher
	Policy             PolicySnapshot
	Renderer           httpauth.AuthErrorRenderer
	RendererByFrontend map[string]httpauth.AuthErrorRenderer
	FrontendID         func(*http.Request) string
}

type authSuccessContextAttacher interface {
	attachAuthSuccessContext(ctx context.Context, r *http.Request, res httpauth.AuthenticationResult) context.Context
}

func NewPolicyProvider(authenticator coreauth.Authenticator, events *coreauth.EventDispatcher, pol PolicySnapshot, renderer httpauth.AuthErrorRenderer) *PolicyProvider {
	if renderer == nil {
		renderer = DefaultAuthErrorRenderer{}
	}
	return &PolicyProvider{Auth: authenticator, Events: events, Policy: pol, Renderer: renderer}
}

func DefaultFrontendIDFromRequest(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/v1beta/") || strings.HasPrefix(p, "/v1beta1/"):
		return "gemini"
	case strings.HasPrefix(p, "/v1/messages"), strings.HasPrefix(p, "/anthropic/"):
		return "anthropic"
	case strings.HasPrefix(p, "/v1/"):
		return "openai_compatible"
	case strings.HasPrefix(p, "/admin"), strings.HasPrefix(p, "/debug"):
		return "stdhttp"
	default:
		return ""
	}
}

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

	bridged := bridgeScope(d)
	if bridged.err != nil {
		d.Outcome = auth.OutcomeDeny
		d.ReasonCode = "unsafe_scope"
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
		attr := ingressAttributionFromAllow(r, frontendID, d)
		if bridged.lifecycle != nil {
			s := bridged.lifecycle.Scope
			return httpauth.AuthenticationResult{
				Type: httpauth.TypePrincipal, Principal: bridged.lifecycle.Principal, Scope: &s, IngressAttribution: attr,
			}, nil
		}
		return httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: d.Principal, IngressAttribution: attr}, nil
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

func bridgeScope(d auth.Decision) scopeBridgeResult {
	if d.Outcome == auth.OutcomeAllow {
		res, bErr := coreauth.BuildScope(coreauth.ScopeBuildInput{Decision: d})
		switch {
		case bErr == nil:
			s := res.Scope
			return scopeBridgeResult{lifecycle: &res, evidence: &s}
		case errors.Is(bErr, coreauth.ErrNoIdentity):
			return scopeBridgeResult{}
		default:
			return scopeBridgeResult{err: bErr}
		}
	}
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

func ingressAttributionFromAllow(r *http.Request, frontendID string, d auth.Decision) httpauth.IngressAttribution {
	var remote string
	if r != nil {
		remote = r.RemoteAddr
	}
	return httpauth.IngressAttribution{
		PeerIP: peerIPFromRemoteAddr(remote), FrontendID: frontendID,
		DeviceID: strings.TrimSpace(d.Device.ID), KeyID: strings.TrimSpace(d.Device.KeyID),
		Fingerprint: strings.TrimSpace(d.Device.Fingerprint),
	}
}

func (p *PolicyProvider) attachAuthSuccessContext(ctx context.Context, r *http.Request, res httpauth.AuthenticationResult) context.Context {
	if p == nil || r == nil || res.Type != httpauth.TypePrincipal {
		return ctx
	}
	m := newExactCredentialMatcher(authorizationBearerFromHeader(r.Header.Get("Authorization")), res.IngressAttribution.KeyID)
	if m == nil {
		return ctx
	}
	return httpauth.WithCredentialMatcher(ctx, m)
}

func (p *PolicyProvider) callRenderer(ctx context.Context, frontendID string, meta *auth.InboundCallMeta, d auth.Decision, ev auth.AuthDecisionEvent, defaultStatus int) httpauth.AuthErrorRenderResult {
	return p.rendererForRequest(frontendID).RenderAuthError(ctx, httpauth.AuthErrorRenderInput{
		FrontendID: ev.Frontend, RequestPath: meta.Path, Decision: d, DefaultStatus: defaultStatus,
		AccessMode: p.Policy.AccessMode, HandlerKind: p.Policy.HandlerKind, RequiredLevel: p.Policy.RequiredLevel,
		TraceID: ev.TraceID, RemoteAddr: meta.ClientAddr,
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
		Type: typ, HTTPStatus: st, Headers: cloneHeader(rend.Headers),
		Body: slices.Clone(rend.Body), ContentType: rend.ContentType,
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
		return http.StatusServiceUnavailable
	case "forbidden", "insufficient", "remote_denied":
		return http.StatusForbidden
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
	return auth.InboundCallMeta{
		TraceID: diag.TraceID(r.Context()), Frontend: frontendID, Method: r.Method, Path: path,
		ClientAddr: r.RemoteAddr, AuthorizationBearer: authorizationBearerFromHeader(r.Header.Get("Authorization")),
		SessionHint: strings.TrimSpace(r.Header.Get("X-LIP-Session-Hint")),
	}
}

func authorizationBearerFromHeader(raw string) string {
	scheme, cred := splitAuthorizationScheme(raw)
	if scheme != "bearer" {
		return ""
	}
	return cred
}

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

func authDecisionEvent(now time.Time, traceID string, pol PolicySnapshot, meta auth.InboundCallMeta, d auth.Decision, evidenceScope *scope.PrincipalScopeView) auth.AuthDecisionEvent {
	src := d.Principal
	if evidenceScope != nil {
		src = evidenceScope.Principal()
	}
	roles := slices.Clone(src.Roles)
	var claims map[string]string
	if len(src.Claims) > 0 {
		claims = make(map[string]string, len(src.Claims))
		for k := range src.Claims {
			if k = strings.TrimSpace(k); k != "" {
				claims[k] = ""
			}
		}
		if len(claims) == 0 {
			claims = nil
		}
	}
	ev := auth.AuthDecisionEvent{
		Time: now, TraceID: traceID, AccessMode: pol.AccessMode, RequiredLevel: pol.RequiredLevel,
		HandlerKind: pol.HandlerKind, Frontend: meta.Frontend, Outcome: d.Outcome, ReasonCode: d.ReasonCode,
		PrincipalID: strings.TrimSpace(src.ID), PrincipalDisplayName: strings.TrimSpace(src.DisplayName),
		PrincipalRoles: roles, PrincipalSafeClaims: claims,
		DeviceID: strings.TrimSpace(d.Device.ID), DeviceKeyID: strings.TrimSpace(d.Device.KeyID),
		DeviceFingerprint: strings.TrimSpace(d.Device.Fingerprint),
		ChallengeKind:     d.Challenge.Kind, ChallengeSummary: d.Challenge.Summary,
	}
	if evidenceScope != nil {
		s := evidenceScope.Clone()
		ev.Scope = &s
	}
	return ev
}

var _ httpauth.Provider = (*PolicyProvider)(nil)
