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
		h, err := diag.AttemptsHandler(attemptLoader)
		if err != nil {
			h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			})
		}
		mux.Handle("/attempts", h)
	}
	return mux
}
