package diag

import (
	"encoding/json"
	"net/http"
)

// HealthResponse is the JSON body for the basic liveness probe.
type HealthResponse struct {
	Status string `json:"status"`
}

// HealthHandler returns an http.Handler that serves GET with JSON {"status":"ok"}.
func HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
	})
}
