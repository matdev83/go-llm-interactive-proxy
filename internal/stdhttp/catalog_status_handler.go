package stdhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
)

// NewCatalogStatusHandler serves GET JSON from [modelcatalog.BuildCatalogDiagnosticsJSON] for the request time.
// log is the process HTTP logger (same as [Run]); if nil, [slog.Default] is used.
func NewCatalogStatusHandler(log *slog.Logger, cfg modelcatalog.CatalogStatusHandlerConfig) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			if r.Method == http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		v := modelcatalog.BuildCatalogDiagnosticsJSON(cfg)
		if err := enc.Encode(v); err != nil {
			log.ErrorContext(r.Context(), "modelcatalog: catalog status encode", "err", err)
		}
	})
}
