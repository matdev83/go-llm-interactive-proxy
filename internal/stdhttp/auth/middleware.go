// Package auth integrates transport-layer [httpauth.Provider] chains into stdhttp (R4, design §13).
package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// annotateResponseHeaderNames is the allow-list for [httpauth.TypeAnnotate] ResponseHeaders
// merged onto the success-path response (defense in depth: providers must not set cookies,
// auth challenges, or other high-impact headers through annotate).
var annotateResponseHeaderNames = map[string]struct{}{
	"Cache-Control":                       {},
	"Content-Security-Policy":             {},
	"Content-Security-Policy-Report-Only": {},
	"Cross-Origin-Embedder-Policy":        {},
	"Cross-Origin-Opener-Policy":          {},
	"Cross-Origin-Resource-Policy":        {},
	"Expires":                             {},
	"Permissions-Policy":                  {},
	"Pragma":                              {},
	"Referrer-Policy":                     {},
	"Strict-Transport-Security":           {},
	"Vary":                                {},
	"X-Content-Type-Options":              {},
}

// terminalResponseHeaderNames allow-lists headers merged onto TypeReject/TypeChallenge
// responses (defense in depth: providers must not set cookies, redirects, or other
// high-impact headers on the termination path).
var terminalResponseHeaderNames map[string]struct{}

func init() {
	terminalResponseHeaderNames = make(map[string]struct{}, len(annotateResponseHeaderNames)+3)
	for k := range annotateResponseHeaderNames {
		terminalResponseHeaderNames[k] = struct{}{}
	}
	terminalResponseHeaderNames["Www-Authenticate"] = struct{}{}
	terminalResponseHeaderNames["Retry-After"] = struct{}{}
}

