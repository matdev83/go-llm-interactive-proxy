// Package auth integrates transport-layer [httpauth.Provider] chains into stdhttp (R4, design §13).
package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

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

// Middleware returns an HTTP handler that runs providers in order before delegating to next.
// Provider errors are fail-closed (HTTP 500). An empty provider list is a no-op passthrough.
// When log is non-nil, provider failures and unknown result types emit a single structured log line
// (trace-correlated via request context); reject/challenge responses are not logged here (HTTP outcome only).
func Middleware(log *slog.Logger, providers []httpauth.Provider, next http.Handler) http.Handler {
	if len(providers) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			if log != nil {
				log.Warn("stdhttp: nil request in auth middleware")
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		for _, p := range providers {
			if p == nil {
				continue
			}
			res, err := p.Authenticate(ctx, w, r)
			if err != nil {
				if log != nil {
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
				writeTermination(w, res)
				return
			default:
				if log != nil {
					log.WarnContext(ctx, "stdhttp: auth provider returned unknown result type", "type", res.Type)
				}
				http.Error(w, "authentication failed", http.StatusInternalServerError)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func mergeHeaders(dst http.Header, src http.Header) {
	if len(src) == 0 {
		return
	}
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
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

func writeTermination(w http.ResponseWriter, res httpauth.AuthenticationResult) {
	h := w.Header()
	mergeHeaders(h, res.Headers)
	if h.Get("Content-Type") == "" && len(res.Body) > 0 {
		h.Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(res.EffectiveStatus())
	if len(res.Body) > 0 {
		_, _ = w.Write(res.Body)
	}
}

// EnsureContextPrincipal copies a transport principal from parent into child if child has none.
// Used when a sub-context loses values (tests or isolated decode paths).
// If child is nil, it returns a non-nil context ([context.Background] with the parent principal
// when present) so the result is safe for APIs that require a non-nil [context.Context]. Prefer
// passing a request-derived or cancelable child in production.
func EnsureContextPrincipal(parent, child context.Context) context.Context {
	if child == nil {
		if p, ok := httpauth.PrincipalFromContext(parent); ok {
			return httpauth.WithPrincipal(context.Background(), p)
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
