package admin

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

// DiagnosticsHandler serves the admin diagnostics surface combining health
// and attempts endpoints under a common mux.
func DiagnosticsHandler(attemptLoader diag.AttemptLoader) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", diag.HealthHandler())
	if attemptLoader != nil {
		mux.Handle("/attempts", diag.AttemptsHandler(attemptLoader))
	}
	return mux
}