// Middleware returns an HTTP handler that runs providers in order before delegating to next.
// Provider errors are fail-closed (HTTP 500). An empty provider list is a no-op passthrough at
// this layer only; product wiring must supply providers so anonymous pass-through never replaces
// explicit configured authentication (auth-architecture requirements 1.7 / 5.6).
// A non-empty list where every entry is nil is treated as misconfiguration and fails closed (HTTP 500)
// so anonymous pass-through cannot result from an accidental nil-only override slice.
// When log is non-nil, provider failures and unknown result types emit a single structured log line
// (trace-correlated via request context); reject/challenge responses are not logged here (HTTP outcome only).
func Middleware(log *slog.Logger, providers []httpauth.Provider, next http.Handler) http.Handler {
	nonNil := compactNonNilHTTPAuthProviders(providers)
	if len(nonNil) == 0 {
		if len(providers) > 0 {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if log != nil {
					ctx := context.Background()
					if r != nil {
						ctx = r.Context()
					}
					log.ErrorContext(ctx, "stdhttp: auth middleware has non-empty provider list but every entry is nil",
						slog.String("component", "stdhttp.auth"),
						slog.String("reason", "all_httpauth_providers_nil"),
					)
				}
				http.Error(w, "authentication misconfigured", http.StatusInternalServerError)
			})
		}
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			if log != nil {
				log.WarnContext(context.Background(), "stdhttp: nil request in auth middleware",
					slog.String("component", "stdhttp.auth"),
				)
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		for _, p := range nonNil {
			res, err := p.Authenticate(ctx, w, r)
			if err != nil {
				if log != nil {
					// Provider errors are logged: [httpauth.Provider] implementations must not wrap
					// secrets or raw Authorization material in returned errors.
					log.ErrorContext(ctx, "stdhttp: auth provider authenticate failed", "error", err)
				}
				http.Error(w, "authentication failed", http.StatusInternalServerError)
				return
			}
			switch res.Type {
			case httpauth.TypeContinue:
				continue
			case httpauth.TypePrincipal:
				ctx = httpauth.WithPrincipal(ctx, res.Principal)
			case httpauth.TypeAnnotate:
				mergeAnnotateResponseHeaders(ctx, log, w.Header(), res.ResponseHeaders)
			case httpauth.TypeReject, httpauth.TypeChallenge:
				writeTermination(ctx, log, w, res)
				return
			default:
				if log != nil {
					log.WarnContext(ctx, "stdhttp: auth provider returned unknown result type", "type", res.Type)
				}
				http.Error(w, "authentication failed", http.StatusInternalServerError)
				return
			}
		}
		// Align with [PolicyProvider.frontendID] when [PolicyProvider.FrontendID] is nil (path-derived wire id).
		ctx = execview.WithFrontendID(ctx, DefaultFrontendIDFromRequest(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func compactNonNilHTTPAuthProviders(providers []httpauth.Provider) []httpauth.Provider {
	out := make([]httpauth.Provider, 0, len(providers))
	for _, p := range providers {
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

func mergeAnnotateResponseHeaders(ctx context.Context, log *slog.Logger, dst, src http.Header) {
	if len(src) == 0 {
		return
	}
	for rawKey, vs := range src {
		canon := http.CanonicalHeaderKey(strings.TrimSpace(rawKey))
		if canon == "" {
			continue
		}
		if _, ok := annotateResponseHeaderNames[canon]; !ok {
			if log != nil {
				log.WarnContext(ctx, "stdhttp: auth annotate dropped disallowed response header", "header", canon)
			}
			continue
		}
		for _, v := range vs {
			dst.Add(canon, v)
		}
	}
}

func mergeTerminalResponseHeaders(ctx context.Context, log *slog.Logger, dst, src http.Header) {
	if len(src) == 0 {
		return
	}
	for rawKey, vs := range src {
		canon := http.CanonicalHeaderKey(strings.TrimSpace(rawKey))
		if canon == "" {
			continue
		}
		if _, ok := terminalResponseHeaderNames[canon]; !ok {
			if log != nil {
				log.WarnContext(ctx, "stdhttp: auth termination dropped disallowed response header", "header", canon)
			}
			continue
		}
		for _, v := range vs {
			dst.Add(canon, v)
		}
	}
}

func writeTermination(ctx context.Context, log *slog.Logger, w http.ResponseWriter, res httpauth.AuthenticationResult) {
	h := w.Header()
	mergeTerminalResponseHeaders(ctx, log, h, res.Headers)
	switch ct := strings.TrimSpace(res.ContentType); {
	case ct != "":
		h.Set("Content-Type", ct)
	case h.Get("Content-Type") == "" && len(res.Body) > 0:
		h.Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(res.EffectiveStatus())
	if len(res.Body) > 0 {
		if _, err := w.Write(res.Body); err != nil && log != nil {
			log.WarnContext(ctx, "stdhttp: auth termination response write failed",
				slog.String("component", "stdhttp.auth"),
				"error", err,
			)
		}
	}
}

// EnsureContextPrincipal copies a transport principal from parent into child if child has none.
// Used when a sub-context loses values (tests or isolated decode paths).
// A nil child is reserved for tests and isolated decode helpers; production request paths must pass
// a non-nil request-derived child so cancellation and context values behave normally.
// If child is nil, it returns a non-nil context: when parent is non-nil, context.WithoutCancel(parent)
// with the parent principal attached (preserves request-scoped values such as trace IDs without
// inheriting parent cancellation); otherwise context.Background.
func EnsureContextPrincipal(parent, child context.Context) context.Context {
	if child == nil {
		if p, ok := httpauth.PrincipalFromContext(parent); ok {
			if parent != nil {
				return httpauth.WithPrincipal(context.WithoutCancel(parent), p)
			}
			return httpauth.WithPrincipal(context.Background(), p)
		}
		if parent != nil {
			return context.WithoutCancel(parent)
		}
		return context.Background()
	}
	if _, ok := httpauth.PrincipalFromContext(child); ok {
		return child
	}
	if p, ok := httpauth.PrincipalFromContext(parent); ok {
		return httpauth.WithPrincipal(child, p)
	}
	return child
}
