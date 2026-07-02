package httpauth

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// AuthenticationType classifies the outcome of one provider invocation.
type AuthenticationType uint8

const (
	// TypeContinue means this provider did not terminate the chain; processing continues.
	TypeContinue AuthenticationType = iota
	// TypePrincipal attaches identity and continues the provider chain.
	TypePrincipal
	// TypeReject ends the request with HTTPStatus and optional Body.
	TypeReject
	// TypeChallenge ends the request with HTTPStatus and response Headers (e.g. WWW-Authenticate).
	TypeChallenge
	// TypeAnnotate merges ResponseHeaders onto the outbound response and continues the chain.
	TypeAnnotate
)

// AuthenticationResult is the typed outcome from [Provider.Authenticate].
type AuthenticationResult struct {
	Type AuthenticationType

	// Principal is used when Type is TypePrincipal.
	Principal execview.PrincipalView

	// Scope is an optional authoritative safe principal/scope snapshot carried from trusted
	// auth provider code into the middleware. It is nil for legacy principal-only results.
	// Raw secrets and transport headers must never be placed here (requirements 2.1, 2.6).
	Scope *scope.PrincipalScopeView

	// HTTPStatus is used for TypeReject and TypeChallenge (default 401 if zero).
	HTTPStatus int

	// Headers is copied for TypeChallenge (and optional metadata for TypeReject).
	Headers http.Header

	// Body is optional for TypeReject and TypeChallenge.
	Body []byte

	// ContentType is optional for TypeReject and TypeChallenge. When non-empty, stdhttp
	// sets the Content-Type response header. When empty with a non-empty Body, the default
	// is "text/plain; charset=utf-8" (pre-1.x compatible).
	ContentType string

	// ResponseHeaders are merged on the success path when Type is TypeAnnotate.
	// The stdhttp integration allow-lists safe response header names (cache, security
	// meta, Vary); disallowed names (e.g. Set-Cookie) are dropped with a warning log.
	ResponseHeaders http.Header
}

// EffectiveStatus returns a non-zero HTTP status for reject/challenge results.
func (r AuthenticationResult) EffectiveStatus() int {
	if r.HTTPStatus != 0 {
		return r.HTTPStatus
	}
	switch r.Type {
	case TypeReject, TypeChallenge:
		return http.StatusUnauthorized
	default:
		return 0
	}
}
