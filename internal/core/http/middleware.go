package http

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

// TraceMiddleware injects a trace ID into the request context when the
// X-Trace-ID header is present, enabling downstream handlers to propagate
// diagnostics context.
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-ID")
		if traceID != "" {
			ctx := diag.WithTraceID(r.Context(), traceID)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// RequestIDMiddleware generates and injects a new trace ID when one is not
// already present in the request context.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if diag.TraceID(r.Context()) == "" {
			ctx := diag.WithTraceID(r.Context(), diag.NewTraceID())
			r = r.WithContext(ctx)
			w.Header().Set("X-Trace-ID", diag.TraceID(ctx))
		}
		next.ServeHTTP(w, r)
	})
}
